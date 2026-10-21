package bus

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/procs"
)

// ADR 0022: a process whose session rows another process swept after a
// sleep puts them back, keeping name and registered_at.

// sleepSweep jumps the clock past the attachment expiry on every bus in bs
// and has the first one run the stale-session sweep, as a wake does.
func sleepSweep(t *testing.T, bs ...*Bus) {
	t.Helper()
	later := time.Now().Add(time.Minute)
	for _, b := range bs {
		b.Now = func() time.Time { return later }
	}
	if _, err := bs[0].db.Exec("DELETE FROM sessions WHERE heartbeat < ?", bs[0].nowMs()-attachmentExpiryMs); err != nil {
		t.Fatal(err)
	}
}

func sessionRowFor(t *testing.T, b *Bus, name string) (owner string, heartbeat, registeredAt int64, found bool) {
	t.Helper()
	err := b.db.QueryRow("SELECT owner, heartbeat, registered_at FROM sessions WHERE sender=?", name).Scan(&owner, &heartbeat, &registeredAt)
	return owner, heartbeat, registeredAt, err == nil
}

func openOther(t *testing.T, b *Bus) *Bus {
	t.Helper()
	o, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o
}

func TestHeartbeatRestoresSweptSession(t *testing.T) {
	b := newTestBus(t)
	h := procs.Ref{Pid: 100, Start: 5}
	b.SetHarness(h)
	if _, err := b.RegisterWithClient("repo", "", "ctx", true, "claude-code", "2.0"); err != nil {
		t.Fatal(err)
	}
	_, _, regAt, _ := sessionRowFor(t, b, "repo")
	sleepSweep(t, b)
	if _, _, _, ok := sessionRowFor(t, b, "repo"); ok {
		t.Fatal("sweep did not delete the row")
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	owner, hb, gotReg, ok := sessionRowFor(t, b, "repo")
	if !ok || owner != b.owner || gotReg != regAt || hb != b.nowMs() {
		t.Fatalf("restored row: owner=%q hb=%d reg=%d (want %d) found=%v", owner, hb, gotReg, regAt, ok)
	}
	if got, err := b.IdentityForHarness(h); err != nil || got != "repo" {
		t.Fatalf("IdentityForHarness = %q %v", got, err)
	}
	if c, _ := b.SessionClient("repo"); c != "claude-code" {
		t.Fatalf("harness client not restored: %q", c)
	}
}

func TestAuthRestoresSweptSessionBeforeHeartbeat(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	sleepSweep(t, b)
	if _, err := b.Discover("repo"); err != nil {
		t.Fatalf("Discover after sweep: %v", err)
	}
	if _, _, _, ok := sessionRowFor(t, b, "repo"); !ok {
		t.Fatal("row not restored")
	}
	// The transaction path restores too.
	sleepSweep(t, b)
	if _, err := b.Send("repo", SendInput{Channel: "dm/repo", Content: "hi"}); err != nil {
		t.Fatalf("Send after sweep: %v", err)
	}
}

func TestRestoreNeverTakesAnotherOwnersName(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	other := openOther(t, b)
	sleepSweep(t, b, other)
	if r, err := other.Register("repo", "", "ctx2", true); err != nil || r.Sender != "repo" {
		t.Fatalf("other should take the freed name: %+v %v", r, err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if owner, _, _, _ := sessionRowFor(t, b, "repo"); owner != other.owner {
		t.Fatalf("other's row overwritten, owner=%q", owner)
	}
	if _, err := b.Discover("repo"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("original must get not_registered, got %v", err)
	}
	// The entry was forgotten: once the other's row is gone, nothing returns.
	if _, err := other.db.Exec("DELETE FROM sessions WHERE owner=?", other.owner); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, b, "repo"); ok {
		t.Fatal("original retried after losing the name")
	}
}

func TestEndSessionsForgetsRemembered(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	if err := b.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, b, "repo"); ok {
		t.Fatal("ended session resurrected")
	}
}

func TestSleepKeepsNamesOfTwoSessions(t *testing.T) {
	b := newTestBus(t)
	b.SetHarness(procs.Ref{Pid: 1, Start: 1})
	other := openOther(t, b)
	other.SetHarness(procs.Ref{Pid: 2, Start: 2})
	if _, err := b.Register("agentbus", "", "r", true); err != nil {
		t.Fatal(err)
	}
	if r, _ := other.Register("agentbus", "", "r", true); r.Sender != "agentbus2" {
		t.Fatalf("setup: %q", r.Sender)
	}
	sleepSweep(t, b, other)
	// The second session heartbeats first, as the race would have it.
	if err := other.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if o, _, _, ok := sessionRowFor(t, b, "agentbus"); !ok || o != b.owner {
		t.Fatalf("agentbus owner=%q ok=%v", o, ok)
	}
	if o, _, _, ok := sessionRowFor(t, b, "agentbus2"); !ok || o != other.owner {
		t.Fatalf("agentbus2 owner=%q ok=%v", o, ok)
	}
}

func TestHeartbeatWithoutRegistrationInsertsNothing(t *testing.T) {
	b := newTestBus(t)
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestRestoreKeepsSubagentName(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	if r, err := b.Register("impl", "repo", "ctx", true); err != nil || r.Sender != "repo/impl" {
		t.Fatalf("%+v %v", r, err)
	}
	sleepSweep(t, b)
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"repo", "repo/impl"} {
		if _, _, _, ok := sessionRowFor(t, b, n); !ok {
			t.Fatalf("%s not restored", n)
		}
	}
}

func TestResetBlocksRestore(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	admin := openOther(t, b)
	if err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, b, "repo"); ok {
		t.Fatal("heartbeat restored a row after reset")
	}
	if _, err := b.Discover("repo"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("auth after reset: %v", err)
	}
}

func TestResetThenAuthNotRegistered(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	if err := openOther(t, b).Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Discover("repo"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("auth after reset: %v", err)
	}
}

func TestReRegisterAfterResetRestoresAfterSweep(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	if err := openOther(t, b).Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	sleepSweep(t, b)
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := sessionRowFor(t, b, "repo"); !ok {
		t.Fatal("not restored under the new generation")
	}
}

func TestMetaMigrationIdempotent(t *testing.T) {
	b := newTestBus(t)
	if err := openOther(t, b).Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("PRAGMA user_version = 11"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := migrate(b.db, 11); err != nil {
			t.Fatal(err)
		}
	}
	if g, err := resetGeneration(b.db); err != nil || g != 1 {
		t.Fatalf("generation=%d err=%v", g, err)
	}
}
