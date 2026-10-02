package bus

import (
	"context"
	"database/sql"
	"errors"
)

// abandoned reports whether t is in_progress with an expired lease or an
// owner that has no live session. An in_progress task with an empty owner
// (only possible from a corrupt or old-binary write) matches the second
// condition: an empty sender never has a session row.
func (b *Bus) abandoned(q queryRower, t Task, now int64) (bool, error) {
	if t.Status != "in_progress" {
		return false, nil
	}
	if t.LeasedUntil != 0 && t.LeasedUntil <= now {
		return true, nil
	}
	var exists int
	err := q.QueryRow("SELECT 1 FROM sessions WHERE sender=? AND heartbeat>=?", t.Owner, now-attachmentExpiryMs).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, internal(err)
	}
	return false, nil
}

// abandonedSet reports which of ts are abandoned, with the same rule as
// abandoned but one sessions query for the whole list instead of one per
// task. The query is skipped when no task needs it (none in_progress, or
// every in_progress one already has an expired lease).
func (b *Bus) abandonedSet(q querier, ts []Task, now int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	needLive := false
	for _, t := range ts {
		if t.Status != "in_progress" {
			continue
		}
		if t.LeasedUntil != 0 && t.LeasedUntil <= now {
			out[t.ID] = true
			continue
		}
		needLive = true
	}
	if !needLive {
		return out, nil
	}
	rows, err := q.Query("SELECT sender FROM sessions WHERE heartbeat>=?", now-attachmentExpiryMs)
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var sender string
		if err := rows.Scan(&sender); err != nil {
			return nil, internal(err)
		}
		live[sender] = true
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	for _, t := range ts {
		if t.Status == "in_progress" && !out[t.ID] && !live[t.Owner] {
			out[t.ID] = true
		}
	}
	return out, nil
}

// reclaimAbandoned returns every abandoned task among ids (all tasks in the
// channel when ids is nil) to pending, owner and lease cleared, as a
// revision of type "reclaimed" sent by as (empty for the tick). It is not
// rate-charged: it bypasses sendEnvelope, inspect, and limits.allow, and
// writeTaskRevision stores no receipt. It also bypasses checkCapacity by
// design: a read (TaskGet, TaskList) must never fail just because the
// database is over budget, and the net growth is one small row (the old
// revision is tombstoned, not deleted). Returns how many it reclaimed.
func (b *Bus) reclaimAbandoned(tx *sql.Tx, as, context, channel string, ids []int64) (int, error) {
	ts, err := loadTasks(tx, channel)
	if err != nil {
		return 0, err
	}
	targets := ts
	if ids != nil {
		targets = nil
		for _, id := range ids {
			if t := taskByID(ts, id); t != nil {
				targets = append(targets, *t)
			}
		}
	}
	now := b.nowMs()
	ab, err := b.abandonedSet(tx, targets, now)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range targets {
		if !ab[t.ID] {
			continue
		}
		t.Status, t.Owner, t.LeasedUntil = "pending", "", 0
		if _, err := b.writeTaskRevision(tx, as, context, t, "reclaimed"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// fixUpBegin opens fixUpAbandoned's write transaction; a package-level test
// hook so a fix-up failure (the write lock unavailable, say) can be
// injected without waiting on the real 5s busy_timeout.
var fixUpBegin = (*sql.DB).Begin

// fixUpAbandoned opens a write transaction to reclaim abandoned tasks (ids
// nil for every task in channel) on behalf of a read that found one. Errors
// here are only internal or not_registered: reclaimAbandoned itself never
// returns anything else.
func (b *Bus) fixUpAbandoned(as, channel string, ids []int64) error {
	tx, err := fixUpBegin(b.db)
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := b.auth(tx, as); err != nil {
		return err
	}
	rctx, err := b.senderContext(tx, as)
	if err != nil {
		return internal(err)
	}
	if _, err := b.reclaimAbandoned(tx, as, rctx, channel, ids); err != nil {
		return err
	}
	return internal(tx.Commit())
}

// tickBegin opens the tick's per-channel write transaction; a package-level
// test hook so a test can count how many channels needed one.
var tickBegin = (*sql.DB).Begin

// reclaimAllAbandonedTasks is the maintenance tick step: it reclaims
// abandoned tasks in every task-list channel, one transaction per channel
// that has any (found by a read-only check first, so an idle list never
// takes the write lock), stopping early once ctx's deadline has passed.
func (b *Bus) reclaimAllAbandonedTasks(ctx context.Context) error {
	rows, err := b.db.Query("SELECT name FROM channels WHERE name = 'tasks' OR name LIKE 'tasks/%'")
	if err != nil {
		return err
	}
	var channels []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		channels = append(channels, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, ch := range channels {
		if ctx.Err() != nil {
			return nil
		}
		ts, err := loadTasks(b.db, ch)
		if err != nil {
			return err
		}
		ab, err := b.abandonedSet(b.db, ts, b.nowMs())
		if err != nil {
			return err
		}
		if len(ab) == 0 {
			continue
		}
		// Re-checked inside the transaction: the snapshot above may be stale.
		tx, err := tickBegin(b.db)
		if err != nil {
			return err
		}
		if _, err := b.reclaimAbandoned(tx, "", "", ch, nil); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
