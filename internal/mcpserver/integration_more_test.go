// This file covers the spec-required integration scenarios that Task 14's
// integration_test.go left to a follow-up task (see that file's header):
//
//  1. TestConcurrentRegistrationDisambiguatesAcrossProcesses - three
//     processes racing to register the same name get a deterministic
//     {Sam, Sam2, Sam3} set, and each can then act as its own owner.
//  2. TestGapAfterAgeEvictionUnderConcurrentSends - age-based capacity
//     eviction, chunked in 1,000-row transactions, running concurrently
//     with live sends; a lagging subscriber gets gap notices covering
//     exactly the evicted range and then only the surviving messages, in
//     order, with no duplicates.
//  3. TestTickDeadlineBoundsBlockedEmbeddingStep - a tick's embedding HTTP
//     call is bounded by the tick's own deadline (half the cleanup
//     interval), and the process stays responsive while it's blocked.
//  4. TestEmbeddingImmediatePassAndTickDrain - a fresh memory becomes
//     semantically searchable via the post-write immediate pass, and a
//     backlog of memories is drained by the periodic tick in batches no
//     larger than the embedding batch size.
//  5. TestLeaseContentionBetweenProcesses - while a foreign process holds
//     the maintenance lease, no real process's tick can do lease-holder
//     work; once released, some real process's tick does drain the backlog,
//     and both processes stay responsive throughout.
//  6. TestSearchFallsBackWhenEmbeddingEndpointFails - a failing embedding
//     endpoint makes search report semantic_unavailable and still return
//     text-search hits.
//
// All six spawn real `agentbus mcp` processes (via TestMain's binary and
// spawn/call from integration_test.go) sharing one on-disk SQLite database,
// exactly like Task 14. Where a scenario needs pre-aged data (old
// created_at), a receipt backlog, or a maintenance tick deterministically
// suppressed around a step it must not race with, this file opens the same
// database file with modernc.org/sqlite and writes the rows/lease state
// directly, matching the columns bus package's own insertMessage/schema use
// - see openDirectDB, holdForeignLease, seedOldMessages, seedLiveMemory, and
// seedExpiredReceipts. Every wait polls a deadline (no unbounded waits);
// every fake HTTP server's blocking gate is released in t.Cleanup so a
// failing test cannot hang; every helper called from a goroutine other than
// the test's own (callSafe) returns errors instead of calling t.Fatalf,
// since only the test goroutine may call FailNow.
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

	"github.com/ericfitz/agentbus/internal/bus"
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
	if len(res.Content) == 0 {
		t.Fatalf("%s: result has no content", name)
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
// internal/bus/bus.go, including foreign_keys(1) - the embeddings table has
// an ON DELETE CASCADE FK to messages), for tests that need to seed rows no
// tool call can produce (pre-aged messages, expired receipts) or read/write
// internal state (lease/session owner tokens) no tool exposes.
func openDirectDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	dsn, err := bus.SQLiteDSN(dir)
	if err != nil {
		t.Fatalf("build direct db dsn: %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open direct db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

// holdForeignLease sets the maintenance lease to a fake owner with an
// expiry far in the future, so takeLease's conditional UPDATE (expires_at <
// now OR owner=?) cannot succeed for any real process until release is
// called. Used to deterministically suppress every process's maintenance
// tick around a step that must not race with it (priming a cursor, an
// immediate-embedding-pass check) without depending on tick timing/jitter.
func holdForeignLease(t *testing.T, db *sql.DB) (release func()) {
	t.Helper()
	farFuture := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.Exec("UPDATE leases SET owner='test-holder', expires_at=? WHERE name='maintenance'", farFuture); err != nil {
		t.Fatalf("hold foreign lease: %v", err)
	}
	return func() {
		if _, err := db.Exec("UPDATE leases SET owner='', expires_at=0 WHERE name='maintenance'"); err != nil {
			t.Fatalf("release foreign lease: %v", err)
		}
	}
}

// callSafe is p.call's logic without any t.Fatalf call, for use from a
// goroutine other than the test's own: Go's testing contract requires
// FailNow (which t.Fatalf calls) to run only on the test goroutine, so a
// helper invoked from a worker goroutine must report a protocol-level
// failure as a returned error instead, for the test goroutine to fail on
// after collecting it.
func callSafe(p *proc, name string, args map[string]any) (result map[string]any, errText string, protoErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	res, err := p.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, "", fmt.Errorf("%s: protocol error: %w", name, err)
	}
	if len(res.Content) == 0 {
		return nil, "", fmt.Errorf("%s: result has no content", name)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, "", fmt.Errorf("%s: content[0] is not text: %#v", name, res.Content[0])
	}
	if res.IsError {
		return nil, tc.Text, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		return nil, "", fmt.Errorf("%s: unmarshal result %q: %w", name, tc.Text, err)
	}
	return out, "", nil
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
	protoErrs := make([]error, len(procs))
	var wg sync.WaitGroup
	for i, p := range procs {
		wg.Add(1)
		go func(i int, p *proc) {
			defer wg.Done()
			<-start
			// callSafe, not p.call: this runs on a worker goroutine, and
			// only the test goroutine may call t.Fatalf (which p.call does
			// internally on a protocol-level failure).
			r, e, protoErr := callSafe(p, "register", map[string]any{"name": "Sam"})
			errs[i], protoErrs[i] = e, protoErr
			if protoErr == nil && e == "" {
				names[i], _ = r["as"].(string)
			}
		}(i, p)
	}
	close(start)
	wg.Wait()
	for i, pe := range protoErrs {
		if pe != nil {
			t.Fatalf("process %d register: %v", i, pe)
		}
	}
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

// TestGapAfterAgeEvictionUnderConcurrentSends drives age-based eviction
// (chunked in 1,000-row transactions per maintenance.go) concurrently with
// live sends on the same channel, and proves a subscriber whose cursor
// falls behind the evicted boundary gets gap notices that, together with
// the old-content messages it received before eviction caught up, account
// for every seq in the pre-eviction backlog (a coverage union that
// tolerates overlap - see below) - then delivery of only the surviving
// (live) messages, in seq order, with no duplicates.
//
// Chunked eviction commits its 1,000-row chunks back-to-back with no
// ordering guarantee against a concurrent poll: a poll landing between two
// chunk commits can legitimately see a gap for the first chunk PLUS
// delivery of already-evicted-boundary-adjacent survivors from the second
// in the very same response (or several such interleavings). So "coverage"
// here is not "the first gap spans everything" but a set: every seq in
// (primedCursor, nOld] must be accounted for by either a gap notice or a
// delivered old- message - see the covered map below (the union allows a
// seq to be covered more than once, e.g. by both a gap and a redelivered
// message for an overlapping range). At least
// one real gap notice must occur (delivery alone must not satisfy
// coverage), and the aged rows must actually have been deleted from the
// table by the end (a direct-SQL check), so coverage cannot be satisfied
// by a run that never actually evicted anything.
//
// A foreign lease (holdForeignLease) blocks every process's tick while the
// subscriber primes its cursor with one acked batch of pre-eviction
// messages, so eviction can never race ahead of that prime - on a correct
// implementation it would otherwise be free to start the gap at seq 1
// instead of the intended boundary, since the tick's startup jitter can be
// arbitrarily close to zero.
//
// There are no separate "ack" calls: acking is itself just a receive call,
// so the token from every response is carried into the very next receive's
// ack argument (or omitted, to deliberately leave a pre-gap old-content
// batch pending - it is then simply redelivered unchanged on a later poll,
// per receiveOnce's pending-batch branch, until any part of the gap has
// actually been observed - preventing the test from draining the whole
// backlog before eviction ever runs). A redelivered response's gaps are
// still recorded (a fresh signal: the gap check runs unconditionally on the
// live cursor, independent of pending state), but its messages are not
// reprocessed - they were already recorded on their first delivery, and
// retention can evict a pending row before a redelivery ever happens, so a
// redelivered batch is not guaranteed identical to what was first recorded.
//
// Coverage marking is a plain idempotent union (covered[seq] = true, never
// an error to set twice): a gap can legitimately reclaim part of a range
// this test already saw via an unacked pending batch that eviction then
// caught up to before the batch was ever acked - receiveOnce's gap check
// uses the raw (still un-advanced) subscription cursor regardless of
// whether a stale pending batch for that same range is being redelivered
// in the very same response, so one response can carry both a gap and
// (identical, already-recorded) redelivered messages for an overlapping
// range. A gap notice is bounds-checked only (from >= ackedCount+1, to <=
// nOld, from <= to), not asserted to start exactly where a prior gap left
// off: acking a batch also advances the cursor, so a later gap can
// legitimately start after an acked old batch rather than after the
// previous gap's end. Correctness comes from the final union covering
// every seq in (ackedCount, nOld] exactly (checked after the loop), plus
// at least one real gap having occurred and the aged rows actually gone
// from the table by then (both also checked after the loop, and folded
// into the loop's own completion condition so the aged-rows check cannot
// fire before the last eviction chunk has actually landed).
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
	release := holdForeignLease(t, db)

	const nOld = 2500 // >= 3 chunks of 1,000
	seedOldMessages(t, db, "dev", nOld, time.Now().Add(-2*time.Hour).UnixMilli())

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "subscribe", map[string]any{"as": "Sam", "channel": "dev", "from": "oldest"}); e != "" {
		t.Fatal(e)
	}

	// covered[seq] is set, idempotently (see the func doc), by either a gap
	// notice or a delivered old- message, for every seq in (ackedCount,
	// nOld]. ackedCount is set below once the priming batch is known;
	// record is defined first because it is used by every subsequent
	// receive response, priming's own ack response included.
	covered := map[int64]bool{}
	var ackedCount int64 = -1
	gotGap := false
	seenLiveSeqs := map[float64]bool{}
	var deliveredLive []map[string]any

	// record processes gap and message content from one receive response.
	// Gap notices are only bounds-checked (from >= ackedCount+1, to <= nOld,
	// from <= to), not asserted to start exactly where a prior gap left
	// off: a poll landing between two eviction chunk commits can see a gap
	// for chunk 1 plus a fresh old batch from chunk 2 delivered in the same
	// response; once that batch is (later) acked, the cursor has moved past
	// it, so chunk 3's gap legitimately starts after it, not merely after
	// chunk 1's gap. Correctness is enforced by the union coverage check
	// after the loop instead.
	record := func(rr map[string]any) {
		for _, g := range gapsOf(rr) {
			gm, ok := g.(map[string]any)
			if !ok || gm["channel"] != "dev" {
				continue
			}
			from, _ := gm["from"].(float64)
			to, _ := gm["to"].(float64)
			f, tt := int64(from), int64(to)
			if f < ackedCount+1 || tt > nOld || f > tt {
				t.Fatalf("gap notice for dev out of bounds: from=%v to=%v, want %d <= from <= to <= %d", from, to, ackedCount+1, nOld)
			}
			for s := f; s <= tt; s++ {
				covered[s] = true
			}
			gotGap = true
		}
		if rr["redelivered"] == true {
			// Byte-identical repeat of a previously recorded, deliberately
			// unacknowledged batch - expected, not a duplicate to record again.
			return
		}
		for _, m := range messagesOf(t, rr) {
			mm, ok := m.(map[string]any)
			if !ok {
				t.Fatalf("message is not an object: %#v", m)
			}
			content, _ := mm["content"].(string)
			seq, _ := mm["seq"].(float64)
			switch {
			case strings.HasPrefix(content, "live-"):
				if seenLiveSeqs[seq] {
					t.Fatalf("duplicate live seq %v delivered across receive calls", seq)
				}
				seenLiveSeqs[seq] = true
				deliveredLive = append(deliveredLive, mm)
			case strings.HasPrefix(content, "old-"):
				if s := int64(seq); ackedCount >= 0 && s > ackedCount {
					covered[s] = true
				}
			default:
				t.Fatalf("unexpected message content on dev: %v", mm)
			}
		}
	}

	primeRR, e := a.call(t, "receive", map[string]any{"as": "Sam", "wait_seconds": 0})
	if e != "" {
		t.Fatal(e)
	}
	primed := messagesOf(t, primeRR)
	if len(primed) == 0 || len(gapsOf(primeRR)) != 0 {
		t.Fatalf("expected a fresh batch of pre-eviction messages with no gap while maintenance is held off: %v", primeRR)
	}
	ackedCount = int64(len(primed))
	ackToken, _ := primeRR["batch"].(string)
	if ackToken == "" {
		t.Fatalf("priming batch missing a batch token: %v", primeRR)
	}
	release()

	b := spawn(t, dir)
	if _, e := b.call(t, "register", map[string]any{"name": "Kim"}); e != "" {
		t.Fatal(e)
	}

	const nLive = 50
	sendErrs := make([]string, nLive)
	sendProtoErrs := make([]error, nLive)
	stopSends := make(chan struct{})
	// Registered before the sender goroutine starts, so a t.Fatal anywhere
	// below (which unwinds via runtime.Goexit and skips the rest of this
	// function) still stops it during test teardown, instead of it issuing
	// sends until proc cleanup tears down process b.
	t.Cleanup(func() { close(stopSends) })
	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		for i := 0; i < nLive; i++ {
			select {
			case <-stopSends:
				return
			default:
			}
			// callSafe, not b.call: runs on a worker goroutine (see callSafe's doc).
			_, e, protoErr := callSafe(b, "send", map[string]any{"as": "Kim", "channel": "dev", "content": fmt.Sprintf("live-%d", i)})
			sendErrs[i], sendProtoErrs[i] = e, protoErr
		}
	}()

	// agedRemaining is part of the loop's own completion condition (not
	// just a post-loop check): coverage can complete via delivered old
	// messages while the last eviction chunk is still pending, so checking
	// row-zero only after the loop can fail a correct implementation that
	// just hadn't finished evicting yet.
	agedRemaining := func() int64 {
		var n int64
		if err := db.QueryRow("SELECT count(*) FROM messages WHERE channel='dev' AND seq<=?", nOld).Scan(&n); err != nil {
			t.Fatalf("count remaining aged rows: %v", err)
		}
		return n
	}

	wantCovered := nOld - ackedCount
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (int64(len(covered)) != wantCovered || len(deliveredLive) != nLive || agedRemaining() != 0) {
		args := map[string]any{"as": "Sam", "wait_seconds": 0}
		if ackToken != "" {
			args["ack"] = ackToken
		}
		rr, e := a.call(t, "receive", args)
		if e != "" {
			t.Fatal(e)
		}
		record(rr)
		// Ack (carry the token into the next call) once any part of the gap
		// has been observed (safe: further progress only) or this response
		// itself just carried part of it; otherwise this is a further
		// pre-gap old-content batch - leave it unacknowledged so the cursor
		// stays pinned and it is simply redelivered unchanged next poll.
		batch, _ := rr["batch"].(string)
		if batch != "" && (gotGap || len(gapsOf(rr)) > 0) {
			ackToken = batch
		} else {
			ackToken = ""
		}
		time.Sleep(150 * time.Millisecond)
	}
	sendWG.Wait()
	for i, pe := range sendProtoErrs {
		if pe != nil {
			t.Fatalf("live send %d: %v", i, pe)
		}
	}
	for i, e := range sendErrs {
		if e != "" {
			t.Fatalf("live send %d: %s", i, e)
		}
	}

	if int64(len(covered)) != wantCovered {
		t.Fatalf("gap+delivery coverage for channel dev incomplete: %d/%d seqs in (%d,%d] accounted for within the deadline", len(covered), wantCovered, ackedCount, nOld)
	}
	if !gotGap {
		t.Fatalf("no gap notice for channel dev was ever observed - coverage must not be satisfiable by delivery alone")
	}
	if remaining := agedRemaining(); remaining != 0 {
		t.Fatalf("aged rows were never evicted: %d of %d remain in the messages table", remaining, nOld)
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
	respondedWhileBlocked := time.Now()

	if !pollUntil(time.Now().Add(6*time.Second), 50*time.Millisecond, func() bool { return len(fake.canceledSnapshot()) > 0 }) {
		t.Fatalf("tick's embedding request was never canceled by the tick deadline within 6s")
	}
	canceled := fake.canceledSnapshot()[0]

	// The responsiveness check above must have completed strictly before
	// the blocked request was released, or it doesn't actually prove the
	// process answered tool calls WHILE genuinely blocked.
	if !respondedWhileBlocked.Before(canceled) {
		t.Fatalf("responsiveness check (completed at %v) did not finish before the blocked embedding request was released at %v", respondedWhileBlocked, canceled)
	}

	// The promised deadline is exactly half the interval (2.5s here); allow
	// scheduling slack but not a full extra interval (5s).
	if elapsed := canceled.Sub(entered); elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
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

// collectSemanticContents pages through search(mode: "semantic") via its
// continuation token and returns the set of every hit's content, so a
// caller can compare it against an expected set instead of trusting a bare
// non-empty hit list. Bounded at 20 pages so a product bug that never stops
// paging fails the test instead of looping forever.
func collectSemanticContents(t *testing.T, p *proc, as, query string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		args := map[string]any{"as": as, "query": query, "mode": "semantic", "count": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		r, e := p.call(t, "search", args)
		if e != "" {
			t.Fatal(e)
		}
		if r["semantic_unavailable"] == true {
			t.Fatalf("semantic search unexpectedly unavailable: %v", r)
		}
		hits, _ := r["hits"].([]any)
		for _, h := range hits {
			hm, ok := h.(map[string]any)
			if !ok {
				t.Fatalf("hit is not an object: %#v", h)
			}
			content, _ := hm["content"].(string)
			got[content] = true
		}
		next, _ := r["next"].(string)
		if next == "" {
			return got
		}
		cursor = next
	}
	t.Fatalf("search(mode: semantic) never stopped paging (20 pages, %d contents seen)", len(got))
	return got
}

// TestEmbeddingImmediatePassAndTickDrain covers both embedding triggers
// documented in maintenance.go/embed.go: (a) Send's post-write embedSoon
// pass makes a single new memory searchable within ~2s, and (b) a backlog
// built up while the endpoint is unreachable is drained by the periodic
// tick, one request per batch of at most embedBatchSize (64) inputs.
//
// Part (a) holds every process's maintenance tick off with a foreign lease
// (holdForeignLease), so a jittered Tick can never mask a broken embedSoon
// by doing the embedding work itself. Its search query is deliberately
// different text from the memory's content: search always makes its own
// query-embedding HTTP request in semantic mode, so if the query text
// equaled the content text, seeing that text at the fake endpoint would not
// distinguish "embedSoon embedded the content" from "search embedded its
// own query" - the latter would false-pass even if embedSoon were broken.
func TestEmbeddingImmediatePassAndTickDrain(t *testing.T) {
	dir := t.TempDir()

	setup := spawn(t, dir)
	if _, e := setup.call(t, "register", map[string]any{"name": "Setup"}); e != "" {
		t.Fatal(e)
	}
	setup.close(t)

	db := openDirectDB(t, dir)
	release := holdForeignLease(t, db)

	fake := newFakeEmbedServer(t, 0)
	// send_messages_per_second/send_kib_per_second raised above their
	// defaults (100/1024): part (b) sends 130 messages in quick succession,
	// which the default send rate limit would otherwise reject with
	// rate_limited - a concern orthogonal to what this scenario tests.
	writeConfig(t, dir, fmt.Sprintf(`{"cleanup_interval_seconds":5,"send_messages_per_second":1000,"send_kib_per_second":65536,"embedding_endpoint":%q,"embedding_model":"test"}`, fake.url()))

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "mem", "kind": "memory"}); e != "" {
		t.Fatal(e)
	}

	// (a) immediate pass: one send should be searchable in semantic mode
	// within 2s (via embedSoon alone - the tick is held off), and the fake
	// endpoint must have completed a request containing the content itself.
	const immediateContent = "immediate-pass-memory-content-alpha"
	const immediateQuery = "unrelated-search-probe-text-beta"
	if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "mem", "content": immediateContent}); e != "" {
		t.Fatal(e)
	}
	var immediateResult map[string]any
	found := pollUntil(time.Now().Add(2*time.Second), 100*time.Millisecond, func() bool {
		r, e := a.call(t, "search", map[string]any{"as": "Sam", "query": immediateQuery, "mode": "semantic"})
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
			if in == immediateContent {
				sawImmediate = true
			}
		}
	}
	if !sawImmediate {
		t.Fatalf("fake embeddings endpoint never completed a request containing the memory's own content (with the tick held off, only the query-embedding request(s) may have completed)")
	}
	release()

	// (b) drain: block the endpoint, send 130 memories (2 batches of 64 +
	// 2), unblock, and wait for the (now-free) tick to drain the backlog -
	// verified by paging semantic search results until every drain-N
	// memory's content is present, not merely "a request was sent" (which
	// doesn't prove the embedding was actually persisted).
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

	var got map[string]bool
	pollUntil(time.Now().Add(12*time.Second), 300*time.Millisecond, func() bool {
		got = collectSemanticContents(t, a, "Sam", "drain-backlog-probe")
		for text := range want {
			if !got[text] {
				return false
			}
		}
		return true
	})
	var missing []string
	for text := range want {
		if !got[text] {
			missing = append(missing, text)
		}
	}
	if len(missing) > 0 {
		if len(missing) > 5 {
			missing = missing[:5]
		}
		t.Fatalf("drain incomplete: search never returned all %d drain memories; missing e.g. %v", n, missing)
	}

	maxBatch := 0
	for _, req := range fake.completedSnapshot() {
		if len(req.Input) > maxBatch {
			maxBatch = len(req.Input)
		}
	}
	if maxBatch > 64 {
		t.Fatalf("an embedding request batch size of %d exceeds embedBatchSize (64)", maxBatch)
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
// the leases table) actually gates real processes' maintenance work, not
// just that its own row can't hold two owners at once (a sampler observing
// only the lease row proves nothing: a process whose Tick never even calls
// takeLease would leave it empty and still drain the seeded backlog,
// passing such a check vacuously).
//
// holdForeignLease sets the lease to a foreign owner with an expiry far in
// the future before either process is spawned, so takeLease cannot succeed
// for either of them; a backlog of expired receipts is seeded, and the test
// asserts the backlog survives at least two tick intervals completely
// untouched while both processes stay responsive. The lease is then
// released, and the backlog must drain within a couple more intervals -
// this is the test's functional proof that some real process's tick can
// take the lease and do the work once it's actually available, closing the
// loop the vacuous-pass check above would have missed.
func TestLeaseContentionBetweenProcesses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"cleanup_interval_seconds":5}`)

	setup := spawn(t, dir)
	if _, e := setup.call(t, "register", map[string]any{"name": "Setup"}); e != "" {
		t.Fatal(e)
	}
	setup.close(t)

	db := openDirectDB(t, dir)
	release := holdForeignLease(t, db)

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

	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "P1"}); e != "" {
		t.Fatal(e)
	}
	b := spawn(t, dir)
	if _, e := b.call(t, "register", map[string]any{"name": "P2"}); e != "" {
		t.Fatal(e)
	}

	// >= 2 tick intervals (cleanup_interval_seconds: 5) while a valid
	// foreign lease is held: no process's tick can run maintenance, so
	// every seeded receipt must survive untouched. Both processes must
	// stay responsive throughout.
	suppressDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(suppressDeadline) {
		if e := callOK(t, a, "list_channels", map[string]any{"as": "P1"}); e != "" {
			t.Fatalf("process A unresponsive while a foreign lease is held: %s", e)
		}
		if e := callOK(t, b, "list_channels", map[string]any{"as": "P2"}); e != "" {
			t.Fatalf("process B unresponsive while a foreign lease is held: %s", e)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if n := receiptCount(); n != nReceipts {
		t.Fatalf("a process ran maintenance while a valid foreign lease was held: %d/%d receipts remain", n, nReceipts)
	}

	release()

	drainDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(drainDeadline) && receiptCount() > 0 {
		if e := callOK(t, a, "list_channels", map[string]any{"as": "P1"}); e != "" {
			t.Fatalf("process A unresponsive after the lease was released: %s", e)
		}
		if e := callOK(t, b, "list_channels", map[string]any{"as": "P2"}); e != "" {
			t.Fatalf("process B unresponsive after the lease was released: %s", e)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if n := receiptCount(); n != 0 {
		t.Fatalf("expired receipts were never cleaned up after the lease was released: %d remain", n)
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
