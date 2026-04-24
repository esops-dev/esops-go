package client

import (
	"fmt"
	"net/http"
)

// ClassifyStatus maps an HTTP status to the sentinel error the rest of
// the codebase observes (auth/forbidden/unreachable). Returns nil for
// 2xx/3xx — adapters can short-circuit on nil. The `op` label is used
// in messages for 4xx-but-not-401/403 bodies; pass something short like
// "cat shards".
func ClassifyStatus(op string, statusCode int, body []byte) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return ErrForbidden
	}
	if statusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d: %s", op, statusCode, string(body))
	}
	return nil
}

// ClassifyTransport wraps a transport-level error (connection refused,
// TLS handshake, DNS) with ErrUnreachable so callers can errors.Is
// against it. The upstream clients expose this as a plain error, but
// exit-code stability depends on it being this sentinel.
func ClassifyTransport(err error) error {
	return fmt.Errorf("%w: %v", ErrUnreachable, err)
}
