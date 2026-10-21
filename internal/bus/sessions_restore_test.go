package bus

import (
	"database/sql"
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

// TestMetaMigrationIdempotent opens a v11 file (no meta table) twice: the
// first open creates meta and stamps 12, the second changes nothing.
func TestMetaMigrationIdempotent(t *testing.T) {
	b := newTestBus(t)
	cfg := b.cfg
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	dsn, err := SQLiteDSN(cfg.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE meta"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 11"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		o, err := Open(cfg, b.log)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var uv, has int
		if err := o.db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil || uv != 12 {
			t.Fatalf("open %d: user_version=%d err=%v", i, uv, err)
		}
		if err := o.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'").Scan(&has); err != nil || has != 1 {
			t.Fatalf("open %d: meta tables=%d err=%v", i, has, err)
		}
		if g, err := resetGeneration(o.db); err != nil || g != 0 {
			t.Fatalf("open %d: generation=%d err=%v", i, g, err)
		}
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A reset that commits after the process remembered its row (here: the
// generation bumped directly) must stop the restore in the INSERT itself,
// leaving no row and no entry. The interleaving between a separate read and
// write cannot be driven without a hook; this checks the single statement.
func TestRestoreInsertCarriesGenerationCheck(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	r, ok := b.rememberedRow("repo")
	if !ok {
		t.Fatal("not remembered")
	}
	sleepSweep(t, b)
	if _, err := b.db.Exec("INSERT INTO meta(key,value) VALUES('reset_generation',1) ON CONFLICT(key) DO UPDATE SET value=value+1"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.restore(b.db, r); err != nil || got {
		t.Fatalf("restore = %v, %v; want false, nil", got, err)
	}
	if _, _, _, found := sessionRowFor(t, b, "repo"); found {
		t.Fatal("row restored across a reset")
	}
	if _, ok := b.rememberedRow("repo"); ok {
		t.Fatal("entry not forgotten")
	}
}

// Heartbeat snapshots the entries, then EndSessions clears them; a restore
// that runs after must not leave a row for the ended session.
func TestRestoreAfterEndSessionsLeavesNoRow(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("repo", "", "ctx", true); err != nil {
		t.Fatal(err)
	}
	r, _ := b.rememberedRow("repo")
	if err := b.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if got, err := b.restore(b.db, r); err != nil || got {
		t.Fatalf("restore = %v, %v; want false, nil", got, err)
	}
	if _, _, _, found := sessionRowFor(t, b, "repo"); found {
		t.Fatal("restore left a row for an ended session")
	}
}

// One name failing to restore must not keep Heartbeat from restoring the
// others, and the error is still reported.
func TestHeartbeatRestoresOthersAfterAFailure(t *testing.T) {
	b := newTestBus(t)
	for _, n := range []string{"bad", "good"} {
		if _, err := b.Register(n, "", "ctx", true); err != nil {
			t.Fatal(err)
		}
	}
	sleepSweep(t, b)
	if _, err := b.db.Exec("CREATE TRIGGER fail_bad BEFORE INSERT ON sessions WHEN NEW.sender='bad' BEGIN SELECT RAISE(ABORT,'boom'); END"); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(); err == nil {
		t.Fatal("Heartbeat swallowed the restore error")
	}
	if _, _, _, ok := sessionRowFor(t, b, "good"); !ok {
		t.Fatal("good was not restored after bad failed")
	}
}
