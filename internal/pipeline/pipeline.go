// Package pipeline holds the business logic behind the
// `esops pipeline ...` command tree. Like its sibling packages, it
// depends only on the small capability interfaces from
// pkg/client — never on a specific dialect's adapter — so the same
// code path covers Elasticsearch and OpenSearch.
package pipeline

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ListOptions narrows a `pipeline list` call. Names is a repeatable
// filter forwarded to the inspector; both ES and OS honour a
// comma-joined name list at the path segment, so multi-name fans out
// in a single round-trip. Empty Names returns every configured
// pipeline.
type ListOptions struct {
	Names []string
}

// List returns the cluster's ingest pipelines, sorted by name for
// deterministic output. Pure pass-through over the capability today;
// kept as its own function so a future filter (e.g. by processor
// type) lands in one place.
func List(ctx context.Context, c client.IngestPipelineInspector, opts ListOptions) ([]types.IngestPipeline, error) {
	rows, err := c.ListPipelines(ctx, opts.Names)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// Get fetches one ingest pipeline by literal id. Patterns are refused
// up-front — both products would happily return a multi-pipeline map
// for a wildcard, but `pipeline get` is the single-id verb. Use
// `pipeline list --name a,b` for multi-name lookups.
func Get(ctx context.Context, c client.IngestPipelineInspector, name string) (types.IngestPipeline, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return types.IngestPipeline{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.IngestPipeline{}, client.UsageError("--name must be a literal pipeline name, not a pattern (got %q)", name)
	}
	return c.GetPipeline(ctx, name)
}
