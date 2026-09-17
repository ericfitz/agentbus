package bus

import (
	"strings"
	"testing"
	"time"
)

// filterDM, filterDMPending, and filterDMChannels drop DM inbox entries from
// results, so tests written before every register() minted an inbox don't
// need per-test knowledge of it to keep asserting what they always asserted.
func filterDM(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := dmOwner(n); !ok {
			out = append(out, n)
		}
	}
	return out
}

func filterDMPending(ps []PendingChannel) []PendingChannel {
	out := make([]PendingChannel, 0, len(ps))
	for _, p := range ps {
		if _, ok := dmOwner(p.Channel); !ok {
			out = append(out, p)
		}
	}
	return out
}

func filterDMChannels(cs []Channel) []Channel {
	out := make([]Channel, 0, len(cs))
	for _, c := range cs {
		if _, ok := dmOwner(c.Name); !ok {
			out = append(out, c)
		}
	}
	return out
}

func TestDMOwner(t *testing.T) {
	for ch, want := range map[string]string{"dm/Sam": "Sam", "dm/Sam/impl": "Sam/impl"} {
		if got, ok := dmOwner(ch); !ok || got != want {
			t.Fatalf("dmOwner(%q)=%q,%v", ch, got, ok)
		}
	}
	for _, ch := range []string{"dm", "dm/", "general", "xdm/Sam"} {
		if _, ok := dmOwner(ch); ok {
			t.Fatalf("%q must not be a DM channel", ch)
		}
	}
	if DMChannel("Sam") != "dm/Sam" {
		t.Fatal(DMChannel("Sam"))
	}
}

func TestRegisterCreatesInboxIdempotently(t *testing.T) {
	b := newTestBus(t)
	for i, wantResumed := range []bool{false, true} {
		r, err := b.Register("Sam", "", "repo", true)
		if err != nil {
			t.Fatal(err)
		}
		// The inbox created by this very call must not itself count as a
		// resume: only a genuinely prior registration should.
		if r.Resumed != wantResumed {
			t.Fatalf("register %d: resumed=%v want %v: %+v", i, r.Resumed, wantResumed, r)
		}
		found := false
		for _, p := range r.Pending {
			found = found || p.Channel == "dm/Sam"
		}
		if !found {
			t.Fatalf("register must report the inbox in pending: %+v", r.Pending)
		}
	}
	var chans, subs int
	_ = b.db.QueryRow("SELECT count(*) FROM channels WHERE name='dm/Sam' AND kind='ordinary'").Scan(&chans)
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&subs)
	if chans != 1 || subs != 1 {
		t.Fatalf("channels=%d subs=%d", chans, subs)
	}
	// resume=false drops and recreates the inbox row at the current head.
	if _, err := b.Register("Sam", "", "repo", false); err != nil {
		t.Fatal(err)
	}
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&subs)
	if subs != 1 {
		t.Fatalf("resume=false must still leave the identity with an inbox subscription, got %d", subs)
	}
}

// TestResumeFalseSkipsWholeBacklogIncludingInbox reproduces the human
// decision of 2026-09-17 (ADR 0004): resume=false means no backlog of any
// kind, inbox included — not from the cursor, not from the beginning, only
// messages that arrive after the register.
func TestResumeFalseSkipsWholeBacklogIncludingInbox(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Send("Sam", SendInput{Channel: "dm/Pat", Content: "DM1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Send("Sam", SendInput{Channel: "dm/Pat", Content: "DM2"}); err != nil {
		t.Fatal(err)
	}
	r, err := b.Register("Pat", "", "repo", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range r.Pending {
		if p.Channel == "dm/Pat" {
			found = true
			if p.Pending != 0 {
				t.Fatalf("resume=false must report no backlog for the inbox: %+v", r.Pending)
			}
		}
	}
	if !found {
		t.Fatalf("register must still report the inbox in pending: %+v", r.Pending)
	}
	res, err := b.Receive("Pat", ReceiveInput{})
	if err != nil || len(res.Messages) != 0 {
		t.Fatalf("resume=false must not deliver anything queued before the register: %+v %v", res, err)
	}
	if _, err := other.Send("Sam", SendInput{Channel: "dm/Pat", Content: "DM3"}); err != nil {
		t.Fatal(err)
	}
	res2, err := b.Receive("Pat", ReceiveInput{})
	if err != nil || len(res2.Messages) != 1 || res2.Messages[0].Content != "DM3" {
		t.Fatalf("only a message sent after the register must be delivered: %+v %v", res2, err)
	}
}

func TestDMDeliveryAndQueueWhileOffline(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, _ := Open(b.cfg, b.log)
	defer func() { _ = other.Close() }()
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	// Pat goes away; the inbox persists and queues.
	if err := other.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "rename landed"}); err != nil {
		t.Fatalf("DM to a known offline identity must queue: %v", err)
	}
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	res, err := other.Receive("Pat", ReceiveInput{})
	if err != nil || len(res.Messages) != 1 || res.Messages[0].Content != "rename landed" || res.Messages[0].Sender != "Sam" {
		t.Fatalf("%+v %v", res, err)
	}
	// A reply is a DM back, optionally naming the original.
	seq := res.Messages[0].Seq
	if _, err := other.Send("Pat", SendInput{Channel: "dm/Sam", Content: "thanks", ReplyTo: &seq}); err != nil {
		t.Fatalf("cross-channel reply_to must be accepted: %v", err)
	}
}

func TestDMToUnknownIdentityIsNotFound(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	_, err := b.Send("Sam", SendInput{Channel: "dm/Nobody", Content: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not_found") || !strings.Contains(err.Error(), "never registered") {
		t.Fatalf("got %v", err)
	}
}

func TestChannelNameDMIsReserved(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel("Sam", "dm", "ordinary"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("create_channel dm: %v", err)
	}
	if err := b.EnsureChannel("dm", "ordinary"); err == nil {
		t.Fatal("EnsureChannel dm must fail")
	}
}

// twoAgents registers Sam on b and Pat on a second process.
func twoAgents(t *testing.T) (b, other *Bus) {
	t.Helper()
	b = newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	other, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	return b, other
}

func TestDMGuards(t *testing.T) {
	b, other := twoAgents(t)
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "secret zebra"}); err != nil {
		t.Fatal(err)
	}
	wantErr := func(name, kind string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), kind) {
			t.Fatalf("%s: want %s, got %v", name, kind, err)
		}
	}
	wantErr("subscribe other", "validation", b.Subscribe("Sam", "dm/Pat", "now"))
	wantErr("subscribe own", "validation", other.Subscribe("Pat", "dm/Pat", "now"))
	wantErr("unsubscribe own", "validation", other.Unsubscribe("Pat", "dm/Pat"))
	_, err := b.DeleteChannel("dm/Pat", "")
	wantErr("delete", "validation", err)
	_, errExisting := b.History("Sam", "dm/Pat", nil, nil, 10)
	wantErr("history by sender", "not_found", errExisting)
	_, errUnknown := b.History("Sam", "dm/Nobody", nil, nil, 10)
	wantErr("history of unknown inbox", "not_found", errUnknown)
	if errExisting.Error() != errUnknown.Error() {
		t.Fatalf("history must return identical errors for an existing and a never-registered inbox, so existence does not leak: %v vs %v", errExisting, errUnknown)
	}
	_, err = b.Search("Sam", SearchInput{Query: "zebra", Channel: "dm/Pat", Mode: "text"})
	wantErr("scoped search by sender", "not_found", err)
	res, err := b.Search("Sam", SearchInput{Query: "zebra", Mode: "text"})
	if err != nil || len(res.Hits) != 0 {
		t.Fatalf("unscoped search must not leak another inbox: %+v %v", res, err)
	}
	// The owner can read its own inbox both ways.
	if ms, err := other.History("Pat", "dm/Pat", nil, nil, 10); err != nil || len(ms) != 1 {
		t.Fatalf("%+v %v", ms, err)
	}
	if res, err := other.Search("Pat", SearchInput{Query: "zebra", Mode: "text"}); err != nil || len(res.Hits) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	chans, _ := b.ListChannels("Sam")
	for _, c := range chans {
		if _, ok := dmOwner(c.Name); ok {
			t.Fatalf("list_channels must omit DM channels: %s", c.Name)
		}
	}
}

func TestObserverReadsEveryInbox(t *testing.T) {
	b, _ := twoAgents(t)
	if _, err := b.Send("Sam", SendInput{Channel: "dm/Pat", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	b.SetObserver("Sam")
	if err := b.Subscribe("Sam", "dm/Pat", "oldest"); err != nil {
		t.Fatalf("observer subscribe: %v", err)
	}
	if ms, err := b.History("Sam", "dm/Pat", nil, nil, 10); err != nil || len(ms) != 1 {
		t.Fatalf("observer history: %+v %v", ms, err)
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range st.Channels {
		found = found || c.Name == "dm/Pat"
	}
	if !found {
		t.Fatal("StatusReport must list DM channels for the TUI")
	}
}

func TestInboxSurvivesReaperAndIdleExpiry(t *testing.T) {
	b := newTestBus(t)
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	idle := int64(b.cfg.CursorIdleHours)*3_600_000 + 1
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=? WHERE sender='Sam'", b.nowMs()-idle); err != nil {
		t.Fatal(err)
	}
	res, err := b.Receive("Sam", ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range res.Expired {
		if ch == "dm/Sam" {
			t.Fatal("the inbox subscription must never idle-expire")
		}
	}
	// Session gone, inbox empty: the reaper must still keep it.
	if err := b.EndSessions(); err != nil {
		t.Fatal(err)
	}
	if err := b.reapEmptyChannels(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM channels WHERE name='dm/Sam'").Scan(&n)
	if n != 1 {
		t.Fatal("reaper dropped an inbox")
	}
	// Resume after idling: register's purge must keep the inbox subscription.
	if _, err := b.db.Exec("UPDATE subscriptions SET last_activity=0 WHERE sender='Sam'"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("Sam", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&n)
	if n != 1 {
		t.Fatal("register purge dropped the inbox subscription")
	}
}

// TestDeliveryHonorsDMReadableAfterObserverEndsSession reproduces F1: once
// the TUI's session ends, its subscription to another identity's inbox
// (minted while it was the observer) persists in the subscriptions table,
// but a later, non-observer process registering the same name must not be
// able to read through it via receive, wait, or register's pending list.
func TestDeliveryHonorsDMReadableAfterObserverEndsSession(t *testing.T) {
	a := newTestBus(t)
	if _, err := a.Register("human", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	a.SetObserver("human")
	pat, err := Open(a.cfg, a.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pat.Close() })
	if _, err := pat.Register("Pat", "", "repo", true); err != nil {
		t.Fatal(err)
	}
	if err := a.Subscribe("human", "dm/Pat", "oldest"); err != nil {
		t.Fatalf("observer subscribe: %v", err)
	}
	if _, err := pat.Send("Pat", SendInput{Channel: "dm/Pat", Content: "secret for Pat"}); err != nil {
		t.Fatal(err)
	}
	if err := a.EndSessions(); err != nil {
		t.Fatal(err)
	}

	// A third, non-observer process resumes the "human" identity.
	c, err := Open(a.cfg, a.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	reg, err := c.Register("human", "", "repo", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.Pending {
		if p.Channel == "dm/Pat" {
			t.Fatalf("register pending must not list another identity's inbox once the observer session has ended: %+v", reg.Pending)
		}
	}
	res, err := c.Receive("human", ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Messages {
		if m.Channel == "dm/Pat" {
			t.Fatalf("receive must not deliver another identity's inbox: %+v", res.Messages)
		}
	}
	msgs, err := c.Wait("human", nil, false, nil, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Channel == "dm/Pat" {
			t.Fatalf("wait must not return another identity's inbox: %+v", msgs)
		}
	}
}
