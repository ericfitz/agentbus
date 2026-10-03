package bus

import (
	"context"
	"errors"
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
		// After "stale sessions" so an owner whose session just expired
		// counts as dead in the same tick.
		{"abandoned tasks", func() error { return b.reclaimAllAbandonedTasks(tickCtx) }},
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
		{"empty channels", b.reapEmptyChannels},
		{"task expiry", func() error { return b.taskExpiry(tickCtx, now) }},
		{"memory expiry", func() error {
			if b.cfg.MemoryExpiryHours <= 0 {
				return nil
			}
			cutoff := now - int64(b.cfg.MemoryExpiryHours)*3_600_000
			return b.loopChunks(tickCtx, func() (int, error) { return b.memoryExpiryChunk(cutoff) })
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
			// A text the endpoint rejects on its own is recorded and skipped
			// inside embedBatch (ADR 0018), so it cannot block this loop.
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
	defer func() { _ = rows.Close() }()
	var seqs []any
	maxByChannel := map[string]int64{}
	for rows.Next() {
		var s int64
		var ch string
		if err := rows.Scan(&s, &ch); err != nil {
			return 0, err
		}
		seqs = append(seqs, s)
		maxByChannel[ch] = s
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
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

// taskExpiry is the tick's task-expiry step (ADR 0013, design doc section
// 1): every task channel whose last activity (taskLastActivitySQL) is
// older than task_expiry_hours is deleted through the internal
// deleteChannel helper with expiryCutoff set, taking all its tasks
// whatever their status and all subscriptions — a running TUI subscribes
// to every channel, so DeleteChannel's usual live-subscriber refusal would
// otherwise keep every task channel alive forever. 0 disables. Channel
// names are collected first (this query, outside any transaction) and
// deleted one at a time by expireStaleChannels; deleteChannel re-checks
// each one's activity against the same cutoff inside its own transaction,
// closing the gap a plain re-check here would leave against a task write
// landing between this SELECT and that call.
func (b *Bus) taskExpiry(ctx context.Context, now int64) error {
	if b.cfg.TaskExpiryHours <= 0 {
		return nil
	}
	cutoff := now - int64(b.cfg.TaskExpiryHours)*3_600_000
	rows, err := b.db.Query(`SELECT name FROM channels WHERE (name='tasks' OR name LIKE 'tasks/%') AND `+taskLastActivitySQL("channels")+` < ?`, cutoff)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	return b.expireStaleChannels(ctx, names, cutoff)
}

// expireStaleChannels deletes each of names via deleteChannel, checking ctx
// between each since deleteChannel is not chunked (a task channel's whole
// row set goes in one transaction, like DeleteChannel always has). A
// not_found (the channel was deleted concurrently, by another process or an
// earlier step, between taskExpiry's selection and this call) is skipped
// rather than aborting the rest of the list; any other error still returns.
// deleted false with a nil error means deleteChannel's own re-check found
// the channel no longer stale, which is logged as a survival, not an
// expiry.
func (b *Bus) expireStaleChannels(ctx context.Context, names []string, cutoff int64) error {
	for _, n := range names {
		if ctx.Err() != nil {
			return nil
		}
		c, deleted, err := b.deleteChannel(n, "", cutoff)
		if err != nil {
			var be *Error
			if errors.As(err, &be) && be.Code == "not_found" {
				continue
			}
			return err
		}
		if !deleted {
			continue
		}
		b.log.Info("task channel expired", "channel", n, "messages", c.Messages)
	}
	return nil
}

// memoryExpiryChunk is the memory-expiry step's unit of work, called
// through loopChunks: it tombstones up to cleanupChunk live memories
// (messages.memory_id is set, tombstone=0, outside every task channel)
// whose memory_access.accessed_at is older than cutoff, via
// tombstoneMemoryRow — the same write delete_memory does, so readers see
// the deletion and the tombstone is purged by the existing tombstone-purge
// step. Tasks are excluded twice over, independently: the channel<>'tasks'
// filter below (design doc section 2, "non-task channel"), and the INNER
// join to memory_access, which a task row never has (insertMessage skips
// one for a task channel) — so a live memory with no memory_access row can
// never be expired even if the filter were ever wrong (section 2,
// "defensive"). Returns the number of rows this chunk selected (not the
// smaller number actually tombstoned, if any raced with a concurrent edit
// or delete), so loopChunks keeps calling while a full chunk keeps turning
// up.
func (b *Bus) memoryExpiryChunk(cutoff int64) (int, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT m.memory_id, m.seq FROM messages m JOIN memory_access a ON a.memory_id=m.memory_id
	  WHERE m.tombstone=0 AND m.channel<>'tasks' AND m.channel NOT LIKE 'tasks/%' AND a.accessed_at<? LIMIT ?`, cutoff, cleanupChunk)
	if err != nil {
		return 0, err
	}
	type liveMemory struct{ id, seq int64 }
	var candidates []liveMemory
	for rows.Next() {
		var c liveMemory
		if err := rows.Scan(&c.id, &c.seq); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	expired := 0
	for _, c := range candidates {
		ok, err := b.tombstoneMemoryRow(tx, c.id, c.seq)
		if err != nil {
			return 0, err
		}
		if ok {
			expired++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if expired > 0 {
		b.log.Info("memories expired", "count", expired)
	}
	return len(candidates), nil
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
		// Each request has its own embedRequestTimeout; the pass as a whole
		// may make one per row when a batch is rejected (ADR 0018).
		ctx, cancel := context.WithTimeout(context.Background(), (embedBatchSize+1)*embedRequestTimeout)
		defer cancel()
		if _, err := b.embedBatch(ctx); err != nil {
			b.log.Warn("background embedding failed", "err", err)
		}
	}()
}
