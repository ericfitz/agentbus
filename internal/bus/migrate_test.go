package bus

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/config"
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

// TestMigrateV1DropsMessagesBytes (ADR 0006 item 4): opening a schema
// version 1 database rebuilds messages without bytes in one transaction,
// keeps every row, embedding, and FTS entry, carries the AUTOINCREMENT
// counter over, and stamps user_version 2.
func TestMigrateV1DropsMessagesBytes(t *testing.T) {
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
	for _, s := range []string{schemaV1Messages, schema, "PRAGMA user_version = 1",
		"INSERT INTO channels(name,kind,created_seq) VALUES('memory','memory',0)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,memory_id,revision,bytes) VALUES('memory','sam','r',1,'','remember me',1,1,42)",
		"INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('general','sam','r',2,'','hello world',42)",
		"INSERT INTO embeddings(seq,model,vector) VALUES(1,'m',x'00')",
		"DELETE FROM messages WHERE seq=2", // counter (2) now exceeds max(seq) (1)
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
	var content string
	if err := b.db.QueryRow("SELECT content FROM messages WHERE seq=1").Scan(&content); err != nil || content != "remember me" {
		t.Fatalf("row 1 after rebuild: %q, %v", content, err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&n); err != nil || n != 1 {
		t.Fatalf("embeddings after rebuild: %d, %v (want 1: rebuild must not cascade)", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'remember'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts after rebuild: %d, %v", n, err)
	}
	for _, obj := range []string{"messages_ai", "messages_ad", "messages_channel_seq", "messages_memory"} {
		if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", obj).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s not recreated after rebuild", obj)
		}
	}
	// seq stays monotonic: the next message must get 3, not 2.
	sam := reg(t, b, "sam")
	r, err := b.Send(sam, SendInput{Channel: "general", Content: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seq != 3 {
		t.Fatalf("seq after migration = %d, want 3 (AUTOINCREMENT counter not carried over)", r.Seq)
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

// TestMigrateV2To5 (#13): a database shaped like 1.6.0 -- user_version 2,
// missing message_tags, tag_subscriptions, and tag_subscription_tags --
// opens, walks every migration step up to schemaVersion, passes the FK
// check each step already runs, and tag subscribe/send/receive works
// afterward.
func TestMigrateV2To5(t *testing.T) {
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
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags_tag'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("message_tags_tag must be dropped: %d %v", n, err)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_tags_tag_seq'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("message_tags_tag_seq missing: %d %v", n, err)
	}
}
