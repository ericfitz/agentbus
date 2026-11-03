package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
)

// historyFixture is a board with Ann (live, n revisions in the bus) and Bob,
// the board focused with Ann selected.
func historyFixture(t *testing.T, n int) *fixture {
	t.Helper()
	f := newFixture(t)
	for i := 1; i <= n; i++ {
		if _, err := f.ab.SetStatusFor("Sam", "implementing", fmt.Sprintf("step %02d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	f.setBoard(
		boardEntry("Bob", "blocked", "waiting on review", "", true),
		boardEntry("Sam", "implementing", "step", "", true),
	)
	f.key("esc")
	f.key("b")
	f.key("tab")
	f.key("down") // time sort ties by name: Bob, Sam
	return f
}

func TestBoardEnterOpensHistory(t *testing.T) {
	f := newFixture(t)
	for _, s := range [][3]string{
		{"planning", "drafting the plan", ""},
		{"blocked", "stuck on CI", "runner down"},
		{"implementing", "back at it", ""},
	} {
		if _, err := f.ab.SetStatusFor("Sam", s[0], s[1], s[2]); err != nil {
			t.Fatal(err)
		}
	}
	f.setBoard(boardEntry("Sam", "implementing", "back at it", "", true))
	f.key("esc")
	f.key("b")
	f.key("tab")
	f.key("enter")
	pane := f.mainPane()
	lines := strings.Split(pane, "\n")
	hist, _, err := f.c.b.StatusHistory("Sam")
	if err != nil || len(hist) != 3 {
		t.Fatalf("history %v %v", hist, err)
	}
	prev := -1
	for _, s := range hist {
		want := []string{
			fmt.Sprintf("%d", s.Revision),
			time.UnixMilli(s.SetAt).Local().Format("15:04"),
			s.Tag, s.Text,
		}
		if s.Problem != "" {
			want = append(want, s.Problem)
		}
		at := -1
		for i, l := range lines {
			ok := true
			for _, w := range want {
				if !strings.Contains(l, w) {
					ok = false
				}
			}
			if ok {
				at = i
			}
		}
		if at < 0 || at <= prev {
			t.Fatalf("revision %d missing or out of order (oldest first):\n%s", s.Revision, pane)
		}
		prev = at
	}
	if !strings.Contains(lines[0], "Sam") || strings.Contains(lines[0], "offline") {
		t.Fatalf("live header: %q", lines[0])
	}
	if !strings.Contains(pane, statusIcon("planning")) && !strings.Contains(pane, strings.TrimSpace(statusIcon("planning"))) {
		t.Fatalf("no icon:\n%s", pane)
	}
}

func TestHistoryEscReturns(t *testing.T) {
	f := historyFixture(t, 2)
	f.key("enter")
	if f.m.boardState.history != "Sam" {
		t.Fatalf("history=%q", f.m.boardState.history)
	}
	// A status tick while the history is open must not reset it.
	f.run(f.m.statusCmd())
	if f.m.boardState.history != "Sam" || !strings.Contains(f.mainPane(), "step 02") {
		t.Fatalf("tick reset the history:\n%s", f.mainPane())
	}
	f.key("esc")
	if f.m.boardState.history != "" || !f.m.boardFocused() {
		t.Fatalf("esc: history=%q focused=%v", f.m.boardState.history, f.m.boardFocused())
	}
	if f.m.boardState.selID != "Sam" {
		t.Fatalf("selected %q, want Sam", f.m.boardState.selID)
	}
	if !strings.Contains(f.mainPane(), "eric") {
		t.Fatalf("board not shown:\n%s", f.mainPane())
	}
	f.key("esc") // a second esc leaves the board focus
	if f.m.boardFocused() {
		t.Fatal("second esc must unfocus the board")
	}
}

func TestHistoryScroll(t *testing.T) {
	f := historyFixture(t, 20)
	f.m.height = 14 // a short pane
	f.m.layout()
	f.key("enter")
	h := f.m.stream.Height
	if got := f.mainPane(); !strings.Contains(got, "step 01") || strings.Contains(got, "step 20") {
		t.Fatalf("top:\n%s", got)
	}
	f.key("pgdown")
	if got := f.mainPane(); strings.Contains(got, "step 01") || !strings.Contains(got, "step 0") {
		t.Fatalf("after pgdn:\n%s", got)
	}
	f.key("G")
	got := f.mainPane()
	if !strings.Contains(got, "step 20") || strings.Contains(got, "step 01") {
		t.Fatalf("G:\n%s", got)
	}
	if n := len(strings.Split(got, "\n")); n > h {
		t.Fatalf("%d lines in a %d-row pane", n, h)
	}
	f.key("pgup")
	if got := f.mainPane(); strings.Contains(got, "step 20") {
		t.Fatalf("pgup:\n%s", got)
	}
	f.key("g")
	if got := f.mainPane(); !strings.Contains(got, "step 01") {
		t.Fatalf("g:\n%s", got)
	}
}

func TestHistoryOffline(t *testing.T) {
	f := newFixture(t)
	if _, err := f.ab.SetStatusFor("Sam", "planning", "plan one", ""); err != nil {
		t.Fatal(err)
	}
	// Zed's statuses, then Zed leaves.
	zb2, zs := agent(t, f.c.cfg, "Zed")
	if _, err := zb2.SetStatus(zs, "blocked", "gone fishing", "no network"); err != nil {
		t.Fatal(err)
	}
	if err := zb2.EndSessions(); err != nil {
		t.Fatal(err)
	}
	off := boardEntry("Zed", "blocked", "gone fishing", "no network", false)
	f.setBoard(off)
	f.key("esc")
	f.key("b")
	f.key("tab")
	f.key("enter")
	pane := f.mainPane()
	first := strings.Split(pane, "\n")[0]
	if !strings.Contains(first, "Zed") || !strings.Contains(first, "offline") {
		t.Fatalf("header %q:\n%s", first, pane)
	}
	if !strings.Contains(pane, "gone fishing") || !strings.Contains(pane, "no network") {
		t.Fatalf("offline history missing:\n%s", pane)
	}
	for _, l := range strings.Split(pane, "\n") {
		if ansi.StringWidth(l) > f.m.stream.Width {
			t.Errorf("line wider than pane: %q", l)
		}
	}
}

func TestHistoryLoadError(t *testing.T) {
	f := historyFixture(t, 1)
	f.key("enter")
	f.send(statusHistoryMsg{name: "Sam", err: fmt.Errorf("boom")})
	if got := f.mainPane(); !strings.Contains(got, "boom") {
		t.Fatalf("error not shown:\n%s", got)
	}
	// A stale answer for another identity is ignored.
	f.send(statusHistoryMsg{name: "Other", hist: []bus.AgentStatus{{Identity: "Other", Revision: 9, Tag: "idle", Text: "stale"}}})
	if strings.Contains(f.mainPane(), "stale") {
		t.Fatal("stale history shown")
	}
}
