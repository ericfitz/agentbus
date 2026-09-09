package tui

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

type mode int

const (
	modeInsert mode = iota // compose line focused; letters type
	modeNormal             // letter keymap active
	modeSearch
	modeMemories
	modeHealth
	modeConfirmDelete
)

const (
	historyPage    = 200
	statusEvery    = 5 * time.Second
	toastFor       = 5 * time.Second
	idleSessionTTL = time.Hour
)

// tick is tea.Tick, swapped out by tests so no command ever sleeps.
var tick = tea.Tick

func statusTick() tea.Cmd {
	return tick(statusEvery, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// Model is the whole TUI state. Bubble Tea copies it by value on every
// Update, so maps and slices are shared and the pointer-receiver helpers
// mutate the copy Update is working on.
type Model struct {
	c      *client
	theme  Theme
	width  int
	height int
	mode   mode

	channels []bus.Channel
	sel      int
	msgs     map[string][]bus.Message
	gaps     map[string][]bus.Gap
	loaded   map[string]bool
	seen     map[string]int64
	divider  int64
	cursor   int
	stream   viewport.Model
	follow   bool

	compose  textarea.Model
	replyTo  *bus.Message
	lastSent string
	prompt   promptState

	status       bus.Status
	statusErr    error
	sessionsSeen map[string]time.Time
	lastBatchAt  time.Time
	gapCount     int
	receiveErr   error

	toast    string
	toastSeq int

	search searchState //nolint:unused // consumed by Task 8
	mem    memState    //nolint:unused // consumed by Task 9
	health healthState //nolint:unused // consumed by Task 10
}

// Placeholders until Tasks 8-10 define the real overlay state.
type (
	searchState struct{} //nolint:unused // consumed by Task 8
	memState    struct{} //nolint:unused // consumed by Task 9
	healthState struct{} //nolint:unused // consumed by Task 10
)

func New(c *client, th Theme) Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.KeyMap.InsertNewline.SetEnabled(false)
	// Blinking is a perpetual tea.Cmd chain (bubbles/cursor reschedules
	// itself on every message forever); fine for the real async runtime, but
	// it never terminates, so a static cursor is used instead.
	ta.Cursor.SetMode(cursor.CursorStatic)
	ta.Focus()
	return Model{
		c:            c,
		theme:        th,
		mode:         modeInsert,
		sel:          -1,
		cursor:       -1,
		divider:      -1,
		follow:       true,
		msgs:         map[string][]bus.Message{},
		gaps:         map[string][]bus.Gap{},
		loaded:       map[string]bool{},
		seen:         map[string]int64{},
		sessionsSeen: map[string]time.Time{},
		stream:       viewport.New(80, 20),
		compose:      ta,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.statusCmd(), statusTick())
}

// keyString is the key as Bubble Tea names it ("q", "ctrl+c", "alt+enter"),
// or "" for any other message.
func keyString(msg tea.Msg) string {
	if k, ok := msg.(tea.KeyMsg); ok {
		return k.String()
	}
	return ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if k := keyString(msg); k != "" {
		if m.toast != "" {
			m.toast = ""
			m.layout()
		}
		if k == "ctrl+c" {
			return m, tea.Quit
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case batchMsg:
		cmds = append(cmds, m.onBatch(msg.res))
	case receiveErrMsg:
		m.receiveErr = msg.err
		cmds = append(cmds, m.showToast("receive: "+errText(msg.err)))
	case statusTickMsg:
		// The one periodic chain: Init starts it, and only this case
		// re-arms it, so opening Health (which also calls statusCmd) never
		// doubles the tick rate.
		cmds = append(cmds, m.statusCmd(), statusTick())
	case statusMsg:
		cmds = append(cmds, m.onStatus(msg))
	case subscribedMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("subscribe "+msg.channel+": "+errText(msg.err)))
		} else {
			cmds = append(cmds, m.statusCmd()) // a channel created from the prompt shows up in the rail
		}
	case historyMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("history: "+errText(msg.err)))
			break
		}
		m.loaded[msg.channel] = true
		// A prepend shifts indices; keep the cursor on the same message.
		var cursorSeq int64
		if ms := m.msgs[msg.channel]; msg.channel == m.selName() && m.cursor >= 0 && m.cursor < len(ms) {
			cursorSeq = ms[m.cursor].Seq
		}
		m.addMessages(msg.channel, msg.msgs)
		if !msg.prepend {
			// selectChannel already set the divider from whatever was
			// loaded at open time (e.g. a live message received before
			// this history page arrived); only fill it in here if nothing
			// was known to be unread yet, using seen as it stood at open
			// time (markSeen below hasn't advanced it for this load yet).
			if msg.channel == m.selName() && m.divider < 0 {
				m.divider = m.dividerFor(msg.channel)
			}
			m.markSeen(msg.channel)
		}
		m.refreshStream()
		if cursorSeq > 0 {
			m.placeCursor(cursorSeq)
		}
	case toastClearMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
			m.layout()
		}
	case sentMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("send: "+errText(msg.err)))
		}
	}
	switch m.mode {
	case modeInsert:
		cmds = append(cmds, m.updateInsert(msg))
	case modeNormal:
		cmds = append(cmds, m.updateNormal(msg))
	case modeSearch:
		cmds = append(cmds, m.updateSearch(msg))
	case modeMemories, modeConfirmDelete:
		cmds = append(cmds, m.updateMemories(msg))
	case modeHealth:
		cmds = append(cmds, m.updateHealth(msg))
	}
	return m, tea.Batch(cmds...)
}

// updateInsert: compose focused. Only the keys the compose line owns plus
// tab (next unread), pgup/pgdn (stream), esc (to normal mode) are handled
// here; everything else types.
func (m *Model) updateInsert(msg tea.Msg) tea.Cmd {
	if m.prompt.active {
		return m.updatePrompt(msg)
	}
	switch keyString(msg) {
	case "esc":
		if m.replyTo != nil {
			m.replyTo = nil
			m.layout()
			return nil
		}
		m.mode = modeNormal
		m.compose.Blur()
		return nil
	case "tab":
		return m.nextUnread()
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "enter":
		return m.submitCompose()
	case "alt+enter":
		m.compose.InsertString("\n")
		m.fitCompose()
		return nil
	case "ctrl+u":
		m.compose.Reset()
		m.fitCompose()
		return nil
	case "up":
		if m.compose.Value() == "" && m.lastSent != "" {
			m.compose.SetValue(m.lastSent)
			m.fitCompose()
			return nil
		}
	}
	var cmd tea.Cmd
	m.compose, cmd = m.compose.Update(msg)
	m.fitCompose()
	return cmd
}

// updateNormal is the letter keymap from the design notes.
func (m *Model) updateNormal(msg tea.Msg) tea.Cmd {
	if m.prompt.active {
		return m.updatePrompt(msg)
	}
	switch keyString(msg) {
	case "q":
		return tea.Quit
	case "i", "enter":
		m.mode = modeInsert
		return m.compose.Focus()
	case "esc":
		m.replyTo = nil
		m.cursor = -1
		m.layout()
	case "j":
		return m.selectChannel(m.sel + 1)
	case "k":
		return m.selectChannel(m.sel - 1)
	case "down":
		if m.cursor < 0 {
			m.cursor = len(m.msgs[m.selName()]) - 1
		}
		return m.moveCursor(1)
	case "up":
		if m.cursor < 0 {
			m.cursor = len(m.msgs[m.selName()]) - 1
		}
		return m.moveCursor(-1)
	case "tab":
		return m.nextUnread()
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "g":
		m.follow = false
		m.stream.GotoTop()
		return m.loadOlder()
	case "G":
		m.follow = true
		m.stream.GotoBottom()
	case "r":
		if ms := m.msgs[m.selName()]; m.cursor >= 0 && m.cursor < len(ms) {
			target := ms[m.cursor]
			m.replyTo = &target
		} else if n := len(ms); n > 0 {
			target := ms[n-1]
			m.replyTo = &target
		}
		m.layout()
		m.mode = modeInsert
		return m.compose.Focus()
	case "c":
		return m.createChannelPrompt()
	case "s":
		return m.toggleSubscribe()
	case "/":
		return m.openSearch()
	case "m":
		return m.openMemories()
	case "h", "?":
		return m.openHealth()
	}
	return nil
}

// moveCursor moves the normal-mode stream cursor and scrolls to keep it
// visible (refreshStream renders the cursor row highlighted).
func (m *Model) moveCursor(d int) tea.Cmd {
	n := len(m.msgs[m.selName()])
	if n == 0 {
		m.cursor = -1
		return nil
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = false
	m.refreshStream()
	return nil
}

// placeCursor puts the normal-mode cursor on the message with seq (no-op if
// it is not loaded) and scrolls so it sits mid-viewport.
func (m *Model) placeCursor(seq int64) {
	for i, x := range m.msgs[m.selName()] {
		if x.Seq == seq {
			m.cursor = i
		}
	}
	m.follow = false
	m.refreshStream()
	m.stream.SetYOffset(max(m.cursor-m.stream.Height/2, 0))
}

func (m *Model) scrollStream(msg tea.Msg) tea.Cmd {
	if keyString(msg) == "pgup" && m.stream.AtTop() {
		return m.loadOlder()
	}
	m.follow = false
	var cmd tea.Cmd
	m.stream, cmd = m.stream.Update(msg)
	if m.stream.AtBottom() {
		m.follow = true
	}
	return cmd
}

// loadOlder pages history backwards from the oldest loaded message.
func (m *Model) loadOlder() tea.Cmd {
	ch := m.selName()
	ms := m.msgs[ch]
	if ch == "" || len(ms) == 0 {
		return nil
	}
	before := ms[0].Seq
	return m.loadHistory(ch, &before)
}

func (m *Model) selName() string {
	if c := m.selected(); c != nil {
		return c.Name
	}
	return ""
}

func (m *Model) selected() *bus.Channel {
	if m.sel < 0 || m.sel >= len(m.channels) {
		return nil
	}
	return &m.channels[m.sel]
}

func (m *Model) unread(ch string) int {
	n := 0
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			n++
		}
	}
	return n
}

// markSeen records the newest loaded seq of ch as seen.
func (m *Model) markSeen(ch string) {
	if ms := m.msgs[ch]; len(ms) > 0 {
		m.seen[ch] = max(m.seen[ch], ms[len(ms)-1].Seq)
	}
}

// dividerFor returns the "new" divider for ch: the seq just before the
// oldest currently-loaded message newer than the last-seen seq, or -1 if
// nothing loaded so far is unread. Call before markSeen advances seen[ch].
func (m *Model) dividerFor(ch string) int64 {
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			return x.Seq - 1
		}
	}
	return -1
}

// selectChannel moves the selection (clamped), places the "new" divider
// just before the oldest unread message loaded so far, marks everything
// seen, and loads history on first visit.
func (m *Model) selectChannel(i int) tea.Cmd {
	if len(m.channels) == 0 {
		m.sel = -1
		return nil
	}
	i = min(max(i, 0), len(m.channels)-1)
	m.sel = i
	m.cursor = -1
	m.follow = true
	m.replyTo = nil
	ch := m.channels[i].Name
	m.divider = m.dividerFor(ch)
	m.markSeen(ch)
	m.refreshStream()
	m.stream.GotoBottom()
	if !m.loaded[ch] {
		return m.loadHistory(ch, nil)
	}
	return nil
}

func (m *Model) nextUnread() tea.Cmd {
	n := len(m.channels)
	for k := 1; k <= n; k++ {
		i := (m.sel + k) % n
		if m.unread(m.channels[i].Name) > 0 {
			return m.selectChannel(i)
		}
	}
	return nil
}

// addMessages merges in into the channel buffer, ascending by seq, dropping
// duplicates (a message can arrive via history and receive both).
func (m *Model) addMessages(ch string, in []bus.Message) {
	all := append(append([]bus.Message{}, m.msgs[ch]...), in...)
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	out := all[:0]
	for i, x := range all {
		if i == 0 || x.Seq != all[i-1].Seq {
			out = append(out, x)
		}
	}
	m.msgs[ch] = out
}

func (m *Model) onBatch(res bus.ReceiveResult) tea.Cmd {
	m.lastBatchAt = time.Now()
	m.receiveErr = nil
	byCh := map[string][]bus.Message{}
	for _, x := range res.Messages {
		byCh[x.Channel] = append(byCh[x.Channel], x)
	}
	for ch, ms := range byCh {
		m.addMessages(ch, ms)
	}
	for _, g := range res.Gaps {
		m.gaps[g.Channel] = append(m.gaps[g.Channel], g)
		m.gapCount++
	}
	if cur := m.selName(); cur != "" {
		if _, ok := byCh[cur]; ok {
			m.markSeen(cur)
		}
	}
	m.refreshStream()
	if m.follow {
		m.stream.GotoBottom()
	}
	var cmds []tea.Cmd
	for _, ch := range res.Expired {
		cmds = append(cmds, m.showToast("subscription to "+ch+" expired; resubscribing"), m.subscribeCmd(bus.Channel{Name: ch}, "now"))
	}
	if res.Notice != "" {
		cmds = append(cmds, m.showToast(res.Notice))
	}
	return tea.Batch(cmds...)
}

// onStatus refreshes the status bar data, learns channels created since
// startup (subscribing from oldest so nothing is missed), and tracks when
// each session was last listed live.
func (m *Model) onStatus(msg statusMsg) tea.Cmd {
	m.statusErr = msg.err
	if msg.err != nil {
		return nil
	}
	m.status = msg.st
	now := time.Now()
	for _, s := range msg.st.Sessions {
		m.sessionsSeen[s.Sender] = now
	}
	for name, at := range m.sessionsSeen {
		if now.Sub(at) > idleSessionTTL {
			delete(m.sessionsSeen, name)
		}
	}
	return m.setChannels(msg.st.Channels, "oldest")
}

// setChannels replaces the channel list (sorted by name), keeps the current
// selection by name, and subscribes to any channel not yet subscribed.
func (m *Model) setChannels(chans []bus.Channel, from string) tea.Cmd {
	cur := m.selName()
	chans = slices.Clone(chans) // don't sort the caller's slice (bus.Status.Channels) in place
	sort.Slice(chans, func(i, j int) bool { return chans[i].Name < chans[j].Name })
	m.channels = chans
	m.sel = -1
	for i, c := range chans {
		if c.Name == cur {
			m.sel = i
		}
	}
	var cmds []tea.Cmd
	for _, c := range chans {
		if !m.c.isSubscribed(c.Name) {
			cmds = append(cmds, m.subscribeCmd(c, from))
		}
	}
	if m.sel < 0 && len(chans) > 0 {
		cmds = append(cmds, m.selectChannel(0))
	}
	return tea.Batch(cmds...)
}

func (m *Model) subscribeCmd(ch bus.Channel, from string) tea.Cmd {
	c := m.c
	return func() tea.Msg { return subscribedMsg{channel: ch.Name, err: c.subscribe(ch, from)} }
}

func (m *Model) statusCmd() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		st, err := c.b.StatusReport()
		return statusMsg{st: st, err: err}
	}
}

// loadHistory fetches the newest historyPage messages of ch (before == nil)
// or the page before seq *before.
func (m *Model) loadHistory(ch string, before *int64) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		b := before
		if b == nil {
			top := int64(math.MaxInt64)
			b = &top
		}
		ms, err := c.b.History(c.as, ch, b, nil, historyPage)
		return historyMsg{channel: ch, msgs: ms, prepend: before != nil, err: err}
	}
}

func (m *Model) showToast(s string) tea.Cmd {
	m.toast = s
	m.toastSeq++
	m.layout()
	seq := m.toastSeq
	return tick(toastFor, func(time.Time) tea.Msg { return toastClearMsg{seq: seq} })
}

// errText renders a bus error as "code: message" and anything else as-is.
func errText(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		if be.Retryable {
			return fmt.Sprintf("%s: %s · retryable", be.Code, be.Message)
		}
		return be.Code + ": " + be.Message
	}
	return err.Error()
}

// layout, fitCompose, refreshStream are completed in Task 7; these minimal
// versions keep the stream viewport sized and the compose line 1-3 rows.
func (m *Model) layout() {
	m.stream.Width = max(m.width-leftRail-rightRail-2, 20)
	m.stream.Height = max(m.height-composeHeight(m.compose)-4, 3)
	m.compose.SetWidth(max(m.stream.Width-len(m.selName())-3, 10))
	m.refreshStream()
}

func composeHeight(ta textarea.Model) int { return min(max(ta.LineCount(), 1), 3) }

func (m *Model) fitCompose() {
	if h := composeHeight(m.compose); h != m.compose.Height() {
		m.compose.SetHeight(h)
		m.layout()
	}
}

func (m *Model) refreshStream() { m.stream.SetContent(m.renderStream()) }

// Stubs replaced by later tasks. Each returns nil so Task 5 compiles alone.
func (m *Model) renderStream() string           { return "" }
func (m *Model) openSearch() tea.Cmd            { return nil }
func (m *Model) openMemories() tea.Cmd          { return nil }
func (m *Model) openHealth() tea.Cmd            { return nil }
func (m *Model) updateSearch(tea.Msg) tea.Cmd   { return nil }
func (m *Model) updateMemories(tea.Msg) tea.Cmd { return nil }
func (m *Model) updateHealth(tea.Msg) tea.Cmd   { return nil }
func (m Model) View() string                    { return "" }

const (
	leftRail  = 18
	rightRail = 22
)
