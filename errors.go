package zsp

import (
	"context"
	"errors"
	"fmt"
)

type operationError struct {
	sentinel  error
	cause     error
	message   string
	retryable bool
	code      string
}

func (e *operationError) Error() string {
	if e.sentinel == nil {
		return e.message
	}
	return fmt.Sprintf("%s: %s", e.sentinel, e.message)
}

func (e *operationError) Unwrap() []error {
	var wrapped []error
	if e.sentinel != nil {
		wrapped = append(wrapped, e.sentinel)
	}
	if e.cause != nil {
		wrapped = append(wrapped, e.cause)
	}
	return wrapped
}

func (e *operationError) Retryable() bool { return e.retryable }
func (e *operationError) Code() string    { return e.code }

func operationErr(sentinel error, retryable bool, format string, args ...any) error {
	retryable = promisedRetryable(sentinel, retryable)
	return &operationError{
		sentinel:  sentinel,
		message:   fmt.Sprintf(format, args...),
		retryable: retryable,
	}
}

func operationCodeErr(sentinel error, code string, retryable bool, format string, args ...any) error {
	retryable = promisedRetryable(sentinel, retryable)
	return &operationError{
		sentinel:  sentinel,
		message:   fmt.Sprintf(format, args...),
		retryable: retryable,
		code:      code,
	}
}

func wrapOperationError(sentinel, cause error, retryable bool, action string) error {
	if cause == nil {
		return operationErr(sentinel, retryable, "%s", action)
	}
	retryable = promisedRetryable(sentinel, retryable)
	return &operationError{
		sentinel:  sentinel,
		cause:     safeCause(cause),
		message:   action,
		retryable: retryable,
	}
}

func promisedRetryable(sentinel error, retryable bool) bool {
	return retryable || errors.Is(sentinel, ErrRateLimited) || errors.Is(sentinel, ErrTemporaryFailure)
}

func safeCause(cause error) error {
	switch {
	case errors.Is(cause, context.Canceled):
		return context.Canceled
	case errors.Is(cause, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func contextOperationError(err error, action string) error {
	if errors.Is(err, context.Canceled) {
		return operationCodeErr(context.Canceled, "cancelled", false, "%s", action)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return operationCodeErr(context.DeadlineExceeded, "cancelled", false, "%s timed out", action)
	}
	return nil
}

var _ Error = (*operationError)(nil)
