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
	n := 0
	for _, t := range targets {
		ab, err := b.abandoned(tx, t, now)
		if err != nil {
			return n, err
		}
		if !ab {
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

// reclaimAllAbandonedTasks is the maintenance tick step: it reclaims
// abandoned tasks in every task-list channel, one transaction per channel,
// stopping early once ctx's deadline has passed.
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
		tx, err := b.db.Begin()
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
