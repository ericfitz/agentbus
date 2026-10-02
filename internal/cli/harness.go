package cli

import (
	"context"
	"errors"
	"time"

	"github.com/ericfitz/agentbus/internal/procs"
)

// ErrHarnessGone is returned by Wait, as the cancel cause, when the harness
// that started the wait has exited (ADR 0014). Nothing is written to out.
var ErrHarnessGone = errors.New("harness gone")

// harnessPollInterval is how often watchHarness checks the harness. A var so
// tests do not wait seconds.
var harnessPollInterval = 2 * time.Second

// watchHarness cancels with ErrHarnessGone once the process h is no longer
// alive (gone, or its pid reused). It returns when ctx is done.
func watchHarness(ctx context.Context, t procs.Table, h procs.Ref, cancel context.CancelCauseFunc) {
	tick := time.NewTicker(harnessPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !procs.Alive(t, h) {
				cancel(ErrHarnessGone)
				return
			}
		}
	}
}
