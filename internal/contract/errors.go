package contract

import "fmt"

// Error carries only safe public information, never raw Git stderr or a local path.
type Error struct {
	Code           string
	Message        string
	HTTPStatus     int
	Retryable      bool
	SuggestedQuery string
	QueryFragment  string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func Fail(code, message string, status int) error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}
