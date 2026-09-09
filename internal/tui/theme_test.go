package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

func TestLoadThemeUsesDefaultsAndReportsBadValues(t *testing.T) {
	env := map[string]string{
		"AGENTBUS_TUI_AGENTCOLOR": "green",
		"AGENTBUS_TUI_MEMCOLOR":   "#ff00ff",
	}
	var warn bytes.Buffer
	th := LoadTheme(func(k string) string { return env[k] }, &warn)
	if th.Agent != lipgloss.Color("2") {
		t.Fatalf("agent colour not applied: %v", th.Agent)
	}
	if th.Mem != lipgloss.Color("5") {
		t.Fatalf("bad value must fall back to default magenta, got %v", th.Mem)
	}
	if th.User != lipgloss.Color("3") || th.Dim != lipgloss.Color("8") || th.BG != (lipgloss.NoColor{}) {
		t.Fatalf("defaults wrong: %+v", th)
	}
	out := warn.String()
	if !strings.Contains(out, "AGENTBUS_TUI_MEMCOLOR") || !strings.Contains(out, "brightblack") || !strings.Contains(out, "default") {
		t.Fatalf("warning must name the variable and the accepted forms, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("exactly one line per bad variable, got %q", out)
	}
	if th.Sources["AGENTBUS_TUI_AGENTCOLOR"] != "green" || th.Sources["AGENTBUS_TUI_MEMCOLOR"] != "magenta" {
		t.Fatalf("sources wrong: %v", th.Sources)
	}
}
