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

	// Insert mode is the fixture's starting mode: the compose line reads
	// "dev ›" and the status bar shows the insert-mode key hints.
	v := f.m.View()
	for _, want := range []string{"channels", "dev", "◆ dev-notes", "hello from sam", "Sam", "sessions", "eric", "as eric", "esc commands", "alt+enter", "dev ›"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	assertStatusBarIsLastLine(t, v, f.m.height)

	// Normal mode swaps the status bar for the command-key help.
	f.key("esc")
	v = f.m.View()
	for _, want := range []string{"? help", "/ search", "m memories", "h health", "q quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("normal-mode view lacks %q:\n%s", want, v)
		}
	}
	assertStatusBarIsLastLine(t, v, f.m.height)
}

// assertStatusBarIsLastLine catches both a status bar that wrapped onto two
// rows (pushing the real last row off screen) and a layout that reserves one
// row too many or too few for it.
func assertStatusBarIsLastLine(t *testing.T, v string, height int) {
	t.Helper()
	lines := strings.Split(v, "\n")
	if len(lines) > height {
		t.Fatalf("view is %d lines for height %d:\n%s", len(lines), height, v)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "db ") {
		t.Fatalf("status bar must be the last line, got %q:\n%s", last, v)
	}
}

func TestLongChannelNameAndShortRailDoNotOverflow(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 30)
	if _, err := f.ab.CreateChannel(f.sam, long, "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.m.stream.Height = 1 // fewer rows than channels: rails must not grow past this
	v := f.m.View()
	assertStatusBarIsLastLine(t, v, f.m.height)
}

func TestStreamShowsNewDividerAndGap(t *testing.T) {
	f := newFixture(t)
	one := f.agentSend(t, "dev-notes", "one")
	f.receive(t)
	f.key("esc")
	f.key("down") // visit dev-notes: "one" is now seen
	f.key("up")   // back to dev
	f.agentSend(t, "dev-notes", "two")
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev-notes", From: one.Seq, To: one.Seq}}}})
	f.receive(t)
	f.key("down") // dev-notes again: divider sits after "one"
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
	f.key("down")
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
