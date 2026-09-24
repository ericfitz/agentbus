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
	BG, Text, Dim, Stamp, Tag, Agent, User, Mem, Tasks, Health, Warn, Error, Sel lipgloss.TerminalColor
	// Name is the config theme applied and Sources the resolved value per
	// color key ("cyan", "default", ...), for the Health overlay.
	Name    string
	Sources map[string]string
	// IconSet is the active icon set name (LoadIcons's return value), for
	// the Health overlay.
	IconSet string
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

// LoadTheme applies the entry of cfg.Themes named cfg.Theme. A name that
// is not in the list, and any value that is empty or not a color, falls
// back to config.DefaultTheme; a bad value prints one line to warn naming
// it and the accepted forms. The TUI always starts.
func LoadTheme(cfg config.Config, warn io.Writer) Theme {
	def := config.DefaultTheme()
	sel, ok := cfg.FindTheme(cfg.Theme)
	if !ok {
		_, _ = fmt.Fprintf(warn, "agentbus tui: theme %q is not in themes; using %s\n", cfg.Theme, def.Name)
		sel = def
	}
	defaults := map[string]string{}
	for _, kv := range def.Colors() {
		defaults[kv[0]] = kv[1]
	}
	t := Theme{Name: sel.Name, Sources: map[string]string{}}
	for _, kv := range sel.Colors() {
		key, value := kv[0], strings.ToLower(strings.TrimSpace(kv[1]))
		if value == "" {
			value = defaults[key]
		} else if _, err := ParseColor(value); err != nil {
			_, _ = fmt.Fprintf(warn, "agentbus tui: theme %q %s=%q is not a color (%v); accepted: %s; using %s\n", sel.Name, key, kv[1], err, config.ColorForms, defaults[key])
			value = defaults[key]
		}
		c, _ := ParseColor(value) // defaults and validated values both parse
		t.set(key, c)
		t.Sources[key] = value
	}
	return t
}

func (t *Theme) set(key string, c lipgloss.TerminalColor) {
	switch key {
	case "background":
		t.BG = c
	case "text":
		t.Text = c
	case "dim":
		t.Dim = c
	case "timestamp":
		t.Stamp = c
	case "tag":
		t.Tag = c
	case "agent":
		t.Agent = c
	case "user":
		t.User = c
	case "memory":
		t.Mem = c
	case "tasks":
		t.Tasks = c
	case "health":
		t.Health = c
	case "warn":
		t.Warn = c
	case "error":
		t.Error = c
	case "selection":
		t.Sel = c
	}
}

// Style is a foreground-only style in color c.
func (t Theme) Style(c lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c)
}

// Highlight renders line on the selection background. Styled segments
// inside the line end with a reset that would drop the background for the
// rest of that line, so the background is re-applied after every reset.
func (t Theme) Highlight(line string, width int) string {
	bg := lipgloss.NewStyle().Background(t.Sel)
	if pre, _, ok := strings.Cut(bg.Render("\x00"), "\x00"); ok && pre != "" {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+pre)
	}
	return bg.Width(width).Render(line)
}
