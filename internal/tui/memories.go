package tui

import (
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
	return k == "." || k == ">" || k == "," || k == "<"
}

// stepVersion moves the cursor memory row one version older (older=true,
// . or >) or newer (, or <), stopping at either end. The first older step
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
