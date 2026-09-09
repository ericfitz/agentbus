//nolint:unused // message types for later TUI tasks; see task briefs 5-10
package tui

import "github.com/ericfitz/agentbus/internal/bus"

// Messages delivered to Model.Update. Every bus call the model makes runs in
// a tea.Cmd and comes back as one of these; the receive goroutine sends
// batchMsg and receiveErrMsg directly.
type (
	batchMsg      struct{ res bus.ReceiveResult }
	receiveErrMsg struct{ err error }
	statusTickMsg struct{}
	statusMsg     struct {
		st  bus.Status
		err error
	}
	historyMsg struct {
		channel string
		msgs    []bus.Message
		prepend bool
		err     error
	}
	sentMsg struct {
		channel string
		err     error
	}
	searchMsg struct {
		res bus.SearchResult
		err error
	}
	memListMsg struct {
		channel string
		msgs    []bus.Message
		err     error
	}
	revisionsMsg struct {
		id   int64
		revs []bus.Message
		err  error
	}
	memEditedMsg struct {
		id   int64
		path string
		err  error
	}
	memChangedMsg struct { // an edit or delete finished; reload the list
		id  int64
		err error
	}
	configEditedMsg struct{ err error }
	toastClearMsg   struct{ seq int }
	subscribedMsg   struct {
		channel string
		err     error
	}
)
