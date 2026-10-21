package bus

import (
	"errors"
	"strings"
	"testing"
)

// A DM to your own inbox is stored where receive and wait skip your own
// messages, so it would never be delivered (#42). It is rejected in the
// shape check, before the receipt lookup and the rate charge, and leaves
// nothing behind.
func TestSendToOwnInboxIsRejected(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SendMessagesPerSecond = 1
	b.limits = newLimiter(b.cfg)
	sam := reg(t, b, "Sam")
	count := func(table string) (n int) {
		t.Helper()
		if err := b.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	_, err := b.Send(sam, SendInput{Channel: "dm/Sam", Content: "note to self", IdempotencyKey: "k1"})
	var be *Error
	if !errors.As(err, &be) || be.Code != "validation" || !strings.Contains(be.Message, "cannot send a direct message to yourself; dm/Sam is your own inbox") {
		t.Fatalf("want validation naming the own inbox, got %v", err)
	}
	if n := count("messages"); n != 0 {
		t.Fatalf("rejected self-DM stored %d messages", n)
	}
	if n := count("receipts"); n != 0 {
		t.Fatalf("rejected self-DM wrote %d receipts", n)
	}
	// The one-token bucket is untouched, so a real send still succeeds.
	if _, err := b.Send(sam, SendInput{Channel: "general", Content: "hello"}); err != nil {
		t.Fatalf("rejected self-DM must not be rate-charged: %v", err)
	}
}

// The owner match is exact: a parent and its subagent have different
// inboxes, so each may message the other.
func TestSendToParentOrSubagentInboxIsAllowed(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	r, err := b.Register("impl", "Sam", "repo", true)
	if err != nil || r.Sender != "Sam/impl" {
		t.Fatalf("subagent registration: %+v %v", r, err)
	}
	if _, err := b.Send("Sam/impl", SendInput{Channel: "dm/Sam", Content: "done"}); err != nil {
		t.Fatalf("subagent to parent: %v", err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dm/Sam/impl", Content: "next"}); err != nil {
		t.Fatalf("parent to subagent: %v", err)
	}
}
