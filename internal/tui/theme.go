// Package tui is the agentbus terminal dashboard: a chat-client view of the
// bus that a human reads and posts to, running in-process on the bus
// package like the status and reset commands do.
package tui

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds one terminal colour per role. Only ANSI indices 0-15 and the
// terminal default are representable in v1, on purpose: the parser rejects
// #RGB so a later version can add it without silently changing behaviour.
type Theme struct {
	BG, Text, Dim, Agent, User, Mem, Health, Warn, Error, Sel lipgloss.TerminalColor
	// Sources records the resolved value per environment variable for the
	// Health overlay ("cyan", "default", ...).
	Sources map[string]string
}

type themeVar struct {
	env string
	def string // parseable default
	set func(*Theme, lipgloss.TerminalColor)
}

var themeVars = []themeVar{
	{"AGENTBUS_TUI_BGCOLOR", "default", func(t *Theme, c lipgloss.TerminalColor) { t.BG = c }},
	{"AGENTBUS_TUI_TEXTCOLOR", "default", func(t *Theme, c lipgloss.TerminalColor) { t.Text = c }},
	{"AGENTBUS_TUI_DIMCOLOR", "brightblack", func(t *Theme, c lipgloss.TerminalColor) { t.Dim = c }},
	{"AGENTBUS_TUI_AGENTCOLOR", "cyan", func(t *Theme, c lipgloss.TerminalColor) { t.Agent = c }},
	{"AGENTBUS_TUI_USERCOLOR", "yellow", func(t *Theme, c lipgloss.TerminalColor) { t.User = c }},
	{"AGENTBUS_TUI_MEMCOLOR", "magenta", func(t *Theme, c lipgloss.TerminalColor) { t.Mem = c }},
	{"AGENTBUS_TUI_HEALTHCOLOR", "green", func(t *Theme, c lipgloss.TerminalColor) { t.Health = c }},
	{"AGENTBUS_TUI_WARNCOLOR", "yellow", func(t *Theme, c lipgloss.TerminalColor) { t.Warn = c }},
	{"AGENTBUS_TUI_ERRORCOLOR", "red", func(t *Theme, c lipgloss.TerminalColor) { t.Error = c }},
	{"AGENTBUS_TUI_SELCOLOR", "brightblack", func(t *Theme, c lipgloss.TerminalColor) { t.Sel = c }},
}

var ansiNames = []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// ParseColor accepts the sixteen ANSI names (bright forms prefixed with
// "bright"), the integers 0-15, and "default"; case-insensitive, surrounding
// whitespace ignored.
func ParseColor(s string) (lipgloss.TerminalColor, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "default" {
		return lipgloss.NoColor{}, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 15 {
			return nil, errors.New("index out of range")
		}
		return lipgloss.Color(strconv.Itoa(n)), nil
	}
	base, bright := strings.CutPrefix(v, "bright")
	for i, name := range ansiNames {
		if base == name {
			if bright {
				i += 8
			}
			return lipgloss.Color(strconv.Itoa(i)), nil
		}
	}
	return nil, errors.New("unknown colour")
}

// LoadTheme reads every theme variable through getenv. A bad value prints one
// line to warn naming the variable and the accepted forms, then keeps the
// role's default; the TUI still starts.
func LoadTheme(getenv func(string) string, warn io.Writer) Theme {
	t := Theme{Sources: map[string]string{}}
	for _, v := range themeVars {
		value := v.def
		if raw := getenv(v.env); raw != "" {
			if _, err := ParseColor(raw); err != nil {
				_, _ = fmt.Fprintf(warn, "agentbus tui: %s=%q is not a colour (%v); accepted: black, red, green, yellow, blue, magenta, cyan, white, their bright forms such as brightblack, 0-15, or default; using %s\n", v.env, raw, err, v.def)
			} else {
				value = strings.ToLower(strings.TrimSpace(raw))
			}
		}
		c, _ := ParseColor(value) // v.def and the validated raw both parse
		v.set(&t, c)
		t.Sources[v.env] = value
	}
	return t
}

// Style is a foreground-only style in colour c.
func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c)
}
