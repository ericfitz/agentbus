package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

// patSend sends from a second agent, Pat, for tests that need a message in
// Sam's inbox from someone other than Sam (a self-DM is rejected, #42).
func (f *fixture) patSend(t *testing.T, ch, content string, tags ...string) bus.SendResult {
	t.Helper()
	// A second process: one process holds one top-level name (ADR 0025
	// decision 6), so Pat cannot share f.ab with Sam.
	if f.pat == nil {
		f.pat, _ = agent(t, f.c.cfg, "Pat")
	}
	r, err := f.pat.Send("Pat", bus.SendInput{Channel: ch, Content: content, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The observer sends as its own identity, so composing in its own inbox
// (without replying to anyone) would be a DM to itself. The bus rejects it
// and the TUI toasts the error instead of storing an undeliverable message.
func TestComposeInOwnInboxIsRejected(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.agentSend(t, "dm/"+f.c.as, "ping")
	f.receive(t)
	f.key("esc")
	f.toSessions()
	for i, n := range f.m.sessionNames() {
		if n == f.c.as {
			for f.m.sessSel < i {
				f.key("down")
			}
		}
	}
	if f.m.selName() != "dm/"+f.c.as {
		t.Fatalf("selected=%q", f.m.selName())
	}
	f.key("i")
	for _, r := range "note to self" {
		f.key(string(r))
	}
	f.key("enter")
	if !strings.Contains(f.m.toast, "yourself") {
		t.Fatalf("toast=%q", f.m.toast)
	}
	ms, err := f.c.b.History(f.c.as, "dm/"+f.c.as, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Content == "note to self" {
			t.Fatalf("self-DM was stored: %+v", m)
		}
	}
}
