package cli

import (
	"io"
	"log/slog"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// peerBus is a second process on cfg's database with name registered. One
// process holds one top-level name (ADR 0025 decision 6), so a test that
// needs a sender besides the watcher registers it here.
func peerBus(t *testing.T, cfg config.Config, name string) *bus.Bus {
	t.Helper()
	p, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if _, err := p.Register(name, "", "", false); err != nil {
		t.Fatal(err)
	}
	return p
}
