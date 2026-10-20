//go:build !windows

package cli

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/procs"
)

// The replacement signal path end to end, as main wires it: a newer wait or
// watch for the identity (both go through acquireWaitLock) SIGTERMs this
// watch, whose handler cancels ctx.
func TestWatchReplacedBySignalFromNewerAcquire(t *testing.T) {
	e := newWatchEnv(t, nil)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	e.done = make(chan error, 1)
	go func() {
		e.done <- Watch(ctx, WatchOptions{Config: e.cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true}, e.out)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, ok := readPidfile(waitPidfile(e.cfg.DataDirectory, "Pat")); ok && rec.Self == selfRef() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watch never recorded itself")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The newer waiter is another process: a sleeper stands in for it.
	newer, _ := startSleeper(t)
	release, err := acquireWaitLock(e.cfg.DataDirectory, "Pat", newer, procs.Ref{}, SourceAs)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case err := <-e.done:
		if !errors.Is(err, ErrWaitReplaced) {
			t.Fatalf("err = %v, want ErrWaitReplaced", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch was not replaced")
	}
	if e.out.String() != "" {
		t.Fatalf("printed after replacement: %q", e.out.String())
	}
}
