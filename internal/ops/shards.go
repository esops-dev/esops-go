package ops

import (
	"context"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Shards returns the shard list from the cluster, then applies the
// client-side filters (state/node/role) and a stable sort. The Index
// filter on f is already applied by the upstream client; we re-apply it
// here only defensively would be overkill, so we trust the adapter.
func Shards(ctx context.Context, c client.ShardInspector, f types.ShardFilter) ([]types.Shard, error) {
	raw, err := c.Shards(ctx, f)
	if err != nil {
		return nil, err
	}

	out := raw[:0]
	wantState := strings.ToUpper(strings.TrimSpace(f.State))
	for _, s := range raw {
		if wantState != "" && strings.ToUpper(s.State) != wantState {
			continue
		}
		if f.Node != "" && s.Node != f.Node {
			continue
		}
		switch f.Role {
		case types.ShardRolePrimary:
			if !s.Primary {
				continue
			}
		case types.ShardRoleReplica:
			if s.Primary {
				continue
			}
		}
		out = append(out, s)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		if out[i].Shard != out[j].Shard {
			return out[i].Shard < out[j].Shard
		}
		return out[i].Primary && !out[j].Primary
	})
	return out, nil
}
