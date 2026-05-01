package snapshot

import (
	"context"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// CreateOptions are the knobs the CLI layer hands to Create. Repository
// and Snapshot are required and must be literal names — patterns are
// not honoured by the cluster on the create path. Indices is the
// comma-separated pattern forwarded as the body's `indices` field;
// empty leaves the cluster default ("every open index").
//
// IncludeGlobalState / Partial / IgnoreUnavailable are *bool so the CLI
// can distinguish "leave the cluster default" from "explicitly false".
// WaitForCompletion mirrors ?wait_for_completion=true; default false so
// long-running snapshots don't hold an HTTP connection open.
//
// DryRun is the global flag — when true the cluster is not contacted;
// the function returns a synthetic Accepted=true reflecting what would
// have been sent. The cluster has no native dry-run for snapshot
// creation, so this is implemented client-side.
type CreateOptions struct {
	Repository         string
	Snapshot           string
	Indices            string
	IncludeGlobalState *bool
	Partial            *bool
	IgnoreUnavailable  *bool
	WaitForCompletion  bool
	Metadata           map[string]any
	DryRun             bool
}

// Create is the business-layer entry point for `esops snapshot create`.
// Validation:
//
//  1. Repository and Snapshot are required and must be literal names.
//  2. Snapshot names with whitespace are refused — the cluster accepts
//     them but they are a recurring foot-gun in shell pipelines (a
//     trailing space from a $(date) substitution silently produces a
//     snapshot the operator can't reference back without quoting).
//
// Repository existence is not pre-flighted: the cluster's 404 carries
// the canonical error and a separate list-then-create dance would
// double the round-trip cost without adding signal.
func Create(ctx context.Context, c client.SnapshotCreator, opts CreateOptions) (types.SnapshotCreateResult, error) {
	if err := validateCreateOptions(opts); err != nil {
		return types.SnapshotCreateResult{}, err
	}

	if opts.DryRun {
		return types.SnapshotCreateResult{
			Action:            "create",
			DryRun:            true,
			Repository:        opts.Repository,
			Name:              opts.Snapshot,
			Accepted:          true,
			WaitForCompletion: opts.WaitForCompletion,
			Snapshot: types.Snapshot{
				Repository: opts.Repository,
				Name:       opts.Snapshot,
				State:      "DRY_RUN",
				Indices:    splitIndicesPreview(opts.Indices),
			},
		}, nil
	}

	req := types.SnapshotCreateRequest{
		Repository:         opts.Repository,
		Snapshot:           opts.Snapshot,
		Indices:            opts.Indices,
		IncludeGlobalState: opts.IncludeGlobalState,
		Partial:            opts.Partial,
		IgnoreUnavailable:  opts.IgnoreUnavailable,
		WaitForCompletion:  opts.WaitForCompletion,
		Metadata:           opts.Metadata,
	}
	return c.CreateSnapshot(ctx, req)
}

func validateCreateOptions(opts CreateOptions) error {
	repo := strings.TrimSpace(opts.Repository)
	snap := strings.TrimSpace(opts.Snapshot)
	if repo == "" {
		return client.UsageError("--repository is required")
	}
	if snap == "" {
		return client.UsageError("--snapshot is required")
	}
	if isSnapshotPatternish(repo) {
		return client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	if isSnapshotPatternish(snap) {
		return client.UsageError("--snapshot must be a literal name, not a pattern (got %q)", snap)
	}
	if strings.ContainsAny(snap, " \t\n") {
		return client.UsageError("--snapshot must not contain whitespace (got %q)", snap)
	}
	return nil
}

// splitIndicesPreview renders the operator's --indices pattern as a
// preview slice for dry-run output. Trims whitespace around each entry
// because shell pipelines often pass `"a, b, c"`. Empty input returns
// nil so the dry-run record reads cleanly ("indices were not specified
// → cluster default").
func splitIndicesPreview(indices string) []string {
	indices = strings.TrimSpace(indices)
	if indices == "" {
		return nil
	}
	parts := strings.Split(indices, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isSnapshotPatternish mirrors the index package's helper but is local
// to this package so the snapshot business layer stays free of cross-
// package dependencies on internal/index.
func isSnapshotPatternish(s string) bool {
	switch s {
	case "*", "_all":
		return true
	}
	return strings.ContainsAny(s, "*?")
}
