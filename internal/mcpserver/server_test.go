package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

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
