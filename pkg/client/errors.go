package client

import (
	"errors"
	"fmt"
)

// UsageError formats a message and wraps it with ErrUsage so the
// exit-code mapper can surface the operator-input class. Use it at
// validation sites in the business layer (missing flag, pattern where
// literal required, mutually-exclusive flags both set).
func UsageError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUsage}, args...)...)
}

// Sentinel errors that callers can use errors.Is against. They map
// to stable esops exit codes in cmd/esops.
var (
	// ErrUnreachable — the cluster could not be contacted at all
	// (DNS, TCP, TLS handshake, non-HTTP response). Exit code 3.
	ErrUnreachable = errors.New("cluster unreachable")

	// ErrAuth — HTTP 401 from the cluster. Exit code 4.
	ErrAuth = errors.New("authentication failed")

	// ErrForbidden — HTTP 403 from the cluster. Exit code 5.
	ErrForbidden = errors.New("authorization failed")

	// ErrUnsupported — operation not supported by the current dialect
	// (e.g. ILM on an OpenSearch cluster). Exit code 6.
	ErrUnsupported = errors.New("operation not supported on this cluster dialect")

	// ErrUnknownProduct — GET / returned something we don't recognise
	// as either Elasticsearch or OpenSearch.
	ErrUnknownProduct = errors.New("cluster is neither Elasticsearch nor OpenSearch")

	// ErrDryRunBlocker — a dry-run (or live operation) reported a
	// blocker the cluster will not execute. The canonical case is
	// /_cluster/reroute returning acknowledged=false with an
	// explanations[] block describing why. Exit code 7.
	ErrDryRunBlocker = errors.New("dry-run reported a blocker")

	// ErrBlastRadius — a destructive operation refused to proceed
	// because its resolved scope exceeds the safety threshold and the
	// purpose-built opt-in flag (e.g. --max-indices, --allow-large-prune)
	// was not set. Exit code 8.
	ErrBlastRadius = errors.New("blast-radius limit exceeded")

	// ErrNotFound — the cluster reported a 404 for a literal-name
	// lookup (template, ILM policy, ISM policy). Surfaced separately
	// so the business layer can produce a precise "X not found"
	// message without scanning a list response.
	ErrNotFound = errors.New("not found")

	// ErrUsage — the operator's input was wrong (missing flag,
	// pattern where literal required, mutually-exclusive flags both
	// set, …). Maps to exit code 2 — distinct from generic exit 1
	// so CI scripts can tell "I called esops wrong" from "esops or
	// the cluster blew up".
	ErrUsage = errors.New("usage error")

	// ErrResponseTooLarge — a cluster response exceeded the configured
	// body cap before completing. Protects the CLI from buffering
	// hundreds of MB of an unbounded `ism explain --show-policy` or
	// similar runaway read into memory.
	ErrResponseTooLarge = errors.New("response body exceeds size cap")
)
