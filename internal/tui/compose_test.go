package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestEnterSendsToSelectedChannelAndRecordsLastSent(t *testing.T) {
	f := newFixture(t)
	f.key("ship it")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["dev"]
	if len(ms) != 1 || ms[0].Content != "ship it" || ms[0].Sender != "eric" {
		t.Fatalf("got %+v", ms)
	}
	if f.m.compose.Value() != "" || f.m.lastSent != "ship it" {
		t.Fatalf("compose=%q lastSent=%q", f.m.compose.Value(), f.m.lastSent)
	}
	f.key("up")
	if f.m.compose.Value() != "ship it" {
		t.Fatal("up on empty compose recalls last sent")
	}
	f.key("ctrl+u")
	if f.m.compose.Value() != "" {
		t.Fatal("ctrl+u clears")
	}
}

func TestEmptyComposeDoesNotSend(t *testing.T) {
	f := newFixture(t)
	f.key("enter")
	f.receive(t)
	if len(f.m.msgs["dev"]) != 0 {
		t.Fatal("empty send")
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	f := newFixture(t)
	f.key("a")
	f.key("alt+enter")
	f.key("b")
	if f.m.compose.Value() != "a\nb" {
		t.Fatalf("got %q", f.m.compose.Value())
	}
	if f.m.compose.Height() != 2 {
		t.Fatalf("compose grows to 2 rows, got %d", f.m.compose.Height())
	}
}

func TestReplySetsReplyToAndEscCancels(t *testing.T) {
	f := newFixture(t)
	r := f.agentSend(t, "dev", "question?")
	f.receive(t)
	f.key("esc")
	f.key("r")
	if f.m.replyTo == nil || f.m.replyTo.Seq != r.Seq || f.m.mode != modeInsert {
		t.Fatalf("replyTo=%v mode=%v", f.m.replyTo, f.m.mode)
	}
	f.key("yes")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["dev"]
	if len(ms) != 2 || ms[1].ReplyTo == nil || *ms[1].ReplyTo != r.Seq {
		t.Fatalf("reply not linked: %+v", ms)
	}
	if f.m.replyTo != nil {
		t.Fatal("reply mode ends after send")
	}
	f.key("esc")
	f.key("r")
	f.key("esc")
	if f.m.replyTo != nil || f.m.mode != modeInsert {
		t.Fatal("esc in reply mode cancels the reply and stays in insert mode")
	}
}

func TestEnterOnMemoryChannelCreatesMemory(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("down") // dev-notes
	f.key("i")
	f.key("Reviewer checklist")
	f.key("enter")
	f.receive(t)
	ms := f.m.msgs["dev-notes"]
	if len(ms) != 1 || ms[0].MemoryID == nil {
		t.Fatalf("memory not created: %+v", ms)
	}
}

func TestSendErrorToasts(t *testing.T) {
	f := newFixture(t)
	f.send(sentMsg{channel: "dev", err: &bus.Error{Code: "rate_limited", Message: "slow down", Retryable: true}})
	if !strings.Contains(f.m.toast, "rate_limited") || !strings.Contains(f.m.toast, "retryable") {
		t.Fatalf("toast=%q", f.m.toast)
	}
}

func TestCreateChannelPrompt(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("c")
	if !f.m.prompt.active {
		t.Fatal("c opens the create-channel prompt")
	}
	f.key("ops")
	f.key("enter")
	found := false
	for _, c := range f.m.channels {
		found = found || c.Name == "ops"
	}
	if !found || f.m.prompt.active {
		t.Fatalf("channel not created: %v active=%v", f.m.channels, f.m.prompt.active)
	}
}

func TestToggleSubscribe(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("s")
	if f.c.subscribed["dev"] {
		t.Fatal("s unsubscribes the selected channel")
	}
	// A status tick must not resubscribe (and so not replay retained history)
	// while the channel is deliberately off.
	f.run(f.m.statusCmd())
	if f.c.subscribed["dev"] {
		t.Fatal("status tick must not resubscribe an explicitly unsubscribed channel")
	}
	if f.c.isSubscribed("dev") {
		t.Fatal("isSubscribed must still report off after a status tick")
	}
	f.key("s")
	if !f.c.subscribed["dev"] {
		t.Fatal("s again resubscribes")
	}
}
