package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// setBoard feeds a status refresh carrying entries.
func (f *fixture) setBoard(entries ...bus.BoardEntry) {
	f.run(f.m.statusCmd())
	f.send(statusMsg{st: f.m.status, board: entries})
}

// mainPane is the main pane as plain text.
func (f *fixture) mainPane() string { return ansi.Strip(f.m.stream.View()) }

// boardOrder is the names in the main pane, top to bottom.
func (f *fixture) boardOrder(names ...string) []string {
	type at struct {
		name string
		pos  int
	}
	var found []at
	pane := f.mainPane()
	for _, n := range names {
		if i := strings.Index(pane, n); i >= 0 {
			found = append(found, at{n, i})
		}
	}
	for i := range found {
		for j := i + 1; j < len(found); j++ {
			if found[j].pos < found[i].pos {
				found[i], found[j] = found[j], found[i]
			}
		}
	}
	out := make([]string, len(found))
	for i, x := range found {
		out[i] = x.name
	}
	return out
}

func boardFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.setBoard(
		withAge(boardEntry("Cy", "blocked", "stuck on CI", "runner down", true), 5*time.Minute),
		withAge(boardEntry("Ann", "planning", "drafting the plan", "", true), 3*time.Hour),
		withAge(boardEntry("Bob", "blocked", "waiting on review", "", true), 40*time.Minute),
	)
	f.key("esc")
	return f
}

func TestBoardOpensFromHeading(t *testing.T) {
	f := boardFixture(t)
	f.moveTo(t, "dev")
	if f.m.boardActive() {
		t.Fatal("board active on a channel")
	}
	f.toHeading(t, secSessions)
	if !f.m.boardActive() {
		t.Fatal("sessions heading must show the board")
	}
	pane := f.mainPane()
	for _, want := range []string{"Ann", "Bob", "Cy", "stuck on CI", "runner down", "5m", "3h"} {
		if !strings.Contains(pane, want) {
			t.Errorf("board missing %q:\n%s", want, pane)
		}
	}
	f.key("down") // first session row
	if f.m.boardActive() {
		t.Fatal("a session row leaves the board")
	}
	f.moveTo(t, "dev")
	if strings.Contains(f.mainPane(), "stuck on CI") {
		t.Fatalf("channel stream must replace the board:\n%s", f.mainPane())
	}
}

func TestBoardOpensFromB(t *testing.T) {
	f := boardFixture(t)
	f.moveTo(t, "dev")
	f.key("b")
	if !f.m.boardActive() || f.m.headSel != secSessions {
		t.Fatalf("b must select the sessions heading: headSel=%d", f.m.headSel)
	}
	if !strings.Contains(f.mainPane(), "stuck on CI") {
		t.Fatalf("no board:\n%s", f.mainPane())
	}
	// b is text in compose.
	f.moveTo(t, "dev")
	f.key("i")
	f.key("b")
	if f.m.boardActive() || f.m.compose.Value() != "b" {
		t.Fatalf("b in compose must type: active=%v value=%q", f.m.boardActive(), f.m.compose.Value())
	}
}

func TestBoardSortCycle(t *testing.T) {
	f := boardFixture(t)
	f.key("b")
	names := []string{"Ann", "Bob", "Cy"}
	want := func(label string, order ...string) {
		t.Helper()
		if got := f.boardOrder(names...); strings.Join(got, ",") != strings.Join(order, ",") {
			t.Fatalf("%s: got %v want %v\n%s", label, got, order, f.mainPane())
		}
	}
	want("time", "Cy", "Bob", "Ann") // newest first
	f.key("o")
	want("name", "Ann", "Bob", "Cy")
	f.key("o")
	want("tag", "Bob", "Cy", "Ann") // blocked (ties by name), then planning
	f.key("o")
	want("time again", "Cy", "Bob", "Ann")
}

func TestBoardExpandCollapse(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("abcdefghij ", 14) + "END" // > 150 runes
	f.setBoard(boardEntry("Ann", "implementing", long, "problem-detail "+long+" PEND", true))
	f.key("esc")
	f.key("b")
	f.key("tab")
	if f.m.pane() != paneStream {
		t.Fatalf("tab must focus the board, pane=%v", f.m.pane())
	}
	before := f.mainPane()
	if strings.Contains(before, "END") || strings.Contains(before, "PEND") {
		t.Fatalf("collapsed row must be cut:\n%s", before)
	}
	f.key("right")
	after := f.mainPane()
	if !strings.Contains(after, "END") || !strings.Contains(after, "PEND") {
		t.Fatalf("expanded row must show full text and problem:\n%s", after)
	}
	for _, l := range strings.Split(after, "\n") {
		if ansi.StringWidth(l) > f.m.stream.Width {
			t.Errorf("line wider than pane: %q", l)
		}
	}
	f.key("left")
	if got := f.mainPane(); strings.Contains(got, "END") {
		t.Fatalf("left must fold the row:\n%s", got)
	}
	f.key("esc")
	if f.m.pane() == paneStream {
		t.Fatal("esc must leave the board focus")
	}
}

func TestBoardUpDownMovesSelection(t *testing.T) {
	f := boardFixture(t)
	f.key("b")
	f.key("tab")
	f.key("down")
	if f.m.boardState.sel != 1 || f.m.headSel != secSessions {
		t.Fatalf("sel=%d headSel=%d", f.m.boardState.sel, f.m.headSel)
	}
	f.key("up")
	f.key("up")
	if f.m.boardState.sel != 0 {
		t.Fatalf("sel=%d", f.m.boardState.sel)
	}
}

func TestBoardTruncation(t *testing.T) {
	f := newFixture(t)
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	problem := strings.Repeat("問題が発生しました", 12)
	f.setBoard(boardEntry("Ann", "blocked", strings.Repeat("long text ", 30), problem, true))
	f.key("esc")
	f.key("b")
	var row string
	for _, l := range strings.Split(f.mainPane(), "\n") {
		if strings.Contains(l, "Ann") {
			row = strings.TrimRight(l, " ")
		}
	}
	if row == "" {
		t.Fatalf("no row:\n%s", f.mainPane())
	}
	if w := ansi.StringWidth(row); w > f.m.stream.Width {
		t.Fatalf("row is %d columns, pane is %d: %q", w, f.m.stream.Width, row)
	}
	if !strings.HasSuffix(row, "…") {
		t.Fatalf("row must end in an ellipsis: %q", row)
	}
	for _, l := range strings.Split(f.m.renderBoard(f.m.stream.Width, f.m.stream.Height), "\n") {
		if ansi.StringWidth(l) > f.m.stream.Width {
			t.Errorf("line wider than pane: %q", l)
		}
	}
}

func TestBoardEmpty(t *testing.T) {
	f := newFixture(t)
	f.setBoard()
	f.key("esc")
	f.key("b")
	if !strings.Contains(f.mainPane(), "no agents") {
		t.Fatalf("empty board:\n%s", f.mainPane())
	}
}

func TestHeadingLegacyUnchanged(t *testing.T) {
	f := sectionedFixture(t)
	f.setBoard(boardEntry("Ann", "blocked", "stuck on CI", "", true))
	for _, sec := range []int{secChannels, secMemories, secTasks, secTags} {
		f.toHeading(t, sec)
		if f.m.boardActive() {
			t.Fatalf("%s heading must not show the board", railHeadings[sec])
		}
		// The pane holds the last selected item's content, as it did before.
		legacy := f.m
		legacy.headSel = -1
		want := ansi.Strip(legacy.renderStream())
		if strings.Contains(f.mainPane(), "stuck on CI") || !strings.Contains(f.mainPane(), strings.TrimSpace(strings.Split(want, "\n")[0])) {
			t.Errorf("%s heading changed the main pane, want %q:\n%s", railHeadings[sec], want, f.mainPane())
		}
	}
}

func (f *fixture) selectedBoardAgent() string {
	e := f.m.sortedBoard()
	return e[f.m.boardState.sel].Identity
}

func TestBoardSelectionSurvivesRefresh(t *testing.T) {
	f := boardFixture(t)
	f.key("b")
	f.key("tab")
	f.key("down") // time order Cy, Bob, Ann: Bob
	if got := f.selectedBoardAgent(); got != "Bob" {
		t.Fatalf("selected %q, want Bob", got)
	}
	// Ann updates and jumps to the top; Bob shifts down a row.
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{
		boardEntry("Ann", "implementing", "now busy", "", true),
		withAge(boardEntry("Bob", "blocked", "waiting on review", "", true), 40*time.Minute),
		withAge(boardEntry("Cy", "blocked", "stuck on CI", "runner down", true), 5*time.Minute),
	}})
	if got := f.selectedBoardAgent(); got != "Bob" {
		t.Fatalf("after refresh selected %q, want Bob", got)
	}
	f.key("right")
	if !f.m.boardState.expanded["Bob"] || f.m.boardState.expanded["Ann"] {
		t.Fatalf("right must expand Bob: %v", f.m.boardState.expanded)
	}
	// The selected agent disappears: the selection lands on a row.
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{boardEntry("Ann", "implementing", "now busy", "", true)}})
	if got := f.selectedBoardAgent(); got != "Ann" {
		t.Fatalf("after Bob left selected %q, want Ann", got)
	}
}

func TestBoardSortCycleKeepsSelection(t *testing.T) {
	f := boardFixture(t)
	f.key("b")
	f.key("tab")
	f.key("down") // Bob
	for _, mode := range []string{"name", "tag", "time"} {
		f.key("o")
		if f.m.boardSortMode() != mode || f.selectedBoardAgent() != "Bob" {
			t.Fatalf("sort %s: selected %q", f.m.boardSortMode(), f.selectedBoardAgent())
		}
	}
}

func TestBoardNotShownOverCollapsedDM(t *testing.T) {
	f := boardFixture(t)
	f.toSessions()
	if f.m.sessSel < 0 {
		t.Fatal("no session selected")
	}
	dm := f.m.selName()
	f.m.toggleSection(secSessions, true) // collapse while viewing the DM
	if f.m.boardActive() {
		t.Fatal("board must not replace a DM selected under a collapsed section")
	}
	f.m.refreshStream()
	if strings.Contains(f.mainPane(), "stuck on CI") || f.m.selName() != dm {
		t.Fatalf("stream must show %s:\n%s", dm, f.mainPane())
	}
}

// The board hides the selected channel, so compose must not be reachable
// while it shows (a send would go to a channel nobody can see).
func TestBoardRefusesCompose(t *testing.T) {
	for _, keys := range [][]string{{"b", "tab", "tab"}, {"b", "i"}, {"b", "r"}, {"b", "tab", "enter"}} {
		f := boardFixture(t)
		f.moveTo(t, "dev")
		for _, k := range keys {
			f.key(k)
		}
		if !f.m.boardActive() {
			t.Fatalf("%v: board closed", keys)
		}
		if f.m.mode == modeInsert || f.m.pane() == paneCompose {
			t.Errorf("%v: compose entered under the board (mode=%v pane=%v)", keys, f.m.mode, f.m.pane())
		}
	}
}
