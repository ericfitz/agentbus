package bus

import (
	"context"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/procs"
)

// ADR 0023: after a wake, liveness is judged from before the sleep.

// hostClock is one machine's clocks for every bus attached to it: wall
// advances while awake and asleep, mono only while awake.
type hostClock struct {
	base       time.Time
	wall, mono time.Duration
}

func newHostClock(bs ...*Bus) *hostClock {
	h := &hostClock{base: time.Now()}
	h.attach(bs...)
	return h
}

func (h *hostClock) attach(bs ...*Bus) {
	for _, b := range bs {
		b.Now = func() time.Time { return h.base.Add(h.wall) }
		b.Mono = func() time.Duration { return h.mono }
		b.clockMu.Lock()
		b.clock = clockSample{}
		b.clockMu.Unlock()
	}
}

// awake advances both clocks.
func (h *hostClock) awake(d time.Duration) { h.wall += d; h.mono += d }

// sleep advances the wall clock alone.
func (h *hostClock) sleep(d time.Duration) { h.wall += d }

type wakeEnv struct {
	b, pat *Bus // b is Sam, pat is the task owner
	hc     *hostClock
	ch     string
	id     int64
}

// newWakeEnv has Pat claim a task (lease 0 for none), then both buses take
// a clock sample with a heartbeat, as their MCP servers do every 10 s.
func newWakeEnv(t *testing.T, lease time.Duration) *wakeEnv {
	t.Helper()
	b, pat := twoAgents(t)
	e := &wakeEnv{b: b, pat: pat, hc: newHostClock(b, pat), ch: taskList(t, b)}
	e.id = mustCreate(t, b, TaskCreateInput{Subject: "x"}).ID
	var until int64
	if lease != 0 {
		until = e.hc.base.Add(e.hc.wall + lease).UnixMilli()
	}
	if _, err := pat.TaskClaim("Pat", e.id, until, ""); err != nil {
		t.Fatal(err)
	}
	e.beat(t, b, pat)
	return e
}

func (e *wakeEnv) beat(t *testing.T, bs ...*Bus) {
	t.Helper()
	for _, b := range bs {
		if err := b.Heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *wakeEnv) task(t *testing.T) Task {
	t.Helper()
	ts, err := loadTasks(e.b.db, e.ch)
	if err != nil {
		t.Fatal(err)
	}
	return *taskByID(ts, e.id)
}

func (e *wakeEnv) wantOwner(t *testing.T, owner string) {
	t.Helper()
	if got := e.task(t); got.Owner != owner {
		t.Fatalf("owner=%q status=%q, want owner %q", got.Owner, got.Status, owner)
	}
}

func (e *wakeEnv) list(t *testing.T) {
	t.Helper()
	if _, err := e.b.TaskList("Sam", TaskListInput{Channel: e.ch}); err != nil {
		t.Fatal(err)
	}
}

func storedWake(t *testing.T, b *Bus) (slept, woke int64) {
	t.Helper()
	slept, woke, err := readWake(b.db)
	if err != nil {
		t.Fatal(err)
	}
	return slept, woke
}

// 1. The owner sleeps 5 minutes; another bus that also slept lists tasks
// before the owner heartbeats. The task stays claimed.
func TestWakeGraceKeepsSleepingOwnersTask(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.list(t)
	e.wantOwner(t, "Pat")
}

func TestWakeGraceKeepsTaskThroughMaintenanceTick(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.b.Tick(context.Background())
	e.wantOwner(t, "Pat")
}

// 2. The sweep in register leaves the sleeping owner's row; readers see it.
func TestWakeGraceKeepsSessionRowAndReaders(t *testing.T) {
	e := newWakeEnv(t, 0)
	if _, err := e.b.db.Exec("UPDATE sessions SET harness_pid=777, harness_start=1, harness='cc' WHERE sender='Pat'"); err != nil {
		t.Fatal(err)
	}
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	if _, err := e.b.Register("Zed", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, e.b, "Pat"); !ok {
		t.Fatal("register's sweep deleted the sleeping owner's row")
	}
	if got, err := e.b.IdentityForHarness(procsRef(777, 1)); err != nil || got != "Pat" {
		t.Fatalf("IdentityForHarness=%q %v", got, err)
	}
	if live, err := e.b.SessionLive("Pat"); err != nil || !live {
		t.Fatalf("SessionLive=%v %v", live, err)
	}
	if _, live, err := e.b.SessionHarness("Pat"); err != nil || !live {
		t.Fatalf("SessionHarness live=%v %v", live, err)
	}
	if c, err := e.b.SessionClient("Pat"); err != nil || c != "cc" {
		t.Fatalf("SessionClient=%q %v", c, err)
	}
	if n, err := e.b.LiveSessionCount(); err != nil || n != 3 {
		t.Fatalf("LiveSessionCount=%d %v, want 3", n, err)
	}
	e.b.Tick(context.Background())
	if _, _, _, ok := sessionRowFor(t, e.b, "Pat"); !ok {
		t.Fatal("tick's sweep deleted the sleeping owner's row")
	}
}

// 3. Once the host has been awake for the expiry with no heartbeat from the
// owner, it is stale and its task is reclaimed.
func TestWakeGraceEndsAfterExpiry(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.hc.awake(31 * time.Second)
	e.list(t)
	e.wantOwner(t, "")
	if _, err := e.b.Register("Zed", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, e.b, "Pat"); ok {
		t.Fatal("Pat's stale row survived the sweep after grace")
	}
}

// 4. A session already stale before the sleep is swept and loses its tasks
// during grace.
func TestWakeGraceStillSweepsSessionStaleBeforeSleep(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.awake(60 * time.Second)
	e.beat(t, e.b) // Pat is silent: dead before the machine slept
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.list(t)
	e.wantOwner(t, "")
	if _, err := e.b.Register("Zed", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, e.b, "Pat"); ok {
		t.Fatal("a session stale before the sleep survived the sweep")
	}
}

// 5. A lease that expired during the sleep is not reclaimed in grace,
// is reclaimed after it unless renewed, and a renewal in grace keeps it.
func TestWakeGraceLeaseExpiredDuringSleep(t *testing.T) {
	e := newWakeEnv(t, time.Minute)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.list(t)
	e.wantOwner(t, "Pat")

	e.hc.awake(31 * time.Second)
	e.list(t)
	e.wantOwner(t, "")
}

func TestWakeGraceLeaseRenewedInGraceIsKept(t *testing.T) {
	e := newWakeEnv(t, time.Minute)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	renewed := e.hc.base.Add(e.hc.wall + 10*time.Minute).UnixMilli()
	if _, err := e.pat.TaskClaim("Pat", e.id, renewed, ""); err != nil {
		t.Fatal(err)
	}
	e.hc.awake(31 * time.Second)
	e.beat(t, e.pat, e.b)
	e.list(t)
	e.wantOwner(t, "Pat")
	revs, err := e.b.MemoryRevisions("Sam", e.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range revs {
		if r.Type == "reclaimed" {
			t.Fatal("the renewal went through a reclaim")
		}
	}
}

// 6. Two processes detecting one wake keep the earliest slept_at and the latest woke_at; a later wake replaces the pair.
func TestWakeRecordMergesSameWakeAndReplacesLaterOne(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.awake(3 * time.Second)
	e.beat(t, e.pat)
	s2 := e.hc.base.Add(e.hc.wall).UnixMilli() // the sleep began here, for both
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.pat)
	w1 := e.hc.base.Add(e.hc.wall).UnixMilli()
	e.hc.awake(2 * time.Second)
	e.beat(t, e.b)
	w2 := e.hc.base.Add(e.hc.wall).UnixMilli()
	if slept, woke := storedWake(t, e.b); slept != s2 || woke != w2 || w2 <= w1 {
		t.Fatalf("slept=%d woke=%d, want %d %d (w1=%d)", slept, woke, s2, w2, w1)
	}

	e.hc.awake(2 * time.Minute)
	e.beat(t, e.b)
	s3 := e.hc.base.Add(e.hc.wall).UnixMilli()
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	w3 := e.hc.base.Add(e.hc.wall).UnixMilli()
	if slept, woke := storedWake(t, e.b); slept != s3 || woke != w3 {
		t.Fatalf("slept=%d woke=%d, want the later wake %d %d", slept, woke, s3, w3)
	}
}

// 7. Without a wake nothing changes; a drift under 5 s is not a wake.
func TestNoWakeBehaviorUnchanged(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.awake(31 * time.Second)
	e.beat(t, e.b) // Pat is silent
	e.list(t)
	e.wantOwner(t, "")
	if slept, woke := storedWake(t, e.b); slept != 0 || woke != 0 {
		t.Fatalf("recorded a wake: %d %d", slept, woke)
	}
}

func TestSmallClockDriftIsNotAWake(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(4 * time.Second)
	e.beat(t, e.b, e.pat)
	if slept, woke := storedWake(t, e.b); slept != 0 || woke != 0 {
		t.Fatalf("recorded a wake: %d %d", slept, woke)
	}
}

// 8. A bus with no earlier sample reads the stored grace and applies it.
func TestShortLivedBusAppliesStoredGrace(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	c := openOther(t, e.b)
	e.hc.attach(c)
	if live, err := c.SessionLive("Pat"); err != nil || !live {
		t.Fatalf("SessionLive=%v %v", live, err)
	}
	e.hc.awake(31 * time.Second)
	if live, err := c.SessionLive("Pat"); err != nil || live {
		t.Fatalf("after grace SessionLive=%v %v", live, err)
	}
}

// 9. Reset leaves the pair: it is a fact about the host, not bus state.
func TestResetKeepsWakeGrace(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	s, w := storedWake(t, e.b)
	if s == 0 || w == 0 {
		t.Fatalf("no wake recorded: %d %d", s, w)
	}
	if err := e.b.Reset(); err != nil {
		t.Fatal(err)
	}
	if s2, w2 := storedWake(t, e.b); s2 != s || w2 != w {
		t.Fatalf("reset changed the pair: %d %d, want %d %d", s2, w2, s, w)
	}
}

// A wake detected inside a transaction that rolls back is not lost: the
// competitor's refused task_claim during grace must not leave the next
// task_list without the grace.
func TestWakeDetectedInRolledBackTxIsKept(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	_, err := e.b.TaskClaim("Sam", e.id, 0, "")
	wantCode(t, err, "conflict")
	if _, woke := storedWake(t, e.b); woke != 0 {
		t.Fatal("the rolled-back transaction left a wake record")
	}
	e.list(t)
	e.wantOwner(t, "Pat")
	if _, woke := storedWake(t, e.b); woke == 0 {
		t.Fatal("the kept wake was never written")
	}
}

// A backward wall-clock step of more than the expiry after a recorded wake
// must not read every session as stale: a stored woke_at in the future is
// no grace at all.
func TestBackwardClockStepAfterWakeIsNotGrace(t *testing.T) {
	e := newWakeEnv(t, 0)
	e.hc.sleep(5 * time.Minute)
	e.beat(t, e.b)
	e.hc.wall -= 10 * time.Minute // stepped back before the sleep began
	e.beat(t, e.pat, e.b)
	if live, err := e.b.SessionLive("Pat"); err != nil || !live {
		t.Fatalf("SessionLive=%v %v after a backward step", live, err)
	}
	e.list(t)
	e.wantOwner(t, "Pat")
	if _, err := e.b.Register("Zed", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, e.b, "Pat"); !ok {
		t.Fatal("the sweep deleted a live session after a backward clock step")
	}
}

// A stored pair that is not 0 < slept <= woke is never grace, and
// recordWake refuses to store one.
func TestInvalidStoredWakeIsNoGrace(t *testing.T) {
	e := newWakeEnv(t, 0)
	now := e.b.nowMs()
	for _, p := range [][2]int64{{0, now}, {now, now - 1000}, {-5, now}, {now, 0}} {
		if _, err := e.b.db.Exec("INSERT INTO meta(key,value) VALUES('slept_at',?),('woke_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", p[0], p[1]); err != nil {
			t.Fatal(err)
		}
		if _, grace, err := e.b.livenessCutoff(e.b.db, now); err != nil || grace {
			t.Fatalf("pair %v: grace=%v err=%v", p, grace, err)
		}
	}
	for _, p := range [][2]int64{{0, now}, {now, now - 1}, {-1, now}} {
		if err := recordWake(e.b.db, p[0], p[1]); err == nil {
			t.Fatalf("recordWake accepted %v", p)
		}
	}
}

func procsRef(pid int, start int64) procs.Ref { return procs.Ref{Pid: pid, Start: start} }
