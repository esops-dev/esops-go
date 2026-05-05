package ops

import (
	"context"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ListPendingTasks returns the master's accepted-but-not-yet-applied
// cluster-state tasks. ClusterHealth.NumberOfPendingTasks gives the
// count; this is the per-task view operators reach for when triaging
// a slow master.
func ListPendingTasks(ctx context.Context, c client.PendingTasksInspector) ([]types.PendingTask, error) {
	return c.PendingTasks(ctx)
}
