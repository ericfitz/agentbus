package bus

import (
	"strings"
	"testing"
	"time"
)

func TestIdempotentSendReplaysAndConflicts(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
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
	_ = b.db.QueryRow("SELECT count(*) FROM messages").Scan(&n)
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

// Replaying an idempotency key or hitting its conflict check must not
// consume a rate-limit token: only a send that actually clears every check
// and is about to be accepted gets charged.
func TestReplayAndConflictDoNotChargeTheRateLimit(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SendMessagesPerSecond = 1
	b.limits = newLimiter(b.cfg)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	in := SendInput{Channel: "dev", Content: "once", IdempotencyKey: "k1"}
	if _, err := b.Send(sam, in); err != nil {
		t.Fatal(err)
	}
	// The one-token-per-second bucket is now empty; a replay of the same
	// key must still succeed without needing a token.
	if _, err := b.Send(sam, in); err != nil {
		t.Fatal("replay must not be rate limited:", err)
	}
	// A conflicting payload under the same key is rejected before the rate
	// limit is reached, so it must not consume a token either.
	conflicting := in
	conflicting.Content = "different"
	if _, err := b.Send(sam, conflicting); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflicting payload must error: %v", err)
	}
	// The bucket still holds only what one charged send left (0): a
	// brand-new, unkeyed send must be rate limited.
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "new"}); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatal("bucket must still be empty after exactly one charged send:", err)
	}
}

// R4: the byte bucket is charged with the real envelope size and refuses a
// send it can't afford, independent of the message-count bucket.
func TestByteBucketRateLimits(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SendKiBPerSecond = 64 // config's minimum; byteCap = max(this, max_message_kib) = 64 KiB
	b.limits = newLimiter(b.cfg)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	content := strings.Repeat("x", 45*1024) // two sends exceed the 64 KiB/s budget; one alone fits
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: content}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: content}); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatalf("second large send must be byte rate limited: %v", err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Second) }
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: content}); err != nil {
		t.Fatal("byte bucket must refill:", err)
	}
}

func TestRateLimits(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SendMessagesPerSecond = 2
	b.limits = newLimiter(b.cfg)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
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
