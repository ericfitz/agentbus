package bus

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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

// TestDataDirWithURIMetacharactersOpensAndStaysIsolated reproduces F2: a
// data directory name containing '?', '#', or '%' must not change the
// SQLite DSN's meaning (a naive "file:"+path concatenation lets '?' start
// query parameters and '#' start a fragment), and two such directories
// must remain distinct databases.
func TestDataDirWithURIMetacharactersOpensAndStaysIsolated(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "proj?a#b%c")
	dirB := filepath.Join(base, "proj?x#y%z")
	openAt := func(dir string) *Bus {
		t.Helper()
		cfg := config.Default()
		cfg.DataDirectory = dir
		cfg.Path = filepath.Join(dir, "config.json")
		b, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("open %q: %v", dir, err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}
	a := openAt(dirA)
	bb := openAt(dirB)

	aSam := reg(t, a, "Sam")
	a.CreateChannel(aSam, "dev", "ordinary")
	if _, err := a.Send(aSam, SendInput{Channel: "dev", Content: "only in a"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := bb.db.QueryRow("SELECT count(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("dirB sees dirA's data: n=%d (directories are not isolated)", n)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dirA lost its own write: n=%d", n)
	}
}

// TestOpenRejectsNewerSchemaVersion is the A6 forward-compat guard: an
// older binary opening a database a newer binary already stamped with a
// higher user_version must fail loudly instead of silently running
// against a schema it doesn't understand.
func TestOpenRejectsNewerSchemaVersion(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	cfg := b.cfg
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("Open must reject a database with a newer schema version")
	} else if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error must name the found schema version, got %v", err)
	}
}

func TestErrorIsJSON(t *testing.T) {
	e := errf("not_found", false, "memory %d", 7)
	if e.Error() != `{"code":"not_found","message":"memory 7","retryable":false}` {
		t.Fatal(e.Error())
	}
}
