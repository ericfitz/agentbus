package bus

import (
	"database/sql"
	"time"
)

// A wake is noticed when the wall clock advanced this much more than the
// monotonic clock between two samples (ADR 0023).
const wakeSlackMs = 5_000

var processStart = time.Now()

// monotonicSinceStart is Bus.Mono's default: time.Since reads the
// monotonic clock, which processStart carries.
func monotonicSinceStart() time.Duration { return time.Since(processStart) }

// dbOrTx is what a liveness check needs of b.db or a *sql.Tx.
type dbOrTx interface {
	querier
	execQuerier
}

// clockSample is the last wall and monotonic reading a Bus took.
type clockSample struct {
	taken     bool
	wall      int64 // unix ms
	mono      time.Duration
	pendSlept int64 // a detected wake not yet written to meta; 0 when none
	pendWoke  int64
}

// mergeWake folds the wake (slept, woke) into the one stored (storedSlept,
// storedWoke): the same wake seen twice keeps the earlier slept_at and the
// later woke_at, a separate wake replaces it (ADR 0023).
func mergeWake(storedSlept, storedWoke, slept, woke int64) (int64, int64) {
	if storedWoke == 0 {
		return slept, woke
	}
	d := woke - storedWoke
	if d < 0 {
		d = -d
	}
	if d > attachmentExpiryMs {
		return slept, woke
	}
	return min(storedSlept, slept), max(storedWoke, woke)
}

// readWake returns the stored sleep and wake times (unix ms), 0 when none.
func readWake(q queryRower) (slept, woke int64, err error) {
	err = q.QueryRow("SELECT coalesce((SELECT value FROM meta WHERE key='slept_at'),0), coalesce((SELECT value FROM meta WHERE key='woke_at'),0)").Scan(&slept, &woke)
	if err != nil {
		return 0, 0, internal(err)
	}
	return slept, woke, nil
}

// recordWake merges a detected wake into meta in one transaction: read the
// stored pair, merge, write. Given a *sql.Tx it joins the caller's.
func recordWake(q execQuerier, slept, woke int64) error {
	if db, ok := q.(*sql.DB); ok {
		tx, err := db.Begin()
		if err != nil {
			return internal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := recordWake(tx, slept, woke); err != nil {
			return err
		}
		return internal(tx.Commit())
	}
	s, w, err := readWake(q)
	if err != nil {
		return err
	}
	s, w = mergeWake(s, w, slept, woke)
	if _, err := q.Exec("INSERT INTO meta(key,value) VALUES('slept_at',?),('woke_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", s, w); err != nil {
		return internal(err)
	}
	return nil
}

// noteClock takes a clock sample and, if the machine slept since the last
// one, records the wake. A failed write keeps the wake to retry on the next
// call. No lock is held across the database write: callers may hold a
// transaction that another noteClock is waiting on.
func (b *Bus) noteClock(q execQuerier) error {
	b.clockMu.Lock()
	wall, mono := b.nowMs(), b.Mono()
	c := &b.clock
	if c.taken && (wall-c.wall)-(mono-c.mono).Milliseconds() > wakeSlackMs {
		c.pendSlept, c.pendWoke = mergeWake(c.pendSlept, c.pendWoke, c.wall, wall)
	}
	c.taken, c.wall, c.mono = true, wall, mono
	slept, woke := c.pendSlept, c.pendWoke
	c.pendSlept, c.pendWoke = 0, 0
	b.clockMu.Unlock()
	if woke == 0 {
		return nil
	}
	if err := recordWake(q, slept, woke); err != nil {
		b.clockMu.Lock()
		c.pendSlept, c.pendWoke = mergeWake(c.pendSlept, c.pendWoke, slept, woke)
		b.clockMu.Unlock()
		return err
	}
	return nil
}

// livenessCutoff is the heartbeat a session needs to count as live at now
// (ADR 0023). During the grace period after a wake it is the cutoff from
// before the sleep, so a session live when the machine slept stays live
// until the host has been awake for the expiry, and grace is true.
func (b *Bus) livenessCutoff(q dbOrTx, now int64) (cutoff int64, grace bool, err error) {
	if err := b.noteClock(q); err != nil {
		return 0, false, err
	}
	slept, woke, err := readWake(q)
	if err != nil {
		return 0, false, err
	}
	if woke != 0 && now < woke+attachmentExpiryMs {
		return slept - attachmentExpiryMs, true, nil
	}
	return now - attachmentExpiryMs, false, nil
}

// liveCutoff is livenessCutoff at the current time, for checks that do not
// care whether grace applies.
func (b *Bus) liveCutoff(q dbOrTx) (int64, error) {
	c, _, err := b.livenessCutoff(q, b.nowMs())
	return c, err
}
