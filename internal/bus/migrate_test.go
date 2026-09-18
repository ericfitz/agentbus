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

// schemaV1 is the messages DDL as shipped through v1.3.1, with the bytes
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
