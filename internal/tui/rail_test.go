package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestRailWindowScrollsByOneWithArrows(t *testing.T) {
	// 10 items in 5 rows: from the top, 4 items and the down arrow.
	if top, end := railWindow(10, 5, 0, 0); top != 0 || end != 4 {
		t.Fatalf("top=%d end=%d, want 0 4", top, end)
	}
	// Stepping past the last shown item scrolls just far enough to show
	// it; the up arrow now takes a row too.
	top, end := railWindow(10, 5, 0, 4)
	if top != 2 || end != 5 {
		t.Fatalf("top=%d end=%d, want 2 5", top, end)
	}
	if top, end = railWindow(10, 5, top, 5); top != 3 || end != 6 {
		t.Fatalf("top=%d end=%d, want 3 6", top, end)
	}
	// The last item: no down arrow, and no empty rows under it.
	if top, end = railWindow(10, 5, top, 9); top != 6 || end != 10 {
		t.Fatalf("top=%d end=%d, want 6 10", top, end)
	}
	// Back up: stepping above the first shown item scrolls by one.
	if top, end = railWindow(10, 5, 6, 5); top != 5 || end != 8 {
		t.Fatalf("top=%d end=%d, want 5 8", top, end)
	}
	// Everything fits: no scrolling at all.
	if top, end = railWindow(3, 5, 2, 2); top != 0 || end != 3 {
		t.Fatalf("top=%d end=%d, want 0 3", top, end)
	}
	// A selection elsewhere keeps the offset.
	if top, _ = railWindow(10, 5, 3, -1); top != 3 {
		t.Fatalf("top=%d, want 3", top)
	}
}

func TestRailBudgetsKeepTwoItemsPerList(t *testing.T) {
	// Plenty of room: every list gets all it needs.
	if b := railBudgets(40, [railLists]int{5, 1, 3}); b != [railLists]int{5, 1, 3} {
		t.Fatalf("budgets %v", b)
	}
	// Too little: each long list still gets four rows (two items plus both
	// arrows), and the spare row goes round-robin.
	if b := railBudgets(railFixedRows+9, [railLists]int{20, 0, 20}); b != [railLists]int{5, 0, 4} {
		t.Fatalf("budgets %v", b)
	}
	if b := railBudgets(railFixedRows+5, [railLists]int{20, 2, 20}); b != [railLists]int{4, 2, 4} {
		t.Fatalf("budgets %v", b)
	}
}

// railLines is the rendered rail, stripped and trimmed, one entry per row.
func (f *fixture) railLines() []string {
	lines := strings.Split(ansi.Strip(f.m.renderRails()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func TestRailShowsEveryListWithTagHint(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	tags := slices.Index(lines, "tags")
	if tags < 0 || lines[tags+1] != railTagHint || slices.Index(lines, "sessions") < tags {
		t.Fatalf("want a tags list holding only the hint, before sessions:\n%s", strings.Join(lines, "\n"))
	}
	f.followTags(t, []string{"docs"})
	lines = f.railLines()
	tags = slices.Index(lines, "tags")
	if tags < 0 || !strings.Contains(lines[tags+1], "docs") || lines[tags+2] != railTagHint {
		t.Fatalf("want the hint after the tag set:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRailSelectedRowStaysOneLine(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 60)
	if _, err := f.ab.CreateChannel(f.sam, long, "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.key("esc")
	before := len(f.railLines())
	for f.m.selName() != long {
		f.key("down")
	}
	lines := f.railLines()
	if len(lines) != before {
		t.Fatalf("selecting the long channel changed the rail from %d to %d rows:\n%s", before, len(lines), strings.Join(lines, "\n"))
	}
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "xxxx") })
	if !strings.HasSuffix(lines[i], "…") || strings.Contains(lines[i+1], "x") {
		t.Fatalf("want one row ending in an ellipsis:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if w := ansi.StringWidth(l); w > f.m.railWidth() {
			t.Fatalf("row %q is %d wide, rail is %d", ansi.Strip(l), w, f.m.railWidth())
		}
	}
}

func TestRailScrollsLongChannelList(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if _, err := f.ab.CreateChannel(f.sam, fmt.Sprintf("ch%02d", i), "ordinary"); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
	f.send(tea.WindowSizeMsg{Width: 100, Height: 20})
	f.key("esc")
	f.key("home")
	for f.m.sel > 0 {
		f.key("up")
	}
	nc := spans(f)[listChannels]
	if nc.top != 0 || nc.end >= nc.n {
		t.Fatalf("want the list cut from the top, got %+v", nc)
	}
	lines := f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▼") }) {
		t.Fatalf("want a down arrow:\n%s", strings.Join(lines, "\n"))
	}
	if slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▲") }) {
		t.Fatalf("no up arrow at the top:\n%s", strings.Join(lines, "\n"))
	}
	// Down from the last shown channel scrolls and selects the next one.
	for range nc.end - 1 {
		f.key("down")
	}
	f.key("down")
	if f.m.sel != nc.end {
		t.Fatalf("sel=%d, want %d", f.m.sel, nc.end)
	}
	s := spans(f)[listChannels]
	if s.top == 0 || f.m.sel >= s.end || s.end-s.top < 2 {
		t.Fatalf("selection not scrolled into view: %+v sel=%d", s, f.m.sel)
	}
	lines = f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▲") }) {
		t.Fatalf("want an up arrow once scrolled:\n%s", strings.Join(lines, "\n"))
	}
	// Down through the rest reaches the last channel, then leaves the list.
	for f.m.sel < s.n-1 {
		f.key("down")
	}
	if s = spans(f)[listChannels]; s.end != s.n {
		t.Fatalf("last channel not shown: %+v", s)
	}
	if lines = f.railLines(); slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▼") }) {
		t.Fatalf("no down arrow at the end:\n%s", strings.Join(lines, "\n"))
	}
	f.key("down")
	if f.m.pane() != paneSessions {
		t.Fatalf("down from the last channel should enter sessions, pane=%v", f.m.pane())
	}
	// Up from the first session returns to the last channel, still shown.
	f.key("up")
	if s = spans(f)[listChannels]; f.m.sel != s.n-1 || s.end != s.n {
		t.Fatalf("sel=%d span=%+v", f.m.sel, s)
	}
	// Up scrolls back one at a time to the top.
	for f.m.sel > 0 {
		top := spans(f)[listChannels].top
		f.key("up")
		if s = spans(f)[listChannels]; f.m.sel < s.top || top-s.top > 1 {
			t.Fatalf("sel=%d span=%+v (was top %d)", f.m.sel, s, top)
		}
	}
	if s = spans(f)[listChannels]; s.top != 0 {
		t.Fatalf("back at the first channel, want top 0: %+v", s)
	}
}

func spans(f *fixture) [railLists]railSpan { return f.m.railSpans() }
