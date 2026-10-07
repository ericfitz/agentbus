package bus

import (
	"strings"
	"testing"
	"time"
)

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

func TestSearchRejectsInvalidModeAndCursor(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.Search(sam, SearchInput{Query: "x", Mode: "bogus"}); err == nil || !strings.Contains(err.Error(), "mode must be") {
		t.Fatalf("invalid mode must be rejected: %v", err)
	}
	if _, err := b.Search(sam, SearchInput{Query: "x", Cursor: "not-a-number"}); err == nil || !strings.Contains(err.Error(), "invalid cursor") {
		t.Fatalf("non-numeric cursor must be rejected: %v", err)
	}
	if _, err := b.Search(sam, SearchInput{Query: "x", Cursor: "-1"}); err == nil || !strings.Contains(err.Error(), "invalid cursor") {
		t.Fatalf("negative cursor must be rejected: %v", err)
	}
}

func TestSearchFiltersBySinceAndUntilAndOrdersByScore(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	b.Now = func() time.Time { return time.UnixMilli(1000) }
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "widget one"})
	b.Now = func() time.Time { return time.UnixMilli(2000) }
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "widget widget two"})
	b.Now = func() time.Time { return time.UnixMilli(3000) }
	_, _ = b.Send(sam, SendInput{Channel: "dev", Content: "widget three"})

	since := int64(1500)
	if r, err := b.Search(sam, SearchInput{Query: "widget", Since: &since}); err != nil || len(r.Hits) != 2 {
		t.Fatalf("since filter: %+v %v", r, err)
	}
	until := int64(2500)
	if r, err := b.Search(sam, SearchInput{Query: "widget", Until: &until}); err != nil || len(r.Hits) != 2 {
		t.Fatalf("until filter: %+v %v", r, err)
	}
	if r, err := b.Search(sam, SearchInput{Query: "widget", Since: &since, Until: &until}); err != nil || len(r.Hits) != 1 || r.Hits[0].Content != "widget widget two" {
		t.Fatalf("since+until filter: %+v %v", r, err)
	}
	r, err := b.Search(sam, SearchInput{Query: "widget"})
	if err != nil || len(r.Hits) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	for i := 1; i < len(r.Hits); i++ {
		if r.Hits[i-1].Score < r.Hits[i].Score {
			t.Fatalf("hits must be ordered best score first: %+v", r.Hits)
		}
	}
}

// TestFTSQueryPrefixTerms (ADR 0019): a term ending in * becomes a quoted
// prefix phrase; a term that is only stars is dropped; a star anywhere else
// stays inside the quotes; embedded quotes are still doubled.
func TestFTSQueryPrefixTerms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"e159e8c*", `"e159e8c"*`},
		{"commit:e159e8c*", `"commit:e159e8c"*`},
		{"ericfitz/agentbus@e159*", `"ericfitz/agentbus@e159"*`},
		{"a**", `"a"*`},
		{"*", ""},
		{"**", ""},
		{"* **", ""},
		{"widget *", `"widget"`},
		{"a*b", `"a*b"`},
		{"*abc", `"*abc"`},
		{`say "hi"`, `"say" """hi"""`},
		{"deploy widget", `"deploy" "widget"`},
	}
	for _, c := range cases {
		if got := ftsQuery(c.in); got != c.want {
			t.Errorf("ftsQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSearchRejectsStarOnlyQuery (ADR 0019): a query left with no terms
// after dropping star-only words is refused with validation in every
// mode, before any MATCH is built or anything is embedded.
func TestSearchRejectsStarOnlyQuery(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	for _, q := range []string{"*", "**", "* **"} {
		for _, mode := range []string{"text", "semantic", "both"} {
			if _, err := b.Search(sam, SearchInput{Query: q, Mode: mode}); err == nil || !strings.Contains(err.Error(), "validation") {
				t.Fatalf("Search(%q, %s) must fail with validation, got %v", q, mode, err)
			}
		}
	}
	// A star that is not at the end of a word is plain text, not syntax.
	if _, err := b.Search(sam, SearchInput{Query: "a*b *abc", Mode: "text"}); err != nil {
		t.Fatalf("stars inside words must be searchable text: %v", err)
	}
}
