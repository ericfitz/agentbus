package tui

import (
	"io"
	"log/slog"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cfg.Path = cfg.DataDirectory + "/config.json"
	cfg.ReceiveMaxWaitSeconds = 1
	return cfg
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// agent registers a second identity on the same database, as an agent
// process would, so tests can post messages the TUI must see.
func agent(t *testing.T, cfg config.Config, name string) (*bus.Bus, string) {
	t.Helper()
	b, err := bus.Open(cfg, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	r, err := b.Register(name, "", "test", false)
	if err != nil {
		t.Fatal(err)
	}
	return b, r.Sender
}

func TestNewClientRegistersAndSubscribesToEveryChannel(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	for _, ch := range []string{"dev", "notes"} {
		kind := "ordinary"
		if ch == "notes" {
			kind = "memory"
		}
		if _, err := ab.CreateChannel(sam, ch, kind); err != nil {
			t.Fatal(err)
		}
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.close() }()
	if c.as != "eric" {
		t.Fatalf("as = %q", c.as)
	}
	if !c.subscribed["dev"] || !c.subscribed["notes"] {
		t.Fatalf("subscribed = %v", c.subscribed)
	}
	live, err := c.b.LiveSessionCount()
	if err != nil || live != 2 {
		t.Fatalf("live sessions = %d, %v", live, err)
	}
}

func TestReceiveLoopDeliversBatchesAndStopsOnClose(t *testing.T) {
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan tea.Msg, 16)
	done := make(chan struct{})
	go func() { c.receiveLoop(func(m tea.Msg) { got <- m }); close(done) }()
	if _, err := ab.Send(sam, bus.SendInput{Channel: "dev", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		b, ok := m.(batchMsg)
		if !ok || len(b.res.Messages) != 1 || b.res.Messages[0].Content != "hello" {
			t.Fatalf("got %#v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no batch delivered")
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receive loop did not stop after close")
	}
}
