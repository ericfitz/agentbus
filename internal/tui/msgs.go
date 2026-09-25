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
		st   bus.Status
		tags [][]string
		err  error
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
		query string // the query that produced res, so a stale response (search reopened, or a newer search in flight) is ignored
		res   bus.SearchResult
		err   error
	}
	revisionsMsg struct {
		seq  int64 // the live row the fetch was for
		revs []bus.Message
		err  error
	}
	configEditedMsg struct{ err error }
	toastClearMsg   struct{ seq int }
	subscribedMsg   struct {
		channel string
		err     error
	}
	tasksMsg struct {
		ch    string
		tasks []bus.TaskSummary
		err   error
	}
	taskMsg struct {
		task bus.Task
		err  error
	}
)
