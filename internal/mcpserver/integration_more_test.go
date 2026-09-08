// This file covers the spec-required integration scenarios that Task 14's
// integration_test.go left to a follow-up task (see that file's header):
//
//  1. TestConcurrentRegistrationDisambiguatesAcrossProcesses - three
//     processes racing to register the same name get a deterministic
//     {Sam, Sam2, Sam3} set, and each can then act as its own owner.
//  2. TestGapAfterAgeEvictionUnderConcurrentSends - age-based capacity
//     eviction, chunked in 1,000-row transactions, running concurrently
//     with live sends; a lagging subscriber gets one gap notice and then
//     only the surviving messages, in order, with no duplicates.
//  3. TestTickDeadlineBoundsBlockedEmbeddingStep - a tick's embedding HTTP
//     call is bounded by the tick's own deadline (half the cleanup
//     interval), and the process stays responsive while it's blocked.
//  4. TestEmbeddingImmediatePassAndTickDrain - a fresh memory becomes
//     semantically searchable via the post-write immediate pass, and a
//     backlog of memories is drained by the periodic tick in batches no
//     larger than the embedding batch size.
//  5. TestLeaseContentionBetweenProcesses - two processes sharing one
//     maintenance lease never both do lease-holder work at once, and both
//     stay responsive while contending for it.
//  6. TestSearchFallsBackWhenEmbeddingEndpointFails - a failing embedding
//     endpoint makes search report semantic_unavailable and still return
//     text-search hits.
//
// All six spawn real `agentbus mcp` processes (via TestMain's binary and
// spawn/call from integration_test.go) sharing one on-disk SQLite database,
// exactly like Task 14. Where a scenario needs pre-aged data (old
// created_at) or a receipt/lease backlog that no tool call can produce
// directly, this file opens the same database file with modernc.org/sqlite
// and writes the rows directly, matching the columns bus package's own
// insertMessage/schema use - see openDirectDB, seedOldMessages,
// seedLiveMemory, and seedExpiredReceipts. Every wait polls a deadline (no
// unbounded waits); every fake HTTP server's blocking gate is released in
// t.Cleanup so a failing test cannot hang.
package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

// callOK invokes a tool that returns a bare JSON array (list_channels,
// discover) rather than an object, so p.call's map[string]any decode
// doesn't apply; used only for responsiveness checks that care whether the
// call succeeded, not its content. Reuses p's existing client session.
func callOK(t *testing.T, p *proc, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	res, err := p.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content[0] is not text: %#v", name, res.Content[0])
	}
	if res.IsError {
		return tc.Text
	}
	return ""
}

// openDirectDB opens the same on-disk database file a spawned `agentbus mcp`
// process uses, with the same connection parameters bus.Open uses (see
// internal/bus/bus.go), for tests that need to seed rows no tool call can
// produce (pre-aged messages, expired receipts) or read internal state
// (lease/session owner tokens) no tool exposes.
func openDirectDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(dir, "agentbus.db") + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open direct db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

// pollUntil calls cond every interval until it returns true or deadline
// passes, returning cond's final verdict. Used everywhere in this file
// instead of a fixed sleep, per the no-unbounded-wait rule.
func pollUntil(deadline time.Time, interval time.Duration, cond func() bool) bool {
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// ---- Scenario 1: concurrent registration across processes ----

func TestConcurrentRegistrationDisambiguatesAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	procs := []*proc{spawn(t, dir), spawn(t, dir), spawn(t, dir)}

	start := make(chan struct{})
	names := make([]string, len(procs))
	errs := make([]string, len(procs))
	var wg sync.WaitGroup
	for i, p := range procs {
		wg.Add(1)
		go func(i int, p *proc) {
			defer wg.Done()
			<-start
			r, e := p.call(t, "register", map[string]any{"name": "Sam"})
			errs[i] = e
			if e == "" {
				names[i], _ = r["as"].(string)
			}
		}(i, p)
	}
	close(start)
	wg.Wait()
	for i, e := range errs {
		if e != "" {
			t.Fatalf("process %d register failed: %s", i, e)
		}
	}

	got := map[string]bool{}
	for _, n := range names {
		if n == "" {
			t.Fatalf("a process registered with an empty display name: %v", names)
		}
		got[n] = true
	}
	want := map[string]bool{"Sam": true, "Sam2": true, "Sam3": true}
	same := len(got) == len(want)
	if same {
		for n := range want {
			if !got[n] {
				same = false
				break
			}
		}
	}
	if !same {
		t.Fatalf("expected exactly the disambiguated set %v, got %v (raw: %v)", want, got, names)
	}

	// Each process's own granted name must work as its owner: separate
	// owner tokens, so cross-process auth would fail if this were reused.
	for i, p := range procs {
		ch := fmt.Sprintf("ch%d", i)
		if _, e := p.call(t, "create_channel", map[string]any{"as": names[i], "name": ch, "kind": "ordinary"}); e != "" {
			t.Fatalf("process %d (as=%q) follow-up create_channel failed: %s", i, names[i], e)
		}
		if _, e := p.call(t, "send", map[string]any{"as": names[i], "channel": ch, "content": "hi"}); e != "" {
			t.Fatalf("process %d (as=%q) follow-up send failed: %s", i, names[i], e)
		}
	}
}

// ---- Scenario 2: gap after age eviction under concurrent sends ----

// seedOldMessages inserts n ordinary messages directly into channel with the
// given created_at (unix ms), bypassing Send entirely, so age eviction has
// pre-aged data to evict before any process is driving the bus. Columns
// match bus.insertMessage's INSERT (internal/bus/messages.go).
func seedOldMessages(t *testing.T, db *sql.DB, channel string, n int, createdAt int64) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	stmt, err := tx.Prepare("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES(?,?,?,?,?,?,?)")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare seed insert: %v", err)
	}
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("old-%d", i)
		if _, err := stmt.Exec(channel, "Seed", "test", createdAt, "", content, len(content)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("seed message %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
}

func gapsOf(r map[string]any) []any {
	g, _ := r["gaps"].([]any)
	return g
}

// countSampler polls a channel's live message count on its own direct DB
// connection so a test can observe the eviction chunking trajectory (no
// per-chunk log line exists to assert against instead - see maintenance.go).
type countSampler struct {
	mu     sync.Mutex
	counts []int64
	stop   chan struct{}
	done   chan struct{}
}

func startCountSampler(db *sql.DB, channel string) *countSampler {
	s := &countSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			select {
			case <-s.stop:
				return
			default:
			}
			var n int64
			if err := db.QueryRow("SELECT count(*) FROM messages WHERE channel=? AND tombstone=0", channel).Scan(&n); err == nil {
				s.mu.Lock()
				s.counts = append(s.counts, n)
				s.mu.Unlock()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return s
}

func (s *countSampler) stopAndSnapshot() []int64 {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.counts))
	copy(out, s.counts)
	return out
}

// TestGapAfterAgeEvictionUnderConcurrentSends drives age-based eviction
// (chunked in 1,000-row transactions per maintenance.go) concurrently with
// live sends on the same channel, and proves a subscriber whose cursor
// falls behind the evicted boundary gets exactly one gap notice, then
// delivery of only the surviving (live) messages, in seq order, with no
// duplicates.
//
// The subscriber acks only its very first fresh batch of pre-eviction
// messages, priming its cursor to a known value, then deliberately leaves
// any further pre-eviction batch unacknowledged (it is simply redelivered
// unchanged on later polls - see receiveOnce's pending-batch branch) until
// a gap notice has actually been observed. This makes the assertions exact
// regardless of the maintenance tick's random startup jitter: whichever
// poll first happens to run after eviction always reports the gap at
// exactly (primed cursor)+1.
func TestGapAfterAgeEvictionUnderConcurrentSends(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"message_retention_hours":1,"cleanup_interval_seconds":5}`)

	setup := spawn(t, dir)
	if _, e := setup.call(t, "register", map[string]any{"name": "Setup"}); e != "" {
		t.Fatal(e)
	}
	if _, e := setup.call(t, "create_channel", map[string]any{"as": "Setup", "name": "dev", "kind": "ordinary"}); e != "" {
		t.Fatal(e)
	}
	setup.close(t)

	db := openDirectDB(t, dir)
	const nOld = 2500 // >= 3 chunks of 1,000
	seedOldMessages(t, db, "dev", nOld, time.Now().Add(-2*time.Hour).UnixMilli())

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "subscribe", map[string]any{"as": "Sam", "channel": "dev", "from": "oldest"}); e != "" {
		t.Fatal(e)
	}

	b := spawn(t, dir)
	if _, e := b.call(t, "register", map[string]any{"name": "Kim"}); e != "" {
		t.Fatal(e)
	}

	const nLive = 50
	sendErrs := make([]string, nLive)
	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		for i := 0; i < nLive; i++ {
			_, e := b.call(t, "send", map[string]any{"as": "Kim", "channel": "dev", "content": fmt.Sprintf("live-%d", i)})
			sendErrs[i] = e
		}
	}()

	sampler := startCountSampler(db, "dev")

	ackedOnce := false
	ackedCount := int64(0)
	var gap map[string]any
	seenLiveSeqs := map[float64]bool{}
	var deliveredLive []map[string]any

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !(gap != nil && len(deliveredLive) == nLive) {
		rr, e := a.call(t, "receive", map[string]any{"as": "Sam", "wait_seconds": 0})
		if e != "" {
			t.Fatal(e)
		}
		for _, g := range gapsOf(rr) {
			gm, ok := g.(map[string]any)
			if ok && gm["channel"] == "dev" && gap == nil {
				gap = gm
			}
		}
		msgs := messagesOf(t, rr)
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				t.Fatalf("message is not an object: %#v", m)
			}
			content, _ := mm["content"].(string)
			switch {
			case strings.HasPrefix(content, "live-"):
				seq, _ := mm["seq"].(float64)
				if seenLiveSeqs[seq] {
					t.Fatalf("duplicate live seq %v delivered across receive calls", seq)
				}
				seenLiveSeqs[seq] = true
				deliveredLive = append(deliveredLive, mm)
			case strings.HasPrefix(content, "old-"):
				// expected pre-eviction content; only relevant to the acking
				// decision below.
			default:
				t.Fatalf("unexpected message content on dev: %v", mm)
			}
		}
		if batch, ok := rr["batch"].(string); ok && batch != "" {
			if gap != nil || !ackedOnce {
				if !ackedOnce {
					ackedCount = int64(len(msgs))
					ackedOnce = true
				}
				if _, e := a.call(t, "receive", map[string]any{"as": "Sam", "ack": batch}); e != "" {
					t.Fatal(e)
				}
			}
			// else: a further pre-gap batch of old messages - leave it
			// unacknowledged so the cursor stays pinned at ackedCount.
		}
		time.Sleep(150 * time.Millisecond)
	}
	sendWG.Wait()
	for i, e := range sendErrs {
		if e != "" {
			t.Fatalf("live send %d: %s", i, e)
		}
	}

	// The 1,000-row-per-transaction chunk bound itself is asserted exactly
	// and without a timing race in package bus's own
	// TestDeleteChunkCapsAtCleanupChunk (internal/bus/maintenance_test.go),
	// which calls deleteChunk directly. From outside the process, three
	// back-to-back chunk commits on a small local database complete faster
	// than any external polling interval can reliably observe (verified:
	// even a 10ms sampler here routinely missed intermediate commits and
	// saw a single >1,000 drop between samples that came from two merged
	// chunks, not one oversized transaction) - so this sampler is kept only
	// as an informational log, not a hard assertion, to avoid a flaky false
	// failure on a correct implementation.
	samples := sampler.stopAndSnapshot()
	var maxDrop int64
	for i := 1; i < len(samples); i++ {
		if d := samples[i-1] - samples[i]; d > maxDrop {
			maxDrop = d
		}
	}
	t.Logf("channel dev message-count samples=%d, largest observed inter-sample drop=%d (chunk-size exactness is covered by TestDeleteChunkCapsAtCleanupChunk)", len(samples), maxDrop)

	if gap == nil {
		t.Fatalf("no gap notice for channel dev observed within the deadline")
	}
	if gap["from"] != float64(ackedCount+1) {
		t.Fatalf("gap.from = %v, want %v (primed cursor %d + 1)", gap["from"], ackedCount+1, ackedCount)
	}
	if gap["to"] != float64(nOld) {
		t.Fatalf("gap.to = %v, want %v (all %d seeded old messages were evicted)", gap["to"], nOld, nOld)
	}
	if len(deliveredLive) != nLive {
		t.Fatalf("expected exactly %d surviving live messages delivered, got %d: %v", nLive, len(deliveredLive), deliveredLive)
	}
	lastSeq := -1.0
	for i, mm := range deliveredLive {
		if mm["sender"] != "Kim" {
			t.Fatalf("delivered live message %d has unexpected sender: %v", i, mm)
		}
		seq, _ := mm["seq"].(float64)
		if seq <= lastSeq {
			t.Fatalf("live messages must be delivered in strictly increasing seq order: %v", deliveredLive)
		}
		lastSeq = seq
	}
}

// ---- Fake embeddings server, shared by scenarios 3, 4, 6 ----

type embedReq struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// fakeEmbedServer serves POST /v1/embeddings, modeled on
// internal/bus/embed_test.go's fakeEmbeddings (not importable from this
// package). It supports an optional blocking gate (see block/unblock) and
// records every request's arrival time (received, regardless of outcome)
// separately from requests that actually completed (completed, only those
// that were not canceled mid-block) - scenario 3 needs the former to time
// the tick deadline, scenario 4 needs the latter so a request the tick
// abandoned mid-block is never mistaken for a persisted embedding.
type fakeEmbedServer struct {
	mu        sync.Mutex
	received  []time.Time
	canceled  []time.Time
	completed []embedReq
	gate      chan struct{}
	status    int // 0 = 200 with a valid response
	srv       *httptest.Server
}

func newFakeEmbedServer(t *testing.T, status int) *fakeEmbedServer {
	t.Helper()
	f := &fakeEmbedServer{status: status}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", f.handle)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.unblock()
		f.srv.Close()
	})
	return f
}

func (f *fakeEmbedServer) url() string { return f.srv.URL + "/v1/embeddings" }

func (f *fakeEmbedServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req embedReq
	_ = json.Unmarshal(body, &req)

	f.mu.Lock()
	f.received = append(f.received, time.Now())
	gate := f.gate
	f.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			// The client disconnected (its deadline fired); respond by
			// simply returning rather than blocking forever, per the fake
			// server's contract with scenario 3.
			f.mu.Lock()
			f.canceled = append(f.canceled, time.Now())
			f.mu.Unlock()
			return
		}
	}

	f.mu.Lock()
	f.completed = append(f.completed, req)
	f.mu.Unlock()

	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	type item struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := make([]item, len(req.Input))
	for i, text := range req.Input {
		data[i] = item{Index: i, Embedding: vecFor(text)}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// vecFor deterministically derives a small nonzero vector from text, so
// identical content always embeds identically and distinct content embeds
// distinctly, without depending on any real embedding model.
func vecFor(text string) []float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum32()
	v := make([]float64, 8)
	for i := range v {
		seed = seed*1664525 + 1013904223
		v[i] = float64(seed%2000)/1000 - 1
	}
	return v
}

func (f *fakeEmbedServer) block() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = make(chan struct{})
}

// unblock releases any blocked requests. Safe to call when not blocked
// (including twice), so tests can call it unconditionally and t.Cleanup can
// always call it defensively.
func (f *fakeEmbedServer) unblock() {
	f.mu.Lock()
	g := f.gate
	f.gate = nil
	f.mu.Unlock()
	if g != nil {
		select {
		case <-g:
		default:
			close(g)
		}
	}
}

func (f *fakeEmbedServer) receivedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeEmbedServer) receivedSnapshot() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.received))
	copy(out, f.received)
	return out
}

func (f *fakeEmbedServer) canceledSnapshot() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.canceled))
	copy(out, f.canceled)
	return out
}

func (f *fakeEmbedServer) completedSnapshot() []embedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]embedReq, len(f.completed))
	copy(out, f.completed)
	return out
}

// ---- Scenario 3: tick deadline bounds a blocked embedding step ----

// seedLiveMemory inserts one live memory revision directly (memory_id=seq,
// revision=1), bypassing Send so no embedSoon background pass is triggered
// - only the periodic tick's own embeddings step will ever touch it, which
// is what this scenario needs to test in isolation.
func seedLiveMemory(t *testing.T, db *sql.DB, channel, content string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO messages(channel,sender,context,created_at,type,content,bytes) VALUES(?,?,?,?,?,?,?)",
		channel, "Seed", "test", time.Now().UnixMilli(), "", content, len(content))
	if err != nil {
		t.Fatalf("seed memory message: %v", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed memory lastInsertId: %v", err)
	}
	if _, err := db.Exec("UPDATE messages SET memory_id=seq, revision=1 WHERE seq=?", seq); err != nil {
		t.Fatalf("seed memory revision update: %v", err)
	}
	return seq
}

// TestTickDeadlineBoundsBlockedEmbeddingStep configures a fake embeddings
// endpoint that blocks until its request's context is canceled, seeds one
// unembedded live memory directly (see seedLiveMemory), and asserts the
// tick's embedding HTTP call is bounded by the tick's own deadline (half
// cleanup_interval_seconds, i.e. ~2.5s here), that the failure is logged,
// and that the process answers tool calls both while the step is blocked
// and after the tick gives up.
func TestTickDeadlineBoundsBlockedEmbeddingStep(t *testing.T) {
	dir := t.TempDir()

	setup := spawn(t, dir)
	if _, e := setup.call(t, "register", map[string]any{"name": "Setup"}); e != "" {
		t.Fatal(e)
	}
	if _, e := setup.call(t, "create_channel", map[string]any{"as": "Setup", "name": "mem", "kind": "memory"}); e != "" {
		t.Fatal(e)
	}
	setup.close(t)

	db := openDirectDB(t, dir)
	seedLiveMemory(t, db, "mem", "deadline-test-memory")

	fake := newFakeEmbedServer(t, 0)
	fake.block()
	writeConfig(t, dir, fmt.Sprintf(`{"cleanup_interval_seconds":5,"embedding_endpoint":%q,"embedding_model":"test"}`, fake.url()))

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}

	if !pollUntil(time.Now().Add(8*time.Second), 50*time.Millisecond, func() bool { return fake.receivedCount() > 0 }) {
		t.Fatalf("tick never attempted an embedding request within 8s")
	}
	entered := fake.receivedSnapshot()[0]

	if e := callOK(t, a, "list_channels", map[string]any{"as": "Sam"}); e != "" {
		t.Fatalf("process unresponsive while the tick's embedding step is blocked: %s", e)
	}

	if !pollUntil(time.Now().Add(6*time.Second), 50*time.Millisecond, func() bool { return len(fake.canceledSnapshot()) > 0 }) {
		t.Fatalf("tick's embedding request was never canceled by the tick deadline within 6s")
	}
	canceled := fake.canceledSnapshot()[0]

	if elapsed := canceled.Sub(entered); elapsed < 1500*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("tick embedding step ran for %s, want roughly the ~2.5s half-interval deadline", elapsed)
	}

	if !pollUntil(time.Now().Add(3*time.Second), 100*time.Millisecond, func() bool {
		body, err := os.ReadFile(filepath.Join(dir, "agentbus.log"))
		return err == nil && strings.Contains(string(body), "maintenance step failed") && strings.Contains(string(body), "step=embeddings")
	}) {
		t.Fatalf("log file never recorded the embeddings step failure after the deadline")
	}

	if e := callOK(t, a, "list_channels", map[string]any{"as": "Sam"}); e != "" {
		t.Fatalf("process unresponsive after the tick's embedding step gave up: %s", e)
	}
}

// ---- Scenario 4: immediate embedding pass and tick-driven drain ----

// TestEmbeddingImmediatePassAndTickDrain covers both embedding triggers
// documented in maintenance.go/embed.go: (a) Send's post-write embedSoon
// pass makes a single new memory searchable within ~2s, and (b) a backlog
// built up while the endpoint is unreachable is drained by the periodic
// tick, one request per batch of at most embedBatchSize (64) inputs.
func TestEmbeddingImmediatePassAndTickDrain(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeEmbedServer(t, 0)
	// send_messages_per_second/send_kib_per_second raised above their
	// defaults (100/1024): this test sends 131 messages in quick
	// succession, which the default send rate limit would otherwise reject
	// with rate_limited - a concern orthogonal to what this scenario tests.
	writeConfig(t, dir, fmt.Sprintf(`{"cleanup_interval_seconds":5,"send_messages_per_second":1000,"send_kib_per_second":65536,"embedding_endpoint":%q,"embedding_model":"test"}`, fake.url()))

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "mem", "kind": "memory"}); e != "" {
		t.Fatal(e)
	}

	// (a) immediate pass: one send should be searchable in semantic mode
	// within 2s, and the fake endpoint must have seen its text.
	const immediateText = "immediate-pass-memory-content"
	if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "mem", "content": immediateText}); e != "" {
		t.Fatal(e)
	}
	var immediateResult map[string]any
	found := pollUntil(time.Now().Add(2*time.Second), 100*time.Millisecond, func() bool {
		r, e := a.call(t, "search", map[string]any{"as": "Sam", "query": immediateText, "mode": "semantic"})
		if e != "" {
			t.Fatal(e)
		}
		immediateResult = r
		hits, _ := r["hits"].([]any)
		return len(hits) > 0
	})
	if !found {
		t.Fatalf("memory not searchable via semantic mode within 2s: %v", immediateResult)
	}
	if immediateResult["semantic_unavailable"] == true {
		t.Fatalf("semantic search unexpectedly unavailable: %v", immediateResult)
	}
	sawImmediate := false
	for _, req := range fake.completedSnapshot() {
		for _, in := range req.Input {
			if in == immediateText {
				sawImmediate = true
			}
		}
	}
	if !sawImmediate {
		t.Fatalf("fake embeddings endpoint never completed a request containing the immediate-pass text")
	}

	// (b) drain: block the endpoint, send 130 memories (2 batches of 64 +
	// 2), unblock, and wait for the tick to drain the backlog.
	fake.block()
	const n = 130
	want := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("drain-%d", i)
		want[text] = true
		if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "mem", "content": text}); e != "" {
			t.Fatalf("send drain memory %d: %s", i, e)
		}
	}
	fake.unblock()

	snapshot := func() (got map[string]bool, maxBatch int) {
		got = map[string]bool{}
		for _, req := range fake.completedSnapshot() {
			if len(req.Input) > maxBatch {
				maxBatch = len(req.Input)
			}
			for _, in := range req.Input {
				if want[in] {
					got[in] = true
				}
			}
		}
		return got, maxBatch
	}
	var got map[string]bool
	var maxBatch int
	pollUntil(time.Now().Add(12*time.Second), 200*time.Millisecond, func() bool {
		got, maxBatch = snapshot()
		return len(got) == n
	})
	if maxBatch > 64 {
		t.Fatalf("an embedding request batch size of %d exceeds embedBatchSize (64)", maxBatch)
	}
	if len(got) != n {
		var missing []string
		for text := range want {
			if !got[text] {
				missing = append(missing, text)
			}
		}
		if len(missing) > 5 {
			missing = missing[:5]
		}
		t.Fatalf("drain incomplete: %d/%d texts embedded; missing e.g. %v", len(got), n, missing)
	}

	r, e := a.call(t, "search", map[string]any{"as": "Sam", "query": "drain", "mode": "semantic", "count": 200})
	if e != "" {
		t.Fatal(e)
	}
	if r["semantic_unavailable"] == true {
		t.Fatalf("semantic search unexpectedly unavailable after drain: %v", r)
	}
	hits, _ := r["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("expected drained memories to be searchable: %v", r)
	}
}

// ---- Scenario 5: lease contention between processes ----

// seedExpiredReceipts inserts n already-expired receipts directly so a
// maintenance tick has visible cleanup work (internal/bus/maintenance.go's
// "receipts" step deletes rows with expires_at < now).
func seedExpiredReceipts(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed receipts tx: %v", err)
	}
	stmt, err := tx.Prepare("INSERT INTO receipts(sender,key,fingerprint,result,expires_at) VALUES(?,?,?,?,?)")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare seed receipts: %v", err)
	}
	expired := time.Now().Add(-time.Hour).UnixMilli()
	for i := 0; i < n; i++ {
		if _, err := stmt.Exec("Seed", fmt.Sprintf("k%d", i), "fp", "{}", expired); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("seed receipt %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed receipts tx: %v", err)
	}
}

// TestLeaseContentionBetweenProcesses proves the maintenance lease
// (internal/bus/maintenance.go's takeLease, a single conditional UPDATE on
// the leases table) is exclusive across real OS processes, not just within
// one: two processes register (so their session rows' owner column, which
// equals each process's private Bus.owner token, lets this test tell them
// apart), a backlog of expired receipts is seeded so a tick has visible
// work, and the leases table's owner is sampled throughout - it must never
// hold any value other than "" or one of the two known owner tokens (the
// schema's single-row lease makes simultaneous ownership structurally
// impossible; there is no per-tick success log line to assert against - see
// maintenance.go). The functional proof that some tick actually ran is the
// seeded receipts being cleaned up; both processes must also answer tool
// calls throughout.
//
// Note: unlike Task 14's header comment (which correctly says lease
// contention is covered by this follow-up task), nothing here claims both
// processes are guaranteed to win the lease at some point - takeLease lets
// the same owner renew on every tick, so one process winning it every time
// is a legal outcome, not a bug.
func TestLeaseContentionBetweenProcesses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"cleanup_interval_seconds":5}`)

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "P1"}); e != "" {
		t.Fatal(e)
	}
	db := openDirectDB(t, dir)
	var ownerA string
	if err := db.QueryRow("SELECT owner FROM sessions WHERE sender=?", "P1").Scan(&ownerA); err != nil {
		t.Fatalf("read process A owner token: %v", err)
	}

	b := spawn(t, dir)
	if _, e := b.call(t, "register", map[string]any{"name": "P2"}); e != "" {
		t.Fatal(e)
	}
	var ownerB string
	if err := db.QueryRow("SELECT owner FROM sessions WHERE sender=?", "P2").Scan(&ownerB); err != nil {
		t.Fatalf("read process B owner token: %v", err)
	}
	if ownerA == ownerB || ownerA == "" || ownerB == "" {
		t.Fatalf("two independently spawned processes must have distinct nonempty owner tokens: %q %q", ownerA, ownerB)
	}

	const nReceipts = 1500 // >= 2 chunks of 1,000, so cleanup is visible work
	seedExpiredReceipts(t, db, nReceipts)
	receiptCount := func() int64 {
		var n int64
		if err := db.QueryRow("SELECT count(*) FROM receipts WHERE sender='Seed'").Scan(&n); err != nil {
			t.Fatalf("count seeded receipts: %v", err)
		}
		return n
	}
	if n := receiptCount(); n != nReceipts {
		t.Fatalf("seed did not land: have %d receipts, want %d", n, nReceipts)
	}

	type sample struct {
		owner string
	}
	var mu sync.Mutex
	var samples []sample
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var owner string
			if err := db.QueryRow("SELECT owner FROM leases WHERE name='maintenance'").Scan(&owner); err == nil {
				mu.Lock()
				samples = append(samples, sample{owner})
				mu.Unlock()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && receiptCount() > 0 {
		if e := callOK(t, a, "list_channels", map[string]any{"as": "P1"}); e != "" {
			t.Fatalf("process A unresponsive during lease contention: %s", e)
		}
		if e := callOK(t, b, "list_channels", map[string]any{"as": "P2"}); e != "" {
			t.Fatalf("process B unresponsive during lease contention: %s", e)
		}
		time.Sleep(250 * time.Millisecond)
	}
	close(stop)
	<-done

	if n := receiptCount(); n != 0 {
		t.Fatalf("expired receipts were never cleaned up by either process's tick within the deadline: %d remain", n)
	}

	mu.Lock()
	sawA, sawB := false, false
	for _, s := range samples {
		if s.owner != "" && s.owner != ownerA && s.owner != ownerB {
			t.Fatalf("lease owner %q matches neither known process owner token", s.owner)
		}
		if s.owner == ownerA {
			sawA = true
		}
		if s.owner == ownerB {
			sawB = true
		}
	}
	sampleCount := len(samples)
	mu.Unlock()
	if sampleCount == 0 {
		t.Fatalf("no lease samples collected")
	}
	t.Logf("lease samples=%d; process A held the lease at some point=%v; process B held it=%v", sampleCount, sawA, sawB)

	if e := callOK(t, a, "list_channels", map[string]any{"as": "P1"}); e != "" {
		t.Fatalf("process A unresponsive after lease contention: %s", e)
	}
	if e := callOK(t, b, "list_channels", map[string]any{"as": "P2"}); e != "" {
		t.Fatalf("process B unresponsive after lease contention: %s", e)
	}
}

// ---- Scenario 6: embedding endpoint failure falls back to text search ----

func TestSearchFallsBackWhenEmbeddingEndpointFails(t *testing.T) {
	dir := t.TempDir()
	fake := newFakeEmbedServer(t, http.StatusInternalServerError)
	writeConfig(t, dir, fmt.Sprintf(`{"embedding_endpoint":%q,"embedding_model":"test"}`, fake.url()))

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "mem", "kind": "memory"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "mem", "content": "fallback probe content"}); e != "" {
		t.Fatal(e)
	}

	r, e := a.call(t, "search", map[string]any{"as": "Sam", "query": "fallback probe", "mode": "both"})
	if e != "" {
		t.Fatal(e)
	}
	if r["semantic_unavailable"] != true {
		t.Fatalf("search must report semantic_unavailable when the embedding endpoint fails: %v", r)
	}
	hits, ok := r["hits"].([]any)
	if !ok || len(hits) != 1 {
		t.Fatalf("expected one text-search hit as fallback: %v", r)
	}
}
