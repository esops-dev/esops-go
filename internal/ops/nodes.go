package ops

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Nodes returns the cluster's node list with role/version/heap/disk
// enrichment populated by the adapter. Rows are sorted alphabetically
// by name so the table view is stable across runs and dialects — the
// cluster's /_cat/nodes order is implementation-defined and shifts
// after a master election.
func Nodes(ctx context.Context, c client.NodeInspector) ([]types.Node, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	return nodes, nil
}
