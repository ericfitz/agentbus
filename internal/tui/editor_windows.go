//go:build windows

package tui

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const defaultEditor = "notepad"

// 9009 is the %ERRORLEVEL% for a missing command inside a cmd session, but
// cmd.exe /S /C exits 1 for one (windows.yml run 37650681754), the same code
// many editor failures use, so editorMissing looks the setting up instead.
// cmd has no distinct "not executable" code, so that constant never matches.
const (
	exitCommandNotFound = 9009
	exitNotExecutable   = -1
)

// editorMissing reports whether the program editor setting ed names is not
// on PATH (or is not an existing file), checked after the editor failed. A
// program found only in the current directory (exec.ErrDot) counts as
// present, since cmd.exe runs it. A cmd builtin such as start is not on
// PATH and reads as missing.
func editorMissing(ed string) bool {
	_, err := exec.LookPath(editorProgram(ed))
	return err != nil && !errors.Is(err, exec.ErrDot)
}

// cmdExePath is cmd.exe in the system directory. A bare "cmd.exe" would be
// resolved through PATH, and exec refuses a planted .\cmd.exe in the current
// directory (ErrDot) so the editor would never run.
func cmdExePath() string {
	if d, err := windows.GetSystemDirectory(); err == nil {
		return filepath.Join(d, "cmd.exe")
	}
	return "cmd.exe"
}

// shellEditorCommand runs an editor setting through cmd.exe. The command
// line is built by hand (see cmdExeCommandLine): Go's default argument
// escaping would let cmd mangle the quotes.
func shellEditorCommand(ed, path string) *exec.Cmd {
	cmd := exec.Command(cmdExePath())
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdExeCommandLine(ed, path)}
	return cmd
}
