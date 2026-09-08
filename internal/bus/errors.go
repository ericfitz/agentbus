package bus

import (
	"encoding/json"
	"fmt"
)

// Error is the application error carried to the agent as a failed tool result.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string {
	j, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprintf(`{"code":"internal","message":%q,"retryable":true}`, err.Error())
	}
	return string(j)
}

// MaxErrorMessageBytes bounds a rendered error message so an oversized
// caller-controlled string (a cursor, channel name, key, ...) interpolated
// into the message cannot bypass result budgets. Exported so the MCP layer
// can apply the same bound to SDK-produced validation text.
const MaxErrorMessageBytes = 1024

// TruncateErrorMessage bounds s to MaxErrorMessageBytes, appending a
// trailing "…" marker when truncated. Truncation is byte-based (not
// rune-aware) since the bound exists purely to cap size, not to render
// cleanly.
func TruncateErrorMessage(s string) string {
	if len(s) <= MaxErrorMessageBytes {
		return s
	}
	return s[:MaxErrorMessageBytes-len("…")] + "…"
}

func errf(code string, retryable bool, format string, args ...any) *Error {
	return &Error{Code: code, Message: TruncateErrorMessage(fmt.Sprintf(format, args...)), Retryable: retryable}
}

// internal wraps an unexpected error so the agent sees code "internal".
func internal(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Code: "internal", Message: err.Error(), Retryable: true}
}
