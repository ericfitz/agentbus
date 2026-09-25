package tui

import (
	"sort"
	"strconv"
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

// taskRow is one visible row of a task tree.
type taskRow struct {
	t      bus.TaskSummary
	hidden int  // descendants not shown under this row (a summary line follows)
	open   bool // at least one direct child is shown
}

// taskRows projects ch's tree (TaskList's order: a parent before its
// children) onto the rows shown. Children start collapsed (ADR 0011
// decision 3): a task is shown only when its parent is shown with its
// children expanded (m.taskExpanded); otherwise the nearest shown ancestor
// counts it as hidden. A task whose parent is not in the list is a root.
func (m *Model) taskRows(ch string) []taskRow {
	var out []taskRow
	at := map[int64]int{}    // shown task id -> its row index
	under := map[int64]int{} // hidden task id -> the row index counting it
	for _, t := range m.tasks[ch] {
		pi, shown := at[t.Parent]
		hi, hidden := under[t.Parent]
		switch {
		case shown && !m.taskExpanded[t.Parent]:
			out[pi].hidden++
			under[t.ID] = pi
			continue
		case shown:
			out[pi].open = true
		case hidden:
			out[hi].hidden++
			under[t.ID] = hi
			continue
		}
		at[t.ID] = len(out)
		out = append(out, taskRow{t: t})
	}
	return out
}

// taskIndex is id's row index in taskRows(ch), or -1 while it is hidden or
// absent.
func (m *Model) taskIndex(ch string, id int64) int {
	for i, r := range m.taskRows(ch) {
		if r.t.ID == id {
			return i
		}
	}
	return -1
}

// revealTask shows the children of every ancestor of id so its row is
// visible (a search jump or pending cursor into a collapsed subtree).
func (m *Model) revealTask(ch string, id int64) {
	byID := make(map[int64]bus.TaskSummary, len(m.tasks[ch]))
	for _, t := range m.tasks[ch] {
		byID[t.ID] = t
	}
	p := byID[id].Parent
	for n := 0; p != 0 && n < len(byID); n++ { // bounded: the bus forbids cycles, this never trusts it
		m.taskExpanded[p] = true
		p = byID[p].Parent
	}
}

// cursorTaskRow returns the visible row under the cursor in the selected
// task channel.
func (m *Model) cursorTaskRow() (taskRow, bool) {
	rs := m.taskRows(m.selName())
	if m.cursor < 0 || m.cursor >= len(rs) {
		return taskRow{}, false
	}
	return rs[m.cursor], true
}

// cursorTask returns the task at the cursor in the selected task channel.
func (m *Model) cursorTask() (bus.TaskSummary, bool) {
	r, ok := m.cursorTaskRow()
	return r.t, ok
}

// openTask (→) opens the cursor task's details when it has any and they
// are closed; otherwise it shows the task's direct children.
func (m *Model) openTask() tea.Cmd {
	r, ok := m.cursorTaskRow()
	if !ok {
		return nil
	}
	if r.t.HasDetails && !m.taskOpen[r.t.ID] {
		return m.expandTask()
	}
	if r.hidden > 0 {
		m.taskExpanded[r.t.ID] = true
		m.refreshStream()
		m.scrollCursorIntoView()
	}
	return nil
}

// closeTask (←) hides the cursor task's shown children, the whole subtree
// (so the next → shows direct children only); otherwise it closes the
// task's details.
func (m *Model) closeTask() {
	r, ok := m.cursorTaskRow()
	if !ok {
		return
	}
	if !r.open {
		m.collapseTask()
		return
	}
	under := map[int64]bool{r.t.ID: true}
	for _, t := range m.tasks[m.selName()] { // tree order: a parent precedes its children
		if under[t.Parent] {
			under[t.ID] = true
			delete(m.taskExpanded, t.ID)
		}
	}
	delete(m.taskExpanded, r.t.ID)
	m.refreshStream()
	m.scrollCursorIntoView()
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
// per visible task (taskRows; children start collapsed), indented by depth
// (capped at six levels), a tree mark, a status mark, then the row's
// suffixes: an in-progress task's owner with the rail icon and color, a
// pending task's assignee as a dim arrow, blockers, and the lease. Completed
// rows are dimmed whole. The tree mark is ▶ when anything is hidden (closed
// details, hidden children), ▼ when details or children are open with
// nothing hidden, blank otherwise; a row with hidden children is followed
// by a dim "N subtasks" line. The normal-mode cursor highlights its row; an
// expanded task's block (description, blockers, metadata, last update)
// follows it, word-wrapped at the task's indent.
func (m *Model) renderTasks(ch string) string {
	th := m.theme
	dim := th.Style(th.Dim)
	rs := m.taskRows(ch)
	m.cursorLine = -1
	if len(rs) == 0 {
		return dim.Render("no tasks yet in " + ch)
	}
	w := max(m.stream.Width, 20)
	byID := make(map[int64]bus.TaskSummary, len(m.tasks[ch]))
	for _, t := range m.tasks[ch] {
		byID[t.ID] = t
	}
	var b strings.Builder
	lineNum := 0
	for i, r := range rs {
		t := r.t
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
		detailsOpen := t.HasDetails && m.taskOpen[t.ID]
		var treeMark string
		switch {
		case (t.HasDetails && !detailsOpen) || r.hidden > 0:
			treeMark = rowDim.Render(markSel + " ")
		case detailsOpen || r.open:
			treeMark = rowDim.Render(markOpen + " ")
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
			body = rowDim.Render(body)
		}
		pw := lipgloss.Width(prefix)
		line := prefix + body
		if r.hidden > 0 {
			summary := strconv.Itoa(r.hidden) + " subtasks"
			if r.hidden == 1 {
				summary = "1 subtask"
			}
			line += "\n" + strings.Repeat(" ", pw) + rowDim.Render(summary)
		}
		line = lipgloss.NewStyle().MaxWidth(w).Render(line)
		if selected {
			line = th.Highlight(th.Sel, line, w)
		}
		if i == m.cursor {
			m.cursorLine = lineNum
		}
		b.WriteString(line + "\n")
		lineNum += strings.Count(line, "\n") + 1
		if detailsOpen {
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
