package cli

import (
	"bytes"
	"encoding/json"
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
	if strings.Contains(out.String(), "rename landed") {
		t.Fatalf("a direct message's content must not be printed: %q", out.String())
	}
	if !strings.Contains(out.String(), `"channel":"dm/Pat"`) {
		t.Fatalf("channel must still be printed: %q", out.String())
	}
}

// TestWaitWithholdsDirectMessageContentFromJSON reproduces F2: wait's JSON
// output is easy to leave in a shell's scrollback or a background-job log,
// unlike an MCP tool call, so a direct message's content must not be
// printed. Other fields (seq, channel, sender, created_at) are unaffected.
func TestWaitWithholdsDirectMessageContentFromJSON(t *testing.T) {
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
	if _, err := b.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "secret payload"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Wait(WaitOptions{Config: cfg, As: "Pat", Timeout: 2 * time.Second}, &out); err != nil {
		t.Fatal(err)
	}
	var got bus.Message
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output must still be valid JSON: %v (%q)", err, out.String())
	}
	if got.Content != "" {
		t.Fatalf("content must be withheld, got %q", got.Content)
	}
	if got.Channel != "dm/Pat" || got.Sender != "Sam" || got.Seq == 0 {
		t.Fatalf("other fields must be unaffected: %+v", got)
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

func TestWaitWakesOnTagMatchDespiteFilter(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	for _, n := range []string{"Pat", "Sam"} {
		if _, err := b.Register(n, "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SubscribeTags("Pat", []string{"release"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: "general", Content: "cut v2", Tags: []string{"release"}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Wait(WaitOptions{Config: cfg, As: "Pat", Filter: "@Pat", Timeout: 2 * time.Second}, &out); err != nil {
		t.Fatalf("a tag match must wake the waiter: %v", err)
	}
	if !strings.Contains(out.String(), `"matched_tags":["release"]`) {
		t.Fatalf("%q", out.String())
	}
}
