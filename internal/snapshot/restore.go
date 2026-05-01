package snapshot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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
// MasterTimeout maps to ?master_timeout on the cluster endpoint. Zero
// leaves the cluster default (30s); long-running master state operations
// on busy clusters routinely need 1m+.
//
// DryRun is the global flag — when true the cluster is not contacted. The
// dry-run path runs a *collision pre-flight* via the SnapshotInspector:
// it resolves the snapshot's index list, applies the rename, and reports
// any existing-index collisions in the result so the operator sees them
// before the live run trips the cluster's 500. The cluster has no native
// dry-run for restore so this is implemented client-side.
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
	MasterTimeout       time.Duration
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
// On --dry-run, when a SnapshotInspector and IndexInspector are both
// available, this function pre-flights the index-collision check so the
// operator sees what would conflict before the live run. On a live run
// the cluster's own 500 ("[idx] already exists") is surfaced verbatim —
// pre-flighting always races with concurrent index creation, so the
// pre-flight is dry-run-only.
func Restore(ctx context.Context, c client.SnapshotRestorer, opts RestoreOptions) (types.SnapshotRestoreResult, error) {
	return RestoreWithPreflight(ctx, c, nil, nil, opts)
}

// RestoreWithPreflight is the full entry point used when the caller has a
// SnapshotInspector and IndexInspector to hand: --dry-run consults them
// to compute the post-rename index list and warn about collisions before
// the operator hits enter on the live command. ins or idx may be nil; in
// that case the dry-run path falls back to the cosmetic preview.
func RestoreWithPreflight(ctx context.Context, c client.SnapshotRestorer, ins client.SnapshotInspector, idx client.IndexInspector, opts RestoreOptions) (types.SnapshotRestoreResult, error) {
	if err := validateRestoreOptions(opts); err != nil {
		return types.SnapshotRestoreResult{}, err
	}

	if opts.DryRun {
		out := types.SnapshotRestoreResult{
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
		}
		// Best-effort collision pre-flight. Failures here are wrapped in
		// the result rather than surfaced as a fatal error: the operator
		// asked for a dry-run, not a live operation, and reporting "I
		// couldn't check, but the live run might collide" is more useful
		// than refusing to dry-run.
		if ins != nil && idx != nil {
			collisions, err := preflightRestoreCollisions(ctx, ins, idx, opts)
			switch {
			case err != nil:
				out.Snapshot.PreflightWarning = "preflight skipped: " + err.Error()
			case len(collisions.Collided) > 0:
				out.Snapshot.Indices = collisions.Targets
				out.Snapshot.Collisions = collisions.Collided
				return out, fmt.Errorf("%w: restore would clobber %d existing index/indices: %s",
					client.ErrDryRunBlocker, len(collisions.Collided), strings.Join(collisions.Collided, ", "))
			default:
				out.Snapshot.Indices = collisions.Targets
			}
		}
		return out, nil
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
		MasterTimeout:       opts.MasterTimeout,
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
	if client.IsPatternish(repo) {
		return client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	if client.IsPatternish(snap) {
		return client.UsageError("--snapshot must be a literal name, not a pattern (got %q)", snap)
	}
	pat := strings.TrimSpace(opts.RenamePattern)
	repl := strings.TrimSpace(opts.RenameReplacement)
	if (pat == "") != (repl == "") {
		return client.UsageError("--rename-pattern and --rename-replacement must be set together")
	}
	if pat != "" {
		if _, err := regexp.Compile(pat); err != nil {
			return client.UsageError("--rename-pattern is not a valid regex: %v", err)
		}
	}
	return nil
}

// applyRestoreRename produces the post-rename target index list for a
// restore. Returns the input slice unchanged when the rename pair is
// empty. Compilation errors on the pattern surface verbatim — the
// validator already rejected unparseable patterns up-front, so a
// compile failure here means an internal-API misuse (the function is
// also used by the wait/resume path which doesn't run the validator).
func applyRestoreRename(sourceIndices []string, pattern, replacement string) ([]string, error) {
	if pattern == "" {
		out := make([]string, len(sourceIndices))
		copy(out, sourceIndices)
		return out, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("rename pattern: %w", err)
	}
	out := make([]string, 0, len(sourceIndices))
	for _, n := range sourceIndices {
		out = append(out, re.ReplaceAllString(n, replacement))
	}
	return out, nil
}

// preflightOutcome captures what the pre-flight learned about a dry-run
// restore: Targets is the post-rename index list (intersected with the
// operator's --indices filter), Collided is the subset that already
// exists in the cluster.
type preflightOutcome struct {
	Targets  []string
	Collided []string
}

// preflightRestoreCollisions resolves the snapshot's index list, applies
// the operator's --indices filter and rename pair, then asks the cluster
// which of the resulting names already exist. Returns the post-rename
// list plus the subset that would collide on a live run.
//
// Two round-trips: one to read the snapshot's stored index list, one
// /_cat/indices to discover what's currently live. Both are cheap on
// healthy clusters and the dry-run shouldn't be a hot path anyway.
func preflightRestoreCollisions(ctx context.Context, ins client.SnapshotInspector, idx client.IndexInspector, opts RestoreOptions) (preflightOutcome, error) {
	rows, err := ins.Snapshots(ctx, types.SnapshotFilter{
		Repository: opts.Repository,
		Snapshot:   opts.Snapshot,
	})
	if err != nil {
		return preflightOutcome{}, err
	}
	var sourceIndices []string
	for _, r := range rows {
		if r.Name == opts.Snapshot {
			sourceIndices = append(sourceIndices, r.Indices...)
			break
		}
	}
	if len(sourceIndices) == 0 {
		// Older clusters return the snapshot via a different key; if we
		// can't find it, bail out without warning so the dry-run isn't
		// noisy. The live run will surface the cluster's own diagnosis.
		return preflightOutcome{}, errors.New("snapshot not found or has no indices recorded")
	}

	// Apply --indices filter (cosmetic match — patterns and date math
	// stay opaque, same as the live cluster behaviour we can't perfectly
	// emulate client-side; mismatches just leave more in the target).
	if filt := strings.TrimSpace(opts.Indices); filt != "" {
		want := splitIndicesPreview(filt)
		wantSet := make(map[string]struct{}, len(want))
		for _, w := range want {
			wantSet[w] = struct{}{}
		}
		filtered := sourceIndices[:0]
		for _, n := range sourceIndices {
			// Keep names whose literal form is mentioned. A pattern like
			// `logs-*` matches no literal name in the source list, so the
			// pre-flight conservatively passes everything through — the
			// dry-run output makes the over-inclusion visible.
			if _, ok := wantSet[n]; ok {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			sourceIndices = filtered
		}
	}

	// Apply rename pair.
	targets, err := applyRestoreRename(sourceIndices, opts.RenamePattern, opts.RenameReplacement)
	if err != nil {
		return preflightOutcome{}, err
	}

	// Look up which targets already exist. /_cat/indices accepts a
	// comma-separated list and a missing-index case returns 404, so we
	// query open and closed states in one shot via the inspector's slim
	// helper — the response is just (name, status) tuples.
	existing, err := idx.IndicesNamesStatus(ctx, types.IndexFilter{Index: strings.Join(targets, ",")})
	if err != nil {
		// 404 on every target → nothing exists, no collisions. Other
		// errors propagate.
		if errors.Is(err, client.ErrNotFound) {
			return preflightOutcome{Targets: targets}, nil
		}
		return preflightOutcome{}, err
	}

	exists := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		exists[e.Name] = struct{}{}
	}
	collided := make([]string, 0)
	for _, t := range targets {
		if _, ok := exists[t]; ok {
			collided = append(collided, t)
		}
	}
	return preflightOutcome{Targets: targets, Collided: collided}, nil
}
