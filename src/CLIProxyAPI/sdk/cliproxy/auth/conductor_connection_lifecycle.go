package auth

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gorilla/websocket"
)

// shouldSkipCredentialCooldown reports failures that must not mark auth/model cooling.
// Connection lifecycle is intentionally separate from request_scoped so transport
// drops do not also stop credential rotation via isRequestInvalidError.
func shouldSkipCredentialCooldown(err *Error) bool {
	return isRequestScopedResultError(err) || isConnectionLifecycleResultError(err)
}

// isConnectionLifecycleError reports transport/session lifecycle failures that must
// not cool credentials: client cancellation and WebSocket close/EOF disconnects.
func isConnectionLifecycleError(err error) bool {
	if err == nil {
		return false
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr != nil {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure:
			return true
		}
	}
	if statusCodeFromError(err) != 0 {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return isConnectionLifecycleMessage(err.Error())
}

func isConnectionLifecycleResultError(err *Error) bool {
	if err == nil {
		return false
	}
	if err.Code == connectionLifecycleErrorCode {
		return true
	}
	if statusCodeFromResult(err) != 0 {
		return false
	}
	return isConnectionLifecycleMessage(err.Message)
}

func isConnectionLifecycleMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	switch lower {
	case "context canceled", "context deadline exceeded", "eof", "unexpected eof":
		return true
	}
	if strings.Contains(lower, "context canceled") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "context cancelled") {
		return true
	}
	if strings.Contains(lower, "websocket: close 1000") ||
		strings.Contains(lower, "websocket: close 1001") ||
		strings.Contains(lower, "websocket: close 1006") {
		return true
	}
	if strings.Contains(lower, "unexpected eof") {
		return true
	}
	return false
}
