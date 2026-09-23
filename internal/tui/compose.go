package tui

import (
	"fmt"
	"slices"
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
	// A reply while viewing the TUI's own inbox goes back to the sender, not
	// into the inbox being viewed. The pane also shows the TUI's own outgoing
	// messages (#4); a reply to one of those stays in the partner's inbox,
	// the channel it was sent to.
	if m.replyTo != nil && ch == bus.DMChannel(m.c.as) {
		ch = bus.DMChannel(m.replyTo.Sender)
		if m.replyTo.Sender == m.c.as {
			ch = m.replyTo.Channel
		}
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
	if isTagPane(ch.Name) {
		if err := m.c.b.UnsubscribeTags(m.c.as, tagPaneSet(ch.Name)); err != nil {
			return m.showToast("unsubscribe: " + errText(err))
		}
		return tea.Batch(m.showToast("unsubscribed from tags "+strings.TrimPrefix(ch.Name, tagPanePrefix)), m.statusCmd())
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

// updateConfirmChannel answers the delete-channel prompt: y deletes the
// selected channel (the TUI's own subscription is excepted, since it
// subscribes to every channel), any other key cancels.
func (m *Model) updateConfirmChannel(msg tea.Msg) tea.Cmd {
	k := keyString(msg)
	if k == "" {
		return nil
	}
	m.mode = modeNormal
	ch := m.selected()
	if k != "y" || ch == nil {
		return nil
	}
	c := m.c
	name := ch.Name
	if _, err := c.b.DeleteChannel(name, c.as); err != nil {
		return m.showToast("delete: " + errText(err))
	}
	c.forget(name)
	m.channels = slices.DeleteFunc(m.channels, func(x bus.Channel) bool { return x.Name == name })
	m.sel = min(m.sel, len(m.channels)-1)
	return m.showToast("deleted channel " + name)
}

// confirmChannelLine is the compose-row text while modeConfirmChannel is up.
func (m Model) confirmChannelLine() string {
	ch := m.selected()
	if ch == nil {
		return ""
	}
	noun := "messages"
	if ch.Kind == "memory" {
		noun = "memories"
	}
	return m.theme.Style(m.theme.Error).Render(fmt.Sprintf("delete channel %s and PERMANENTLY destroy its %d %s? no undo · y yes  any other key no", ch.Name, ch.Messages, noun))
}
