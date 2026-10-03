package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// embedUntilDone runs embedBatch until it embeds nothing more or fails.
func embedUntilDone(t *testing.T, b *Bus) error {
	t.Helper()
	b.waitEmbed()
	for i := 0; i < 10; i++ {
		n, err := b.embedBatch(context.Background())
		if err != nil || n == 0 {
			return err
		}
	}
	return nil
}

// ADR 0018: a memory the endpoint rejects on its own (400) no longer blocks
// the batch behind it. Its batch is retried one row at a time; the
// rejected row is recorded and left out of the backlog, the rest embed,
// and the backlog drains to 0 with the rejection counted.
func TestEmbedRejectedRowIsSkippedNotBlocking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var data []map[string]any
		for i, s := range req.Input {
			if strings.Contains(s, "BAD") {
				http.Error(w, `{"error":{"message":"input is too long"}}`, http.StatusBadRequest)
				return
			}
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	for _, c := range []string{"first", "BAD huge memory", "third"} {
		if _, err := b.Send(sam, SendInput{Channel: "mem", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := embedUntilDone(t, b); err != nil {
		t.Fatalf("a rejected row must not fail the pass: %v", err)
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 0 || st.EmbeddingRejected != 1 || st.EmbeddingError != "" {
		t.Fatalf("backlog=%d rejected=%d error=%q, want 0 1 \"\"", st.EmbeddingBacklog, st.EmbeddingRejected, st.EmbeddingError)
	}
	var embedded int
	if err := b.db.QueryRow("SELECT count(*) FROM embeddings").Scan(&embedded); err != nil || embedded != 2 {
		t.Fatalf("embedded=%d %v, want 2", embedded, err)
	}
	var reason string
	if err := b.db.QueryRow("SELECT error FROM embed_failures").Scan(&reason); err != nil || !strings.Contains(reason, "too long") {
		t.Fatalf("recorded rejection %q %v", reason, err)
	}
}

// ADR 0018: a failure that is not one row's fault (here 401, a missing or
// wrong key in the embedding process) is reported in status with a hint,
// rejects nothing, and clears once embedding works again.
func TestEmbedFailureIsReportedAndClears(t *testing.T) {
	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized.Load() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
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
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "a memory"}); err != nil {
		t.Fatal(err)
	}
	if err := embedUntilDone(t, b); err == nil {
		t.Fatal("401 must fail the pass")
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 1 || st.EmbeddingRejected != 0 || !strings.Contains(st.EmbeddingError, "401") ||
		!strings.Contains(st.EmbeddingError, "embedding_api_key") || st.EmbeddingErrorAt == 0 {
		t.Fatalf("status = backlog %d rejected %d error %q at %d", st.EmbeddingBacklog, st.EmbeddingRejected, st.EmbeddingError, st.EmbeddingErrorAt)
	}
	authorized.Store(true)
	if err := embedUntilDone(t, b); err != nil {
		t.Fatal(err)
	}
	if st, err = b.StatusReport(); err != nil || st.EmbeddingBacklog != 0 || st.EmbeddingError != "" {
		t.Fatalf("after recovery: %+v %v", st, err)
	}
}

// An endpoint that rejects every input (a wrong path answering 422 for all,
// an unknown model answering 400) is a configuration error, not bad rows:
// nothing is marked rejected, the pass fails, and status says why. A 422
// body is left out of the error, since it can echo the memory text back.
func TestEmbedEndpointRejectingEverythingIsAnErrorNotRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":[{"input":"secret memory text"}]}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	for _, c := range []string{"one", "two"} {
		if _, err := b.Send(sam, SendInput{Channel: "mem", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := embedUntilDone(t, b); err == nil {
		t.Fatal("rejecting every input must fail the pass")
	}
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 2 || st.EmbeddingRejected != 0 || !strings.Contains(st.EmbeddingError, "422") {
		t.Fatalf("backlog %d rejected %d error %q, want 2 0 and a 422 error", st.EmbeddingBacklog, st.EmbeddingRejected, st.EmbeddingError)
	}
	if strings.Contains(st.EmbeddingError, "secret") {
		t.Fatalf("422 body must not reach status: %q", st.EmbeddingError)
	}
	// A lone memory rejected while nothing under this model has ever
	// embedded is the same configuration error, not a bad row.
	if _, err := b.db.Exec("DELETE FROM messages WHERE content='two'"); err != nil {
		t.Fatal(err)
	}
	if err := embedUntilDone(t, b); err == nil {
		t.Fatal("a lone rejected memory with nothing ever embedded must fail the pass")
	}
	if st, _ = b.StatusReport(); st.EmbeddingRejected != 0 {
		t.Fatalf("rejected = %d, want 0", st.EmbeddingRejected)
	}
}

// A non-row failure (429) partway through the one-at-a-time retry keeps
// what that retry already did: the next pass picks up after it instead of
// repeating the whole batch.
func TestEmbedRowRetryKeepsProgressOnLaterFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, s := range req.Input {
			if strings.Contains(s, "BAD") {
				http.Error(w, "input is too long", http.StatusBadRequest)
				return
			}
		}
		if len(req.Input) == 1 && req.Input[0] == "third" && calls.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		var data []map[string]any
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "mem", "memory")
	b.waitEmbed()
	b.embedMu.Lock() // keep embedSoon out so the passes below see all three
	for _, c := range []string{"first", "BAD huge memory", "third"} {
		if _, err := b.Send(sam, SendInput{Channel: "mem", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.embedBatch(context.Background()); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("first pass: %v, want the 429", err)
	}
	b.embedMu.Unlock()
	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if st.EmbeddingBacklog != 1 || st.EmbeddingRejected != 1 {
		t.Fatalf("after the 429: backlog %d rejected %d, want 1 1", st.EmbeddingBacklog, st.EmbeddingRejected)
	}
	if err := embedUntilDone(t, b); err != nil {
		t.Fatal(err)
	}
	if st, _ = b.StatusReport(); st.EmbeddingBacklog != 0 || st.EmbeddingRejected != 1 || !strings.Contains(st.EmbeddingRejectedLast, "too long") {
		t.Fatalf("after recovery: %+v", st)
	}
}
