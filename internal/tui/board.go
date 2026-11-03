package tui

import (
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/cli"
)

// boardState is the status board's view state. It lives only for this TUI
// run (ADR 0024 amendment): nothing here is saved.
type boardState struct {
	sort     string          // "time", "name" or "tag"; "" is "time"
	sel      int             // index of the selected row in the sorted entries; the fallback when selID leaves the board
	selID    string          // the selected agent: the selection follows it as the order changes
	heading  bool            // the sessions heading itself was chosen (not a session row under a collapsed section)
	expanded map[string]bool // identity -> its full text and problem are shown below its row
	history  string          //nolint:unused // Task 8: the identity whose history view is open, "" when off
	focus    bool            // the board holds keyboard focus (tab), not the rail
}

const (
	boardTagWidth = 12 // "implementing"
	boardAgeWidth = 4
	boardNameMax  = 24
)

// boardActive is true while the sessions heading is selected: the main pane
// then shows the status board. The other headings keep the last selected
// content (ADR 0024 amendment, decision 8).
func (m *Model) boardActive() bool { return m.headSel == secSessions && m.boardState.heading }

// boardFocused is true when keys go to the board rows.
func (m *Model) boardFocused() bool { return m.boardActive() && m.boardState.focus }

func (m *Model) boardSortMode() string {
	if m.boardState.sort == "" {
		return "time"
	}
	return m.boardState.sort
}

// sortedBoard is a copy of m.board in the current sort order.
func (m *Model) sortedBoard() []bus.BoardEntry {
	e := slices.Clone(m.board)
	_ = cli.SortBoard(e, m.boardSortMode()) // the mode only ever comes from boardSortModes
	return e
}

// clampBoardSel keeps the board selection on a row.
func (m *Model) clampBoardSel() { m.boardSelIndex(m.sortedBoard()) }

// boardSelIndex is the selected row in entries: the selected agent's row,
// else (it left the board) the row at its last index, clamped. It records
// the result.
func (m *Model) boardSelIndex(entries []bus.BoardEntry) int {
	if len(entries) == 0 {
		m.boardState.sel = 0
		return 0
	}
	i := slices.IndexFunc(entries, func(e bus.BoardEntry) bool { return e.Identity == m.boardState.selID })
	if i < 0 {
		i = min(max(m.boardState.sel, 0), len(entries)-1)
	}
	m.boardState.sel, m.boardState.selID = i, entries[i].Identity
	return i
}

// boardTagLabel is the tag as the board shows it: — for a live agent that
// never set a status.
func boardTagLabel(e bus.BoardEntry) string {
	if e.Tag == "" {
		return "—"
	}
	return e.Tag
}

// fit cuts s to w display columns with … and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = cut(s, w, "…")
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// renderBoard draws the board in a width x height window, scrolled to keep
// the selected row visible. Every line is at most width display columns.
func (m *Model) renderBoard(width, height int) string {
	dim := m.theme.Style(m.theme.Dim)
	entries := m.sortedBoard()
	if len(entries) == 0 {
		return dim.Render(cut("no agents", width, "…"))
	}
	sel := m.boardSelIndex(entries)
	nameW := 0
	for _, e := range entries {
		nameW = max(nameW, ansi.StringWidth(e.Identity))
	}
	nameW = min(nameW, boardNameMax)
	// icon (2 columns) + name + tag + age, each followed by a space.
	fixed := 2 + 1 + nameW + 1 + boardTagWidth + 1 + boardAgeWidth + 1
	rest := max(width-fixed, 0)
	textW := rest * 6 / 10
	probW := max(rest-textW-1, 0)
	now := time.Now()

	var lines []string
	selTop, selEnd := 0, 0
	for i, e := range entries {
		age := "—"
		if e.SetAt > 0 {
			age = bus.ShortAge(now.Sub(time.UnixMilli(e.SetAt)))
		}
		icon := statusIcon(e.Tag)
		if !e.Live && e.Tag == "" {
			icon = statusIcon(bus.StatusOffline)
		}
		icon = fit(strings.TrimRight(icon, " "), 2)
		row := icon + " " + fit(e.Identity, nameW) + " " + fit(boardTagLabel(e), boardTagWidth) + " " + fit(age, boardAgeWidth)
		if rest > 0 {
			row += " " + fit(e.Text, textW)
			if probW > 0 {
				row += " " + fit(e.Problem, probW)
			}
		}
		row = strings.TrimRight(cut(row, width, "…"), " ")
		block := []string{row}
		if m.boardState.expanded[e.Identity] {
			block = append(block, wrapBoardDetail(e, width, fixed)...)
		}
		if i == sel {
			selTop, selEnd = len(lines), len(lines)+len(block)
			if m.boardFocused() {
				block[0] = m.theme.Highlight(m.theme.Sel, row, width)
			} else {
				block[0] = m.theme.Highlight(m.theme.SelInactive, row, width)
			}
		}
		lines = append(lines, block...)
	}
	if height > 0 && len(lines) > height {
		top := min(max(selEnd-height, 0), len(lines)-height)
		top = min(top, selTop)
		lines = lines[top : top+height]
	}
	return strings.Join(lines, "\n")
}

// wrapBoardDetail is the expanded text and problem of e, wrapped to fit
// width and indented to the text column.
func wrapBoardDetail(e bus.BoardEntry, width, indent int) []string {
	indent = min(indent, max(width/2, 0))
	room := max(width-indent, 1)
	text := e.Text
	if e.Problem != "" {
		text += "\nproblem: " + e.Problem
	}
	var out []string
	pad := strings.Repeat(" ", indent)
	for _, para := range strings.Split(text, "\n") {
		for _, l := range strings.Split(ansi.Hardwrap(ansi.Wrap(para, room, ""), room, true), "\n") {
			out = append(out, pad+l)
		}
	}
	return out
}

// updateBoardKey handles a key while the board has focus; ok is false when
// the key is not the board's and normal handling should go on.
func (m *Model) updateBoardKey(k string) (tea.Cmd, bool) {
	entries := m.sortedBoard()
	sel := m.boardSelIndex(entries)
	move := func(i int) {
		if len(entries) > 0 {
			i = min(max(i, 0), len(entries)-1)
			m.boardState.sel, m.boardState.selID = i, entries[i].Identity
		}
	}
	switch k {
	case "up":
		move(sel - 1)
	case "down":
		move(sel + 1)
	case "right", "left":
		if sel < len(entries) {
			name := entries[sel].Identity
			if m.boardState.expanded == nil {
				m.boardState.expanded = map[string]bool{}
			}
			m.boardState.expanded[name] = k == "right"
		}
	case "enter":
		// The history view is not built yet (Task 8); enter does nothing.
	case "esc":
		m.boardState.focus = false
	default:
		return nil, false
	}
	m.refreshStream()
	return nil, true
}

// cycleBoardSort moves the sort time -> name -> tag -> time, keeping the
// selected agent selected.
func (m *Model) cycleBoardSort() {
	m.clampBoardSel() // pin selID before the order changes
	i := slices.Index(cli.StatusSortModes, m.boardSortMode())
	m.boardState.sort = cli.StatusSortModes[(i+1)%len(cli.StatusSortModes)]
	m.clampBoardSel()
	m.refreshStream()
}

// openBoard selects the sessions heading, which shows the board.
func (m *Model) openBoard() {
	if m.boardActive() {
		return
	}
	m.headSel = secSessions
	m.boardState.heading = true
	m.cursor = -1
	m.boardState.focus = false
	m.refreshStream()
}
