package bus

import "testing"

func TestTextSearchFiltersAndPaging(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	kim := reg(t, b, "Kim")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	_, _ = b.CreateChannel(sam, "ops", "ordinary")
	r1, _ := b.Send(sam, SendInput{Channel: "dev", Content: "deploy the widget service"})
	_, _ = b.Send(kim, SendInput{Channel: "dev", Content: "widget deploy failed", ReplyTo: &r1.Seq})
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "widget rollout ok"})
	_, _ = b.Send(sam, SendInput{Channel: "ops", Content: "unrelated"})

	r, err := b.Search(sam, SearchInput{Query: "widget"})
	if err != nil || len(r.Hits) != 3 || r.SemanticUnavailable {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Mode: "semantic"})
	if !r.SemanticUnavailable || len(r.Hits) != 3 {
		t.Fatalf("semantic without an endpoint must fall back to text: %+v", r)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Channel: "ops"})
	if len(r.Hits) != 1 || r.Hits[0].Content != "widget rollout ok" {
		t.Fatalf("channel filter: %+v", r.Hits)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Sender: kim})
	if len(r.Hits) != 1 {
		t.Fatalf("sender filter: %+v", r.Hits)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Thread: &r1.Seq})
	if len(r.Hits) != 2 {
		t.Fatalf("thread filter: %+v", r.Hits)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Count: 2})
	if len(r.Hits) != 2 || r.Next == "" {
		t.Fatalf("paging: %+v", r)
	}
	r, _ = b.Search(sam, SearchInput{Query: "widget", Count: 2, Cursor: r.Next})
	if len(r.Hits) != 1 || r.Next != "" {
		t.Fatalf("second page: %+v", r)
	}
	// Query syntax that would break FTS5 must be treated as plain words.
	if _, err := b.Search(sam, SearchInput{Query: `widget AND (`}); err != nil {
		t.Fatal(err)
	}
	// Tombstoned rows are invisible.
	_, _ = b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "secret widget"})
	_ = b.DeleteMemory(sam, *c.MemoryID, "")
	r, _ = b.Search(sam, SearchInput{Query: "secret"})
	if len(r.Hits) != 0 {
		t.Fatal("tombstoned memory returned")
	}
}
