package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

func seedMemories(t *testing.T, f *fixture) (first, second bus.SendResult) {
	t.Helper()
	first = f.agentSend(t, "notes", "Release procedure\nbump, tag, push")
	second = f.agentSend(t, "notes", "Reviewer checklist")
	if _, err := f.ab.EditMemory(f.sam, bus.EditInput{ID: *first.MemoryID, Content: "Release procedure\nbump, tag, push, run release.sh"}); err != nil {
		t.Fatal(err)
	}
	f.receive(t)
	return first, second
}

func TestMemoriesOverlayListsNewestFirstWithRevisions(t *testing.T) {
	f := newFixture(t)
	first, _ := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	if f.m.mode != modeMemories || f.m.mem.channel != "notes" {
		t.Fatalf("mode=%v channel=%q err=%v", f.m.mode, f.m.mem.channel, f.m.mem.err)
	}
	if len(f.m.mem.list) != 2 || f.m.mem.list[0].Content != "Reviewer checklist" {
		t.Fatalf("list=%+v", f.m.mem.list)
	}
	f.key("down")
	if got := f.m.mem.list[f.m.mem.cursor].MemoryID; got == nil || *got != *first.MemoryID {
		t.Fatalf("cursor not on first memory")
	}
	if len(f.m.mem.revs) != 2 || f.m.mem.rev != 1 {
		t.Fatalf("revs=%d rev=%d err=%v", len(f.m.mem.revs), f.m.mem.rev, f.m.mem.err)
	}
	v := f.m.View()
	if !strings.Contains(v, "2 memories") || !strings.Contains(v, "revision 2 of 2") || !strings.Contains(v, "run release.sh") {
		t.Fatalf("view:\n%s", v)
	}
	f.key("left")
	if v := f.m.View(); !strings.Contains(v, "revision 1 of 2") || strings.Contains(v, "run release.sh") {
		t.Fatalf("left must show the older revision:\n%s", v)
	}
	f.key("right")
	if f.m.mem.rev != 1 {
		t.Fatal("right returns to the current revision")
	}
}

func TestMemoryEditViaEditorFile(t *testing.T) {
	f := newFixture(t)
	_, second := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	// Simulate the editor having returned: write the temp file ourselves,
	// under the test's own tmp dir rather than the shared one the real
	// editMemoryInEditor now uses (os.CreateTemp).
	path := filepath.Join(t.TempDir(), "memory.md")
	if err := os.WriteFile(path, []byte("Reviewer checklist\nrun -race\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(memEditedMsg{id: *second.MemoryID, path: path, original: "Reviewer checklist"})
	f.receive(t)
	cur, err := f.c.b.GetMemory(f.c.as, *second.MemoryID)
	if err != nil || cur.Content != "Reviewer checklist\nrun -race" || *cur.Revision != 2 {
		t.Fatalf("edit not applied: %+v %v", cur, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("temp file must be removed after the edit")
	}
	if f.m.mem.list[0].Content != "Reviewer checklist\nrun -race" {
		t.Fatalf("list must refresh after edit: %+v", f.m.mem.list[0])
	}
}

func TestMemoryDeleteAsksThenTombstones(t *testing.T) {
	f := newFixture(t)
	_, second := seedMemories(t, f)
	f.key("esc")
	f.key("m")
	f.key("d")
	if f.m.mode != modeConfirmDelete {
		t.Fatal("d asks for confirmation")
	}
	if v := f.m.View(); !strings.Contains(v, "delete memory #"+itoa(*second.MemoryID)) || !strings.Contains(v, "Reviewer checklist") {
		t.Fatalf("view:\n%s", v)
	}
	f.key("n")
	if f.m.mode != modeMemories {
		t.Fatal("n cancels")
	}
	f.key("d")
	f.key("y")
	if _, err := f.c.b.GetMemory(f.c.as, *second.MemoryID); err == nil {
		t.Fatal("memory must be deleted")
	}
	if len(f.m.mem.list) != 1 || f.m.mode != modeMemories {
		t.Fatalf("list=%d mode=%v", len(f.m.mem.list), f.m.mode)
	}
}

func TestMemoriesWithoutMemoryChannelToasts(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam, m: New(c, LoadTheme(func(string) string { return "" }, nil))}
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	f.key("esc")
	f.key("m")
	if f.m.mode != modeNormal || !strings.Contains(f.m.toast, "no memory channels") {
		t.Fatalf("mode=%v toast=%q", f.m.mode, f.m.toast)
	}
}

// TestMemoriesOverlayWindowsALongList seeds 12 memories in a short terminal
// and checks that moving the cursor to the last one keeps it visible and the
// whole view still fits inside the overlay's height, per the controller's
// windowing ruling for lists that can hold up to 200 rows.
func TestMemoriesOverlayWindowsALongList(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 12; i++ {
		f.agentSend(t, "notes", fmt.Sprintf("memo %d", i))
	}
	f.receive(t)
	f.m.height = 14
	f.key("esc")
	f.key("m")
	if len(f.m.mem.list) != 12 {
		t.Fatalf("list=%d", len(f.m.mem.list))
	}
	for i := 0; i < 11; i++ {
		f.key("down")
	}
	if f.m.mem.cursor != 11 {
		t.Fatalf("cursor=%d", f.m.mem.cursor)
	}
	v := f.m.View()
	if !strings.Contains(v, "memo 0") {
		t.Fatalf("last memory (title %q) must be visible:\n%s", "memo 0", v)
	}
	lines := strings.Split(v, "\n")
	if len(lines) > f.m.height {
		t.Fatalf("view has %d lines, want <= height %d:\n%s", len(lines), f.m.height, v)
	}
}

// TestMovingCursorClearsStaleRevisionsBeforeReload guards against the
// detail section showing one memory's revision label over another's
// content while MemoryRevisions is still in flight: the cursor move must
// clear m.mem.revs synchronously, before the returned command runs.
func TestMovingCursorClearsStaleRevisionsBeforeReload(t *testing.T) {
	f := newFixture(t)
	seedMemories(t, f)
	f.key("esc")
	f.key("m")
	if len(f.m.mem.revs) == 0 {
		t.Fatal("cursor 0 must already have revisions loaded")
	}
	next, cmd := f.m.Update(tea.KeyMsg{Type: tea.KeyDown})
	f.m = next.(Model)
	if len(f.m.mem.revs) != 0 {
		t.Fatal("revisions must clear immediately on cursor move, before the reload lands")
	}
	wantID := f.m.mem.currentID()
	f.run(cmd)
	if len(f.m.mem.revs) == 0 || *f.m.mem.revs[0].MemoryID != wantID {
		t.Fatalf("revisions must reload for the new cursor: %+v", f.m.mem.revs)
	}
}

func TestEditorCommandPrecedence(t *testing.T) {
	t.Setenv("VISUAL", "code -w")
	t.Setenv("EDITOR", "nano")
	if got := editorCommand("x.md").Args; len(got) != 3 || got[0] != "code" || got[1] != "-w" {
		t.Fatalf("VISUAL must win over EDITOR, with its own args: %v", got)
	}
	t.Setenv("VISUAL", "")
	if got := editorCommand("x.md").Args[0]; got != "nano" {
		t.Fatalf("EDITOR must win when VISUAL is unset, got %q", got)
	}
	t.Setenv("EDITOR", "")
	if got := editorCommand("x.md").Args[0]; got != "vi" {
		t.Fatalf("default must be vi, got %q", got)
	}
	t.Setenv("VISUAL", "   ") // whitespace-only used to panic on parts[0]
	if got := editorCommand("x.md").Args[0]; got != "vi" {
		t.Fatalf("whitespace-only VISUAL must fall back to vi without panicking, got %q", got)
	}
}
