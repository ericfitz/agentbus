package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

func TestHealthOverlayShowsStorageSessionsThemeAndConfig(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.m.height = 69 // tall enough that the whole body (plus #15's version line, #17's icons line, and the selection_text theme key) fits without scrolling
	f.key("esc")
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	v := f.m.View()
	for _, want := range []string{"storage", "2.0 GiB", "sessions 2 live", "channels 7 (3 memory)", "embeddings unset", "receive", "log ·", "agentbus.log", "theme · default", "agent cyan", "config ·", filepath.Base(f.c.cfg.Path), "\"tui_name\""} {
		if !strings.Contains(v, want) {
			t.Errorf("health lacks %q:\n%s", want, v)
		}
	}
	// The box wraps long lines and the config JSON runs past the fixture's
	// height; check the unwrapped body for the full log path and the JSON.
	body := strings.Join(f.m.healthLines(), "\n")
	for _, want := range []string{filepath.Join(f.c.cfg.DataDirectory, "agentbus.log"), "\"agent\": \"cyan\"", "\"sqlite_budget_mib\": 2048"} {
		if !strings.Contains(body, want) {
			t.Errorf("health body lacks %q:\n%s", want, body)
		}
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes")
	}
}

func TestHealthLinesShowVersion(t *testing.T) {
	f := newFixture(t)
	body := strings.Join(f.m.healthLines(), "\n")
	if !strings.Contains(body, "version") || !strings.Contains(body, mcpserver.Version) {
		t.Fatalf("health body lacks the version:\n%s", body)
	}
}

// TestHealthOverlayScrollReachesConfigJSON covers the fixture's default
// (shorter) terminal height, where the body is taller than the box and the
// config JSON near the bottom is only reachable by scrolling. It scrolls
// well past the end (200 downs against a body far shorter than that) to
// confirm the clamp stops at the last line rather than running off into a
// blank body.
func TestHealthOverlayScrollReachesConfigJSON(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("h")
	for i := 0; i < 200; i++ {
		f.key("down")
	}
	if want := len(f.m.healthLines()) - 1; f.m.health.scroll != want {
		t.Fatalf("scroll = %d, want clamped at %d", f.m.health.scroll, want)
	}
	v := f.m.View()
	if !strings.Contains(v, "}") {
		t.Fatalf("scrolling to the clamp must still show the config JSON's closing brace, got:\n%s", v)
	}
}

// TestHealthOverlaySurvivesUsageOverBudget covers the reachable state where
// usage has grown past the budget (the bus only starts evicting once it's
// over, and the capacity notice exists for exactly that moment): the
// storage bar's fill must clamp rather than passing strings.Repeat a
// negative count.
func TestHealthOverlaySurvivesUsageOverBudget(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("h") // openHealth also issues its own statusCmd; set the override after it lands
	f.m.status.UsageBytes = 3 << 30
	f.m.status.BudgetBytes = 2 << 30
	v := f.m.View()
	if !strings.Contains(v, "150%") {
		t.Fatalf("over-budget usage must still show its percentage, got:\n%s", v)
	}
}

func TestConfigEditedReloadsAndReportsErrors(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "tui_name") {
		t.Fatalf("invalid config must toast the validation error, got %q", f.m.toast)
	}
	if err := os.WriteFile(f.c.cfg.Path, []byte(`{"tui_name": "eric"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "restart") {
		t.Fatalf("valid config must say a restart applies it, got %q", f.m.toast)
	}
}

// A GUI $VISUAL runs in the background: o returns at once and the TUI stays
// live; the config check toasts when the editor exits. A terminal editor
// still gets the terminal (blocking).
func TestVisualEditorRunsInBackground(t *testing.T) {
	for _, tc := range []struct {
		visual string
		bg     bool
	}{
		{"code --wait", true},
		{"'/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code' --wait", true},
		{"vim", false},
		{"/usr/bin/nano -w", false},
		{"'/opt/homebrew/bin/nvim'", false},
		{"", false},
	} {
		t.Setenv("VISUAL", tc.visual)
		if _, ok := backgroundEditor("x.json"); ok != tc.bg {
			t.Errorf("VISUAL=%q: background=%v, want %v", tc.visual, ok, tc.bg)
		}
	}

	f := newFixture(t)
	f.key("esc")
	f.key("h")
	t.Setenv("VISUAL", "true")
	cmd := f.m.updateHealth(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if cmd == nil || f.m.mode != modeHealth {
		t.Fatalf("o must return a background command and stay in health, mode=%v", f.m.mode)
	}
	if msg, ok := cmd().(configEditedMsg); !ok || msg.err != nil {
		t.Fatalf("editor exit must report configEditedMsg without error, got %#v", msg)
	}
}

func TestEditorCommandPrecedence(t *testing.T) {
	// The editor runs through sh -c with the path as $1, so a value with
	// arguments or with spaces in its path both work.
	line := func() string {
		got := editorCommand("x.md").Args
		if len(got) != 5 || got[0] != "/bin/sh" || got[1] != "-c" || got[4] != "x.md" {
			t.Fatalf("editor must run as sh -c <line> sh <path>: %v", got)
		}
		return got[2]
	}
	t.Setenv("VISUAL", "code -w")
	t.Setenv("EDITOR", "nano")
	if got := line(); got != `code -w "$1"` {
		t.Fatalf("VISUAL must win over EDITOR, with its own args: %q", got)
	}
	spaced := filepath.Join(t.TempDir(), "Visual Studio Code.app", "Electron")
	if err := os.MkdirAll(filepath.Dir(spaced), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spaced, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", spaced)
	if got := editorCommand("x.md").Args; len(got) != 2 || got[0] != spaced || got[1] != "x.md" {
		t.Fatalf("an existing file with spaces in its path must run as-is, got %v", got)
	}
	t.Setenv("VISUAL", "")
	if got := line(); got != `nano "$1"` {
		t.Fatalf("EDITOR must win when VISUAL is unset, got %q", got)
	}
	t.Setenv("EDITOR", "")
	if got := line(); got != `vi "$1"` {
		t.Fatalf("default must be vi, got %q", got)
	}
	t.Setenv("VISUAL", "   ") // whitespace-only used to panic on parts[0]
	if got := line(); got != `vi "$1"` {
		t.Fatalf("whitespace-only VISUAL must fall back to vi without panicking, got %q", got)
	}
}

// A $VISUAL that names no command makes sh exit 127; the toast says so and
// shows the setting instead of a bare "exit status 127".
func TestEditorErrTextExplainsMissingCommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no such editor")
	t.Setenv("VISUAL", missing)
	err := editorCommand("x.md").Run()
	if got := editorErrText(err); !strings.Contains(got, "not found") || !strings.Contains(got, missing) || !strings.Contains(got, "$VISUAL") {
		t.Fatalf("editorErrText(%v) = %q", err, got)
	}
}
