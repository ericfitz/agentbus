package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// WatchOptions configures Watch: WaitOptions without Timeout, because a watch
// has no lifetime of its own (ADR 0021).
type WatchOptions struct {
	Config         config.Config
	As             string
	Channels       []string
	IncludeOwn     bool
	Filter         string
	NoHarnessWatch bool
	IdentitySource string
}

// watchMinGap is the least time between two wake lines. A var so tests do not
// wait seconds.
var watchMinGap = 5 * time.Second

// Watch is a long-lived Wait for Claude Code's Monitor tool (ADR 0021): on
// each wake it writes one line to out, `agentbus: <n> new message(s) for <as>
// on <ch>, <ch>; call receive` (n gets a '+' when the batch hit
// receive_max_count), and keeps watching. It never prints message content,
// registers, acks, or moves a cursor. It keeps one floor, the highest seq
// examined, so a message the agent has not received yet is announced once;
// a later message wakes it again. Lines are at least watchMinGap apart:
// messages arriving in the gap are announced together on the next line.
// Wake rules, the single-watcher lock and the harness watch are Wait's; the
// lock is shared, so a newer watch or wait for the identity replaces this one.
// Watch returns only on error: ErrWaitReplaced when ctx is canceled,
// ErrHarnessGone when the harness died, or the write or bus error. After a
// cancellation it prints nothing more.
func Watch(ctx context.Context, o WatchOptions, out io.Writer) error {
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
	var floor int64
	for {
		msgs, next, err := b.WaitAbove(ctx, o.As, o.Channels, o.IncludeOwn, parkedMatch(b, o.As, match), floor)
		if ctx.Err() != nil {
			return waitCanceled(ctx) // replaced or harness gone: print nothing
		}
		if err != nil {
			return err
		}
		floor = next
		if _, err := fmt.Fprintln(out, watchLine(o.As, msgs, o.Config.ReceiveMaxCount)); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return waitCanceled(ctx)
		case <-time.After(watchMinGap):
		}
	}
}

// watchLine formats the wake line for msgs (never empty).
func watchLine(as string, msgs []bus.Message, maxCount int) string {
	var chans []string
	for _, m := range msgs {
		chans = append(chans, m.Channel)
	}
	slices.Sort(chans)
	chans = slices.Compact(chans)
	more := ""
	if len(msgs) >= maxCount {
		more = "+"
	}
	return fmt.Sprintf("agentbus: %d%s new message(s) for %s on %s; call receive", len(msgs), more, as, strings.Join(chans, ", "))
}
