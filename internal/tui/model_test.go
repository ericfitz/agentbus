package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// fixture opens a bus with channels dev (ordinary) and notes (memory), an
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
	if _, err := ab.CreateChannel(sam, "notes", "memory"); err != nil {
		t.Fatal(err)
	}
	c, err := newClient(cfg, "eric", discardLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	f := &fixture{c: c, ab: ab, sam: sam}
	f.m = New(c, LoadTheme(func(string) string { return "" }, nil))
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
	f.agentSend(t, "notes", "remember this")
	f.receive(t)
	if f.m.unread("notes") != 1 || f.m.unread("dev") != 0 {
		t.Fatalf("unread notes=%d dev=%d", f.m.unread("notes"), f.m.unread("dev"))
	}
	f.key("esc") // normal mode
	f.key("j")   // select notes
	if f.m.selected().Name != "notes" {
		t.Fatalf("j did not select notes: %v", f.m.selected())
	}
	if f.m.unread("notes") != 0 {
		t.Fatalf("selecting must mark seen, unread=%d", f.m.unread("notes"))
	}
	if f.m.divider < 0 {
		t.Fatal("divider must mark where new messages start")
	}
	f.key("k")
	if f.m.selected().Name != "dev" {
		t.Fatal("k did not go back to dev")
	}
}

func TestTabJumpsToNextUnreadInBothModes(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "notes", "x")
	f.receive(t)
	f.key("tab") // insert mode
	if f.m.selected().Name != "notes" {
		t.Fatal("tab in insert mode must jump to the unread channel")
	}
	f.key("k") // typed into compose in insert mode: must not change selection
	if f.m.selected().Name != "notes" || f.m.compose.Value() != "k" {
		t.Fatalf("insert mode must type, sel=%v compose=%q", f.m.selected(), f.m.compose.Value())
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
	if len(f.m.channels) != 3 || !f.c.subscribed["late"] {
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

func TestDownInNormalModeDrivesCursorNotSelection(t *testing.T) {
	f := newFixture(t)
	f.agentSend(t, "dev", "one")
	f.m = New(f.c, f.m.theme) // re-init after the message exists
	f.run(f.m.Init())
	sel := f.m.sel
	f.key("esc") // normal mode
	f.key("down")
	if f.m.cursor < 0 {
		t.Fatal("down in normal mode must set the stream cursor")
	}
	if f.m.sel != sel {
		t.Fatalf("down must not change the selected channel: sel=%d want %d", f.m.sel, sel)
	}
}

func TestDividerLandsBeforeFirstUnreadOnFirstVisit(t *testing.T) {
	f := newFixture(t)
	old := f.agentSend(t, "notes", "old")
	f.drainAndAck(t) // "old" is ack'd unseen: only History will ever surface it
	live := f.agentSend(t, "notes", "live")
	f.receive(t) // notes now has "live" loaded but not the pre-existing "old" history
	f.key("esc") // normal mode
	f.key("j")   // dev -> notes, triggering the first-ever history load
	if f.m.selected().Name != "notes" {
		t.Fatalf("j did not select notes: %v", f.m.selected())
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
