package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus-local/internal/bus"
	"github.com/ericfitz/agentbus-local/internal/config"
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
