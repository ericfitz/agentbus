package procs

import (
	"errors"
	"testing"
)

type fakeProc struct {
	ppid  int
	comm  string
	start int64
}

type fakeTable map[int]fakeProc

func (f fakeTable) get(pid int) (fakeProc, error) {
	p, ok := f[pid]
	if !ok {
		return fakeProc{}, ErrGone
	}
	return p, nil
}
func (f fakeTable) Parent(pid int) (int, error)  { p, err := f.get(pid); return p.ppid, err }
func (f fakeTable) Comm(pid int) (string, error) { p, err := f.get(pid); return p.comm, err }
func (f fakeTable) StartTime(pid int) (int64, error) {
	p, err := f.get(pid)
	return p.start, err
}

func TestFindHarness(t *testing.T) {
	cases := []struct {
		name string
		tbl  fakeTable
		want Ref
		err  error
	}{
		{"wait via zsh to claude", fakeTable{10: {20, "agentbus", 1}, 20: {30, "/bin/zsh", 2}, 30: {40, "claude", 3}, 40: {1, "-zsh", 4}}, Ref{30, 3}, nil},
		{"wait direct to claude", fakeTable{10: {30, "agentbus", 1}, 30: {40, "claude", 3}}, Ref{30, 3}, nil},
		{"orphaned shell", fakeTable{10: {20, "agentbus", 1}, 20: {1, "zsh", 2}}, Ref{}, ErrGone},
		{"login shell then harness", fakeTable{10: {20, "agentbus", 1}, 20: {30, "-zsh", 2}, 30: {40, "login", 3}}, Ref{30, 3}, nil},
		{"nested shells", fakeTable{10: {20, "agentbus", 1}, 20: {25, "bash", 2}, 25: {30, "sh", 3}, 30: {1, "codex", 7}}, Ref{30, 7}, nil},
		{"windows hook via git bash", fakeTable{10: {20, "agentbus.exe", 1}, 20: {30, "bash.exe", 2}, 30: {40, "claude.exe", 3}, 40: {1, "explorer.exe", 4}}, Ref{30, 3}, nil},
		{"windows cmd and powershell", fakeTable{10: {20, "agentbus.exe", 1}, 20: {25, "cmd.exe", 2}, 25: {30, "PowerShell.EXE", 3}, 30: {35, "pwsh.exe", 4}, 35: {1, "codex.exe", 5}}, Ref{35, 5}, nil},
		{"exe suffix is not a shell marker", fakeTable{10: {20, "agentbus.exe", 1}, 20: {1, "claude.exe", 2}}, Ref{20, 2}, nil},
		{"parent vanished", fakeTable{10: {20, "agentbus", 1}}, Ref{}, ErrGone},
	}
	for _, c := range cases {
		got, err := FindHarness(c.tbl, 10)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: FindHarness = %v, %v; want %v, %v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestIsShell(t *testing.T) {
	for name, want := range map[string]bool{
		"zsh": true, "-zsh": true, "/bin/bash": true, "fish": true,
		"cmd": true, "cmd.exe": true, "CMD.EXE": true, "powershell.exe": true, "PowerShell.EXE": true, "pwsh": true, "pwsh.exe": true, "bash.exe": true,
		"claude": false, "claude.exe": false, "codex": false, "cmdx": false, "cmd.exe.exe": false, "": false,
	} {
		if got := isShell(name); got != want {
			t.Errorf("isShell(%q) = %v, want %v", name, got, want)
		}
	}
}
