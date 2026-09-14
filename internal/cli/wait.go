package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// WaitOptions configures Wait. As defaults to the identity the current
// directory would register as (see Identity). Timeout <= 0 waits forever.
type WaitOptions struct {
	Config     config.Config
	As         string
	Channels   []string
	IncludeOwn bool
	Filter     string // regexp on content; non-matching messages are skipped
	Timeout    time.Duration
}

// ErrWaitTimeout is returned when no message arrived before Timeout.
var ErrWaitTimeout = fmt.Errorf("timed out waiting for messages")

// Wait blocks until at least one undelivered message is available for the
// identity, writes each as one JSON line to out, and returns. It never
// registers, acks, or advances a cursor: a following MCP receive returns the
// same messages. Meant for `Bash(run_in_background: true)` so an agent is
// woken once instead of polling receive from model turns.
func Wait(o WaitOptions, out io.Writer) error {
	var match func(bus.Message) bool
	if o.Filter != "" {
		re, err := regexp.Compile(o.Filter)
		if err != nil {
			return fmt.Errorf("filter: %w", err)
		}
		match = func(m bus.Message) bool { return re.MatchString(m.Content) }
	}
	b, err := bus.Open(o.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	msgs, err := b.Wait(o.As, o.Channels, o.IncludeOwn, match, o.Timeout)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return ErrWaitTimeout
	}
	enc := json.NewEncoder(out)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}
