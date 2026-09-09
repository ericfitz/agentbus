package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/bus"
)

// promptState is the one-line inline prompt used by "c" (create channel).
// While active it replaces the compose line and takes every key.
type promptState struct {
	active bool
	label  string
	input  textinput.Model
	accept func(m *Model, value string) tea.Cmd
}

// submitCompose sends the compose text to the selected channel. On a memory
// channel the bus turns the send into a memory. The text is kept for ↑
// recall, and a failed send leaves it there via the toast's hint: the user
// presses ↑ then enter.
func (m *Model) submitCompose() tea.Cmd {
	text := strings.TrimRight(m.compose.Value(), "\n ")
	ch := m.selName()
	if strings.TrimSpace(text) == "" || ch == "" {
		return nil
	}
	in := bus.SendInput{Channel: ch, Content: text}
	if m.replyTo != nil {
		seq := m.replyTo.Seq
		in.ReplyTo = &seq
	}
	m.lastSent = text
	m.replyTo = nil
	m.compose.Reset()
	m.fitCompose()
	c := m.c
	return func() tea.Msg {
		_, err := c.b.Send(c.as, in)
		return sentMsg{channel: ch, err: err}
	}
}

func (m *Model) openPrompt(label string, accept func(*Model, string) tea.Cmd) tea.Cmd {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 128
	// Static cursor: textinput's default Blink mode reschedules itself
	// forever (see the compose textarea's Cursor.SetMode note in model.go),
	// which never terminates against the fixture's synchronous cmd runner.
	in.Cursor.SetMode(cursor.CursorStatic)
	m.prompt = promptState{active: true, label: label, input: in, accept: accept}
	return m.prompt.input.Focus() // focus the copy the model keeps, not the local
}

func (m *Model) updatePrompt(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.prompt = promptState{}
		return nil
	case "enter":
		v := strings.TrimSpace(m.prompt.input.Value())
		accept := m.prompt.accept
		m.prompt = promptState{}
		if v == "" {
			return nil
		}
		return accept(m, v)
	}
	var cmd tea.Cmd
	m.prompt.input, cmd = m.prompt.input.Update(msg)
	return cmd
}

// createChannelPrompt asks "<name> [memory]": the kind is "memory" or
// omitted for ordinary; e.g. "notes memory".
func (m *Model) createChannelPrompt() tea.Cmd {
	return m.openPrompt("new channel <name> [memory]", func(m *Model, v string) tea.Cmd {
		name, kind, _ := strings.Cut(v, " ")
		kind = strings.TrimSpace(kind)
		if kind == "" {
			kind = "ordinary"
		}
		c := m.c
		// subscribedMsg's success path refreshes the status (and so the rail).
		return func() tea.Msg {
			ch, err := c.b.CreateChannel(c.as, name, kind)
			if err != nil {
				return sentMsg{channel: name, err: err}
			}
			return subscribedMsg{channel: name, err: c.subscribe(ch, "oldest")}
		}
	})
}

// toggleSubscribe unsubscribes the selected channel (its messages stop
// arriving; the rail still lists it) or subscribes it again from now.
func (m *Model) toggleSubscribe() tea.Cmd {
	ch := m.selected()
	if ch == nil {
		return nil
	}
	c := m.c
	name := ch.Name
	if c.isSubscribed(name) {
		if err := c.b.Unsubscribe(c.as, name); err != nil {
			return m.showToast("unsubscribe: " + errText(err))
		}
		c.forget(name)
		return m.showToast("unsubscribed from " + name + " (s to resubscribe)")
	}
	if err := c.subscribe(*ch, "now"); err != nil {
		return m.showToast("subscribe: " + errText(err))
	}
	return nil
}
