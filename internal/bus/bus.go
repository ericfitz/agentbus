// Package bus is the whole Agentbus data layer: one SQLite file, opened directly
// by every MCP adapter process. There is no daemon.
package bus

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

	budgetOverride int64 // tests only
}

func Open(cfg config.Config, log *slog.Logger) (*Bus, error) {
	if err := os.MkdirAll(cfg.DataDirectory, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(cfg.DataDirectory, "agentbus.db")
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
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
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		db.Close()
		return nil, err
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

func (b *Bus) Close() error { return b.db.Close() }

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
