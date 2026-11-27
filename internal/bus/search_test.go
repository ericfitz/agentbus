package bus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// recordingEmbeddings answers every input with the same unit vector and
// records the inputs it was asked to embed, so a test can see the exact
// text a query embedded. Unlike fakeEmbeddings it does not check the key.
func recordingEmbeddings() (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		var data []item
		mu.Lock()
		for i, s := range req.Input {
			inputs = append(inputs, s)
			data = append(data, item{Index: i, Embedding: []float64{1, 0, 0}})
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), inputs...)
	}
}

// TestSearchPrefixMatchesFullHashInEveryMode (ADR 0019): a memory holding
// a full 40-character commit hash is found by its short hash plus * in
// text, semantic and both modes; the semantic modes embed the bare short
// hash, never the star; and with the endpoint gone, both falls back to
// text and still prefix-matches.
func TestSearchPrefixMatchesFullHashInEveryMode(t *testing.T) {
	srv, inputs := recordingEmbeddings()
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	const hash = "e159e8c3b7d2a1f4e5c6d7b8a9f0e1d2c3b4a5f6"
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "fixed in ericfitz/agentbus@" + hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "unrelated note"}); err != nil {
		t.Fatal(err)
	}
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"text", "semantic", "both"} {
		r, err := b.Search(sam, SearchInput{Query: "e159e8c*", Mode: mode})
		if err != nil || r.SemanticUnavailable || len(r.Hits) == 0 || !strings.Contains(r.Hits[0].Content, hash) {
			t.Fatalf("mode %s: %+v %v", mode, r, err)
		}
	}
	var sawBare bool
	for _, in := range inputs() {
		if strings.Contains(in, "*") {
			t.Fatalf("embedded text must not carry the prefix star: %q", in)
		}
		if in == "e159e8c" {
			sawBare = true
		}
	}
	if !sawBare {
		t.Fatalf("semantic modes must embed the bare term, embedded: %q", inputs())
	}
	// Endpoint gone: both falls back to text and the prefix still matches.
	srv.Close()
	r, err := b.Search(sam, SearchInput{Query: "e159e8c*", Mode: "both"})
	if err != nil || !r.SemanticUnavailable || len(r.Hits) != 1 || !strings.Contains(r.Hits[0].Content, hash) {
		t.Fatalf("fallback after the endpoint is gone: %+v %v", r, err)
	}
}

// A query of only stars is refused before anything is embedded (ADR 0019):
// the endpoint must see no request from semantic or both mode.
func TestSearchStarOnlyQueryNeverEmbeds(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		t.Errorf("embedder called for a star-only query: %s", r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	for _, mode := range []string{"semantic", "both"} {
		for _, q := range []string{"*", "** *"} {
			if _, err := b.Search(sam, SearchInput{Query: q, Mode: mode}); err == nil {
				t.Fatalf("mode %s query %q: want a validation error", mode, q)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("embedder was called %d times", calls)
	}
}

func TestSearchTagsMustAllMatchInEveryMode(t *testing.T) {
	srv := fakeEmbeddings(t)
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "mem", "widget both", "foo", "bar")
	sendTagged(t, b, sam, "mem", "widget foo", "foo")
	sendTagged(t, b, sam, "mem", "widget bar", "bar")
	// Semantic search ranks embedded memory revisions only.
	b.waitEmbed()
	if _, err := b.embedBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"text", "semantic", "both"} {
		r, err := b.Search(sam, SearchInput{Query: "widget", Tags: []string{"foo", "bar"}, Mode: mode})
		if err != nil || len(r.Hits) != 1 || r.Hits[0].Content != "widget both" {
			t.Fatalf("mode %s: %+v %v", mode, r.Hits, err)
		}
	}
}

func TestSearchTagsOnly(t *testing.T) {
	b, sam, kim := setupTwo(t)
	if _, err := b.CreateChannel(sam, "ops", "ordinary"); err != nil {
		t.Fatal(err)
	}
	a := sendTagged(t, b, sam, "dev", "one", "deployment", "failed")
	sendTagged(t, b, sam, "dev", "skip", "deployment")
	c := sendTagged(t, b, kim, "dev", "two", "deployment", "failed")
	d := sendTagged(t, b, sam, "ops", "three", "deployment", "failed")
	e := sendTagged(t, b, sam, "dev", "four", "deployment", "failed")
	// Reply to a, so Thread=a.Seq matches a and the reply.
	rep, err := b.Send(kim, SendInput{Channel: "dev", Content: "reply", ReplyTo: &a.Seq, Tags: []string{"deployment", "failed"}})
	if err != nil {
		t.Fatal(err)
	}
	// Another agent's DM inbox: Kim to Lee; Sam must not see it.
	lee := reg(t, b, "Lee")
	if _, err := b.Send(kim, SendInput{Channel: DMChannel(lee), Content: "secret", Tags: []string{"deployment", "failed"}}); err != nil {
		t.Fatal(err)
	}
	// A tombstoned match.
	gone := sendTagged(t, b, sam, "dev", "gone", "deployment", "failed")
	if _, err := b.db.Exec("UPDATE messages SET tombstone=1 WHERE seq=?", gone.Seq); err != nil {
		t.Fatal(err)
	}
	seqs := func(r SearchResult) []int64 {
		var out []int64
		for _, h := range r.Hits {
			if h.Score != 0 {
				t.Fatalf("score %v, want 0", h.Score)
			}
			out = append(out, h.Seq)
		}
		return out
	}
	tags := []string{"deployment", "failed"}
	want := []int64{rep.Seq, e.Seq, d.Seq, c.Seq, a.Seq}
	r, err := b.Search(sam, SearchInput{Tags: tags})
	if err != nil || !slices.Equal(seqs(r), want) {
		t.Fatalf("tags only: %v %v want %v", seqs(r), err, want)
	}
	r, err = b.Search(sam, SearchInput{Query: "*", Tags: tags})
	if err != nil || !slices.Equal(seqs(r), want) {
		t.Fatalf("star + tags: %v %v", seqs(r), err)
	}
	r, err = b.Search(sam, SearchInput{Tags: tags, Count: 1})
	if err != nil || len(r.Hits) != 1 || r.Next == "" {
		t.Fatalf("page 1: %+v %v", r, err)
	}
	r2, err := b.Search(sam, SearchInput{Tags: tags, Count: 1, Cursor: r.Next})
	if err != nil || len(r2.Hits) != 1 || r2.Hits[0].Seq != e.Seq {
		t.Fatalf("page 2: %+v %v", r2, err)
	}
	r, _ = b.Search(sam, SearchInput{Tags: tags, Channel: "ops"})
	if !slices.Equal(seqs(r), []int64{d.Seq}) {
		t.Fatalf("channel: %v", seqs(r))
	}
	r, _ = b.Search(sam, SearchInput{Tags: tags, Sender: "Kim"})
	if !slices.Equal(seqs(r), []int64{rep.Seq, c.Seq}) {
		t.Fatalf("sender: %v", seqs(r))
	}
	r, _ = b.Search(sam, SearchInput{Tags: tags, Thread: &a.Seq})
	if !slices.Equal(seqs(r), []int64{rep.Seq, a.Seq}) {
		t.Fatalf("thread: %v", seqs(r))
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	r, _ = b.Search(sam, SearchInput{Tags: tags, Since: &future})
	if len(r.Hits) != 0 {
		t.Fatalf("since in the future: %v", seqs(r))
	}
	r, _ = b.Search(sam, SearchInput{Tags: tags, Until: &future})
	if len(r.Hits) != len(want) {
		t.Fatalf("until in the future: %v", seqs(r))
	}
	wantCode(t, func() error { _, err := b.Search(sam, SearchInput{Tags: tags, Mode: "bogus"}); return err }(), "validation")
	_, err = b.Search(sam, SearchInput{})
	wantCode(t, err, "validation")
	if !strings.Contains(err.Error(), "query is required (or give tags)") {
		t.Fatalf("message: %v", err)
	}
	_, err = b.Search(sam, SearchInput{Query: "*"})
	wantCode(t, err, "validation")
	if !strings.Contains(err.Error(), "* alone matches nothing") {
		t.Fatalf("ADR 0019 error: %v", err)
	}
}

func TestSearchTagsOnlyNeverEmbeds(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		t.Errorf("embedder called for a tags-only search: %s", r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	sendTagged(t, b, sam, "dev", "x", "foo")
	for _, mode := range []string{"semantic", "both"} {
		r, err := b.Search(sam, SearchInput{Tags: []string{"foo"}, Mode: mode})
		if err != nil || r.SemanticUnavailable || len(r.Hits) != 1 {
			t.Fatalf("mode %s: %+v %v", mode, r, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("embedder was called %d times", calls)
	}
}

// searchKindsFixture builds one widget row in each kind of channel: memory
// "mem", ordinary "general", task list "tasks/w", and Ann's DM to Sam.
func searchKindsFixture(t *testing.T) (b *Bus, ann, sam, bob string) {
	t.Helper()
	b = newTestBus(t)
	ann = reg(t, b, "Ann")
	sam = reg(t, b, "Sam")
	bob = reg(t, b, "Bob")
	for _, c := range []struct{ name, kind string }{{"mem", "memory"}, {"general", "ordinary"}, {"tasks/w", "memory"}} {
		if _, err := b.CreateChannel(ann, c.name, c.kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Send(ann, SendInput{Channel: "mem", Content: "widget memory", Tags: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(ann, SendInput{Channel: "general", Content: "widget chat", Tags: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(bob, SendInput{Channel: "general", Content: "widget other", Tags: []string{"j"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.TaskCreate(ann, TaskCreateInput{Channel: "tasks/w", Subject: "job", Description: "widget task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(ann, SendInput{Channel: DMChannel(sam), Content: "widget dm"}); err != nil {
		t.Fatal(err)
	}
	return b, ann, sam, bob
}

func searchChannels(t *testing.T, b *Bus, as string, in SearchInput) []string {
	t.Helper()
	if in.Query == "" && len(in.Tags) == 0 {
		in.Query = "widget"
	}
	r, err := b.Search(as, in)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range r.Hits {
		got = append(got, h.Channel)
	}
	slices.Sort(got)
	return got
}

func wantChannels(t *testing.T, got []string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
}

func TestSearchKindsDefaultAll(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{}), "mem", "general", "general", "tasks/w", DMChannel(sam))
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Kinds: []string{"memories", "chat", "tasks"}}), "mem", "general", "general", "tasks/w", DMChannel(sam))
}

func TestSearchKindsChat(t *testing.T) {
	b, _, sam, bob := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Kinds: []string{"chat"}}), "general", "general", DMChannel(sam))
	wantChannels(t, searchChannels(t, b, bob, SearchInput{Kinds: []string{"chat"}}), "general", "general")
}

func TestSearchKindsTasks(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Kinds: []string{"tasks"}}), "tasks/w")
}

func TestSearchKindsMemories(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Kinds: []string{"memories"}}), "mem")
}

func TestSearchKindsUnion(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Kinds: []string{"memories", "tasks"}}), "mem", "tasks/w")
}

func TestSearchKindsUnknown(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	_, err := b.Search(sam, SearchInput{Query: "widget", Kinds: []string{"chats", "x", "Chat"}})
	if err == nil {
		t.Fatal("unknown kinds must be rejected")
	}
	var be *Error
	if !errors.As(err, &be) || be.Code != "validation" {
		t.Fatalf("want validation error, got %v", err)
	}
	const want = `unknown kind "chats", "x", "Chat"; kinds are memories, chat, tasks`
	if be.Message != want {
		t.Fatalf("message %q, want %q", be.Message, want)
	}
	if _, err := b.Search(sam, SearchInput{Query: "widget", Kinds: []string{"chat", "chat"}}); err != nil {
		t.Fatal(err)
	}
	// Unknown values are rejected even when a channel scope would ignore them.
	if _, err := b.Search(sam, SearchInput{Query: "widget", Channel: "mem", Kinds: []string{"bogus"}}); err == nil {
		t.Fatal("unknown kind with channel must be rejected")
	}
}

func TestSearchChannelOverridesKinds(t *testing.T) {
	b, _, sam, _ := searchKindsFixture(t)
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Channel: "tasks/w", Kinds: []string{"chat"}}), "tasks/w")
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Channel: "mem", Kinds: []string{"chat"}}), "mem")
}

func TestSearchKindsWithFilters(t *testing.T) {
	b, ann, sam, bob := searchKindsFixture(t)
	// Tags-only: tag k is on a memory row and a chat row.
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Tags: []string{"k"}, Kinds: []string{"chat"}}), "general")
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Tags: []string{"k"}, Kinds: []string{"memories"}}), "mem")
	// Sender filter.
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Sender: bob, Kinds: []string{"chat"}}), "general")
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Sender: ann, Kinds: []string{"chat"}}), "general", DMChannel(sam))
	// Since filter in the future matches nothing.
	future := time.Now().Add(time.Hour).UnixMilli()
	wantChannels(t, searchChannels(t, b, sam, SearchInput{Since: &future, Kinds: []string{"chat"}}))
}
