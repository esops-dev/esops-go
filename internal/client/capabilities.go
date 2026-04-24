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
}
