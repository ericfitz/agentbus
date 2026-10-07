//go:build windows

package tui

import (
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

const defaultEditor = "notepad"

// cmd.exe exits 9009 when the command is not found; it has no distinct
// "not executable" code, so that constant never matches.
const (
	exitCommandNotFound = 9009
	exitNotExecutable   = -1
)

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
