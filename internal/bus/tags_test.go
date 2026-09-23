package bus

import (
	"slices"
	"testing"
)

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{"Release", "bug", "release", "a_b-1"})
	if err != nil || !slices.Equal(got, []string{"a_b-1", "bug", "release"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"x/y"}, {"ünïcode"}, {"123456789012345678901"}} {
		if _, err := NormalizeTags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i))
	}
	wantCode(t, func() error { _, err := NormalizeTags(eleven); return err }(), "validation")
	if _, err := NormalizeTags(append(eleven[:10], "TA")); err != nil {
		t.Fatalf("duplicates are removed before the limit applies: %v", err)
	}
}

func TestSendStoresTagsAndHistorySearchFilter(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "one", Tags: []string{"Release", "bug", "release"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "two", Tags: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "three"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, func() error {
		_, err := b.Send(sam, SendInput{Channel: "dev", Content: "x", Tags: []string{"ok", "not ok"}})
		return err
	}(), "validation")
	wantCode(t, func() error {
		_, err := b.Send(sam, SendInput{Channel: "tasks", Content: "x", Tags: []string{"a"}})
		return err
	}(), "validation")

	r, err := b.Receive(kim, ReceiveInput{})
	if err != nil || len(r.Messages) != 3 || !slices.Equal(r.Messages[0].Tags, []string{"bug", "release"}) || r.Messages[2].Tags != nil {
		t.Fatalf("receive tags: %+v %v", r.Messages, err)
	}
	h, err := b.History(sam, "dev", nil, nil, 10, "docs", "release")
	if err != nil || len(h) != 2 || h[0].Content != "one" || h[1].Content != "two" {
		t.Fatalf("history any-of filter: %+v %v", h, err)
	}
	if h, _ := b.History(sam, "dev", nil, nil, 10); len(h) != 3 || !slices.Equal(h[1].Tags, []string{"docs"}) {
		t.Fatalf("history returns tags: %+v", h)
	}
	wantCode(t, func() error { _, err := b.History(sam, "dev", nil, nil, 10, "bad tag"); return err }(), "validation")
	s, err := b.Search(sam, SearchInput{Query: "one two three", Mode: "text"})
	if err != nil || len(s.Hits) != 0 {
		t.Fatalf("implicit AND: %+v %v", s.Hits, err)
	}
	s, err = b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"docs"}})
	if err != nil || len(s.Hits) != 1 || !slices.Equal(s.Hits[0].Tags, []string{"docs"}) {
		t.Fatalf("search tags filter: %+v %v", s.Hits, err)
	}
	if s, _ := b.Search(sam, SearchInput{Query: "two", Mode: "text", Tags: []string{"release"}}); len(s.Hits) != 0 {
		t.Fatalf("search filter excludes: %+v", s.Hits)
	}
}

func TestEditMemoryTagsKeepAndClear(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	r, err := b.Send(sam, SendInput{Channel: "memory", Content: "v1", Tags: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	id := *r.MemoryID
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go"}) {
		t.Fatalf("omitted tags keep the current ones: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v3", Tags: []string{"sqlite", "GO"}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); !slices.Equal(m.Tags, []string{"go", "sqlite"}) {
		t.Fatalf("replace: %+v", m)
	}
	if _, err := b.EditMemory(sam, EditInput{ID: id, Content: "v4", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.GetMemory(sam, id); m.Tags != nil {
		t.Fatalf("empty tags clear: %+v", m)
	}
	revs, _ := b.MemoryRevisions(sam, id)
	if len(revs) != 4 || !slices.Equal(revs[0].Tags, []string{"go"}) || !slices.Equal(revs[2].Tags, []string{"go", "sqlite"}) {
		t.Fatalf("tags belong to each revision: %+v", revs)
	}
	wantCode(t, func() error {
		_, err := b.EditMemory(sam, EditInput{ID: id, Content: "v5", Tags: []string{"no way"}})
		return err
	}(), "validation")
}

func TestSendTagsAreIdempotentAndCascadeOnDelete(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	a, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"B", "a"}, IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Send(sam, SendInput{Channel: "general", Content: "k", Tags: []string{"a", "b"}, IdempotencyKey: "k1"})
	if err != nil || c.Seq != a.Seq {
		t.Fatalf("normalized tags fingerprint the same: %+v %+v %v", a, c, err)
	}
	if _, err := b.db.Exec("DELETE FROM messages WHERE seq=?", a.Seq); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM message_tags WHERE seq=?", a.Seq).Scan(&n); err != nil || n != 0 {
		t.Fatalf("message_tags must cascade: %d %v", n, err)
	}
}
