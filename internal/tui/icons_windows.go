//go:build windows

package tui

// Windows Terminal honors U+FE0F and advances two cells for the gear, as
// lipgloss counts, so the Unix CSI 1C trick (icons_unix.go) would put every
// line with a gear one cell past the width: a full-width line wrapped, the
// alt screen scrolled, and redraws left stale rows behind.
const gearIcon = "\u2699\uFE0F "
