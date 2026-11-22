//go:build !windows

package tui

import "testing"

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
