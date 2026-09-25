package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/muesli/termenv"
)

// selectTaskChannel walks the rail down until ch is selected, bounded so a
// key that stops moving focus can't hang the test.
func (f *fixture) selectTaskChannel(t *testing.T, ch string) {
	t.Helper()
	for i := 0; i <= len(f.m.channels)+len(f.m.dms); i++ {
		if f.m.selName() == ch {
			return
		}
		f.key("down")
	}
	t.Fatalf("could not select %q, at %q", ch, f.m.selName())
}

func TestTaskChannelRendersTree(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	a, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"})
	if err != nil {
		t.Fatal(err)
	}
	a1, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a1", Parent: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskClaim(f.sam, a1.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b", BlockedBy: []int64{a.ID}}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")

	// Children start collapsed: a's row (▶, its child hidden), the hidden
	// count, then b.
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], markSel+" "+taskPending+"#") || strings.TrimSpace(lines[1]) != "1 subtask" {
		t.Fatalf("collapsed tree = %q", lines)
	}
	f.key("tab")   // cursor on b
	f.key("up")    // a
	f.key("right") // a has no details, so → shows its child
	lines = strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 3 {
		t.Fatalf("want at least 3 lines, got %d: %q", len(lines), lines)
	}
	// a's child is shown and nothing is hidden, so a carries ▼.
	if !strings.HasPrefix(lines[0], markOpen+" "+taskPending+"#") || !strings.Contains(lines[0], "a") {
		t.Fatalf("line0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  "+"  "+taskInProgress+"#") || !strings.Contains(lines[1], ansi.Strip(iconAgent)+"Sam") {
		t.Fatalf("line1 = %q", lines[1])
	}
	// "b" is blocked, so it has details and shows the collapsed triangle.
	if !strings.HasPrefix(lines[2], markSel+" "+taskPending+"#") || !strings.Contains(lines[2], "blocked by #"+itoa(a.ID)) {
		t.Fatalf("line2 = %q", lines[2])
	}
}

func TestTaskChannelRailIcon(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if !strings.Contains(f.m.renderRails(), iconTasks) {
		t.Fatalf("rail missing task icon:\n%s", f.m.renderRails())
	}
}

func TestTaskChannelIsReadOnly(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")

	for _, k := range []string{"i", "enter", "r"} {
		f.key(k)
		if f.m.mode != modeNormal {
			t.Fatalf("key %q left mode %v", k, f.m.mode)
		}
		if f.m.toast != tasksReadOnlyToast {
			t.Fatalf("key %q toast = %q, want %q", k, f.m.toast, tasksReadOnlyToast)
		}
	}

	f.key("tab")
	if f.m.pane() != paneChannels {
		t.Fatalf("tab moved pane to %v, want paneChannels", f.m.pane())
	}

	f.key("d")
	if f.m.mode != modeConfirmChannel {
		t.Fatalf("d mode = %v, want modeConfirmChannel", f.m.mode)
	}
}

func TestTaskTreeRefreshesOnRevision(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	a, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b", BlockedBy: []int64{a.ID}}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")

	before := ansi.Strip(f.m.renderStream())
	if !strings.Contains(before, "blocked by") {
		t.Fatalf("expected b blocked before a completes:\n%s", before)
	}

	completed := "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: a.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)

	after := ansi.Strip(f.m.renderStream())
	if !strings.Contains(after, taskCompleted) {
		t.Fatalf("expected a completed:\n%s", after)
	}
	if strings.Contains(after, "blocked by") {
		t.Fatalf("expected b no longer blocked:\n%s", after)
	}
}

// TestSelectedLongSubjectTaskRendersOneLine: a selected row with a long
// subject must be clipped to the stream width before going through
// th.Highlight, or the width padding wraps it onto a second line and
// lineNum/cursorLine drift for every row after it.
func TestSelectedLongSubjectTaskRendersOneLine(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: strings.Repeat("x", 256)}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab") // focus stream; cursor lands on the only (selected) task

	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) != 1 {
		t.Fatalf("selected long-subject row wrapped to %d lines: %q", len(lines), lines)
	}
}

// TestSelectedCompletedTaskDimsWithRowDim: a selected completed row must
// swap dim text to the selection-readable color (rowDim), not stay dim on
// the whole body, matching the unselected-row convention used for suffix.
func TestSelectedCompletedTaskDimsWithRowDim(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	task, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "done"})
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: task.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab") // focus stream; cursor lands on the only (selected) task

	th := f.m.theme
	textCode, _, _ := strings.Cut(th.Style(th.Text).Render("\x00"), "\x00")
	dimCode, _, _ := strings.Cut(th.Style(th.Dim).Render("\x00"), "\x00")
	got := f.m.renderStream()
	if !strings.Contains(got, textCode) {
		t.Fatalf("selected completed row must switch to th.Text (rowDim), got:\n%q", got)
	}
	if strings.Contains(got, dimCode) {
		t.Fatalf("selected completed row must not render its body in th.Dim, got:\n%q", got)
	}
}

// TestTasksMsgClampsCursorWhenTaskDeleted: if the task under the cursor is
// gone from the next tasksMsg (e.g. it dropped out of a filtered list), the
// cursor must clamp to the new last index rather than pointing past the end
// of m.tasks[ch].
func TestTasksMsgClampsCursorWhenTaskDeleted(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	a, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab") // cursor lands on the last task, "b" (index 1)
	if f.m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (b)", f.m.cursor)
	}

	// b is gone from the next tasksMsg.
	f.send(tasksMsg{ch: "tasks/work", tasks: []bus.TaskSummary{{ID: a.ID, Subject: "a"}}})

	if f.m.cursor != 0 {
		t.Fatalf("cursor = %d, want clamped to 0", f.m.cursor)
	}
}

// taskTree creates the tree the cursor/expand tests share: "a" (a
// description and metadata, so it has details), "a1" (a's child, bare, no
// details), and "b" (blocked by a1, so it has details too).
func (f *fixture) taskTree(t *testing.T) (a, a1, b bus.Task) {
	t.Helper()
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	var err error
	a, err = f.ab.TaskCreate(f.sam, bus.TaskCreateInput{
		Channel: "tasks/work", Subject: "a", Description: "desc a", Metadata: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a1, err = f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a1", Parent: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err = f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b", BlockedBy: []int64{a1.ID}})
	if err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab")
	return a, a1, b
}

// TestTaskCursorMovesOverTasks: tab focuses the stream with the cursor on
// the last task; up/down move over tasks in tree order, and expanding a
// task adds no cursor stops of its own.
func TestTaskCursorMovesOverTasks(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	a, a1, _ := f.taskTree(t)
	if f.m.pane() != paneStream {
		t.Fatalf("tab did not focus the stream, pane = %v", f.m.pane())
	}
	if got, ok := f.m.cursorTask(); !ok || got.Subject != "b" {
		t.Fatalf("tab cursor = %+v, want b", got)
	}
	f.key("up")
	f.key("up")
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("cursor after two ups = %+v, want a", got)
	}
	f.key("right")
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("expanding a moved the cursor: %+v", got)
	}
	f.key("right") // details are open: the second → shows a's child
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("showing a's children moved the cursor: %+v", got)
	}
	f.key("down")
	if got, ok := f.m.cursorTask(); !ok || got.ID != a1.ID {
		t.Fatalf("down from a = %+v, want a1", got)
	}
}

// TestTaskExpandCollapse: ▶/▼ marks only on tasks with details; right
// expands and fetches the block via task_get; left collapses it; right on a
// task with no details does nothing.
func TestTaskExpandCollapse(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)

	before := ansi.Strip(f.m.renderStream())
	if strings.Contains(before, ansi.Strip(markOpen)) {
		t.Fatalf("nothing should start expanded:\n%s", before)
	}
	lines := strings.Split(before, "\n")
	if !strings.HasPrefix(lines[0], markSel+" ") {
		t.Fatalf("a should show %q: %q", markSel, lines[0])
	}
	if strings.TrimSpace(lines[1]) != "1 subtask" {
		t.Fatalf("a1 is hidden; a's summary line follows a: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], markSel+" ") {
		t.Fatalf("b (has a blocker) should show %q: %q", markSel, lines[2])
	}

	f.key("up")
	f.key("up") // cursor on a
	cmd := f.m.expandTask()
	if cmd == nil {
		t.Fatal("expanding a should return a task_get cmd")
	}
	f.run(cmd)
	expanded := ansi.Strip(f.m.renderStream())
	if lines := strings.Split(expanded, "\n"); !strings.HasPrefix(lines[0], markSel+" ") {
		t.Fatalf("a keeps %q while its child is hidden, even with details open: %q", markSel, lines[0])
	}
	if !strings.Contains(expanded, "desc a") {
		t.Fatalf("expanded block should show the description:\n%s", expanded)
	}
	if !strings.Contains(expanded, "k: v") {
		t.Fatalf("expanded block should show metadata:\n%s", expanded)
	}
	if !strings.Contains(expanded, "updated by") {
		t.Fatalf("expanded block should show the last update:\n%s", expanded)
	}

	f.m.collapseTask()
	collapsed := ansi.Strip(f.m.renderStream())
	if strings.Contains(collapsed, "desc a") {
		t.Fatalf("left should hide the block again:\n%s", collapsed)
	}

	f.key("right") // details again
	f.key("right") // then a's child
	f.key("down")  // cursor on a1, no details
	if got, ok := f.m.cursorTask(); !ok || got.Subject != "a1" {
		t.Fatalf("cursor = %+v, want a1", got)
	}
	if cmd := f.m.expandTask(); cmd != nil {
		t.Fatal("right on a task with no details should do nothing")
	}
}

// TestTaskExpansionSurvivesRevision: an expanded task's block refetches and
// stays open, with the cursor still on it, after a live revision arrives.
func TestTaskExpansionSurvivesRevision(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	a, _, _ := f.taskTree(t)
	f.key("up")
	f.key("up") // cursor on a
	f.run(f.m.expandTask())
	f.key("right") // and a's child, so nothing is hidden and a shows ▼

	desc2 := "desc a2"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: a.ID, Description: &desc2}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)

	after := ansi.Strip(f.m.renderStream())
	if !strings.Contains(after, "desc a2") {
		t.Fatalf("block should show the revised description:\n%s", after)
	}
	lines := strings.Split(after, "\n")
	if !strings.HasPrefix(lines[0], markOpen+" ") {
		t.Fatalf("a should still show %q: %q", markOpen, lines[0])
	}
	if got, ok := f.m.cursorTask(); !ok || got.ID != a.ID {
		t.Fatalf("cursor should still be on a: %+v", got)
	}
}

// TestTaskChannelPgupDoesNotLoadHistory covers T6-2 of the final review:
// pgup at the top of a task channel must not page in history that
// renderTasks never shows.
func TestTaskChannelPgupDoesNotLoadHistory(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t) // populate m.msgs["tasks/work"], as a real session would
	f.selectTaskChannel(t, "tasks/work")
	if len(f.m.msgs["tasks/work"]) == 0 {
		t.Fatal("expected m.msgs to hold the channel's raw revisions")
	}

	if cmd := f.m.loadOlder(); cmd != nil {
		t.Fatal("loadOlder must be a no-op on a task channel")
	}
}

func TestTaskIndentIsCapped(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	var parent int64
	for i := 0; i < 8; i++ {
		task, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: fmt.Sprintf("t%d", i), Parent: parent})
		if err != nil {
			t.Fatal(err)
		}
		parent = task.ID
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.m.revealTask("tasks/work", parent) // the deepest task: every ancestor's children shown
	f.m.refreshStream()

	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	deepest := lines[len(lines)-1]
	trimmed := strings.TrimLeft(deepest, " ")
	// 6 levels of two-space indent, capped, plus the two-space no-details
	// mark (these bare tasks have no triangle) (#16).
	if got := len(deepest) - len(trimmed); got != 14 {
		t.Fatalf("leading spaces = %d, want 14: %q", got, deepest)
	}
}

// Row suffixes (issue #9): a pending task with an owner shows a dim "→ owner";
// only an in-progress row shows the owner with the rail icon; completed rows
// are dimmed as a whole; the lease suffix follows the owner.
func TestTaskRowSuffixes(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	assigned, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "assigned"})
	if err != nil {
		t.Fatal(err)
	}
	owner := f.sam
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: assigned.ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	working, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "working"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskClaim(f.sam, working.ID, time.Now().Add(time.Hour).UnixMilli(), ""); err != nil {
		t.Fatal(err)
	}
	done, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "done"})
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: done.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	raw := strings.Split(f.m.renderStream(), "\n")
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 rows: %q", lines)
	}
	if !strings.Contains(lines[0], "→ Sam") || strings.Contains(lines[0], "⚙") {
		t.Fatalf("pending with owner shows a dim arrow, not the icon: %q", lines[0])
	}
	agentOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Agent).Render("\x00"), "\x00")
	if !strings.Contains(raw[1], iconAgent+agentOpen+"Sam") || !strings.Contains(lines[1], "lease 59m") && !strings.Contains(lines[1], "lease 60m") {
		t.Fatalf("in-progress row shows icon + colored owner then the lease: %q", raw[1])
	}
	if idx := strings.Index(lines[1], "lease"); idx < strings.Index(lines[1], "Sam") {
		t.Fatalf("lease must follow the owner: %q", lines[1])
	}
	dimOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Dim).Render("\x00"), "\x00")
	// "done" is bare (no details): its row gets the plain two-space
	// no-details mark before the dimmed status icon and subject (#16).
	if !strings.HasPrefix(raw[2], "  "+dimOpen+taskCompleted) {
		t.Fatalf("completed row (icon included) must be dim: %q", raw[2])
	}
}

// A task channel never shows message-style header rows, whatever m.msgs
// holds for it (live revisions arrive through receive): every rendered
// line is a task row.
func TestTaskChannelNeverShowsMessageHeaders(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	f.selectTaskChannel(t, "tasks/work")
	if len(f.m.msgs["tasks/work"]) == 0 {
		t.Fatal("fixture: expected raw revisions in m.msgs")
	}
	for _, l := range strings.Split(ansi.Strip(f.m.renderStream()), "\n") {
		l = strings.TrimLeft(l, " ")
		if !strings.HasPrefix(l, taskPending) && !strings.HasPrefix(l, taskInProgress) && !strings.HasPrefix(l, taskCompleted) {
			t.Fatalf("non-task row in task pane: %q", l)
		}
	}
}

// Fix round 1 (#9): the dim "→ owner" arrow is a pending-only suffix. An
// owner that survives onto a completed task (assigned, then finished without
// ever going through in_progress) must show no arrow at all -- the row is
// just dimmed whole, like any other completed row.
func TestCompletedTaskWithOwnerShowsNoArrow(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	done, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "done"})
	if err != nil {
		t.Fatal(err)
	}
	owner, completed := f.sam, "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: done.ID, Owner: &owner, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	line := ansi.Strip(f.m.renderStream())
	if strings.Contains(line, "→") {
		t.Fatalf("completed task with an owner must show no arrow: %q", line)
	}
}

// Fix round 1 (#9): an in-progress task owned by the TUI's own identity uses
// the user icon and color, matching how the session rail distinguishes the
// TUI's own row from everyone else's.
func TestTaskInProgressOwnedBySelfUsesUserIcon(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	mine, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.b.TaskClaim(f.c.as, mine.ID, time.Now().Add(time.Hour).UnixMilli(), ""); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	raw := f.m.renderStream()
	userOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.User).Render("\x00"), "\x00")
	agentOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Agent).Render("\x00"), "\x00")
	if !strings.Contains(raw, iconUser+userOpen+f.c.as) {
		t.Fatalf("self-owned in-progress row must use the user icon and color: %q", raw)
	}
	if strings.Contains(raw, agentOpen+f.c.as) {
		t.Fatalf("self-owned in-progress row must not use the agent color: %q", raw)
	}
}
