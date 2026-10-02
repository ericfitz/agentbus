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
// replacement signal is never lost; a wait also exits with ErrHarnessGone, silently, when the harness that started it dies (unless NoHarnessWatch); a canceled ctx returns ErrWaitReplaced
// unless messages were already selected, which are still printed.
func Wait(ctx context.Context, o WaitOptions, out io.Writer) error {
	var match func(bus.Message) bool
	if o.Filter != "" {
		re, err := regexp.Compile(o.Filter)
		if err != nil {
			return fmt.Errorf("filter: %w", err)
		}
		inbox := bus.DMChannel(o.As)
		// A direct message is addressed to this identity by definition, and a
		// tag-subscription match is addressed to this identity too, so both
		// wake the waiter even when its text does not match the filter.
		match = func(m bus.Message) bool {
			return m.Channel == inbox || len(m.MatchedTags) > 0 || re.MatchString(m.Subject) || re.MatchString(m.Content)
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if !o.NoHarnessWatch {
		// The harness is the first non-shell ancestor, not the direct parent:
		// a background shell can outlive it. If it is already gone, exit now.
		// Any other lookup failure (unsupported platform) disables the watch.
		switch h, err := procs.FindHarness(procs.System, os.Getpid()); {
		case errors.Is(err, procs.ErrGone):
			return ErrHarnessGone
		case err == nil:
			go watchHarness(ctx, procs.System, h, cancel)
		}
	}
	if ctx.Err() != nil {
		return waitCanceled(ctx)
	}
	release, err := acquireWaitLock(o.Config.DataDirectory, o.As, procs.Self(procs.System, os.Getpid()))
	if err != nil {
		return err
	}
	defer release()
	b, err := bus.Open(o.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	msgs, err := b.Wait(ctx, o.As, o.Channels, o.IncludeOwn, match, o.Timeout)
	if errors.Is(err, context.Canceled) {
		return waitCanceled(ctx)
	}
	if err != nil {
		return err
	}
	if errors.Is(context.Cause(ctx), ErrHarnessGone) {
		return ErrHarnessGone // nobody is left to wake: print nothing
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

// waitCanceled maps a canceled ctx to the error that says why.
func waitCanceled(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrHarnessGone) {
		return ErrHarnessGone
	}
	return ErrWaitReplaced
}
