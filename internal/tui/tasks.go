package tui

import (
	"sort"
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

// loadTask fetches one task's full detail (description, metadata, blockers,
// last update) for its expanded block.
func (m *Model) loadTask(id int64) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		t, err := c.b.TaskGet(c.as, id)
		return taskMsg{task: t, err: err}
	}
}

// cursorTask returns the task at the cursor in the selected task channel.
func (m *Model) cursorTask() (bus.TaskSummary, bool) {
	ts := m.tasks[m.selName()]
	if m.cursor < 0 || m.cursor >= len(ts) {
		return bus.TaskSummary{}, false
	}
	return ts[m.cursor], true
}

// expandTask shows the cursor task's detail block, fetching it with
// task_get. A task with no description, metadata, or blockers (HasDetails
// false) has nothing to show and does not expand.
func (m *Model) expandTask() tea.Cmd {
	t, ok := m.cursorTask()
	if !ok || !t.HasDetails {
		return nil
	}
	m.taskOpen[t.ID] = true
	m.refreshStream()
	return m.loadTask(t.ID)
}

// collapseTask hides the cursor task's detail block.
func (m *Model) collapseTask() {
	t, ok := m.cursorTask()
	if !ok {
		return
	}
	delete(m.taskOpen, t.ID)
	m.refreshStream()
}

// renderTasks draws ch's task tree in place of the revision stream: one row
// per task in the tree order TaskList returns, indented by depth (capped at
// six levels), a collapsed/expanded mark for a task with details, a status
// mark, then the row's suffixes: an in-progress task's owner with the rail
// icon and color, a pending task's assignee as a dim arrow, blockers, and
// the lease. Completed rows are dimmed whole. The normal-mode cursor
// highlights its row; an expanded task's block (description, blockers,
// metadata, last update) follows it, word-wrapped at the task's indent.
func (m *Model) renderTasks(ch string) string {
	th := m.theme
	dim := th.Style(th.Dim)
	ts := m.tasks[ch]
	m.cursorLine = -1
	if len(ts) == 0 {
		return dim.Render("no tasks yet in " + ch)
	}
	w := max(m.stream.Width, 20)
	byID := make(map[int64]bus.TaskSummary, len(ts))
	for _, t := range ts {
		byID[t.ID] = t
	}
	var b strings.Builder
	lineNum := 0
	for i, t := range ts {
		mark := taskPending
		switch t.Status {
		case "in_progress":
			mark = taskInProgress
		case "completed":
			mark = taskCompleted
		}
		selected := i == m.cursor && m.mode == modeNormal
		rowDim := dim
		if selected {
			rowDim = th.Style(th.Text)
		}
		indent := strings.Repeat("  ", min(t.Depth, 6))
		var treeMark string
		switch {
		case t.HasDetails && m.taskOpen[t.ID]:
			treeMark = rowDim.Render(markOpen + " ")
		case t.HasDetails:
			treeMark = rowDim.Render(markSel + " ")
		default:
			treeMark = "  "
		}
		prefix := indent + treeMark
		body := mark + "#" + itoa(t.ID) + " " + t.Subject
		var suffix string
		switch {
		case t.Status == "in_progress" && t.Owner != "":
			icon, style := iconAgent, th.Style(th.Agent)
			if t.Owner == m.c.as {
				icon, style = iconUser, th.Style(th.User)
			}
			body += "  " + icon + style.Render(t.Owner)
		case t.Status == "pending" && t.Owner != "":
			suffix += iconArrow + t.Owner
		}
		if len(t.OpenBlockers) > 0 {
			ids := make([]string, len(t.OpenBlockers))
			for j, id := range t.OpenBlockers {
				ids[j] = "#" + itoa(id)
			}
			suffix += " blocked by " + strings.Join(ids, " ")
		}
		if t.LeasedUntil != 0 {
			if left := time.Until(time.UnixMilli(t.LeasedUntil)); left > 0 {
				suffix += " lease " + shortDur(left)
			} else {
				suffix += " lease expired"
			}
		}
		if suffix != "" {
			body += rowDim.Render(suffix)
		}
		if t.Status == "completed" {
			body = dim.Render(body)
		}
		pw := lipgloss.Width(prefix)
		line := prefix + body
		if selected {
			line = th.Highlight(line, w)
		} else {
			line = lipgloss.NewStyle().MaxWidth(w).Render(line)
		}
		if i == m.cursor {
			m.cursorLine = lineNum
		}
		b.WriteString(line + "\n")
		lineNum++
		if t.HasDetails && m.taskOpen[t.ID] {
			block := m.renderTaskBlock(t, byID, pw+2, w)
			b.WriteString(block + "\n")
			lineNum += strings.Count(block, "\n") + 1
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTaskBlock draws t's expanded detail block, indented by indent
// columns and word-wrapped to w: the description, its blockers (marking
// which are still open, with subjects looked up in byID), metadata as dim
// "key: value" lines in sorted order, and a dim last-update line. Until
// m.taskDetail has the task_get result, it shows a dim "loading…".
func (m *Model) renderTaskBlock(t bus.TaskSummary, byID map[int64]bus.TaskSummary, indent, w int) string {
	th := m.theme
	dim := th.Style(th.Dim)
	pad := strings.Repeat(" ", indent)
	detail, ok := m.taskDetail[t.ID]
	var content string
	if !ok {
		content = dim.Render("loading…")
	} else {
		var lines []string
		if detail.Description != "" {
			lines = append(lines, detail.Description)
		}
		for _, bid := range detail.BlockedBy {
			subj := "#" + itoa(bid)
			if s, ok := byID[bid]; ok {
				subj = "#" + itoa(bid) + " " + s.Subject
			}
			status := "done"
			for _, o := range detail.OpenBlockers {
				if o == bid {
					status = "open"
				}
			}
			lines = append(lines, "blocked by "+subj+" ("+status+")")
		}
		keys := make([]string, 0, len(detail.Metadata))
		for k := range detail.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lines = append(lines, dim.Render(k+": "+detail.Metadata[k]))
		}
		lines = append(lines, dim.Render("updated by "+senderName(detail.UpdatedBy)+" · "+stamp(detail.UpdatedAt)))
		content = strings.Join(lines, "\n")
	}
	wrapped := lipgloss.NewStyle().Width(max(w-indent, 1)).Render(content)
	return pad + strings.ReplaceAll(wrapped, "\n", "\n"+pad)
}
