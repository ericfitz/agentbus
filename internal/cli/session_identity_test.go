package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/procs"
)

// twoSessions registers the cwd identity from harness A and, from harness
// B, a second session that gets the suffixed name (ADR 0017's case: two
// sessions in one repository). It returns the config, cwd, both names, and
// the first bus for sending.
func twoSessions(t *testing.T) (cfg config.Config, cwd, first, second string, b *bus.Bus) {
	t.Helper()
	cfg = config.Default()
	cfg.DataDirectory = t.TempDir()
	cwd = t.TempDir() // no .git: identity is the directory's basename
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var err error
	if b, err = bus.Open(cfg, log); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	other, err := bus.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	b.SetHarness(procs.Ref{Pid: 100, Start: 1})
	other.SetHarness(procs.Ref{Pid: 200, Start: 2})
	r1, err := b.Register(filepath.Base(cwd), "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := other.Register(filepath.Base(cwd), "", "", false)
	if err != nil || r2.Sender == r1.Sender {
		t.Fatalf("second session: %+v %v", r2, err)
	}
	return cfg, cwd, r1.Sender, r2.Sender, b
}

// setHarness makes this process's harness lookup return ref (or fail).
func setHarness(t *testing.T, ref procs.Ref, err error) {
	t.Helper()
	prev := findHarness
	findHarness = func() (procs.Ref, error) { return ref, err }
	t.Cleanup(func() { findHarness = prev })
}

// The Stop hook checks the session registered from its own harness, not
// the cwd identity: the second session is not woken for the first one's
// messages, and is woken (and told to wait as itself) for its own. With no
// match it falls back to the cwd identity.
func TestStopHookUsesSessionFromItsHarness(t *testing.T) {
	cfg, cwd, first, second, _ := twoSessions(t)
	hook := func() string {
		var out bytes.Buffer
		StopHook(cfg, strings.NewReader(`{"cwd":`+strconvQuote(cwd)+`}`), &out, io.Discard)
		return out.String()
	}
	sam := peerBus(t, cfg, "Sam")
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(first), Content: "for first"}); err != nil {
		t.Fatal(err)
	}
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	if got := hook(); got != "" {
		t.Fatalf("second session must not block for the first one's messages: %q", got)
	}
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(second), Content: "for second"}); err != nil {
		t.Fatal(err)
	}
	if got := hook(); !strings.Contains(got, "dm/"+second) || !strings.Contains(got, "-as "+second) || strings.Contains(got, "dm/"+first+" ") {
		t.Fatalf("second session must block for its own message and wait as itself: %q", got)
	}
	setHarness(t, procs.Ref{}, procs.ErrGone)
	if got := hook(); !strings.Contains(got, "dm/"+first) {
		t.Fatalf("no harness match must fall back to the cwd identity: %q", got)
	}
}

// The SubagentStart hook names the session registered from its harness as
// the parent.
func TestSubagentHookParentIsSessionFromItsHarness(t *testing.T) {
	cfg, cwd, _, second, _ := twoSessions(t)
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	var out bytes.Buffer
	SubagentHook(cfg, strings.NewReader(`{"cwd":`+strconvQuote(cwd)+`,"agent_type":"general-purpose"}`), &out, io.Discard)
	var res struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	if want := `parent="` + second + `"`; !strings.Contains(res.HookSpecificOutput.AdditionalContext, want) {
		t.Fatalf("want %s in %q", want, res.HookSpecificOutput.AdditionalContext)
	}
}

// A wait started without -as waits as the session registered from its
// harness, falling back to the cwd identity.
func TestWaitIdentityUsesSessionFromItsHarness(t *testing.T) {
	cfg, cwd, first, second, _ := twoSessions(t)
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	if got := WaitIdentity(cfg, cwd, io.Discard); got != second {
		t.Fatalf("WaitIdentity = %q, want %q", got, second)
	}
	setHarness(t, procs.Ref{}, procs.ErrGone)
	if got := WaitIdentity(cfg, cwd, io.Discard); got != first {
		t.Fatalf("no harness: WaitIdentity = %q, want %q", got, first)
	}
}

// A session that has not registered yet must not borrow the cwd name while
// a live session registered from another harness holds it: its Stop hook
// does not block for that session's messages, and its wait has no identity.
func TestUnregisteredSessionDoesNotBorrowAnotherHarnessName(t *testing.T) {
	cfg, cwd, first, _, b := twoSessions(t)
	sam := peerBus(t, cfg, "Sam")
	if _, err := sam.Send("Sam", bus.SendInput{Channel: bus.DMChannel(first), Content: "for first"}); err != nil {
		t.Fatal(err)
	}
	setHarness(t, procs.Ref{Pid: 300, Start: 3}, nil)
	if got := SessionIdentity(b, cwd, io.Discard); got != "" {
		t.Fatalf("SessionIdentity = %q, want none", got)
	}
	if got := WaitIdentity(cfg, cwd, io.Discard); got != "" {
		t.Fatalf("WaitIdentity = %q, want none", got)
	}
	var out bytes.Buffer
	StopHook(cfg, strings.NewReader(`{"cwd":`+strconvQuote(cwd)+`}`), &out, io.Discard)
	if out.Len() != 0 {
		t.Fatalf("unregistered session must not block for %s's messages: %q", first, out.String())
	}
}

// The trigger for #39: after a stale sweep (every attachment expired, e.g.
// after a laptop sleep) a session that re-registers gets the bare cwd name
// it did not have before, while its harness is unchanged. Its identity
// changed under a live harness, so its old wait (under the suffixed name)
// is the one a new wait must replace.
//
// It models a new MCP process (a new owner) re-registering after a sweep,
// which ADR 0022 deliberately leaves alone. A live server restores its own
// row instead (internal/bus/sessions_restore_test.go).
func TestIdentityChangesAfterStaleSweepWhileHarnessSame(t *testing.T) {
	cfg, cwd, first, second, b := twoSessions(t)
	if second != first+"2" {
		t.Fatalf("setup: first=%q second=%q", first, second)
	}
	h2 := procs.Ref{Pid: 200, Start: 2}
	setHarness(t, h2, nil)
	if got := WaitIdentity(cfg, cwd, io.Discard); got != second {
		t.Fatalf("before the sweep WaitIdentity = %q, want %q", got, second)
	}
	// Both sessions' heartbeats are now older than the expiry; any Register sweeps them.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sweeper, err := bus.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sweeper.Close() }()
	sweeper.Now = func() time.Time { return b.Now().Add(31 * time.Second) }
	sweeper.Mono = func() time.Duration { return sweeper.Now().Sub(time.Unix(0, 0)) } // not a sleep
	if _, err := sweeper.Register("Sam", "", "", false); err != nil {
		t.Fatal(err)
	}
	// The second session re-registers from the same harness, asking for the cwd name.
	again, err := bus.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	again.Now = sweeper.Now
	again.Mono = func() time.Duration { return again.Now().Sub(time.Unix(0, 0)) }
	again.SetHarness(h2)
	r, err := again.Register(filepath.Base(cwd), "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sender != first || r.Sender == second {
		t.Fatalf("re-registered as %q, want the bare name %q (was %q)", r.Sender, first, second)
	}
	name, src := WaitIdentitySource(cfg, cwd, io.Discard)
	if name != first || src != SourceSession {
		t.Fatalf("WaitIdentitySource = %q, %q; want %q, session", name, src, first)
	}
}

func TestWaitIdentitySourceCwdFallback(t *testing.T) {
	cfg, cwd, first, _, _ := twoSessions(t)
	setHarness(t, procs.Ref{}, procs.ErrGone)
	name, src := WaitIdentitySource(cfg, cwd, io.Discard)
	if name != first || src != SourceCwd {
		t.Fatalf("WaitIdentitySource = %q, %q; want %q, cwd", name, src, first)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
