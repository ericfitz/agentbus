package cli

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func TestWaitWakesOnDirectMessageDespiteFilter(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register("Pat", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("Sam", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "rename landed"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Wait(WaitOptions{Config: cfg, As: "Pat", Filter: "@Pat", Timeout: 2 * time.Second}, &out); err != nil {
		t.Fatalf("a direct message must wake the waiter despite a non-matching filter: %v", err)
	}
	if !strings.Contains(out.String(), "rename landed") {
		t.Fatalf("%q", out.String())
	}
}

func TestWaitFilterStillAppliesToOrdinaryChannels(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register("Pat", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Register("Sam", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateChannel("Sam", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Pat", "dev", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: "dev", Content: "rename landed"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = Wait(WaitOptions{Config: cfg, As: "Pat", Filter: "@Pat", Timeout: 300 * time.Millisecond}, &out)
	if err != ErrWaitTimeout {
		t.Fatalf("non-matching content on an ordinary channel must still time out: %v", err)
	}
}
