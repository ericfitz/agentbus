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

// Tick runs one maintenance pass if this process holds the lease.
func (b *Bus) Tick(ctx context.Context) {
	if !b.takeLease() {
		return
	}
	deadline := time.Now().Add(time.Duration(b.cfg.CleanupIntervalSeconds) * time.Second / 2)
	now := b.nowMs()
	steps := []struct {
		name string
		fn   func() error
	}{
		{"stale sessions", func() error {
			_, err := b.db.Exec("DELETE FROM sessions WHERE heartbeat < ?", now-attachmentExpiryMs)
			return err
		}},
		{"receipts", func() error {
			_, err := b.db.Exec("DELETE FROM receipts WHERE expires_at < ?", now)
			return err
		}},
		{"idle subscriptions", func() error {
			_, err := b.db.Exec("DELETE FROM subscriptions WHERE last_activity < ?", now-int64(b.cfg.CursorIdleHours)*3_600_000)
			return err
		}},
		{"age cleanup", func() error {
			return b.loopChunks(deadline, func() (int, error) {
				return b.deleteOrdinaryChunk("created_at < ?", []any{now - int64(b.cfg.MessageRetentionHours)*3_600_000})
			})
		}},
		{"tombstone purge", func() error {
			return b.loopChunks(deadline, func() (int, error) {
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
		{"capacity", func() error { return b.evictToBudget(deadline) }},
		{"vacuum", func() error {
			_, err := b.db.Exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages))
			return err
		}},
		{"embeddings", func() error {
			// ponytail: embedBatch retries the same first-64 batch forever if one
			// text is permanently rejected by the endpoint (e.g. malformed
			// content). Upgrade path: mark per-row failures so a bad row is
			// skipped instead of blocking the whole batch.
			for i := 0; i < embedBatchesPT && time.Now().Before(deadline); i++ {
				n, err := b.embedBatch(ctx)
				if err != nil || n == 0 {
					return err
				}
			}
			return nil
		}},
	}
	for _, s := range steps {
		if time.Now().After(deadline) {
			b.log.Info("maintenance deadline reached", "at", s.name)
			return
		}
		if err := s.fn(); err != nil {
			b.log.Warn("maintenance step failed", "step", s.name, "err", err)
		}
	}
}

func (b *Bus) loopChunks(deadline time.Time, step func() (int, error)) error {
	for time.Now().Before(deadline) {
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
	defer tx.Rollback()
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
			rows.Close()
			return 0, err
		}
		seqs = append(seqs, s)
		maxByChannel[ch] = s
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
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

func (b *Bus) budget() int64 {
	if b.budgetOverride > 0 {
		return b.budgetOverride
	}
	return int64(b.cfg.SQLiteBudgetMiB) << 20
}

// evictToBudget deletes ordinary messages oldest-first until usage is below
// budget minus cleanup_free_percent, then sets or clears the capacity notice.
func (b *Bus) evictToBudget(deadline time.Time) error {
	budget := b.budget()
	target := budget - budget*int64(b.cfg.CleanupFreePercent)/100
	for time.Now().Before(deadline) {
		u, err := b.Usage()
		if err != nil {
			return err
		}
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
	}
	u, err := b.Usage()
	if err != nil {
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
	if err := b.evictToBudget(time.Now().Add(2 * time.Second)); err != nil {
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
