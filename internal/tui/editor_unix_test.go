//go:build !windows

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The background half of TestVisualEditorRunsInBackground: "true" is a Unix
// command.
func TestVisualEditorBackgroundCommandUnix(t *testing.T) {
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
