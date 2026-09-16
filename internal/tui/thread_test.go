package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

func (f *fixture) agentReply(t *testing.T, ch string, to int64, content string) bus.SendResult {
	t.Helper()
	r, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Content: content, ReplyTo: &to})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func contents(rs []row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = strings.Repeat(">", r.depth) + r.msg.Content
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// Threads sort by newest message; replies stay hidden until expanded, and
// expanding shows only the direct children.
func TestThreadsOrderCollapseAndExpandOneLevel(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "A")
	f.agentSend(t, "dev", "B")
	r1 := f.agentReply(t, "dev", a.Seq, "A1")
	f.agentReply(t, "dev", r1.Seq, "A1a")
	f.run(f.m.loadHistory("dev", nil)) // history load: no peek
	rs := f.m.rows("dev")
	if got := contents(rs); !eq(got, []string{"B", "A"}) || rs[1].hidden != 2 || rs[1].latest == 0 {
		t.Fatalf("collapsed rows %v hidden=%d latest=%d", got, rs[1].hidden, rs[1].latest)
	}
	f.m.expanded[a.Seq] = true
	rs = f.m.rows("dev")
	if got := contents(rs); !eq(got, []string{"B", "A", ">A1"}) || rs[2].hidden != 1 {
		t.Fatalf("one level expanded %v hidden=%d", got, rs[2].hidden)
	}
	v := f.m.renderStream()
	if !strings.Contains(ansi.Strip(v), markSel+" ") || !strings.Contains(v, "1 reply") || !strings.Contains(ansi.Strip(v), markOpen+" ") {
		t.Fatalf("summary line missing:\n%s", v)
	}
}

// A reply received live peeks just that reply (and its path) under a
// collapsed thread; a newer reply replaces it; space toggles the cursor
// message and drops the peek.
func TestNewReplyPeeksThenSpaceToggles(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "A")
	f.receive(t)
	r1 := f.agentReply(t, "dev", a.Seq, "A1")
	f.receive(t)
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1"}) {
		t.Fatalf("peek after live reply: %v", got)
	}
	f.agentReply(t, "dev", r1.Seq, "A1a")
	f.receive(t)
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1", ">>A1a"}) {
		t.Fatalf("newer peek replaces path: %v", got)
	}
	f.agentReply(t, "dev", a.Seq, "A2")
	f.receive(t)
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A2"}) {
		t.Fatalf("newest reply is the only peek: %v", got)
	}
	f.key("esc")
	f.key("tab") // cursor on the last row, A2
	f.key("up")  // root A
	f.key(" ")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1", ">A2"}) {
		t.Fatalf("space on the root shows direct children only: %v", got)
	}
	f.key("down")
	f.key("r")
	if f.m.replyTo == nil || f.m.replyTo.Content != "A1" {
		t.Fatalf("r replies to the visible cursor row, got %+v", f.m.replyTo)
	}
}

// TestLeftCollapsesTheWholeSubtree: right shows direct replies; left on the
// root hides every level, so the next right shows direct replies only.
func TestLeftCollapsesTheWholeSubtree(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "A")
	a1 := f.agentReply(t, "dev", a.Seq, "A1")
	f.agentReply(t, "dev", a1.Seq, "A11")
	f.receive(t)
	f.key("esc")
	f.key("tab") // cursor on the last row
	f.key("up")
	f.key("up") // root A
	f.key("right")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1"}) {
		t.Fatalf("right on the root shows direct children only: %v", got)
	}
	f.key("down")
	f.key("right")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1", ">>A11"}) {
		t.Fatalf("right on A1 shows A11: %v", got)
	}
	f.key("up")
	f.key("left")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A"}) {
		t.Fatalf("left on the root hides the subtree: %v", got)
	}
	f.key("right")
	if got := contents(f.m.rows("dev")); !eq(got, []string{"A", ">A1"}) {
		t.Fatalf("expand after a full collapse shows direct children only: %v", got)
	}
}
