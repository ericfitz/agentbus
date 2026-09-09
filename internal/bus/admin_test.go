package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestResetWipesAndLiveProcessGetsNotRegistered(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	first, err := b.Send(sam, SendInput{Channel: "dev", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := Open(b.cfg, b.log)
	defer func() { _ = admin.Close() }()
	if n, _ := admin.LiveSessionCount(); n != 1 {
		t.Fatal(n)
	}
	if err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "y"}); err == nil {
		t.Fatal("live process must lose registration after reset")
	}
	r, _ := b.Register("Sam", "", "", true)
	if r.Sender != "Sam" || r.Resumed {
		t.Fatalf("%+v", r)
	}
	_, _ = b.CreateChannel("Sam", "dev", "ordinary")
	s, _ := b.Send("Sam", SendInput{Channel: "dev", Content: "z"})
	// seq must stay monotonic across a reset (A13.1): it is never reused, so
	// an in-flight embedding HTTP call from another process keyed by seq
	// cannot land its old vector on an unrelated new message. It must NOT
	// restart at 1.
	if s.Seq <= first.Seq {
		t.Fatalf("sequence must stay monotonic after reset: pre-reset seq %d, post-reset seq %d", first.Seq, s.Seq)
	}
	st, _ := b.StatusReport()
	if len(st.Sessions) != 1 || len(st.Channels) != 1+len(DefaultChannels) || st.UsageBytes <= 0 {
		t.Fatalf("%+v", st)
	}
}

// TestStatusReportEmbeddingBacklogZeroWithNoEmbedder covers the minor
// fold-in: with no embedder configured, backlog must report 0, not a count
// of every live memory (which querying with model="" would give, since no
// embeddings row is ever inserted under that key).
func TestStatusReportEmbeddingBacklogZeroWithNoEmbedder(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"}); err != nil {
		t.Fatal(err)
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 0 {
		t.Fatalf("embedding backlog with no embedder configured must be 0, got %d", st.EmbeddingBacklog)
	}
}

// TestResetDoesNotLetInFlightEmbeddingLandOnAReusedSeq is the regression for
// A13.1: seq must not be reused across Reset, or an embedding HTTP call that
// started before the reset (keyed only by seq) could install its vector on
// an unrelated post-reset message that happened to get the same seq.
func TestResetDoesNotLetInFlightEmbeddingLandOnAReusedSeq(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-release
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data []map[string]any
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	// Guarantee the handler unblocks even if an assertion below fails
	// (t.Fatal) before the normal release point below: deferred LIFO, this
	// runs BEFORE srv.Close (which waits for in-flight requests), so a
	// failure path cannot hang the test forever.
	defer releaseHandler()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")

	// Hold embedMu so Send's own background embedSoon pass no-ops; the test
	// drives embedBatch directly so it controls exactly when the HTTP call
	// starts relative to Reset below.
	b.embedMu.Lock()
	first, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.embedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := b.embedBatch(context.Background())
		done <- result{n, err}
	}()
	select { // the batch's HTTP call for first.Seq is now blocked mid-flight
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("embedBatch's HTTP call never reached the handler")
	}

	admin, err := Open(b.cfg, b.log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	if err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	sam2, err := b.Register("Sam", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel(sam2.Sender, "mem", "memory"); err != nil {
		t.Fatal(err)
	}
	// Also hold embedMu here: the first embedBatch call is still blocked
	// mid-request (not holding embedMu itself, since the test invoked it
	// directly rather than through embedSoon), so this Send's own
	// embedSoon would otherwise race a second concurrent request into the
	// same single-shot HTTP handler.
	b.embedMu.Lock()
	second, err := b.Send(sam2.Sender, SendInput{Channel: "mem", Content: "violets are blue"})
	b.embedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq <= first.Seq {
		t.Fatalf("seq must not be reused across Reset: pre-reset seq %d, post-reset seq %d", first.Seq, second.Seq)
	}

	releaseHandler()
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("embedBatch did not return after the handler was released")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}

	// The in-flight batch targeted only first.Seq, which Reset deleted, so
	// its INSERT...SELECT...WHERE EXISTS liveness guard must have skipped it
	// (0 rows actually inserted), and the second message must have no
	// embedding row from it.
	if r.n != 0 {
		t.Fatalf("in-flight batch for a reset-deleted seq must insert nothing, got n=%d", r.n)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings WHERE seq=?", second.Seq).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the new post-reset message must not carry the old in-flight batch's vector")
	}
}
