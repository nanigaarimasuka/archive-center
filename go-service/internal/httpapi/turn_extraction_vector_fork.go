package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// embeddingStatusError is a non-2xx answer from an embedding provider.
type embeddingStatusError struct {
	code    int
	message string
}

func (e *embeddingStatusError) Error() string { return e.message }

// embeddingRetryDelay is the pause before retry n (1-based), growing to 3s.
var embeddingRetryDelay = func(n int) time.Duration {
	return min(time.Duration(n)*500*time.Millisecond, 3*time.Second)
}

// embeddingErrorRetryable reports a failure another try may not repeat: the
// try's own timeout, a failed connection, rate limiting or a server error.
// A refused or malformed request fails the same way again.
func embeddingErrorRetryable(err error) bool {
	var status *embeddingStatusError
	if errors.As(err, &status) {
		return status.code == http.StatusTooManyRequests || status.code >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}
