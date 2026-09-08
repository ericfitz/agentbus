package bus

import (
	"strings"
	"testing"
)

// TestErrorMessagesAreBoundedRegardlessOfCallerInput reproduces F1: an
// oversized caller-controlled string interpolated into an error message
// (a cursor, a channel name, an idempotency key) must not make the
// rendered error itself exceed the result budgets it is supposed to be
// bounded by.
func TestErrorMessagesAreBoundedRegardlessOfCallerInput(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	big := strings.Repeat("x", 5*1024*1024) // >4 MiB

	// search.go:139 — invalid cursor.
	if _, err := b.Search(sam, SearchInput{Query: "widget", Cursor: big}); err == nil {
		t.Fatal("expected an error for a non-numeric oversized cursor")
	} else if n := len(err.Error()); n > 2*MaxErrorMessageBytes {
		t.Fatalf("oversized cursor error is %d bytes, want <= ~1 KiB", n)
	}

	// messages.go:118 — channel does not exist.
	if _, err := b.Send(sam, SendInput{Channel: big, Content: "hi"}); err == nil {
		t.Fatal("expected an error for a nonexistent oversized channel")
	} else if n := len(err.Error()); n > 2*MaxErrorMessageBytes {
		t.Fatalf("oversized channel error is %d bytes, want <= ~1 KiB", n)
	}

	// receipts.go:48 — idempotency key reused with a different payload.
	b.CreateChannel(sam, "dev", "ordinary")
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "first", IdempotencyKey: big}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "second", IdempotencyKey: big}); err == nil {
		t.Fatal("expected a conflict error for a reused oversized key with a different payload")
	} else if n := len(err.Error()); n > 2*MaxErrorMessageBytes {
		t.Fatalf("oversized key conflict error is %d bytes, want <= ~1 KiB", n)
	}
}
