//go:build windows

package tui

import (
	"os/exec"
	"syscall"
)

const defaultEditor = "notepad"

// cmd.exe exits 9009 when the command is not found; it has no distinct
// "not executable" code, so that constant never matches.
const (
	exitCommandNotFound = 9009
	exitNotExecutable   = -1
)

// shellEditorCommand runs an editor setting through cmd.exe. The command
// line is built by hand (see cmdExeCommandLine): Go's default argument
// escaping would let cmd mangle the quotes.
func shellEditorCommand(ed, path string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdExeCommandLine(ed, path)}
	return cmd
}
