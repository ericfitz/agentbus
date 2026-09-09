package bus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRegisterAllocatesSuffixes(t *testing.T) {
	b := newTestBus(t)
	r1, err := b.Register("Sam", "", "repo", true)
	if err != nil || r1.Sender != "Sam" {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := b.Register("Sam", "", "repo", true)
	if r2.Sender != "Sam2" {
		t.Fatal(r2.Sender)
	}
	c1, _ := b.Register("implement804", "Sam2", "repo", true)
	c2, _ := b.Register("implement804", "Sam2", "repo", true)
	if c1.Sender != "Sam2/implement804" || c2.Sender != "Sam2/implement804-2" {
		t.Fatal(c1.Sender, c2.Sender)
	}
}

func TestRegisterRejectsBadNames(t *testing.T) {
	b := newTestBus(t)
	for _, n := range []string{"", "a/b", "x\ny", strings.Repeat("z", 129)} {
		if _, err := b.Register(n, "", "", true); err == nil {
			t.Fatalf("accepted %q", n)
		}
	}
}

// M12.2: context is bounded to 1024 bytes so one register call cannot make
// discover/list results exceed the 4 MiB hard ceiling.
func TestRegisterRejectsOversizedContext(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", strings.Repeat("c", 1024), true); err != nil {
		t.Fatalf("1024-byte context must be accepted: %v", err)
	}
	if _, err := b.Register("Kim", "", strings.Repeat("c", 1025), true); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("1025-byte context must be rejected as validation: %v", err)
	}
}

// M12.2 amendment: parent is a display name, so it may contain '/' (unlike
// name), but is bounded to 512 bytes with no control characters.
func TestRegisterRejectsOversizedOrControlParent(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("impl", "Sam/team", "", true); err != nil {
		t.Fatalf("parent containing '/' must be accepted: %v", err)
	}
	if _, err := b.Register("impl", strings.Repeat("p", 512), "", true); err != nil {
		t.Fatalf("512-byte parent must be accepted: %v", err)
	}
	if _, err := b.Register("impl", strings.Repeat("p", 513), "", true); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("513-byte parent must be rejected as validation: %v", err)
	}
	if _, err := b.Register("impl", "bad\x00parent", "", true); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("control character in parent must be rejected as validation: %v", err)
	}
}

func TestStaleOwnerLosesName(t *testing.T) {
	b := newTestBus(t)
	_, _ = b.Register("Sam", "", "", true)
	if err := b.auth(b.db, "Sam"); err != nil {
		t.Fatal(err)
	}
	// Another process, 31 seconds later, registers Sam.
	other2, _ := Open(b.cfg, b.log)
	defer func() { _ = other2.Close() }()
	later := b.Now().Add(31 * time.Second)
	other2.Now = func() time.Time { return later }
	r, _ := other2.Register("Sam", "", "", true)
	if r.Sender != "Sam" {
		t.Fatalf("stale Sam not reclaimed: %s", r.Sender)
	}
	if err := b.auth(b.db, "Sam"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("stale process still authorized: %v", err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := b.auth(b.db, "Sam"); err == nil {
		t.Fatal("heartbeat must not revive a lost name")
	}
}

// R3: every mutating operation must re-verify ownership on the transaction
// that writes, not rely solely on a preflight check, so a process that has
// lost its name to a newer registration (after a 30s heartbeat lapse)
// cannot keep mutating state as the replaced owner.
func TestMutationsRejectAfterOwnerTakeover(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(sam, "dev", "now"); err != nil {
		t.Fatal(err)
	}

	// Another process, 31 seconds later, reclaims the stale "Sam" name.
	other, _ := Open(b.cfg, b.log)
	defer func() { _ = other.Close() }()
	later := b.Now().Add(31 * time.Second)
	other.Now = func() time.Time { return later }
	if r, err := other.Register("Sam", "", "", true); err != nil || r.Sender != "Sam" {
		t.Fatalf("takeover failed: %+v %v", r, err)
	}

	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "x"}); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("Send after takeover must be not_registered: %v", err)
	}
	if _, err := b.Receive(sam, ReceiveInput{}); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("Receive after takeover must be not_registered: %v", err)
	}
	if err := b.Subscribe(sam, "dev", "now"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("Subscribe after takeover must be not_registered: %v", err)
	}
}

func TestRegisterReportsResumeAndPending(t *testing.T) {
	b := newTestBus(t)
	_, _ = b.db.Exec("INSERT INTO channels(name,kind,created_seq) VALUES('c','ordinary',0)")
	_, _ = b.db.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('Sam','c',0,?)", b.nowMs())
	_, _ = b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('c','Other','',1,'','hi',2)")
	r, _ := b.Register("Sam", "", "", true)
	if !r.Resumed || len(r.Pending) != 1 || r.Pending[0].Pending != 1 {
		t.Fatalf("%+v", r)
	}
	// Fresh registration drops subscriptions.
	b2, _ := Open(b.cfg, b.log)
	defer func() { _ = b2.Close() }()
	b2.Now = func() time.Time { return b.Now().Add(31 * time.Second) }
	r2, _ := b2.Register("Sam", "", "", false)
	if r2.Resumed || len(r2.Pending) != 0 {
		t.Fatalf("%+v", r2)
	}
}

// T10.4: Register(resume=true) must drop this name's idle-expired
// subscriptions before listing pending channels, since the tick no longer
// reaps them itself.
func TestRegisterResumePurgesExpiredSubscriptions(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(sam, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Duration(b.cfg.CursorIdleHours+1) * time.Hour) }
	r, err := b.Register("Sam", "", "repo", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Pending {
		if p.Channel == "dev" {
			t.Fatalf("expired subscription must not appear as pending: %+v", r.Pending)
		}
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=? AND channel='dev'", sam).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("expired subscription row must be deleted on resume")
	}
}

func TestDiscoverListsLiveSessions(t *testing.T) {
	b := newTestBus(t)
	_, _ = b.Register("Sam", "", "tmi", true)
	s, err := b.Discover("Sam")
	if err != nil || len(s) != 1 || s[0].Context != "tmi" {
		t.Fatalf("%+v %v", s, err)
	}
	b.cfg.DiscoveryEnabled = false
	if _, err := b.Discover("Sam"); err == nil {
		t.Fatal("discover must fail when disabled")
	}
}

// M12.2: Discover must trim to a whole-record prefix against
// result_default_kib rather than returning an unbounded array.
func TestDiscoverTrimsToWholeRecordPrefix(t *testing.T) {
	b := newTestBus(t)
	b.cfg.ResultDefaultKiB = 1 // 1 KiB
	var as string
	for i := 0; i < 30; i++ {
		r, err := b.Register("Sam", "", strings.Repeat("c", 60), true)
		if err != nil {
			t.Fatal(err)
		}
		as = r.Sender
	}
	s, err := b.Discover(as)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) == 0 || len(s) >= 30 {
		t.Fatalf("want a whole-record prefix strictly between 0 and 30, got %d", len(s))
	}
	j, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(j) > b.cfg.ResultDefaultKiB*1024+trimFramingBytes {
		t.Fatalf("serialized discover result (%d bytes) exceeds result_default_kib plus one record's framing", len(j))
	}
}
