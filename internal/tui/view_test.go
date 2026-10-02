package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
	"github.com/muesli/termenv"
)

func TestViewShowsRailsStreamComposeAndStatus(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.run(f.m.statusCmd())
	// Wide enough that the version prefix (#15) doesn't truncate the normal-
	// mode key hints this test checks for below; truncation itself is
	// TestStatusBarShowsVersion's job.
	f.m.width = 140

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
	for _, want := range []string{"? help", "/ search", "h health", "q quit"} {
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

func TestStatusBarShowsVersion(t *testing.T) {
	f := newFixture(t)
	bar := ansi.Strip(f.m.renderStatusBar())
	if !strings.HasPrefix(bar, "agentbus v"+mcpserver.Version+"  db ") {
		t.Fatalf("status bar: %q", bar)
	}
	for _, w := range []int{60, 40} {
		f.m.width = w
		if got := lipgloss.Width(f.m.renderStatusBar()); got > w {
			t.Fatalf("width %d: status bar is %d cols wide, must stay one row: %q", w, got, f.m.renderStatusBar())
		}
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

// setChannelLastActivity overwrites a channel's LastActivity (ADR 0013) in
// the rail's already-fetched status, so tests can force a stale task
// channel without waiting on the real clock.
func (f *fixture) setChannelLastActivity(t *testing.T, ch string, at time.Time) {
	t.Helper()
	for i, c := range f.m.channels {
		if c.Name == ch {
			f.m.channels[i].LastActivity = at.UnixMilli()
			return
		}
	}
	t.Fatalf("channel %q not found in rail", ch)
}

// TestTaskChannelIdleMarker covers the ADR 0013 rail marker: past
// task_idle_hours it shows, before it or with the setting disabled (0) it
// does not, and a non-task channel never gets one regardless of its
// (unused) LastActivity.
func TestTaskChannelIdleMarker(t *testing.T) {
	cfg := testConfig(t)
	cfg.TaskIdleHours = 1
	f := newFixtureWith(t, cfg)
	if _, err := f.ab.CreateChannel(f.sam, "tasks/work", "memory"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())

	f.setChannelLastActivity(t, "tasks/work", time.Now().Add(-2*time.Hour))
	if rail := f.m.renderRails(); !strings.Contains(rail, iconIdle) {
		t.Fatalf("rail lacks idle marker past task_idle_hours:\n%s", rail)
	}

	f.setChannelLastActivity(t, "tasks/work", time.Now().Add(-30*time.Minute))
	if rail := f.m.renderRails(); strings.Contains(rail, iconIdle) {
		t.Fatalf("rail shows idle marker before task_idle_hours:\n%s", rail)
	}

	f.m.c.cfg.TaskIdleHours = 0
	f.setChannelLastActivity(t, "tasks/work", time.Now().Add(-999*time.Hour))
	if rail := f.m.renderRails(); strings.Contains(rail, iconIdle) {
		t.Fatalf("rail shows idle marker with task_idle_hours=0:\n%s", rail)
	}
	f.m.c.cfg.TaskIdleHours = 1
	f.setChannelLastActivity(t, "tasks/work", time.Now())

	f.setChannelLastActivity(t, "dev", time.Now().Add(-999*time.Hour))
	if rail := f.m.renderRails(); strings.Contains(rail, iconIdle) {
		t.Fatalf("rail shows idle marker on a non-task channel:\n%s", rail)
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
	if got := fmtBytes(264 << 10); got != "264 KiB" {
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
	f.key("shift+tab") // compose -> stream directly: cursor lands on the last message
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, "Sam") || strings.Count(first, "44m") < 2 {
		t.Fatalf("segments after a reset lost the selection background: %q", first)
	}
	rail := strings.SplitN(f.m.renderRails(), "\n", 3)[1]
	if !strings.Contains(rail, markSel) || !strings.Contains(rail, "100m") || strings.Contains(rail, "44m") {
		t.Fatalf("selected channel row must keep a background, the inactive one while the stream has focus: %q", rail)
	}
	if f.m.theme.Sel == f.m.theme.Dim {
		t.Fatal("selection background must differ from the dim text color")
	}
}

// TestRailSelectionUsesInactiveColorWithoutFocus (ADR 0011 section 3): the
// rail's selected channel row is on the selection color (blue, 44m) while
// the rail has focus and on selection_inactive (brightblack, 100m) while
// the stream does; the stream cursor stays on selection. Same for the
// sessions pane's selected row.
func TestRailSelectionUsesInactiveColorWithoutFocus(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.key("esc") // compose -> channel list: the rail has focus
	selectedRow := func() string {
		for _, l := range strings.Split(f.m.renderRails(), "\n") {
			if strings.Contains(l, markSel) {
				return l
			}
		}
		return ""
	}
	if row := selectedRow(); !strings.Contains(row, "44m") || strings.Contains(row, "100m") {
		t.Fatalf("focused rail row must use selection: %q", row)
	}
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	if row := selectedRow(); !strings.Contains(row, "100m") || strings.Contains(row, "44m") {
		t.Fatalf("unfocused rail row must use selection_inactive: %q", row)
	}
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, "44m") {
		t.Fatalf("stream cursor keeps selection: %q", first)
	}
	f.key("home")
	f.toSessions()
	if f.m.pane() != paneSessions {
		t.Fatalf("pane = %v, want sessions", f.m.pane())
	}
	if row := selectedRow(); !strings.Contains(row, "44m") {
		t.Fatalf("focused session row must use selection: %q", row)
	}
	f.key("tab")
	if row := selectedRow(); !strings.Contains(row, "100m") || strings.Contains(row, "44m") {
		t.Fatalf("unfocused session row must use selection_inactive: %q", row)
	}
}

// TestRailSelectionTextColorOnFocus: a focused rail's selected row renders
// its whole text in selection_text (black, 30m), replacing the per-segment
// colors (cyan agent fg, 36m); an unfocused selection keeps the original
// colors.
func TestRailSelectionTextColorOnFocus(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.agentSend(t, "dev", "hello from sam")
	f.receive(t)
	f.key("esc") // compose -> channel list: the rail has focus
	selectedRow := func() string {
		for _, l := range strings.Split(f.m.renderRails(), "\n") {
			if strings.Contains(l, markSel) {
				return l
			}
		}
		return ""
	}
	if row := selectedRow(); !strings.Contains(row, "30m") || strings.Contains(row, "36m") {
		t.Fatalf("focused rail row must use selection_text, not the agent color: %q", row)
	}
	f.key("tab") // stream: rail loses focus
	if row := selectedRow(); !strings.Contains(row, "36m") || strings.Contains(row, "30m") {
		t.Fatalf("unfocused rail row must keep its original color: %q", row)
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
	f.key("shift+tab") // compose -> stream directly
	f.key("up")
	f.key("right") // A has no body: shows the reply
	f.key("down")
	f.key("right") // the reply's cut first line is its body: open it
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

// The gear's erase and cursor-forward escapes must stay invisible to lipgloss,
// or the rail border drifts.
func TestIconAgentWidth(t *testing.T) {
	if w := lipgloss.Width(iconAgent); w != lipgloss.Width("⚙️ ") {
		t.Fatal(w)
	}
}

// The task-list icon is a Wide emoji like chat and memory, so it needs no
// CSI erase/cursor trick: rail, compose, search, and the task pane title all
// line up on the same column.
func TestIconTasksIsWideEmojiWithoutEscapes(t *testing.T) {
	if w := lipgloss.Width(iconTasks); w != lipgloss.Width(iconChat) {
		t.Fatalf("iconTasks width %d, want %d", w, lipgloss.Width(iconChat))
	}
	if strings.Contains(iconTasks, "\x1b[") {
		t.Fatalf("iconTasks must not need the CSI erase trick: %q", iconTasks)
	}
}

// The channel name carries its kind's color (agent for chat, memory for
// memory) in the rail and the stream header, as it does in the compose row.
func TestChannelNameColoredInRailAndHeader(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	ch := f.m.selected()
	if ch == nil {
		t.Fatal("no selected channel")
	}
	st := f.m.chanStyle(*ch)
	if want := st.Render(iconChat + ch.Name); !strings.Contains(f.m.renderCompose(), want) {
		t.Fatalf("compose lost the channel color: %q", f.m.renderCompose())
	}
	// The selected rail row re-applies the background after each reset, so
	// compare on the color's opening sequence plus the name.
	open, _, _ := strings.Cut(st.Render("\x00"), "\x00")
	if open == "" {
		t.Fatal("channel style renders no color")
	}
	if rail := f.m.renderRails(); !strings.Contains(rail, open+iconChat+ch.Name) {
		t.Fatalf("rail channel name uncolored: %q", rail)
	}
	if h := f.m.renderHeader(); !strings.Contains(h, open+ch.Name) {
		t.Fatalf("header channel name uncolored: %q", h)
	}
}

// A memory channel's name carries the memory color, not the chat color, and
// chanStyle assigns the same color to the same channel every time it's asked
// (no per-render randomness or drift).
func TestMemoryChannelColorIsStable(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	var notes bus.Channel
	for _, c := range f.m.channels {
		if c.Name == "dev-notes" {
			notes = c
		}
	}
	if notes.Name == "" {
		t.Fatal("dev-notes not in the channel list")
	}
	st := f.m.chanStyle(notes)
	open, _, _ := strings.Cut(st.Render("\x00"), "\x00")
	if open == "" {
		t.Fatal("memory channel style renders no color")
	}
	if open == f.m.chanStyle(*f.m.selected()).Render("\x00") {
		t.Fatal("memory and chat channels must not share a color")
	}
	if got, _, _ := strings.Cut(f.m.chanStyle(notes).Render("\x00"), "\x00"); got != open {
		t.Fatalf("chanStyle must assign the same channel the same color every call: %q vs %q", got, open)
	}
	if rail := f.m.renderRails(); !strings.Contains(rail, open+iconMem+"dev-notes") {
		t.Fatalf("memory channel uncolored in rail: %q", rail)
	}
}

// A live session's identity name is colored the same way a DM inbox's label
// would be: the TUI's own row in the user color, everyone else's in the
// agent color.
func TestSessionRowColorsMatchIdentity(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.run(f.m.statusCmd()) // learn Sam's and eric's (the TUI's own) live sessions
	agentOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Agent).Render("\x00"), "\x00")
	userOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.User).Render("\x00"), "\x00")
	rail := f.m.renderRails()
	var samRow, ericRow string
	for _, l := range strings.Split(rail, "\n") {
		switch {
		case strings.Contains(l, "Sam"):
			samRow = l
		case strings.Contains(l, "eric"):
			ericRow = l
		}
	}
	if !strings.Contains(samRow, agentOpen+"Sam") {
		t.Fatalf("another identity's session row must use the agent color: %q", samRow)
	}
	if !strings.Contains(ericRow, userOpen+"eric") {
		t.Fatalf("the TUI's own session row must use the user color: %q", ericRow)
	}
}

// msgAt injects one already-loaded message so a header test controls every
// field (sender, channel, time, tags) without a bus round trip.
func (f *fixture) msgAt(ch, sender string, seq, createdAt int64, content string, tags ...string) {
	f.m.addMessages(ch, []bus.Message{{Seq: seq, Channel: ch, Sender: sender, CreatedAt: createdAt, Content: content, Tags: tags}})
	f.m.refreshStream()
}

func TestStampTodayAndPast(t *testing.T) {
	if got := stamp(time.Now().UnixMilli()); !strings.HasPrefix(got, "(today)    ") || len([]rune(got)) != 19 {
		t.Fatalf("today: %q", got)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if got := stamp(past); got != "2026-01-02 03:04:05" {
		t.Fatalf("past: %q", got)
	}
}

func TestHeaderShowsSenderArrowChannelAndDM(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "hello")
	f.receive(t)
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	want := ansi.Strip(iconAgent) + "Sam"
	if !strings.Contains(first, want) || !strings.Contains(first, "(today)") {
		t.Fatalf("channel header %q lacks %q", first, want)
	}
	if strings.Contains(first, strings.TrimSpace(iconArrow)) || strings.Contains(first, "dev") {
		t.Fatalf("own-channel header must drop the target: %q", first)
	}
	lines := strings.Split(ansi.Strip(f.m.renderStream()), "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "hello") || strings.Contains(lines[0], "hello") {
		t.Fatalf("body must start on the next line: %q", lines)
	}

	f.run(f.m.statusCmd())
	f.key("esc")
	f.toSessions()
	// Select the DM session before the message arrives, same as "dev" above
	// (already selected by default): onBatch marks an already-selected
	// channel seen on arrival, so no "new" divider is inserted ahead of the
	// header this assertion reads as the first line.
	for i, n := range f.m.sessionNames() {
		if n == f.c.as {
			f.run(f.m.selectSession(i))
		}
	}
	f.agentSend(t, "dm/"+f.c.as, "psst")
	f.receive(t)
	first = ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	want = ansi.Strip(iconAgent) + "Sam" + iconArrow + iconUser + f.c.as
	if !strings.Contains(first, want) {
		t.Fatalf("DM header %q lacks %q", first, want)
	}
}

// header drops "→ channel" only when the message's channel is the one being
// viewed; a mixed pane (tags, search) and a DM recipient keep it.
func TestHeaderTargetOnlyOutsideOwnChannel(t *testing.T) {
	f := newFixture(t)
	st := lipgloss.NewStyle()
	mk := func(ch string) bus.Message {
		return bus.Message{Seq: 1, Channel: ch, Sender: "Sam", CreatedAt: time.Now().UnixMilli()}
	}
	arrow := strings.TrimSpace(iconArrow)
	for _, ch := range []string{"general", "general/agentbus", "memory/agentbus", "dev-notes", "dev"} {
		if got := ansi.Strip(f.m.header(mk(ch), ch, "", st, 200)); strings.Contains(got, arrow) || strings.Contains(got, ch) {
			t.Errorf("own-channel header for %q keeps the target: %q", ch, got)
		}
	}
	for _, viewed := range []string{tagPanePrefix + "bug", "general"} {
		if got := ansi.Strip(f.m.header(mk("dev"), viewed, "", st, 200)); !strings.Contains(got, iconArrow+iconChat+"dev") {
			t.Errorf("header viewed from %q lost the target: %q", viewed, got)
		}
	}
	if got := ansi.Strip(f.m.header(mk("dm/Eric"), "dm/Eric", "", st, 200)); !strings.Contains(got, iconArrow+ansi.Strip(iconAgent)+"Eric") {
		t.Errorf("DM header in its own pane lost the recipient: %q", got)
	}
}

func TestHeaderBusSender(t *testing.T) {
	f := newFixture(t)
	f.msgAt("dev", "", 9001, time.Now().UnixMilli(), "reclaimed")
	first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0])
	if !strings.Contains(first, ansi.Strip(iconAgent)+"bus") {
		t.Fatalf("empty sender renders as bus: %q", first)
	}
}

func TestSelectedRowSwapsDimToText(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.m.theme.Text = lipgloss.Color("7")
	f.agentSend(t, "dev", "one")
	f.agentSend(t, "dev", "two")
	f.receive(t)
	f.key("shift+tab") // cursor on "two"
	lines := strings.Split(f.m.renderStream(), "\n")
	stampOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Stamp).Render("\x00"), "\x00")
	textOpen, _, _ := strings.Cut(f.m.theme.Style(f.m.theme.Text).Render("\x00"), "\x00")
	if !strings.Contains(lines[0], stampOpen+"(today)") {
		t.Fatalf("unselected row keeps the timestamp color: %q", lines[0])
	}
	if !strings.Contains(lines[2], textOpen+"(today)") || strings.Contains(lines[2], stampOpen+"(today)") {
		t.Fatalf("selected row swaps dim text to the text color: %q", lines[2])
	}
	if f.m.cursorLine != 2 {
		t.Fatalf("cursorLine=%d, want 2 (header + body of the first row)", f.m.cursorLine)
	}
}

func TestTagChipsAndFallback(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	f := newFixture(t)
	f.msgAt("dev", "Sam", 9001, time.Now().UnixMilli(), "tagged", "release", "bug")
	chip := lipgloss.NewStyle().Background(f.m.theme.Tag).Foreground(lipgloss.Color("15"))
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, chip.Render(" release ")+" "+chip.Render(" bug ")) {
		t.Fatalf("chips missing: %q", first)
	}
	f.key("shift+tab") // selected row keeps the chip background
	if first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]; !strings.Contains(first, chip.Render(" release ")) {
		t.Fatalf("selected row lost chip background: %q", first)
	}
	f.m.theme.Tag = lipgloss.NoColor{}
	if first := ansi.Strip(strings.SplitN(f.m.renderStream(), "\n", 2)[0]); !strings.Contains(first, "#release #bug") {
		t.Fatalf("fallback: %q", first)
	}
}

func TestHeaderNeverWrapsOnNarrowPane(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 40)
	if _, err := f.ab.CreateChannel(f.sam, long, "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.m.width = 60
	f.m.layout()
	// Select the channel before the message arrives (same reason as the DM
	// case above): msgAt calls addMessages directly, and a first select on
	// an empty channel leaves divider at -1, so injecting afterward never
	// inserts a "new" divider ahead of the header line this test measures.
	for i, c := range f.m.channels {
		if c.Name == long {
			f.run(f.m.selectChannel(i))
		}
	}
	f.msgAt(long, "Sam", 9001, time.Now().UnixMilli(), "body", "a", "b", "c")
	lines := strings.Split(f.m.renderStream(), "\n")
	if w := lipgloss.Width(lines[0]); w > f.m.stream.Width {
		t.Fatalf("header wrapped or overflowed: width %d > %d: %q", w, f.m.stream.Width, lines[0])
	}
	// The own-channel header drops the recipient, so only a header viewed
	// from elsewhere (a tag pane) is long enough to need the cut.
	msg := bus.Message{Channel: long, Sender: "Sam", CreatedAt: time.Now().UnixMilli(), Tags: []string{"a", "b", "c"}}
	if w := lipgloss.Width(f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), f.m.stream.Width)); w > f.m.stream.Width {
		t.Fatalf("mixed-pane header overflowed: width %d > %d", w, f.m.stream.Width)
	}
	lines[0] = f.m.header(msg, tagPanePrefix+"a", "", lipgloss.NewStyle(), f.m.stream.Width)
	if s := ansi.Strip(lines[0]); !strings.Contains(s, "…") || strings.Contains(s, " a ") {
		t.Fatalf("tags go first, then the recipient is cut with …: %q", s)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "body") {
		t.Fatalf("body still on line 2: %q", lines)
	}
}

func TestChipTextContrastsWithTagColor(t *testing.T) {
	for bg, want := range map[string]string{"7": "0", "11": "0", "15": "0", "3": "0", "4": "15", "8": "15", "1": "15"} {
		if got := chipText(lipgloss.Color(bg)); got != lipgloss.Color(want) {
			t.Errorf("tag %s: chip text %v, want %s", bg, got, want)
		}
	}
}

func TestRailWidthClampsToTerminal(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ saved, width, rail int }{
		{0, 100, 20},   // unset: a fifth
		{30, 100, 30},  // honored
		{4, 100, 16},   // floor
		{90, 100, 59},  // leaves the messages pane 40 columns
		{30, 60, 19},   // a narrow terminal shrinks the saved width
		{30, 200, 30},  // and a wide one restores it
		{5000, 60, 19}, // out of range in a hand edit
	} {
		f.m.c.cfg.TUIRailWidth = tc.saved
		f.m.width = tc.width
		f.m.layout()
		if got := f.m.railWidth(); got != tc.rail {
			t.Errorf("saved %d, width %d: rail %d, want %d", tc.saved, tc.width, got, tc.rail)
		}
		if f.m.stream.Width != tc.width-tc.rail-1 {
			t.Errorf("saved %d, width %d: stream %d, want %d", tc.saved, tc.width, f.m.stream.Width, tc.width-tc.rail-1)
		}
	}
}

func TestRailKeysResizeAndPersist(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key(">")
	if got := f.m.railWidth(); got != 22 {
		t.Fatalf("after >: rail %d, want 22", got)
	}
	if f.m.stream.Width != 100-22-1 {
		t.Fatalf("stream %d did not reflow", f.m.stream.Width)
	}
	f.key("<")
	f.key("<")
	if got := f.m.railWidth(); got != 18 {
		t.Fatalf("after < <: rail %d, want 18", got)
	}
	c, _, err := config.Load(f.c.cfg.Path)
	if err != nil || c.TUIRailWidth != 18 {
		t.Fatalf("saved %d, err %v; want 18", c.TUIRailWidth, err)
	}
	// A restart reads the saved width back.
	if m := New(f.c, f.m.theme); m.c.cfg.TUIRailWidth != 18 {
		t.Fatalf("restart rail %d", m.c.cfg.TUIRailWidth)
	}
}

func TestRailKeysStopAtBounds(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.m.width = 60
	f.m.layout()
	f.key("<") // already at the floor: nothing changes, nothing is saved
	if f.m.c.cfg.TUIRailWidth != 0 {
		t.Fatalf("a no-op press saved %d", f.m.c.cfg.TUIRailWidth)
	}
	f.key(">") // 16 -> 18
	f.key(">") // 19 is the ceiling at 60 columns; the saved width keeps stepping
	f.key(">")
	if got := f.m.railWidth(); got != 19 || f.m.c.cfg.TUIRailWidth != 22 {
		t.Fatalf("rail %d, saved %d; want 19, 22", got, f.m.c.cfg.TUIRailWidth)
	}
	if f.m.stream.Width < streamMin {
		t.Fatalf("stream %d below the %d-column minimum", f.m.stream.Width, streamMin)
	}
	for range 20 {
		f.key("<")
	}
	if got := f.m.railWidth(); got != railMin {
		t.Fatalf("rail %d, want floor %d", got, railMin)
	}
}

// ADR 0006 item 7: < and > step the saved width, not the clamped one on
// screen, so a narrow terminal does not overwrite a wide saved setting.
// TestRailKeysFromASavedWidthBelowMin: a saved width under railMin steps
// from railMin, so < never saves a wider rail and > shows at once.
func TestRailKeysFromASavedWidthBelowMin(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.m.width = 200
	f.m.c.cfg.TUIRailWidth = 5
	f.m.layout()
	f.key("<")
	if f.m.c.cfg.TUIRailWidth != 5 {
		t.Fatalf("< from 5 saved %d, want 5 (no change)", f.m.c.cfg.TUIRailWidth)
	}
	f.key(">")
	if f.m.c.cfg.TUIRailWidth != railMin+2 || f.m.railWidth() != railMin+2 {
		t.Fatalf("> from 5: saved %d, shown %d; want %d", f.m.c.cfg.TUIRailWidth, f.m.railWidth(), railMin+2)
	}
}

func TestRailKeysStepFromTheSavedWidth(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.m.width = 60
	f.m.c.cfg.TUIRailWidth = 40 // shows as 19
	f.m.layout()
	f.key("<")
	if f.m.c.cfg.TUIRailWidth != 38 || f.m.railWidth() != 19 {
		t.Fatalf("after <: saved %d, shown %d; want 38, 19", f.m.c.cfg.TUIRailWidth, f.m.railWidth())
	}
	f.key(">")
	f.key(">")
	if f.m.c.cfg.TUIRailWidth != 42 || f.m.railWidth() != 19 {
		t.Fatalf("after > >: saved %d, shown %d; want 42, 19", f.m.c.cfg.TUIRailWidth, f.m.railWidth())
	}
	c, _, err := config.Load(f.c.cfg.Path)
	if err != nil || c.TUIRailWidth != 42 {
		t.Fatalf("file saved %d, err %v; want 42", c.TUIRailWidth, err)
	}
	f.m.width = 200 // a wide terminal shows the saved width
	f.m.layout()
	if got := f.m.railWidth(); got != 42 {
		t.Fatalf("wide terminal shows %d, want 42", got)
	}
}

// With a revision indicator the header reads stamp, sender, [-> channel],
// indicator, chips; a narrow pane cuts the chips before the indicator, then
// the whole line with an ellipsis.
func TestHeaderRevisionIndicatorOrderAndNarrowCut(t *testing.T) {
	f := newFixture(t)
	st := lipgloss.NewStyle()
	msg := bus.Message{Seq: 1, Channel: "dev", Sender: "Sam", CreatedAt: time.Now().UnixMilli(), Tags: []string{"alpha", "beta"}}
	full := ansi.Strip(f.m.header(msg, tagPanePrefix+"a", "r3 · 5 kept", st, 200))
	iArrow, iRev, iTag := strings.Index(full, strings.TrimSpace(iconArrow)), strings.Index(full, "r3 · 5 kept"), strings.Index(full, "alpha")
	if iArrow <= 0 || iArrow >= iRev || iRev >= iTag {
		t.Fatalf("want sender, arrow, indicator, chips in order: %q", full)
	}
	noTags := msg
	noTags.Tags = nil
	base := f.m.header(noTags, tagPanePrefix+"a", "r3", st, 200)
	bw := lipgloss.Width(base)
	// Room for the indicator but none for chips: chips go, indicator stays.
	if got := ansi.Strip(f.m.header(msg, tagPanePrefix+"a", "r3", st, bw+3)); !strings.Contains(got, "r3") || strings.Contains(got, "alpha") || strings.Contains(got, "…") {
		t.Fatalf("chips must be cut before the indicator: %q", got)
	}
	// Too narrow for the indicator: the whole line is cut with an ellipsis.
	got := f.m.header(msg, tagPanePrefix+"a", "r3", st, bw-2)
	if w := lipgloss.Width(got); w > bw-2 || !strings.Contains(ansi.Strip(got), "…") {
		t.Fatalf("narrow header %q (width %d)", ansi.Strip(got), w)
	}
	// No indicator: unchanged from before.
	if got := ansi.Strip(f.m.header(noTags, tagPanePrefix+"a", "", st, 200)); got != strings.TrimRight(got, " ") {
		t.Fatalf("no indicator, no trailing gap: %q", got)
	}
}
