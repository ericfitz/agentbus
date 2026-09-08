package bus

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus-local/internal/config"
)

// newTestBus opens a bus on a fresh temp directory with default config.
func newTestBus(t *testing.T) *Bus {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = filepath.Join(cfg.DataDirectory, "config.json")
	b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}

func TestOpenCreatesSchemaWithFTSAndWAL(t *testing.T) {
	b := newTestBus(t)
	var jm string
	if err := b.db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil || jm != "wal" {
		t.Fatalf("journal_mode=%q err=%v", jm, err)
	}
	var av int
	if err := b.db.QueryRow("PRAGMA auto_vacuum").Scan(&av); err != nil || av != 2 {
		t.Fatalf("auto_vacuum=%d err=%v", av, err)
	}
	for _, tbl := range []string{"channels", "sessions", "subscriptions", "messages", "messages_fts", "embeddings", "receipts", "leases", "notices"} {
		var n int
		if err := b.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing", tbl)
		}
	}
	if _, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('c','s','x',1,'','roses are red',13)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'roses'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("fts trigger failed: n=%d err=%v", n, err)
	}
	u, err := b.Usage()
	if err != nil || u <= 0 {
		t.Fatalf("usage=%d err=%v", u, err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.db.Exec("INSERT INTO channels(name,kind,created_seq,evicted_before_seq) VALUES('c','ordinary',0,0)"); err != nil {
		t.Fatal(err)
	}
	cfg := b.cfg
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b2, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	var n int
	if err := b2.db.QueryRow("SELECT count(*) FROM channels").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("data lost on reopen")
	}
}

func TestErrorIsJSON(t *testing.T) {
	e := errf("not_found", false, "memory %d", 7)
	if e.Error() != `{"code":"not_found","message":"memory 7","retryable":false}` {
		t.Fatal(e.Error())
	}
}
