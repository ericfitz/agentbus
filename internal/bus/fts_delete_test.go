package bus

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus/internal/config"
)

// ftsVocabRows counts the terms left in the full-text index.
func ftsVocabRows(t *testing.T, b *Bus) int {
	t.Helper()
	conn, err := b.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), "CREATE VIRTUAL TABLE IF NOT EXISTS temp.fts_vocab USING fts5vocab(main, messages_fts, 'row')"); err != nil {
		t.Fatalf("fts5vocab: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(context.Background(), "SELECT count(*) FROM temp.fts_vocab").Scan(&n); err != nil {
		t.Fatalf("vocab count: %v", err)
	}
	return n
}

// seedFTSRows writes task rows (valid JSON and malformed or odd shapes),
// chat and memory rows through whatever triggers are installed.
func seedFTSRows(t *testing.T, b *Bus) {
	t.Helper()
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "tasks/w", "memory"); err != nil {
		t.Fatal(err)
	}
	tk, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "Fix rotation", Description: "log the retry"})
	if err != nil {
		t.Fatal(err)
	}
	st := "in_progress"
	if _, err := b.TaskUpdate(sam, TaskPatch{ID: tk.ID, Status: &st, Owner: &sam}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(sam, TaskCreateInput{Channel: "tasks/w", Subject: "nodesc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "general", Content: "plain chatword here"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "memory", Subject: "memsubj", Content: "memory bodyword"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{`not json`, `{"description": 42, "status":"pending"}`, `{"description": {"a":"nestedword"}}`, `{"description": null}`, `[1,2,3]`, ``} {
		if _, err := b.db.Exec(`INSERT INTO messages(channel, sender, context, created_at, content, subject) VALUES ('tasks/w', 'Sam', 'c', 1, ?, 'oddrow')`, c); err != nil {
			t.Fatalf("insert %q: %v", c, err)
		}
	}
}

// deleteAllAndCheckFTS deletes every row; the delete trigger must remove
// exactly what the insert trigger indexed, leaving an empty, intact index.
func deleteAllAndCheckFTS(t *testing.T, b *Bus, when string) {
	t.Helper()
	if n := ftsVocabRows(t, b); n == 0 {
		t.Fatalf("%s: vocabulary empty before delete; the fixture is wrong", when)
	}
	if _, err := b.db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')"); err != nil {
		t.Fatalf("%s: integrity-check before delete: %v", when, err)
	}
	if _, err := b.db.Exec("DELETE FROM messages"); err != nil {
		t.Fatalf("%s: delete: %v", when, err)
	}
	if n := ftsVocabRows(t, b); n != 0 {
		t.Fatalf("%s: %d terms left after deleting every row (the delete trigger removed different text than was indexed)", when, n)
	}
	if _, err := b.db.Exec("INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')"); err != nil {
		t.Fatalf("%s: integrity-check after delete: %v", when, err)
	}
}

func TestFTSDeleteConsistencyFresh(t *testing.T) {
	b := newTestBus(t)
	seedFTSRows(t, b)
	deleteAllAndCheckFTS(t, b, "fresh")
}

// Rows indexed raw under the v13 form, migrated to v14, then deleted
// (tombstoned rows included): the index must end empty and intact.
func TestFTSDeleteConsistencyAfterMigration(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"DROP TRIGGER messages_ai", "DROP TRIGGER messages_ad", "DROP TABLE messages_fts", messagesFTSDDLv6} {
		if _, err := b.db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	seedFTSRows(t, b)
	if _, err := b.db.Exec("PRAGMA user_version = 13"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	for q, want := range map[string]int{"status": 0, "pending": 0, "retry": 2, "chatword": 1, "bodyword": 1, "nestedword": 1, "oddrow": 6} {
		var n int
		if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?", q).Scan(&n); err != nil || n != want {
			t.Fatalf("after migration: %q matches %d (%v), want %d", q, n, err, want)
		}
	}
	deleteAllAndCheckFTS(t, b, "migrated")
}
