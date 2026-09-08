package bus

import (
	"strings"
	"testing"
)

func reg(t *testing.T, b *Bus, name string) string {
	t.Helper()
	r, err := b.Register(name, "", "repo", true)
	if err != nil {
		t.Fatal(err)
	}
	return r.Sender
}

func TestCreateChannelIdempotentAndConflict(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal("repeat with same kind must succeed:", err)
	}
	if _, err := b.CreateChannel(sam, "dev", "memory"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal("kind mismatch must conflict:", err)
	}
	if _, err := b.CreateChannel("", "x", "ordinary"); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatal("missing as must be validation error")
	}
}

func TestSendAndHistory(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	one := int64(1)
	r1, err := b.Send(sam, SendInput{Channel: "dev", Content: "first", Refs: []Ref{{Kind: "windows_path", Value: `C:\x\y`}}})
	if err != nil || r1.Seq != 1 {
		t.Fatalf("%+v %v", r1, err)
	}
	r2, _ := b.Send(sam, SendInput{Channel: "dev", Content: "second", ReplyTo: &one, Metadata: map[string]string{"k": "v"}})
	if r2.Seq != 2 {
		t.Fatal(r2.Seq)
	}
	h, err := b.History(sam, "dev", nil, nil, 10)
	if err != nil || len(h) != 2 || h[0].Content != "first" || h[1].ReplyTo == nil || *h[1].ReplyTo != 1 || h[0].Refs[0].Value != `C:\x\y` || h[1].Metadata["k"] != "v" {
		t.Fatalf("%+v %v", h, err)
	}
	before := int64(2)
	h, _ = b.History(sam, "dev", &before, nil, 10)
	if len(h) != 1 || h[0].Seq != 1 {
		t.Fatalf("before filter: %+v", h)
	}
	ch, _ := b.ListChannels(sam)
	if len(ch) != 1 || ch[0].Messages != 2 || ch[0].LatestSeq != 2 {
		t.Fatalf("%+v", ch)
	}
}

func TestHistoryBeforeKeepsRowsNearestCursor(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	// Content sized so each envelope is ~397 bytes; with trimToBytes's
	// per-record framing allowance (#27) and History's zero reserve (C2),
	// 2 fit in a 1 KiB page and 3 don't.
	content := strings.Repeat("x", 300)
	for i := 1; i <= 5; i++ {
		if _, err := b.Send(sam, SendInput{Channel: "dev", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	b.cfg.ResultDefaultKiB = 1
	before := int64(6)
	h, err := b.History(sam, "dev", &before, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Rows nearest `before` (seq 6) must be kept, not the oldest ones.
	if len(h) != 2 || h[0].Seq != 4 || h[1].Seq != 5 {
		t.Fatalf("expected rows adjacent to before=6 ([4 5]), got %+v", h)
	}
}

func TestSendValidation(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	cases := []SendInput{
		{Channel: "nope", Content: "x"},
		{Channel: "dev", Content: ""},
		{Channel: "dev", Content: strings.Repeat("x", 65*1024)},
		{Channel: "dev", Content: "x", Refs: []Ref{{Kind: "ftp", Value: "v"}}},
		{Channel: "dev", Content: "x", ReplyTo: new(int64)},
	}
	for i, c := range cases {
		if _, err := b.Send(sam, c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestSendOnMemoryChannelCreatesMemory(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	r, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	if err != nil || r.MemoryID == nil || *r.MemoryID != r.Seq {
		t.Fatalf("%+v %v", r, err)
	}
	h, _ := b.History(sam, "mem", nil, nil, 10)
	if h[0].Revision == nil || *h[0].Revision != 1 {
		t.Fatalf("%+v", h[0])
	}
}

// R2(b): a keyed send's receipt lookup must precede the reply_to lookup, so
// a retry replays the stored result even if the message it replied to has
// since been evicted, instead of failing not_found.
func TestSendReceiptReplaysAfterReplyToEvicted(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	base, err := b.Send(sam, SendInput{Channel: "dev", Content: "base"})
	if err != nil {
		t.Fatal(err)
	}
	in := SendInput{Channel: "dev", Content: "reply", ReplyTo: &base.Seq, IdempotencyKey: "k1"}
	r1, err := b.Send(sam, in)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate capacity eviction of the replied-to row.
	if _, err := b.db.Exec("DELETE FROM messages WHERE seq=?", base.Seq); err != nil {
		t.Fatal(err)
	}
	r2, err := b.Send(sam, in)
	if err != nil || r2.Seq != r1.Seq {
		t.Fatalf("keyed retry after reply_to eviction must replay the stored result: r1=%+v r2=%+v err=%v", r1, r2, err)
	}
}

// R4: max_message_kib must be checked against the real registered context,
// not a small placeholder, so a long context that pushes the envelope over
// the limit is rejected even though the content alone would fit.
func TestSendRejectsWhenContextPushesEnvelopeOverLimit(t *testing.T) {
	b := newTestBus(t)
	r, err := b.Register("Sam", "", strings.Repeat("c", 70000), true)
	if err != nil {
		t.Fatal(err)
	}
	sam := r.Sender
	b.CreateChannel(sam, "dev", "ordinary")
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "short"}); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("send must reject when the registered context pushes the envelope over max_message_kib: %v", err)
	}
}

// C1: an oversized send must be rejected before the inspect hook or
// checkCapacity ever run, so it can neither trigger the hook nor evict
// ordinary messages to make room for a write that will be rejected anyway.
func TestSendOversizedSkipsHookAndEviction(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	fill(t, b, sam, "dev", 20, 2000)
	var before int
	if err := b.db.QueryRow("SELECT count(*) FROM messages").Scan(&before); err != nil {
		t.Fatal(err)
	}

	b.inspectCalls.Store(0) // fill() above legitimately called the hook; only count from here
	b.budgetOverride = 1    // checkCapacity would evict everything it can, if reached
	_, err := b.Send(sam, SendInput{Channel: "dev", Content: strings.Repeat("x", 65*1024)})
	if err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("oversized send must be rejected as validation: %v", err)
	}
	if n := b.inspectCalls.Load(); n != 0 {
		t.Fatalf("oversized send must not invoke the inspect hook: %d calls", n)
	}
	var after int
	if err := b.db.QueryRow("SELECT count(*) FROM messages").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("oversized send must not trigger eviction: before=%d after=%d", before, after)
	}
}
