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

func TestLoadThemeByNameWithDefaultsForMissingBadAndUnknown(t *testing.T) {
	cfg := config.Default()
	cfg.Theme = "night"
	cfg.Themes = append(cfg.Themes, config.Theme{Name: "night", User: "Blue", Agent: "green", Memory: "#ff00ff"})
	var warn bytes.Buffer
	th := LoadTheme(cfg, &warn)
	if th.Name != "night" || th.Agent != lipgloss.Color("2") || th.User != lipgloss.Color("4") {
		t.Fatalf("named theme not applied: %+v", th)
	}
	if th.Mem != lipgloss.Color("5") {
		t.Fatalf("bad value must fall back to the default magenta, got %v", th.Mem)
	}
	if th.Dim != lipgloss.Color("8") || th.BG != (lipgloss.NoColor{}) {
		t.Fatalf("missing values must fall back to the defaults: %+v", th)
	}
	out := warn.String()
	if !strings.Contains(out, "night") || !strings.Contains(out, "memory") || !strings.Contains(out, "brightblack") || !strings.Contains(out, "using magenta") {
		t.Fatalf("warning must name the theme, the key, the accepted forms, and the value used, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("exactly one line per bad value, none for missing ones, got %q", out)
	}
	if th.Sources["agent"] != "green" || th.Sources["memory"] != "magenta" || th.Sources["user"] != "blue" || th.Sources["dim"] != "brightblack" {
		t.Fatalf("sources wrong: %v", th.Sources)
	}

	cfg.Theme = "nope"
	warn.Reset()
	th = LoadTheme(cfg, &warn)
	if th.Name != "default" || th.Agent != lipgloss.Color("6") || !strings.Contains(warn.String(), `"nope"`) {
		t.Fatalf("unknown theme name must warn and use the default theme: %+v %q", th, warn.String())
	}
}
