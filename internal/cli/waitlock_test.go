package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

func selfRef() procs.Ref { return procs.Self(procs.System, os.Getpid()) }

func writeRef(t *testing.T, dir, as string, r procs.Ref) {
	t.Helper()
	p := waitPidfile(dir, as)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(sprintRef(r)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sprintRef(r procs.Ref) string { return fmt.Sprintf("%d %d\n", r.Pid, r.Start) }

// startSleeper runs a sleeper helper child, killed at cleanup, and returns its
// Ref and a channel closed once it has exited (and been reaped).
func startSleeper(t *testing.T) (procs.Ref, <-chan error) {
	t.Helper()
	cmd := sleeperCommand()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	start, err := procs.System.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return procs.Ref{Pid: cmd.Process.Pid, Start: start}, done
}

func TestAcquireWaitLockReplacesLiveWait(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	writeRef(t, dir, "Pat", old)
	release, err := acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old wait should have been terminated")
		}
		if runtime.GOOS == "windows" {
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 3 {
				t.Fatalf("replaced wait must exit 3 on Windows, got %v", err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old wait was not terminated")
	}
	if got, ok := readPidfile(waitPidfile(dir, "Pat")); !ok || got != selfRef() {
		t.Fatalf("pidfile = %+v, want ours", got)
	}
}

func TestAcquireWaitLockStaleNotSignaled(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	// Stale: the recorded process is gone. Use a reused-pid shape for the live
	// sleeper (start mismatch) and a dead one for the gone case.
	writeRef(t, dir, "Pat", procs.Ref{Pid: old.Pid, Start: old.Start + 1})
	release, err := acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
		t.Fatal("a reused pid (start mismatch) must never be signaled")
	case <-time.After(300 * time.Millisecond):
	}
	// A pid whose process is gone: acquire succeeds.
	cmd := sleeperCommand()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, _ := procs.System.StartTime(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	writeRef(t, dir, "Pat", procs.Ref{Pid: cmd.Process.Pid, Start: start})
	release, err = acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatalf("stale pidfile must not block: %v", err)
	}
	release()
}

func TestAcquireWaitLockGarbagePidfile(t *testing.T) {
	dir := t.TempDir()
	p := waitPidfile(dir, "Pat")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("not a pidfile"), 0o600)
	release, err := acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestReleaseLeavesForeignPidfile(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatal(err)
	}
	other := procs.Ref{Pid: os.Getpid() + 100000, Start: 5}
	writeRef(t, dir, "Pat", other)
	release()
	if got, ok := readPidfile(waitPidfile(dir, "Pat")); !ok || got != other {
		t.Fatalf("release removed or changed a foreign pidfile: %+v", got)
	}
}

func TestReleaseRemovesOwnPidfile(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireWaitLock(dir, "Pat", selfRef())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(waitPidfile(dir, "Pat")); !os.IsNotExist(err) {
		t.Fatalf("own pidfile should be removed, stat err = %v", err)
	}
}

func TestWaitPidfileSanitizesIdentity(t *testing.T) {
	got := waitPidfile("/d", "../a b/c")
	if filepath.Dir(got) != filepath.Join("/d", "wait") {
		t.Fatalf("identity escaped the wait dir: %s", got)
	}
}

func TestWaitCanceledProducesNoOutput(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err := Wait(ctx, WaitOptions{Config: cfg, As: "Pat"}, &out)
	if err != ErrWaitReplaced || out.Len() != 0 {
		t.Fatalf("err = %v, out = %q; want ErrWaitReplaced and no output", err, out.String())
	}
}

// A running wait (no subscriptions would error, so cancel races the poll loop)
// is replaced when the context is canceled mid-wait.
func TestWaitCanceledMidWait(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	registerPat(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	var out bytes.Buffer
	go func() { res <- Wait(ctx, WaitOptions{Config: cfg, As: "Pat"}, &out) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-res:
		if err != ErrWaitReplaced || out.Len() != 0 {
			t.Fatalf("err = %v, out = %q", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not stop on cancel")
	}
	if _, err := os.Stat(waitPidfile(cfg.DataDirectory, "Pat")); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be released, stat err = %v", err)
	}
}

func registerPat(t *testing.T, cfg config.Config) {
	t.Helper()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register("Pat", "", "", false); err != nil {
		t.Fatal(err)
	}
}
