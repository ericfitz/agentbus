package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParkEndedByToolCallsExceptReceive(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cs := sessionOn(t, cfg)
	admin, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	call(t, cs, "register", map[string]any{"name": "Sam"})

	park := func() {
		t.Helper()
		if err := admin.Park("Sam"); err != nil {
			t.Fatal(err)
		}
	}
	park()
	call(t, cs, "receive", map[string]any{"as": "Sam"})
	if !admin.Parked("Sam") {
		t.Fatal("receive must not unpark")
	}
	for name, args := range map[string]map[string]any{
		"discover":      {"as": "Sam"},
		"list_channels": {"as": "Sam"},
		"send":          {"as": "Sam", "channel": "general", "content": "hi"},
		"task_list":     {"as": "Sam", "channel": "tasks/x"},
	} {
		park()
		call(t, cs, name, args)
		if admin.Parked("Sam") {
			t.Errorf("%s must unpark", name)
		}
	}
	// A failed call (bad arguments) still counts as activity.
	park()
	call(t, cs, "send", map[string]any{"as": "Sam"})
	if admin.Parked("Sam") {
		t.Error("a rejected send must unpark too")
	}
	// A call for another name unparks that name only.
	park()
	_ = admin.Park("Pat")
	call(t, cs, "discover", map[string]any{"as": "Sam"})
	if !admin.Parked("Pat") {
		t.Error("a call as Sam must not unpark Pat")
	}
	// register unparks, via the bus (leftover park from a dead session).
	park()
	call(t, cs, "register", map[string]any{"name": "Sam"})
	if admin.Parked("Sam") {
		t.Error("register must unpark")
	}
	// A call without `as`, or with a name that cannot be parked, is harmless.
	call(t, cs, "discover", map[string]any{})
	call(t, cs, "discover", map[string]any{"as": "a/b"})
}

func TestUnparkMiddlewareSkipsSubagentNamesSilently(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	b, err := bus.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	reached := false
	h := unparkOnToolCall(b, log)(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		reached = true
		return nil, nil
	})
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{
		Name: "send", Arguments: json.RawMessage(`{"as":"Sam/worker"}`),
	}}
	if _, err := h(context.Background(), "tools/call", req); err != nil || !reached {
		t.Fatalf("the call must proceed: reached=%v err=%v", reached, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("a subagent call must log nothing, got %q", logs.String())
	}
}

func TestUnparkIgnoresNamesThisProcessDoesNotOwn(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cs := sessionOn(t, cfg)
	other, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.Register("Pat", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := other.Park("Pat"); err != nil {
		t.Fatal(err)
	}
	call(t, cs, "discover", map[string]any{"as": "Pat"})
	if !other.Parked("Pat") {
		t.Fatal("a call as a name another process owns must leave its park in place")
	}
}
