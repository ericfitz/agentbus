package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// fixture opens a bus with channels dev (ordinary) and dev-notes (memory), an
// agent "Sam", and a TUI model registered as "eric" whose Init has run.
type fixture struct {
	m   Model
	c   *client
	ab  *bus.Bus
	sam string
	ack string // batch token from the last receive(), acked on the next one (mirrors client.receiveLoop's own ack variable)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testConfig(t)
	ab, sam := agent(t, cfg, "Sam")
	if _, err := ab.CreateChannel(sam, "dev", "ordinary"); err != nil {
		t.Fatal(err)
	}
	if _, err := ab.CreateChannel(sam, "dev-notes", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam}
	f.m = New(c, LoadTheme(cfg, io.Discard))
	f.m.width, f.m.height = 100, 32
	f.run(f.m.Init())
	return f
}

// Timers (status tick, toast clear) would sleep for real inside run; tests
// drive those messages by hand instead.
func init() { tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil } }

// run executes cmd synchronously and feeds every resulting message back
// through Update, recursively, so tests see the same state the program would.
func (f *fixture) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			f.run(c)
		}
		return
	}
	if _, ok := msg.(statusTickMsg); ok {
		return // the periodic timer; tests trigger status explicitly
	}
	f.send(msg)
}

func (f *fixture) send(msg tea.Msg) {
	next, cmd := f.m.Update(msg)
	f.m = next.(Model)
	f.run(cmd)
}

func (f *fixture) key(k string) {
	switch k {
	case "enter":
		f.send(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		f.send(tea.KeyMsg{Type: tea.KeyEsc})
	case "tab":
		f.send(tea.KeyMsg{Type: tea.KeyTab})
	case "shift+tab":
		f.send(tea.KeyMsg{Type: tea.KeyShiftTab})
	case "home":
		f.send(tea.KeyMsg{Type: tea.KeyHome})
	case "up":
		f.send(tea.KeyMsg{Type: tea.KeyUp})
	case "down":
		f.send(tea.KeyMsg{Type: tea.KeyDown})
	case "pgup":
		f.send(tea.KeyMsg{Type: tea.KeyPgUp})
	case "pgdown":
		f.send(tea.KeyMsg{Type: tea.KeyPgDown})
	case "left":
		f.send(tea.KeyMsg{Type: tea.KeyLeft})
	case "right":
		f.send(tea.KeyMsg{Type: tea.KeyRight})
	case "ctrl+u":
		f.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	case "alt+enter":
		f.send(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	default:
		f.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
}

func (f *fixture) agentSend(t *testing.T, ch, content string) bus.SendResult {
	t.Helper()
	r, err := f.ab.Send(f.sam, bus.SendInput{Channel: ch, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// receive pulls whatever is pending for the TUI, as the receive goroutine
// would, and feeds it to the model.
func (f *fixture) receive(t *testing.T) {
	t.Helper()
	res, err := f.c.b.Receive(f.c.as, bus.ReceiveInput{Ack: f.ack, IncludeOwn: true, WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	f.ack = res.Batch
	f.send(batchMsg{res})
}

// drainAndAck fully acknowledges whatever is currently pending for the TUI's
// session without feeding it to the model, as if the human wasn't running
// the TUI when it arrived: it can only be discovered later via History, the
// same as any message that predates the client's subscription.
func (f *fixture) drainAndAck(t *testing.T) {
	t.Helper()
	res, err := f.c.b.Receive(f.c.as, bus.ReceiveInput{IncludeOwn: true, WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch == "" {
		return
	}
	if _, err := f.c.b.Receive(f.c.as, bus.ReceiveInput{Ack: res.Batch, IncludeOwn: true, WaitSeconds: 0}); err != nil {
		t.Fatal(err)
	}
}

func TestInitSelectsFirstChannelAndLoadsHistory(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "old one")
	f.m = New(f.c, f.m.theme) // re-init after the message exists
	f.run(f.m.Init())
	if got := f.m.selected(); got == nil || got.Name != "dev" {
		t.Fatalf("selected = %v", got)
	}
	if len(f.m.msgs["dev"]) != 1 || f.m.msgs["dev"][0].Content != "old one" {
		t.Fatalf("history not loaded: %+v", f.m.msgs["dev"])
	}
	if f.m.unread("dev") != 0 {
		t.Fatalf("history is not unread, got %d", f.m.unread("dev"))
	}
	if f.m.mode != modeInsert {
		t.Fatalf("start in insert mode, got %v", f.m.mode)
	}
}

func TestBatchOnOtherChannelCountsUnreadAndSelectingClearsIt(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev-notes", "remember this")
	f.receive(t)
	if f.m.unread("dev-notes") != 1 || f.m.unread("dev") != 0 {
		t.Fatalf("unread dev-notes=%d dev=%d", f.m.unread("dev-notes"), f.m.unread("dev"))
	}
	f.key("esc")  // normal mode
	f.key("down") // select dev-notes
	if f.m.selected().Name != "dev-notes" {
		t.Fatalf("down did not select dev-notes: %v", f.m.selected())
	}
	if f.m.unread("dev-notes") != 0 {
		t.Fatalf("selecting must mark seen, unread=%d", f.m.unread("dev-notes"))
	}
	if f.m.divider < 0 {
		t.Fatal("divider must mark where new messages start")
	}
	f.key("up")
	if f.m.selected().Name != "dev" {
		t.Fatal("up did not go back to dev")
	}
}

// TestTabCyclesPanesAndHomeReturnsToChannels: compose -> channels ->
// messages -> compose, shift+tab back, home from anywhere to channels, and
// an empty channel's message pane is skipped.
func TestTabCyclesPanesAndHomeReturnsToChannels(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "x")
	f.receive(t)
	if f.m.pane() != paneCompose {
		t.Fatalf("start in compose, got %v", f.m.pane())
	}
	f.key("tab")
	if f.m.pane() != paneChannels || f.m.mode != modeNormal {
		t.Fatalf("tab from compose wraps to channels, got pane=%v mode=%v", f.m.pane(), f.m.mode)
	}
	f.key("tab")
	if f.m.pane() != paneStream || f.m.cursor != 0 {
		t.Fatalf("tab from channels focuses the newest message, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
	f.key("tab")
	if f.m.pane() != paneCompose || f.m.mode != modeInsert {
		t.Fatalf("tab from messages focuses compose, got pane=%v", f.m.pane())
	}
	f.key("shift+tab")
	if f.m.pane() != paneStream {
		t.Fatalf("shift+tab from compose goes back to messages, got %v", f.m.pane())
	}
	f.key("tab")
	f.key("tab") // stream -> compose -> channels: a lap must not leave follow off
	if !f.m.follow {
		t.Fatal("leaving the message pane at the bottom must keep following new messages")
	}
	f.key("tab")
	f.key("up") // in the channel pane up moves channels: dev is first, so it stays
	f.key("home")
	if f.m.pane() != paneChannels || f.m.cursor != -1 {
		t.Fatalf("home returns to channels, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
	f.key("down") // dev-notes: no messages, so tab must skip the message pane
	f.key("tab")
	if f.m.pane() != paneCompose {
		t.Fatalf("tab skips an empty message pane, got %v", f.m.pane())
	}
	f.key("x")
	if f.m.compose.Value() != "x" {
		t.Fatalf("compose must type after tab, got %q", f.m.compose.Value())
	}
	f.key("home")
	if f.m.pane() != paneChannels {
		t.Fatalf("home from compose returns to channels, got %v", f.m.pane())
	}
}

func TestHelpOverlayListsKeysAndHealthIsSeparate(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatalf("? opens help, got mode %v", f.m.mode)
	}
	v := f.m.View()
	for _, want := range []string{"help", "tab / shift+tab", "next / previous pane", "home", "<name> [memory]", "alt+enter", "quit"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "storage") {
		t.Fatalf("help must not be the health overlay:\n%s", v)
	}
	f.key("down")
	if f.m.helpScroll != 1 {
		t.Fatalf("down scrolls help, got %d", f.m.helpScroll)
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes help")
	}
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatal("? inside health switches to help")
	}
}

func TestGapsAreRecordedPerChannel(t *testing.T) {
	f := newFixture(t)
	f.send(batchMsg{res: bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev", From: 3, To: 7}}}})
	if len(f.m.gaps["dev"]) != 1 || f.m.gapCount != 1 {
		t.Fatalf("gaps=%v count=%d", f.m.gaps, f.m.gapCount)
	}
}

func TestAddMessagesMergesAndDedupes(t *testing.T) {
	f := newFixture(t)
	f.m.addMessages("dev", []bus.Message{{Seq: 5}, {Seq: 2}})
	f.m.addMessages("dev", []bus.Message{{Seq: 3}, {Seq: 5}})
	got := f.m.msgs["dev"]
	if len(got) != 3 || got[0].Seq != 2 || got[1].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestPgUpAtTopLoadsOlderHistory(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.agentSend(t, "dev", "m")
	}
	f.m = New(f.c, f.m.theme)
	f.run(f.m.Init())
	// Pretend only the newest message was loaded, then page up.
	f.m.msgs["dev"] = f.m.msgs["dev"][2:]
	f.key("pgup")
	if len(f.m.msgs["dev"]) != 3 {
		t.Fatalf("pgup at top must prepend older history, have %d", len(f.m.msgs["dev"]))
	}
}

func TestStatusMsgTracksSessionsAndNewChannels(t *testing.T) {
	f := newFixture(t)
	if _, err := f.ab.CreateChannel(f.sam, "late", "ordinary"); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	if len(f.m.channels) != 5 || !f.c.subscribed["late"] {
		t.Fatalf("new channel not picked up: %v %v", f.m.channels, f.c.subscribed)
	}
	if _, ok := f.m.sessionsSeen["Sam"]; !ok {
		t.Fatal("Sam must be listed as seen")
	}
}

func TestToastClearsOnKey(t *testing.T) {
	f := newFixture(t)
	f.send(receiveErrMsg{err: &bus.Error{Code: "internal", Message: "boom"}})
	if f.m.toast == "" {
		t.Fatal("receive error must toast")
	}
	f.key("x")
	if f.m.toast != "" {
		t.Fatal("any key clears the toast")
	}
}

// TestArrowsFollowTheFocusedPane: in the channel pane up/down move the
// selection and right expands the channel into its messages; in the
// message pane up/down move the cursor and left collapses back.
func TestArrowsFollowTheFocusedPane(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "one")
	f.agentSend(t, "dev", "two")
	f.m = New(f.c, f.m.theme) // re-init after the messages exist
	f.run(f.m.Init())
	f.key("esc") // normal mode, channel pane
	f.key("down")
	if f.m.selected().Name != "dev-notes" || f.m.cursor >= 0 {
		t.Fatalf("down in the channel pane selects the next channel, got %v cursor=%d", f.m.selected(), f.m.cursor)
	}
	f.key("right")
	if f.m.pane() != paneChannels {
		t.Fatal("right on a channel with no messages stays in the channel pane")
	}
	f.key("up")
	f.key("right")
	if f.m.pane() != paneStream || f.m.cursor != 1 {
		t.Fatalf("right expands dev onto its newest message, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
	f.key("up")
	if f.m.cursor != 0 || f.m.selected().Name != "dev" {
		t.Fatalf("up in the message pane moves the cursor, not the channel: cursor=%d sel=%v", f.m.cursor, f.m.selected())
	}
	f.key("left")
	if f.m.pane() != paneChannels || f.m.cursor != -1 {
		t.Fatalf("left collapses back to the channel pane, got pane=%v cursor=%d", f.m.pane(), f.m.cursor)
	}
}

func TestDividerLandsBeforeFirstUnreadOnFirstVisit(t *testing.T) {
	f := newFixture(t)
	old := f.agentSend(t, "dev-notes", "old")
	f.drainAndAck(t) // "old" is ack'd unseen: only History will ever surface it
	live := f.agentSend(t, "dev-notes", "live")
	f.receive(t)  // dev-notes now has "live" loaded but not the pre-existing "old" history
	f.key("esc")  // normal mode
	f.key("down") // dev -> dev-notes, triggering the first-ever history load
	if f.m.selected().Name != "dev-notes" {
		t.Fatalf("down did not select dev-notes: %v", f.m.selected())
	}
	if want := live.Seq - 1; f.m.divider != want {
		t.Fatalf("divider = %d, want %d (== old.Seq %d)", f.m.divider, want, old.Seq)
	}
	if old.Seq < 1 {
		t.Fatalf("test assumption broken: old.Seq = %d, want >= 1", old.Seq)
	}
}

// TestNormalModeCursorScrollsIntoView guards F2: moving the cursor far in
// either direction must keep its rendered line inside the stream viewport,
// not just re-render in place.
func TestNormalModeCursorScrollsIntoView(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 40; i++ {
		f.agentSend(t, "dev", fmt.Sprintf("msg %d", i))
	}
	f.receive(t)
	f.m.height = 20
	f.m.layout()
	f.key("esc")
	f.key("right") // into the message pane
	for i := 0; i < 30; i++ {
		f.key("up")
	}
	if f.m.cursorLine < f.m.stream.YOffset || f.m.cursorLine >= f.m.stream.YOffset+f.m.stream.Height {
		t.Fatalf("cursor scrolled out of view going up: cursorLine=%d YOffset=%d Height=%d", f.m.cursorLine, f.m.stream.YOffset, f.m.stream.Height)
	}
	for i := 0; i < 30; i++ {
		f.key("down")
	}
	if f.m.cursorLine < f.m.stream.YOffset || f.m.cursorLine >= f.m.stream.YOffset+f.m.stream.Height {
		t.Fatalf("cursor scrolled out of view going down: cursorLine=%d YOffset=%d Height=%d", f.m.cursorLine, f.m.stream.YOffset, f.m.stream.Height)
	}
}

func TestQuitKeysInNormalModeOnly(t *testing.T) {
	f := newFixture(t)
	f.key("q")
	if f.m.compose.Value() != "q" {
		t.Fatal("q in insert mode types")
	}
	f.key("esc")
	next, cmd := f.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	f.m = next.(Model)
	if cmd == nil {
		t.Fatal("q in normal mode must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must return tea.Quit")
	}
}

// TestExpiredSubscriptionIsResubscribed verifies that an onBatch report of an
// expired subscription actually restores delivery, rather than leaving the
// client's stale "subscribed" flag blocking a real resubscribe.
func TestExpiredSubscriptionIsResubscribed(t *testing.T) {
	f := newFixture(t)
	if err := f.c.b.Unsubscribe(f.c.as, "dev"); err != nil {
		t.Fatal(err)
	}
	f.send(batchMsg{res: bus.ReceiveResult{Expired: []string{"dev"}}})
	if !f.c.isSubscribed("dev") {
		t.Fatal("expired channel was not resubscribed")
	}
	f.agentSend(t, "dev", "after expiry")
	f.receive(t)
	for _, msg := range f.m.msgs["dev"] {
		if msg.Content == "after expiry" {
			return
		}
	}
	t.Fatal("message sent after resubscribe was not delivered")
}
