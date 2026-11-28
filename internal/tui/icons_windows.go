//go:build windows

package tui

// Windows Terminal honors U+FE0F and advances two cells for the gear, as
// lipgloss counts, so the Unix CSI 1C trick (icons_unix.go) would put every
// line with a gear one cell past the width: a full-width line wrapped, the
// alt screen scrolled, and redraws left stale rows behind.
const gearIcon = "\u2699\uFE0F "

// stopwatchIcon is the in-progress task mark, plain for the same reason.
const stopwatchIcon = "\u23F1\uFE0F "

// wastebasketIcon is the pruned-memory mark, plain for the same reason.
const wastebasketIcon = "\U0001F5D1\uFE0F "
