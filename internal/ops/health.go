// Package ops holds business logic for the `esops ops ...` command
// tree. Functions here depend on small capability interfaces defined
// in internal/client, never on upstream client types.
package ops

import (
	"context"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Health returns a cluster-health snapshot. For v0.1.0 it's a thin
// pass-through to the capability — the business value here is the
// neutral type and the exit-code-friendly error classification done
// at the adapter layer. Allocation-summary enrichment (§6 in CLAUDE.md)
// will be layered in as a subsequent change.
func Health(ctx context.Context, c client.HealthInspector) (types.ClusterHealth, error) {
	return c.Health(ctx)
}
