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
	if _, err := b.Register("Lead", "", "", false); err != nil {
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

	// No marker, even with agentbus mentioned: silent.
	for _, p := range []string{"Find the agentbus schema file", "Review the agentbus repo", ""} {
		if got := run(map[string]any{"agent_prompt": p}); got != "" {
			t.Fatalf("prompt %q must not inject: %q", p, got)
		}
	}
	// Each marker spelling, case-insensitive, with the parent from cwd.
	for _, p := range []string{"Please use agentbus.", "USE AGENTBUS to report", "register on agentbus first", "Register with the agentbus MCP? no: register with agentbus"} {
		c := context(t, run(map[string]any{"agent_prompt": p, "description": "Fix the Parser!", "agent_type": "general-purpose"}))
		if !strings.Contains(c, `parent="`+me+`"`) || !strings.Contains(c, `name="fix-the-parser-`) ||
			!strings.Contains(c, "do not start a background") {
			t.Fatalf("prompt %q context: %s", p, c)
		}
	}
	// Parent and name named in the prompt win over cwd and description.
	c := context(t, run(map[string]any{"agent_prompt": "use agentbus: register with parent=Lead, name=Lead-reviewer.", "description": "x"}))
	if !strings.Contains(c, `parent="Lead"`) || !strings.Contains(c, `name="Lead-reviewer"`) {
		t.Fatalf("named parent/name context: %s", c)
	}
	// No description: slug the agent type.
	if c := context(t, run(map[string]any{"agent_prompt": "use agentbus", "agent_type": "Code Reviewer"})); !strings.Contains(c, `name="code-reviewer-`) {
		t.Fatalf("agent_type name: %s", c)
	}
	// Parent not registered: named or cwd-derived, silent.
	if got := run(map[string]any{"agent_prompt": "use agentbus parent=Nobody"}); got != "" {
		t.Fatalf("unregistered named parent must not inject: %q", got)
	}
	if got := run(map[string]any{"agent_prompt": "use agentbus", "cwd": t.TempDir()}); got != "" {
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

func TestSubagentMarker(t *testing.T) {
	for p, want := range map[string]bool{
		"use agentbus":                   true,
		"You should use the agentbus":    true,
		"register on agentbus":           true,
		"register with agentbus":         true,
		"agentbus is the repo name":      false,
		"read internal/agentbus/foo.go":  false,
		"misuse agentbus":                false,
		"register with parent=Lead":      false,
		"register on the agentbus board": true,
	} {
		if got := subagentMarker.MatchString(p); got != want {
			t.Errorf("marker(%q) = %v, want %v", p, got, want)
		}
	}
}
