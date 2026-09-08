package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	srv := NewServer(b, cfg)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var out map[string]any
	json.Unmarshal([]byte(text), &out)
	return out, res
}

func TestToolsRegisteredWithPrefixDescriptions(t *testing.T) {
	cs := testSession(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"register": true, "create_channel": true, "list_channels": true, "subscribe": true, "unsubscribe": true, "send": true, "receive": true, "history": true, "search": true, "get_memory": true, "edit_memory": true, "delete_memory": true, "discover": true}
	for _, tl := range tools.Tools {
		if !strings.HasPrefix(tl.Description, "Agentbus:") {
			t.Fatalf("%s description must start with Agentbus:", tl.Name)
		}
		delete(want, tl.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

func TestRegisterSendReceiveOverMCP(t *testing.T) {
	cs := testSession(t)
	reg, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	if reg["as"] != "Sam" {
		t.Fatal(reg)
	}
	kim, _ := call(t, cs, "register", map[string]any{"name": "Kim"})
	call(t, cs, "create_channel", map[string]any{"as": "Sam", "name": "dev", "kind": "ordinary"})
	call(t, cs, "subscribe", map[string]any{"as": kim["as"], "channel": "dev"})
	call(t, cs, "send", map[string]any{"as": "Sam", "channel": "dev", "content": "hi"})
	r, _ := call(t, cs, "receive", map[string]any{"as": "Kim"})
	msgs := r["messages"].([]any)
	if len(msgs) != 1 || r["batch"] == "" {
		t.Fatalf("%v", r)
	}
	_, res := call(t, cs, "send", map[string]any{"as": "Nobody", "channel": "dev", "content": "x"})
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "not_registered") {
		t.Fatalf("error must be a tool error carrying the code: %+v", res)
	}
	_, res = call(t, cs, "send", map[string]any{"channel": "dev", "content": "x"})
	if !res.IsError {
		t.Fatal("missing as must fail")
	}
}

func TestMissingAsIsValidationError(t *testing.T) {
	cs := testSession(t)
	_, res := call(t, cs, "list_channels", map[string]any{})
	if !res.IsError {
		t.Fatal("missing as must fail")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "validation") {
		t.Fatalf("missing as must be a validation error, got %s", text)
	}
}

// TestBackgroundLoopsStopBeforeCallerCanCloseSafely covers fix round 1
// finding 1: Run must wait for the heartbeat and tick goroutines to exit
// before closing the bus, or an in-flight call can run against a closed
// bus. startBackgroundLoops is exercised directly (Run itself blocks on
// stdio, which isn't testable in-process) with a fast heartbeat interval so
// several heartbeats actually fire while the loop is running.
func TestBackgroundLoopsStopBeforeCallerCanCloseSafely(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.CleanupIntervalSeconds = 5 // long enough not to fire during this test
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	b, err := bus.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	wg := startBackgroundLoops(ctx, b, cfg, log, 3*time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let several heartbeats actually run
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wg.Wait did not return after cancel; a background goroutine leaked")
	}

	// Only safe to close now that wg.Wait has confirmed both loops exited.
	if err := b.Close(); err != nil {
		t.Fatalf("close after wg.Wait failed: %v", err)
	}
	if strings.Contains(buf.String(), "heartbeat failed") {
		t.Fatalf("a heartbeat ran against the closed bus: %s", buf.String())
	}
}

func TestDefaultContextFallsBackAndLogsOnGetwdError(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	got := defaultContextFor("", errors.New("boom"), log)
	if got != "." {
		t.Fatalf(`want ".", got %q`, got)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("expected the Getwd error to be logged, got %q", buf.String())
	}
}

func TestDefaultContextUsesCwdBasenameWhenNoError(t *testing.T) {
	got := defaultContextFor("/foo/bar", nil, nil)
	if got != "bar" {
		t.Fatalf("want bar, got %q", got)
	}
}

// TestRunLogsBusOpenFailure covers the M12.1-3 amendment's logging minor:
// once OpenLog has succeeded, a subsequent bus.Open failure must be
// recorded in the log file, not silently discarded.
func TestRunLogsBusOpenFailure(t *testing.T) {
	dir := t.TempDir()
	// agentbus.db as a directory makes bus.Open fail after OpenLog succeeds.
	if err := os.Mkdir(filepath.Join(dir, "agentbus.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDirectory = dir

	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("expected Run to fail")
	}
	data, err := os.ReadFile(filepath.Join(dir, "agentbus.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "bus open failed") {
		t.Fatalf("expected the bus.Open failure to be logged, got %q", data)
	}
}

// assertValidationEnvelope requires res to be a tool error whose content is
// the {code,message,retryable} JSON envelope with code "validation".
func assertValidationEnvelope(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error, got %+v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var envelope struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("error content is not a JSON envelope: %s (%v)", text, err)
	}
	if envelope.Code != "validation" {
		t.Fatalf("want code validation, got %q (content %q)", envelope.Code, text)
	}
}

// TestSchemaValidationFailuresGetJSONEnvelope covers M12.3: the SDK's own
// typed-argument validation (missing required field, wrong-typed field)
// must produce the same JSON envelope as a bus.Error, via
// wrapSchemaErrorsInEnvelope, not go-sdk's plain validation text.
func TestSchemaValidationFailuresGetJSONEnvelope(t *testing.T) {
	cs := testSession(t)

	_, res := call(t, cs, "register", map[string]any{}) // missing required "name"
	assertValidationEnvelope(t, res)

	sam, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "create_channel", map[string]any{"as": sam["as"], "name": "dev", "kind": "ordinary"})
	_, res = call(t, cs, "history", map[string]any{"as": sam["as"], "channel": "dev", "count": "not-a-number"}) // wrong-typed
	assertValidationEnvelope(t, res)
}

// TestOversizedArgumentErrorIsBoundedOverMCP reproduces F1 at the MCP layer:
// an oversized caller-controlled argument (here search's cursor) must not
// make the serialized tool error result itself blow past the result
// budgets it exists to enforce.
func TestOversizedArgumentErrorIsBoundedOverMCP(t *testing.T) {
	cs := testSession(t)
	sam, _ := call(t, cs, "register", map[string]any{"name": "Sam"})
	big := strings.Repeat("x", 5*1024*1024) // >4 MiB
	_, res := call(t, cs, "search", map[string]any{"as": sam["as"], "query": "widget", "cursor": big})
	if !res.IsError {
		t.Fatal("expected an error for a non-numeric oversized cursor")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if n := len(text); n > 8*1024 {
		t.Fatalf("serialized error result is %d bytes, want <= 8 KiB", n)
	}
}

// TestRealBusErrorsPassThroughUnwrapped confirms wrapSchemaErrorsInEnvelope
// leaves an already-enveloped bus.Error untouched (it must not double-wrap
// or otherwise alter a real bus error's code, such as not_registered).
func TestRealBusErrorsPassThroughUnwrapped(t *testing.T) {
	cs := testSession(t)
	_, res := call(t, cs, "list_channels", map[string]any{"as": "Nobody"})
	if !res.IsError {
		t.Fatal("expected a tool error")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "not_registered") {
		t.Fatalf("real bus error must pass through with its own code, got %s", text)
	}
}
