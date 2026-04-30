package client

import (
	"context"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// HealthInspector exposes read-only cluster-health operations.
type HealthInspector interface {
	Health(ctx context.Context) (types.ClusterHealth, error)
}

// ShardInspector exposes read-only shard-allocation operations. Kept
// separate from HealthInspector so a future capability (e.g. a cached
// allocation summariser) can implement just this one.
type ShardInspector interface {
	Shards(ctx context.Context, filter types.ShardFilter) ([]types.Shard, error)
}

// NodeInspector exposes /_cat/nodes — the cluster-side source of truth
// for node names and roles. Used by ops shards analyze to identify the
// real set of data nodes for skew analysis: a "shard count per node"
// derived from /_cat/shards alone misses freshly-added or drained data
// nodes (zero shards = invisible) which is the most interesting case
// to surface.
type NodeInspector interface {
	Nodes(ctx context.Context) ([]types.Node, error)
}

// IndexInspector exposes read-only index-listing operations backed by
// /_cat/indices. The filter's Index field is forwarded to the cluster;
// Health/Status/Hidden/Sort are applied in the business layer so the
// same filter produces the same view on ES and OS.
type IndexInspector interface {
	Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error)
}

// IndexStateChanger exposes /{index}/_open and /{index}/_close. Both
// endpoints share the same request shape (a comma-separated index
// pattern plus a small set of query params) and the same response
// envelope (acknowledged + shards_acknowledged) on both dialects, so
// one capability covers the pair.
//
// The cluster has no dry_run query flag for these endpoints; the
// business layer implements --dry-run client-side by resolving the
// pattern via IndexInspector and reporting the would-be transition
// without firing the call. Implementations therefore only need to
// execute the live transition.
//
// Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (the typical case is a missing index when
//     IgnoreUnavailable is false)
type IndexStateChanger interface {
	OpenIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error)
	CloseIndex(ctx context.Context, req types.IndexStateChangeRequest) (types.IndexStateChange, error)
}

// AllocationExplainer exposes /_cluster/allocation/explain. An empty
// request.Index lets the cluster pick any unassigned shard it finds —
// matching the raw endpoint's default behaviour — so on-call operators
// can drop straight into "why is anything unassigned" without having to
// name a specific shard first.
type AllocationExplainer interface {
	AllocationExplain(ctx context.Context, req types.AllocationExplainRequest) (types.AllocationExplanation, error)
}

// SecurityAuditor exposes the user/role surface of the cluster's
// security model. The implementation is dialect-aware: ES reads the
// native-realm endpoints under /_security/*; OS reads the security
// plugin's admin API under /_plugins/_security/api/*. When the cluster
// has no security in play at all (ES with xpack.security.enabled=false,
// OS without the plugin), implementations return Status.Enabled=false
// with an empty Users/Roles slice rather than an error — "no security"
// is a valid audit outcome that operators want surfaced, not hidden
// behind a failed command.
type SecurityAuditor interface {
	Audit(ctx context.Context) (types.SecurityReport, error)
}

// HotThreadsInspector wraps /_nodes/hot_threads — the JVM introspection
// endpoint both dialects expose in the same text format. Response bodies
// are not JSON and the schema is an implementation detail of the
// cluster's code; the capability keeps it as an opaque Text blob so the
// upstream format can change without touching esops.
type HotThreadsInspector interface {
	HotThreads(ctx context.Context, req types.HotThreadsRequest) (types.HotThreads, error)
}

// RerouteExecutor wraps /_cluster/reroute — the cluster-state mutation
// behind `ops rebalance`. The endpoint shape is identical on both
// dialects (POST a body of {commands: [...]}, plus dry_run/explain/
// retry_failed query flags) so one capability covers ES and OS.
//
// Implementations must surface the cluster's own acknowledged flag and
// any explanations[] block verbatim — operators rely on the cluster's
// per-decider prose to triage a refused move; rephrasing it would lose
// signal. Errors:
//   - ErrUnreachable on transport failure
//   - ErrAuth / ErrForbidden on 401/403
//   - generic wrapped error on 4xx with the cluster's body in the
//     message (typical case: a bad command shape that the cluster
//     rejects with a 400 + reason)
type RerouteExecutor interface {
	Reroute(ctx context.Context, req types.RerouteRequest) (types.RerouteResult, error)
}

// ClusterDiagnoser performs raw HTTP GETs against cluster endpoints for
// the `ops diag` bundle. Purposely minimal — a generic raw-HTTP escape
// hatch is a power tool we only want the diag collector using, so the
// rest of the codebase keeps talking through dialect-aware capabilities.
//
// Errors:
//   - transport failures (DNS, TCP, TLS): wrapped ErrUnreachable
//   - HTTP 401: ErrAuth
//   - HTTP 403: ErrForbidden
//   - any other non-2xx: returned as (body, status, nil) so the caller
//     can record the body in the bundle. A missing-endpoint 404 on a
//     dialect-specific path (ILM on OS, ISM on ES) is expected data,
//     not a bundle-breaking error.
type ClusterDiagnoser interface {
	DoGET(ctx context.Context, path string) (body []byte, status int, err error)
}

// Client is the composite surface returned by the cluster factory: the
// probed cluster metadata plus one capability per feature area.
// Business-logic callers should depend on the capability interfaces, not
// on *Client directly — composing (rather than embedding) the interfaces
// is what makes that a compile-time constraint instead of a convention.
type Client struct {
	Info              types.ClusterInfo
	Health            HealthInspector
	Shards            ShardInspector
	Nodes             NodeInspector
	Indices           IndexInspector
	IndexState        IndexStateChanger
	AllocationExplain AllocationExplainer
	Security          SecurityAuditor
	Diag              ClusterDiagnoser
	HotThreads        HotThreadsInspector
	Reroute           RerouteExecutor
}
