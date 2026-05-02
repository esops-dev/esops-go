package clientutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
)

// ClassifyStatus maps an HTTP status to the sentinel error the rest of
// the codebase observes (auth/forbidden/unreachable). Returns nil for
// 2xx/3xx — adapters can short-circuit on nil. The `op` label is used
// in messages for 4xx-but-not-401/403 bodies; pass something short like
// "cat shards".
//
// For 4xx/5xx responses we try to extract the cluster's own
// error.reason / error.type from the body. Both ES and OS return the
// same envelope shape, and the `reason` field is the load-bearing line
// — operators reading the message want "no such index [logs]", not a
// 600-character JSON blob with an embedded stack trace. When the body
// isn't a recognisable envelope we fall back to a clipped raw snippet
// so unknown error formats don't go silent.
func ClassifyStatus(op string, statusCode int, body []byte) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return client.ErrAuth
	case http.StatusForbidden:
		return client.ErrForbidden
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s returned HTTP 404: %s", client.ErrNotFound, op, formatClusterError(body))
	}
	if statusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d: %s", op, statusCode, formatClusterError(body))
	}
	return nil
}

// ClassifyTransport wraps a transport-level error (connection refused,
// TLS handshake, DNS) with ErrUnreachable so callers can errors.Is
// against it. The upstream clients expose this as a plain error, but
// exit-code stability depends on it being this sentinel.
func ClassifyTransport(err error) error {
	return fmt.Errorf("%w: %v", client.ErrUnreachable, err)
}

// formatClusterError extracts a one-line operator-readable string from
// an ES/OS error response body. Three shapes seen in the wild:
//
//  1. {"error":{"type":"...","reason":"...","root_cause":[...]}, "status":N}
//     — the typical typed error.
//  2. {"error":"<bare string>"} — the 9.x "no handler found" form and
//     some plugin endpoints.
//  3. anything else (HTML 502 page, plain text) — we clip to a snippet.
//
// The output never embeds newlines and is bounded in length so the
// resulting error fits one terminal line. Operators chasing the full
// body still have it via --log-level=debug on the response logger.
func formatClusterError(body []byte) string {
	if len(body) == 0 {
		return "(empty response body)"
	}
	var typed struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &typed); err == nil {
		switch {
		case typed.Error.Type != "" && typed.Error.Reason != "":
			return fmt.Sprintf("%s: %s", typed.Error.Type, typed.Error.Reason)
		case typed.Error.Reason != "":
			return typed.Error.Reason
		case typed.Error.Type != "":
			return typed.Error.Type
		}
	}
	var bare struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &bare); err == nil && bare.Error != "" {
		return bare.Error
	}
	return clipErrorSnippet(body)
}

// clipErrorSnippet renders a non-JSON error body as a bounded one-line
// snippet. Long HTML 502 pages and multi-line stack traces become
// readable in a terminal.
func clipErrorSnippet(body []byte) string {
	const max = 200
	s := strings.TrimSpace(string(body))
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}
