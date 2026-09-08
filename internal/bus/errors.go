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

func errf(code string, retryable bool, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Retryable: retryable}
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
