// Package bus is the whole Agentbus data layer: one SQLite file, opened directly
// by every MCP adapter process. There is no daemon.
package bus

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericfitz/agentbus-local/internal/config"
	_ "modernc.org/sqlite"
)

const (
	attachmentExpiryMs  = 30_000
	receivePollInterval = 250 * time.Millisecond
)

type Bus struct {
	db    *sql.DB
	cfg   config.Config
	log   *slog.Logger
	owner string
	// Now is the clock; tests override it.
	Now func() time.Time

	limits   *limiter
	embedder *embedder
	embedMu  sync.Mutex

	// hookMu serializes inspection hook runs within this process (spec:
	// "serial within a process, concurrent across processes"). Held only
	// across a hook's own Start..Wait, never while a DB transaction is open.
	hookMu sync.Mutex
	// keyLocks serializes operations sharing the same (sender, idempotency
	// key) process-local, keyed by lockKey, so two concurrent calls with an
	// identical key cannot both miss the receipt check and both run the
	// inspection hook before either commits.
	keyLocks sync.Map // string -> *sync.Mutex

	budgetOverride int64 // tests only
	// inspectCalls counts calls to the inspect hook (tests only). atomic:
	// Send/Edit/Delete run concurrently in production.
	inspectCalls atomic.Int64
}

// lockKey returns an unlock func for the process-local critical section
// shared by all keyed Send/EditMemory/DeleteMemory calls for (sender, key).
// The operation kind is deliberately not part of the key: two different
// operations reusing the same idempotency key must still serialize, so the
// resulting fingerprint conflict (receipts.go) is found after one commits,
// not raced. Callers acquire it before their preflight receipt read and
// release (via defer) on every return path through commit.
//
// ponytail: entries in keyLocks are never removed, so it grows with the
// number of distinct (sender, key) pairs a process ever sees. Add reference
// counting if a long-running process sees enough distinct keys for this to
// matter; receipts themselves already expire after receipt_retention_minutes.
func (b *Bus) lockKey(sender, key string) func() {
	v, _ := b.keyLocks.LoadOrStore(sender+"\x00"+key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// SQLiteDSN builds the "file:" URI DSN for the agentbus.db file inside
// dataDir, using net/url so a directory name containing '?', '#', or '%'
// (which change a naively-concatenated DSN's meaning: '?' starts query
// parameters, '#' a fragment) is properly percent-encoded into the URI's
// path component instead. Exported so tests that open the same on-disk
// file directly (bypassing Open) build an identical, correct DSN. The
// connection parameters (_txlock, _pragma...) are fixed and go in
// RawQuery verbatim: modernc.org/sqlite parses each _pragma value as a
// literal "PRAGMA ..." statement via url.ParseQuery, which treats "(" and
// ")" as ordinary query characters, so they must not be escaped here.
func SQLiteDSN(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "agentbus.db")
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{
		Scheme:   "file",
		Path:     abs,
		RawQuery: "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
	}
	return u.String(), nil
}

func Open(cfg config.Config, log *slog.Logger) (*Bus, error) {
	if err := os.MkdirAll(cfg.DataDirectory, 0o700); err != nil {
		return nil, err
	}
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// auto_vacuum only takes effect via VACUUM; WAL mode above already wrote
	// page 1, so a plain PRAGMA on a fresh file is silently ignored. Convert
	// once (VACUUM is a no-op cost-wise on an empty/small database) and skip
	// on later opens once the mode has stuck.
	var av int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&av); err != nil {
		db.Close()
		return nil, err
	}
	if av != 2 {
		if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			db.Close()
			return nil, err
		}
		if _, err := db.Exec("VACUUM"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// A6: user_version records the schema this database was created with.
	// Read it first so an older binary opening a database a newer binary
	// already stamped fails loudly instead of silently running against a
	// schema it doesn't understand; only a fresh (0) database gets stamped.
	var uv int
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		db.Close()
		return nil, err
	}
	switch {
	case uv > schemaVersion:
		db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than this binary supports (schema version %d)", uv, schemaVersion)
	case uv == 0:
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	}
	owner, err := randomToken()
	if err != nil {
		db.Close()
		return nil, err
	}
	b := &Bus{db: db, cfg: cfg, log: log, owner: owner, Now: time.Now}
	b.limits = newLimiter(cfg)
	if cfg.EmbeddingEndpoint != "" {
		e, err := newEmbedder(cfg)
		if err != nil {
			db.Close()
			return nil, err
		}
		b.embedder = e
	}
	return b, nil
}

// Close waits for any in-flight background embedSoon pass to finish before
// closing the database, so that goroutine never runs its final query against
// an already-closed *sql.DB.
func (b *Bus) Close() error {
	b.embedMu.Lock()
	defer b.embedMu.Unlock()
	return b.db.Close()
}

func (b *Bus) nowMs() int64 { return b.Now().UnixMilli() }

// Usage is the space live data occupies: (page_count - freelist_count) * page_size.
func (b *Bus) Usage() (int64, error) {
	var pc, fc, ps int64
	if err := b.db.QueryRow("PRAGMA page_count").Scan(&pc); err != nil {
		return 0, err
	}
	if err := b.db.QueryRow("PRAGMA freelist_count").Scan(&fc); err != nil {
		return 0, err
	}
	if err := b.db.QueryRow("PRAGMA page_size").Scan(&ps); err != nil {
		return 0, err
	}
	return (pc - fc) * ps, nil
}

func randomToken() (string, error) {
	t := make([]byte, 12)
	if _, err := rand.Read(t); err != nil {
		return "", err
	}
	return hex.EncodeToString(t), nil
}
