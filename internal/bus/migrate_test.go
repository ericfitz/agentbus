package bus

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

// schemaV1 is the messages DDL as shipped through v1.3.0, with the bytes
// column that schema version 2 drops. Kept here verbatim so the migration
// is tested against a real v1 file, not a simulated one.
const schemaV1Messages = `
CREATE TABLE messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  sender TEXT NOT NULL,
  context TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to INTEGER,
  metadata TEXT,
  refs TEXT,
  memory_id INTEGER,
  revision INTEGER,
  tombstone INTEGER NOT NULL DEFAULT 0,
  tombstone_at INTEGER,
  bytes INTEGER NOT NULL
);`

// schemaV5FTS is messages_fts and its two triggers as shipped through
// v1.7.0 (content only). Schema version 6 rebuilds them over subject and
// content, so a test shaping an older file replaces the current ones with
// these first.
const schemaV5FTS = `
DROP TRIGGER IF EXISTS messages_ai;
DROP TRIGGER IF EXISTS messages_ad;
DROP TABLE IF EXISTS messages_fts;
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='seq');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.seq, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.seq, old.content);
END;`

// TestMigrateV1DropsMessagesBytes (ADR 0006 item 4): opening a schema
// version 1 database rebuilds messages without bytes in one transaction,
// keeps every row and FTS entry, carries the AUTOINCREMENT counter over,
// and stamps user_version 2. Row 1's embedding is dropped by the chain
// because foldRefs changes its content.
func TestMigrateV1DropsMessagesBytes(t *testing.T) {
	// Row 1 carries refs (a path with a space, a URL with non-ASCII and a
	// query string) so the v11 fold is proven through the messages_v2 and
	// addSubject rebuilds (ADR 0019).
	const v1Refs = `[{"kind":"unix_path","value":"/Users/sam/release notes.md"},{"kind":"url","value":"https://example.com/café?q=ü&r=1"}]`
	const wantRow1 = "remember me\n\nRefs:\n- /Users/sam/release notes.md\n- https://example.com/café?q=ü&r=1"
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A v1 file: v1 messages table, then the rest of the shared DDL (which
	// skips messages because it already exists), stamped 1.
	for _, s := range []string{schemaV1Messages, schema, schemaV5FTS, "PRAGMA user_version = 1",
		"INSERT INTO channels(name,kind,created_seq) VALUES('memory','memory',0)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,bytes,refs) VALUES('memory','sam','r',1,'','remember me',1,1,42,'" + v1Refs + "')",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('general','sam','r',2,'','hello world',42)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('general','sam','r',3,'','doomed',42)",
		"INSERT INTO embeddings(seq,model,vector) VALUES(1,'m',x'00')", // dropped by the fold (row 1 has refs)
		"INSERT INTO embeddings(seq,model,vector) VALUES(2,'m',x'00')", // kept: row 2 has no refs
		"DELETE FROM messages WHERE seq=3", // counter (3) now exceeds max(seq) (2)
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v1 database: %v", err)
	}
	defer func() { _ = b.Close() }()

	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d", uv, schemaVersion)
	}
	var cols string
	if err := b.db.QueryRow("SELECT group_concat(name, ',') FROM pragma_table_info('messages')").Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cols, "bytes") {
		t.Fatalf("bytes column survived: %s", cols)
	}
	if !strings.Contains(cols, "subject") {
		t.Fatalf("subject column missing after the full chain: %s", cols)
	}
	if strings.Contains(cols, "refs") {
		t.Fatalf("refs column survived the full chain: %s", cols)
	}
	var content string
	if err := b.db.QueryRow("SELECT content FROM messages WHERE seq=1").Scan(&content); err != nil || content != wantRow1 {
		t.Fatalf("row 1 after rebuild: %q, %v; want %q", content, err, wantRow1)
	}
	if _, err := b.db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')"); err != nil {
		t.Fatalf("fts integrity-check after the full chain: %v", err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&n); err != nil || n != 1 {
		t.Fatalf("embeddings after rebuild: %d, %v (want 1: the fold drops row 1's, the rebuilds must not cascade to row 2's)", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'remember'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts after rebuild: %d, %v", n, err)
	}
	for _, obj := range []string{"messages_ai", "messages_ad", "messages_channel_seq", "messages_memory"} {
		if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", obj).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s not recreated after rebuild", obj)
		}
	}
	// seq stays monotonic: the next message must get 4, not 3.
	sam := reg(t, b, "sam")
	r, err := b.Send(sam, SendInput{Channel: "general", Content: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 4 {
		t.Fatalf("seq after migration = %d, want 4 (AUTOINCREMENT counter not carried over)", r.Seq)
	}
	if _, err := b.Search(sam, SearchInput{Query: "remember", Mode: "text"}); err != nil {
		t.Fatalf("search after rebuild: %v", err)
	}
}

// TestMigrateV2AddsMessageTags: a schema version 2 file opens, gains
// message_tags through the shared DDL, and is stamped 3.
func TestMigrateV2AddsMessageTags(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DROP TABLE message_tags"); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v2 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv, n int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv < 3 {
		t.Fatalf("user_version = %d, %v", uv, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("message_tags missing: %d %v", n, err)
	}
}

// TestMigrateV3AddsTagSubscriptions: a schema version 3 file opens, gains
// tag_subscriptions through the shared DDL, and is stamped 4.
func TestMigrateV3AddsTagSubscriptions(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("DROP TABLE tag_subscriptions"); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v3 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv, n int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv < 4 {
		t.Fatalf("user_version = %d, %v", uv, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='tag_subscriptions'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("tag_subscriptions missing: %d %v", n, err)
	}
}

// TestMigrateV2ToLatest (#13, ADR 0010): a database shaped like 1.6.0 --
// user_version 2, missing message_tags, tag_subscriptions, and
// tag_subscription_tags -- opens, walks every migration step up to
// schemaVersion, passes the FK check each step already runs, and tag
// subscribe/send/receive works afterward.
func TestMigrateV2ToLatest(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"DROP TABLE tag_subscription_tags", // FK'd to tag_subscriptions: drop first
		"DROP TABLE tag_subscriptions",
		"DROP TABLE message_tags",
		"PRAGMA user_version = 2",
	} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v2 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	for _, tbl := range []string{"message_tags", "tag_subscriptions", "tag_subscription_tags"} {
		var n int
		if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s missing after migration: %d %v", tbl, n, err)
		}
	}
	sam, kim := reg(t, b, "Sam"), reg(t, b, "Kim")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.SubscribeTags(kim, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "hi", Tags: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 || r.Messages[0].Content != "hi" {
		t.Fatalf("tag subscribe/send/receive after migration: %+v %v", r, err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Subject: "Zebra alert", Content: "after the chain"}); err != nil {
		t.Fatal(err)
	}
	if s, err := b.Search(sam, SearchInput{Query: "zebra", Mode: "text"}); err != nil || len(s.Hits) != 1 || s.Hits[0].Subject != "Zebra alert" {
		t.Fatalf("subject after the v2 chain: %+v %v", s, err)
	}
}

// TestMigrateV4SplitsTagSets (#13): a v4 database's tag_subscriptions row
// backfills into tag_subscription_tags (one row per tag), message_tags_tag
// is dropped, and message_tags_tag_seq replaces it.
func TestMigrateV4SplitsTagSets(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Shape a v4 database: no tag_subscription_tags, the old tag-only
	// index, and one AND set already subscribed.
	for _, s := range []string{
		"DROP TABLE tag_subscription_tags",
		"DROP INDEX IF EXISTS message_tags_tag_seq",
		"CREATE INDEX message_tags_tag ON message_tags(tag)",
		"INSERT INTO tag_subscriptions(sender, tags_key, created_seq) VALUES('Kim','a,b',7)",
		"PRAGMA user_version = 4",
	} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v4 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	rows, err := b.db.Query("SELECT sender, tags_key, tag FROM tag_subscription_tags ORDER BY tag")
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for rows.Next() {
		var sender, key, tag string
		if err := rows.Scan(&sender, &key, &tag); err != nil {
			t.Fatal(err)
		}
		got = append(got, [3]string{sender, key, tag})
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := [][3]string{{"Kim", "a,b", "a"}, {"Kim", "a,b", "b"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("tag_subscription_tags after migration: %v, want %v", got, want)
	}
	lrows, err := b.db.Query("SELECT tag, lo, hi FROM tag_subscription_tags")
	if err != nil {
		t.Fatal(err)
	}
	for lrows.Next() {
		var tag, lo, hi string
		if err := lrows.Scan(&tag, &lo, &hi); err != nil {
			t.Fatal(err)
		}
		if lo != tag || hi != tag {
			t.Fatalf("migrated row %q has lo=%q hi=%q, want both = tag", tag, lo, hi)
		}
	}
	if err := lrows.Close(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags_tag'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("message_tags_tag must be dropped: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags_tag_seq'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("message_tags_tag_seq missing: %d %v", n, err)
	}
}

// TestMigrateV5AddsSubject (ADR 0010): a v5 file gains messages.subject,
// task rows are backfilled from their JSON document (a non-JSON row on a
// task channel is skipped, not fatal), messages_fts is rebuilt over subject
// and content so it still finds old content and now finds subjects, and
// user_version is 6.
func TestMigrateV5AddsSubject(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Shape a v5 database: content-only FTS and triggers, no subject
	// column, one task row, one malformed task-channel row, one chat row.
	for _, s := range []string{
		schemaV5FTS,
		"ALTER TABLE messages DROP COLUMN subject",
		"INSERT INTO channels(name,kind,created_seq) VALUES('tasks/repo','memory',0)",
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('tasks/repo','sam','r',1,'','{"subject":"Ship v6","status":"pending","rank":"V"}',1,1)`,
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('tasks/repo','sam','r',2,'','not json',2,1)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content) VALUES('general','sam','r',3,'','hello world')",
		"PRAGMA user_version = 5",
	} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v5 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var subjects []string
	rows, err := b.db.Query("SELECT subject FROM messages ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, s)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(subjects, "|") != "Ship v6||" {
		t.Fatalf("backfilled subjects %q, want task row only", subjects)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'hello'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts lost old content after rebuild: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'subject:ship'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts must index the backfilled subject: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('messages_ai','messages_ad','messages_fts')").Scan(&n); err != nil || n != 3 {
		t.Fatalf("fts objects after migration: %d %v", n, err)
	}
	// The recreated insert trigger carries both columns: a new row with a
	// subject is found by a word that appears only there. Task 2 wires
	// SendInput.Subject; here the row is written directly.
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,subject,content) VALUES('general','sam','r',4,'','Zebra alert','nothing here')"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'zebra'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("insert trigger must index subject: %d %v", n, err)
	}
}

// TestMigrateV6AddsMemoryAccessAndDropsTaskSubscriptions (ADR 0013): a v6
// file gains memory_access, backfilled at the migration time for every live
// memory but not for task rows, and loses every subscription to a task list
// (bare "tasks" and "tasks/..."), for every sender, while other
// subscriptions survive.
func TestMigrateV6AddsMemoryAccessAndDropsTaskSubscriptions(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Shape a v6 database: no memory_access, two live memories (one on a
	// task list, which must not get a row), a tombstoned memory (must not
	// get a row either), and subscriptions to a task list and a chat
	// channel, from two different senders.
	for _, s := range []string{
		"DROP TABLE memory_access",
		"INSERT INTO channels(name,kind,created_seq) VALUES('tasks/repo','memory',0)",
		"INSERT INTO channels(name,kind,created_seq) VALUES('dev','ordinary',0)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('memory','sam','r',1,'','remember me',1,1)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,tombstone,tombstone_at) VALUES('memory','sam','r',2,'','gone',2,1,1,2)",
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('tasks/repo','sam','r',3,'','{"subject":"x","status":"pending","rank":"V"}',3,1)`,
		"INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('sam','tasks/repo',0,0)",
		"INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('sam','tasks',0,0)",
		"INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('kim','tasks/repo',0,0)",
		"INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('sam','dev',0,0)",
		"PRAGMA user_version = 6",
	} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v6 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	after := time.Now().UnixMilli()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	rows, err := b.db.Query("SELECT memory_id, accessed_at FROM memory_access ORDER BY memory_id")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, at int64 }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.at); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].id != 1 {
		t.Fatalf("memory_access after migration: %+v, want one row for memory 1 only (not the tombstone, not the task)", got)
	}
	if got[0].at < before || got[0].at > after {
		t.Fatalf("accessed_at = %d, want between %d and %d (migration time)", got[0].at, before, after)
	}
	var subs []string
	rows, err = b.db.Query("SELECT sender || ' ' || channel FROM subscriptions ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(subs, "|") != "sam dev" {
		t.Fatalf("subscriptions after migration: %v, want only sam's dev subscription (every task-list subscription dropped)", subs)
	}
}

// schemaV6Channels is the channels DDL as shipped through v1.9.0, without
// the created_at column schema version 7 adds (ADR 0013). Kept here
// verbatim so the migration is tested against a real pre-column table, not
// a simulated one (the shared schema string already carries the column, so
// every other migration test in this file, which bootstraps from it,
// starts with created_at already present and never exercises this step's
// ALTER TABLE path).
const schemaV6Channels = `
CREATE TABLE channels (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary','memory')),
  created_seq INTEGER NOT NULL,
  evicted_before_seq INTEGER NOT NULL DEFAULT 0
);`

// TestMigrateV6AddsChannelsCreatedAt (ADR 0013): a v6 file's channels table,
// which has no created_at column, gains one, backfilled to the migration
// time for every existing channel (there is no earlier per-channel
// timestamp to recover), so no task channel looks stale from the moment
// the upgrade lands.
func TestMigrateV6AddsChannelsCreatedAt(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{schema, "DROP TABLE channels", schemaV6Channels, "PRAGMA user_version = 6",
		"INSERT INTO channels(name,kind,created_seq) VALUES('tasks/repo','memory',0)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UnixMilli()
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v6 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	after := time.Now().UnixMilli()

	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var createdAt int64
	if err := b.db.QueryRow("SELECT created_at FROM channels WHERE name='tasks/repo'").Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	if createdAt < before || createdAt > after {
		t.Fatalf("created_at = %d, want between %d and %d (migration time)", createdAt, before, after)
	}
}

// schemaV7Sessions is the sessions DDL as shipped through v1.11.0, without
// the harness columns schema version 8 adds (ADR 0015), so the migration's
// ALTER TABLE path runs against a real pre-column table.
const schemaV7Sessions = `
CREATE TABLE sessions (
  sender TEXT PRIMARY KEY,
  context TEXT NOT NULL,
  owner TEXT NOT NULL,
  heartbeat INTEGER NOT NULL,
  registered_at INTEGER NOT NULL
);`

// TestMigrateV7AddsSessionHarness (ADR 0015): a v7 file's sessions table
// gains harness and harness_version, existing rows read as unknown (”), and
// a later register records the harness.
func TestMigrateV7AddsSessionHarness(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, s := range []string{schema, "DROP INDEX sessions_heartbeat", "DROP TABLE sessions", schemaV7Sessions, "PRAGMA user_version = 7",
		"INSERT INTO sessions(sender,context,owner,heartbeat,registered_at) VALUES('Old','repo','x'," + strconv.FormatInt(now, 10) + "," + strconv.FormatInt(now, 10) + ")",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v7 database: %v", err)
	}
	defer func() { _ = b.Close() }()

	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var h, hv string
	if err := b.db.QueryRow("SELECT harness, harness_version FROM sessions WHERE sender='Old'").Scan(&h, &hv); err != nil {
		t.Fatal(err)
	}
	if h != "" || hv != "" {
		t.Fatalf("existing row harness = %q %q, want empty", h, hv)
	}
	if _, err := b.RegisterWithClient("New", "", "repo", true, "claude-code", "2.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow("SELECT harness, harness_version FROM sessions WHERE sender='New'").Scan(&h, &hv); err != nil || h != "claude-code" || hv != "2.1.0" {
		t.Fatalf("new row harness = %q %q, %v", h, hv, err)
	}
}

// schemaV8Sessions is the sessions table as schema v8 created it, before
// ADR 0017 added the harness process columns.
const schemaV8Sessions = `
CREATE TABLE sessions (
  sender TEXT PRIMARY KEY,
  context TEXT NOT NULL,
  owner TEXT NOT NULL,
  heartbeat INTEGER NOT NULL,
  registered_at INTEGER NOT NULL,
  harness TEXT NOT NULL DEFAULT '',
  harness_version TEXT NOT NULL DEFAULT ''
);`

// TestMigrateV8AddsSessionHarnessProcess (ADR 0017): a v8 file's sessions
// table gains harness_pid and harness_start, existing rows read as 0
// (unknown), and a later register records the harness process.
func TestMigrateV8AddsSessionHarnessProcess(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	for _, s := range []string{schema, "DROP INDEX sessions_heartbeat", "DROP TABLE sessions", schemaV8Sessions, "PRAGMA user_version = 8",
		"INSERT INTO sessions(sender,context,owner,heartbeat,registered_at) VALUES('Old','repo','x'," + now + "," + now + ")",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v8 database: %v", err)
	}
	defer func() { _ = b.Close() }()

	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var pid, start int64
	if err := b.db.QueryRow("SELECT harness_pid, harness_start FROM sessions WHERE sender='Old'").Scan(&pid, &start); err != nil || pid != 0 || start != 0 {
		t.Fatalf("existing row harness process = %d %d, %v; want 0 0", pid, start, err)
	}
	b.SetHarness(procs.Ref{Pid: 42, Start: 7})
	if _, err := b.Register("New", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow("SELECT harness_pid, harness_start FROM sessions WHERE sender='New'").Scan(&pid, &start); err != nil || pid != 42 || start != 7 {
		t.Fatalf("new row harness process = %d %d, %v", pid, start, err)
	}
}

// schemaV9TagSubscriptionTags is tag_subscription_tags as schema v5 through
// v9 created it, before #20 added the pattern range columns.
const schemaV9TagSubscriptionTags = `
CREATE TABLE tag_subscription_tags (
  sender TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY (sender, tags_key, tag),
  FOREIGN KEY (sender, tags_key) REFERENCES tag_subscriptions(sender, tags_key) ON DELETE CASCADE
);`

// shapeV9 writes a v9 database with one subscribed AND set (Kim: a,b) and
// returns the config that opens it.
func shapeV9(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{schema, "DROP TABLE tag_subscription_tags", schemaV9TagSubscriptionTags, "PRAGMA user_version = 9",
		"INSERT INTO tag_subscriptions(sender, tags_key, created_seq) VALUES('Kim','a,b',0)",
		"INSERT INTO tag_subscription_tags(sender, tags_key, tag) VALUES('Kim','a,b','a'),('Kim','a,b','b')",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// tagRanges reads (tag, lo, hi) for sender, ordered by tag.
func tagRanges(t *testing.T, db *sql.DB, sender string) [][3]string {
	t.Helper()
	rows, err := db.Query("SELECT tag, lo, hi FROM tag_subscription_tags WHERE sender=? ORDER BY tag", sender)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestMigrateV9AddsTagRanges (#20): a v9 file's tag_subscription_tags
// gains lo and hi with every existing row backfilled as an exact tag; a
// second open (user_version already 10) runs nothing, so a range written
// in between (here by hand; Task 5's SubscribeTags does it for real) is
// not reset to lo=hi=tag by a repeated backfill.
func TestMigrateV9AddsTagRanges(t *testing.T) {
	cfg := shapeV9(t)
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v9 database: %v", err)
	}
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "a", "a"} || got[1] != [3]string{"b", "b", "b"} {
		t.Fatalf("backfill: %v", got)
	}
	if _, err := b.db.Exec("UPDATE tag_subscription_tags SET lo='env:', hi='env:~' WHERE sender='Kim' AND tag='a'"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen v10 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version after reopen = %d, %v", uv, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "env:", "env:~"} || got[1] != [3]string{"b", "b", "b"} {
		t.Fatalf("a second open must not re-run the backfill: %v", got)
	}
}

// TestMigrateV9FailureLeavesV9AndRetries (#20): the step is one
// transaction, so a failure after its writes leaves the file at v9 without
// the columns, and the next open runs the step again and succeeds.
func TestMigrateV9FailureLeavesV9AndRetries(t *testing.T) {
	cfg := shapeV9(t)
	orig := migrations[9]
	migrations[9] = func(tx *sql.Tx) error {
		if err := orig(tx); err != nil {
			return err
		}
		return errors.New("injected failure after the step's writes")
	}
	t.Cleanup(func() { migrations[9] = orig })
	if _, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("Open must fail when the migration step fails")
	}
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var uv, has int
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != 9 {
		t.Fatalf("user_version after a failed step = %d, want 9, %v", uv, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('tag_subscription_tags') WHERE name IN ('lo','hi')").Scan(&has); err != nil || has != 0 {
		t.Fatalf("columns after a failed step = %d, want 0, %v", has, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrations[9] = orig
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open after the failed step: %v", err)
	}
	defer func() { _ = b.Close() }()
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	if got := tagRanges(t, b.db, "Kim"); len(got) != 2 || got[0] != [3]string{"a", "a", "a"} {
		t.Fatalf("backfill on retry: %v", got)
	}
}

// TestRefsBlock (ADR 0019): the text foldRefs appends for one stored refs
// value. Values are verbatim; a control character puts the value in
// strconv.Quote form; an empty or null array folds nothing; anything that
// is not an array of {kind, value} with values is appended raw.
func TestRefsBlock(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"url", `[{"kind":"url","value":"https://a/b"}]`, "\n\nRefs:\n- https://a/b"},
		{"two refs", `[{"kind":"unix_path","value":"/a b/c"},{"kind":"url","value":"https://d"}]`, "\n\nRefs:\n- /a b/c\n- https://d"},
		{"newline", `[{"kind":"unix_path","value":"a\nb"}]`, "\n\nRefs:\n- \"a\\nb\""},
		{"tab", `[{"kind":"unix_path","value":"a\tb"}]`, "\n\nRefs:\n- \"a\\tb\""},
		{"windows path", `[{"kind":"windows_path","value":"C:\\x\\y"}]`, "\n\nRefs:\n- C:\\x\\y"},
		{"percent encoded", `[{"kind":"url","value":"https://a/My%20Docs"}]`, "\n\nRefs:\n- https://a/My%20Docs"},
		{"empty", `[]`, ""},
		{"null", `null`, ""},
		{"malformed", `not json`, "\n\nRefs: not json"},
		{"malformed with tab", "not\tjson", "\n\nRefs: \"not\\tjson\""},
		{"missing value", `[{"kind":"url"}]`, "\n\nRefs: [{\"kind\":\"url\"}]"},
		{"object", `{"kind":"url","value":"x"}`, "\n\nRefs: {\"kind\":\"url\",\"value\":\"x\"}"},
		{"wrong element type", `[1]`, "\n\nRefs: [1]"},
	}
	for _, c := range cases {
		if got := refsBlock(c.raw); got != c.want {
			t.Errorf("%s: refsBlock(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// newV10RefsFile creates a schema version 10 database (the current DDL
// plus the messages.refs column that version 11 folds away, ADR 0019),
// runs stmts against it, and returns the config that opens it. No channel
// rows are created: the fold and search do not need them, and Open's
// ensureDefaults adds general and memory afterwards.
func newV10RefsFile(t *testing.T, stmts ...string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]string{schema, "ALTER TABLE messages ADD COLUMN refs TEXT", "PRAGMA user_version = 10"}, stmts...)
	for _, s := range all {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// v10RefsRows shapes every case the fold must handle: seq 1 a URL, 2 a
// path with spaces, 3 a Windows path, 4 a value with a newline, 5 two
// refs, 6 an empty array, 7 malformed JSON, 8 a chat message, 9 a
// tombstoned first revision, 10 its live second revision, 11 a memory
// without refs, 12 an entry without a value; embeddings on 1 (folded),
// 6 and 11 (untouched); embed_failures on 1 (folded) and 11 (untouched). Statements are raw strings, so the backslashes
// reach SQLite as written and JSON decodes them.
var v10RefsRows = []string{
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',1,'','issue link',1,1,'[{"kind":"url","value":"https://github.com/ericfitz/agentbus/issues/874"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',2,'','path with spaces',2,1,'[{"kind":"unix_path","value":"/Users/sam/release notes.md"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',3,'','windows path',3,1,'[{"kind":"windows_path","value":"C:\\Users\\sam\\view.go"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',4,'','control chars',4,1,'[{"kind":"unix_path","value":"a\nb"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',5,'','two refs',5,1,'[{"kind":"url","value":"https://example.com/a"},{"kind":"unix_path","value":"/tmp/b"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',6,'','empty refs',6,1,'[]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',7,'','malformed',7,1,'not json')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,refs) VALUES('general','sam','r',8,'','chat with ref','[{"kind":"url","value":"https://example.com/chat"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,tombstone,tombstone_at,refs) VALUES('memory','sam','r',9,'','old revision',9,1,1,9,'[{"kind":"url","value":"https://example.com/rev1"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',10,'','new revision',9,2,'[{"kind":"url","value":"https://example.com/rev2"}]')`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision) VALUES('memory','sam','r',11,'','no refs',11,1)`,
	`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',12,'','missing value',12,1,'[{"kind":"url"}]')`,
	"INSERT INTO embeddings(seq,model,vector) VALUES(1,'m',x'00')",
	"INSERT INTO embeddings(seq,model,vector) VALUES(6,'m',x'00')",
	"INSERT INTO embeddings(seq,model,vector) VALUES(11,'m',x'00')",
	"INSERT INTO embed_failures(seq,model,error,failed_at) VALUES(1,'m','too long',1)",
	"INSERT INTO embed_failures(seq,model,error,failed_at) VALUES(11,'m','too long',1)",
}

// foldedSeq1 is seq 1's content after the fold, asserted by several tests.
const foldedSeq1 = "issue link\n\nRefs:\n- https://github.com/ericfitz/agentbus/issues/874"

// TestMigrateV10FoldsRefs (ADR 0019): opening a v10 file folds every live
// row's refs into its content exactly as the spec shows, leaves tombstoned
// rows and empty arrays alone, keeps messages_fts consistent (integrity
// check and search by each ref), drops the embeddings of folded rows only,
// drops the column, and stamps the current version.
func TestMigrateV10FoldsRefs(t *testing.T) {
	cfg := newV10RefsFile(t, v10RefsRows...)
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v10 database: %v", err)
	}
	defer func() { _ = b.Close() }()

	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var cols string
	if err := b.db.QueryRow("SELECT group_concat(name, ',') FROM pragma_table_info('messages')").Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cols, "refs") {
		t.Fatalf("refs column survived: %s", cols)
	}
	want := map[int64]string{
		1:  foldedSeq1,
		2:  "path with spaces\n\nRefs:\n- /Users/sam/release notes.md",
		3:  "windows path\n\nRefs:\n- C:\\Users\\sam\\view.go",
		4:  "control chars\n\nRefs:\n- \"a\\nb\"",
		5:  "two refs\n\nRefs:\n- https://example.com/a\n- /tmp/b",
		6:  "empty refs",
		7:  "malformed\n\nRefs: not json",
		8:  "chat with ref\n\nRefs:\n- https://example.com/chat",
		9:  "old revision",
		10: "new revision\n\nRefs:\n- https://example.com/rev2",
		11: "no refs",
		12: "missing value\n\nRefs: [{\"kind\":\"url\"}]",
	}
	rows, err := b.db.Query("SELECT seq, content FROM messages ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for rows.Next() {
		var seq int64
		var content string
		if err := rows.Scan(&seq, &content); err != nil {
			t.Fatal(err)
		}
		got[seq] = content
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d rows after migration, want %d", len(got), len(want))
	}
	for seq, w := range want {
		if got[seq] != w {
			t.Errorf("seq %d content = %q, want %q", seq, got[seq], w)
		}
	}
	// The index was maintained by hand around each UPDATE; the external
	// content check must agree with the table.
	if _, err := b.db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')"); err != nil {
		t.Fatalf("messages_fts integrity-check: %v", err)
	}
	sam := reg(t, b, "sam")
	for q, seq := range map[string]int64{
		"issues/874":       1,
		"release notes.md": 2,
		"view.go":          3,
		"/tmp/b":           5,
		"example.com/chat": 8,
		"example.com/rev2": 10,
		"nb":               4,  // token of the quoted newline "a\nb"
		"json":             7,  // Refs: not json
		"kind":             12, // raw array
		"issue link":       1,  // the old entry was replaced, not duplicated
	} {
		r, err := b.Search(sam, SearchInput{Query: q, Mode: "text"})
		if err != nil || len(r.Hits) != 1 || r.Hits[0].Seq != seq {
			t.Fatalf("search %q after the fold: %+v %v, want only seq %d", q, r.Hits, err, seq)
		}
	}
	var embedded string
	if err := b.db.QueryRow("SELECT coalesce(group_concat(seq, ','), '') FROM (SELECT seq FROM embeddings ORDER BY seq)").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != "6,11" {
		t.Fatalf("embeddings after the fold: %q, want 6,11 (folded rows re-embed; untouched rows keep theirs)", embedded)
	}
	var failed string
	if err := b.db.QueryRow("SELECT coalesce(group_concat(seq, ','), '') FROM (SELECT seq FROM embed_failures ORDER BY seq)").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != "11" {
		t.Fatalf("embed_failures after the fold: %q, want 11 (a folded row's rejection concerned its old content)", failed)
	}
}

// TestMigrateV8FileWithoutEmbedFailuresFoldsRefs (ADR 0019): a file last
// written by a binary through v1.12.0 (schema 8) has the refs column but no
// embed_failures table, which only the schema DDL creates and which runs
// after migrate. The fold must not touch that table when it is absent, and
// the DDL creates it afterwards.
func TestMigrateV8FileWithoutEmbedFailuresFoldsRefs(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{schema,
		"DROP INDEX sessions_heartbeat", "DROP TABLE sessions", schemaV8Sessions,
		"DROP TABLE embed_failures",
		"ALTER TABLE messages ADD COLUMN refs TEXT",
		"PRAGMA user_version = 8",
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',1,'','issue link',1,1,'[{"kind":"url","value":"https://github.com/ericfitz/agentbus/issues/874"}]')`,
		"INSERT INTO embeddings(seq,model,vector) VALUES(1,'m',x'00')",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v8 file with live refs and no embed_failures: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var content string
	if err := b.db.QueryRow("SELECT content FROM messages WHERE seq=1").Scan(&content); err != nil || content != foldedSeq1 {
		t.Fatalf("content = %q, want %q, %v", content, foldedSeq1, err)
	}
	var embedded, failures int
	if err := b.db.QueryRow("SELECT (SELECT count(*) FROM embeddings), (SELECT count(*) FROM sqlite_master WHERE type='table' AND name='embed_failures')").Scan(&embedded, &failures); err != nil {
		t.Fatal(err)
	}
	if embedded != 0 || failures != 1 {
		t.Fatalf("embeddings = %d (want 0, folded row re-embeds), embed_failures tables = %d (want 1, created by the DDL)", embedded, failures)
	}
}

// TestMigrateV10DropsRefsWhenNothingToFold (ADR 0019): a v10 file whose
// refs are all on tombstoned rows or empty arrays folds nothing, but the
// column is still dropped and the version stamped.
func TestMigrateV10DropsRefsWhenNothingToFold(t *testing.T) {
	cfg := newV10RefsFile(t,
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,tombstone,tombstone_at,refs) VALUES('memory','sam','r',1,'','gone',1,1,1,1,'[{"kind":"url","value":"https://example.com/gone"}]')`,
		`INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,refs) VALUES('memory','sam','r',2,'','kept',2,1,'[]')`,
	)
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open v10 database: %v", err)
	}
	defer func() { _ = b.Close() }()
	var uv int
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM pragma_table_info('messages') WHERE name='refs'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("refs column must be dropped even with nothing to fold: %d %v", n, err)
	}
	var contents string
	if err := b.db.QueryRow("SELECT group_concat(content, '|') FROM (SELECT content FROM messages ORDER BY seq)").Scan(&contents); err != nil || contents != "gone|kept" {
		t.Fatalf("contents = %q, %v; want unchanged", contents, err)
	}
}

// TestMigrateV10FoldRefsReopenRunsNothing (ADR 0019): a second open of
// the folded file starts at the stored user_version, so the step does not
// run again and no row is folded twice.
func TestMigrateV10FoldRefsReopenRunsNothing(t *testing.T) {
	cfg := newV10RefsFile(t, v10RefsRows...)
	for i := 0; i < 2; i++ {
		b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var uv int
		var content string
		if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
			t.Fatal(err)
		}
		if err := b.db.QueryRow("SELECT content FROM messages WHERE seq=1").Scan(&content); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if uv != schemaVersion || content != foldedSeq1 {
			t.Fatalf("open %d: user_version %d, content %q", i, uv, content)
		}
	}
}

// TestMigrateV10FoldRefsFailureLeavesPreviousVersion (ADR 0019): a step
// that fails after its writes rolls every one of them back (one
// transaction), so the file stays at version 10 with its refs, content
// and embeddings intact, and the next open runs the step again and
// succeeds.
func TestMigrateV10FoldRefsFailureLeavesPreviousVersion(t *testing.T) {
	cfg := newV10RefsFile(t, v10RefsRows...)
	orig := migrations[10]
	migrations[10] = func(tx *sql.Tx) error {
		if err := orig(tx); err != nil {
			return err
		}
		return errors.New("injected after the fold")
	}
	t.Cleanup(func() { migrations[10] = orig })
	if _, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "injected after the fold") {
		t.Fatalf("Open must fail with the injected error, got %v", err)
	}

	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var uv, embedded int
	var content, refs string
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT content, refs FROM messages WHERE seq=1").Scan(&content, &refs); err != nil {
		t.Fatalf("refs column must survive the failed step: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if uv != 10 || content != "issue link" || refs != `[{"kind":"url","value":"https://github.com/ericfitz/agentbus/issues/874"}]` || embedded != 3 {
		t.Fatalf("after the failed step: user_version %d, content %q, refs %q, %d embeddings; want 10, unfolded, intact, 3", uv, content, refs, embedded)
	}

	migrations[10] = orig
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Open after the failed step must succeed: %v", err)
	}
	defer func() { _ = b.Close() }()
	if err := b.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != schemaVersion {
		t.Fatalf("user_version = %d, want %d, %v", uv, schemaVersion, err)
	}
	if err := b.db.QueryRow("SELECT content FROM messages WHERE seq=1").Scan(&content); err != nil || content != foldedSeq1 {
		t.Fatalf("content after the retried step = %q, %v", content, err)
	}
}
