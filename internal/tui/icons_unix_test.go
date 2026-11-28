//go:build !windows

package tui

import (
	"strings"
	"testing"
	"time"
)

// Terminal.app advances one cell for the text-presentation gear and
// stopwatch even with U+FE0F, while lipgloss counts two (measured with
// CSI 6n on 2026-11-21: column 2 after either, column 3 after the Wide
// cross and check marks). Each carries CSI 2X before and CSI 1C after.
func TestNeutralWidthIconsCarryCursorEscapesUnix(t *testing.T) {
	for name, got := range map[string]string{"gearIcon": gearIcon, "stopwatchIcon": stopwatchIcon, "taskInProgress": emojiIcons["task_in_progress"]} {
		want := "\x1b[2X⚙️\x1b[1C "
		if name != "gearIcon" {
			want = "\x1b[2X⏱️\x1b[1C "
		}
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// The pruned icon is Neutral width like the gear, so it carries the same
// cursor escapes, and the pruned header and search line keep them through
// dimming.
func TestWastebasketIconCarriesCursorEscapesUnix(t *testing.T) {
	if want := "\x1b[2X\U0001F5D1\uFE0F\x1b[1C "; wastebasketIcon != want || iconPruned != want {
		t.Fatalf("wastebasketIcon = %q, iconPruned = %q, want %q", wastebasketIcon, iconPruned, want)
	}
}

func TestPrunedRowsKeepWastebasketEscapesUnix(t *testing.T) {
	f, id := onMemory(t)
	other := otherMemory(t, f, id)
	rawExec(t, f, "UPDATE memory_access SET pruned_at=?, pruned_reason='noise' WHERE memory_id=?", time.Now().UnixMilli(), other)
	f.reloadMem(t)
	if !strings.Contains(f.m.renderStream(), wastebasketIcon+"pruned (noise)") {
		t.Fatalf("pruned header lost the icon wrapper: %q", f.m.renderStream())
	}
	f.key("/")
	f.key("Reviewer")
	f.key("enter")
	if len(f.m.search.hits) == 0 {
		t.Fatalf("no hits: %v", f.m.search.err)
	}
	if !strings.Contains(f.m.View(), wastebasketIcon+"pruned") {
		t.Fatalf("pruned search line lost the icon wrapper: %q", f.m.View())
	}
}
