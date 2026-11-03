package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestStopHookBlocksOnlyForUnseenMessages(t *testing.T) {
	cfg := config.Default()
	cfg.StatusStaleMinutes = 0 // these tests are about messages, not the status nudge
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
	if want := "Then, except on Codex, make sure a background `agentbus wait -as " + me + "` is running (start one; it replaces any running wait) so the next message wakes you, and stop. If it exits 2, read stderr; do not loop."; !strings.Contains(res["reason"], want) || strings.Contains(res["reason"], "watch") || strings.Contains(res["reason"], "-filter") {
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
			if _, err := b.RegisterWithClient(me, "", "", false, false, harness, "1.0"); err != nil {
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
			want := "Then, except on Codex, make sure a background `agentbus wait -as " + me + "` is running (start one; it replaces any running wait) so the next message wakes you, and stop. If it exits 2, read stderr; do not loop."
			if r := res["reason"]; !strings.HasSuffix(r, want) || strings.Contains(r, "Monitor") {
				t.Fatalf("every harness gets the wait text: %q", r)
			}
		})
	}
}

// nudgeEnv is a registered session at a fresh directory, with a status of tag
// and text set age ago (no status when tag is "").
func nudgeEnv(t *testing.T, tag, text string, age time.Duration, mutate func(*config.Config)) (config.Config, string, string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	cwd := t.TempDir()
	me := filepath.Base(cwd)
	b := peerBus(t, cfg, me)
	if tag != "" {
		b.Now = func() time.Time { return time.Now().Add(-age) }
		if _, err := b.SetStatus(me, tag, text, ""); err != nil {
			t.Fatal(err)
		}
		b.Now = time.Now
	}
	return cfg, cwd, me
}

func runHook(cfg config.Config, cwd string, extra map[string]any) string {
	in := map[string]any{"cwd": cwd}
	for k, v := range extra {
		in[k] = v
	}
	body, _ := json.Marshal(in)
	var out bytes.Buffer
	StopHook(cfg, bytes.NewReader(body), &out, io.Discard)
	return out.String()
}

func reasonOf(t *testing.T, got string) string {
	t.Helper()
	var res map[string]string
	if err := json.Unmarshal([]byte(got), &res); err != nil || res["decision"] != "block" {
		t.Fatalf("want one block, got %q", got)
	}
	return res["reason"]
}

func TestStopHookNudge(t *testing.T) {
	long := strings.Repeat("é", 120)
	cases := []struct {
		name   string
		tag    string
		text   string
		age    time.Duration
		mut    func(*config.Config)
		extra  map[string]any
		parked bool
		want   string // "" = no output; else the reason prefix (exact when exact)
		exact  bool
	}{
		{name: "stale working", tag: "implementing", text: "fix it", age: 31 * time.Minute,
			want: `Agentbus: your status is "implementing: fix it" (31m); if that is no longer true, call set_status.`, exact: true},
		{name: "fresh working", tag: "implementing", text: "fix it", age: 29 * time.Minute},
		{name: "idle old", tag: "idle", text: "x", age: 2 * time.Hour},
		{name: "waiting old", tag: "waiting", text: "x", age: 2 * time.Hour},
		{name: "no status", want: "Agentbus: you have no status; call set_status.", exact: true},
		{name: "parked", tag: "implementing", text: "x", age: time.Hour, parked: true},
		{name: "parked no status", parked: true},
		{name: "hook active", tag: "implementing", text: "x", age: time.Hour, extra: map[string]any{"stop_hook_active": true}},
		{name: "disabled", tag: "implementing", text: "x", age: time.Hour, mut: func(c *config.Config) { c.StatusStaleMinutes = 0 }},
		{name: "disabled no status", mut: func(c *config.Config) { c.StatusStaleMinutes = 0 }},
		{name: "long text", tag: "planning", text: long, age: 31 * time.Minute,
			want: `Agentbus: your status is "planning: ` + strings.Repeat("é", 79) + `…" (31m);`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, cwd, me := nudgeEnv(t, c.tag, c.text, c.age, c.mut)
			if c.parked {
				b, _ := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
				defer func() { _ = b.Close() }()
				if err := b.Park(me); err != nil {
					t.Fatal(err)
				}
			}
			got := runHook(cfg, cwd, c.extra)
			if c.want == "" {
				if got != "" {
					t.Fatalf("want no output, got %q", got)
				}
				return
			}
			r := reasonOf(t, got)
			if c.exact && r != c.want || !strings.HasPrefix(r, c.want) {
				t.Fatalf("reason %q, want %q", r, c.want)
			}
		})
	}
}

func TestStopHookNudgeWithMessages(t *testing.T) {
	cfg, cwd, me := nudgeEnv(t, "implementing", "fix it", 31*time.Minute, nil)
	sam := peerBus(t, cfg, "Sam")
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(me), Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	r := reasonOf(t, runHook(cfg, cwd, nil))
	nudge := `Agentbus: your status is "implementing: fix it" (31m); if that is no longer true, call set_status.`
	if !strings.HasPrefix(r, nudge+" Agentbus: 1 new message(s) for "+me) || !strings.Contains(r, "Call receive") {
		t.Fatalf("combined reason %q", r)
	}
	// A parked session keeps today's message reason, with no nudge.
	b, _ := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = b.Close() }()
	if err := b.Park(me); err != nil {
		t.Fatal(err)
	}
	if r := reasonOf(t, runHook(cfg, cwd, nil)); !strings.HasPrefix(r, "Agentbus: 1 new message(s)") {
		t.Fatalf("parked reason %q", r)
	}
}

func TestStopHookNudgeFailsOpen(t *testing.T) {
	cfg, cwd, _ := nudgeEnv(t, "implementing", "x", time.Hour, nil)
	// The data directory becomes a file: the bus cannot open.
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DataDirectory = filepath.Join(bad, "sub")
	if got := runHook(cfg, cwd, nil); got != "" {
		t.Fatalf("must fail open, got %q", got)
	}
}
