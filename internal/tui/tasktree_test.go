package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// ids lists the task ids of rows, in order.
func ids(rs []taskRow) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.t.ID
	}
	return out
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTaskChildrenStartCollapsedAndArrowsLayerDetailsThenChildren (ADR
// 0011 decision 3): a (details, child a1 with its own child a1a) and b.
// Only roots show at first, with a's hidden count; → opens a's details,
// then its child; ← hides the whole subtree, then the details.
func TestTaskChildrenStartCollapsedAndArrowsLayerDetailsThenChildren(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	a, a1, b := f.taskTree(t) // cursor on the last visible row, b
	a1a, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a1a", Parent: a1.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.loadTasks("tasks/work"))
	rows := f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{a.ID, b.ID}) || rows[0].hidden != 2 || rows[0].open {
		t.Fatalf("collapsed rows = %+v", rows)
	}
	if f.m.paneLen("tasks/work") != 2 {
		t.Fatalf("paneLen = %d, want the visible rows", f.m.paneLen("tasks/work"))
	}
	lines := strings.Split(f.stream(), "\n")
	if !strings.HasPrefix(lines[0], markSel+" ") || strings.TrimSpace(lines[1]) != "2 subtasks" || !strings.Contains(lines[2], "b") {
		t.Fatalf("collapsed stream:\n%s", f.stream())
	}
	f.key("up") // a
	f.key("right")
	if !f.m.taskOpen[a.ID] || len(f.m.taskRows("tasks/work")) != 2 {
		t.Fatal("first → opens the details only")
	}
	if lines = strings.Split(f.stream(), "\n"); !strings.HasPrefix(lines[0], markSel+" ") {
		t.Fatalf("children still hidden: a keeps %s:\n%s", markSel, f.stream())
	}
	f.key("right")
	rows = f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{a.ID, a1.ID, b.ID}) || !rows[0].open || rows[0].hidden != 0 || rows[1].hidden != 1 {
		t.Fatalf("second → shows a's direct children: %+v", rows)
	}
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("→ moved the cursor: %+v", got)
	}
	lines = strings.Split(f.stream(), "\n")
	if !strings.HasPrefix(lines[0], markOpen+" ") {
		t.Fatalf("a shows %s once nothing is hidden: %q", markOpen, lines[0])
	}
	f.key("down") // a1
	f.key("right")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, a1.ID, a1a.ID, b.ID}) {
		t.Fatalf("→ on a1 shows a1a: %v", ids(f.m.taskRows("tasks/work")))
	}
	f.key("up") // a
	f.key("left")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, b.ID}) || f.m.taskExpanded[a1.ID] || !f.m.taskOpen[a.ID] {
		t.Fatalf("← hides the whole subtree and keeps the details: rows=%v expanded=%v open=%v", ids(f.m.taskRows("tasks/work")), f.m.taskExpanded, f.m.taskOpen)
	}
	f.key("left")
	if f.m.taskOpen[a.ID] {
		t.Fatal("second ← closes the details")
	}
	f.key("right")
	f.key("right")
	if !sameIDs(ids(f.m.taskRows("tasks/work")), []int64{a.ID, a1.ID, b.ID}) {
		t.Fatalf("re-showing a's children shows direct children only: %v", ids(f.m.taskRows("tasks/work")))
	}
}

// TestHiddenChildSummaryCountsAsALine: the summary line under a collapsed
// parent is part of that row's block (highlighted with it) and shifts the
// next row's cursorLine.
func TestHiddenChildSummaryCountsAsALine(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, _, b := f.taskTree(t) // cursor on b
	if got, ok := f.m.cursorTask(); !ok || got.ID != b.ID {
		t.Fatalf("cursor = %+v, want b", got)
	}
	if f.m.cursorLine != 2 {
		t.Fatalf("cursorLine = %d, want 2 (a's row, its summary, then b)", f.m.cursorLine)
	}
	f.key("up") // a, whose block is two lines
	if f.m.cursorLine != 0 {
		t.Fatalf("cursorLine = %d, want 0", f.m.cursorLine)
	}
	lines := strings.Split(f.m.renderStream(), "\n")
	if w0, w1 := ansi.StringWidth(lines[0]), ansi.StringWidth(lines[1]); w0 != w1 || w0 < f.m.stream.Width {
		t.Fatalf("both lines of the selected block are padded to the pane width: %d, %d (width %d)", w0, w1, f.m.stream.Width)
	}
}

// TestTaskRowsShowAnOrphanAsARoot: a summary whose parent is not in the
// list (a filtered or stale view) is shown as a root, not dropped.
func TestTaskRowsShowAnOrphanAsARoot(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.send(tasksMsg{ch: "tasks/work", tasks: []bus.TaskSummary{
		{ID: 1, Subject: "root", Status: "pending"},
		{ID: 7, Subject: "orphan", Status: "pending", Parent: 999, Depth: 1},
	}})
	rows := f.m.taskRows("tasks/work")
	if !sameIDs(ids(rows), []int64{1, 7}) || rows[0].hidden != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	f.key("tab")
	if got, ok := f.m.cursorTask(); !ok || got.ID != 7 {
		t.Fatalf("cursor reaches the orphan: %+v", got)
	}
}

// TestJumpIntoTaskListRevealsTheTarget (spec section 4): placing the
// cursor on a nested task (what a search hit does through placeCursor)
// expands its ancestors so its row is visible, both with the tree loaded
// and through the pending path when it is not.
func TestJumpIntoTaskListRevealsTheTarget(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, a1, _ := f.taskTree(t)
	f.receive(t) // m.msgs["tasks/work"] holds the revision rows placeTaskCursor resolves through
	var seq int64
	for _, x := range f.m.msgs["tasks/work"] {
		if x.MemoryID != nil && *x.MemoryID == a1.ID {
			seq = x.Seq
		}
	}
	if seq == 0 {
		t.Fatal("fixture: no revision row for a1")
	}
	if got, _ := f.m.cursorTask(); got.ID == a1.ID {
		t.Fatal("fixture: a1 must start hidden")
	}
	f.m.placeCursor(seq)
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("placeCursor must reveal and land on a1, got %+v", got)
	}
	// The pending path: the tree arrives after the jump asked for a1.
	f.m.taskExpanded = map[int64]bool{}
	f.m.pendingTaskCursorCh, f.m.pendingTaskCursorID = "tasks/work", a1.ID
	f.run(f.m.loadTasks("tasks/work"))
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("tasksMsg must reveal and land on the pending a1, got %+v", got)
	}
}
