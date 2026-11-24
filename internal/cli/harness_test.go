package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/procs"
)

func TestWatchHarnessCancelsWhenHarnessDies(t *testing.T) {
	old := harnessPollInterval
	harnessPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { harnessPollInterval = old })

	h, done := startSleeper(t) // stands in for the harness
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go watchHarness(ctx, procs.System, h, cancel)

	select {
	case <-ctx.Done():
		t.Fatal("canceled while the harness is alive")
	case <-time.After(100 * time.Millisecond):
	}
	if err := killRef(h); err != nil {
		t.Fatal(err)
	}
	<-done
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not notice the harness exit")
	}
	if !errors.Is(context.Cause(ctx), ErrHarnessGone) {
		t.Fatalf("cause = %v, want ErrHarnessGone", context.Cause(ctx))
	}
}

func TestWatchHarnessStopsWhenCtxDone(t *testing.T) {
	old := harnessPollInterval
	harnessPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { harnessPollInterval = old })
	ctx, cancel := context.WithCancelCause(context.Background())
	exited := make(chan struct{})
	go func() { watchHarness(ctx, procs.System, selfRef(), cancel); close(exited) }()
	cancel(nil)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher outlived its ctx")
	}
}

func killRef(r procs.Ref) error {
	p, err := os.FindProcess(r.Pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// flakyTable fails every StartTime lookup with an error other than ErrGone
// until gone is set, then reports the process gone.
type flakyTable struct{ gone chan struct{} }

func (f flakyTable) Parent(int) (int, error)  { return 0, errors.New("unused") }
func (f flakyTable) Comm(int) (string, error) { return "", errors.New("unused") }
func (f flakyTable) StartTime(int) (int64, error) {
	select {
	case <-f.gone:
		return 0, procs.ErrGone
	default:
		return 0, errors.New("transient lookup failure")
	}
}

// A lookup error is not proof the harness exited: the watcher keeps waiting
// and still notices a later, explicit exit.
func TestWatchHarnessIgnoresLookupErrors(t *testing.T) {
	old := harnessPollInterval
	harnessPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { harnessPollInterval = old })
	tbl := flakyTable{gone: make(chan struct{})}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go watchHarness(ctx, tbl, procs.Ref{Pid: 4242, Start: 7}, cancel)
	select {
	case <-ctx.Done():
		t.Fatalf("canceled on a lookup error: %v", context.Cause(ctx))
	case <-time.After(100 * time.Millisecond):
	}
	close(tbl.gone)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not notice the harness exit")
	}
	if !errors.Is(context.Cause(ctx), ErrHarnessGone) {
		t.Fatalf("cause = %v, want ErrHarnessGone", context.Cause(ctx))
	}
}
