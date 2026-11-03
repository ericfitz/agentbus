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

// normWS collapses whitespace so assertions survive reflowing.
func normWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestAgentFacingTextHasNoBannedPhrases guards ADR 0025: no agent-facing text
// models the retired Monitor/watch/-filter advice.
func TestAgentFacingTextHasNoBannedPhrases(t *testing.T) {
	banned := []string{"Monitor", "agentbus watch -filter", "-filter @", "timeout_ms", "under the Monitor"}
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cwd := t.TempDir()
	me := filepath.Base(cwd)
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register(me, "", "", false); err != nil {
		t.Fatal(err)
	}
	other, err := b.Register("Other", me, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(other.Sender, bus.SendInput{Channel: "dm/" + me, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	hookIn, _ := json.Marshal(map[string]any{"cwd": cwd, "agent_type": "general-purpose"})
	var stop, sub bytes.Buffer
	StopHook(cfg, bytes.NewReader(hookIn), &stop, io.Discard)
	SubagentHook(cfg, bytes.NewReader(hookIn), &sub, io.Discard)
	if stop.Len() == 0 || sub.Len() == 0 {
		t.Fatalf("hooks printed nothing: stop=%q sub=%q", stop.String(), sub.String())
	}
	texts := map[string]string{
		"protocol":      protocol,
		"InitPrompt":    InitPrompt,
		"SKILL.md":      string(skillMD),
		"stop-hook":     stop.String(),
		"subagent-hook": sub.String(),
	}
	for name, text := range texts {
		for _, bad := range banned {
			if strings.Contains(text, bad) {
				t.Errorf("%s mentions %q", name, bad)
			}
		}
	}
}

// TestAgentTextStatusProtocol pins the status protocol text (ADR 0026): the
// skill's Status section, the park rule, and the SessionStart protocol lines.
func TestAgentTextStatusProtocol(t *testing.T) {
	skill := normWS(string(skillMD))
	for _, want := range []string{
		"## Status", "set_status", "status_history",
		"Set `idle` before `agentbus park`",
		"Subagents do not set a status",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	for _, tag := range bus.SettableStatusTags {
		if !strings.Contains(skill, "`"+tag+"`") {
			t.Errorf("SKILL.md lacks tag %q", tag)
		}
	}
	p := normWS(protocol)
	for _, want := range []string{
		"Call set_status (tag, one-line text, optional problem) at the start of each task and when the kind of work changes; set idle when you have nothing in hand.",
		`set idle with set_status first, then run "agentbus park"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("protocol lacks %q", want)
		}
	}
	if strings.Index(p, "set idle with set_status") > strings.Index(p, `then run "agentbus park"`) {
		t.Error("protocol: idle must come before agentbus park")
	}
}
