package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestStopHookBlocksOnlyForUnseenMessages(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cwd := t.TempDir() // no .git: identity is the directory's basename
	me := filepath.Base(cwd)
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register(me, "", "", false); err != nil {
		t.Fatal(err)
	}
	sam := peerBus(t, cfg, "Sam")
	hook := func(input map[string]any) string {
		input["cwd"] = cwd
		body, _ := json.Marshal(input)
		var out bytes.Buffer
		StopHook(cfg, bytes.NewReader(body), &out, io.Discard)
		return out.String()
	}

	if got := hook(map[string]any{}); got != "" {
		t.Fatalf("nothing waiting must not block: %q", got)
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(me), Content: "secret"}); err != nil {
		t.Fatal(err)
	}
	got := hook(map[string]any{})
	var res map[string]string
	if err := json.Unmarshal([]byte(got), &res); err != nil || res["decision"] != "block" ||
		!strings.Contains(res["reason"], "receive") || !strings.Contains(res["reason"], "dm/"+me) {
		t.Fatalf("waiting message must block with a receive reason: %q", got)
	}
	if want := "Then, except on Codex, make sure a background `agentbus wait -as " + me + "` is running (start one; it replaces any running wait) so the next message wakes you, and stop."; !strings.Contains(res["reason"], want) || strings.Contains(res["reason"], "watch") || strings.Contains(res["reason"], "-filter") {
		t.Fatalf("stop hook tail: %q", res["reason"])
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("reason must not carry message content: %q", got)
	}
	// Already continuing because of a Stop hook, in either spelling: never block.
	if got := hook(map[string]any{"stop_hook_active": true}); got != "" {
		t.Fatalf("stop_hook_active must not block: %q", got)
	}
	if got := hook(map[string]any{"stopHookActive": true}); got != "" {
		t.Fatalf("stopHookActive must not block: %q", got)
	}
	// Once receive has handed the message out, it no longer blocks, even unacked.
	if r, err := b.Receive(me, bus.ReceiveInput{}); err != nil || len(r.Messages) != 1 {
		t.Fatalf("receive: %+v %v", r, err)
	}
	if got := hook(map[string]any{}); got != "" {
		t.Fatalf("a handed-out batch must not block again: %q", got)
	}
	// A directory that is not an agentbus identity fails open.
	var out bytes.Buffer
	StopHook(cfg, strings.NewReader(`{"cwd":"/nonexistent/elsewhere"}`), &out, io.Discard)
	if out.Len() != 0 {
		t.Fatalf("unregistered identity must not block: %q", out.String())
	}
}

func TestStopHookReasonSameForEveryHarness(t *testing.T) {
	for _, harness := range []string{"claude-code", "grok", "codex-mcp-client", ""} {
		t.Run("harness="+harness, func(t *testing.T) {
			cfg := config.Default()
			cfg.DataDirectory = t.TempDir()
			cwd := t.TempDir()
			me := filepath.Base(cwd)
			b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = b.Close() }()
			if _, err := b.RegisterWithClient(me, "", "", false, harness, "1.0"); err != nil {
				t.Fatal(err)
			}
			sam := peerBus(t, cfg, "Sam")
			if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(me), Content: "x"}); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"cwd": cwd})
			var out bytes.Buffer
			StopHook(cfg, bytes.NewReader(body), &out, io.Discard)
			var res map[string]string
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("%v: %q", err, out.String())
			}
			want := "Then, except on Codex, make sure a background `agentbus wait -as " + me + "` is running (start one; it replaces any running wait) so the next message wakes you, and stop."
			if r := res["reason"]; !strings.HasSuffix(r, want) || strings.Contains(r, "Monitor") {
				t.Fatalf("every harness gets the wait text: %q", r)
			}
		})
	}
}
