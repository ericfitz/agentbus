// Package tui is the agentbus terminal dashboard: a chat-client view of the
// bus that a human reads and posts to, running in-process on the bus
// package like the status and reset commands do.
package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/config"
)

// Theme holds one terminal color per role. Only ANSI indices 0-15 and the
// terminal default are representable in v1, on purpose (see
// config.ColorIndex).
type Theme struct {
	BG, Text, Dim, Agent, User, Mem, Health, Warn, Error, Sel lipgloss.TerminalColor
	// Sources records the resolved value per config key ("cyan", "default",
	// ...) and Overrides names the environment variable that replaced the
	// config value, for the Health overlay.
	Sources   map[string]string
	Overrides map[string]string
}

type themeVar struct {
	key string // config key, e.g. tui_agent_color
	env string // environment variable that overrides it
	set func(*Theme, lipgloss.TerminalColor)
}

var themeVars = []themeVar{
	{"tui_background_color", "AGENTBUS_TUI_BGCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.BG = c }},
	{"tui_text_color", "AGENTBUS_TUI_TEXTCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Text = c }},
	{"tui_dim_color", "AGENTBUS_TUI_DIMCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Dim = c }},
	{"tui_agent_color", "AGENTBUS_TUI_AGENTCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Agent = c }},
	{"tui_user_color", "AGENTBUS_TUI_USERCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.User = c }},
	{"tui_memory_color", "AGENTBUS_TUI_MEMCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Mem = c }},
	{"tui_health_color", "AGENTBUS_TUI_HEALTHCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Health = c }},
	{"tui_warn_color", "AGENTBUS_TUI_WARNCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Warn = c }},
	{"tui_error_color", "AGENTBUS_TUI_ERRORCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Error = c }},
	{"tui_selection_color", "AGENTBUS_TUI_SELCOLOR", func(t *Theme, c lipgloss.TerminalColor) { t.Sel = c }},
}

// ParseColor is config.ColorIndex as a Lip Gloss color.
func ParseColor(s string) (lipgloss.TerminalColor, error) {
	n, err := config.ColorIndex(s)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return lipgloss.NoColor{}, nil
	}
	return lipgloss.Color(strconv.Itoa(n)), nil
}

// LoadTheme starts from the validated tui_*_color config values and lets
// each AGENTBUS_TUI_*COLOR environment variable, read through getenv,
// override its key. A bad variable prints one line to warn naming it and
// the accepted forms, then keeps the config value; the TUI still starts.
func LoadTheme(cfg config.Config, getenv func(string) string, warn io.Writer) Theme {
	t := Theme{Sources: map[string]string{}, Overrides: map[string]string{}}
	values := map[string]string{}
	for _, kv := range cfg.TUIColors() {
		values[kv[0]] = kv[1]
	}
	for _, v := range themeVars {
		value := strings.ToLower(strings.TrimSpace(values[v.key]))
		if raw := getenv(v.env); raw != "" {
			if _, err := ParseColor(raw); err != nil {
				_, _ = fmt.Fprintf(warn, "agentbus tui: %s=%q is not a color (%v); accepted: %s; using %s\n", v.env, raw, err, config.ColorForms, value)
			} else {
				value = strings.ToLower(strings.TrimSpace(raw))
				t.Overrides[v.key] = v.env
			}
		}
		c, _ := ParseColor(value) // the config value and the validated raw both parse
		v.set(&t, c)
		t.Sources[v.key] = value
	}
	return t
}

// Style is a foreground-only style in color c.
func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c)
}
