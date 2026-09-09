package bus

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	cleanupChunk   = 1000
	vacuumPages    = 256
	embedBatchesPT = 4 // embedding batches per tick
)

// takeLease claims the maintenance lease with one conditional update.
func (b *Bus) takeLease() bool {
	now := b.nowMs()
	interval := int64(b.cfg.CleanupIntervalSeconds) * 1000
	r, err := b.db.Exec("UPDATE leases SET owner=?, expires_at=? WHERE name='maintenance' AND (expires_at < ? OR owner=?)",
		b.owner, now+2*interval, now, b.owner)
	if err != nil {
		return false
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false
	}
	return n == 1
}

// Tick runs one maintenance pass if this process holds the lease. Every
// step is bounded by a context carrying the tick's wall-clock deadline, but
// SQL statements stay non-context: the deadline is enforced between chunks
// and for HTTP; a SQL statement may overrun it. busy_timeout (5 s) bounds
// only how long a statement waits for the lock, not how long it runs once
// it has it (incremental_vacuum in particular). Harmless: every step is
// idempotent.
func (b *Bus) Tick(ctx context.Context) {
	if !b.takeLease() {
		return
	}
	deadline := time.Now().Add(time.Duration(b.cfg.CleanupIntervalSeconds) * time.Second / 2)
	tickCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	now := b.nowMs()
	steps := []struct {
		name string
		fn   func() error
	}{
		{"stale sessions", func() error {
			return b.loopChunks(tickCtx, func() (int, error) {
				return b.deleteChunk("sessions", "heartbeat < ?", []any{now - attachmentExpiryMs})
			})
		}},
		{"receipts", func() error {
			return b.loopChunks(tickCtx, func() (int, error) {
				return b.deleteChunk("receipts", "expires_at < ?", []any{now})
			})
		}},
		{"age cleanup", func() error {
			return b.loopChunks(tickCtx, func() (int, error) {
				return b.deleteOrdinaryChunk("created_at < ?", []any{now - int64(b.cfg.MessageRetentionHours)*3_600_000})
			})
		}},
		{"tombstone purge", func() error {
			return b.loopChunks(tickCtx, func() (int, error) {
				r, err := b.db.Exec("DELETE FROM messages WHERE seq IN (SELECT seq FROM messages WHERE tombstone=1 AND tombstone_at < ? LIMIT ?)",
					now-int64(b.cfg.TombstoneMinHours)*3_600_000, cleanupChunk)
				if err != nil {
					return 0, err
				}
				n, err := r.RowsAffected()
				if err != nil {
					return 0, err
				}
				return int(n), nil
			})
		}},
		{"capacity", func() error { return b.evictToBudget(tickCtx, deadline) }},
		{"vacuum", func() error {
			_, err := b.db.Exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages))
			return err
		}},
		{"embeddings", func() error {
			// embedSoon (the post-write immediate pass) takes embedMu before
			// running embedBatch in the background; without the same lock
			// here, Tick and a concurrent embedSoon pass can both call the
			// embedding endpoint for the same batch at once, doubling HTTP
			// cost for no benefit (A4). A skipped tick pass is harmless:
			// embedSoon or the next tick picks up the same backlog.
			if !b.embedMu.TryLock() {
				return nil
			}
			defer b.embedMu.Unlock()
			// ponytail: embedBatch retries the same first-64 batch forever if one
			// text is permanently rejected by the endpoint (e.g. malformed
			// content). Upgrade path: mark per-row failures so a bad row is
			// skipped instead of blocking the whole batch.
			for i := 0; i < embedBatchesPT && tickCtx.Err() == nil; i++ {
				n, err := b.embedBatch(tickCtx)
				if err != nil || n == 0 {
					return err
				}
			}
			return nil
		}},
	}
	for _, s := range steps {
		if tickCtx.Err() != nil {
			b.log.Info("maintenance deadline reached", "at", s.name)
			return
		}
		if err := s.fn(); err != nil {
			b.log.Warn("maintenance step failed", "step", s.name, "err", err)
		}
	}
}

// loopChunks repeats step until it reports fewer than cleanupChunk rows
// affected (nothing left to do) or ctx's deadline passes.
func (b *Bus) loopChunks(ctx context.Context, step func() (int, error)) error {
	for ctx.Err() == nil {
		n, err := step()
		if err != nil || n < cleanupChunk {
			return err
		}
	}
	return nil
}

// deleteOrdinaryChunk deletes up to cleanupChunk ordinary messages matching cond,
// oldest first, and raises each affected channel's retention boundary.
func (b *Bus) deleteOrdinaryChunk(cond string, args []any) (int, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query("SELECT seq, channel FROM messages WHERE memory_id IS NULL AND "+cond+" ORDER BY seq LIMIT ?", append(args, cleanupChunk)...)
	if err != nil {
		return 0, err
	}
	var seqs []any
	maxByChannel := map[string]int64{}
	for rows.Next() {
		var s int64
		var ch string
		if err := rows.Scan(&s, &ch); err != nil {
			_ = rows.Close()
			return 0, err
		}
		seqs = append(seqs, s)
		maxByChannel[ch] = s
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	if len(seqs) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec("DELETE FROM messages WHERE seq IN (?"+strings.Repeat(",?", len(seqs)-1)+")", seqs...); err != nil {
		return 0, err
	}
	for ch, m := range maxByChannel {
		if _, err := tx.Exec("UPDATE channels SET evicted_before_seq=max(evicted_before_seq, ?) WHERE name=?", m+1, ch); err != nil {
			return 0, err
		}
	}
	return len(seqs), tx.Commit()
}

// deleteChunk deletes at most cleanupChunk rows from table matching cond,
// via rowid (every table here has an implicit or explicit rowid).
func (b *Bus) deleteChunk(table, cond string, args []any) (int, error) {
	r, err := b.db.Exec("DELETE FROM "+table+" WHERE rowid IN (SELECT rowid FROM "+table+" WHERE "+cond+" LIMIT ?)", append(args, cleanupChunk)...)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (b *Bus) budget() int64 {
	if b.budgetOverride > 0 {
		return b.budgetOverride
	}
	return int64(b.cfg.SQLiteBudgetMiB) << 20
}

// evictToBudget deletes ordinary messages oldest-first, then sets or clears
// the capacity notice. A sweep only starts once usage exceeds budget itself
// (not merely the lower target it sweeps down to); notice clearing runs
// independently of whether a sweep ran at all. The sweep loop also checks
// ctx (A5): looping on the wall-clock deadline alone means a caller whose
// ctx is canceled mid-sweep (Tick on client disconnect) still runs up to a
// full chunk-and-vacuum cycle before the deadline check catches it.
func (b *Bus) evictToBudget(ctx context.Context, deadline time.Time) error {
	budget := b.budget()
	target := budget - budget*int64(b.cfg.CleanupFreePercent)/100
	u, err := b.Usage()
	if err != nil {
		return err
	}
	if u > budget {
		for ctx.Err() == nil && time.Now().Before(deadline) {
			if u <= target {
				break
			}
			n, err := b.deleteOrdinaryChunk("1=1", nil)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			if _, err := b.db.Exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages)); err != nil {
				return err
			}
			if u, err = b.Usage(); err != nil {
				return err
			}
		}
	}
	if u, err = b.Usage(); err != nil {
		return err
	}
	if u > budget {
		_, err = b.db.Exec("INSERT OR REPLACE INTO notices(kind,message,set_at) VALUES('capacity',?,?)", b.capacityNotice(), b.nowMs())
		return err
	}
	_, err = b.db.Exec("DELETE FROM notices WHERE kind='capacity'")
	return err
}

func (b *Bus) capacityNotice() string {
	return fmt.Sprintf("Agentbus capacity exhausted: sqlite_budget_mib=%d in %s is filled by live memories and protected tombstones, which are never evicted automatically. Either raise sqlite_budget_mib in that file and restart your agents, or run `agentbus reset` to discard all bus data.",
		b.cfg.SQLiteBudgetMiB, b.cfg.Path)
}

// checkCapacity runs before a write: evict inline if over budget; fail if still over.
func (b *Bus) checkCapacity() error {
	u, err := b.Usage()
	if err != nil {
		return internal(err)
	}
	if u <= b.budget() {
		return nil
	}
	if err := b.evictToBudget(context.Background(), time.Now().Add(2*time.Second)); err != nil {
		return internal(err)
	}
	u, err = b.Usage()
	if err != nil {
		return internal(err)
	}
	if u > b.budget() {
		return errf("capacity_exhausted", false, "%s", b.capacityNotice())
	}
	return nil
}

// embedSoon runs one best-effort embedding pass in the background so a new
// memory is searchable quickly. Overlapping calls collapse into one pass.
func (b *Bus) embedSoon() {
	if b.embedder == nil || !b.embedMu.TryLock() {
		return
	}
	go func() {
		defer b.embedMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), embedRequestTimeout)
		defer cancel()
		if _, err := b.embedBatch(ctx); err != nil {
			b.log.Warn("background embedding failed", "err", err)
		}
	}()
}
