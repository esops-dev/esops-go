// Package index holds the business logic behind the `esops index ...`
// command tree. It depends only on the small capability interfaces from
// internal/client, never on a specific dialect's adapter.
package index

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// ListStream is the streaming companion to List: invokes yield per
// matching row as the cluster's response is decoded. Filtering matches
// List byte-for-byte (hidden / health / status). Sorting is *not*
// applied — sort buffers the whole result set, which defeats the
// streaming purpose. Callers that need a sorted view (table mode,
// JSON, YAML) use List; NDJSON consumers that want the full set in
// shell-pipe-able order should pipe through `sort` themselves.
func ListStream(ctx context.Context, c client.IndexInspector, f types.IndexFilter, yield func(types.Index) error) error {
	wantHealth := strings.ToLower(strings.TrimSpace(f.Health))
	wantStatus := strings.ToLower(strings.TrimSpace(f.Status))
	return c.IndicesStream(ctx, f, func(ix types.Index) error {
		if !f.Hidden && strings.HasPrefix(ix.Name, ".") {
			return nil
		}
		if f.ExcludeManaged && isDataStreamBackingIndex(ix.Name) {
			return nil
		}
		if wantHealth != "" && strings.ToLower(ix.Health) != wantHealth {
			return nil
		}
		if wantStatus != "" && strings.ToLower(ix.Status) != wantStatus {
			return nil
		}
		return yield(ix)
	})
}

// List returns the filtered, sorted index view. The cluster honours
// Index and (server-side, via expand_wildcards) Hidden — but we still
// drop dot-prefixed names client-side as defence-in-depth: cluster
// versions occasionally surface system indices regardless of the
// expand_wildcards request, and a leaked .kibana row in the default
// view is a real surprise. Health/Status/Sort are always applied here.
func List(ctx context.Context, c client.IndexInspector, f types.IndexFilter) ([]types.Index, error) {
	raw, err := c.Indices(ctx, f)
	if err != nil {
		return nil, err
	}

	wantHealth := strings.ToLower(strings.TrimSpace(f.Health))
	wantStatus := strings.ToLower(strings.TrimSpace(f.Status))

	out := raw[:0]
	for _, ix := range raw {
		if !f.Hidden && strings.HasPrefix(ix.Name, ".") {
			continue
		}
		if f.ExcludeManaged && isDataStreamBackingIndex(ix.Name) {
			continue
		}
		if wantHealth != "" && strings.ToLower(ix.Health) != wantHealth {
			continue
		}
		if wantStatus != "" && strings.ToLower(ix.Status) != wantStatus {
			continue
		}
		out = append(out, ix)
	}

	sortIndices(out, f.Sort)
	return out, nil
}

// isDataStreamBackingIndex reports whether name follows the
// data-stream backing-index convention shared by ES and OS:
// `.ds-<stream>-<date>-<gen>`. The cluster surfaces backing indices
// in /_cat/indices alongside standalone indices; operators who only
// want to see the latter pass --exclude-managed to drop them.
//
// Both products use the `.ds-` prefix for data-stream-managed
// backing indices; standalone hidden indices (`.kibana`, `.security`)
// don't carry it. Checking the prefix alone is enough — the cluster
// is the source of truth for the naming convention, and we don't
// want to round-trip /_data_stream just to filter the cat list.
func isDataStreamBackingIndex(name string) bool {
	return strings.HasPrefix(name, ".ds-")
}

// sortIndices is the small helper that backs List's post-filter
// ordering. Pulled out of List so the filter loop stays linear.
func sortIndices(out []types.Index, key types.IndexSort) {
	switch key {
	case types.IndexSortSize:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].StoreBytes > out[j].StoreBytes
		})
	case types.IndexSortDocs:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].DocsCount > out[j].DocsCount
		})
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Name < out[j].Name
		})
	}
}
