package tui

import (
	"fmt"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

// TestBatchReloadsOnlyTheSelectedTaskList: a receive batch that touches
// task lists reloads the one on screen; the others load when selected.
func TestBatchReloadsOnlyTheSelectedTaskList(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)
	if _, err := f.ab.CreateChannel(f.sam, "tasks/zeta", "memory"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"tasks/work", "tasks/zeta"} {
		if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: ch, Subject: "new in " + ch}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(f.m.tasks["tasks/work"])
	f.send(batchMsg{res: bus.ReceiveResult{Messages: []bus.Message{
		{Channel: "tasks/work", Seq: 1000}, {Channel: "tasks/zeta", Seq: 1001},
	}}})
	if got := len(f.m.tasks["tasks/work"]); got != before+1 {
		t.Fatalf("selected list has %d tasks, want %d", got, before+1)
	}
	if got := len(f.m.tasks["tasks/zeta"]); got != 0 {
		t.Fatalf("unselected list was loaded: %d tasks", got)
	}
	f.run(f.m.statusCmd())
	f.key("shift+tab") // back to the rail
	f.selectTaskChannel(t, "tasks/zeta")
	if got := len(f.m.tasks["tasks/zeta"]); got != 1 {
		t.Fatalf("selecting the list loaded %d tasks, want 1", got)
	}
}

// TestTasksReloadScrollsToBottomWhileFollowing: a growing task tree follows
// to its newest row, as a growing message stream does.
func TestTasksReloadScrollsToBottomWhileFollowing(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.taskTree(t)
	for i := range 60 {
		if _, err := f.ab.TaskCreate(f.sam, bus.TaskCreateInput{Channel: "tasks/work", Subject: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	f.m.cursor, f.m.follow = -1, true
	f.run(f.m.loadTasks("tasks/work"))
	if !f.m.stream.AtBottom() {
		t.Fatalf("following tree not at bottom: offset %d of %d lines", f.m.stream.YOffset, f.m.stream.TotalLineCount())
	}
}
