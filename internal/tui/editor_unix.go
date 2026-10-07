//go:build !windows

package tui

import "os/exec"

const defaultEditor = "vi"

// The shell exits 127 when the editor command does not exist and 126 when
// it cannot be run.
const (
	exitCommandNotFound = 127
	exitNotExecutable   = 126
)

// shellEditorCommand runs an editor setting through the shell the way git
// runs GIT_EDITOR, with the file as $1, so arguments and quoting work.
func shellEditorCommand(ed, path string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", ed+` "$1"`, "sh", path)
}
