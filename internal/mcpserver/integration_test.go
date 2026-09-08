// This file spawns real `agentbus mcp` processes (the actual built binary,
// not an in-process server) sharing one data directory and drives them over
// stdio with the go-sdk client, exercising what only real separate processes
// can show: separate owner tokens so a name registered in one process is
// unusable from another, a stalled/killed process holding its name until the
// 30s attachment-expiry window, redelivery across a lost response,
// cross-process visibility of a memory edit (get/receive/search), the
// inspection hook running in the sending process, `reset` invalidating a
// live process's registration out from under it, and clean stdio framing
// (the transport owns stdout; diagnostics go only to the log file). Time-
// dependent behavior (age cleanup, expiry, tombstone purge) is covered by
// the fake-clock tests in package bus. Lease contention and other
// spec-required scenarios not listed above are covered by a follow-up task.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTimeout bounds every client call so a hung server fails the test
// instead of hanging it; it is generous relative to the wait_seconds values
// used below (at most a few seconds).
const callTimeout = 20 * time.Second

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentbus-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentbus-bin mkdir temp:", err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "agentbus")
	build := exec.Command("go", "build", "-o", binary, "../..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build agentbus: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "remove agentbus-bin temp dir:", err)
	}
	os.Exit(code)
}

// syncBuffer is a concurrency-safe io.Writer: os/exec copies a subprocess's
// stderr into the configured Writer from a background goroutine that can
// still be running when a test reads it (the goroutine only provably exits
// once cmd.Wait, called inside ClientSession.Close, returns), so a plain
// bytes.Buffer would race with an in-test read.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

type proc struct {
	cs     *mcp.ClientSession
	cmd    *exec.Cmd
	stderr *syncBuffer
}

// spawn starts a real `agentbus mcp` process against dataDir and connects an
// MCP client to it over stdio via mcp.CommandTransport.
func spawn(t *testing.T, dataDir string, env ...string) *proc {
	t.Helper()
	cmd := exec.Command(binary, "mcp", "--config", filepath.Join(dataDir, "config.json"))
	cmd.Env = append(os.Environ(), append([]string{"AGENTBUS_DATA_DIR=" + dataDir}, env...)...)
	cmd.Dir = dataDir
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("spawn agentbus mcp: %v", err)
	}
	p := &proc{cs: cs, cmd: cmd, stderr: stderr}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			// Expected once a test has already killed the process (e.g.
			// TestDeadProcessHoldsNameUntilExpiry): log, don't fail cleanup.
			t.Logf("close client session: %v", err)
		}
	})
	return p
}

// call invokes a tool with a deadline-bound context and returns either the
// decoded result or, on a tool-level failure, the raw error envelope text.
func (p *proc) call(t *testing.T, name string, args map[string]any) (map[string]any, string) {
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
		return nil, tc.Text
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("%s: unmarshal result %q: %v", name, tc.Text, err)
	}
	return out, ""
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func TestTwoProcessesShareNamesAndMessages(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	a, b := spawn(t, dir), spawn(t, dir)

	ra, e := a.call(t, "register", map[string]any{"name": "Sam"})
	if e != "" {
		t.Fatal(e)
	}
	rb, e := b.call(t, "register", map[string]any{"name": "Sam"})
	if e != "" {
		t.Fatal(e)
	}
	if ra["as"] != "Sam" || rb["as"] != "Sam2" {
		t.Fatalf("second process must get a disambiguated name: %v %v", ra, rb)
	}

	// A name registered in process a has a session row owned by process a;
	// process b has a different owner token, so "as":"Sam" fails auth there.
	if _, e := b.call(t, "list_channels", map[string]any{"as": "Sam"}); !strings.Contains(e, "not_registered") {
		t.Fatalf("cross-process as must fail with not_registered: %s", e)
	}

	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "dev", "kind": "ordinary"}); e != "" {
		t.Fatal(e)
	}
	if _, e := b.call(t, "subscribe", map[string]any{"as": "Sam2", "channel": "dev"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "dev", "content": "hello"}); e != "" {
		t.Fatal(e)
	}

	r, e := b.call(t, "receive", map[string]any{"as": "Sam2", "wait_seconds": 2})
	if e != "" {
		t.Fatal(e)
	}
	msgs, ok := r["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected one message: %v", r)
	}

	// Lost response: calling receive again without ack redelivers the same batch.
	r2, e := b.call(t, "receive", map[string]any{"as": "Sam2"})
	if e != "" {
		t.Fatal(e)
	}
	if r2["redelivered"] != true || r2["batch"] != r["batch"] {
		t.Fatalf("unacked batch must be redelivered unchanged: %v", r2)
	}

	r3, e := b.call(t, "receive", map[string]any{"as": "Sam2", "ack": r["batch"]})
	if e != "" {
		t.Fatal(e)
	}
	msgs3, ok := r3["messages"].([]any)
	if !ok || len(msgs3) != 0 {
		t.Fatalf("ack must advance past the batch: %v", r3)
	}
}

func TestDeadProcessHoldsNameUntilExpiry(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill process a: %v", err)
	}

	b := spawn(t, dir)
	rb, e := b.call(t, "register", map[string]any{"name": "Sam"})
	if e != "" {
		t.Fatal(e)
	}
	if rb["as"] != "Sam2" {
		t.Fatalf("within the 30s attachment-expiry window the dead process still holds Sam: got %v", rb["as"])
	}
	// Expiry after 30s is covered by the fake-clock test TestStaleOwnerLosesName in package bus.
}

func TestHookRunsInSendingProcess(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook.sh")
	script := "#!/bin/sh\ncat >/dev/null; echo '{\"allow\":false,\"reason\":\"blocked by test\"}'\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	writeConfig(t, dir, `{"inspection_command": ["`+hook+`"]}`)
	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "dev", "kind": "ordinary"}); e != "" {
		t.Fatal(e)
	}
	_, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "dev", "content": "x"})
	if !strings.Contains(e, "inspection_rejected") || !strings.Contains(e, "blocked by test") {
		t.Fatalf("expected inspection_rejected with the hook's reason: %s", e)
	}
}

func TestResetWhileLive(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "dev", "kind": "ordinary"}); e != "" {
		t.Fatal(e)
	}
	before, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "dev", "content": "before reset"})
	if e != "" {
		t.Fatal(e)
	}
	beforeSeq, ok := before["seq"].(float64)
	if !ok {
		t.Fatalf("send result missing numeric seq: %v", before)
	}

	reset := exec.Command(binary, "reset", "--config", filepath.Join(dir, "config.json"))
	reset.Env = append(os.Environ(), "AGENTBUS_DATA_DIR="+dir)
	reset.Stdin = strings.NewReader("yes\n")
	out, err := reset.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "1 live session") {
		t.Fatalf("reset subcommand: %s %v", out, err)
	}

	if _, e := a.call(t, "list_channels", map[string]any{"as": "Sam"}); !strings.Contains(e, "not_registered") {
		t.Fatalf("live process must see the reset as not_registered: %s", e)
	}

	r, e := a.call(t, "register", map[string]any{"name": "Sam"})
	if e != "" {
		t.Fatal(e)
	}
	if r["as"] != "Sam" || r["resumed"] == true {
		t.Fatalf("after reset the name is free and fresh: %v", r)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "dev", "kind": "ordinary"}); e != "" {
		t.Fatal(e)
	}
	s, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "dev", "content": "first again"})
	if e != "" {
		t.Fatal(e)
	}
	afterSeq, ok := s["seq"].(float64)
	if !ok {
		t.Fatalf("send result missing numeric seq: %v", s)
	}
	// Ruling: reset deliberately leaves sqlite_sequence alone so seq stays
	// monotonic across a reset (an in-flight embedding HTTP call keyed only
	// by seq must never land its old vector on a recycled seq number). So a
	// post-reset send must NOT restart at 1 - it must continue strictly past
	// whatever was assigned before the reset.
	if afterSeq <= beforeSeq {
		t.Fatalf("seq must stay monotonic across reset: before=%v after=%v", beforeSeq, afterSeq)
	}
}

func TestMemoryEditVisibleAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	a, b := spawn(t, dir), spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if _, e := b.call(t, "register", map[string]any{"name": "Kim"}); e != "" {
		t.Fatal(e)
	}
	if _, e := a.call(t, "create_channel", map[string]any{"as": "Sam", "name": "mem", "kind": "memory"}); e != "" {
		t.Fatal(e)
	}
	if _, e := b.call(t, "subscribe", map[string]any{"as": "Kim", "channel": "mem"}); e != "" {
		t.Fatal(e)
	}
	c, e := a.call(t, "send", map[string]any{"as": "Sam", "channel": "mem", "content": "v1"})
	if e != "" {
		t.Fatal(e)
	}
	id := c["memory_id"]
	if _, e := a.call(t, "edit_memory", map[string]any{"as": "Sam", "id": id, "content": "v2"}); e != "" {
		t.Fatal(e)
	}

	r, e := b.call(t, "receive", map[string]any{"as": "Kim"})
	if e != "" {
		t.Fatal(e)
	}
	msgs, ok := r["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected one message: %v", r)
	}
	first, ok := msgs[0].(map[string]any)
	if !ok || first["content"] != "v2" {
		t.Fatalf("receive must show the edited content: %v", msgs)
	}

	m, e := b.call(t, "get_memory", map[string]any{"as": "Kim", "id": id})
	if e != "" {
		t.Fatal(e)
	}
	if m["content"] != "v2" {
		t.Fatalf("get_memory must show the edited content: %v", m)
	}

	s, e := b.call(t, "search", map[string]any{"as": "Kim", "query": "v2"})
	if e != "" {
		t.Fatal(e)
	}
	hits, ok := s["hits"].([]any)
	if !ok || len(hits) != 1 {
		t.Fatalf("expected one search hit for the edited content: %v", s)
	}
}

func TestStdoutCarriesOnlyProtocol(t *testing.T) {
	// A process that logged to stdout would corrupt framing; the client
	// would fail to connect or to parse the first response, so spawning and
	// successfully calling a tool already proves stdout is clean protocol
	// framing. Separately assert stderr is empty and diagnostics went only
	// to the log file, per Run's contract.
	dir := t.TempDir()
	writeConfig(t, dir, `{"log_level": "debug"}`)
	a := spawn(t, dir)
	if _, e := a.call(t, "register", map[string]any{"name": "Sam"}); e != "" {
		t.Fatal(e)
	}
	if a.stderr.Len() != 0 {
		t.Fatalf("stderr must be empty: %q", a.stderr.String())
	}
	logBody, err := os.ReadFile(filepath.Join(dir, "agentbus.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(logBody), "agentbus mcp started") {
		t.Fatalf("log file missing startup line: %s", logBody)
	}
}
