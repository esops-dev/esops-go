package ops

import (
	"context"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ListDeprecations returns the cluster's pre-upgrade deprecation
// findings, flattened across the cluster's per-category groupings
// (cluster_settings, node_settings, index_settings, ml_settings,
// data_streams, templates, ilm_policies). Returns ErrUnsupported
// against an OpenSearch cluster — /_migration/deprecations is an
// ES-only endpoint.
func ListDeprecations(ctx context.Context, c client.DeprecationInspector, req types.DeprecationsRequest) ([]types.DeprecationIssue, error) {
	return c.Deprecations(ctx, req)
}
