package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPrunedMemoryNeverLeaksThroughAnyTool (#53): a pruned memory is
// invisible to agents through every MCP tool. Each tool needs a case below;
// a tool without one fails the test, so a new tool cannot ship unguarded.
func TestPrunedMemoryNeverLeaksThroughAnyTool(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
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
	t.Cleanup(func() { _ = cs.Close() })
	raw := openDirectDB(t, cfg.DataDirectory)

	call(t, cs, "register", map[string]any{"name": "Sam"})
	call(t, cs, "create_channel", map[string]any{"as": "Sam", "name": "mem", "kind": "memory"})
	call(t, cs, "create_channel", map[string]any{"as": "Sam", "name": "tasks/work", "kind": "memory"})
	send := func(content string) int64 {
		out, _ := call(t, cs, "send", map[string]any{"as": "Sam", "channel": "mem", "content": content, "tags": []string{"kind:gotcha"}})
		return int64(out["memory_id"].(float64))
	}
	p, d := send("zebracorn pruned"), send("zebracorn deleted")
	if _, res := call(t, cs, "delete_memory", map[string]any{"as": "Sam", "id": d}); res.IsError {
		t.Fatalf("delete d: %+v", res)
	}
	if _, err := raw.Exec("UPDATE memory_access SET pruned_at=?, pruned_reason='noise' WHERE memory_id=?", time.Now().UnixMilli(), p); err != nil {
		t.Fatal(err)
	}

	text := func(res *mcp.CallToolResult) string {
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String()
	}
	norm := func(s string, id int64) string {
		return regexp.MustCompile(fmt.Sprintf(`\b%d\b`, id)).ReplaceAllString(s, "N")
	}
	// sameAsDeleted calls tool with args for p and for d and requires equal
	// error text once each id is replaced by N.
	sameAsDeleted := func(tool string, args func(id int64) map[string]any) func(t *testing.T) {
		return func(t *testing.T) {
			_, rp := call(t, cs, tool, args(p))
			_, rd := call(t, cs, tool, args(d))
			if !rp.IsError || !rd.IsError {
				t.Fatalf("%s: p error=%v, d error=%v; both must be refused", tool, rp.IsError, rd.IsError)
			}
			if a, b := norm(text(rp), p), norm(text(rd), d); a != b {
				t.Fatalf("%s: pruned answers %q, deleted answers %q", tool, a, b)
			}
		}
	}
	// hidesContent calls tool and requires no trace of the pruned memory.
	hidesContent := func(tool string, args map[string]any) func(t *testing.T) {
		return func(t *testing.T) {
			_, res := call(t, cs, tool, args)
			s := text(res)
			if strings.Contains(s, "zebracorn") || strings.Contains(s, fmt.Sprintf(`"memory_id":%d`, p)) {
				t.Fatalf("%s leaked the pruned memory: %s", tool, s)
			}
		}
	}
	// clean makes one valid call and requires no "zebracorn" in the result.
	clean := func(tool string, args map[string]any) func(t *testing.T) {
		return func(t *testing.T) {
			_, res := call(t, cs, tool, args)
			if res.IsError {
				t.Fatalf("%s: unexpected error: %s", tool, text(res))
			}
			if strings.Contains(text(res), "zebracorn") {
				t.Fatalf("%s leaked: %s", tool, text(res))
			}
		}
	}
	as := func(m map[string]any) map[string]any { m["as"] = "Sam"; return m }

	cases := map[string]func(t *testing.T){
		"get_memory":    sameAsDeleted("get_memory", func(id int64) map[string]any { return as(map[string]any{"id": id}) }),
		"edit_memory":   sameAsDeleted("edit_memory", func(id int64) map[string]any { return as(map[string]any{"id": id, "content": "x"}) }),
		"delete_memory": sameAsDeleted("delete_memory", func(id int64) map[string]any { return as(map[string]any{"id": id}) }),
		"task_get":      sameAsDeleted("task_get", func(id int64) map[string]any { return as(map[string]any{"task_id": id}) }),
		"task_update": sameAsDeleted("task_update", func(id int64) map[string]any {
			return as(map[string]any{"task_id": id, "status": "completed"})
		}),
		"task_claim":   sameAsDeleted("task_claim", func(id int64) map[string]any { return as(map[string]any{"task_id": id}) }),
		"task_release": sameAsDeleted("task_release", func(id int64) map[string]any { return as(map[string]any{"task_id": id}) }),

		"history": hidesContent("history", as(map[string]any{"channel": "mem"})),
		"receive": hidesContent("receive", as(map[string]any{})),

		"register":       clean("register", map[string]any{"name": "Sam"}),
		"create_channel": clean("create_channel", as(map[string]any{"name": "chat", "kind": "ordinary"})),
		"list_channels":  clean("list_channels", as(map[string]any{})),
		"subscribe":      clean("subscribe", as(map[string]any{"channel": "chat"})),
		"unsubscribe":    clean("unsubscribe", as(map[string]any{"channel": "chat"})),
		"send":           clean("send", as(map[string]any{"channel": "chat", "content": "hello"})),
		"discover":       clean("discover", as(map[string]any{})),
		"task_create":    clean("task_create", as(map[string]any{"channel": "tasks/work", "subject": "ship it"})),
		"task_list":      clean("task_list", as(map[string]any{"channel": "tasks/work"})),
		"set_status":     clean("set_status", as(map[string]any{"tag": "idle", "text": "waiting"})),
		"status_history": clean("status_history", as(map[string]any{"name": "Sam"})),
	}
	searches := map[string]map[string]any{
		"query":   as(map[string]any{"query": "zebracorn"}),
		"tags":    as(map[string]any{"tags": []string{"kind:gotcha"}}),
		"channel": as(map[string]any{"query": "zebracorn", "channel": "mem"}),
		"text":    as(map[string]any{"query": "zebracorn", "mode": "text"}),
	}
	cases["search"] = func(t *testing.T) {
		for name, args := range searches {
			t.Run(name, hidesContent("search", args))
		}
	}

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		c, ok := cases[tl.Name]
		if !ok {
			t.Errorf("no leak case for tool %s", tl.Name)
			continue
		}
		t.Run(tl.Name, c)
	}
}
