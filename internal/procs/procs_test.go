package procs

import (
	"os"
	"strings"
	"testing"
)

func TestSystemSelf(t *testing.T) {
	pid := os.Getpid()
	ppid, err := System.Parent(pid)
	if err != nil || ppid != os.Getppid() {
		t.Fatalf("Parent(self) = %d, %v; want %d", ppid, err, os.Getppid())
	}
	if c, err := System.Comm(pid); err != nil || c == "" {
		t.Fatalf("Comm(self) = %q, %v", c, err)
	}
	start, err := System.StartTime(pid)
	if err != nil || start == 0 {
		t.Fatalf("StartTime(self) = %d, %v", start, err)
	}
	if !Alive(System, Ref{pid, start}) {
		t.Fatal("self must be alive with its own start time")
	}
	if Alive(System, Ref{pid, start + 1}) {
		t.Fatal("a different start time is a reused pid, not alive")
	}
	if !Alive(System, Ref{Pid: pid}) {
		t.Fatal("start 0 matches any incarnation")
	}
}

func TestSystemGone(t *testing.T) {
	cmd := sleeperCommand()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	pid := cmd.Process.Pid
	start, err := System.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := System.Comm(pid); strings.TrimSuffix(c, ".exe") != helperComm() {
		t.Fatalf("Comm = %q, want %s", c, helperComm())
	}
	if ppid, err := System.Parent(pid); err != nil || ppid != os.Getpid() {
		t.Fatalf("Parent(child) = %d, %v; want %d", ppid, err, os.Getpid())
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if Alive(System, Ref{pid, start}) {
		t.Fatal("killed and reaped process must not be alive")
	}
	if _, err := System.Parent(pid); err != ErrGone {
		t.Fatalf("Parent of gone pid = %v, want ErrGone", err)
	}
	if _, err := System.StartTime(pid); err != ErrGone {
		t.Fatalf("StartTime of gone pid = %v, want ErrGone", err)
	}
}
