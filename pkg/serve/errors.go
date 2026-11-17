package serve

import (
	"context"
	"errors"
	"net"
)

// RemoteError is a failure to talk to a run's endpoint. Its message is a fixed category, never the cause's text:
// net/http quotes a redirect's Location, credentials included, before CheckRedirect can refuse it. The cause
// stays reachable through errors.Is and errors.As.
type RemoteError struct {
	Op    string
	cause error
}

func (e *RemoteError) Error() string {
	var timeout net.Error
	switch {
	case errors.Is(e.cause, context.Canceled):
		return e.Op + ": the request was cancelled"
	case errors.Is(e.cause, context.DeadlineExceeded), errors.As(e.cause, &timeout) && timeout.Timeout():
		return e.Op + ": the request timed out"
	default:
		return e.Op + ": the request failed"
	}
}

func (e *RemoteError) Unwrap() error { return e.cause }

// remoteFailure hides the text of err behind a RemoteError; errors the host itself worded pass unchanged.
func remoteFailure(op string, err error) error {
	var (
		remote   *RemoteError
		changed  *RunChangedError
		unknown  *OutcomeUnknownError
		capacity *WaitCapacityError
	)
	if err == nil || answeredByRun(err) || errors.As(err, &remote) || errors.As(err, &changed) ||
		errors.As(err, &unknown) || errors.As(err, &capacity) || errors.Is(err, errIdentityChanged) ||
		errors.Is(err, errShuttingDown) {
		return err
	}
	return &RemoteError{Op: op, cause: err}
}
