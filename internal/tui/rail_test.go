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
	if top, end := railWindow(10, 5, 0, 0, false); top != 0 || end != 4 {
		t.Fatalf("top=%d end=%d, want 0 4", top, end)
	}
	// Stepping past the last shown item scrolls just far enough to show
	// it; the up arrow now takes a row too.
	top, end := railWindow(10, 5, 0, 4, false)
	if top != 2 || end != 5 {
		t.Fatalf("top=%d end=%d, want 2 5", top, end)
	}
	if top, end = railWindow(10, 5, top, 5, false); top != 3 || end != 6 {
		t.Fatalf("top=%d end=%d, want 3 6", top, end)
	}
	// The last item: no down arrow, and no empty rows under it.
	if top, end = railWindow(10, 5, top, 9, false); top != 6 || end != 10 {
		t.Fatalf("top=%d end=%d, want 6 10", top, end)
	}
	// Back up: stepping above the first shown item scrolls by one.
	if top, end = railWindow(10, 5, 6, 5, false); top != 5 || end != 8 {
		t.Fatalf("top=%d end=%d, want 5 8", top, end)
	}
	// Everything fits: no scrolling at all.
	if top, end = railWindow(3, 5, 2, 2, false); top != 0 || end != 3 {
		t.Fatalf("top=%d end=%d, want 0 3", top, end)
	}
	// A selection elsewhere keeps the offset.
	if top, _ = railWindow(10, 5, 3, -1, false); top != 3 {
		t.Fatalf("top=%d, want 3", top)
	}
}

func TestRailLayoutFitsAll(t *testing.T) {
	n := [railSections]int{5, 1, 3, 1, 3}
	b, blanks, compact := railLayout(40, n, [railSections]bool{})
	if b != n || !blanks || compact {
		t.Fatalf("b=%v blanks=%v compact=%v, want every item, blanks, not compact", b, blanks, compact)
	}
}

func TestRailLayoutKeepsFourRowsPerSection(t *testing.T) {
	// Blanks fit, the floors fit: each long section keeps four rows (two
	// items plus both arrows) and the spare row goes round-robin.
	b, blanks, compact := railLayout(railFixedRows+9, [railSections]int{20, 0, 0, 0, 20}, [railSections]bool{})
	if b != [railSections]int{5, 0, 0, 0, 4} || !blanks || compact {
		t.Fatalf("b=%v blanks=%v compact=%v", b, blanks, compact)
	}
	// A section that fits in fewer rows takes only what it needs.
	b, _, _ = railLayout(railFixedRows+10, [railSections]int{20, 0, 0, 2, 20}, [railSections]bool{})
	if b != [railSections]int{4, 0, 0, 2, 4} {
		t.Fatalf("b=%v", b)
	}
}

func TestRailLayoutDropsBlanksFirst(t *testing.T) {
	n := [railSections]int{6, 19, 4, 0, 4}
	b, blanks, compact := railLayout(21, n, [railSections]bool{})
	if blanks || compact {
		t.Fatalf("blanks=%v compact=%v, want neither (headings only, four-row floors)", blanks, compact)
	}
	sum := 0
	for i, v := range b {
		if v < min(n[i], 4) {
			t.Fatalf("section %d budget %d, want at least %d", i, v, min(n[i], 4))
		}
		sum += v
	}
	if sum+len(railHeadings) > 21 {
		t.Fatalf("budgets %v need %d rows, have 21", b, sum+len(railHeadings))
	}
}

func TestRailLayoutCompact(t *testing.T) {
	n := [railSections]int{6, 19, 4, 0, 4}
	b, blanks, compact := railLayout(16, n, [railSections]bool{})
	if blanks || !compact {
		t.Fatalf("blanks=%v compact=%v, want compact without blanks", blanks, compact)
	}
	sum := 0
	for i, v := range b {
		if v < min(n[i], 2) {
			t.Fatalf("section %d budget %d, want at least %d", i, v, min(n[i], 2))
		}
		sum += v
	}
	if sum+len(railHeadings) > 16 {
		t.Fatalf("budgets %v need %d rows, have 16", b, sum+len(railHeadings))
	}
}

func TestRailLayoutTooShortStillFloors(t *testing.T) {
	// Below the compact floors the layout cannot fit; the rail's MaxHeight
	// clips. The floors are still what is asked for, never less.
	b, blanks, compact := railLayout(8, [railSections]int{20, 0, 0, 0, 20}, [railSections]bool{})
	if b != [railSections]int{2, 0, 0, 0, 2} || blanks || !compact {
		t.Fatalf("b=%v blanks=%v compact=%v", b, blanks, compact)
	}
}

func TestRailLayoutCollapsedGetsNothing(t *testing.T) {
	n := [railSections]int{3, 3, 3, 3, 3}
	b, _, _ := railLayout(40, n, [railSections]bool{secMemories: true})
	if b != [railSections]int{3, 0, 3, 3, 3} {
		t.Fatalf("b=%v", b)
	}
}

func TestRailWindowCompactCombinedArrow(t *testing.T) {
	// Budget 2: one item row and one row for both arrows.
	top, end := railWindow(19, 2, 0, 10, true)
	if top != 10 || end != 11 {
		t.Fatalf("top=%d end=%d, want 10 11", top, end)
	}
	// From the top: one item and a down-only arrow row.
	if top, end = railWindow(19, 2, 0, 0, true); top != 0 || end != 1 {
		t.Fatalf("top=%d end=%d, want 0 1", top, end)
	}
	// The last item: still one item and one (up-only) arrow row.
	if top, end = railWindow(19, 2, 0, 18, true); top != 18 || end != 19 {
		t.Fatalf("top=%d end=%d, want 18 19", top, end)
	}
}

func TestRailCompactArrowRow(t *testing.T) {
	f := newFixture(t)
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	var chans []bus.Channel
	for i := range 19 {
		chans = append(chans, bus.Channel{Name: fmt.Sprintf("memory/m%02d", i), Kind: "memory"})
	}
	f.setRailChannels(chans...)
	f.key("esc")
	f.key("home")
	for range 10 {
		f.key("down")
	}
	// Too short for four-row floors: compact arrows are forced.
	f.send(tea.WindowSizeMsg{Width: 80, Height: 12})
	lines := f.railLines()
	want := "▲" + fmt.Sprint(spans(f)[secMemories].top) + " ▼" + fmt.Sprint(19-spans(f)[secMemories].end) + " more"
	if !slices.Contains(lines, want) {
		t.Fatalf("want a combined arrow row %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

func TestRail24LinesShowsEverySection(t *testing.T) {
	f := newFixture(t)
	var chans []bus.Channel
	for i := range 6 {
		chans = append(chans, bus.Channel{Name: fmt.Sprintf("general/c%d", i), Kind: "ordinary"})
	}
	for i := range 19 {
		chans = append(chans, bus.Channel{Name: fmt.Sprintf("memory/m%02d", i), Kind: "memory"})
	}
	for i := range 4 {
		chans = append(chans, bus.Channel{Name: fmt.Sprintf("tasks/t%d", i), Kind: "memory"})
	}
	f.setRailChannels(chans...)
	sessions := []string{"s1", "s2", "s3", "s4", "s5"}
	for _, n := range sessions {
		f.m.sessionsSeen[n] = time.Now()
	}
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := f.railLines()
	if len(lines) > f.m.stream.Height+1 {
		t.Fatalf("rail is %d rows, stream+1 is %d", len(lines), f.m.stream.Height+1)
	}
	all := strings.Join(lines, "\n")
	// Items under each non-empty section: the row after its heading is not
	// another heading or blank.
	for sec, h := range railHeadings {
		i := slices.Index(lines, h)
		if i < 0 {
			t.Fatalf("heading %q missing:\n%s", h, all)
		}
		if sec == secTags {
			continue
		}
		if i+1 >= len(lines) || lines[i+1] == "" || slices.Contains(railHeadings[:], lines[i+1]) {
			t.Fatalf("no item under %q:\n%s", h, all)
		}
	}
	// The last session is reachable with down, selected and visible.
	f.key("esc")
	f.key("home")
	last := f.m.sessionNames()[len(f.m.sessionNames())-1]
	for range len(chans) + len(sessions) + 2 {
		if f.m.pane() == paneSessions && f.m.sessSel == len(f.m.sessionNames())-1 {
			break
		}
		f.key("down")
	}
	if f.m.pane() != paneSessions || f.m.sessionNames()[f.m.sessSel] != last {
		t.Fatalf("down did not reach %q: pane=%v sessSel=%d", last, f.m.pane(), f.m.sessSel)
	}
	lines = f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, last) }) {
		t.Fatalf("selected last session %q not visible:\n%s", last, strings.Join(lines, "\n"))
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

func TestRailShowsEverySectionHeading(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	prev := -1
	for _, h := range railHeadings {
		i := slices.Index(lines, h)
		if i <= prev {
			t.Fatalf("want heading %q after the previous one:\n%s", h, strings.Join(lines, "\n"))
		}
		prev = i
	}
	// An empty tags section is its heading alone, with no hint row.
	if i := slices.Index(lines, "tags"); lines[i+1] != "" || strings.Contains(strings.Join(lines, "\n"), "press t") {
		t.Fatalf("want the tags heading with no rows under it:\n%s", strings.Join(lines, "\n"))
	}
	f.followTags(t, []string{"docs"})
	lines = f.railLines()
	tags := slices.Index(lines, "tags")
	if tags < 0 || !strings.Contains(lines[tags+1], "docs") || lines[tags+2] != "" {
		t.Fatalf("want the tag set under its heading:\n%s", strings.Join(lines, "\n"))
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
	nc := spans(f)[secChannels]
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
	s := spans(f)[secChannels]
	if s.top == 0 || f.m.sel >= s.end || s.end-s.top < 2 {
		t.Fatalf("selection not scrolled into view: %+v sel=%d", s, f.m.sel)
	}
	lines = f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▲") }) {
		t.Fatalf("want an up arrow once scrolled:\n%s", strings.Join(lines, "\n"))
	}
	// Down through the rest reaches the last channel, then leaves the section
	// for the first memory channel.
	for f.m.sel < s.n-1 {
		f.key("down")
	}
	if s = spans(f)[secChannels]; s.end != s.n {
		t.Fatalf("last channel not shown: %+v", s)
	}
	if lines = f.railLines(); slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "▼") }) {
		t.Fatalf("no down arrow at the end:\n%s", strings.Join(lines, "\n"))
	}
	f.key("down")
	if f.m.selName() != "dev-notes" || f.m.pane() != paneChannels {
		t.Fatalf("down from the last channel should enter memories, sel=%q pane=%v", f.m.selName(), f.m.pane())
	}
	// Up returns to the last channel, still shown.
	f.key("up")
	if s = spans(f)[secChannels]; f.m.sel != s.n-1 || s.end != s.n {
		t.Fatalf("sel=%d span=%+v", f.m.sel, s)
	}
	// Up scrolls back one at a time to the top.
	for f.m.sel > 0 {
		top := spans(f)[secChannels].top
		f.key("up")
		if s = spans(f)[secChannels]; f.m.sel < s.top || top-s.top > 1 {
			t.Fatalf("sel=%d span=%+v (was top %d)", f.m.sel, s, top)
		}
	}
	if s = spans(f)[secChannels]; s.top != 0 {
		t.Fatalf("back at the first channel, want top 0: %+v", s)
	}
}

func spans(f *fixture) [railSections]railSpan {
	s, _, _ := f.m.railSpans()
	return s
}

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

func TestChannelSection(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		want       int
	}{
		{"general", "ordinary", secChannels},
		{"general/x", "ordinary", secChannels},
		{"ops/x", "ordinary", secChannels},
		{"memory", "memory", secMemories},
		{"memory/tmi", "memory", secMemories},
		{"tasks/aws", "memory", secTasks},
		{"tasks", "memory", secTasks},
		{tagPanePrefix + "a,b", "tags", secTags},
	} {
		if got := channelSection(bus.Channel{Name: c.name, Kind: c.kind}); got != c.want {
			t.Errorf("channelSection(%q, %s) = %d, want %d", c.name, c.kind, got, c.want)
		}
	}
}

func TestShortName(t *testing.T) {
	for _, c := range []struct {
		name string
		sec  int
		want string
	}{
		{"general/agentbus", secChannels, "agentbus"},
		{"general", secChannels, "general"},
		{"ops/deploys", secChannels, "ops/deploys"},
		{"memory/tmi", secMemories, "tmi"},
		{"memory", secMemories, "memory"},
		{"tasks/aws-eip-logging", secTasks, "aws-eip-logging"},
		{"tasks", secTasks, "tasks"},
		{"memory/x", secChannels, "memory/x"},
	} {
		if got := shortName(c.name, c.sec); got != c.want {
			t.Errorf("shortName(%q, %d) = %q, want %q", c.name, c.sec, got, c.want)
		}
	}
}

// setRailChannels replaces the rail's channels with chans (name, kind).
func (f *fixture) setRailChannels(chans ...bus.Channel) {
	f.m.setChannels(chans, nil, "")
}

func TestShortNameCollision(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "agentbus", Kind: "ordinary"},
		bus.Channel{Name: "general/agentbus", Kind: "ordinary"},
		bus.Channel{Name: "tmi", Kind: "memory"},
		bus.Channel{Name: "memory/tmi", Kind: "memory"},
		bus.Channel{Name: "memory/solo", Kind: "memory"},
	)
	got := f.m.railLabels()
	for name, want := range map[string]string{
		"agentbus": "agentbus", "general/agentbus": "general/agentbus",
		"tmi": "tmi", "memory/tmi": "memory/tmi", "memory/solo": "solo",
	} {
		if got[name] != want {
			t.Errorf("label(%q) = %q, want %q", name, got[name], want)
		}
	}
}

func TestRailSectionsOrder(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "tasks/aws-eip-logging", Kind: "memory"},
		bus.Channel{Name: "memory/tmi", Kind: "memory"},
		bus.Channel{Name: "general/agentbus", Kind: "ordinary"},
		bus.Channel{Name: "memory", Kind: "memory"},
		bus.Channel{Name: "general", Kind: "ordinary"},
	)
	var lines []string
	for _, l := range f.railLines() {
		if l != "" {
			lines = append(lines, l)
		}
	}
	want := []string{"channels", "general", "agentbus", "memories", "memory", "tmi", "tasks", "aws-eip-logging", "tags", "sessions"}
	if len(lines) < len(want) {
		t.Fatalf("rail too short:\n%s", strings.Join(lines, "\n"))
	}
	for i, w := range want {
		heading := slices.Contains(railHeadings[:], w)
		if (heading && lines[i] != w) || (!heading && !strings.Contains(lines[i], w)) {
			t.Fatalf("rail line %d = %q, want %q:\n%s", i, lines[i], w, strings.Join(lines, "\n"))
		}
		// Item rows show short names: a "/" would be a kind prefix left on.
		if !heading && strings.Contains(lines[i], "/") {
			t.Fatalf("rail line %d = %q still shows a prefix:\n%s", i, lines[i], strings.Join(lines, "\n"))
		}
	}
}

func TestEmptySectionHeadingOnly(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	i := slices.Index(lines, "tags")
	if i < 0 || i+2 >= len(lines) || lines[i+1] != "" || lines[i+2] != "sessions" {
		t.Fatalf("want the tags heading, a blank, then sessions:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "press t to create") {
		t.Fatalf("the tag hint is gone:\n%s", strings.Join(lines, "\n"))
	}
}
