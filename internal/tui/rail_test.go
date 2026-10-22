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
	b, blanks, compact := railLayout(railFixed(true)+9, [railSections]int{20, 0, 0, 0, 20}, [railSections]bool{})
	if b != [railSections]int{5, 0, 0, 0, 4} || !blanks || compact {
		t.Fatalf("b=%v blanks=%v compact=%v", b, blanks, compact)
	}
	// A section that fits in fewer rows takes only what it needs.
	b, _, _ = railLayout(railFixed(true)+10, [railSections]int{20, 0, 0, 2, 20}, [railSections]bool{})
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
	// Two-item floors {2,2,2,0,2} fit (8 + 5 headings <= 16); the three spare
	// rows go round-robin to the sections still short: channels, memories,
	// tasks. Tags is empty and sessions is already full at its floor.
	if want := [railSections]int{3, 3, 3, 0, 2}; b != want {
		t.Fatalf("budgets %v, want %v", b, want)
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
		f.key("down") // the channels section is empty: memories heading, then m00..m08
	}
	if got := f.m.selName(); got != "memory/m08" {
		t.Fatalf("setup: selected %q, want memory/m08", got)
	}
	// Too short for four-row floors: compact arrows are forced.
	f.send(tea.WindowSizeMsg{Width: 80, Height: 12})
	if _, blanks, compact := f.m.railSpans(); blanks || !compact {
		t.Fatalf("blanks=%v compact=%v, want compact without blanks", blanks, compact)
	}
	// Literal counts: m08 is the only item shown, so 8 are hidden above it
	// and 10 below.
	lines := f.railLines()
	if want := "▲8 ▼10 more"; !slices.Contains(lines, want) {
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
		i := slices.Index(lines, headLine(h))
		if i < 0 {
			t.Fatalf("heading %q missing:\n%s", h, all)
		}
		if sec == secTags {
			continue
		}
		if i+1 >= len(lines) || lines[i+1] == "" || slices.ContainsFunc(railHeadings[:], func(h string) bool { return lines[i+1] == headLine(h) }) {
			t.Fatalf("no item under %q:\n%s", h, all)
		}
	}
	// The last session is reachable with down, selected and visible.
	f.key("esc")
	f.key("home")
	last := f.m.sessionNames()[len(f.m.sessionNames())-1]
	for range len(f.m.railPositions()) + 1 {
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

// isArrowRow reports whether the trimmed rail row l is a scroll arrow row
// ("▼ 3 more"), not a section heading, which starts with the same glyph.
func isArrowRow(l, arrow string) bool {
	return strings.HasPrefix(l, arrow) && strings.HasSuffix(l, "more")
}

// headLine is the rail row of the expanded, unselected section heading h.
func headLine(h string) string { return markOpen + " " + h }

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
		i := slices.Index(lines, headLine(h))
		if i <= prev {
			t.Fatalf("want heading %q after the previous one:\n%s", h, strings.Join(lines, "\n"))
		}
		prev = i
	}
	// An empty tags section is its heading alone, with no hint row.
	if i := slices.Index(lines, headLine("tags")); lines[i+1] != "" || strings.Contains(strings.Join(lines, "\n"), "press t") {
		t.Fatalf("want the tags heading with no rows under it:\n%s", strings.Join(lines, "\n"))
	}
	f.followTags(t, []string{"docs"})
	lines = f.railLines()
	tags := slices.Index(lines, headLine("tags"))
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
	f.key("down") // off the channels heading onto the first channel
	for range len(f.m.channels) {
		if f.m.sel <= 0 {
			break
		}
		f.key("up")
	}
	nc := spans(f)[secChannels]
	if nc.top != 0 || nc.end >= nc.n {
		t.Fatalf("want the list cut from the top, got %+v", nc)
	}
	lines := f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▼") }) {
		t.Fatalf("want a down arrow:\n%s", strings.Join(lines, "\n"))
	}
	if slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▲") }) {
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
	if !slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▲") }) {
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
	if lines = f.railLines(); slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▼") }) {
		t.Fatalf("no down arrow at the end:\n%s", strings.Join(lines, "\n"))
	}
	f.key("down")
	if f.m.headSel != secMemories || f.m.sel != s.n-1 {
		t.Fatalf("down from the last channel should select the memories heading, headSel=%d sel=%d", f.m.headSel, f.m.sel)
	}
	f.key("down")
	if f.m.selName() != "dev-notes" || f.m.pane() != paneChannels {
		t.Fatalf("down from the memories heading should enter memories, sel=%q pane=%v", f.m.selName(), f.m.pane())
	}
	// Up returns through the heading to the last channel, still shown.
	f.key("up")
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
	for range len(f.m.sessionNames()) {
		if f.m.pane() == paneSessions && f.m.sessionNames()[f.m.sessSel] == name {
			break
		}
		f.key("down")
	}
	if f.m.pane() == paneSessions && f.m.sessionNames()[f.m.sessSel] != name {
		t.Fatalf("down never reached session %q, at %q", name, f.m.sessionNames()[f.m.sessSel])
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

// isHeadingRow reports whether the stripped rail row is a section heading
// (collapsed or expanded). A collapsed heading starts with markSel like a
// selected item row does, so the two are told apart by the heading name.
func isHeadingRow(plain string) bool {
	return slices.ContainsFunc(railHeadings[:], func(h string) bool {
		return strings.HasPrefix(plain, markSel+" "+h) || strings.HasPrefix(plain, markOpen+" "+h)
	})
}

// Every selectable row type (chat, memory, task channel, tag set, live
// session, idle session, and each section heading, expanded and collapsed),
// under both icon sets, at the narrowest and a normal rail width, with and
// without rail focus: the selected row renders as exactly one row of exactly
// the rail's width, with no text drawn in its background color, and reads as
// its unselected row with markSel in place of the leading space. Spec
// 2026-10-21 section 5.
func TestSelectedRowsStayOneLine(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev); setIcons(emojiIcons) })
	for _, icons := range []string{"emoji", "nerdfont"} {
		for _, rail := range []int{12, 24} {
			for _, collapsed := range [][]int{nil, {secMemories, secTasks}} {
				name := fmt.Sprintf("%s/%d/collapsed=%v", icons, rail, collapsed)
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
				for _, sec := range collapsed {
					f.m.toggleSection(sec, true)
				}
				f.key("esc")
				f.key("home")
				width := f.m.railWidth()
				plainRows := func(m Model) []string {
					rows := strings.Split(m.renderRails(), "\n")
					for i := range rows {
						rows[i] = ansi.Strip(rows[i])
					}
					return rows
				}
				wantRows := len(plainRows(f.m))
				wantNames := map[railPos]bool{}
				for _, p := range f.m.railPositions() {
					wantNames[p] = false
				}
				for step := 0; ; step++ {
					if step > len(wantNames)+1 {
						t.Fatalf("%s: down never stopped", name)
					}
					pos := f.m.railPos()
					sel := f.m.selName()
					wantNames[pos] = true
					for _, focused := range []bool{true, false} {
						f.m.mode = modeNormal
						if !focused {
							f.m.mode = modeInsert
						}
						raw := strings.Split(f.m.renderRails(), "\n")
						rows := plainRows(f.m)
						if len(rows) != wantRows {
							t.Fatalf("%s/%v/%+v: rail has %d rows selected, %d at the start", name, focused, pos, len(rows), wantRows)
						}
						idx := -1
						n := 0
						for i, r := range rows {
							switch {
							case pos.item < 0 && isHeadingRow(r) && strings.Contains(r, railHeadings[pos.sec]):
								idx, n = i, n+1
							case pos.item >= 0 && strings.HasPrefix(r, markSel) && !isHeadingRow(r):
								idx, n = i, n+1
							}
						}
						if n != 1 {
							t.Fatalf("%s/%+v/%q/focused=%v: %d selected rows:\n%s", name, pos, sel, focused, n, strings.Join(rows, "\n"))
						}
						row := raw[idx]
						if w := ansi.StringWidth(row); w != width {
							t.Errorf("%s/%+v/focused=%v: selected row %q is %d wide, rail is %d", name, pos, focused, rows[idx], w, width)
						}
						if hidden := invisibleText(row); len(hidden) > 0 {
							t.Errorf("%s/%+v/focused=%v: %q drawn in its background color: %q", name, pos, focused, hidden, row)
						}
						// The same row unselected: select the first heading
						// (or, when that is the row, none).
						other := f.m
						other.headSel = secSessions
						if pos.item < 0 && pos.sec == secSessions {
							other.headSel = secChannels
						}
						base := plainRows(other)[idx]
						got := strings.TrimRight(rows[idx], " ")
						want := strings.TrimRight(base, " ")
						if pos.item >= 0 {
							want = markSel + strings.TrimPrefix(want, " ")
						}
						if got != want {
							t.Errorf("%s/%+v/focused=%v: selected row %q, want the unselected row %q with the mark", name, pos, focused, got, want)
						}
					}
					f.m.mode = modeNormal
					f.key("down")
					if f.m.railPos() == pos {
						break
					}
				}
				for p, seen := range wantNames {
					if !seen {
						t.Errorf("%s: never selected %+v", name, p)
					}
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
		if (heading && lines[i] != headLine(w)) || (!heading && !strings.Contains(lines[i], w)) {
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
	i := slices.Index(lines, headLine("tags"))
	if i < 0 || i+2 >= len(lines) || lines[i+1] != "" || lines[i+2] != headLine("sessions") {
		t.Fatalf("want the tags heading, a blank, then sessions:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "press t to create") {
		t.Fatalf("the tag hint is gone:\n%s", strings.Join(lines, "\n"))
	}
}

// toHeading walks the rail selection onto section sec's heading from the top.
func (f *fixture) toHeading(t *testing.T, sec int) {
	t.Helper()
	f.key("esc")
	f.key("home")
	for range len(f.m.railPositions()) {
		if f.m.headSel == sec {
			return
		}
		f.key("down")
	}
	t.Fatalf("down never reached the %s heading, at %+v", railHeadings[sec], f.m.railPos())
}

// sectionedFixture has an item in every section and live sessions, rail
// focused with the channels heading selected.
func sectionedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.followTags(t, []string{"docs"})
	f.key("esc")
	f.key("home")
	return f
}

func TestRailDownWalksHeadings(t *testing.T) {
	f := sectionedFixture(t)
	want := f.m.railPositions()
	// Five headings, every channel (tags included), every session.
	if n := 5 + len(f.m.channels) + len(f.m.sessionNames()); len(want) != n {
		t.Fatalf("railPositions has %d stops, want %d: %v", len(want), n, want)
	}
	var got []railPos
	for range len(want) + 2 {
		pos := f.m.railPos()
		if len(got) == 0 || got[len(got)-1] != pos {
			got = append(got, pos)
		}
		shown := f.m.selName()
		f.key("down")
		if pos.item < 0 && f.m.railPos().item < 0 && f.m.selName() != shown {
			t.Fatalf("moving between headings changed the stream: %q -> %q", shown, f.m.selName())
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("down visited %v, want %v", got, want)
	}
	if last := got[len(got)-1]; last.sec != secSessions || last.item != len(f.m.sessionNames())-1 || f.m.pane() != paneSessions {
		t.Fatalf("down ended on %+v pane=%v, want the last session", last, f.m.pane())
	}
	// A heading is selected without the stream following it: up from the
	// first item of a section lands on that heading with the stream unchanged.
	f.key("esc")
	f.moveTo(t, "general")
	f.key("down")
	if f.m.headSel != secMemories || f.m.selName() != "general" {
		t.Fatalf("down from the last channel: headSel=%d sel=%q, want the memories heading with the stream on general", f.m.headSel, f.m.selName())
	}
}

func TestHeadingLeftCollapsesRightExpands(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	f.key("left")
	if !f.m.collapsed[secMemories] || f.m.headSel != secMemories || f.m.pane() != paneChannels {
		t.Fatalf("left on a heading collapses it: collapsed=%v headSel=%d pane=%v", f.m.collapsed, f.m.headSel, f.m.pane())
	}
	f.key("left")
	if !f.m.collapsed[secMemories] {
		t.Fatal("left on a collapsed heading stays collapsed")
	}
	f.key("right")
	if f.m.collapsed[secMemories] {
		t.Fatalf("right on a heading expands it: %v", f.m.collapsed)
	}
	f.key("right")
	if f.m.collapsed[secMemories] {
		t.Fatal("right on an expanded heading stays expanded")
	}
	// On an item left keeps its stream meaning: it never collapses a section.
	f.key("down")
	f.key("left")
	if f.m.headSel >= 0 || f.m.collapsed != [railSections]bool{} {
		t.Fatalf("left on an item must not collapse: headSel=%d collapsed=%v", f.m.headSel, f.m.collapsed)
	}
}

func TestHeadingEnterToggles(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secTasks)
	f.key("enter")
	if !f.m.collapsed[secTasks] || f.m.mode != modeNormal {
		t.Fatalf("enter collapses an expanded heading and does not compose: collapsed=%v mode=%v", f.m.collapsed, f.m.mode)
	}
	f.key("enter")
	if f.m.collapsed[secTasks] || f.m.mode != modeNormal {
		t.Fatalf("enter expands a collapsed heading: collapsed=%v mode=%v", f.m.collapsed, f.m.mode)
	}
}

func TestCollapsedHeadingSummary(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	cfg := testConfig(t)
	cfg.TUIRailWidth = 30
	f := newFixtureWith(t, cfg)
	var chans []bus.Channel
	for i := range 19 {
		chans = append(chans, bus.Channel{Name: fmt.Sprintf("memory/m%02d", i), Kind: "memory"})
	}
	f.setRailChannels(chans...)
	f.key("esc")
	if !slices.Contains(f.railLines(), headLine("memories")) {
		t.Fatalf("expanded heading is %q:\n%s", headLine("memories"), strings.Join(f.railLines(), "\n"))
	}
	f.m.toggleSection(secMemories, true)
	if want := markSel + " memories (19)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
	for seq := int64(1); seq <= 3; seq++ {
		f.msgAt("memory/m04", "Sam", seq, 0, "hi")
	}
	if want := markSel + " memories (19, 3 unread)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
	if slices.ContainsFunc(f.railLines(), func(l string) bool { return strings.Contains(l, "m04") }) {
		t.Fatalf("a collapsed section draws no items:\n%s", strings.Join(f.railLines(), "\n"))
	}
	cfg.IconMap = map[string]string{"collapsed": "X"}
	LoadIcons(cfg, io.Discard)
	if want := "X memories (19, 3 unread)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("icon_map override: want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
}

func TestCollapsedSectionRowsGoElsewhere(t *testing.T) {
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
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		f.m.sessionsSeen[n] = time.Now()
	}
	shown := func() (n int) {
		for sec, s := range spans(f) {
			if sec != secMemories {
				n += s.end - s.top
			}
		}
		return n
	}
	// At 80x24 the four-row floors fill the rail exactly, so the freed
	// memory rows come back as the blank separators, not as items.
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	before := shown()
	if _, blanks, _ := f.m.railSpans(); blanks {
		t.Fatal("setup: 80x24 with every section expanded has no room for blank rows")
	}
	f.m.toggleSection(secMemories, true)
	if _, blanks, _ := f.m.railSpans(); !blanks || shown() < before {
		t.Fatalf("collapsing memories at 80x24 must restore the blank rows without losing items (was %d, now %d)", before, shown())
	}
	// With a few spare rows they become items of the other sections.
	f.m.toggleSection(secMemories, false)
	f.send(tea.WindowSizeMsg{Width: 80, Height: 30})
	before = shown()
	f.m.toggleSection(secMemories, true)
	if after := shown(); after <= before {
		t.Fatalf("collapsing memories at 80x30 shows %d other items, was %d; want more", after, before)
	}
	if lines := f.railLines(); len(lines) > f.m.stream.Height+1 {
		t.Fatalf("rail is %d rows, stream+1 is %d", len(lines), f.m.stream.Height+1)
	}
}

func TestCollapseShownSectionMovesToHeading(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "general", Kind: "ordinary"},
		bus.Channel{Name: "memory/tmi", Kind: "memory"},
	)
	f.key("esc")
	f.moveTo(t, "memory/tmi")
	f.m.toggleSection(secMemories, true)
	if f.m.headSel != secMemories {
		t.Fatalf("headSel=%d, want the memories heading", f.m.headSel)
	}
	if h := ansi.Strip(f.m.renderHeader()); !strings.Contains(h, "memory/tmi") {
		t.Fatalf("header %q must still name memory/tmi", h)
	}
	if f.m.selName() != "memory/tmi" {
		t.Fatalf("the stream must keep memory/tmi, got %q", f.m.selName())
	}
	// Collapsing a section that does not hold the shown item leaves the
	// selection alone.
	f.moveTo(t, "general")
	f.m.toggleSection(secTasks, true)
	if f.m.headSel >= 0 {
		t.Fatalf("headSel=%d after collapsing an unrelated section", f.m.headSel)
	}
}

func TestHeadingSelectedSingleHighlight(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	probe := sgrPrefix(lipgloss.NewStyle().Background(f.m.theme.SelBG(true)))
	if probe == "" {
		t.Skip("theme selection color has no escape under this profile")
	}
	var marked []string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(l, probe) {
			marked = append(marked, ansi.Strip(l))
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "memories") {
		t.Fatalf("want exactly the memories heading highlighted, got %q", marked)
	}
}

func TestHeadingSurvivesRefresh(t *testing.T) {
	f := newFixture(t)
	chans := []bus.Channel{
		{Name: "general", Kind: "ordinary"},
		{Name: "memory/tmi", Kind: "memory"},
		{Name: "tasks/a", Kind: "memory"},
	}
	f.setRailChannels(chans...)
	f.toHeading(t, secTasks)
	f.key("left")
	f.toHeading(t, secMemories)
	f.setRailChannels(append(chans, bus.Channel{Name: "tasks/b", Kind: "memory"})...)
	if !f.m.collapsed[secTasks] || f.m.headSel != secMemories {
		t.Fatalf("refresh must keep collapsed=%v and headSel=%d, got collapsed=%v headSel=%d", [railSections]bool{secTasks: true}, secMemories, f.m.collapsed, f.m.headSel)
	}
	if want := markSel + " tasks (2)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("a new task list must not expand tasks, want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
}

func TestHomeSelectsChannelsHeading(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secTags)
	f.key("down")
	f.key("home")
	if f.m.headSel != secChannels || f.m.pane() != paneChannels {
		t.Fatalf("home: headSel=%d pane=%v, want the channels heading", f.m.headSel, f.m.pane())
	}
	// From a session the stream stays on the session.
	f.toSessions()
	shown := f.m.selName()
	f.key("home")
	if f.m.headSel != secChannels || f.m.selName() != shown || f.m.sessSel < 0 {
		t.Fatalf("home from a session: headSel=%d sel=%q (was %q) sessSel=%d", f.m.headSel, f.m.selName(), shown, f.m.sessSel)
	}
}

func TestTabReturnsToHeading(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	shown := f.m.selName() // the last channel the walk passed
	f.agentSend(t, shown, "hello")
	f.receive(t)
	f.key("tab")
	if f.m.pane() != paneStream || f.m.headSel != secMemories {
		t.Fatalf("tab from a heading goes to the stream: pane=%v headSel=%d", f.m.pane(), f.m.headSel)
	}
	f.key("tab") // compose
	f.key("tab") // rail
	if f.m.pane() != paneChannels || f.m.headSel != secMemories || f.m.selName() != shown {
		t.Fatalf("tab returns to the heading: pane=%v headSel=%d sel=%q, want %q", f.m.pane(), f.m.headSel, f.m.selName(), shown)
	}
	// With a session shown the heading lap must not drop the session.
	f.toSessions()
	f.key("up")
	if f.m.headSel != secSessions {
		t.Fatalf("setup: headSel=%d", f.m.headSel)
	}
	shown = f.m.selName()
	f.key("tab")
	f.key("shift+tab")
	if f.m.pane() != paneChannels || f.m.headSel != secSessions || f.m.selName() != shown || f.m.sessSel < 0 {
		t.Fatalf("lap from the sessions heading: pane=%v headSel=%d sel=%q (was %q) sessSel=%d", f.m.pane(), f.m.headSel, f.m.selName(), shown, f.m.sessSel)
	}
}

func TestCollapseNotPersisted(t *testing.T) {
	cfg := testConfig(t)
	f := newFixtureWith(t, cfg)
	f.toHeading(t, secMemories)
	f.key("left")
	if !f.m.collapsed[secMemories] {
		t.Fatal("setup: memories not collapsed")
	}
	m := New(f.c, f.m.theme)
	if m.collapsed != [railSections]bool{} || m.headSel != -1 {
		t.Fatalf("a new Model must start expanded with no heading: collapsed=%v headSel=%d", m.collapsed, m.headSel)
	}
}

// Deleting the last row of a section whose successor is collapsed repoints
// m.sel at a hidden row; the selection must land on the collapsed heading,
// with exactly one rail row highlighted.
func TestDeleteLastChannelBeforeCollapsedSection(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := sectionedFixture(t)
	probe := sgrPrefix(lipgloss.NewStyle().Background(f.m.theme.SelBG(true)))
	if probe == "" {
		t.Skip("theme selection color has no escape under this profile")
	}
	f.m.toggleSection(secTags, true)
	idx := slices.IndexFunc(f.m.channels, func(c bus.Channel) bool { return c.Name == "tasks/work" })
	if idx < 0 || idx+1 >= len(f.m.channels) || channelSection(f.m.channels[idx+1]) != secTags {
		t.Fatalf("fixture: tasks/work must be the last tasks row followed by tags, channels=%v", f.m.channels)
	}
	f.m.selectChannel(idx)
	f.key("d")
	f.key("y")
	if f.m.headSel != secTags {
		t.Fatalf("headSel = %d, want the collapsed tags heading %d", f.m.headSel, secTags)
	}
	var marked []string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(l, probe) {
			marked = append(marked, ansi.Strip(l))
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "tags") {
		t.Fatalf("want exactly the tags heading highlighted, got %q", marked)
	}
}

// When the last session ages out the selection returns to the channel the
// list last held; if that channel's section was collapsed meanwhile, the
// selection must land on the collapsed heading, with exactly one rail row
// highlighted.
func TestLastSessionAgesOutIntoCollapsedSection(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := sectionedFixture(t)
	probe := sgrPrefix(lipgloss.NewStyle().Background(f.m.theme.SelBG(true)))
	if probe == "" {
		t.Skip("theme selection color has no escape under this profile")
	}
	f.toSessions()
	if f.m.pane() != paneSessions || f.m.sel < 0 {
		t.Fatalf("setup: pane=%v sel=%d", f.m.pane(), f.m.sel)
	}
	sec := channelSection(f.m.channels[f.m.sel])
	f.m.toggleSection(sec, true)
	if f.m.headSel != -1 {
		t.Fatalf("setup: collapsing a section the stream isn't showing must not move the selection, headSel=%d", f.m.headSel)
	}
	for name := range f.m.sessionsSeen {
		f.m.sessionsSeen[name] = time.Now().Add(-2 * idleSessionTTL)
	}
	// Keep the followed tag set: dropping it would move the selection out of
	// the tags section on its own.
	f.send(statusMsg{st: bus.Status{Channels: f.m.status.Channels}, tags: [][]string{{"docs"}}})
	if f.m.sessSel != -1 || f.m.headSel != sec {
		t.Fatalf("sessSel=%d headSel=%d, want -1 and the collapsed heading %d", f.m.sessSel, f.m.headSel, sec)
	}
	var marked []string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(l, probe) {
			marked = append(marked, ansi.Strip(l))
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], railHeadings[sec]) {
		t.Fatalf("want exactly the collapsed heading highlighted, got %q", marked)
	}
}
