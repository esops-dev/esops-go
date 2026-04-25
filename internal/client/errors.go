package client

import "errors"

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
)
