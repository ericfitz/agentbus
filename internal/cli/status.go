package cli

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// Status opens the bus, prints a human-readable status report to out, and
// closes it. It never requires or performs a registration.
func Status(cfg config.Config, out io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer b.Close()
	st, err := b.StatusReport()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "config: %s\ndata: %s\nusage: %d MiB of %d MiB budget\n", st.ConfigPath, st.DataDirectory, st.UsageBytes>>20, st.BudgetBytes>>20); err != nil {
		return err
	}
	if st.Notice != "" {
		if _, err := fmt.Fprintf(out, "notice: %s\n", st.Notice); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "embedding backlog: %d\nsessions (%d):\n", st.EmbeddingBacklog, len(st.Sessions)); err != nil {
		return err
	}
	for _, s := range st.Sessions {
		if _, err := fmt.Fprintf(out, "  %s (%s) since %s\n", s.Sender, s.Context, time.UnixMilli(s.RegisteredAt).Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "channels (%d):\n", len(st.Channels)); err != nil {
		return err
	}
	for _, c := range st.Channels {
		if _, err := fmt.Fprintf(out, "  %s [%s] %d messages, latest seq %d\n", c.Name, c.Kind, c.Messages, c.LatestSeq); err != nil {
			return err
		}
	}
	return nil
}
