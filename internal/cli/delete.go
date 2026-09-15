package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/repoconfig"
)

// DeleteChannel permanently deletes a channel and its messages. The bus
// refuses default channels and channels with a live subscriber. Unless yes
// is set it describes what will be lost and asks for a y/N answer on in.
// cwd is checked for a .local/agentbus.json listing the channel, since
// register would recreate it (empty) on the next session.
func DeleteChannel(cfg config.Config, cwd, name string, yes bool, in io.Reader, out io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	if !yes {
		st, err := b.StatusReport()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(st.Channels, func(c bus.Channel) bool { return c.Name == name })
		if i < 0 {
			return fmt.Errorf("channel %q does not exist", name)
		}
		ch := st.Channels[i]
		noun := "message"
		if ch.Kind == "memory" {
			noun = "memory"
		}
		if ch.Messages != 1 {
			noun += "s"
			if ch.Kind == "memory" {
				noun = "memories"
			}
		}
		if _, err := fmt.Fprintf(out, "Channel %q (%s) holds %d %s.\nDeleting it PERMANENTLY destroys them. There is no undo.\n", name, ch.Kind, ch.Messages, noun); err != nil {
			return err
		}
		if f, err := repoconfig.Load(cwd); err == nil {
			if chans, _ := f.Channels(); slices.Contains(chans, name) {
				if _, err := fmt.Fprintf(out, "Note: %s is in this repository's .local/agentbus.json, so register will recreate it empty.\n", name); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintf(out, "Delete channel %q? [y/N] ", name); err != nil {
			return err
		}
		line, readErr := bufio.NewReader(in).ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("delete cancelled")
		}
	}
	ch, err := b.DeleteChannel(name, "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "deleted channel %q (%d messages)\n", ch.Name, ch.Messages)
	return err
}
