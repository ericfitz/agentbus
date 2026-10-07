//go:build !windows

package tui

// ponytail: gear is Neutral width: Terminal.app draws it two cells wide but
// advances one, while lipgloss counts two. CSI 1C moves the cursor one
// cell (uncounted by lipgloss) so both agree. Robot U+1F916 was rejected:
// Source Code Pro ships its own glyph there and U+FE0F does not override it.
// CSI 1C skips its cell without writing it, so when a redraw moves the
// gear onto a line that held a wide emoji (a new session sorting above
// the user row) the old glyph's half stays on screen. CSI 2X (erase two
// cells, cursor stays, also uncounted) blanks both cells first.
const gearIcon = "\x1b[2X\u2699\uFE0F\x1b[1C "
