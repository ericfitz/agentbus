package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
)

// tasksReadOnlyToast is shown when a key that would send or compose is
// pressed with a task channel selected: tasks/ channels are read-only here.
const tasksReadOnlyToast = "task lists are read-only here"

// loadTasks fetches ch's current task tree.
func (m *Model) loadTasks(ch string) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		ts, err := c.b.TaskList(c.as, bus.TaskListInput{Channel: ch})
		return tasksMsg{ch: ch, tasks: ts, err: err}
	}
}

// renderTasks draws ch's task tree in place of the revision stream: one row
// per task in the tree order TaskList returns, indented by depth (capped at
// six levels), with a status mark and dim owner/blocker/lease suffixes.
func (m Model) renderTasks(ch string) string {
	dim := m.theme.Style(m.theme.Dim)
	ts := m.tasks[ch]
	if len(ts) == 0 {
		return dim.Render("no tasks yet in " + ch)
	}
	w := max(m.stream.Width, 20)
	lines := make([]string, len(ts))
	for i, t := range ts {
		mark := "○"
		switch t.Status {
		case "in_progress":
			mark = "◐"
		case "completed":
			mark = "●"
		default: // pending
			if len(t.OpenBlockers) > 0 {
				mark = "⊘"
			}
		}
		line := strings.Repeat("  ", min(t.Depth, 6)) + mark + " #" + itoa(t.ID) + " " + t.Subject
		var suffix string
		if t.Owner != "" {
			suffix += " @" + t.Owner
		}
		if len(t.OpenBlockers) > 0 {
			ids := make([]string, len(t.OpenBlockers))
			for j, id := range t.OpenBlockers {
				ids[j] = "#" + itoa(id)
			}
			suffix += " ⊘ " + strings.Join(ids, " ")
		}
		if t.LeasedUntil != 0 {
			if left := time.Until(time.UnixMilli(t.LeasedUntil)); left > 0 {
				suffix += " lease " + shortDur(left)
			} else {
				suffix += " lease expired"
			}
		}
		if suffix != "" {
			line += dim.Render(suffix)
		}
		if t.Status == "completed" {
			line = dim.Render(line)
		}
		lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(line)
	}
	return strings.Join(lines, "\n")
}
