// Package ops holds business logic for the `esops ops ...` command
// tree. Functions here depend on small capability interfaces defined
// in internal/client, never on upstream client types.
package ops

import (
	"context"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Health returns a cluster-health snapshot.
func Health(ctx context.Context, c client.HealthInspector) (types.ClusterHealth, error) {
	return c.Health(ctx)
}
