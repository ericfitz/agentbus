package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitEmbed blocks until any in-flight embedSoon background pass has
// finished, so tests can assert on committed embeddings deterministically.
func (b *Bus) waitEmbed() {
	b.embedMu.Lock()
	b.embedMu.Unlock()
}

func fill(t *testing.T, b *Bus, as, ch string, n int, size int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b.limits = newLimiter(b.cfg) // never rate limit in tests
		if _, err := b.Send(as, SendInput{Channel: ch, Content: strings.Repeat("x", size)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgeCleanupRecordsBoundary(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	b.CreateChannel(sam, "mem", "memory")
	fill(t, b, sam, "dev", 3, 10)
	b.Send(sam, SendInput{Channel: "mem", Content: "keep me"})
	b.Now = func() time.Time { return time.Now().Add(169 * time.Hour) }
	b.Send(sam, SendInput{Channel: "dev", Content: "fresh"})
	b.Tick(context.Background())
	var n int64
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("aged rows not removed: %d", n)
	}
	var ev int64
	if err := b.db.QueryRow("SELECT evicted_before_seq FROM channels WHERE name='dev'").Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if ev != 4 {
		t.Fatalf("evicted_before_seq=%d", ev)
	}
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='mem'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("memory must survive age cleanup")
	}
}

func TestTombstonePurgeAndStaleSessions(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	c, _ := b.Send(sam, SendInput{Channel: "mem", Content: "v1"})
	b.EditMemory(sam, EditInput{ID: *c.MemoryID, Content: "v2"})
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal("young tombstone purged")
	}
	b.Now = func() time.Time { return time.Now().Add(73 * time.Hour) }
	b.Tick(context.Background())
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE memory_id=?", *c.MemoryID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("old tombstone not purged")
	}
	if err := b.db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("stale session not removed")
	}
}

func TestCapacityEvictionAndExhaustion(t *testing.T) {
	b := newTestBus(t)
	b.cfg.SQLiteBudgetMiB = 64 // minimum; usage is measured in pages so we shrink via a test hook
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	b.CreateChannel(sam, "mem", "memory")
	fill(t, b, sam, "dev", 200, 4000)
	before, _ := b.Usage()
	b.budgetOverride = before / 2
	if err := b.checkCapacity(); err != nil {
		t.Fatalf("inline eviction must succeed: %v", err)
	}
	after, _ := b.Usage()
	if after >= before {
		t.Fatalf("no eviction: %d -> %d", before, after)
	}
	var ev int64
	if err := b.db.QueryRow("SELECT evicted_before_seq FROM channels WHERE name='dev'").Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if ev == 0 {
		t.Fatal("eviction must record channel boundary")
	}
	// Fill the budget with memories, which cannot be evicted.
	for i := 0; i < 50; i++ {
		b.limits = newLimiter(b.cfg)
		b.Send(sam, SendInput{Channel: "mem", Content: strings.Repeat("m", 4000)})
	}
	b.budgetOverride = 4096
	err := b.checkCapacity()
	if err == nil || !strings.Contains(err.Error(), "capacity_exhausted") {
		t.Fatalf("want capacity_exhausted, got %v", err)
	}
	b.Tick(context.Background())
	var notice string
	if err := b.db.QueryRow("SELECT message FROM notices WHERE kind='capacity'").Scan(&notice); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "sqlite_budget_mib") || !strings.Contains(notice, b.cfg.Path) {
		t.Fatalf("notice missing budget or config path: %q", notice)
	}
	b.budgetOverride = 0
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM notices").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("notice must clear when under budget")
	}
}

// T10.1: a capacity sweep must start only once usage exceeds the budget
// itself, not merely the (lower) target it sweeps down to; otherwise every
// tick at, say, 90% budget evicts fresh ordinary messages for no reason.
func TestCapacitySweepOnlyWhenOverBudget(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	fill(t, b, sam, "dev", 50, 4000)
	before, err := b.Usage()
	if err != nil {
		t.Fatal(err)
	}
	// Usage sits between target (75% of budget by default) and budget: the
	// old target-based check would sweep here, but usage never exceeded budget.
	b.budgetOverride = before + before/10
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("sweep must not run under budget: dev rows=%d", n)
	}
	// Push usage over budget: a sweep must now run, down toward target.
	b.budgetOverride = before - before/10
	b.Tick(context.Background())
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n >= 50 {
		t.Fatal("sweep must evict once usage exceeds budget")
	}
}

// A5: evictToBudget's sweep loop must also stop when ctx is done, not only
// when the wall-clock deadline passes; otherwise Tick on a canceled ctx (a
// client disconnect mid-sweep) still runs a full chunk-and-vacuum cycle
// before the next deadline check catches it. A canceled ctx with a distant
// deadline and data that would otherwise require a multi-chunk sweep to
// reach target must make evictToBudget return immediately, deleting
// nothing, rather than relying on timing to observe "mid-sweep".
func TestEvictToBudgetStopsPromptlyWhenCtxDone(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	const total = 3000 // several cleanupChunk(1000)-sized chunks
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		if _, err := tx.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES('dev',?,'ctx',?,'','x',1)",
			sam, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b.budgetOverride = 1 // unreachable target: an unbounded sweep would evict everything

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // done before the sweep starts, standing in for "canceled mid-sweep"

	start := time.Now()
	if err := b.evictToBudget(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("evictToBudget with a done ctx took %v, want < 1s", elapsed)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Fatalf("evictToBudget must not delete anything once ctx is done: %d of %d rows remain", n, total)
	}
}

// T10.2: every cleanup statement deletes at most cleanupChunk rows per
// transaction. Exercise deleteChunk directly, the primitive shared by the
// stale-sessions and receipts Tick steps.
func TestDeleteChunkCapsAtCleanupChunk(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	past := b.nowMs() - 1000
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2001; i++ {
		if _, err := tx.Exec("INSERT INTO receipts(sender,key,fingerprint,result,expires_at) VALUES(?,?,?,?,?)",
			sam, fmt.Sprintf("k%d", i), "fp", "{}", past); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for {
		n, err := b.deleteChunk("receipts", "expires_at < ?", []any{b.nowMs()})
		if err != nil {
			t.Fatal(err)
		}
		if n > cleanupChunk {
			t.Fatalf("chunk deleted %d rows, want <= %d", n, cleanupChunk)
		}
		if n == 0 {
			break
		}
		chunks++
	}
	if chunks < 3 {
		t.Fatalf("expected at least 3 chunks for 2001 rows, got %d", chunks)
	}
	var remaining int
	if err := b.db.QueryRow("SELECT count(*) FROM receipts").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("receipts not fully cleaned: %d remain", remaining)
	}
}

// T10.2: Tick actually wires the receipts step through the chunked delete.
func TestTickCleansExpiredReceipts(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	if _, err := b.db.Exec("INSERT INTO receipts(sender,key,fingerprint,result,expires_at) VALUES(?,?,?,?,?)",
		sam, "k", "fp", "{}", b.nowMs()-1000); err != nil {
		t.Fatal(err)
	}
	b.Tick(context.Background())
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM receipts").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("expired receipt not cleaned by Tick")
	}
}

// T10.3: Tick derives a context bound to its deadline and passes it through
// to embedBatch, so a hung embedding HTTP call is cancelled near the
// deadline instead of running for up to embedRequestTimeout (30s).
func TestTickDeadlinePropagatesToEmbedding(t *testing.T) {
	// The handler blocks on an explicit release, not on r.Context().Done():
	// whether an abandoned client request promptly cancels the server-side
	// request context is a detail of the local network stack, not something
	// this test should depend on. The client side still enforces ctx's
	// deadline on its own, independent of the server, which is what this
	// test actually verifies via the elapsed-time assertion below.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	res, err := b.db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,revision,bytes) VALUES('mem',?,'x',?,'','hello',1,5)",
		sam, b.nowMs())
	if err != nil {
		t.Fatal(err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec("UPDATE messages SET memory_id=? WHERE seq=?", seq, seq); err != nil {
		t.Fatal(err)
	}
	b.cfg.CleanupIntervalSeconds = 1 // deadline = 500ms
	start := time.Now()
	b.Tick(context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Tick did not honor its deadline via context: elapsed=%v", elapsed)
	}
}

// T10.4: the tick must not reap idle subscriptions itself, since that erases
// the evidence Receive needs to report them as expired. Before the fix, Tick
// deleted the row first and Receive silently saw nothing.
func TestTickDoesNotReapSubscriptionsBeforeReceiveReportsExpiry(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	if err := b.Subscribe(sam, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Now().Add(time.Duration(b.cfg.CursorIdleHours+1) * time.Hour) }
	// A live agent heartbeats independently of its subscription cursors;
	// refresh it so the far-future clock only ages out the subscription,
	// not the session itself (which Tick's own stale-session step reaps).
	if err := b.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	b.Tick(context.Background())
	res, err := b.Receive(sam, ReceiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range res.Expired {
		if ch == "dev" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dev in Expired after Tick + Receive, got %+v", res.Expired)
	}
	var n int
	if err := b.db.QueryRow("SELECT count(*) FROM subscriptions WHERE sender=?", sam).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("Receive must delete the subscription it reported as expired")
	}
}

// Minor: Close must wait for an in-flight background embedSoon pass, so
// shutdown does not race the goroutine into a closed DB.
func TestCloseWaitsForBackgroundEmbedding(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(500)
	}))
	defer srv.Close()
	// Guarantee the handler unblocks even if an assertion below fails
	// (t.Fatal) before the normal release point: deferred LIFO, this runs
	// BEFORE srv.Close (which waits for in-flight requests), so a failure
	// path cannot hang the test forever.
	defer releaseHandler()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")
	if _, err := b.Send(sam, SendInput{Channel: "mem", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		b.Close()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Close returned before the background embedding pass finished")
	case <-time.After(50 * time.Millisecond):
	}
	releaseHandler()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the background pass finished")
	}
}

// A4: Tick's embeddings step must not run concurrently with a background
// embedSoon pass (they can otherwise both call the embedding endpoint for
// overlapping batches, doubling HTTP cost for no benefit). With a blocked
// fake endpoint, trigger embedSoon then Tick and assert only one request
// reaches the handler while it's held open.
func TestTickSkipsEmbeddingsWhileEmbedSoonHoldsEmbedMu(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	var reqCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		close(reached)
		<-release
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var data []map[string]any
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	defer releaseHandler()
	b := newEmbedBus(t, srv.URL)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "mem", "memory")

	// Hold embedMu while sending so Send's own embedSoon call no-ops; the
	// explicit embedSoon call below is then the one holding embedMu for the
	// blocked HTTP call.
	b.embedMu.Lock()
	_, err := b.Send(sam, SendInput{Channel: "mem", Content: "roses are red"})
	b.embedMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	b.embedSoon()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("embedSoon's HTTP call never reached the handler")
	}

	b.Tick(context.Background())
	if n := reqCount.Load(); n != 1 {
		t.Fatalf("Tick must not call the embedding endpoint while embedSoon holds embedMu: request count = %d, want 1", n)
	}

	releaseHandler()
	b.waitEmbed() // let embedSoon's goroutine finish before the test ends
}

func TestLeaseAllowsOneProcess(t *testing.T) {
	b := newTestBus(t)
	other, _ := Open(b.cfg, b.log)
	defer other.Close()
	if !b.takeLease() {
		t.Fatal("first take must succeed")
	}
	if other.takeLease() {
		t.Fatal("second process must not take a live lease")
	}
	if !b.takeLease() {
		t.Fatal("owner may renew")
	}
	other.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	if !other.takeLease() {
		t.Fatal("expired lease must be takeable")
	}
}
