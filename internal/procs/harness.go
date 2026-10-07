package procs

import (
	"path/filepath"
	"strings"
)

// shells are the command names FindHarness walks past: the background shell a
// harness spawns to run a command (`zsh -c ...`, `cmd /c ...`), and the login
// shell a harness itself may have been started from are not the harness.
// Claude Code on Windows runs hooks through Git Bash, so a stop hook's
// ancestry there is claude.exe -> bash.exe -> agentbus.exe.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"cmd": true, "powershell": true, "pwsh": true,
}

// isShell reports whether a process table command name is a shell: compared
// by basename, with a login shell's leading '-' stripped, case-insensitively
// and without one trailing ".exe" (Windows reports "Bash.EXE", "cmd.exe").
func isShell(comm string) bool {
	name := strings.ToLower(strings.TrimPrefix(filepath.Base(comm), "-"))
	return shells[strings.TrimSuffix(name, ".exe")]
}

// FindHarness returns the process that started the command running as self:
// the first ancestor that is not a shell. A shell between the two (a
// harness's `zsh -c` background command) can outlive the harness, so the
// direct parent is not enough. A command name is compared by basename with a
// login shell's leading '-' stripped. Reaching pid 1 or below without finding
// a non-shell means the harness is already gone (the shell was orphaned): it
// returns ErrGone.
func FindHarness(t Table, self int) (Ref, error) {
	pid, err := t.Parent(self)
	if err != nil {
		return Ref{}, err
	}
	for pid > 1 {
		comm, err := t.Comm(pid)
		if err != nil {
			return Ref{}, err
		}
		if !isShell(comm) {
			start, err := t.StartTime(pid)
			if err != nil {
				return Ref{}, err
			}
			return Ref{Pid: pid, Start: start}, nil
		}
		if pid, err = t.Parent(pid); err != nil {
			return Ref{}, err
		}
	}
	return Ref{}, ErrGone
}
