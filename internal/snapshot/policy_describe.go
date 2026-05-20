package snapshot

import (
	"context"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// PolicyDescribeOptions is the business-layer input to DescribePolicy.
// Name is the operator-supplied policy name; patterns refused.
// RecentLimit caps the size of the recent-snapshots tail — 0 means
// "use DefaultPolicyRecentLimit". Negative is rejected.
type PolicyDescribeOptions struct {
	Name        string
	RecentLimit int
}

// DefaultPolicyRecentLimit is the default tail size for the
// recent-snapshots section. Five recent records cover the "did this
// policy run lately / did it succeed" question without flooding the
// describe output on a daily-snapshot policy that's been running for
// years.
const DefaultPolicyRecentLimit = 5

// PolicyDescribeCapabilities is the read-only capability bundle the
// aggregator consumes. SnapshotPolicyInspector returns the policy
// metadata; SnapshotInspector lists snapshots in the policy's target
// repository so the renderer can show a recent-run tail (the
// cluster reports no back-reference from a Snapshot to the policy
// that produced it).
type PolicyDescribeCapabilities struct {
	Policies  client.SnapshotPolicyInspector
	Snapshots client.SnapshotInspector
	Dialect   string
}

// DescribePolicy is the business-layer entry point for
// `esops snapshot policy describe`. Fetches the policy via the
// existing GetPolicy helper, then lists snapshots in the policy's
// target repository and trims to the most recent N. Recent-snapshot
// matching is best-effort — the cluster does not record which
// policy created which snapshot, so we surface the tail of the
// policy's *target repo* as "the most recent runs that landed
// here", which is the operator-useful interpretation.
//
// Refusals: empty / patternish Name surfaces as ErrUsage via
// GetPolicy. A missing policy surfaces as ErrNotFound. Recent-
// snapshot listing failures are non-fatal SectionWarnings.
func DescribePolicy(ctx context.Context, caps PolicyDescribeCapabilities, opts PolicyDescribeOptions) (types.SnapshotPolicyDescription, error) {
	if opts.RecentLimit < 0 {
		return types.SnapshotPolicyDescription{}, client.UsageError("recent-limit must be >= 0 (got %d)", opts.RecentLimit)
	}
	limit := opts.RecentLimit
	if limit == 0 {
		limit = DefaultPolicyRecentLimit
	}

	policy, err := GetPolicy(ctx, caps.Policies, opts.Name)
	if err != nil {
		return types.SnapshotPolicyDescription{}, err
	}

	desc := types.SnapshotPolicyDescription{
		Name:            policy.Name,
		Dialect:         caps.Dialect,
		Policy:          policy,
		RecentLimit:     limit,
		SectionWarnings: map[string]string{},
	}

	if policy.Repository != "" && caps.Snapshots != nil {
		// We need StartTimeMillis and DurationMillis for the renderer's
		// "Started" and "Duration" columns, so the cluster's full-
		// verbose envelope is required here — Slim drops both fields.
		rows, err := caps.Snapshots.Snapshots(ctx, types.SnapshotFilter{
			Repository: policy.Repository,
			Sort:       types.SnapshotSortStartTime, // most recent first
		})
		if err != nil {
			desc.SectionWarnings["recent_snapshots"] = err.Error()
		} else {
			if len(rows) > limit {
				rows = rows[:limit]
			}
			if len(rows) > 0 {
				desc.RecentSnapshots = rows
			}
		}
	}

	if len(desc.SectionWarnings) == 0 {
		desc.SectionWarnings = nil
	}
	return desc, nil
}
