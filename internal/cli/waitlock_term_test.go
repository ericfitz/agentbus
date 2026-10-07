package cli

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestTerminateWaitSignalOrExitCode pins how a replaced wait dies: SIGTERM on
// Unix (so main's handler maps it to exit 3), exit code 3 on Windows. A
// SIGKILL here would pass a bare err != nil check but lose the exit code.
func TestTerminateWaitSignalOrExitCode(t *testing.T) {
	cmd := sleeperCommand()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	reaped := false
	t.Cleanup(func() {
		if reaped {
			return
		}
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	if err := terminateWait(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	var err error
	select {
	case err = <-done:
		reaped = true
	case <-time.After(5 * time.Second):
		t.Fatal("child was not terminated")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want ExitError, got %v", err)
	}
	if runtime.GOOS == "windows" {
		if ee.ExitCode() != 3 {
			t.Fatalf("exit code = %d, want 3", ee.ExitCode())
		}
	} else if ee.String() != "signal: terminated" {
		t.Fatalf("child ended with %q, want SIGTERM", ee.String())
	}
}
