// Package index holds the business logic behind the `esops index ...`
// command tree. It depends only on the small capability interfaces from
// internal/client, never on a specific dialect's adapter.
package index

import (
	"context"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// List returns the filtered, sorted index view. The cluster only honours
// the Index path filter; Health/Status/Hidden/Sort are applied here so
// the same IndexFilter produces identical output on ES and OS.
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
		if wantHealth != "" && strings.ToLower(ix.Health) != wantHealth {
			continue
		}
		if wantStatus != "" && strings.ToLower(ix.Status) != wantStatus {
			continue
		}
		out = append(out, ix)
	}

	switch f.Sort {
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
	return out, nil
}
