package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// memView is the version shown for the cursor's memory row: revs are its
// revisions oldest first (bus.MemoryRevisions), idx the one shown. seq is
// the live row's seq, so a live edit (which gives the memory a new live
// seq) or a different cursor row no longer matches and the row shows its
// latest version again. revs is nil while the fetch is in flight.
type memView struct {
	seq  int64
	revs []bus.Message
	idx  int
}

// version returns the revision to show in place of row x, if one applies.
func (v memView) version(x bus.Message) (bus.Message, bool) {
	if v.revs == nil || v.seq != x.Seq {
		return bus.Message{}, false
	}
	return v.revs[v.idx], true
}

func isVersionKey(k string) bool {
	return k == "." || k == ","
}

// stepVersion moves the cursor memory row one version older (older=true,
// .) or newer (,), stopping at either end. The first older step
// fetches the revisions; revisionsMsg then applies it.
func (m *Model) stepVersion(older bool) tea.Cmd {
	r, ok := m.cursorRow()
	if !ok || bus.IsTaskChannel(m.selName()) || r.msg.MemoryID == nil {
		return nil
	}
	if m.mem.seq != r.msg.Seq {
		if !older {
			return nil // already on the latest version
		}
		m.mem = memView{seq: r.msg.Seq}
		c, id, seq := m.c, *r.msg.MemoryID, r.msg.Seq
		return func() tea.Msg {
			revs, err := c.b.MemoryRevisions(c.as, id)
			return revisionsMsg{seq: seq, revs: revs, err: err}
		}
	}
	if m.mem.revs == nil {
		return nil // fetch in flight
	}
	if older {
		m.mem.idx = max(m.mem.idx-1, 0)
	} else {
		m.mem.idx = min(m.mem.idx+1, len(m.mem.revs)-1)
	}
	m.refreshStream()
	return nil
}

// applyRevisions shows the version one older than the latest once the
// fetch that the first older step started arrives.
func (m *Model) applyRevisions(msg revisionsMsg) tea.Cmd {
	if msg.seq != m.mem.seq {
		return nil // stale: the cursor moved on
	}
	if msg.err != nil || len(msg.revs) == 0 {
		m.mem = memView{}
		if msg.err != nil {
			return m.showToast("memory: " + errText(msg.err))
		}
		return nil
	}
	m.mem.revs = msg.revs
	m.mem.idx = max(len(msg.revs)-2, 0)
	m.refreshStream()
	return nil
}

// memoryChannel reports whether ch is a memory-kind channel the rail lists
// that is not a task list (task rows carry no counters or flags).
func (m *Model) memoryChannel(ch string) bool {
	if bus.IsTaskChannel(ch) {
		return false
	}
	return slices.ContainsFunc(m.channels, func(c bus.Channel) bool { return c.Name == ch && c.Kind == "memory" })
}

// loadMemStats fetches ch's memory counters and flag state; nil unless ch is
// a memory channel.
func (m *Model) loadMemStats(ch string) tea.Cmd {
	if !m.memoryChannel(ch) {
		return nil
	}
	c := m.c
	return func() tea.Msg {
		st, err := c.b.ChannelMemoryStats(c.as, ch)
		return memStatsMsg{channel: ch, stats: st, err: err}
	}
}

// applyMemStats replaces the channel's stats; a failed load keeps the old
// ones.
func (m *Model) applyMemStats(msg memStatsMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast("memory stats: " + errText(msg.err))
	}
	m.memStats[msg.channel] = msg.stats
	m.refreshStream()
	return nil
}

// memLabel is a memory row's header label: the revision label rev, then the
// pruned state, the incorrect flag and the served and used counts (state
// first, so header truncation drops the counts first). Pruned and incorrect
// carry their icons. Without stats (ok false) it is rev unchanged.
func memLabel(rev string, st bus.MemoryStats, ok bool) string {
	if !ok {
		return rev
	}
	var parts []string
	if rev != "" {
		parts = append(parts, rev)
	}
	// State markers come before the counts so header truncation drops
	// the counts first.
	if st.PrunedAt != nil {
		reason := ""
		if st.PrunedReason != nil {
			reason = *st.PrunedReason
		}
		parts = append(parts, iconPruned+"pruned ("+reason+")")
	}
	if st.IncorrectAt != nil {
		by := ""
		if st.IncorrectBy != nil {
			by = *st.IncorrectBy
		}
		parts = append(parts, iconIncorrect+"incorrect by "+by+" "+strings.Join(strings.Fields(stamp(*st.IncorrectAt)), " "))
	}
	parts = append(parts, "served "+strconv.FormatInt(st.ServedCount, 10), "used "+strconv.FormatInt(st.UsedCount, 10))
	return strings.Join(parts, " · ")
}

// cursorMemory is the stream's cursor row when it is a memory of a memory
// channel (not a task list or a tag pane).
func (m *Model) cursorMemory() (bus.Message, bool) {
	if m.pane() != paneStream || m.boardActive() || m.sessSel >= 0 || isTagPane(m.selName()) || !m.memoryChannel(m.selName()) {
		return bus.Message{}, false
	}
	r, ok := m.cursorRow()
	if !ok || r.msg.MemoryID == nil {
		return bus.Message{}, false
	}
	return r.msg, true
}

// memTitle is the subject or first line of x, cut to 40 columns.
func memTitle(x bus.Message) string {
	t := x.Subject
	if t == "" {
		t = strings.SplitN(x.Content, "\n", 2)[0]
	}
	if rs := []rune(t); len(rs) > 40 {
		t = string(rs[:40]) + "…"
	}
	return t
}

func (m *Model) restoreMemory() tea.Cmd {
	r, ok := m.cursorMemory()
	if !ok {
		return nil
	}
	id := *r.MemoryID
	if err := m.c.b.RestoreMemory(m.c.as, id); err != nil {
		return m.showToast("restore: " + errText(err))
	}
	return tea.Batch(m.showHint("restored memory #"+strconv.FormatInt(id, 10)), m.loadMemStats(r.Channel))
}

func (m *Model) clearIncorrect() tea.Cmd {
	r, ok := m.cursorMemory()
	if !ok {
		return nil
	}
	id := *r.MemoryID
	if err := m.c.b.ClearMemoryIncorrect(m.c.as, id); err != nil {
		return m.showToast("clear flag: " + errText(err))
	}
	return tea.Batch(m.showHint("cleared the incorrect flag on memory #"+strconv.FormatInt(id, 10)), m.loadMemStats(r.Channel))
}

// updateConfirmMemory answers the delete-memory prompt: y deletes, any other
// key cancels.
func (m *Model) updateConfirmMemory(msg tea.Msg) tea.Cmd {
	k := keyString(msg)
	if k == "" {
		return nil
	}
	m.mode = modeNormal
	if k != "y" {
		return nil
	}
	id, ch := m.confirmMem.id, m.confirmMem.ch
	if err := m.c.b.DeleteMemoryAny(m.c.as, id); err != nil {
		return m.showToast("delete: " + errText(err))
	}
	m.msgs[ch] = slices.DeleteFunc(m.msgs[ch], func(x bus.Message) bool { return x.MemoryID != nil && *x.MemoryID == id })
	delete(m.memStats[ch], id)
	if n := len(m.rows(m.selName())); m.cursor >= n {
		m.cursor = n - 1
	}
	m.refreshStream()
	return tea.Batch(m.showHint("deleted memory #"+strconv.FormatInt(id, 10)), m.loadMemStats(ch))
}

// confirmMemoryLine is the compose-row text while modeConfirmMemory is up.
func (m Model) confirmMemoryLine() string {
	return m.theme.Style(m.theme.Error).Render(fmt.Sprintf("delete memory #%d \"%s\" PERMANENTLY? no undo · y yes  any other key no", m.confirmMem.id, m.confirmMem.title))
}
