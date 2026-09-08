package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/ericfitz/agentbus-local/internal/bus"
	"github.com/ericfitz/agentbus-local/internal/config"
)

// Reset warns about live sessions, asks for confirmation on in, and (only on
// an exact "yes" line) wipes every table in one transaction. It never
// unlinks the database file and never touches running processes directly:
// a live process notices on its own, since its session row is gone, and its
// next call fails auth with not_registered.
func Reset(cfg config.Config, in io.Reader, out io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer b.Close()
	live, err := b.LiveSessionCount()
	if err != nil {
		return err
	}
	if live > 0 {
		if _, err := fmt.Fprintf(out, "warning: %d live session(s) will lose their registration and must register again\n", live); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "This deletes ALL Agentbus data in %s: messages, memories, channels, identities, cursors. Configuration is kept.\nType yes to continue: ", cfg.DataDirectory); err != nil {
		return err
	}
	line, readErr := bufio.NewReader(in).ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	if strings.TrimSpace(line) != "yes" {
		return errors.New("reset cancelled")
	}
	if err := b.Reset(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "reset complete")
	return err
}
