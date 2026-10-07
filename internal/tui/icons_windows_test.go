//go:build windows

package tui

import (
	"strings"
	"testing"
)

// The Unix gear's CSI 1C overflows Windows Terminal, which advances two
// cells for the gear; on Windows the icon is the plain emoji and a space.
func TestGearIconHasNoEscapesWindows(t *testing.T) {
	if strings.Contains(gearIcon, "\x1b") || gearIcon != "\u2699\uFE0F " {
		t.Fatalf("gearIcon = %q", gearIcon)
	}
}
