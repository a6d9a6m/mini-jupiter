package errors

import (
	"fmt"
)

type Error struct {
	Code    int
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%d:%s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%d:%s", e.Code, e.Message)
}

func Wrap(code int, msg string, err error) *Error {
	return &Error{Code: code, Message: msg, Cause: err}
}

func New(code int, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

type Reporter func(code int)

var reporter Reporter

func SetReporter(r Reporter) {
	reporter = r
}

func report(code int) {
	if reporter != nil {
		reporter(code)
	}
}
