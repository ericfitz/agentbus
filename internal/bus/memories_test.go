package bus

import (
	"strings"
	"testing"
	"time"
)

func TestMemoryEditDeleteLifecycle(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	id := *c.MemoryID
	accessedAt := func() (int64, bool) {
		var at int64
		err := b.db.QueryRow("SELECT accessed_at FROM memory_access WHERE memory_id=?", id).Scan(&at)
		return at, err == nil
	}
	// send (create) sets memory_access (ADR 0013).
	created, ok := accessedAt()
	if !ok {
		t.Fatal("send must set memory_access for a new memory")
	}
	b.Now = func() time.Time { return time.Now().Add(time.Minute) }
	e, err := b.EditMemory(sam, EditInput{ID: id, Content: "roses are blue"})
	if err != nil || e.Revision != 2 || e.Replaced != c.Seq || e.MemoryID != id {
		t.Fatalf("%+v %v", e, err)
	}
	// edit_memory bumps memory_access (ADR 0013).
	edited, ok := accessedAt()
	if !ok || edited <= created {
		t.Fatalf("edit_memory must bump accessed_at: created=%d edited=%d ok=%v", created, edited, ok)
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
	// delete_memory drops the memory_access row (ADR 0013).
	if _, ok := accessedAt(); ok {
		t.Fatal("delete_memory must drop the memory_access row")
	}
	if _, err := b.GetMemory(sam, id); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatal("deleted memory must be not_found:", err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "x"}); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatal("edit of deleted memory must be not_found:", err)
	}
}

// TestTaskCreateGetsNoMemoryAccessRow (ADR 0013): a task, unlike a memory,
// never gets a memory_access row; the task-channel expiry rule governs it
// instead.
func TestTaskCreateGetsNoMemoryAccessRow(t *testing.T) {
	b := newTestBus(t)
	reg(t, b, "Sam")
	taskList(t, b)
	tk := mustCreate(t, b, TaskCreateInput{Subject: "x"})
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM memory_access WHERE memory_id=?", tk.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("task_create must not add a memory_access row: %d %v", n, err)
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

func TestMemoryRevisionsListsAllRevisionsOldestFirst(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	revs, err := b.MemoryRevisions(sam, *c.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 || revs[0].Content != "v1" || revs[1].Content != "v2" || *revs[0].Revision != 1 || *revs[1].Revision != 2 {
		t.Fatalf("want [v1 r1, v2 r2], got %+v", revs)
	}
	if _, err := b.MemoryRevisions(sam, 999999); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("want not_found, got %v", err)
	}
}

// TestTouchMemoryAccessUpdatesOnlyExistingRows (ADR 0013): TouchMemoryAccess
// bumps accessed_at for a memory that already has a row, and never inserts
// one for an id with none — in particular a task's memory_id, which must
// never gain a memory_access row (an insert here would let memory expiry
// delete tasks).
func TestTouchMemoryAccessUpdatesOnlyExistingRows(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.CreateChannel(sam, "tasks/work", "memory")
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks/work", Subject: "ship it"})

	var before int64
	if err := b.db.QueryRow("SELECT accessed_at FROM memory_access WHERE memory_id=?", *c.MemoryID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Hour) }
	// tk.ID is included alongside a real memory id in the same batched call:
	// the task id must be silently ignored, not error, and must not create
	// a row.
	if err := b.TouchMemoryAccess([]int64{*c.MemoryID, tk.ID}); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := b.db.QueryRow("SELECT accessed_at FROM memory_access WHERE memory_id=?", *c.MemoryID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("TouchMemoryAccess must bump an existing row's accessed_at: before=%d after=%d", before, after)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM memory_access WHERE memory_id=?", tk.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("TouchMemoryAccess must never create a memory_access row for a task")
	}
	if err := b.TouchMemoryAccess(nil); err != nil {
		t.Fatalf("an empty id list must be a no-op, not an error: %v", err)
	}
}

// TestBusReadPathsNeverTouchMemoryAccess (ADR 0013): GetMemory, Search,
// History, and Receive, called directly on the bus the way the TUI and CLI
// do, must never move accessed_at. The mcpserver tool handlers are the only
// callers that touch it (see internal/mcpserver's own tests), via
// TouchMemoryAccess after get_memory and search results are known.
func TestBusReadPathsNeverTouchMemoryAccess(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, err := b.Send(sam, SendInput{Channel: "mem", Content: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := b.db.QueryRow("SELECT accessed_at FROM memory_access WHERE memory_id=?", *c.MemoryID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := b.GetMemory(sam, *c.MemoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Search(sam, SearchInput{Query: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.History(sam, "mem", nil, nil, 10); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(sam, "mem", "oldest"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(sam, ReceiveInput{}); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := b.db.QueryRow("SELECT accessed_at FROM memory_access WHERE memory_id=?", *c.MemoryID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("Bus GetMemory/Search/History/Receive must never touch memory_access: before=%d after=%d", before, after)
	}
}
