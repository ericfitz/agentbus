package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSetStatusTool(t *testing.T) {
	cs := testSession(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	out, res := call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": "implementing", "text": "writing tests"})
	if res.IsError || out["revision"] != float64(1) || out["tag"] != "implementing" {
		t.Fatalf("set_status: %v %+v", out, res)
	}
	_, res = call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": "offline", "text": "x"})
	if !res.IsError {
		t.Fatal("bad tag must be an error result")
	}
	want := "tag must be one of idle, planning, implementing, reviewing, waiting, blocked, needs-human (offline is set by the bus)"
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, want) {
		t.Fatalf("error text %q lacks %q", txt, want)
	}
}

func TestStatusHistoryTool(t *testing.T) {
	cs := testSession(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	for _, tag := range []string{"planning", "implementing", "reviewing"} {
		call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": tag, "text": "t " + tag})
	}
	out, _ := call(t, cs, "status_history", map[string]any{"as": "Sam", "name": "Sam"})
	sts, _ := out["statuses"].([]any)
	if len(sts) != 3 || out["live"] != true || out["name"] != "Sam" {
		t.Fatalf("history: %v", out)
	}
	if sts[0].(map[string]any)["tag"] != "planning" || sts[2].(map[string]any)["tag"] != "reviewing" {
		t.Fatalf("order: %v", sts)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "status_history", Arguments: map[string]any{"as": "Sam", "name": "Nobody"}})
	if err != nil || res.IsError {
		t.Fatalf("unknown: %v %+v", err, res)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(txt, `"statuses":[]`) {
		t.Fatalf("unknown name must give an empty array: %s", txt)
	}
	// An unregistered caller cannot read.
	_, res = call(t, cs, "status_history", map[string]any{"as": "Ghost", "name": "Sam"})
	if !res.IsError {
		t.Fatal("unregistered caller must be refused")
	}
}

func TestRegisterOthersStatus(t *testing.T) {
	cs, peer := testPair(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": "blocked", "text": "stuck", "problem": "no key"})
	reg, _ := call(t, peer, "register", map[string]any{"name": "Kim"})
	others, _ := reg["others"].([]any)
	if len(others) != 1 {
		t.Fatalf("others: %v", reg)
	}
	st, _ := others[0].(map[string]any)["status"].(map[string]any)
	if st["tag"] != "blocked" {
		t.Fatalf("others entry lacks status: %v", others[0])
	}
}

func TestSetStatusUnparks(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cs := sessionOn(t, cfg)
	admin, err := bus.Open(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	call(t, cs, "register", map[string]any{"name": "Sam"})
	if err := admin.Park("Sam"); err != nil {
		t.Fatal(err)
	}
	call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": "idle", "text": "back"})
	if admin.Parked("Sam") {
		t.Fatal("set_status must unpark")
	}
}

func TestDiscoverWithStatusesListsAll(t *testing.T) {
	cs, peer := testPair(t)
	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, peer, "register", map[string]any{"name": "Kim"})
	long := strings.Repeat("x", 200)
	call(t, cs, "set_status", map[string]any{"as": "Sam", "tag": "blocked", "text": long, "problem": long})
	call(t, peer, "set_status", map[string]any{"as": "Kim", "tag": "blocked", "text": long, "problem": long})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "discover", Arguments: map[string]any{"as": "Sam"}})
	if err != nil {
		t.Fatal(err)
	}
	var ss []bus.Session
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &ss); err != nil || len(ss) != 2 {
		t.Fatalf("discover: %v %v", err, ss)
	}
}
