package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestViewShowsRailsStreamComposeAndStatus(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.run(f.m.statusCmd())
	v := f.m.View()
	for _, want := range []string{"channels", "dev", "◆ notes", "hello from sam", "Sam", "sessions", "eric", "as eric", "? help", "/ search", "m memories", "h health", "q quit", "dev ›"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if lines := strings.Count(v, "\n") + 1; lines > f.m.height {
		t.Fatalf("view is %d lines for height %d", lines, f.m.height)
	}
}

func TestStreamShowsNewDividerAndGap(t *testing.T) {
	f := newFixture(t)
	one := f.agentSend(t, "notes", "one")
	f.receive(t)
	f.key("esc")
	f.key("j") // visit notes: "one" is now seen
	f.key("k") // back to dev
	f.agentSend(t, "notes", "two")
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "notes", From: one.Seq, To: one.Seq}}}})
	f.receive(t)
	f.key("j") // notes again: divider sits after "one"
	s := f.m.renderStream()
	if !strings.Contains(s, "new") || !strings.Contains(s, "1 evicted") {
		t.Fatalf("stream:\n%s", s)
	}
	if strings.Index(s, "one") > strings.Index(s, "new") {
		t.Fatalf("divider must sit after the last seen message:\n%s", s)
	}
}

func TestReplyBannerAndMemoryHint(t *testing.T) {
	f := newFixture(t)
	r := f.agentSend(t, "dev", "q")
	f.receive(t)
	f.key("esc")
	f.key("r")
	if v := f.m.View(); !strings.Contains(v, "replying to Sam") || !strings.Contains(v, "#"+itoa(r.Seq)) {
		t.Fatalf("view:\n%s", v)
	}
	f.key("esc")
	f.key("esc")
	f.key("j")
	if v := f.m.View(); !strings.Contains(v, "new memory") {
		t.Fatalf("memory channel hint missing:\n%s", v)
	}
}

func TestIdleSessionShowsAge(t *testing.T) {
	f := newFixture(t)
	f.m.sessionsSeen["codex"] = time.Now().Add(-14 * time.Minute)
	_, right := f.m.renderRails()
	if !strings.Contains(right, "○ codex") || !strings.Contains(right, "14m") {
		t.Fatalf("rail:\n%s", right)
	}
}

func TestEmptyChannelAndCapacityNotice(t *testing.T) {
	f := newFixture(t)
	f.m.status.Notice = "1.9 GiB of 2 GiB used"
	v := f.m.View()
	if !strings.Contains(v, "no messages yet in dev") || !strings.Contains(v, "capacity") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestFmtBytes(t *testing.T) {
	if got := fmtBytes(412 << 20); got != "412 MiB" {
		t.Fatal(got)
	}
	if got := fmtBytes(2 << 30); got != "2.0 GiB" {
		t.Fatal(got)
	}
}
