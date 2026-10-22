package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

// WaitOptions configures Wait. As defaults to the identity the current
// directory would register as (see Identity). Timeout <= 0 waits forever.
type WaitOptions struct {
	Config     config.Config
	As         string
	Channels   []string
	IncludeOwn bool
	Filter     string // regexp on subject or content; non-matching messages are skipped, except direct messages
	Timeout    time.Duration
	// NoHarnessWatch keeps the wait running after the harness that started it
	// is gone (see ErrHarnessGone).
	NoHarnessWatch bool
	// IdentitySource says how As was chosen: SourceAs, SourceSession or
	// SourceCwd. It is recorded in the wait's pidfile; Wait rejects any other
	// value.
	IdentitySource string
}

// ErrWaitTimeout is returned when no message arrived before Timeout.
var ErrWaitTimeout = fmt.Errorf("timed out waiting for messages")

// ErrWaitReplaced is returned when ctx was canceled before any message
// arrived: a newer wait for the same identity took over (it signals this one),
// or the process was interrupted. Nothing is written to out.
var ErrWaitReplaced = fmt.Errorf("wait replaced")

// Wait blocks until at least one undelivered message is available for the
// identity, writes each as one JSON line to out, and returns. It never
// registers, acks, or advances a cursor: a following MCP receive returns the
// same messages. A direct message (channel dm/*) is printed with its content
// withheld — wait's contract is "wake, then call receive" and its output can
// land in a shell's history or logs, unlike an MCP tool call. Meant for
// `Bash(run_in_background: true)` so an agent is woken once instead of
// polling receive from model turns. Only one wait runs per identity: starting
// a second one signals the first to exit silently (see acquireWaitLock). The
// caller cancels ctx on SIGTERM/SIGINT/SIGHUP, installed before calling so the
// replacement signal is never lost; a canceled ctx returns ErrWaitReplaced.
// A wait for a top-level identity also replaces the other waits that
// top-level identities started from the same harness, because a session's
// identity can change while its harness lives on (see acquireWaitLock).
// A wait also exits with ErrHarnessGone when the harness that started it
// dies (unless NoHarnessWatch). Either way it prints nothing, even when
// messages were already selected: the replacing wait reports them instead,
// or nobody is left to wake.
func Wait(ctx context.Context, o WaitOptions, out io.Writer) error {
	match, err := waitMatch(o.Filter, o.As)
	if err != nil {
		return err
	}
	ctx, done, err := startWaiter(ctx, o.Config.DataDirectory, o.As, o.NoHarnessWatch, o.IdentitySource)
	if err != nil {
		return err
	}
	defer done()
	b, err := bus.Open(o.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	msgs, err := b.Wait(ctx, o.As, o.Channels, o.IncludeOwn, parkedMatch(b, o.As, match), o.Timeout)
	if errors.Is(err, context.Canceled) {
		return waitCanceled(ctx)
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return waitCanceled(ctx) // replaced or harness gone: print nothing
	}
	if len(msgs) == 0 {
		return ErrWaitTimeout
	}
	enc := json.NewEncoder(out)
	for _, m := range msgs {
		if strings.HasPrefix(m.Channel, bus.DMPrefix) {
			m.Content, m.Subject = "", ""
		}
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}

// waitMatch compiles filter into the wake predicate for identity as; nil when
// filter is empty. A direct message is addressed to as by definition, and a
// tag-subscription match is addressed to it too, so both wake the waiter even
// when their text does not match the filter.
func waitMatch(filter, as string) (func(bus.Message) bool, error) {
	if filter == "" {
		return nil, nil
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	inbox := bus.DMChannel(as)
	return func(m bus.Message) bool {
		return m.Channel == inbox || len(m.MatchedTags) > 0 || re.MatchString(m.Subject) || re.MatchString(m.Content)
	}, nil
}

// processTable is the process table the harness lookup and watch use; a var so
// tests can stand in a harness.
var processTable = procs.System

// startWaiter is the setup `wait` and `watch` share: find the harness, watch
// it, and become the one waiter for identity as (acquireWaitLock). It returns
// a context that is canceled when the harness dies (cause ErrHarnessGone), and
// a done func that stops the harness watch and releases the lock. When ctx is
// already canceled or the harness is already gone it returns the error that
// says why (ErrWaitReplaced, ErrHarnessGone).
func startWaiter(ctx context.Context, dataDir, as string, noHarnessWatch bool, source string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancelCause(ctx)
	// The harness is the first non-shell ancestor, not the direct parent:
	// a background shell can outlive it. If it is already gone, exit now.
	// Any other lookup failure (unsupported platform) disables the watch and
	// leaves the harness unrecorded.
	h, herr := procs.FindHarness(processTable, os.Getpid())
	if herr != nil {
		h = procs.Ref{}
	}
	if !noHarnessWatch {
		switch {
		case errors.Is(herr, procs.ErrGone):
			cancel(nil)
			return nil, nil, ErrHarnessGone
		case herr == nil:
			go watchHarness(ctx, processTable, h, cancel)
		}
	}
	if ctx.Err() != nil {
		err := waitCanceled(ctx)
		cancel(nil)
		return nil, nil, err
	}
	release, err := acquireWaitLock(dataDir, as, procs.Self(processTable, os.Getpid()), h, source)
	if err != nil {
		cancel(nil)
		return nil, nil, err
	}
	return ctx, func() { release(); cancel(nil) }, nil
}

// waitCanceled maps a canceled ctx to the error that says why.
func waitCanceled(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrHarnessGone) {
		return ErrHarnessGone
	}
	return ErrWaitReplaced
}
