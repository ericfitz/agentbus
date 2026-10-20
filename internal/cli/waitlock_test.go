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
	"strings"
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
	release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
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
	if got, ok := readPidfile(waitPidfile(dir, "Pat")); !ok || got.Self != selfRef() {
		t.Fatalf("pidfile = %+v, want ours", got)
	}
}

func TestAcquireWaitLockStaleNotSignaled(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	// Stale: the recorded process is gone. Use a reused-pid shape for the live
	// sleeper (start mismatch) and a dead one for the gone case.
	writeRef(t, dir, "Pat", procs.Ref{Pid: old.Pid, Start: old.Start + 1})
	release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
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
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	start, _ := procs.System.StartTime(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	writeRef(t, dir, "Pat", procs.Ref{Pid: cmd.Process.Pid, Start: start})
	release, err = acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
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
	release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestReleaseLeavesForeignPidfile(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
	if err != nil {
		t.Fatal(err)
	}
	other := procs.Ref{Pid: os.Getpid() + 100000, Start: 5}
	writeRef(t, dir, "Pat", other)
	release()
	if got, ok := readPidfile(waitPidfile(dir, "Pat")); !ok || got.Self != other {
		t.Fatalf("release removed or changed a foreign pidfile: %+v", got)
	}
}

func TestReleaseRemovesOwnPidfile(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, SourceAs)
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
	err := Wait(ctx, WaitOptions{Config: cfg, As: "Pat", IdentitySource: SourceAs}, &out)
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
	go func() { res <- Wait(ctx, WaitOptions{Config: cfg, As: "Pat", IdentitySource: SourceAs}, &out) }()
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

func harnessRef(pid int) procs.Ref { return procs.Ref{Pid: pid, Start: int64(pid) * 10} }

// writeRecord writes a new-form pidfile for identity as.
func writeRecord(t *testing.T, dir, as string, self, harness procs.Ref, source string) {
	t.Helper()
	p := waitPidfile(dir, as)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d %d %d %d %s\n", self.Pid, self.Start, harness.Pid, harness.Start, source)
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exited(done <-chan error, wait time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(wait):
		return false
	}
}

// #39: the session's identity changed (agentbus2 -> agentbus) while its
// harness lived on; the wait under the new name replaces the old one.
func TestAcquireWaitLockReplacesSameHarnessOtherIdentity(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	h := harnessRef(4242)
	writeRecord(t, dir, "agentbus2", old, h, SourceSession)
	release, err := acquireWaitLock(dir, "agentbus", selfRef(), h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !exited(done, 5*time.Second) {
		t.Fatal("same-harness wait under another identity was not terminated")
	}
}

func TestAcquireWaitLockKeepsOtherHarnessWait(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	writeRecord(t, dir, "agentbus2", old, harnessRef(1), SourceSession)
	release, err := acquireWaitLock(dir, "agentbus", selfRef(), harnessRef(2), SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if exited(done, time.Second) {
		t.Fatal("a wait from another harness must not be terminated")
	}
	// Same harness pid but a different start time is a reused pid: another harness.
	writeRecord(t, dir, "agentbus2", old, procs.Ref{Pid: 2, Start: 99}, SourceSession)
	release2, err := acquireWaitLock(dir, "agentbus3", selfRef(), harnessRef(2), SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if exited(done, time.Second) {
		t.Fatal("a harness with a mismatched start time must not match")
	}
}

func TestAcquireWaitLockHarnessScanNeverSignalsReusedWaitPid(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	h := harnessRef(7)
	writeRecord(t, dir, "agentbus2", procs.Ref{Pid: old.Pid, Start: old.Start + 1}, h, SourceSession)
	release, err := acquireWaitLock(dir, "agentbus", selfRef(), h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if exited(done, time.Second) {
		t.Fatal("a reused wait pid (start mismatch) must never be signaled")
	}
}

func TestAcquireWaitLockSubagentDoesNotScanOrRecordHarness(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	h := harnessRef(4242)
	writeRecord(t, dir, "agentbus", old, h, SourceSession)
	release, err := acquireWaitLock(dir, "agentbus/sub", selfRef(), h, SourceAs)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if exited(done, time.Second) {
		t.Fatal("a subagent wait must not terminate its parent harness's top-level wait")
	}
	rec, ok := readPidfile(waitPidfile(dir, "agentbus/sub"))
	if !ok || rec.Harness != (procs.Ref{}) || rec.Self != selfRef() || rec.Source != SourceAs {
		t.Fatalf("subagent record = %+v, want harness 0 0", rec)
	}
	// A top-level wait from the same harness kills the top-level sleeper but
	// must not find the subagent's wait, which recorded no harness.
	other := procs.Ref{Pid: 1, Start: 1}
	release2, err := acquireWaitLock(dir, "other", other, h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if !exited(done, 5*time.Second) {
		t.Fatal("the top-level sleeper should be terminated by the top-level acquire")
	}
	if rec, ok := readPidfile(waitPidfile(dir, "agentbus/sub")); !ok || rec.Self != selfRef() || rec.Harness != (procs.Ref{}) {
		t.Fatalf("subagent record changed: %+v", rec)
	}
	if !procs.Alive(procs.System, selfRef()) {
		t.Fatal("the subagent wait must still be alive")
	}
}

// A wait terminated by the harness scan leaves no pidfile behind (Windows'
// TerminateProcess skips its release).
func TestAcquireWaitLockScanRemovesTerminatedPidfile(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	h := harnessRef(4242)
	writeRecord(t, dir, "agentbus2", old, h, SourceSession)
	release, err := acquireWaitLock(dir, "agentbus", selfRef(), h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !exited(done, 5*time.Second) {
		t.Fatal("the other wait was not terminated")
	}
	if _, err := os.Stat(waitPidfile(dir, "agentbus2")); !os.IsNotExist(err) {
		t.Fatalf("terminated wait's pidfile should be removed, stat err = %v", err)
	}
}

func TestAcquireWaitLockOldFormOtherIdentityIgnoredButReplacedByName(t *testing.T) {
	dir := t.TempDir()
	old, done := startSleeper(t)
	writeRef(t, dir, "agentbus2", old) // old two-field form
	release, err := acquireWaitLock(dir, "agentbus", selfRef(), harnessRef(4242), SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	if exited(done, time.Second) {
		t.Fatal("an old-form record has no harness and must be ignored by the scan")
	}
	release()
	release, err = acquireWaitLock(dir, "agentbus2", selfRef(), harnessRef(4242), SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !exited(done, 5*time.Second) {
		t.Fatal("same-identity acquire must still replace an old-form wait")
	}
}

func TestReadPidfileForms(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.pid")
	cases := []struct {
		content string
		ok      bool
		want    waitRecord
	}{
		{"10 20\n", true, waitRecord{Self: procs.Ref{Pid: 10, Start: 20}}},
		{"10 20 30 40 session\n", true, waitRecord{procs.Ref{Pid: 10, Start: 20}, procs.Ref{Pid: 30, Start: 40}, "session"}},
		{"10 20 30 40 weird\n", true, waitRecord{procs.Ref{Pid: 10, Start: 20}, procs.Ref{Pid: 30, Start: 40}, "weird"}},
		{"10 20 30\n", false, waitRecord{}},
		{"10 20 30 40\n", false, waitRecord{}},
		{"10 20 30 40 as extra\n", false, waitRecord{}},
		{"10 20 x 40 as\n", true, waitRecord{Self: procs.Ref{Pid: 10, Start: 20}}},
		{"a b\n", false, waitRecord{}},
		{"", false, waitRecord{}},
	}
	for _, c := range cases {
		if err := os.WriteFile(p, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, ok := readPidfile(p)
		if ok != c.ok || got != c.want {
			t.Errorf("readPidfile(%q) = %+v, %v; want %+v, %v", c.content, got, ok, c.want, c.ok)
		}
	}
}

func TestAcquireWaitLockRecordsSource(t *testing.T) {
	for _, src := range []string{SourceAs, SourceSession, SourceCwd} {
		dir := t.TempDir()
		h := harnessRef(9)
		release, err := acquireWaitLock(dir, "Pat", selfRef(), h, src)
		if err != nil {
			t.Fatal(err)
		}
		rec, ok := readPidfile(waitPidfile(dir, "Pat"))
		if !ok || rec.Source != src || rec.Harness != h || rec.Self != selfRef() {
			t.Fatalf("source %s: record = %+v", src, rec)
		}
		release()
	}
}

func TestAcquireWaitLockRejectsUnknownSource(t *testing.T) {
	for _, src := range []string{"", "AS", "env", "as session"} {
		dir := t.TempDir()
		if release, err := acquireWaitLock(dir, "Pat", selfRef(), procs.Ref{}, src); err == nil {
			release()
			t.Fatalf("source %q must be rejected", src)
		}
		if _, err := os.Stat(waitPidfile(dir, "Pat")); !os.IsNotExist(err) {
			t.Fatalf("source %q: a pidfile was written, stat err = %v", src, err)
		}
	}
}

func TestWaitRejectsUnknownIdentitySource(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	var out bytes.Buffer
	if err := Wait(context.Background(), WaitOptions{Config: cfg, As: "Pat"}, &out); err == nil || !strings.Contains(err.Error(), "identity source") {
		t.Fatalf("Wait without a known IdentitySource: err = %v, want an identity source error", err)
	}
}

// Two acquires from one harness in sequence: only the newest survives, and the
// terminated one's release leaves the newer pidfile alone.
func TestAcquireWaitLockNewestWinsAndReleaseKeepsNewer(t *testing.T) {
	dir := t.TempDir()
	first, done := startSleeper(t)
	h := harnessRef(4242)
	rel1, err := acquireWaitLock(dir, "agentbus2", first, h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := acquireWaitLock(dir, "agentbus", selfRef(), h, SourceSession)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if !exited(done, 5*time.Second) {
		t.Fatal("the older wait was not terminated")
	}
	rel1() // what the terminated wait's deferred release does
	if rec, ok := readPidfile(waitPidfile(dir, "agentbus")); !ok || rec.Self != selfRef() {
		t.Fatalf("newer pidfile lost: %+v", rec)
	}
	// The older wait's own file is left for it to remove only if it names it.
	if rec, ok := readPidfile(waitPidfile(dir, "agentbus2")); ok {
		t.Fatalf("older release should remove its own pidfile, got %+v", rec)
	}
}
