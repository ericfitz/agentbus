package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

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
	if _, err := b.Register("Sam", "", "", false); err != nil {
		t.Fatal(err)
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
	cfg, cwd, first, second, b := twoSessions(t)
	hook := func() string {
		var out bytes.Buffer
		StopHook(cfg, strings.NewReader(`{"cwd":`+strconvQuote(cwd)+`}`), &out, io.Discard)
		return out.String()
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: bus.DMChannel(first), Content: "for first"}); err != nil {
		t.Fatal(err)
	}
	setHarness(t, procs.Ref{Pid: 200, Start: 2}, nil)
	if got := hook(); got != "" {
		t.Fatalf("second session must not block for the first one's messages: %q", got)
	}
	if _, err := b.Send("Sam", bus.SendInput{Channel: bus.DMChannel(second), Content: "for second"}); err != nil {
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
	if _, err := b.Send("Sam", bus.SendInput{Channel: bus.DMChannel(first), Content: "for first"}); err != nil {
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

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
