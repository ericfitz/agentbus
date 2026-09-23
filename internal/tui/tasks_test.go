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

	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 3 {
		t.Fatalf("want at least 3 lines, got %d: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], taskPending+"#") || !strings.Contains(lines[0], "a") {
		t.Fatalf("line0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  "+taskInProgress+"#") || !strings.Contains(lines[1], ansi.Strip(iconAgent)+"Sam") {
		t.Fatalf("line1 = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], taskPending+"#") || !strings.Contains(lines[2], "blocked by #"+itoa(a.ID)) {
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

	for _, k := range []string{"i", "enter", "r", "m"} {
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

// TestTaskChannelMoveCursorIsNoop covers T6-1 of the final review: a
// search jump can land the cursor on one of a task channel's raw revisions
// in m.msgs (never rendered; renderTasks draws the tree instead, which is
// why the jump itself is harmless), but arrow keys after that must not walk
// those hidden rows. The jump itself is out of scope here (see the search
// tests); this sets the cursor directly to reach the same state.
func TestTaskChannelMoveCursorIsNoop(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: "b"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	f.selectTaskChannel(t, "tasks/work")
	if len(f.m.msgs["tasks/work"]) < 2 {
		t.Fatalf("expected m.msgs to hold the channel's raw revisions, got %d", len(f.m.msgs["tasks/work"]))
	}
	f.m.cursor = 0

	f.key("down")
	if f.m.cursor != 0 {
		t.Fatalf("down moved the cursor to %d on a task channel", f.m.cursor)
	}
	f.key("up")
	if f.m.cursor != 0 {
		t.Fatalf("up moved the cursor to %d on a task channel", f.m.cursor)
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

	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	deepest := lines[len(lines)-1]
	trimmed := strings.TrimLeft(deepest, " ")
	if got := len(deepest) - len(trimmed); got != 12 {
		t.Fatalf("leading spaces = %d, want 12: %q", got, deepest)
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
	if !strings.HasPrefix(raw[2], dimOpen+taskCompleted) {
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
