package bus

import (
	"strings"
	"testing"
)

func TestMemoryEditDeleteLifecycle(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
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
	b.db.QueryRow("SELECT tombstone FROM messages WHERE seq=?", c.Seq).Scan(&tomb)
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
	b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	in := EditInput{ID: *c.MemoryID, Content: "v2", IdempotencyKey: "e1"}
	e1, _ := b.EditMemory(sam, in)
	e2, _ := b.EditMemory(sam, in)
	if e1.Seq != e2.Seq {
		t.Fatal("retry manufactured a revision")
	}
	var n int
	b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
}

func TestMemoryRevisionsDeliverOnceWithCurrentContent(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	b.CreateChannel(sam, "mem", "memory")
	b.Subscribe(kim, "mem", "now")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"})
	r, _ := b.Receive(kim, ReceiveInput{})
	if len(r.Messages) != 1 || r.Messages[0].Content != "v2" || *r.Messages[0].Revision != 2 {
		t.Fatalf("superseded revision must be skipped: %+v", r.Messages)
	}
	b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v3"})
	r, _ = b.Receive(kim, ReceiveInput{Ack: r.Batch})
	if len(r.Messages) != 1 || r.Messages[0].Content != "v3" {
		t.Fatalf("later edit delivered at its own position: %+v", r.Messages)
	}
}
