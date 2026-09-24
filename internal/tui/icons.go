package tui

import (
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/config"
)

// iconVars maps each icon name an icon_map may use to the variable it sets.
var iconVars = map[string]*string{
	"chat": &iconChat, "memory": &iconMem, "agent": &iconAgent, "user": &iconUser,
	"idle": &iconIdle, "tasks": &iconTasks, "arrow": &iconArrow, "error": &iconError,
	"task_pending": &taskPending, "task_in_progress": &taskInProgress, "task_completed": &taskCompleted,
	"collapsed": &markSel, "expanded": &markOpen,
}

// emojiIcons is the default set, captured from the variables' initial values.
var emojiIcons = func() map[string]string {
	s := map[string]string{}
	for k, p := range iconVars {
		s[k] = *p
	}
	return s
}()

// nerdIcons is the user's preferred Font Awesome iconography (issue #17
// attachment agentbus-tui-icon-map.md) at the codepoints Nerd Fonts 3.x
// gives it. Nerd Fonts carries Font Awesome Free in its "regular" (FA4 _o)
// or solid form; the comment names the Nerd Font glyph where it differs
// from the requested FA6 name.
var nerdIcons = map[string]string{
	"chat":             "",          // fa-message
	"memory":           "",          // fa-floppy-disk (nf fa-save)
	"tasks":            "",          // fa-list-check (nf fa-tasks, its FA5 name)
	"agent":            "",          // fa-robot
	"user":             "",          // fa-user (solid; no regular user in Nerd Fonts)
	"idle":             "\U000f04b2", // md-sleep "zZ"; fa-face-sleeping is Pro-only
	"task_pending":     "",          // fa-square regular (nf fa-square_o)
	"task_in_progress": "",          // fa-square-caret-right regular (nf fa-toggle_right)
	"task_completed":   "",          // fa-square-check regular (nf fa-check_square_o)
	"collapsed":        "",          // fa-caret-right
	"expanded":         "",          // fa-caret-down
	"error":            "",          // fa-circle-exclamation (nf fa-exclamation_circle)
	"arrow":            "",          // fa-arrow-right
}

// padIcon pads a glyph to its emoji counterpart's layout: the wide icons
// take three columns (two for the emoji, one space) so rails and task rows
// line up whichever set is active; the arrow gets a space on each side, the
// error prefix one trailing space, and the thread marks stay bare.
func padIcon(name, g string) string {
	switch name {
	case "arrow":
		return " " + g + " "
	case "error":
		return g + " "
	case "collapsed", "expanded":
		return g
	}
	return g + strings.Repeat(" ", max(3-lipgloss.Width(g), 1))
}

func setIcons(set map[string]string) {
	for k, p := range iconVars {
		*p = set[k]
	}
}

// LoadIcons applies cfg.Icons: emoji (default), nerdfont, or custom (a
// backward-compatible alias for emoji), then applies icon_map on top of
// whichever base set was chosen, overriding any subset of it. An unknown
// set, an unknown icon_map name, or an empty value warns one line and skips
// that entry (leaving the base set's icon in place), like LoadTheme.
func LoadIcons(cfg config.Config, warn io.Writer) string {
	name := strings.ToLower(strings.TrimSpace(cfg.Icons))
	set := maps.Clone(emojiIcons)
	switch name {
	case "", "emoji", "custom":
		if name == "" {
			name = "emoji"
		}
	case "nerdfont":
		set = map[string]string{}
		for k, g := range nerdIcons {
			set[k] = padIcon(k, g)
		}
	default:
		_, _ = fmt.Fprintf(warn, "agentbus tui: icons %q is not emoji, nerdfont, or custom; using emoji\n", cfg.Icons)
		name = "emoji"
	}
	for k, g := range cfg.IconMap {
		if _, ok := emojiIcons[k]; !ok || g == "" {
			_, _ = fmt.Fprintf(warn, "agentbus tui: icon_map %q=%q is not a known icon with a value; keeping the current icon\n", k, g)
			continue
		}
		set[k] = padIcon(k, g)
	}
	setIcons(set)
	return name
}
