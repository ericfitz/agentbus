package bus

import (
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

func TestStaleOwnerLosesName(t *testing.T) {
	b := newTestBus(t)
	b.Register("Sam", "", "", true)
	if err := b.auth("Sam"); err != nil {
		t.Fatal(err)
	}
	// Another process, 31 seconds later, registers Sam.
	other2, _ := Open(b.cfg, b.log)
	defer other2.Close()
	later := b.Now().Add(31 * time.Second)
	other2.Now = func() time.Time { return later }
	r, _ := other2.Register("Sam", "", "", true)
	if r.Sender != "Sam" {
		t.Fatalf("stale Sam not reclaimed: %s", r.Sender)
	}
	if err := b.auth("Sam"); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("stale process still authorized: %v", err)
	}
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := b.auth("Sam"); err == nil {
		t.Fatal("heartbeat must not revive a lost name")
	}
}

func TestRegisterReportsResumeAndPending(t *testing.T) {
	b := newTestBus(t)
	b.db.Exec("INSERT INTO channels(name,kind,created_seq) VALUES('c','ordinary',0)")
	b.db.Exec("INSERT INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES('Sam','c',0,?)", b.nowMs())
	b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('c','Other','',1,'','hi',2)")
	r, _ := b.Register("Sam", "", "", true)
	if !r.Resumed || len(r.Pending) != 1 || r.Pending[0].Pending != 1 {
		t.Fatalf("%+v", r)
	}
	// Fresh registration drops subscriptions.
	b2, _ := Open(b.cfg, b.log)
	defer b2.Close()
	b2.Now = func() time.Time { return b.Now().Add(31 * time.Second) }
	r2, _ := b2.Register("Sam", "", "", false)
	if r2.Resumed || len(r2.Pending) != 0 {
		t.Fatalf("%+v", r2)
	}
}

func TestDiscoverListsLiveSessions(t *testing.T) {
	b := newTestBus(t)
	b.Register("Sam", "", "tmi", true)
	s, err := b.Discover("Sam")
	if err != nil || len(s) != 1 || s[0].Context != "tmi" {
		t.Fatalf("%+v %v", s, err)
	}
	b.cfg.DiscoveryEnabled = false
	if _, err := b.Discover("Sam"); err == nil {
		t.Fatal("discover must fail when disabled")
	}
}
