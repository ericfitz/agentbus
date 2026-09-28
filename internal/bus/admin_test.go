package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// A memory-kind send gets a memory_access row (ADR 0013); Reset must
	// clear that table too (review finding #3).
	if _, err := b.Send(sam, SendInput{Channel: "memory", Content: "m"}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, b, "SELECT count(*) FROM memory_access"); n == 0 {
		t.Fatal("memory_access should be populated before reset")
	}
	admin, _ := Open(b.cfg, b.log)
	defer func() { _ = admin.Close() }()
	if n, _ := admin.LiveSessionCount(); n != 1 {
		t.Fatal(n)
	}
	if err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, b, "SELECT count(*) FROM memory_access"); n != 0 {
		t.Fatal("Reset must clear memory_access")
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "y"}); err == nil || !strings.Contains(err.Error(), "not_registered") {
		t.Fatalf("live process must lose registration after reset with not_registered, got %v", err)
	}
	r, _ := b.Register("Sam", "", "", true)
	if r.Sender != "Sam" || r.Resumed || len(filterDMPending(r.Pending)) != 0 {
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
	if len(st.Sessions) != 1 || len(filterDMChannels(st.Channels)) != 1+len(DefaultChannels) || st.UsageBytes <= 0 {
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

// TestStatusReportLastActivityForTaskChannelsOnly (ADR 0013): the channel
// list's last_activity is populated only for a task channel, tracks its
// newest revision, and is absent (the zero value, omitted by omitempty)
// for an ordinary or plain memory channel.
func TestStatusReportLastActivityForTaskChannelsOnly(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	_, _ = b.CreateChannel(sam, "dev", "ordinary")
	_, _ = b.CreateChannel(sam, "tasks/work", "memory")
	tk := mustCreate(t, b, TaskCreateInput{Channel: "tasks/work", Subject: "ship it"})

	st, err := b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Channel{}
	for _, c := range st.Channels {
		byName[c.Name] = c
	}
	if byName["dev"].LastActivity != 0 {
		t.Fatalf("an ordinary channel must not carry last_activity: %+v", byName["dev"])
	}
	first := byName["tasks/work"].LastActivity
	if first == 0 {
		t.Fatal("a task channel must carry last_activity")
	}
	// Zero out the channel's own created_at directly: with the messages
	// column correctly resolved (not the same-named channels column an
	// unqualified reference could shadow), last_activity is still the
	// task's own created_at/tombstone_at, unchanged by this.
	if _, err := b.db.Exec("UPDATE channels SET created_at=0 WHERE name='tasks/work'"); err != nil {
		t.Fatal(err)
	}
	st, err = b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Channels {
		if c.Name == "tasks/work" && c.LastActivity != first {
			t.Fatalf("last_activity must come from messages.created_at, not resolve to the zeroed channels.created_at: got %d, want %d", c.LastActivity, first)
		}
	}

	b.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, err := b.TaskClaim(sam, tk.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	st, err = b.StatusReport()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Channels {
		if c.Name == "tasks/work" {
			if c.LastActivity <= first {
				t.Fatalf("last_activity must advance on a task write: was %d, now %d", first, c.LastActivity)
			}
			return
		}
	}
	t.Fatal("tasks/work missing from channel list")
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
