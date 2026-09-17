package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) openHelp() tea.Cmd {
	m.mode = modeHelp
	m.helpScroll = 0
	return nil
}

func (m *Model) updateHelp(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc", "?", "q":
		m.mode = modeNormal
	case "up":
		m.helpScroll = max(m.helpScroll-1, 0)
	case "down":
		m.helpScroll = min(m.helpScroll+1, max(len(m.helpLines())-1, 0))
	}
	return nil
}

// helpLines is the full keymap. Keys the status bar cannot fit (shift+tab,
// home, the compose editing keys) live only here.
func (m Model) helpLines() []string {
	rows := [][2]string{
		{"tab / shift+tab", "next / previous pane (channels, sessions, messages, compose)"},
		{"home", "channel list"},
		{"esc", "leave compose; clear the message cursor"},
		{"i", "compose (in the sessions pane, sends a direct message)"},
		{"enter", "reply to the cursor message, else compose"},
		{"↑ / ↓", "previous / next in the focused pane"},
		{"→ / ←", "show the cursor message's replies / hide its whole subtree"},
		{"pgup / pgdn", "scroll (pgup at the top loads older history)"},
		{"g / G", "oldest / newest"},
		{"space", "show / hide the replies under the cursor message"},
		{"r", "reply to the cursor message (own inbox only, in the sessions pane)"},
		{"c", "new channel: <name> [memory]"},
		{"s", "subscribe / unsubscribe the channel (no-op in the sessions pane)"},
		{"d", "delete the channel and all its messages (asks first; no-op in the sessions pane)"},
		{"/", "search (tab cycles text / semantic / both)"},
		{"m", "memory browser"},
		{"h", "health and config"},
		{"?", "this help"},
		{"q", "quit"},
		{"", ""},
		{"compose", ""},
		{"enter", "send (a memory channel creates a memory)"},
		{"alt+enter", "newline"},
		{"↑", "recall the last sent message"},
		{"ctrl+u", "clear"},
	}
	key, dim := m.theme.Style(m.theme.Agent), m.theme.Style(m.theme.Dim)
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		if r[1] == "" {
			lines = append(lines, dim.Render(r[0]))
			continue
		}
		lines = append(lines, key.Render(r[0])+strings.Repeat(" ", max(18-len([]rune(r[0])), 1))+r[1])
	}
	return lines
}

func (m Model) viewHelp() string {
	lines := m.helpLines()
	if m.helpScroll < len(lines) {
		lines = lines[m.helpScroll:]
	}
	return m.overlay("help", m.theme.Agent, strings.Join(lines, "\n"), m.hints("↑↓", "scroll", "esc", "close"))
}
