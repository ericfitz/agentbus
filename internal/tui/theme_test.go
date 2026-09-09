package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestParseColorAcceptsNamesIndicesAndDefault(t *testing.T) {
	cases := map[string]lipgloss.TerminalColor{
		"cyan":        lipgloss.Color("6"),
		"CYAN":        lipgloss.Color("6"),
		"brightblack": lipgloss.Color("8"),
		"BrightWhite": lipgloss.Color("15"),
		"0":           lipgloss.Color("0"),
		"15":          lipgloss.Color("15"),
		"default":     lipgloss.NoColor{},
		" yellow ":    lipgloss.Color("3"),
	}
	for in, want := range cases {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("ParseColor(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestParseColorRejectsHexAndJunk(t *testing.T) {
	for _, in := range []string{"#f00", "#ff0000", "16", "-1", "orange", ""} {
		if _, err := ParseColor(in); err == nil {
			t.Errorf("ParseColor(%q) accepted", in)
		}
	}
}

func TestLoadThemeConfigThenEnvOverrideAndReportsBadValues(t *testing.T) {
	cfg := config.Default()
	cfg.TUIUserColor = "Blue" // config value, mixed case
	cfg.TUIMemoryColor = "white"
	env := map[string]string{
		"AGENTBUS_TUI_AGENTCOLOR": "green",
		"AGENTBUS_TUI_MEMCOLOR":   "#ff00ff",
	}
	var warn bytes.Buffer
	th := LoadTheme(cfg, func(k string) string { return env[k] }, &warn)
	if th.Agent != lipgloss.Color("2") {
		t.Fatalf("env override not applied: %v", th.Agent)
	}
	if th.Mem != lipgloss.Color("7") {
		t.Fatalf("bad env value must keep the config value white, got %v", th.Mem)
	}
	if th.User != lipgloss.Color("4") || th.Dim != lipgloss.Color("8") || th.BG != (lipgloss.NoColor{}) {
		t.Fatalf("config values and defaults wrong: %+v", th)
	}
	out := warn.String()
	if !strings.Contains(out, "AGENTBUS_TUI_MEMCOLOR") || !strings.Contains(out, "brightblack") || !strings.Contains(out, "using white") {
		t.Fatalf("warning must name the variable, the accepted forms, and the value kept, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("exactly one line per bad variable, got %q", out)
	}
	if th.Sources["tui_agent_color"] != "green" || th.Sources["tui_memory_color"] != "white" || th.Sources["tui_user_color"] != "blue" {
		t.Fatalf("sources wrong: %v", th.Sources)
	}
	if th.Overrides["tui_agent_color"] != "AGENTBUS_TUI_AGENTCOLOR" || th.Overrides["tui_memory_color"] != "" {
		t.Fatalf("overrides wrong: %v", th.Overrides)
	}
}
