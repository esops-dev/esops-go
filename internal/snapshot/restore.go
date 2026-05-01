package snapshot

import (
	"context"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// RestoreOptions are the knobs the CLI layer hands to Restore. Repository
// and Snapshot are required and must be literal names — restore does not
// accept patterns at the path level. Indices is the comma-separated pattern
// forwarded to the cluster as the body's `indices` field; empty restores
// every index in the snapshot.
//
// RenamePattern / RenameReplacement is the canonical "restore alongside"
// dance: the cluster rejects a restore that would clobber an existing open
// index, so operators carry the snapshot's indices into a different name
// space (typically `restored-$1`) and copy or alias on top afterwards.
// Both must be set together — the cluster rejects one without the other —
// and the validation enforces that here so the operator gets a clear error
// rather than the cluster's terse 500.
//
// IncludeGlobalState / Partial / IgnoreUnavailable are *bool so the CLI can
// distinguish "leave the cluster default" from "explicitly false".
//
// IndexSettings overrides settings on the restored indices (commonly
// `index.number_of_replicas: 0` to speed up the initial recovery);
// IgnoreIndexSettings drops settings from the snapshot's stored index
// metadata so the cluster falls back to its own defaults on the new index.
//
// DryRun is the global flag — when true the cluster is not contacted and
// the function returns a synthetic Accepted=true reflecting what would
// have been sent. The cluster has no native dry-run for restore.
type RestoreOptions struct {
	Repository          string
	Snapshot            string
	Indices             string
	IncludeGlobalState  *bool
	Partial             *bool
	IgnoreUnavailable   *bool
	RenamePattern       string
	RenameReplacement   string
	IndexSettings       map[string]any
	IgnoreIndexSettings []string
	WaitForCompletion   bool
	DryRun              bool
}

// Restore is the business-layer entry point for `esops snapshot restore`.
// Validation:
//
//  1. Repository and Snapshot are required and must be literal names.
//  2. The rename pair is all-or-nothing — `--rename-pattern` without a
//     replacement (or vice versa) trips the cluster's terse 500 with a
//     message that doesn't tell the operator which half is missing.
//
// The cluster's own 500 ("[idx] already exists") on a restore that would
// clobber an existing index is the canonical error and we surface it
// verbatim — pre-flighting "would this restore clash?" would require a
// second round-trip and still race with concurrent index creation.
func Restore(ctx context.Context, c client.SnapshotRestorer, opts RestoreOptions) (types.SnapshotRestoreResult, error) {
	if err := validateRestoreOptions(opts); err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	if opts.DryRun {
		return types.SnapshotRestoreResult{
			Action:            "restore",
			DryRun:            true,
			Repository:        opts.Repository,
			Name:              opts.Snapshot,
			Accepted:          true,
			WaitForCompletion: opts.WaitForCompletion,
			RenamePattern:     opts.RenamePattern,
			RenameReplacement: opts.RenameReplacement,
			Snapshot: types.SnapshotRestoreInfo{
				Snapshot: opts.Snapshot,
				Indices:  splitIndicesPreview(opts.Indices),
			},
		}, nil
	}

	req := types.SnapshotRestoreRequest{
		Repository:          opts.Repository,
		Snapshot:            opts.Snapshot,
		Indices:             opts.Indices,
		IncludeGlobalState:  opts.IncludeGlobalState,
		Partial:             opts.Partial,
		IgnoreUnavailable:   opts.IgnoreUnavailable,
		RenamePattern:       opts.RenamePattern,
		RenameReplacement:   opts.RenameReplacement,
		IndexSettings:       opts.IndexSettings,
		IgnoreIndexSettings: opts.IgnoreIndexSettings,
		WaitForCompletion:   opts.WaitForCompletion,
	}
	return c.RestoreSnapshot(ctx, req)
}

func validateRestoreOptions(opts RestoreOptions) error {
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
	pat := strings.TrimSpace(opts.RenamePattern)
	repl := strings.TrimSpace(opts.RenameReplacement)
	if (pat == "") != (repl == "") {
		return client.UsageError("--rename-pattern and --rename-replacement must be set together")
	}
	return nil
}
