package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/muesli/termenv"
)

func TestViewShowsRailsStreamComposeAndStatus(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.run(f.m.statusCmd())

	// Insert mode is the fixture's starting mode: the compose line reads
	// "dev ›" and the status bar shows the insert-mode key hints.
	v := f.m.View()
	for _, want := range []string{"channels", "dev", iconMem + "dev-notes", "hello from sam", "Sam", "sessions", "eric", "as eric", "esc commands", "alt+enter", "dev ›"} {
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
	rail := f.m.renderRails()
	if !strings.Contains(rail, iconIdle+"codex 14m") {
		t.Fatalf("rail:\n%s", rail)
	}
}

func TestRailIsFifthOfScreenWithFloor(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ width, rail int }{{60, 16}, {100, 20}, {200, 40}} {
		f.m.width = tc.width
		f.m.layout()
		if f.m.stream.Width != tc.width-tc.rail-1 {
			t.Errorf("width %d: stream %d, want %d", tc.width, f.m.stream.Width, tc.width-tc.rail-1)
		}
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

// A selected row must keep its background across every styled segment: the
// inner timestamp and sender styles end with a reset, which used to drop the
// highlight for the rest of the first line.
func TestSelectedRowKeepsBackgroundAcrossSegments(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.key("esc")
	f.key("tab") // focus the stream: cursor lands on the last message
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, "Sam") || strings.Count(first, "44m") < 3 {
		t.Fatalf("segments after a reset lost the selection background: %q", first)
	}
	rail := strings.SplitN(f.m.renderRails(), "\n", 3)[1]
	if !strings.Contains(rail, "›") || !strings.Contains(rail, "44m") {
		t.Fatalf("selected channel row lost the background: %q", rail)
	}
	if f.m.theme.Sel == f.m.theme.Dim {
		t.Fatal("selection background must differ from the dim text color")
	}
}

// TestReplyIndentAppliesToWrappedLines: a long reply wraps, and every
// wrapped line keeps the reply's tree indentation, not just the first.
func TestReplyIndentAppliesToWrappedLines(t *testing.T) {
	f := newFixture(t)
	a := f.agentSend(t, "dev", "A")
	f.agentReply(t, "dev", a.Seq, strings.Repeat("word ", 30))
	f.receive(t)
	f.m.width = 60
	f.m.layout()
	f.key("esc")
	f.key("tab")
	f.key("up")
	f.key("right")
	var reply []string
	for _, l := range strings.Split(f.m.renderStream(), "\n") {
		if strings.Contains(l, "word") {
			reply = append(reply, ansi.Strip(l))
		}
	}
	if len(reply) < 2 {
		t.Fatalf("reply did not wrap: %q", reply)
	}
	for _, l := range reply[1:] {
		if !strings.HasPrefix(l, "    ") {
			t.Fatalf("wrapped reply line lost its indent: %q", l)
		}
	}
}
