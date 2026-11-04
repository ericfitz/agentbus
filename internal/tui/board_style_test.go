package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/muesli/termenv"
)

// boardRow is the plain board line holding name.
func (f *fixture) boardRow(t *testing.T, name string) string {
	t.Helper()
	for _, l := range strings.Split(f.mainPane(), "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	t.Fatalf("no board row for %s:\n%s", name, f.mainPane())
	return ""
}

// Every board column is at least two spaces from the next: icon, name, tag,
// age, text, problem.
func TestBoardColumnsTwoSpacesApart(t *testing.T) {
	f := boardFixture(t)
	f.key("b")
	cy := f.boardRow(t, "Cy")
	gaps := regexp.MustCompile(`Cy {3,}blocked {7,}5m {4,}stuck on CI {2,}runner down`)
	if !gaps.MatchString(cy) {
		t.Errorf("Cy row columns not two spaces apart: %q", cy)
	}
	ann := f.boardRow(t, "Ann")
	if !regexp.MustCompile(`\S {2,}Ann {2,}planning {6,}3h {4,}drafting`).MatchString(ann) {
		t.Errorf("Ann row columns not two spaces apart: %q", ann)
	}
}

// The history view's columns are two spaces apart too.
func TestBoardHistoryColumnsTwoSpacesApart(t *testing.T) {
	f := historyFixture(t, 1)
	f.key("enter")
	var row string
	for _, l := range strings.Split(f.mainPane(), "\n") {
		if strings.Contains(l, "step 01") {
			row = l
		}
	}
	if !regexp.MustCompile(`^ +1  \d\d:\d\d  \S+ {2,}implementing  step 01`).MatchString(row) {
		t.Errorf("history row columns not two spaces apart: %q", row)
	}
}

// Board names use the agent name colors everywhere else uses: live agents
// in the agent color with the icon in that color, the TUI's own identity in
// the user color, offline identities dim with a plain icon. The board and
// history headers carry the color too.
func TestBoardNameColors(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	me := f.m.c.as
	f.setBoard(
		withAge(boardEntry("Ann", "planning", "drafting", "", true), time.Minute),
		withAge(boardEntry("Old", "idle", "gone", "", false), time.Hour),
		withAge(boardEntry(me, "reviewing", "reading", "", true), 2*time.Hour),
		withAge(boardEntry("Zed", "waiting", "selected row", "", true), 3*time.Hour),
	)
	f.key("esc")
	f.key("b")
	f.m.boardState.selID = "Zed" // keep the rows under test unselected
	th := f.m.theme
	agent, user, dim := sgrPrefix(th.Style(th.Agent)), sgrPrefix(th.Style(th.User)), sgrPrefix(th.Style(th.Dim))
	if agent == "" || user == "" || dim == "" || agent == user {
		t.Fatalf("test needs distinct agent/user/dim colors: %q %q %q", agent, user, dim)
	}
	lines := strings.Split(f.m.renderBoard(f.m.stream.Width, f.m.stream.Height), "\n")
	row := func(name string) string {
		for _, l := range lines {
			if strings.Contains(ansi.Strip(l), name) {
				return l
			}
		}
		t.Fatalf("no row for %s", name)
		return ""
	}
	if r := row("Ann"); !strings.Contains(r, agent+"Ann") || !strings.HasPrefix(r, agent) {
		t.Errorf("live agent: name and icon must be in the agent color: %q", r)
	}
	if r := row(me); !strings.Contains(r, user+me) || !strings.HasPrefix(r, user) {
		t.Errorf("own identity: name and icon must be in the user color: %q", r)
	}
	if r := row("Old"); !strings.Contains(r, dim+"Old") || strings.HasPrefix(r, agent) {
		t.Errorf("offline: name must be dim and the icon plain: %q", r)
	}
	head := f.m.renderHeader()
	if !strings.Contains(head, agent+"sessions") {
		t.Errorf("board header: sessions must be in the agent color: %q", head)
	}

	f.m.boardState.focus = true
	f.m.boardState.selID = "Ann"
	f.key("enter")
	f.send(statusHistoryMsg{name: "Ann", hist: []bus.AgentStatus{{Identity: "Ann", Revision: 1, Tag: "planning", Text: "drafting", SetAt: time.Now().UnixMilli()}}, live: true})
	if h := strings.Split(f.m.renderHistory(f.m.stream.Width, f.m.stream.Height), "\n")[0]; !strings.HasPrefix(h, agent+"Ann") {
		t.Errorf("history header: name must be in the agent color: %q", h)
	}
}

// A focused selected row is recolored in the selection text color like a
// focused rail row, so no name is drawn in a color unreadable on Sel.
func TestBoardFocusedSelectionRecolored(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := boardFixture(t)
	f.key("b")
	f.key("tab")
	if !f.m.boardFocused() {
		t.Fatal("board not focused")
	}
	th := f.m.theme
	agent := sgrPrefix(th.Style(th.Agent))
	sel := f.m.sortedBoard()[f.m.boardSelIndex(f.m.sortedBoard())].Identity
	for _, l := range strings.Split(f.m.renderBoard(f.m.stream.Width, f.m.stream.Height), "\n") {
		if strings.Contains(ansi.Strip(l), sel) && strings.Contains(l, agent) {
			t.Errorf("focused selected row keeps the agent color: %q", l)
		}
	}
}

// Text runs to the right margin when no row has a problem, and the problem
// column is only as wide as the longest problem (up to its 40% cap), so
// text is not cut early.
func TestBoardTextUsesTheWidth(t *testing.T) {
	long := strings.Repeat("word ", 80)
	f := newFixture(t)
	f.setBoard(boardEntry("Ann", "planning", long, "", true))
	f.key("esc")
	f.key("b")
	w := f.m.stream.Width
	for _, l := range strings.Split(f.m.renderBoard(w, f.m.stream.Height), "\n") {
		if p := ansi.Strip(l); strings.Contains(p, "Ann") && ansi.StringWidth(strings.TrimRight(p, " ")) != w {
			t.Errorf("no problems: text must reach the margin (%d columns), got %d: %q", w, ansi.StringWidth(strings.TrimRight(p, " ")), p)
		}
	}

	f.setBoard(boardEntry("Ann", "planning", long, "", true), boardEntry("Bob", "blocked", long, "CI down", true))
	for _, l := range strings.Split(ansi.Strip(f.m.renderBoard(w, f.m.stream.Height)), "\n") {
		if strings.Contains(l, "Bob") {
			if !strings.HasSuffix(strings.TrimRight(l, " "), "…  CI down") || ansi.StringWidth(strings.TrimRight(l, " ")) != w {
				t.Errorf("short problem: text must fill up to it and it must end at the margin: %q", l)
			}
		}
	}
}
