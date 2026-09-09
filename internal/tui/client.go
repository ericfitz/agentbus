package tui

import (
	"context"
	"log/slog"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

// client is the TUI's bus session: one registration, a subscription to every
// channel, and the heartbeat/tick loops the mcp server also runs.
type client struct {
	b          *bus.Bus
	cfg        config.Config
	as         string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         *sync.WaitGroup
	subscribed map[string]bool
}

// newClient opens the bus, registers name (resuming an earlier session of
// the same name so its cursors survive a restart), and subscribes to every
// existing channel from "now".
func newClient(cfg config.Config, name string, log *slog.Logger) (*client, error) {
	b, err := bus.Open(cfg, log)
	if err != nil {
		return nil, err
	}
	reg, err := b.Register(name, "", "tui", true)
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{b: b, cfg: cfg, as: reg.Sender, ctx: ctx, cancel: cancel, subscribed: map[string]bool{}}
	c.wg = mcpserver.StartBackgroundLoops(ctx, b, cfg, log, 10*time.Second)
	chans, err := b.ListChannels(c.as)
	if err != nil {
		_ = c.close()
		return nil, err
	}
	for _, ch := range chans {
		if err := c.subscribe(ch, "now"); err != nil {
			_ = c.close()
			return nil, err
		}
	}
	return c, nil
}

// subscribe is idempotent per channel name.
func (c *client) subscribe(ch bus.Channel, from string) error {
	if c.subscribed[ch.Name] {
		return nil
	}
	if err := c.b.Subscribe(c.as, ch.Name, from); err != nil {
		return err
	}
	c.subscribed[ch.Name] = true
	return nil
}

// receiveLoop long-polls Receive, acking the previous batch each time, and
// hands every non-empty batch to send. It returns once the context is
// cancelled; a Receive in flight at that moment ends on its own when close()
// shuts the database (the bus returns an error, which is ignored after
// cancellation). Own messages are included so the human's sends arrive
// through the same path as everyone else's.
func (c *client) receiveLoop(send func(tea.Msg)) {
	ack := ""
	for c.ctx.Err() == nil {
		res, err := c.b.Receive(c.as, bus.ReceiveInput{
			Ack:         ack,
			Count:       c.cfg.ReceiveMaxCount,
			WaitSeconds: c.cfg.ReceiveMaxWaitSeconds,
			IncludeOwn:  true,
		})
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			send(receiveErrMsg{err})
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		ack = res.Batch
		if len(res.Messages) > 0 || len(res.Gaps) > 0 || len(res.Expired) > 0 || res.Notice != "" {
			send(batchMsg{res})
		}
	}
}

// close stops the background loops, waits for them, then closes the bus:
// the same order as mcpserver.Run, so no loop runs against a closed bus.
func (c *client) close() error {
	c.cancel()
	c.wg.Wait()
	return c.b.Close()
}
