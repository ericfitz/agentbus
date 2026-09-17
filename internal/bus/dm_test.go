package bus

import (
	"strings"
	"testing"
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
	// resume=false drops subscriptions but the inbox subscription comes back.
	if _, err := b.Register("Sam", "", "repo", false); err != nil {
		t.Fatal(err)
	}
	_ = b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender='Sam' AND channel='dm/Sam'").Scan(&subs)
	if subs != 1 {
		t.Fatalf("resume=false must recreate the inbox subscription, got %d", subs)
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
