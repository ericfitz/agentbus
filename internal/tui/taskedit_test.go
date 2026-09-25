package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/muesli/termenv"
)

// openTasks creates tasks/work with unassigned tasks of the given subjects,
// selects it and focuses the stream (cursor on the last task).
func (f *fixture) openTasks(t *testing.T, subjects ...string) []bus.Task {
	t.Helper()
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	var out []bus.Task
	for _, s := range subjects {
		tk, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: s})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tk)
	}
	f.run(f.m.statusCmd())
	f.selectTaskChannel(t, "tasks/work")
	f.key("tab")
	return out
}

// task reads id back from the bus as the TUI.
func (f *fixture) task(t *testing.T, id int64) bus.Task {
	t.Helper()
	tk, err := f.c.b.TaskGet(f.c.as, id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// ansiProfile forces real escape codes for color assertions.
func ansiProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// TestSpaceCyclesADraftAndAssignsAnUnassignedTask (ADR 0011 decisions 2,
// 6): space drafts pending → in progress → completed → pending, taking the
// task while off pending; the row shows the draft in the unsaved color
// with a hint toast; nothing reaches the bus; a full cycle clears it.
func TestSpaceCyclesADraftAndAssignsAnUnassignedTask(t *testing.T) {
	ansiProfile(t)
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	unsaved, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Unsaved).Render("\x00"), "\x00")
	f.key(" ")
	if d := f.m.draft; d.ch != "tasks/work" || d.id != a.ID || d.status != "in_progress" || d.owner != f.c.as {
		t.Fatalf("draft after one space = %+v", d)
	}
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("toast = %q hint=%v", f.m.toast, f.m.toastHint)
	}
	raw := f.m.renderStream()
	if !strings.Contains(raw, unsaved+taskInProgress) || !strings.Contains(ansi.Strip(raw), f.c.as) {
		t.Fatalf("drafted row must show in progress with the TUI's name in the unsaved color: %q", raw)
	}
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("a draft must not touch the bus: %+v", got)
	}
	f.key(" ")
	if d := f.m.draft; d.status != "completed" || d.owner != f.c.as {
		t.Fatalf("draft after two = %+v", d)
	}
	if raw = f.m.renderStream(); !strings.Contains(raw, unsaved+taskCompleted) {
		t.Fatalf("drafted completed row is unsaved-colored, not dim: %q", raw)
	}
	f.key(" ")
	if f.m.draft.id != 0 {
		t.Fatalf("cycling back to the saved state clears the draft: %+v", f.m.draft)
	}
	if raw = f.m.renderStream(); strings.Contains(raw, unsaved+taskPending) {
		t.Fatalf("color returns to normal: %q", raw)
	}
}

// TestEnterSavesTheDraftAsAClaimThenAnUpdate: into in progress is a claim
// (owner and status), the rest a task_update carrying status and owner;
// the tree refreshes after each save and reopening your task keeps it yours.
func TestEnterSavesTheDraftAsAClaimThenAnUpdate(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("enter")
	if f.m.draft.id != 0 || f.m.toast != "" {
		t.Fatalf("enter clears the draft quietly: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" || got.Owner != f.c.as {
		t.Fatalf("enter must claim: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Status != "in_progress" || got.Owner != f.c.as {
		t.Fatalf("the tree refreshes after a save: %+v", got)
	}
	f.key(" ") // completed
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "completed" || got.Owner != f.c.as {
		t.Fatalf("complete: %+v", got)
	}
	f.key(" ") // back to pending, still yours
	if d := f.m.draft; d.status != "pending" || d.owner != f.c.as {
		t.Fatalf("reopening a task you own keeps the assignment in the draft: %+v", d)
	}
	f.key("enter")
	if got := f.task(t, a.ID); got.Status != "pending" || got.Owner != f.c.as {
		t.Fatalf("reopen keeps it yours: %+v", got)
	}
	f.key("enter")
	if f.m.toast != "" {
		t.Fatalf("enter with no draft does nothing: %q", f.m.toast)
	}
}

// TestBusRefusalKeepsTheDraftForEsc: b is blocked, so claiming it is
// refused; the bus's message shows in the error style (hint style does not
// stick), the draft stays, and esc cancels only the draft.
func TestBusRefusalKeepsTheDraftForEsc(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	_, _, b := f.taskTree(t) // cursor on b, blocked by a1
	f.key(" ")
	if f.m.toast != draftHintToast || !f.m.toastHint {
		t.Fatalf("hint first: %q", f.m.toast)
	}
	f.key("enter")
	if !strings.Contains(f.m.toast, "blocked") || f.m.toastHint {
		t.Fatalf("refusal toast = %q hint=%v, want the bus's message in the error style", f.m.toast, f.m.toastHint)
	}
	if f.m.draft.id != b.ID {
		t.Fatalf("a refused save keeps the draft: %+v", f.m.draft)
	}
	if got := f.task(t, b.ID); got.Status != "pending" || got.Owner != "" {
		t.Fatalf("nothing saved: %+v", got)
	}
	f.key("esc")
	if f.m.draft.id != 0 || f.m.cursor < 0 {
		t.Fatalf("esc cancels the draft and only the draft: draft=%+v cursor=%d", f.m.draft, f.m.cursor)
	}
	if got := f.task(t, b.ID); got.Status != "pending" {
		t.Fatalf("esc writes nothing: %+v", got)
	}
}

// TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision (decision 2): the
// draft goes, with "change discarded", when the cursor leaves its row, when
// focus leaves the task pane, and when the bus shows a new revision of the
// drafted task; a revision of another task leaves it alone.
func TestDraftIsDiscardedOnCursorMoveFocusChangeAndRevision(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.key("up")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || !f.m.toastHint {
		t.Fatalf("cursor move: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key(" ") // draft on a
	f.key("home")
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("focus change: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
	f.key("tab") // stream again, cursor on the last task (b)
	f.key(" ")
	drafted, ok := f.m.cursorTask()
	if !ok || f.m.draft.id != drafted.ID {
		t.Fatalf("draft=%+v cursor=%+v", f.m.draft, drafted)
	}
	other := ts[0].ID
	if other == drafted.ID {
		other = ts[1].ID
	}
	desc := "changed elsewhere"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: other, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != drafted.ID {
		t.Fatalf("a revision of another task keeps the draft: %+v", f.m.draft)
	}
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: drafted.ID, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast {
		t.Fatalf("revision of the drafted task: draft=%+v toast=%q", f.m.draft, f.m.toast)
	}
}

// TestDraftIsDiscardedWhenTheTaskDisappears: the drafted task drops out of
// the next tree; the cursor clamps and the draft is discarded.
func TestDraftIsDiscardedWhenTheTaskDisappears(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "a", "b") // cursor on b
	f.key(" ")
	f.send(tasksMsg{ch: "tasks/work", tasks: []bus.TaskSummary{{ID: ts[0].ID, Subject: "a", Status: "pending"}}})
	if f.m.draft.id != 0 || f.m.toast != draftDiscardedToast || f.m.cursor != 0 {
		t.Fatalf("draft=%+v toast=%q cursor=%d", f.m.draft, f.m.toast, f.m.cursor)
	}
}

// TestAgentOwnedTaskCannotBeChanged (decision 1): a task owned by anyone
// else, in progress or merely assigned, refuses space, t and u with the
// owned-by toast and never drafts.
func TestAgentOwnedTaskCannotBeChanged(t *testing.T) {
	f := newFixture(t)
	ts := f.openTasks(t, "theirs", "assigned") // cursor on "assigned"
	if _, err := f.ab.TaskClaim(f.sam, ts[0].ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	owner := f.sam
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: ts[1].ID, Owner: &owner}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	want := ownedToast(f.sam)
	for _, id := range []int64{ts[1].ID, ts[0].ID} {
		for _, k := range []string{" ", "t", "u"} {
			f.key(k)
			if f.m.toast != want || f.m.toastHint || f.m.draft.id != 0 {
				t.Fatalf("task %d key %q: toast=%q hint=%v draft=%+v", id, k, f.m.toast, f.m.toastHint, f.m.draft)
			}
		}
		f.key("up")
	}
	if got := f.task(t, ts[0].ID); got.Owner != f.sam || got.Status != "in_progress" {
		t.Fatalf("untouched: %+v", got)
	}
	if got := f.task(t, ts[1].ID); got.Owner != f.sam || got.Status != "pending" {
		t.Fatalf("untouched: %+v", got)
	}
}

// TestTakeAndUnassign (decision 6): t takes an unassigned task at once
// without changing its state; u clears your not-started task and refuses
// any other state; both only hint while a draft is pending; u on an
// unassigned task does nothing.
func TestTakeAndUnassign(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key("u")
	if f.m.toast != "" {
		t.Fatalf("u on an unassigned task does nothing: %q", f.m.toast)
	}
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a task list is take, not the tag prompt")
	}
	if got := f.task(t, a.ID); got.Owner != f.c.as || got.Status != "pending" {
		t.Fatalf("take: %+v", got)
	}
	if got, ok := f.m.cursorTask(); !ok || got.Owner != f.c.as {
		t.Fatalf("the tree refreshes after take: %+v", got)
	}
	f.key("t")
	if got := f.task(t, a.ID); got.Owner != f.c.as || f.m.toast != "" {
		t.Fatalf("t on your own task is a no-op: %+v toast=%q", got, f.m.toast)
	}
	f.key("u")
	if got := f.task(t, a.ID); got.Owner != "" || got.Status != "pending" {
		t.Fatalf("unassign: %+v", got)
	}
	f.key(" ")
	f.key("enter") // in progress, yours
	f.key("u")
	if f.m.toast != unassignRefusedToast || f.m.toastHint {
		t.Fatalf("u on an in-progress task: toast=%q", f.m.toast)
	}
	if got := f.task(t, a.ID); got.Owner != f.c.as {
		t.Fatalf("refused unassign changed the task: %+v", got)
	}
	f.key(" ") // draft: completed
	for _, k := range []string{"t", "u"} {
		f.key(k)
		if f.m.toast != draftHintToast || !f.m.toastHint || f.m.draft.id != a.ID {
			t.Fatalf("%q while a draft is pending: toast=%q draft=%+v", k, f.m.toast, f.m.draft)
		}
	}
	if got := f.task(t, a.ID); got.Status != "in_progress" {
		t.Fatalf("t/u while a draft is pending change nothing: %+v", got)
	}
}

// TestDraftSurvivesABacklogRevisionItAlreadyReflects: a task edited before
// the draft is taken is already at the revision the draft was built from
// (space fetches it fresh), so that edit's message finally arriving via a
// backlogged "oldest" subscription must not look like a newer change.
func TestDraftSurvivesABacklogRevisionItAlreadyReflects(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	desc := "already there before the draft"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: a.ID, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	f.key(" ") // drafts from the current, already-edited revision
	if f.m.draft.id != a.ID {
		t.Fatalf("draft = %+v", f.m.draft)
	}
	f.receive(t) // delivers the creation and the edit, both backlogged until now
	if f.m.draft.id != a.ID {
		t.Fatalf("a revision the draft already reflects must not discard it: %+v", f.m.draft)
	}
}

// TestSecondDraftSurvivesTheFirstSavesOwnRevision: enter's own save
// produces a revision that reaches the model asynchronously; a new draft
// started right after, from that just-saved state, must not be discarded
// when that revision finally arrives.
func TestSecondDraftSurvivesTheFirstSavesOwnRevision(t *testing.T) {
	f := newFixture(t)
	a := f.openTasks(t, "a")[0]
	f.key(" ")
	f.key("enter") // claims: a is in_progress, owned by the TUI, at a new revision
	f.key(" ")     // a second draft, taken from that just-saved revision
	if f.m.draft.id != a.ID {
		t.Fatalf("draft = %+v", f.m.draft)
	}
	f.receive(t) // delivers the claim's own revision, backlogged until now
	if f.m.draft.id != a.ID {
		t.Fatalf("the save's own revision must not discard the draft it enabled: %+v", f.m.draft)
	}
}
