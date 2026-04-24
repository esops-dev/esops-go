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

// Client is the composite surface returned by factory.New. It embeds
// cluster metadata (from the probe) plus every capability interface.
// Business-logic callers should depend on the capability interfaces, not
// on *Client directly.
type Client struct {
	Info types.ClusterInfo
	HealthInspector
	ShardInspector
}
