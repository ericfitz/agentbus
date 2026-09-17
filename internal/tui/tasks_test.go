package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
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
	if !strings.Contains(lines[0], "○ #") || !strings.Contains(lines[0], "a") {
		t.Fatalf("line0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") || !strings.Contains(lines[1], "◐") || !strings.Contains(lines[1], "@") {
		t.Fatalf("line1 = %q", lines[1])
	}
	if !strings.Contains(lines[2], "⊘") || !strings.Contains(lines[2], "#"+itoa(a.ID)) {
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
	if !strings.Contains(before, "⊘") {
		t.Fatalf("expected b blocked before a completes:\n%s", before)
	}

	completed := "completed"
	if _, err := f.ab.TaskUpdate(f.sam, bus.TaskPatch{ID: a.ID, Status: &completed}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)

	after := ansi.Strip(f.m.renderStream())
	if !strings.Contains(after, "●") {
		t.Fatalf("expected a completed:\n%s", after)
	}
	if strings.Contains(after, "⊘") {
		t.Fatalf("expected b no longer blocked:\n%s", after)
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
