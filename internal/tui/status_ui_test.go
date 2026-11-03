package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

func boardEntry(name, tag, text, problem string, live bool) bus.BoardEntry {
	return bus.BoardEntry{
		AgentStatus: bus.AgentStatus{Identity: name, Revision: 1, Tag: tag, Text: text, Problem: problem, SetAt: time.Now().UnixMilli()},
		Live:        live,
	}
}

func withAge(e bus.BoardEntry, d time.Duration) bus.BoardEntry {
	e.SetAt = time.Now().Add(-d).UnixMilli()
	return e
}

func TestStatusIconsMapped(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	tags := append(append([]string{}, bus.SettableStatusTags...), bus.StatusOffline)
	for _, tag := range tags {
		name := "status_" + tag
		if emojiIcons[name] == "" || nerdIcons[name] == "" {
			t.Errorf("%s: emoji %q nerd %q", name, emojiIcons[name], nerdIcons[name])
		}
		if statusIcon(tag) != emojiIcons[name] {
			t.Errorf("statusIcon(%q) = %q", tag, statusIcon(tag))
		}
	}
	if statusIcon("") != iconAgent {
		t.Errorf("no status must map to the agent icon")
	}
	cfg := config.Default()
	cfg.IconMap = map[string]string{"status_blocked": "X"}
	var warn bytes.Buffer
	LoadIcons(cfg, &warn)
	if got := strings.TrimSpace(statusIcon("blocked")); got != "X" || warn.Len() != 0 {
		t.Fatalf("statusIcon(blocked) = %q, warn %q", got, warn.String())
	}
	cfg = config.Default()
	cfg.Icons = "nerdfont"
	LoadIcons(cfg, &warn)
	if statusIcon("offline") != nerdIcons["status_offline"]+"  " {
		t.Errorf("nerd offline = %q", statusIcon("offline"))
	}
}

func TestRailSessionStatusIcon(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{boardEntry("Sam", "implementing", "wiring it", "", true)}})
	for _, l := range f.railLines() {
		if strings.Contains(l, "Sam") {
			if i, j := strings.Index(l, statusIcon("implementing")), strings.Index(l, "Sam"); i < 0 || i > j {
				t.Fatalf("icon must precede name: %q", l)
			}
			return
		}
	}
	t.Fatal("no Sam row")
}

func TestRailOfflineRow(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{withAge(boardEntry("Ghost", "offline", "was busy", "", false), 3*time.Hour+10*time.Minute)}})
	if !contains(f.m.sessionNames(), "Ghost") {
		t.Fatalf("names=%v", f.m.sessionNames())
	}
	var row string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(l, "Ghost") {
			row = l
		}
	}
	if !strings.Contains(ansi.Strip(row), statusIcon("offline")) {
		t.Fatalf("offline row lacks the offline icon: %q", ansi.Strip(row))
	}
	if !strings.Contains(ansi.Strip(row), "Ghost 3h") {
		t.Fatalf("board-only offline row must show the status age: %q", ansi.Strip(row))
	}
	f.key("esc")
	f.toSessions()
	names := f.m.sessionNames()
	for f.m.sessSel < len(names)-1 && names[f.m.sessSel] != "Ghost" {
		f.key("down")
	}
	if names[f.m.sessSel] != "Ghost" {
		t.Fatalf("could not select Ghost: sessSel=%d", f.m.sessSel)
	}
	if f.m.toast != "" {
		t.Fatalf("selecting an offline row must not error: %q", f.m.toast)
	}
	if h := ansi.Strip(f.m.renderHeader()); !strings.Contains(h, "Ghost") || !strings.Contains(h, "offline · was busy") {
		t.Fatalf("header=%q", h)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestHeaderShowsStatus(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{boardEntry("Sam", "implementing", "wiring it", "tests red", true)}})
	f.key("esc")
	f.toSessions()
	names := f.m.sessionNames()
	for f.m.sessSel < len(names)-1 && names[f.m.sessSel] != "Sam" {
		f.key("down")
	}
	f.m.stream.Width = 80
	h := ansi.Strip(f.m.renderHeader())
	if w := ansi.StringWidth(h); w > 80 || !strings.Contains(h, "implementing · wiring it · tests red") {
		t.Fatalf("header=%q", h)
	}
	// Narrow: cut by display width, with the ellipsis.
	f.m.stream.Width = 40
	long := boardEntry("Sam", "implementing", strings.Repeat("宽", 60), "", true)
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{long}})
	h = ansi.Strip(f.m.renderHeader())
	if w := ansi.StringWidth(h); w > 40 || !strings.HasSuffix(h, "…") {
		t.Fatalf("width %d header=%q", w, h)
	}
}

func TestBoardErrorKeepsStatus(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.send(statusMsg{st: f.m.status, board: []bus.BoardEntry{boardEntry("Sam", "idle", "x", "", true)}})
	f.send(statusMsg{st: f.m.status, boardErr: errTest})
	if len(f.m.board) != 1 || f.m.statusErr != nil {
		t.Fatalf("board=%v statusErr=%v", f.m.board, f.m.statusErr)
	}
}

type testErr struct{}

func (testErr) Error() string { return "boom" }

var errTest error = testErr{}
