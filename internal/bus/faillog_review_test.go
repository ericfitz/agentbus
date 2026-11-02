package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tick with an expired context and a query-keyed endpoint: the embeddings
// step must not log the raw url.Error, and must not add a second line for
// a failure embedBatch already logged.
func TestTickDeadlineEmbeddingStepLogsNoURLQueryAndNoDuplicate(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	b := newEmbedBus(t, srv.URL+"/v1/e?api_key=QUERYSECRET")
	h := newCapHandler()
	b.log = slog.New(h)
	b.embedMu.Lock()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	_, _ = b.Send(sam, SendInput{Channel: "mem", Content: "one"})
	b.embedMu.Unlock()
	b.waitEmbed()
	b.cfg.CleanupIntervalSeconds = 1 // tick deadline = 0.5s
	b.Tick(context.Background())
	out := h.dump()
	if strings.Contains(out, "QUERYSECRET") || strings.Contains(out, "sk-test") {
		t.Fatalf("secret in log:\n%s", out)
	}
	if c := strings.Count(out, "embedding failed"); c != 1 {
		t.Fatalf("want exactly 1 'embedding failed' line, got %d:\n%s", c, out)
	}
	if strings.Contains(out, "step=embeddings") {
		t.Fatalf("duplicate step line for an already-logged failure:\n%s", out)
	}
}

func TestRedactEndpointQueryAndPath(t *testing.T) {
	for _, tc := range []struct{ in, leak string }{
		{"https://h/v1/e?SECRETTOKEN", "SECRETTOKEN"},
		{"https://h/v1/e?key=%zz", "%zz"},
		{"https://h/v1/e?a=1;key=abc", "abc"},
		{"https://u:pw@h/v1/e?k=v#frag", "pw"},
	} {
		if got := redactEndpoint(tc.in); strings.Contains(got, tc.leak) || !strings.Contains(got, "/v1/e") {
			t.Errorf("redactEndpoint(%q)=%q", tc.in, got)
		}
	}
	e := &embedder{endpoint: "https://h/bot-SECRETKEY/embeddings", key: "SECRETKEY"}
	if got := e.safeEndpoint(); strings.Contains(got, "SECRETKEY") || !strings.Contains(got, "/embeddings") {
		t.Errorf("key in path not scrubbed: %q", got)
	}
}

func TestExcerptScrubsKeyAcrossWhitespaceAndCut(t *testing.T) {
	e := &embedder{key: "sk-abcdef"}
	// key split by nothing, but surrounded by whitespace runs that collapse
	if got := e.excerpt("a   \n sk-abcdef   b"); strings.Contains(got, "sk-") {
		t.Errorf("key survived: %q", got)
	}
	// the cut lands inside the key: a partial prefix must not remain
	s := strings.Repeat("x", embedBodyExcerpt-4) + "sk-abcdef tail"
	got := e.excerpt(s)
	if strings.Contains(got, "sk") || len(got) > embedBodyExcerpt {
		t.Errorf("partial key kept (len %d): %q", len(got), got[len(got)-10:])
	}
	// multibyte boundary: valid UTF-8, bounded
	m := strings.Repeat("é", 400)
	got = e.excerpt(m)
	if len(got) > embedBodyExcerpt || !strings.HasSuffix(got, "é") {
		t.Errorf("multibyte cut: len=%d", len(got))
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatal("invalid UTF-8 in excerpt")
		}
	}
}

func TestErrTextKeepsWrapperAndRedactsURL(t *testing.T) {
	e := &embedder{endpoint: "http://bob:pw@127.0.0.1:1/v1/e?api_key=QS", key: "KEYVAL", client: &http.Client{}}
	_, err := e.embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("want error")
	}
	wrapped := fmt.Errorf("pass failed: %w", err)
	got := e.errText(wrapped)
	if !strings.HasPrefix(got, "pass failed: ") {
		t.Errorf("wrapper text lost: %q", got)
	}
	for _, s := range []string{"bob", "pw", "QS"} {
		if strings.Contains(got, s) {
			t.Errorf("%q leaked: %q", s, got)
		}
	}
}

func TestEmbedFailureCancelledNotLoggedDeadlineLogged(t *testing.T) {
	b, h, _ := failingEmbedBus(t, http.StatusInternalServerError, "boom", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.logEmbedFailure(ctx, fmt.Errorf("x"), []int64{1})
	if n := len(h.records()); n != 0 {
		t.Fatalf("cancelled ctx logged %d records", n)
	}
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	b.logEmbedFailure(dctx, fmt.Errorf("x"), []int64{1})
	if n := len(h.records()); n != 1 {
		t.Fatalf("deadline ctx logged %d records, want 1", n)
	}
}

func TestLimiterEvictsOldestOnlyAndSummarizes(t *testing.T) {
	b := newTestBus(t)
	h := newCapHandler()
	b.log = slog.New(h)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	b.warnLimited("victim", "victim msg")
	b.warnLimited("victim", "victim msg") // suppressed=1
	b.warnLimited("victim", "victim msg") // suppressed=2
	for i := 0; i < failLogMaxKeys; i++ {
		now = now.Add(time.Millisecond)
		b.warnLimited(fmt.Sprint("k", i), "other")
	}
	// victim was the oldest: evicted with a summary; recent keys survive.
	if !strings.Contains(h.dump(), "victim msg") {
		t.Fatal("no log at all for victim")
	}
	var sum bool
	for _, r := range h.records() {
		if v, ok := attrOf(r, "suppressed"); ok && v.Int64() == 2 && strings.HasPrefix(r.Message, "victim msg") {
			sum = true
		}
	}
	if !sum {
		t.Fatalf("eviction emitted no summary for suppressed entry:\n%s", h.dump())
	}
	if len(b.failLog.seen) != failLogMaxKeys {
		t.Fatalf("size %d, want %d (oldest-only eviction, not clear)", len(b.failLog.seen), failLogMaxKeys)
	}
	before := len(h.records())
	b.warnLimited("k"+fmt.Sprint(failLogMaxKeys-1), "other") // a recent key must still be suppressed
	if len(h.records()) != before {
		t.Fatal("recent key was forgotten by eviction")
	}
}

func TestLimiterFlushedOnClose(t *testing.T) {
	b := newTestBus(t) // cleanup also closes; Close twice must be safe for flush
	h := newCapHandler()
	b.log = slog.New(h)
	b.warnLimited("a", "pending msg")
	b.warnLimited("a", "pending msg")
	b.warnLimited("a", "pending msg")
	if len(h.records()) != 1 {
		t.Fatal("setup")
	}
	_ = b.Close()
	recs := h.records()
	if len(recs) != 2 {
		t.Fatalf("Close must flush a summary:\n%s", h.dump())
	}
	if v, ok := attrOf(recs[1], "suppressed"); !ok || v.Int64() != 2 {
		t.Fatalf("summary: %s", h.dump())
	}
}

func TestLimiterKeysIgnoreDigitsInText(t *testing.T) {
	b := newTestBus(t)
	h := newCapHandler()
	b.log = slog.New(h)
	b.logStepFailure("vacuum", fmt.Errorf("request id 4711 failed after 3 tries"), time.Millisecond)
	b.logStepFailure("vacuum", fmt.Errorf("request id 9999 failed after 7 tries"), time.Millisecond)
	if n := len(h.records()); n != 1 {
		t.Fatalf("differing digits defeated rate limiting: %d records", n)
	}
}

func TestEmbedFailureKeyKeepsStatusDistinct(t *testing.T) {
	b, h, _ := failingEmbedBus(t, http.StatusInternalServerError, "boom", "")
	_, _ = b.embedBatch(context.Background())
	e := b.embedder
	b.logEmbedFailure(context.Background(), &embedHTTPError{status: "401 Unauthorized", code: 401}, []int64{1})
	_ = e
	if n := len(h.records()); n != 2 {
		t.Fatalf("500 then 401 must both log, got %d", n)
	}
}

func TestRetryPathLogsOnlyFailingRowID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case len(req.Input) > 1:
			http.Error(w, "too many", 400)
		case req.Input[0] == "two":
			http.Error(w, "kaput", 500)
		default:
			_, _ = fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	b := newEmbedBus(t, srv.URL)
	h := newCapHandler()
	b.log = slog.New(h)
	b.embedMu.Lock()
	defer b.embedMu.Unlock()
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	for _, c := range []string{"one", "two", "three"} {
		_, _ = b.Send(sam, SendInput{Channel: "mem", Content: c})
	}
	var two int64
	_ = b.db.QueryRow("SELECT seq FROM messages WHERE content='two'").Scan(&two)
	_, _ = b.embedBatch(context.Background())
	var found bool
	for _, r := range h.records() {
		if r.Message != "embedding failed" {
			continue
		}
		found = true
		ids, _ := attrOf(r, "row_ids")
		bs, _ := attrOf(r, "batch_size")
		if fmt.Sprint(ids.Any()) != fmt.Sprint([]int64{two}) || bs.Int64() != 1 {
			t.Fatalf("row_ids=%v batch_size=%v, want [%d] 1", ids.Any(), bs.Any(), two)
		}
	}
	if !found {
		t.Fatalf("no failure logged:\n%s", h.dump())
	}
}

func TestLogEmbedRejectedDeterministicWithReasonCounts(t *testing.T) {
	b, h, _ := failingEmbedBus(t, 400, "x", "")
	rej := map[int]string{0: "zero reason", 1: "other", 2: "zero reason", 3: "other"}
	for i := 0; i < 20; i++ {
		b.logEmbedRejected(rej, []int64{10, 11, 12, 13})
	}
	for _, r := range h.records() {
		if r.Message != "embedding rejected rows" {
			continue
		}
		if v, _ := attrOf(r, "err"); v.String() != "zero reason" {
			t.Fatalf("err not from lowest index: %v", v)
		}
		if v, ok := attrOf(r, "reasons"); !ok || !strings.Contains(v.String(), "2 x zero reason") || !strings.Contains(v.String(), "2 x other") {
			t.Fatalf("reasons: %v", v)
		}
	}
}

func TestInspectionHookFailureRateLimited(t *testing.T) {
	b := newTestBus(t)
	h := newCapHandler()
	b.log = slog.New(h)
	b.cfg.InspectionCommand = []string{"false"}
	for i := 0; i < 4; i++ {
		_ = b.inspect("send", "Sam", nil)
	}
	if n := len(h.records()); n != 1 {
		t.Fatalf("inspection failures logged %d times, want 1:\n%s", n, h.dump())
	}
}
