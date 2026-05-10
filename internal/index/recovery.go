package index

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// RecoveryOptions are the knobs the CLI layer hands to Recovery. Pattern
// is forwarded to the cluster as the path indices selector — empty
// means "every index". ActiveOnly maps to ?active_only=true so the
// cluster only echoes shards that are still in progress (the common
// case when an operator is watching a restore or a `shrink` settle);
// passing false yields the full DONE history alongside the active set.
type RecoveryOptions struct {
	Pattern    string
	ActiveOnly bool
}

// Recovery is the business-layer entry point for `esops index recovery`.
// It forwards the pattern to the cluster verbatim (the same expansion
// /_recovery already performs) and sorts the per-index slice by name
// so rendered output is stable across calls. Sorting on the business
// side keeps the adapter free of presentation concerns; the wire
// translator already gives us a name-sorted slice but a future
// streaming variant would want the same guarantee here.
func Recovery(ctx context.Context, c client.RecoveryInspector, opts RecoveryOptions) (types.RecoveryReport, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	report, err := c.Recovery(ctx, types.RecoveryRequest{
		Indices:    pattern,
		ActiveOnly: opts.ActiveOnly,
	})
	if err != nil {
		return types.RecoveryReport{}, err
	}
	sort.SliceStable(report.Indices, func(i, j int) bool {
		return report.Indices[i].Index < report.Indices[j].Index
	})
	return report, nil
}
