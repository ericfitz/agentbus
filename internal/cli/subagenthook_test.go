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

func TestSubagentHook(t *testing.T) {
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
	run := func(in map[string]any) string {
		if _, ok := in["cwd"]; !ok {
			in["cwd"] = cwd
		}
		body, _ := json.Marshal(in)
		var out bytes.Buffer
		SubagentHook(cfg, bytes.NewReader(body), &out, io.Discard)
		return out.String()
	}
	context := func(t *testing.T, got string) string {
		t.Helper()
		var res struct {
			H map[string]string `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(got), &res); err != nil || res.H["hookEventName"] != "SubagentStart" {
			t.Fatalf("bad output shape: %q (%v)", got, err)
		}
		return res.H["additionalContext"]
	}

	// The real SubagentStart stdin has no prompt (captured 2026-10-02), so a
	// registered parent always gets the conditional note; the subagent
	// decides from its own prompt whether it applies.
	c := context(t, run(map[string]any{"agent_type": "general-purpose", "agent_id": "afc4f28c9d818fff9"}))
	for _, want := range []string{
		`parent="` + me + `"`, `name="general-purpose-`,
		"If your dispatcher's prompt asks you to use agentbus",
		"parent= or name= your prompt names",
		"do not start a background",
		"Otherwise ignore this note",
	} {
		if !strings.Contains(c, want) {
			t.Fatalf("context lacks %q: %s", want, c)
		}
	}
	// The agent type is slugged; no type falls back to "subagent".
	if c := context(t, run(map[string]any{"agent_type": "Code Reviewer"})); !strings.Contains(c, `name="code-reviewer-`) {
		t.Fatalf("agent_type name: %s", c)
	}
	if c := context(t, run(map[string]any{})); !strings.Contains(c, `name="subagent-`) {
		t.Fatalf("default name: %s", c)
	}
	// Two subagents of one type get distinct names.
	if a, b := run(map[string]any{"agent_type": "x"}), run(map[string]any{"agent_type": "x"}); a == b {
		t.Fatalf("names must differ: %s", a)
	}
	// Parent not registered: silent.
	if got := run(map[string]any{"agent_type": "general-purpose", "cwd": t.TempDir()}); got != "" {
		t.Fatalf("unregistered cwd parent must not inject: %q", got)
	}
	// Bad or empty input: silent.
	for _, in := range []string{"not json", "", "[]"} {
		var out bytes.Buffer
		SubagentHook(cfg, strings.NewReader(in), &out, io.Discard)
		if out.Len() != 0 {
			t.Fatalf("input %q must print nothing: %q", in, out.String())
		}
	}
}
