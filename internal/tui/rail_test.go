package tui

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/muesli/termenv"
)

func TestRailWindowScrollsByOneWithArrows(t *testing.T) {
	// 10 items in 5 rows: from the top, 4 items and the down arrow.
	if top, end := railWindow(10, 5, 0, 0); top != 0 || end != 4 {
		t.Fatalf("top=%d end=%d, want 0 4", top, end)
	}
	// Stepping past the last shown item scrolls just far enough to show
	// it; the up arrow now takes a row too.
	top, end := railWindow(10, 5, 0, 4)
	if top != 2 || end != 5 {
		t.Fatalf("top=%d end=%d, want 2 5", top, end)
	}
	if top, end = railWindow(10, 5, top, 5); top != 3 || end != 6 {
		t.Fatalf("top=%d end=%d, want 3 6", top, end)
	}
	// The last item: no down arrow, and no empty rows under it.
	if top, end = railWindow(10, 5, top, 9); top != 6 || end != 10 {
		t.Fatalf("top=%d end=%d, want 6 10", top, end)
	}
	// Back up: stepping above the first shown item scrolls by one.
	if top, end = railWindow(10, 5, 6, 5); top != 5 || end != 8 {
		t.Fatalf("top=%d end=%d, want 5 8", top, end)
	}
	// Everything fits: no scrolling at all.
	if top, end = railWindow(3, 5, 2, 2); top != 0 || end != 3 {
		t.Fatalf("top=%d end=%d, want 0 3", top, end)
	}
	// A selection elsewhere keeps the offset.
	if top, _ = railWindow(10, 5, 3, -1); top != 3 {
		t.Fatalf("top=%d, want 3", top)
	}
}

func TestRailBudgetsKeepTwoItemsPerList(t *testing.T) {
	// Plenty of room: every list gets all it needs.
	if b := railBudgets(40, [railLists]int{5, 1, 3}); b != [railLists]int{5, 1, 3} {
		t.Fatalf("budgets %v", b)
	}
	// Too little: each long list still gets four rows (two items plus both
	// arrows), and the spare row goes round-robin.
	if b := railBudgets(railFixedRows+9, [railLists]int{20, 0, 20}); b != [railLists]int{5, 0, 4} {
		t.Fatalf("budgets %v", b)
	}
	if b := railBudgets(railFixedRows+5, [railLists]int{20, 2, 20}); b != [railLists]int{4, 2, 4} {
		t.Fatalf("budgets %v", b)
	}
}

// railLines is the rendered rail, stripped and trimmed, one entry per row.
func (f *fixture) railLines() []string {
	lines := strings.Split(ansi.Strip(f.m.renderRails()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func TestRailShowsEveryListWithTagHint(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	tags := slices.Index(lines, "tags")
	if tags < 0 || lines[tags+1] != railTagHint || slices.Index(lines, "sessions") < tags {
		t.Fatalf("want a tags list holding only the hint, before sessions:\n%s", strings.Join(lines, "\n"))
	}
	f.followTags(t, []string{"docs"})
	lines = f.railLines()
	tags = slices.Index(lines, "tags")
	if tags < 0 || !strings.Contains(lines[tags+1], "docs") || lines[tags+2] != railTagHint {
		t.Fatalf("want the hint after the tag set:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRailSelectedRowStaysOneLine(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 60)
	if _, err := f.ab.CreateChannel(f.sam, long, "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.key("esc")
	before := len(f.railLines())
	for f.m.selName() != long {
		f.key("down")
	}
	lines := f.railLines()
	if len(lines) != before {
		t.Fatalf("selecting the long channel changed the rail from %d to %d rows:\n%s", before, len(lines), strings.Join(lines, "\n"))
	}
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "xxxx") })
	if i < 0 || i+1 >= len(lines) {
		t.Fatalf("want the long channel shown, with a row after it:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasSuffix(lines[i], "…") || strings.Contains(lines[i+1], "x") {
		t.Fatalf("want one row ending in an ellipsis:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if w := ansi.StringWidth(l); w > f.m.railWidth() {
			t.Fatalf("row %q is %d wide, rail is %d", ansi.Strip(l), w, f.m.railWidth())
		}
	}
}

func TestRailScrollsLongChannelList(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if _, err := f.ab.CreateChannel(f.sam, fmt.Sprintf("ch%02d", i), "ordinary"); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
	f.send(tea.WindowSizeMsg{Width: 100, Height: 20})
	f.key("esc")
	f.key("home")
	for f.m.sel > 0 {
		f.key("up")
	}
	nc := spans(f)[listChannels]
	if nc.top != 0 || nc.end >= nc.n {
		t.Fatalf("want the list cut from the top, got %+v", nc)
	}
	lines := f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▼") }) {
		t.Fatalf("want a down arrow:\n%s", strings.Join(lines, "\n"))
	}
	if slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▲") }) {
		t.Fatalf("no up arrow at the top:\n%s", strings.Join(lines, "\n"))
	}
	// Down from the last shown channel scrolls and selects the next one.
	for range nc.end - 1 {
		f.key("down")
	}
	f.key("down")
	if f.m.sel != nc.end {
		t.Fatalf("sel=%d, want %d", f.m.sel, nc.end)
	}
	s := spans(f)[listChannels]
	if s.top == 0 || f.m.sel >= s.end || s.end-s.top < 2 {
		t.Fatalf("selection not scrolled into view: %+v sel=%d", s, f.m.sel)
	}
	lines = f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▲") }) {
		t.Fatalf("want an up arrow once scrolled:\n%s", strings.Join(lines, "\n"))
	}
	// Down through the rest reaches the last channel, then leaves the list.
	for f.m.sel < s.n-1 {
		f.key("down")
	}
	if s = spans(f)[listChannels]; s.end != s.n {
		t.Fatalf("last channel not shown: %+v", s)
	}
	if lines = f.railLines(); slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▼") }) {
		t.Fatalf("no down arrow at the end:\n%s", strings.Join(lines, "\n"))
	}
	f.key("down")
	if f.m.pane() != paneSessions {
		t.Fatalf("down from the last channel should enter sessions, pane=%v", f.m.pane())
	}
	// Up from the first session returns to the last channel, still shown.
	f.key("up")
	if s = spans(f)[listChannels]; f.m.sel != s.n-1 || s.end != s.n {
		t.Fatalf("sel=%d span=%+v", f.m.sel, s)
	}
	// Up scrolls back one at a time to the top.
	for f.m.sel > 0 {
		top := spans(f)[listChannels].top
		f.key("up")
		if s = spans(f)[listChannels]; f.m.sel < s.top || top-s.top > 1 {
			t.Fatalf("sel=%d span=%+v (was top %d)", f.m.sel, s, top)
		}
	}
	if s = spans(f)[listChannels]; s.top != 0 {
		t.Fatalf("back at the first channel, want top 0: %+v", s)
	}
}

func spans(f *fixture) [railLists]railSpan { return f.m.railSpans() }

// invisibleText returns the printable runs of line drawn in a foreground
// equal to the background in effect, after SGR colors 30-37/90-97 against
// 40-47/100-107 and 38;5;n against 48;5;n; nothing when every cell is
// legible. Only the sixteen ANSI colors and the default are representable
// in a theme, so those forms are the ones a rendered row can carry.
func invisibleText(line string) []string {
	var runs []string
	var run strings.Builder
	fg, bg := "", ""
	state := byte(ansi.NormalState)
	flush := func() {
		if run.Len() > 0 {
			runs = append(runs, run.String())
			run.Reset()
		}
	}
	for rest := line; rest != ""; {
		seq, width, n, next := ansi.DecodeSequence(rest, state, nil)
		state, rest = next, rest[n:]
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
			for i := 0; i < len(params); i++ {
				p := params[i]
				switch {
				case p == "" || p == "0":
					fg, bg = "", ""
				case p == "38" || p == "48":
					if i+2 < len(params) && params[i+1] == "5" {
						if p == "38" {
							fg = "256:" + params[i+2]
						} else {
							bg = "256:" + params[i+2]
						}
						i += 2
					}
				case p == "39":
					fg = ""
				case p == "49":
					bg = ""
				case len(p) == 2 && p[0] == '3':
					fg = "ansi:" + string(p[1])
				case len(p) == 2 && p[0] == '9':
					fg = "ansi:" + string('8'+p[1]-'0')
				case len(p) == 2 && p[0] == '4':
					bg = "ansi:" + string(p[1])
				case len(p) == 3 && strings.HasPrefix(p, "10"):
					bg = "ansi:" + string('8'+p[2]-'0')
				}
			}
			continue
		}
		if width > 0 && strings.TrimSpace(seq) != "" && fg != "" && fg == bg {
			run.WriteString(seq)
		} else if width > 0 {
			flush()
		}
	}
	flush()
	return runs
}

// selectedRailRow is the rendered rail row holding text, with its escapes.
func (f *fixture) selectedRailRow(t *testing.T, text string) string {
	t.Helper()
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(ansi.Strip(l), text) {
			return l
		}
	}
	t.Fatalf("no rail row holds %q:\n%s", text, strings.Join(f.railLines(), "\n"))
	return ""
}

// idleDMPeer registers cli-bot on the bus, has it DM the TUI, lets a
// status report list it live, then ends its session and reports again: the
// shape of a peer that only ever ran CLI commands, which the rail draws as
// an idle session with a DM inbox.
func (f *fixture) idleDMPeer(t *testing.T, cfg config.Config) {
	t.Helper()
	cb, bot := agent(t, cfg, "cli-bot")
	if _, err := cb.Send(bot, bus.SendInput{Channel: bus.DMChannel(f.c.as), Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if err := cb.EndSessions(); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	f.m.sessionsSeen["cli-bot"] = time.Now().Add(-5 * time.Minute)
	if slices.ContainsFunc(f.m.status.Sessions, func(s bus.Session) bool { return s.Sender == "cli-bot" }) {
		t.Fatal("cli-bot is still live")
	}
}

// selectSession walks the rail selection onto session name.
func (f *fixture) selectSession(t *testing.T, name string) {
	t.Helper()
	f.key("esc")
	f.key("home")
	f.toSessions()
	for f.m.sessionNames()[f.m.sessSel] != name {
		f.key("down")
	}
	if f.m.pane() != paneSessions || f.m.selName() != bus.DMChannel(name) {
		t.Fatalf("pane=%v sel=%q, want sessions/%s", f.m.pane(), f.m.selName(), bus.DMChannel(name))
	}
}

// A DM peer that only ran CLI commands is an idle session with an inbox.
// Its row is idle icon + dim "name age". Selected, the row must still show
// the name in a legible color whether the rail has focus (Sel) or the
// stream or compose has it (SelInactive), and take one row either way.
func TestSelectedIdleSessionKeepsName(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); setIcons(emojiIcons) })
	for _, icons := range []string{"emoji", "nerdfont"} {
		for _, rail := range []int{12, 16, 24} {
			cfg := testConfig(t)
			cfg.Icons = icons
			cfg.TUIRailWidth = rail
			LoadIcons(cfg, io.Discard)
			f := newFixtureWith(t, cfg)
			f.idleDMPeer(t, cfg)
			f.key("esc")
			rows := len(f.railLines())
			f.selectSession(t, "cli-bot")
			for _, focus := range []string{"rail", "stream", "compose"} {
				switch focus {
				case "stream":
					f.key("tab")
				case "compose":
					f.key("i")
				}
				if got := f.m.pane(); got != map[string]pane{"rail": paneSessions, "stream": paneStream, "compose": paneCompose}[focus] {
					t.Fatalf("%s/%d/%s: pane=%v", icons, rail, focus, got)
				}
				row := f.selectedRailRow(t, "cli-bot")
				plain := ansi.Strip(row)
				if !strings.HasPrefix(plain, markSel+iconIdle) || !strings.Contains(plain, "cli-bot 5m") {
					t.Errorf("%s/%d/%s: row %q lacks the idle icon or the name", icons, rail, focus, plain)
				}
				if hidden := invisibleText(row); len(hidden) > 0 {
					t.Errorf("%s/%d/%s: %q drawn in its background color: %q", icons, rail, focus, hidden, row)
				}
				if n := len(f.railLines()); n != rows {
					t.Errorf("%s/%d/%s: rail has %d rows selected, %d unselected", icons, rail, focus, n, rows)
				}
			}
		}
	}
}

// Every selectable row type (chat, memory, task channel, tag set, live
// session, idle session), under both icon sets, at the narrowest and a
// normal rail width, with and without rail focus: the selected row renders
// as exactly one row of exactly the rail's width, with no text drawn in
// its background color. Spec 2026-10-21 section 5.
func TestSelectedRowsStayOneLine(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); setIcons(emojiIcons) })
	for _, icons := range []string{"emoji", "nerdfont"} {
		for _, rail := range []int{12, 24} {
			cfg := testConfig(t)
			cfg.Icons = icons
			cfg.TUIRailWidth = rail
			LoadIcons(cfg, io.Discard)
			f := newFixtureWith(t, cfg)
			if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
				t.Fatal(err)
			}
			f.followTags(t, []string{"docs"})
			f.idleDMPeer(t, cfg) // runs statusCmd: Sam and eric are live sessions
			f.key("esc")
			f.key("home")
			want := map[string]bool{"dev": false, "dev-notes": false, "tasks/work": false, tagPanePrefix + "docs": false, bus.DMChannel(f.sam): false, bus.DMChannel("cli-bot"): false}
			width := f.m.railWidth()
			for prevSel := ""; ; prevSel = f.m.selName() {
				sel := f.m.selName()
				want[sel] = true
				for _, focused := range []bool{true, false} {
					f.m.mode = modeNormal
					if !focused {
						f.m.mode = modeInsert
					}
					rows := strings.Split(f.m.renderRails(), "\n")
					var selected []string
					for _, r := range rows {
						if strings.HasPrefix(ansi.Strip(r), markSel) {
							selected = append(selected, r)
						}
					}
					if len(selected) != 1 {
						t.Fatalf("%s/%d/%q/focused=%v: %d selected rows:\n%s", icons, rail, sel, focused, len(selected), ansi.Strip(strings.Join(rows, "\n")))
					}
					row := selected[0]
					if w := ansi.StringWidth(row); w != width {
						t.Errorf("%s/%d/%q/focused=%v: selected row %q is %d wide, rail is %d", icons, rail, sel, focused, ansi.Strip(row), w, width)
					}
					if hidden := invisibleText(row); len(hidden) > 0 {
						t.Errorf("%s/%d/%q/focused=%v: %q drawn in its background color: %q", icons, rail, sel, focused, hidden, row)
					}
				}
				f.m.mode = modeNormal
				f.key("down")
				if f.m.selName() == prevSel || f.m.selName() == sel {
					break
				}
			}
			for name, seen := range want {
				if !seen {
					t.Errorf("%s/%d: never selected %q", icons, rail, name)
				}
			}
		}
	}
}
