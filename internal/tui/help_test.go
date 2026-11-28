package tui

import (
	"strings"
	"testing"
)

// TestTaskListsHelpRowIsItsOwnGroup covers T6-3 of the final review: the
// "(task lists)" row used to sit directly under the compose keys with no
// separator, reading as part of compose. It must now be its own trailing
// group, preceded by a blank separator row like every other group in
// helpLines.
func TestTaskListsHelpRowIsItsOwnGroup(t *testing.T) {
	f := newFixture(t)
	lines := f.m.helpLines()
	for i, l := range lines {
		if !strings.Contains(l, "task lists") {
			continue
		}
		if i == 0 || strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("(task lists) row at %d must be preceded by a blank separator, got %q", i, lines[i-1])
		}
		return
	}
	t.Fatal("(task lists) row not found in helpLines")
}

func TestHelpListsMemoryActions(t *testing.T) {
	f := newFixture(t)
	help := strings.Join(f.m.helpLines(), "\n")
	for _, want := range []string{
		"memory: restore the pruned cursor memory (agents see it again; its noise grace restarts)",
		"memory: clear the cursor memory's incorrect flag",
		"memory: delete the cursor memory, pruned or not (asks first)",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q", want)
		}
	}
}
