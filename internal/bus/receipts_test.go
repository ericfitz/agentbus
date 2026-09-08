package bus

import (
	"strings"
	"testing"
	"time"
)

func TestIdempotentSendReplaysAndConflicts(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	in := SendInput{Channel: "dev", Content: "once", IdempotencyKey: "k1", Metadata: map[string]string{"b": "2", "a": "1"}}
	r1, err := b.Send(sam, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Metadata = map[string]string{"a": "1", "b": "2"} // same payload, different key order
	r2, err := b.Send(sam, in)
	if err != nil || r2.Seq != r1.Seq {
		t.Fatalf("replay must return original: %+v %v", r2, err)
	}
	var n int
	b.db.QueryRow("SELECT count(*) FROM messages").Scan(&n)
	if n != 1 {
		t.Fatal("replay inserted a row")
	}
	in.Content = "changed"
	if _, err := b.Send(sam, in); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal("changed payload must conflict:", err)
	}
	// Expired receipt: same key becomes a new send.
	b.Now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	r3, err := b.Send(sam, in)
	if err != nil || r3.Seq == r1.Seq {
		t.Fatalf("expired receipt must allow new send: %+v %v", r3, err)
	}
}

func TestRateLimits(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SendMessagesPerSecond = 2
	b.limits = newLimiter(b.cfg)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	for i := 0; i < 2; i++ {
		if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "x"}); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatal("third send in one second must be rate limited:", err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Second) }
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "x"}); err != nil {
		t.Fatal("bucket must refill:", err)
	}
}
