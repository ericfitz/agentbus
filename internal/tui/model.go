package tui

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
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
	modeHealth
	modeHelp
	modeConfirmChannel // d on the channel list; y deletes, anything else cancels
)

// pane is the focused main-screen region; tab and shift+tab cycle them
// (channels and sessions share one slot, see paneKey) and home returns to
// the channel list. Focus is derived from mode and cursor
// rather than stored, so the existing keymap keeps working unchanged.
type pane int

const (
	paneChannels pane = iota
	paneSessions
	paneStream
	paneCompose
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

	channels     []bus.Channel
	dms          []bus.Channel // DM inboxes (dm/*), shown in the sessions rail instead of the channel list
	sel          int
	sessSel      int // index into sessionNames(); -1 unless the sessions pane is the selection source
	msgs         map[string][]bus.Message
	tasks        map[string][]bus.TaskSummary // task channel name -> its current tree
	taskOpen     map[int64]bool               // task id -> its detail block is expanded
	taskExpanded map[int64]bool               // task id -> its direct children are shown (children start collapsed)
	taskDetail   map[int64]bus.Task           // task id -> its full task_get result, once loaded
	gaps         map[string][]bus.Gap
	loaded       map[string]bool
	seen         map[string]int64
	divider      int64
	cursor       int             // index into rows(selName()), the visible display order; -1 for none
	cursorLine   int             // rendered line index of the cursor row's first line, from renderStream; -1 with no cursor
	expanded     map[int64]bool  // message seq -> its direct replies are shown
	peek         map[int64]int64 // thread root seq -> the one reply shown while collapsed
	bodyOpen     map[int64]bool  // message seq -> its body (subject line and full content) is shown

	// pendingTaskCursorCh/ID: a task_get/task_list jump target picked by
	// placeCursor before ch's tree had loaded, e.g. a search jump into a
	// task channel never visited yet. The tasksMsg handler consumes it (and
	// clears it) once ch's tasks arrive. "" / 0 for none.
	pendingTaskCursorCh string
	pendingTaskCursorID int64
	stream              viewport.Model
	follow              bool

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

	toast      string
	toastSeq   int
	toastHint  bool      // the toast is a hint (text color, no error prefix), not an error
	draft      taskDraft // the one unsaved task state change; id 0 for none
	lastNotice string

	search     searchState
	mem        memView
	health     healthState
	helpScroll int
}

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
	return Model{
		c:            c,
		theme:        th,
		mode:         modeNormal, // channel list focused; i or enter opens compose
		sel:          -1,
		sessSel:      -1,
		cursor:       -1,
		cursorLine:   -1,
		expanded:     map[int64]bool{},
		peek:         map[int64]int64{},
		bodyOpen:     map[int64]bool{},
		divider:      -1,
		follow:       true,
		msgs:         map[string][]bus.Message{},
		tasks:        map[string][]bus.TaskSummary{},
		taskOpen:     map[int64]bool{},
		taskExpanded: map[int64]bool{},
		taskDetail:   map[int64]bus.Task{},
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
		// A prepend, or another inbox's page in a merged DM pane, shifts
		// indices; keep the cursor on the same message. On a task channel
		// m.cursor indexes m.tasks(selName()), not rows(selName()) -- tasksMsg
		// owns that cursor (see placeTaskCursor/pendingTaskCursorID), so it's
		// left untouched here.
		var cursorSeq int64
		if !bus.IsTaskChannel(m.selName()) {
			if rs := m.rows(m.selName()); m.cursor >= 0 && m.cursor < len(rs) {
				cursorSeq = rs[m.cursor].msg.Seq
			}
		}
		// A pgup-at-top prepend grows the content above what's on screen;
		// remember the line count so the offset can grow by the same
		// amount below and the row the user was reading stays put.
		prevLines := 0
		if msg.prepend && msg.channel == m.selName() {
			prevLines = m.stream.TotalLineCount()
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
			if msg.channel == m.selName() {
				m.markSeen(msg.channel)
			}
		}
		m.refreshStream()
		switch {
		case cursorSeq > 0:
			m.placeCursor(cursorSeq)
		case msg.prepend && msg.channel == m.selName():
			m.stream.SetYOffset(m.stream.YOffset + (m.stream.TotalLineCount() - prevLines))
		}
	case tasksMsg:
		if msg.err != nil {
			m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
			cmds = append(cmds, m.showToast("tasks: "+errText(msg.err)))
			break
		}
		var cursorID int64
		if msg.ch == m.selName() {
			if t, ok := m.cursorTask(); ok {
				cursorID = t.ID
			}
		}
		if msg.ch == m.pendingTaskCursorCh {
			if msg.ch == m.selName() {
				cursorID = m.pendingTaskCursorID
			}
			m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
		}
		m.tasks[msg.ch] = msg.tasks
		if cursorID != 0 {
			// A pending jump target may sit under collapsed parents.
			m.revealTask(msg.ch, cursorID)
			if i := m.taskIndex(msg.ch, cursorID); i >= 0 {
				m.cursor = i
			}
		}
		if msg.ch == m.selName() && len(msg.tasks) > 0 {
			m.cursor = min(m.cursor, m.paneLen(msg.ch)-1)
		}
		for _, t := range msg.tasks {
			if m.taskOpen[t.ID] {
				cmds = append(cmds, m.loadTask(t.ID))
			}
		}
		m.refreshStream()
		if cursorID != 0 {
			m.scrollCursorIntoView()
		}
	case taskMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("task: "+errText(msg.err)))
			break
		}
		m.taskDetail[msg.task.ID] = msg.task
		m.refreshStream()
	case toastClearMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
			m.layout()
		}
	case sentMsg:
		if msg.err != nil {
			cmds = append(cmds, m.showToast("send: "+errText(msg.err)))
		}
	case searchMsg:
		if msg.query != m.search.query {
			break // stale: the overlay was reopened or a newer search is already in flight
		}
		m.recordQuery(msg.err == nil && !msg.res.SemanticUnavailable)
		m.search.err = msg.err
		m.search.ran = true
		if msg.err == nil {
			m.search.hits = msg.res.Hits
			m.search.cursor = 0
			m.search.textOnly = msg.res.SemanticUnavailable
			if m.search.mode != "text" {
				m.search.semanticDown = msg.res.SemanticUnavailable
			}
		} else {
			m.search.hits = nil
			m.search.textOnly = false
		}
	case configEditedMsg:
		cmds = append(cmds, m.onConfigEdited(msg))
	case revisionsMsg:
		cmds = append(cmds, m.applyRevisions(msg))
	}
	switch m.mode {
	case modeInsert:
		cmds = append(cmds, m.updateInsert(msg))
	case modeNormal:
		cmds = append(cmds, m.updateNormal(msg))
	case modeSearch:
		cmds = append(cmds, m.updateSearch(msg))
	case modeConfirmChannel:
		cmds = append(cmds, m.updateConfirmChannel(msg))
	case modeHealth:
		cmds = append(cmds, m.updateHealth(msg))
	case modeHelp:
		cmds = append(cmds, m.updateHelp(msg))
	}
	// A draft lives only on the cursor row of the focused task pane (ADR
	// 0011 decision 2): whatever the message above did, if that is no longer
	// where the draft is, the draft goes.
	if cmd := m.dropStaleDraft(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// updateInsert: compose focused. Only the keys the compose line owns plus
// tab/shift+tab/home (panes), pgup/pgdn (stream), esc (to normal mode) are
// handled here; everything else types.
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
	case "tab", "shift+tab", "home":
		return m.paneKey(msg)
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "enter":
		if bus.IsTaskChannel(m.selName()) {
			return nil
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
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
	k := keyString(msg)
	if k != "" && !isVersionKey(k) {
		m.mem = memView{} // any other key shows the latest version again
	}
	switch k {
	case "q":
		return tea.Quit
	case "i":
		if bus.IsTaskChannel(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		m.mode = modeInsert
		return m.compose.Focus()
	// enter performs the pane's action: save the task draft in a task list,
	// reply to the cursor message in the stream, compose to the selected
	// channel otherwise.
	case "enter":
		if bus.IsTaskChannel(m.selName()) {
			if m.draft.id != 0 {
				return m.saveDraft()
			}
			return nil
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if r, ok := m.cursorRow(); ok {
			if !m.replyAllowed() {
				return m.showToast(replyRefusedToast)
			}
			m.replyTo = &r.msg
			m.layout()
		}
		m.mode = modeInsert
		return m.compose.Focus()
	case "esc":
		if m.draft.id != 0 {
			m.draft = taskDraft{} // cancel the draft, and only the draft
			m.refreshStream()
			return nil
		}
		m.replyTo = nil
		m.cursor = -1
		m.layout()
	// up/down move within the focused pane. Channels and sessions are one
	// rail: down past the last channel enters the sessions, up from the first
	// session returns to the last channel. right opens the cursor message's
	// body, then its direct replies; left hides its whole subtree, then its
	// body. In a task list right opens the task's details, then its
	// children; left hides its children (whole subtree), then its details.
	case "down":
		switch m.pane() {
		case paneStream:
			return m.moveCursor(1)
		case paneSessions:
			return m.selectSession(m.sessSel + 1)
		default:
			if m.sel >= len(m.channels)-1 && len(m.sessionNames()) > 0 {
				return m.selectSession(0)
			}
			return m.selectChannel(m.sel + 1)
		}
	case "up":
		switch m.pane() {
		case paneStream:
			return m.moveCursor(-1)
		case paneSessions:
			if m.sessSel == 0 && len(m.channels) > 0 {
				return m.selectChannel(len(m.channels) - 1)
			}
			return m.selectSession(m.sessSel - 1)
		default:
			return m.selectChannel(m.sel - 1)
		}
	case "right":
		if bus.IsTaskChannel(m.selName()) {
			return m.openTask()
		}
		m.openCursor()
	case "left":
		if bus.IsTaskChannel(m.selName()) {
			m.closeTask()
		} else {
			m.closeCursor()
		}
	case "tab", "shift+tab", "home":
		return m.paneKey(msg)
	case "pgup", "pgdown":
		return m.scrollStream(msg)
	case "g":
		m.follow = false
		m.stream.GotoTop()
		return m.loadOlder()
	case "G":
		m.follow = true
		m.stream.GotoBottom()
	case " ":
		// Not a show/hide key anywhere (ADR 0011 decision 4): the arrows do
		// that; in a task list space cycles the state.
		if bus.IsTaskChannel(m.selName()) {
			return m.cycleTaskState()
		}
	case "r":
		if bus.IsTaskChannel(m.selName()) {
			return nil // no compose in a task list
		}
		if isTagPane(m.selName()) {
			return m.showToast(tagReadOnlyToast)
		}
		if !m.replyAllowed() {
			return m.showToast(replyRefusedToast)
		}
		if rs := m.rows(m.selName()); m.cursor >= 0 && m.cursor < len(rs) {
			target := rs[m.cursor].msg
			m.replyTo = &target
		} else if n := len(rs); n > 0 {
			target := rs[n-1].msg
			m.replyTo = &target
		}
		m.layout()
		m.mode = modeInsert
		return m.compose.Focus()
	case "c":
		return m.createChannelPrompt()
	case "s":
		if m.sessSel >= 0 {
			return nil
		}
		return m.toggleSubscribe()
	case "t":
		// Rail: follow a tag set. Task list: take (ADR 0011 decision 7).
		switch m.pane() {
		case paneChannels, paneSessions:
			return m.tagPrompt()
		case paneStream:
			if bus.IsTaskChannel(m.selName()) {
				return m.takeTask()
			}
		}
	case "u":
		if bus.IsTaskChannel(m.selName()) {
			return m.unassignTask()
		}
	case "d":
		if m.sessSel >= 0 || m.selected() == nil {
			return nil
		}
		if isTagPane(m.selName()) {
			return m.unfollowTagPane()
		}
		m.mode = modeConfirmChannel
	case "/":
		return m.openSearch()
	case ".", ">":
		return m.stepVersion(true)
	case ",", "<":
		return m.stepVersion(false)
	case "h":
		return m.openHealth()
	case "?":
		return m.openHelp()
	}
	return nil
}

// pane reports which main-screen region has focus.
func (m *Model) pane() pane {
	switch {
	case m.mode == modeInsert:
		return paneCompose
	case m.cursor >= 0:
		return paneStream
	case m.sessSel >= 0:
		return paneSessions
	default:
		return paneChannels
	}
}

// paneKey handles tab (next pane), shift+tab (previous pane), and home
// (channel list). The cycle is rail, messages, compose, where the rail is
// whichever of channels or sessions holds the selection; tab never moves
// between those two (arrows do), so the messages of the highlighted channel
// or session are always one tab away. Panes with nothing to focus are
// skipped: the stream when it has no messages, compose when nothing is
// selected.
func (m *Model) paneKey(msg tea.Msg) tea.Cmd {
	d := 1
	switch keyString(msg) {
	case "shift+tab":
		d = -1
	case "home":
		return m.focusPane(paneChannels)
	}
	rail := paneChannels
	if m.sessSel >= 0 {
		rail = paneSessions
	}
	order := []pane{rail, paneStream, paneCompose}
	i := slices.Index(order, m.pane())
	for range len(order) - 1 {
		i = (i + d + len(order)) % len(order)
		p := order[i]
		switch {
		case p == rail:
			return m.focusPane(p)
		case p == paneStream && m.paneLen(m.selName()) > 0:
			return m.focusPane(p)
		case p == paneCompose && m.selName() != "" && !bus.IsTaskChannel(m.selName()) && !isTagPane(m.selName()):
			return m.focusPane(p)
		}
	}
	return nil
}

// focusPane moves focus. Entering the stream keeps a cursor that is still
// valid, else lands on the newest message; leaving it resumes following new
// messages if the view is at the bottom (moveCursor stops following).
// Entering channels or sessions only runs showSelected's full reset when the
// selection actually changes between them (the stream now shows something
// different); moving focus there without that change -- home from the
// channel list, or a stray refocus of the pane already selecting -- keeps
// the lighter pre-existing reset instead, so it doesn't snap the stream to
// the bottom or discard the "new" divider.
func (m *Model) focusPane(p pane) tea.Cmd {
	if p == paneStream {
		m.mode = modeNormal
		m.compose.Blur()
		if n := m.paneLen(m.selName()); m.cursor < 0 || m.cursor >= n {
			m.cursor = n - 1
		}
		return m.moveCursor(0)
	}
	if p == paneCompose {
		m.follow = m.stream.AtBottom()
		m.mode = modeInsert
		return m.compose.Focus()
	}
	m.mode = modeNormal
	m.compose.Blur()
	wasSessions := m.sessSel >= 0
	switch p {
	case paneSessions:
		if len(m.sessionNames()) == 0 {
			return nil
		}
		m.sessSel = max(m.sessSel, 0)
	default: // paneChannels
		m.sessSel = -1
	}
	if wasSessions == (p == paneSessions) {
		m.follow = m.stream.AtBottom()
		m.cursor = -1
		m.refreshStream()
		return nil
	}
	return m.showSelected()
}

// paneLen is the stream pane's cursor bound for ch: the visible task rows
// of a task channel (renderTasks draws one row per shown task, not per raw
// revision in m.msgs), else the row count rows(ch) shows (fewer than
// paneMsgs(ch) when a thread's replies are collapsed).
func (m *Model) paneLen(ch string) int {
	if bus.IsTaskChannel(ch) {
		return len(m.taskRows(ch))
	}
	return len(m.rows(ch))
}

// moveCursor moves the normal-mode stream cursor and scrolls to keep it
// visible (refreshStream renders the cursor row highlighted). paneLen bounds
// it to the task count on a task channel (renderTasks draws one row per
// task, not per raw revision in m.msgs) and the visible row count otherwise.
func (m *Model) moveCursor(d int) tea.Cmd {
	n := m.paneLen(m.selName())
	if n == 0 {
		m.cursor = -1
		return nil
	}
	m.cursor = min(max(m.cursor+d, 0), n-1)
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
	return nil
}

// placeCursor puts the normal-mode cursor on the message with seq (no-op if
// it is not loaded) and scrolls just enough to bring it into view. On a task
// channel the cursor indexes m.tasks[ch] (see renderTasks), not rows(ch), so
// this resolves to the task-channel case instead.
func (m *Model) placeCursor(seq int64) {
	ch := m.selName()
	if bus.IsTaskChannel(ch) {
		m.placeTaskCursor(ch, seq)
		return
	}
	for i, r := range m.rows(ch) {
		if r.msg.Seq == seq {
			m.cursor = i
		}
	}
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
}

// placeTaskCursor is placeCursor's task-channel case: seq is a raw
// revision's seq (e.g. a search hit), resolved to its task via that
// revision's MemoryID (m.msgs[ch] carries it; jumpTo adds the hit there
// before calling placeCursor), then to that task's index in m.tasks[ch]. If
// ch's tree hasn't loaded yet, the id is remembered in pendingTaskCursorID
// for the tasksMsg that follows (every jump into a task channel triggers a
// loadTasks) to place once it arrives.
func (m *Model) placeTaskCursor(ch string, seq int64) {
	var id int64
	for _, x := range m.msgs[ch] {
		if x.Seq == seq && x.MemoryID != nil {
			id = *x.MemoryID
			break
		}
	}
	m.cursor = -1
	if id != 0 {
		m.revealTask(ch, id)
		m.cursor = m.taskIndex(ch, id)
	}
	if m.cursor < 0 && id != 0 {
		m.pendingTaskCursorCh, m.pendingTaskCursorID = ch, id
	}
	m.follow = false
	m.refreshStream()
	m.scrollCursorIntoView()
}

// scrollCursorIntoView clamps the stream's YOffset so cursorLine (the
// cursor's first rendered line, set by renderStream) lies within the visible
// window, scrolling by the minimum amount rather than recentering every move.
func (m *Model) scrollCursorIntoView() {
	if m.cursor < 0 || m.cursorLine < 0 || m.stream.Height <= 0 {
		return
	}
	switch {
	case m.cursorLine < m.stream.YOffset:
		m.stream.SetYOffset(m.cursorLine)
	case m.cursorLine >= m.stream.YOffset+m.stream.Height:
		m.stream.SetYOffset(m.cursorLine - m.stream.Height + 1)
	}
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

// loadOlder pages history backwards from the oldest loaded message. A task
// channel has no history view (renderTasks ignores m.msgs), so paging it is
// pure waste.
func (m *Model) loadOlder() tea.Cmd {
	ch := m.selName()
	if bus.IsTaskChannel(ch) {
		return nil
	}
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

// selected is the single switch point between the channel list and the
// sessions pane: everything keyed on selName() (stream, history, unread,
// markSeen, divider) works for a DM inbox unchanged once this returns one.
func (m *Model) selected() *bus.Channel {
	if m.sessSel >= 0 {
		names := m.sessionNames()
		if m.sessSel >= len(names) {
			return nil
		}
		want := bus.DMChannel(names[m.sessSel])
		for i := range m.dms {
			if m.dms[i].Name == want {
				return &m.dms[i]
			}
		}
		// A session seen before the status report listed its inbox: no
		// channel to show yet, treated like nothing selected.
		return nil
	}
	if m.sel < 0 || m.sel >= len(m.channels) {
		return nil
	}
	return &m.channels[m.sel]
}

// replyAllowed reports whether a reply may be started for the current
// selection: always outside the sessions pane, and inside it only for the
// TUI's own inbox -- replying inside another identity's inbox would send as
// if the TUI were that identity.
func (m *Model) replyAllowed() bool {
	if m.sessSel < 0 {
		return true
	}
	names := m.sessionNames()
	return m.sessSel < len(names) && names[m.sessSel] == m.c.as
}

const replyRefusedToast = "can't reply inside another identity's inbox; compose sends them a direct message"

func (m *Model) unread(ch string) int {
	n := 0
	for _, x := range m.msgs[ch] {
		if x.Seq > m.seen[ch] {
			n++
		}
	}
	return n
}

// sessionNames returns m.sessionsSeen's names sorted: the order the rail
// draws sessions and the order the sessions pane navigates them.
func (m *Model) sessionNames() []string {
	names := make([]string, 0, len(m.sessionsSeen))
	for n := range m.sessionsSeen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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

// selectChannel moves the channel-list selection (clamped), leaves the
// sessions pane if it held the selection (selecting a channel always means
// the channel list is what's being viewed now), and applies showSelected's
// reset.
func (m *Model) selectChannel(i int) tea.Cmd {
	if len(m.channels) == 0 {
		m.sel = -1
		return nil
	}
	m.sel = min(max(i, 0), len(m.channels)-1)
	m.sessSel = -1
	return m.showSelected()
}

// selectSession moves the sessions-pane selection (clamped) and applies
// showSelected's reset. A no-op when i is already selected -- otherwise
// re-visiting the same pane (e.g. the sessions rail landing on it while
// navigating down, then a caller selecting it again) would recompute the
// divider against seen as the first visit already advanced it, erasing a
// divider that visit legitimately set.
func (m *Model) selectSession(i int) tea.Cmd {
	names := m.sessionNames()
	if len(names) == 0 {
		m.sessSel = -1
		return nil
	}
	i = min(max(i, 0), len(names)-1)
	if i == m.sessSel {
		return nil
	}
	m.sessSel = i
	return m.showSelected()
}

// showSelected resets stream state for whatever selected() now points at:
// cursor cleared, following resumed, any pending reply cancelled, the "new"
// divider placed just before the oldest unread message loaded so far,
// everything marked seen, and history loaded on first visit. Shared by
// selectChannel, selectSession, and focusPane's channels/sessions targets,
// so switching pane, channel, or session all apply the same reset.
func (m *Model) showSelected() tea.Cmd {
	m.cursor = -1
	m.follow = true
	m.replyTo = nil
	m.pendingTaskCursorCh, m.pendingTaskCursorID = "", 0
	ch := m.selName()
	m.divider = m.dividerFor(ch)
	m.markSeen(ch)
	m.refreshStream()
	m.stream.GotoBottom()
	if bus.IsTaskChannel(ch) {
		return m.loadTasks(ch)
	}
	var cmds []tea.Cmd
	if ch != "" && !m.loaded[ch] && !isTagPane(ch) {
		cmds = append(cmds, m.loadHistory(ch, nil))
	}
	// A DM pane merges what its owner sent to every other inbox, so those
	// inboxes need their latest page too. ponytail: one page per inbox on
	// first DM visit; a bus-side conversation query is out of scope (#4).
	if strings.HasPrefix(ch, bus.DMPrefix) {
		for _, d := range m.dms {
			if d.Name != ch && !m.loaded[d.Name] {
				cmds = append(cmds, m.loadHistory(d.Name, nil))
			}
		}
	}
	// A tag pane is drawn from the chat channels' loaded messages, so it
	// needs their latest pages; one page per chat channel on first visit.
	if isTagPane(ch) {
		for _, c := range m.channels {
			if c.Kind == "ordinary" && !m.loaded[c.Name] {
				cmds = append(cmds, m.loadHistory(c.Name, nil))
			}
		}
	}
	return tea.Batch(cmds...)
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
	var cmds []tea.Cmd
	for ch, ms := range byCh {
		m.addMessages(ch, ms)
		for _, x := range ms {
			m.peekReply(ch, x)
		}
		if bus.IsTaskChannel(ch) {
			cmds = append(cmds, m.loadTasks(ch))
			// A revision of the drafted task on the bus discards the draft
			// (ADR 0011 decision 2). Saves are synchronous and clear the
			// draft first, so this is always someone else's change. Revision
			// 1 is the task's own creation record, which a fresh "oldest"
			// subscription backlog can still deliver after the draft was
			// built on the tree it already describes; only revision 2+ (an
			// actual edit) counts.
			if m.draft.id != 0 && ch == m.draft.ch {
				for _, x := range ms {
					if x.MemoryID != nil && *x.MemoryID == m.draft.id && x.Revision != nil && *x.Revision > 1 {
						cmds = append(cmds, m.dropDraft())
						break
					}
				}
			}
		}
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
	for _, ch := range res.Expired {
		// The bus dropped the subscription but c.subscribed[ch] is still
		// true, so client.subscribe would no-op; forget it first so the
		// resubscribe actually reaches the bus.
		m.c.forget(ch)
		cmds = append(cmds, m.showToast("subscription to "+ch+" expired; resubscribing"), m.subscribeCmd(bus.Channel{Name: ch}, "now"))
	}
	if res.Notice != "" && res.Notice != m.lastNotice {
		cmds = append(cmds, m.showToast(res.Notice))
	}
	m.lastNotice = res.Notice
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
	// sessionNames() is sorted, so an index alone doesn't survive the sweep
	// below: a session ahead of the selected one expiring would silently
	// repoint sessSel at a different identity's inbox. Remember the selected
	// session by name instead, and re-resolve it once the sweep is done.
	var selName string
	hadSel := m.sessSel >= 0
	if hadSel {
		if names := m.sessionNames(); m.sessSel < len(names) {
			selName = names[m.sessSel]
		}
	}
	for name, at := range m.sessionsSeen {
		if now.Sub(at) > idleSessionTTL {
			delete(m.sessionsSeen, name)
		}
	}
	cmds := []tea.Cmd{m.setChannels(msg.st.Channels, msg.tags, "oldest")}
	if hadSel {
		names := m.sessionNames()
		switch i := slices.Index(names, selName); {
		case len(names) == 0:
			// The last session aged out: return to the channel list.
			m.sessSel = -1
			cmds = append(cmds, m.showSelected())
		case i >= 0:
			m.sessSel = i // still the same inbox, just possibly reindexed
		default:
			// selName aged out: clamp to a valid neighbor and refresh the
			// stream for whatever that now points at.
			m.sessSel = min(m.sessSel, len(names)-1)
			cmds = append(cmds, m.showSelected())
		}
	}
	return tea.Batch(cmds...)
}

// setChannels replaces the channel list (sorted by name), keeps the current
// selection by name, and subscribes to any channel not yet subscribed. DM
// inboxes (dm/*) are split out into m.dms: they show in the sessions rail,
// never the channel list, and are never selectable.
func (m *Model) setChannels(chans []bus.Channel, tags [][]string, from string) tea.Cmd {
	// cur comes from the channel list itself, not selName(): while the
	// sessions pane holds the selection, selName() names a DM inbox that
	// would never match a channel, silently losing the channel list's
	// selection on every status refresh. Sourcing it from m.channels keeps
	// the rematch below correct (and safe to run unconditionally) whichever
	// pane is currently selecting.
	cur := ""
	if m.sel >= 0 && m.sel < len(m.channels) {
		cur = m.channels[m.sel].Name
	}
	chans = slices.Clone(chans) // don't sort the caller's slice (bus.Status.Channels) in place
	sort.Slice(chans, func(i, j int) bool { return chans[i].Name < chans[j].Name })
	// chans (all of them, DM and ordinary) is still needed below to subscribe
	// to everything, so the split below builds two new slices rather than
	// filtering chans in place.
	var dms, channels []bus.Channel
	for _, c := range chans {
		if strings.HasPrefix(c.Name, bus.DMPrefix) {
			dms = append(dms, c)
		} else {
			channels = append(channels, c)
		}
	}
	m.dms = dms
	m.channels = channels
	// Tag sets are rail entries too (a "tags" section under the channels):
	// synthetic, never subscribed on the bus, drawn as chips, rendered from
	// the messages already loaded for the chat channels.
	for _, set := range tags {
		m.channels = append(m.channels, bus.Channel{Name: tagPanePrefix + strings.Join(set, ","), Kind: "tags"})
	}
	m.sel = -1
	for i, c := range m.channels {
		if c.Name == cur {
			m.sel = i
		}
	}
	var cmds []tea.Cmd
	for _, c := range chans {
		if !m.c.known(c.Name) {
			cmds = append(cmds, m.subscribeCmd(c, from))
		}
	}
	// Only auto-select a first channel while the channel list itself is what
	// nothing has picked from yet; while a session is selected this must not
	// steal the selection away to the channel list.
	if m.sessSel < 0 && m.sel < 0 && len(m.channels) > 0 {
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
		if err != nil {
			return statusMsg{st: st, err: err}
		}
		tags, err := c.b.TagSubscriptions(c.as)
		return statusMsg{st: st, tags: tags, err: err}
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

// showToast shows s as an error (red, ✗ prefix) for toastFor.
func (m *Model) showToast(s string) tea.Cmd {
	m.toast = s
	m.toastHint = false
	m.toastSeq++
	m.layout()
	seq := m.toastSeq
	return tick(toastFor, func(time.Time) tea.Msg { return toastClearMsg{seq: seq} })
}

// showHint is showToast in the hint style: the text color, no prefix.
func (m *Model) showHint(s string) tea.Cmd {
	cmd := m.showToast(s)
	m.toastHint = true
	return cmd
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

// layout sizes the stream viewport and compose line against the rails
// (hidden when the terminal is narrow) and the extra rows the reply banner
// and toast add.
func (m *Model) layout() {
	w := m.width
	if m.showLeft() {
		w -= m.railWidth() + 1
	}
	m.stream.Width = max(w, 20)
	// header (1) is already subtracted below; divider and status bar are the
	// only other always-present rows -- compose's own height is accounted
	// for separately via composeHeight, so it does not belong here too.
	extra := 2 // divider, status bar
	if m.replyTo != nil {
		extra++
	}
	if m.toast != "" {
		extra++
	}
	m.stream.Height = max(m.height-1-composeHeight(m.compose)-extra, 3)
	m.compose.SetWidth(max(m.stream.Width-len(m.selName())-16, 10))
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
