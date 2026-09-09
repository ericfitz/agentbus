package bus

import (
	"strings"
	"testing"
)

func TestMemoryEditDeleteLifecycle(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	id := *c.MemoryID
	e, err := b.EditMemory(sam, EditInput{ID: id, Content: "roses are blue"})
	if err != nil || e.Revision != 2 || e.Replaced != c.Seq || e.MemoryID != id {
		t.Fatalf("%+v %v", e, err)
	}
	m, err := b.GetMemory(sam, id)
	if err != nil || m.Content != "roses are blue" || *m.Revision != 2 {
		t.Fatalf("%+v %v", m, err)
	}
	var tomb int
	_ = b.db.QueryRow("SELECT tombstone FROM messages WHERE seq=?", c.Seq).Scan(&tomb)
	if tomb != 1 {
		t.Fatal("old revision must be tombstoned")
	}
	if err := b.DeleteMemory(sam, id, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetMemory(sam, id); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatal("deleted memory must be not_found:", err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "x"}); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatal("edit of deleted memory must be not_found:", err)
	}
}

func TestMemoryEditIdempotent(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	in := EditInput{ID: *c.MemoryID, Content: "v2", IdempotencyKey: "e1"}
	e1, _ := b.EditMemory(sam, in)
	e2, _ := b.EditMemory(sam, in)
	if e1.Seq != e2.Seq {
		t.Fatal("retry manufactured a revision")
	}
	var n int
	_ = b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
}

// R2(a): a keyed edit's receipt lookup must precede the live-revision
// lookup, so a retry replays the stored result even if the memory has since
// been deleted, instead of failing not_found.
func TestEditReceiptReplaysAfterMemoryDeleted(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	in := EditInput{ID: *c.MemoryID, Content: "v2", IdempotencyKey: "e1"}
	e1, err := b.EditMemory(sam, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteMemory(sam, *c.MemoryID, ""); err != nil {
		t.Fatal(err)
	}
	e2, err := b.EditMemory(sam, in)
	if err != nil || e2 != e1 {
		t.Fatalf("keyed edit retry after delete must replay the stored result: e1=%+v e2=%+v err=%v", e1, e2, err)
	}
}

func TestMemoryRevisionsDeliverOnceWithCurrentContent(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_ = b.Subscribe(kim, "mem", "now")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	_, _ = b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"})
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 1 || r.Messages[0].Content != "v2" || *r.Messages[0].Revision != 2 {
		t.Fatalf("superseded revision must be skipped: %+v", r.Messages)
	}
	_, _ = b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v3"})
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r.Messages) != 1 || r.Messages[0].Content != "v3" {
		t.Fatalf("later edit delivered at its own position: %+v", r.Messages)
	}
}

// C1: an oversized edit must be rejected before the inspect hook or
// checkCapacity ever run, for the same reason as an oversized send.
func TestEditMemoryOversizedSkipsHookAndEviction(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	fill(t, b, sam, "dev", 20, 2000)
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := b.db.QueryRow("SELECT count(*) FROM messages").Scan(&before); err != nil {
		t.Fatal(err)
	}

	b.inspectCalls.Store(0) // fill() and the seed Send above legitimately called the hook
	b.budgetOverride = 1    // checkCapacity would evict everything it can, if reached
	_, err = b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: strings.Repeat("x", 65*1024)})
	if err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("oversized edit must be rejected as validation: %v", err)
	}
	if n := b.inspectCalls.Load(); n != 0 {
		t.Fatalf("oversized edit must not invoke the inspect hook: %d calls", n)
	}
	var after int
	if err := b.db.QueryRow("SELECT count(*) FROM messages").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("oversized edit must not trigger eviction: before=%d after=%d", before, after)
	}
}
