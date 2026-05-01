// Package snapshot holds the business logic behind the
// `esops snapshot ...` command tree. Like internal/index, it depends only
// on the small capability interfaces from internal/client, never on a
// specific dialect's adapter.
package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// ParseSort maps a CLI --sort flag to the typed key. Empty / "start_time"
// / "time" all collapse to the default (most recent first); the typo-y
// values are accepted because both spellings come up in the wild.
func ParseSort(s string) (types.SnapshotSort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "start_time", "starttime", "time", "date":
		return types.SnapshotSortStartTime, nil
	case "name":
		return types.SnapshotSortName, nil
	case "duration":
		return types.SnapshotSortDuration, nil
	default:
		return 0, fmt.Errorf("unknown sort key %q (expected: start_time, name, duration)", s)
	}
}

// ValidateState rejects an unknown --state value up-front so a typo
// doesn't silently filter every snapshot away. The accepted set mirrors
// the cluster's own State vocabulary; we keep it permissive (case-
// insensitive, untrimmed values like "in_progress" or "in progress" both
// work) to match operator habit rather than impose a stricter schema.
func ValidateState(s string) error {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_") {
	case "", "success", "failed", "partial", "in_progress", "incompatible":
		return nil
	default:
		return fmt.Errorf("unknown state %q (expected: success, failed, partial, in_progress, incompatible)", s)
	}
}

// ListRepositories returns the set of registered repositories matching
// filter.Repository. Sorted by name for deterministic output. Pure
// pass-through over the capability today; kept as its own function so a
// future filter (e.g. by repository type) lands in one place.
func ListRepositories(ctx context.Context, c client.SnapshotInspector, f types.SnapshotRepositoryFilter) ([]types.SnapshotRepository, error) {
	repos, err := c.Repositories(ctx, f)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, nil
}

// List returns the filtered, sorted snapshot view across one or more
// repositories. State is enforced client-side because neither cluster's
// raw endpoint accepts a state filter on /_snapshot/<repo>/<pattern>;
// applying it here keeps the CLI surface identical across dialects.
//
// Sort is always applied client-side (rather than via ES's server-side
// sort) for the same reason — OS has no equivalent and we want one story
// at the CLI surface.
func List(ctx context.Context, c client.SnapshotInspector, f types.SnapshotFilter) ([]types.Snapshot, error) {
	raw, err := c.Snapshots(ctx, f)
	if err != nil {
		return nil, err
	}

	wantState := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(f.State)), " ", "_")
	out := raw[:0]
	for _, s := range raw {
		if wantState != "" && strings.ToLower(s.State) != wantState {
			continue
		}
		out = append(out, s)
	}

	switch f.Sort {
	case types.SnapshotSortName:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Repository != out[j].Repository {
				return out[i].Repository < out[j].Repository
			}
			return out[i].Name < out[j].Name
		})
	case types.SnapshotSortDuration:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].DurationMillis > out[j].DurationMillis
		})
	default:
		// Most-recent first. Snapshots without a start_time (rare —
		// in-progress entries on some versions) sink to the bottom so
		// the active fleet view stays readable.
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].StartTimeMillis > out[j].StartTimeMillis
		})
	}
	return out, nil
}

// Stream invokes yield for each snapshot in the resolved set as it
// arrives. Sort is *not* applied — streaming and sorting are mutually
// exclusive (sorting requires the full set in memory, which defeats the
// streaming promise). State filtering is applied so the operator's
// `--state` flag still works under `--output ndjson`.
//
// Per-repo errors land in repoErr; iteration over the rest of the fleet
// continues. The returned error is non-nil only on a fatal failure
// (initial repo resolution, context cancellation, yield error).
func Stream(ctx context.Context, c client.SnapshotInspector, f types.SnapshotFilter, yield func(types.Snapshot) error, repoErr func(repo string, err error)) error {
	wantState := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(f.State)), " ", "_")
	return c.SnapshotsStream(ctx, f, func(s types.Snapshot) error {
		if wantState != "" && strings.ToLower(s.State) != wantState {
			return nil
		}
		return yield(s)
	}, repoErr)
}
