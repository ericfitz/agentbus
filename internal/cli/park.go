package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// ErrNoIdentity is Park's error when -as was not given and this session has
// not registered, while another session holds the directory's name.
var ErrNoIdentity = errors.New("this session has not registered, and another session holds this repository's name; register first or pass -as")

// parkIdentity is the name `park` and `unpark` act on: as when given, else the
// session this process runs for (SessionIdentity, ADR 0017); "" when there is
// none.
func parkIdentity(b *bus.Bus, as, cwd string, warn io.Writer) string {
	if as != "" {
		return as
	}
	return SessionIdentity(b, cwd, warn)
}

// Park is `agentbus park` (ADR 0025 decision 3): only as's direct messages
// wake it until an unpark. Parking a parked session succeeds. Without as it
// parks the session this process runs for, or fails with ErrNoIdentity.
func Park(cfg config.Config, as, cwd string, out, warn io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	name := parkIdentity(b, as, cwd, warn)
	if name == "" {
		return ErrNoIdentity
	}
	if err := b.Park(name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "parked %s: only direct messages wake it until it is unparked\n", name)
	return err
}

// Unpark is `agentbus unpark`, run by the UserPromptSubmit hook. It prints
// nothing and reports nothing: any failure (no bus, no identity, a name that
// cannot be parked) leaves the prompt untouched. Unparking an unparked
// session is a no-op.
func Unpark(cfg config.Config, as, cwd string, warn io.Writer) {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return
	}
	defer func() { _ = b.Close() }()
	if name := parkIdentity(b, as, cwd, warn); name != "" {
		_ = b.Unpark(name)
	}
}

// parkedMatch wraps match, the wake predicate, for a session that may be
// parked (ADR 0025): while b.Parked(as) a message must be on dm/<as> and also
// pass match (nil accepts everything). The park file is read per message, so
// a running wait or watch sees park and unpark as they happen.
func parkedMatch(b *bus.Bus, as string, match func(bus.Message) bool) func(bus.Message) bool {
	inbox := bus.DMChannel(as)
	return func(m bus.Message) bool {
		if b.Parked(as) && m.Channel != inbox {
			return false
		}
		return match == nil || match(m)
	}
}
