package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

// parkEnv is Pat (the session under test, subscribed to dev) and Sam (a peer
// that sends).
func parkEnv(t *testing.T) (config.Config, *bus.Bus, *bus.Bus) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if _, err := b.Register("Pat", "", "", false); err != nil {
		t.Fatal(err)
	}
	sam := peerBus(t, cfg, "Sam")
	if _, err := sam.CreateChannel("Sam", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("Pat", "dev", "now"); err != nil {
		t.Fatal(err)
	}
	return cfg, b, sam
}

func TestParkAndUnparkCommands(t *testing.T) {
	cfg, b, _ := parkEnv(t)
	cwd := t.TempDir()
	var out bytes.Buffer
	for i := 0; i < 2; i++ { // second run is a no-op that still succeeds
		out.Reset()
		if err := Park(cfg, "Pat", cwd, &out, io.Discard); err != nil {
			t.Fatalf("park #%d: %v", i+1, err)
		}
		if !b.Parked("Pat") {
			t.Fatal("Pat must be parked")
		}
	}
	for i := 0; i < 2; i++ {
		Unpark(cfg, "Pat", cwd, io.Discard)
		if b.Parked("Pat") {
			t.Fatalf("unpark #%d: Pat must not be parked", i+1)
		}
	}
}

func TestParkRejectsUnparkableName(t *testing.T) {
	cfg, _, _ := parkEnv(t)
	if err := Park(cfg, "a/b", t.TempDir(), io.Discard, io.Discard); err == nil {
		t.Fatal("a name with '/' must be rejected")
	}
	Unpark(cfg, "a/b", t.TempDir(), io.Discard) // must not panic or print
}

func TestParkWithoutIdentityFails(t *testing.T) {
	cfg, cwd, first, _, b := twoSessions(t)
	setHarness(t, procs.Ref{Pid: 300, Start: 3}, nil) // a harness that registered nothing
	err := Park(cfg, "", cwd, io.Discard, io.Discard)
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("want ErrNoIdentity, got %v", err)
	}
	Unpark(cfg, "", cwd, io.Discard) // silent no-op
	if b.Parked(first) {
		t.Fatal("nothing may be parked")
	}
}

func TestParkAndUnparkResolveSessionFromHarness(t *testing.T) {
	cfg, cwd, first, second, b := twoSessions(t)
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	if err := Park(cfg, "", cwd, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !b.Parked(second) || b.Parked(first) {
		t.Fatalf("want only %q parked", second)
	}
	// A leftover park file does not matter: unpark just removes it.
	if err := b.Park(first); err != nil {
		t.Fatal(err)
	}
	Unpark(cfg, "", cwd, io.Discard)
	if b.Parked(second) || !b.Parked(first) {
		t.Fatal("unpark must clear only this harness's session")
	}
}

func TestUnparkCommandSilentOnBrokenBus(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(cfg.DataDirectory, []byte("x"), 0o600); err != nil { // a file where the directory should be
		t.Fatal(err)
	}
	Unpark(cfg, "Pat", t.TempDir(), io.Discard) // must return, never panic
}

func TestParkedWaitIgnoresChannelWakesOnDM(t *testing.T) {
	cfg, b, sam := parkEnv(t)
	fastWatch(t)
	if err := b.Park("Pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: "dev", Content: "chatter"}); err != nil {
		t.Fatal(err)
	}
	o := WaitOptions{Config: cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true, Timeout: 300 * time.Millisecond}
	var out bytes.Buffer
	if err := Wait(context.Background(), o, &out); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("a parked wait must ignore a channel message: err=%v out=%q", err, out.String())
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "psst"}); err != nil {
		t.Fatal(err)
	}
	o.Timeout = 2 * time.Second
	out.Reset()
	if err := Wait(context.Background(), o, &out); err != nil {
		t.Fatalf("a parked wait must wake on a DM: %v", err)
	}
	if !strings.Contains(out.String(), `"channel":"dm/Pat"`) {
		t.Fatalf("want the DM: %q", out.String())
	}
}

func TestParkedWaitWithFilterStillRequiresDM(t *testing.T) {
	cfg, b, sam := parkEnv(t)
	fastWatch(t)
	if err := b.Park("Pat"); err != nil {
		t.Fatal(err)
	}
	// Matches the filter but is not a DM: ignored while parked.
	if _, err := sam.Send("Sam", bus.SendInput{Channel: "dev", Content: "hey @Pat"}); err != nil {
		t.Fatal(err)
	}
	o := WaitOptions{Config: cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true, Filter: "@Pat", Timeout: 300 * time.Millisecond}
	if err := Wait(context.Background(), o, io.Discard); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("filter-matching channel message must not wake a parked wait: %v", err)
	}
}

func TestRunningWaitSeesParkAndUnpark(t *testing.T) {
	cfg, b, sam := parkEnv(t)
	fastWatch(t)
	o := WaitOptions{Config: cfg, As: "Pat", IdentitySource: SourceAs, NoHarnessWatch: true, Timeout: 5 * time.Second}
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- Wait(context.Background(), o, &out) }()
	time.Sleep(50 * time.Millisecond)
	if err := b.Park("Pat"); err != nil { // parked while the wait is running
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := sam.Send("Sam", bus.SendInput{Channel: "dev", Content: "chatter"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("the running wait woke for a channel message while parked: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "psst"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the DM must wake the parked wait")
	}
}

func TestParkedWatchIgnoresChannelAnnouncesDM(t *testing.T) {
	e := newWatchEnv(t, nil)
	if err := e.b.Park("Pat"); err != nil {
		t.Fatal(err)
	}
	e.start(WatchOptions{})
	e.send("chatter")
	e.quiet(0, 150*time.Millisecond)
	if _, err := e.sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel("Pat"), Content: "psst"}); err != nil {
		t.Fatal(err)
	}
	lines := e.waitLines(1)
	if !strings.Contains(lines[0], "dm/Pat") || strings.Contains(lines[0], "dev") {
		t.Fatalf("want a line for the DM only: %q", lines[0])
	}
}

func TestStopHookParkedKeepsOnlyDMs(t *testing.T) {
	cfg := config.Default()
	cfg.DataDirectory = t.TempDir()
	cwd := t.TempDir()
	me := filepath.Base(cwd)
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Register(me, "", "", false); err != nil {
		t.Fatal(err)
	}
	sam := peerBus(t, cfg, "Sam")
	if _, err := sam.CreateChannel("Sam", "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(me, "dev", "now"); err != nil {
		t.Fatal(err)
	}
	hook := func() string {
		body, _ := json.Marshal(map[string]any{"cwd": cwd})
		var out bytes.Buffer
		StopHook(cfg, bytes.NewReader(body), &out, io.Discard)
		return out.String()
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: "dev", Content: "chatter"}); err != nil {
		t.Fatal(err)
	}
	if got := hook(); got == "" {
		t.Fatal("unparked, a channel message must block the stop")
	}
	if err := b.Park(me); err != nil {
		t.Fatal(err)
	}
	if got := hook(); got != "" {
		t.Fatalf("parked: a channel message must not block: %q", got)
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(me), Content: "psst"}); err != nil {
		t.Fatal(err)
	}
	got := hook()
	if !strings.Contains(got, "dm/"+me) || strings.Contains(got, "dev") {
		t.Fatalf("parked: want a block naming only the DM: %q", got)
	}
}
