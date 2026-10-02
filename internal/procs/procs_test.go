package procs

import (
	"os"
	"os/exec"
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
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start, err := System.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := System.Comm(pid); c != "sleep" {
		t.Fatalf("Comm = %q, want sleep", c)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if Alive(System, Ref{pid, start}) {
		t.Fatal("killed and reaped process must not be alive")
	}
	if _, err := System.Parent(pid); err != ErrGone {
		t.Fatalf("Parent of gone pid = %v, want ErrGone", err)
	}
}
