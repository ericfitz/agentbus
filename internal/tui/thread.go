package tui

import (
	"sort"
	"strings"

	"github.com/ericfitz/agentbus/internal/bus"
)

// paneMsgs returns the messages a pane shows. A dm/X pane merges X's
// inbox with every loaded direct message X sent (DMs are one-way, so a
// conversation alternates between two inboxes); everything else shows its
// own channel. Unread counts and the divider stay on m.msgs[ch] (the
// inbox), see unread and dividerFor.
func (m *Model) paneMsgs(ch string) []bus.Message {
	if isTagPane(ch) {
		return m.tagPaneMsgs(ch)
	}
	owner, ok := strings.CutPrefix(ch, bus.DMPrefix)
	if !ok {
		return m.msgs[ch]
	}
	out := append([]bus.Message{}, m.msgs[ch]...)
	for name, ms := range m.msgs {
		if name == ch || !strings.HasPrefix(name, bus.DMPrefix) {
			continue
		}
		for _, x := range ms {
			if x.Sender == owner {
				out = append(out, x)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// row is one visible message in the stream's display order.
type row struct {
	msg    bus.Message
	depth  int   // 0 for a thread root, +1 per reply level
	hidden int   // descendants not shown under this row (a summary line follows)
	open   bool  // at least one direct reply is shown under this row
	latest int64 // newest created_at in the thread; set on root rows only
	newest int64 // newest seq in the thread; set on root rows only
	root   int64 // seq of the thread root
}

// rows returns ch's loaded messages in display order: threads (a root and
// every reply into it) sorted by their newest message ascending (by seq,
// which is monotonic with time on one bus and never ties), each in tree
// order. A reply whose parent is not loaded is a root. Replies are
// shown only under a parent the user expanded, or along the path to the
// thread's peeked reply (the newest one received while collapsed).
func (m *Model) rows(ch string) []row {
	ms := m.paneMsgs(ch)
	if len(ms) == 0 {
		return nil
	}
	bySeq := make(map[int64]bus.Message, len(ms))
	for _, x := range ms {
		bySeq[x.Seq] = x
	}
	parent := func(x bus.Message) (bus.Message, bool) {
		if x.ReplyTo == nil {
			return bus.Message{}, false
		}
		p, ok := bySeq[*x.ReplyTo]
		return p, ok
	}
	children := map[int64][]bus.Message{}
	rootOf := map[int64]int64{}
	latest := map[int64]int64{} // root seq -> newest created_at in the thread
	newest := map[int64]int64{} // root seq -> newest seq in the thread
	var roots []bus.Message
	for _, x := range ms { // seq order, so a parent is processed before its replies
		if p, ok := parent(x); ok {
			children[p.Seq] = append(children[p.Seq], x)
			rootOf[x.Seq] = rootOf[p.Seq]
		} else {
			rootOf[x.Seq] = x.Seq
			roots = append(roots, x)
		}
		r := rootOf[x.Seq]
		latest[r] = max(latest[r], x.CreatedAt)
		newest[r] = x.Seq
	}
	sort.Slice(roots, func(i, j int) bool { return newest[roots[i].Seq] < newest[roots[j].Seq] })
	var size func(seq int64) int
	size = func(seq int64) int {
		n := 1
		for _, c := range children[seq] {
			n += size(c.Seq)
		}
		return n
	}
	var out []row
	var walk func(x bus.Message, depth int, onPath map[int64]bool)
	walk = func(x bus.Message, depth int, onPath map[int64]bool) {
		r := row{msg: x, depth: depth, root: rootOf[x.Seq]}
		if depth == 0 {
			r.latest, r.newest = latest[x.Seq], newest[x.Seq]
		}
		at := len(out)
		out = append(out, r)
		for _, c := range children[x.Seq] {
			if m.expanded[x.Seq] || onPath[c.Seq] {
				out[at].open = true
				walk(c, depth+1, onPath)
			} else {
				out[at].hidden += size(c.Seq)
			}
		}
	}
	for _, r := range roots {
		onPath := map[int64]bool{}
		for seq, ok := m.peek[r.Seq], true; ok; seq, ok = nextParent(bySeq, seq) {
			onPath[seq] = true
		}
		walk(r, 0, onPath)
	}
	return out
}

func nextParent(bySeq map[int64]bus.Message, seq int64) (int64, bool) {
	x, ok := bySeq[seq]
	if !ok || x.ReplyTo == nil {
		return 0, false
	}
	_, ok = bySeq[*x.ReplyTo]
	return *x.ReplyTo, ok
}

// rootSeq returns the thread root of seq among ch's loaded messages.
func (m *Model) rootSeq(ch string, seq int64) int64 {
	bySeq := map[int64]bus.Message{}
	for _, x := range m.paneMsgs(ch) {
		bySeq[x.Seq] = x
	}
	for {
		p, ok := nextParent(bySeq, seq)
		if !ok {
			return seq
		}
		seq = p
	}
}

// peekReply marks x, a reply just received on ch, as the one reply its
// thread shows while collapsed, replacing any earlier peek.
func (m *Model) peekReply(ch string, x bus.Message) {
	if x.ReplyTo == nil {
		return
	}
	if root := m.rootSeq(ch, x.Seq); root != x.Seq {
		m.peek[root] = x.Seq
	}
}

// cursorRow returns the stream row under the normal-mode cursor.
func (m *Model) cursorRow() (row, bool) {
	rs := m.rows(m.selName())
	if m.cursor < 0 || m.cursor >= len(rs) {
		return row{}, false
	}
	return rs[m.cursor], true
}

// toggleExpand shows or hides the direct replies of the cursor message.
func (m *Model) toggleExpand() {
	if r, ok := m.cursorRow(); ok {
		m.setExpanded(r, !m.expanded[r.msg.Seq])
	}
}

// rowAvail is the width left for a row's text after its tree prefix: two
// columns per depth level plus the two-column marker.
func (m *Model) rowAvail(depth int) int {
	return max(m.stream.Width, 20) - 2*depth - 2
}

// openCursor (→) opens the cursor row's body when it's closed and the row
// has one; otherwise it shows the row's direct replies.
func (m *Model) openCursor() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if _, hasBody := rowLine(r.msg, m.rowAvail(r.depth)); hasBody && !m.bodyOpen[r.msg.Seq] {
		m.bodyOpen[r.msg.Seq] = true
		m.refreshStream()
		m.scrollCursorIntoView()
		return
	}
	m.setExpanded(r, true)
}

// closeCursor (←) hides the replies under the cursor row (the whole
// subtree) when any are shown; otherwise it closes the row's body.
func (m *Model) closeCursor() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	if r.open {
		m.collapseCursor()
		return
	}
	delete(m.bodyOpen, r.msg.Seq)
	m.refreshStream()
	m.scrollCursorIntoView()
}

// collapseCursor hides everything under the cursor message, so a later
// expand shows direct replies only.
func (m *Model) collapseCursor() {
	r, ok := m.cursorRow()
	if !ok {
		return
	}
	under := map[int64]bool{r.msg.Seq: true}
	for _, x := range m.paneMsgs(m.selName()) { // seq order: a parent precedes its replies
		if x.ReplyTo != nil && under[*x.ReplyTo] {
			under[x.Seq] = true
			delete(m.expanded, x.Seq)
		}
	}
	m.setExpanded(r, false)
}

// setExpanded shows or hides r's direct replies. A change drops the
// thread's peek so the user's choice is what shows.
func (m *Model) setExpanded(r row, v bool) {
	if v {
		m.expanded[r.msg.Seq] = true
	} else {
		delete(m.expanded, r.msg.Seq)
	}
	delete(m.peek, r.root)
	m.refreshStream()
	m.scrollCursorIntoView()
}
