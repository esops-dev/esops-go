package client

import (
	"context"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// HealthInspector exposes read-only cluster-health operations.
type HealthInspector interface {
	Health(ctx context.Context) (types.ClusterHealth, error)
}

// Client is the composite surface returned by factory.New. It embeds
// cluster metadata (from the probe) plus every capability interface.
// Business-logic callers should depend on the capability interfaces, not
// on *Client directly.
type Client struct {
	Info types.ClusterInfo
	HealthInspector
}
