package tui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericfitz/agentbus/internal/bus"
)

type memState struct {
	channel string
	list    []bus.Message
	cursor  int
	revs    []bus.Message
	rev     int
	err     error
}

func (s memState) current() *bus.Message {
	if s.cursor < 0 || s.cursor >= len(s.list) {
		return nil
	}
	return &s.list[s.cursor]
}

func (s memState) currentID() int64 {
	if c := s.current(); c != nil && c.MemoryID != nil {
		return *c.MemoryID
	}
	return 0
}

// openMemories browses the selected channel if it is a memory channel, else
// the first memory channel in the rail.
func (m *Model) openMemories() tea.Cmd {
	ch := ""
	if c := m.selected(); c != nil && c.Kind == "memory" {
		ch = c.Name
	}
	if ch == "" {
		for _, c := range m.channels {
			if c.Kind == "memory" {
				ch = c.Name
				break
			}
		}
	}
	if ch == "" {
		return m.showToast("no memory channels · c then \"name memory\" creates one")
	}
	m.mem = memState{channel: ch}
	m.mode = modeMemories
	return m.loadMemoryList()
}

// loadMemoryList fetches the newest historyPage live memories of the
// channel. Every live row in a memory channel is a memory's current
// revision, keyed by memory_id (unchanged by edits); "newest first" orders
// by that id descending, not by the row's own seq, since editing a memory
// bumps its live seq without making it a newer memory.
func (m *Model) loadMemoryList() tea.Cmd {
	c := m.c
	ch := m.mem.channel
	return func() tea.Msg {
		top := int64(math.MaxInt64)
		ms, err := c.b.History(c.as, ch, &top, nil, historyPage)
		sort.SliceStable(ms, func(i, j int) bool { return memoryID(ms[i]) > memoryID(ms[j]) })
		return memListMsg{channel: ch, msgs: ms, err: err}
	}
}

func memoryID(x bus.Message) int64 {
	if x.MemoryID != nil {
		return *x.MemoryID
	}
	return 0
}

func (m *Model) loadRevisions() tea.Cmd {
	id := m.mem.currentID()
	if id == 0 {
		m.mem.revs, m.mem.rev = nil, 0
		return nil
	}
	c := m.c
	return func() tea.Msg {
		revs, err := c.b.MemoryRevisions(c.as, id)
		return revisionsMsg{id: id, revs: revs, err: err}
	}
}

func (m *Model) updateMemories(msg tea.Msg) tea.Cmd {
	k := keyString(msg)
	if m.mode == modeConfirmDelete {
		switch k {
		case "y":
			m.mode = modeMemories
			id := m.mem.currentID()
			c := m.c
			return func() tea.Msg { return memChangedMsg{id: id, err: c.b.DeleteMemory(c.as, id, "")} }
		case "n", "esc":
			m.mode = modeMemories
		}
		return nil
	}
	switch k {
	case "esc":
		m.mode = modeNormal
	case "up", "k":
		m.mem.cursor = max(m.mem.cursor-1, 0)
		return m.loadRevisions()
	case "down", "j":
		m.mem.cursor = min(m.mem.cursor+1, max(len(m.mem.list)-1, 0))
		return m.loadRevisions()
	case "left":
		m.mem.rev = max(m.mem.rev-1, 0)
	case "right":
		m.mem.rev = min(m.mem.rev+1, max(len(m.mem.revs)-1, 0))
	case "e":
		return m.editMemoryInEditor()
	case "d":
		if m.mem.current() != nil {
			m.mode = modeConfirmDelete
		}
	}
	return nil
}

// memEditPath is the temp file the editor opens for the current memory.
func (m *Model) memEditPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agentbus-memory-%d.md", m.mem.currentID()))
}

func editorCommand(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	parts := strings.Fields(ed)
	return exec.Command(parts[0], append(parts[1:], path)...)
}

// editMemoryInEditor writes the current revision to a temp file and hands
// the terminal to the editor; memEditedMsg arrives when it exits.
func (m *Model) editMemoryInEditor() tea.Cmd {
	cur := m.mem.current()
	if cur == nil {
		return nil
	}
	path := m.memEditPath()
	if err := os.WriteFile(path, []byte(cur.Content+"\n"), 0o600); err != nil {
		return m.showToast("edit: " + err.Error())
	}
	id := *cur.MemoryID
	return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg { return memEditedMsg{id: id, path: path, err: err} })
}

// applyMemoryEdit reads the editor's file back, removes it, and edits the
// memory when the content changed.
func (m *Model) applyMemoryEdit(msg memEditedMsg) tea.Cmd {
	defer func() { _ = os.Remove(msg.path) }()
	if msg.err != nil {
		return m.showToast("editor: " + msg.err.Error())
	}
	body, err := os.ReadFile(msg.path)
	if err != nil {
		return m.showToast("edit: " + err.Error())
	}
	content := strings.TrimRight(string(body), "\n")
	cur := m.mem.current()
	if cur == nil || content == "" || content == cur.Content {
		return m.showToast("memory unchanged")
	}
	c := m.c
	id := msg.id
	return func() tea.Msg {
		_, err := c.b.EditMemory(c.as, bus.EditInput{ID: id, Content: content})
		return memChangedMsg{id: id, err: err}
	}
}

// viewMemories draws the memory list windowed to what fits above a detail
// section (revision content, type, refs), same treatment as viewSearch: the
// list can hold up to historyPage rows, so every row is width-truncated and
// only a window around the cursor is shown, with the title noting when it
// is windowed.
func (m Model) viewMemories() string {
	th := m.theme
	dim := th.Style(th.Dim)
	w, h := m.overlaySize()
	trunc := lipgloss.NewStyle().MaxWidth(w - 4)

	detailRows := 1
	if len(m.mem.revs) > 0 {
		detailRows = 8
	}
	visible := max(h-4-detailRows-1, 1)
	start, end := window(m.mem.cursor, len(m.mem.list), visible)

	var b strings.Builder
	for i := start; i < end; i++ {
		x := m.mem.list[i]
		id, rev := int64(0), int64(0)
		if x.MemoryID != nil {
			id = *x.MemoryID
		}
		if x.Revision != nil {
			rev = *x.Revision
		}
		title := strings.SplitN(x.Content, "\n", 2)[0]
		line := fmt.Sprintf("#%d r%d %s %s", id, rev, title, dim.Render(clock(x.CreatedAt)))
		if i == m.mem.cursor {
			line = th.Style(th.Agent).Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(trunc.Render(line) + "\n")
	}
	if m.mem.err != nil {
		b.WriteString(th.Style(th.Error).Render("✗ "+errText(m.mem.err)) + "\n")
	}
	if len(m.mem.list) == 0 {
		b.WriteString(dim.Render("no memories in "+m.mem.channel) + "\n")
	}
	if len(m.mem.revs) > 0 {
		r := m.mem.revs[min(m.mem.rev, len(m.mem.revs)-1)]
		b.WriteString("\n" + dim.Render(fmt.Sprintf("#%d · revision %d of %d · %s · %s", m.mem.currentID(), m.mem.rev+1, len(m.mem.revs), r.Sender, clock(r.CreatedAt))) + "\n")
		lines := strings.Split(r.Content, "\n")
		if maxLines := detailRows - 3; len(lines) > maxLines {
			more := len(lines) - maxLines
			b.WriteString(strings.Join(lines[:maxLines], "\n") + "\n")
			b.WriteString(dim.Render(fmt.Sprintf("… (%d more lines)", more)) + "\n")
		} else {
			b.WriteString(r.Content + "\n")
		}
		if r.Type != "" {
			b.WriteString(dim.Render("type ") + r.Type + "\n")
		}
		if len(r.Refs) > 0 {
			refs := make([]string, len(r.Refs))
			for i, ref := range r.Refs {
				refs[i] = ref.Value
			}
			b.WriteString(dim.Render("refs ") + strings.Join(refs, " · ") + "\n")
		}
	}
	if m.mode == modeConfirmDelete {
		cur := m.mem.current()
		title := strings.SplitN(cur.Content, "\n", 2)[0]
		b.WriteString("\n" + th.Style(th.Error).Render(fmt.Sprintf("delete memory #%d “%s”? tombstones all revisions · y yes  n no", m.mem.currentID(), title)))
	}
	count := fmt.Sprintf(" · %d memories", len(m.mem.list))
	if end-start < len(m.mem.list) {
		count = fmt.Sprintf(" · showing %d-%d of %d memories", start+1, end, len(m.mem.list))
	}
	title := th.Style(th.Mem).Render("◆ "+m.mem.channel) + dim.Render(count)
	return m.overlay(title, th.Mem, b.String(), "e edit  d delete  ← → revision  ↑↓ move  esc close")
}
