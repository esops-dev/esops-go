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

// IndexInspector exposes read-only index-listing operations backed by
// /_cat/indices. The filter's Index field is forwarded to the cluster;
// Health/Status/Hidden/Sort are applied in the business layer so the
// same filter produces the same view on ES and OS.
type IndexInspector interface {
	Indices(ctx context.Context, filter types.IndexFilter) ([]types.Index, error)
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
	Indices           IndexInspector
	AllocationExplain AllocationExplainer
	Security          SecurityAuditor
	Diag              ClusterDiagnoser
	HotThreads        HotThreadsInspector
}
