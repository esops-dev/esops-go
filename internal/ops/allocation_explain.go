package ops

import (
	"context"
	"fmt"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// AllocationExplain returns an allocation-explain record for either a
// specific shard (req.Index set) or the first unassigned shard the
// cluster finds (req.Index empty). The cluster is the source of truth;
// the business layer only validates the request shape so a bad flag
// combination fails fast instead of surfacing a 400 from the cluster.
func AllocationExplain(ctx context.Context, c client.AllocationExplainer, req types.AllocationExplainRequest) (types.AllocationExplanation, error) {
	if req.Index == "" && req.Shard != 0 {
		return types.AllocationExplanation{}, fmt.Errorf("--shard requires --index (cluster picks the shard when --index is omitted)")
	}
	if req.Index == "" && req.Primary {
		return types.AllocationExplanation{}, fmt.Errorf("--primary requires --index (cluster picks the shard when --index is omitted)")
	}
	return c.AllocationExplain(ctx, req)
}
