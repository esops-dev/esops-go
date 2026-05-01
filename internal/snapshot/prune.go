package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// PruneOptions are the knobs the CLI layer hands to Prune. Repository is a
// literal repo name (single-repo retention runs — fanning out across repos
// is a future feature, not a default). SnapshotPattern narrows the candidate
// set; empty matches every snapshot in the repository.
//
// KeepLast keeps the N most-recent (by start_time) successful snapshots.
// Zero means "no minimum kept count" — the operator opted in to potentially
// emptying the repository. OlderThan deletes anything strictly older than
// the given duration. Both filters compose: a snapshot is deleted when it
// fails *every* keep rule (i.e. it's outside the keep-last window AND
// outside the older-than window).
//
// StateFilter restricts the candidate set by snapshot state — by default
// only `SUCCESS` snapshots are eligible for the keep-last window so a
// run of FAILED nightlies doesn't push real backups out of retention. An
// explicit "" disables the filter.
//
// AllowLargePrune is the explicit opt-in for the §6 blast-radius limit:
// refuse when retention would delete more than 50% of the candidate set
// in one run. The threshold is shared with `index optimize`/`close`
// blast-radius logic — same intent, same surface.
//
// DryRun does not contact the cluster. The plan is computed entirely from
// the listing call; the result enumerates what would be deleted.
type PruneOptions struct {
	Repository      string
	SnapshotPattern string
	KeepLast        int
	OlderThan       time.Duration
	OlderThanRaw    string
	StateFilter     string
	AllowLargePrune bool
	DryRun          bool
}

// Prune is the business-layer entry point for `esops snapshot prune`. It
// resolves the candidate set client-side via the SnapshotInspector, applies
// the keep-last / older-than / state filters, enforces the blast-radius
// threshold, and (unless dry-running) issues a single bulk delete via the
// SnapshotPruner.
//
// The retention math runs here rather than at the adapter layer for two
// reasons: neither cluster ships a native prune endpoint, and the operator
// expects `--dry-run` to surface the *exact* same set the live run would
// delete — keeping selection and deletion as separate steps in one
// function makes that invariant easy to test.
func Prune(ctx context.Context, ins client.SnapshotInspector, pr client.SnapshotPruner, opts PruneOptions) (types.SnapshotPruneResult, error) {
	repo := strings.TrimSpace(opts.Repository)
	if repo == "" {
		return types.SnapshotPruneResult{}, client.UsageError("--repository is required")
	}
	if isSnapshotPatternish(repo) {
		return types.SnapshotPruneResult{}, client.UsageError("--repository must be a literal name, not a pattern (got %q)", repo)
	}
	if opts.KeepLast == 0 && opts.OlderThan == 0 {
		return types.SnapshotPruneResult{}, client.UsageError("--keep-last and/or --older-than is required (refusing to prune without a retention rule)")
	}
	if opts.KeepLast < 0 {
		return types.SnapshotPruneResult{}, client.UsageError("--keep-last must be >= 0 (got %d)", opts.KeepLast)
	}

	state := opts.StateFilter
	if state != "" {
		if err := ValidateState(state); err != nil {
			return types.SnapshotPruneResult{}, err
		}
	}

	filter := types.SnapshotFilter{
		Repository: repo,
		Snapshot:   opts.SnapshotPattern,
		Sort:       types.SnapshotSortStartTime,
	}
	candidates, err := List(ctx, ins, filter)
	if err != nil {
		return types.SnapshotPruneResult{}, err
	}

	now := time.Now()
	kept, deleted := planPrune(candidates, opts, now)

	out := types.SnapshotPruneResult{
		Action:          "prune",
		DryRun:          opts.DryRun,
		Repository:      repo,
		Considered:      len(candidates),
		Kept:            namesOf(kept),
		Deleted:         namesOf(deleted),
		KeepLast:        opts.KeepLast,
		OlderThan:       opts.OlderThanRaw,
		StateFilter:     state,
		SnapshotPattern: opts.SnapshotPattern,
	}

	// Blast-radius guard (§6): refuse a prune that wipes more than half the
	// repository in one run unless the operator opted in. Done after the plan
	// is computed so a dry-run shows the operator the same threshold they
	// would have hit live.
	if !opts.AllowLargePrune && len(deleted) > 0 && len(candidates) > 0 {
		ratio := float64(len(deleted)) / float64(len(candidates))
		if ratio > 0.5 {
			return out, fmt.Errorf("%w: prune would delete %d of %d snapshots (%.0f%%); pass --allow-large-prune to proceed",
				client.ErrBlastRadius, len(deleted), len(candidates), ratio*100)
		}
	}

	if opts.DryRun || len(deleted) == 0 {
		return out, nil
	}

	res, err := pr.DeleteSnapshots(ctx, types.SnapshotDeleteRequest{
		Repository: repo,
		Snapshots:  out.Deleted,
	})
	if err != nil {
		return out, err
	}
	out.Acknowledged = res.Acknowledged
	return out, nil
}

// planPrune partitions the candidate set into the slices the cluster will
// keep vs delete. Inputs are assumed pre-filtered by snapshot pattern but
// not yet by state. The split is computed in three steps so the rule
// interaction is auditable from the unit tests:
//
//  1. State filter — snapshots whose state doesn't match are pinned to
//     "kept" (they're not eligible for deletion under the active rules).
//     The default state filter ("") leaves every state in the running.
//  2. KeepLast — within the eligible set, the N most-recent snapshots are
//     pinned to "kept".
//  3. OlderThan — among the remaining eligible-and-not-pinned snapshots,
//     anything strictly older than the cut-off goes to "delete"; the rest
//     stays in "kept".
//
// The two rules compose with AND semantics: a snapshot is deleted only when
// the keep-last window doesn't pin it AND it's older than the cut-off.
// Either rule alone is sufficient to delete (when the other is unset), so
// `--keep-last 7` without `--older-than` deletes everything past the 7th
// most recent, and `--older-than 30d` without `--keep-last` deletes
// everything older than 30 days.
func planPrune(rows []types.Snapshot, opts PruneOptions, now time.Time) (kept, deleted []types.Snapshot) {
	state := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(opts.StateFilter)), " ", "_")

	// Sort by start time descending so KeepLast is index-based.
	sorted := append([]types.Snapshot(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartTimeMillis > sorted[j].StartTimeMillis
	})

	cutoff := time.Time{}
	if opts.OlderThan > 0 {
		cutoff = now.Add(-opts.OlderThan)
	}

	kept = make([]types.Snapshot, 0)
	deleted = make([]types.Snapshot, 0)
	keepCount := 0
	for _, s := range sorted {
		// State filter pins the row to kept — even if it's older than the
		// cut-off, an unsuccessful snapshot isn't subject to the keep-last
		// rule (it never counted as a backup) so we leave it for the
		// operator to delete deliberately.
		if state != "" && strings.ToLower(s.State) != state {
			kept = append(kept, s)
			continue
		}

		// KeepLast window: pin the N most-recent eligible snapshots.
		if opts.KeepLast > 0 && keepCount < opts.KeepLast {
			kept = append(kept, s)
			keepCount++
			continue
		}

		// OlderThan: only deletes when the cutoff is set. With cutoff
		// unset, the keep-last window is the only rule active — anything
		// past index N falls through to deleted.
		if opts.OlderThan > 0 {
			if s.StartTimeMillis == 0 {
				// In-progress or version-quirk rows without a start time
				// are kept by default — destroying a snapshot that hasn't
				// finished is the worst regret in this whole tree.
				kept = append(kept, s)
				continue
			}
			started := time.UnixMilli(s.StartTimeMillis)
			if !started.Before(cutoff) {
				kept = append(kept, s)
				continue
			}
		}

		deleted = append(deleted, s)
	}
	return kept, deleted
}

// ParseRetentionDuration parses the operator-facing `--older-than` value.
// Go's time.ParseDuration covers ns through h; operators reach for d/w/y
// when talking about backup retention, so we extend the syntax with
// d (24h), w (7d), and y (365d). Each unit is a single token —
// `--older-than 30d12h` is rejected as a foot-gun (`30d` then `12h` is
// almost certainly the operator typoing `30d 12h`).
func ParseRetentionDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	// Last character may be d/w/y; if so, parse the leading integer.
	if n := len(s); n > 1 {
		switch s[n-1] {
		case 'd', 'D':
			v, err := strconv.Atoi(s[:n-1])
			if err == nil && v > 0 {
				return time.Duration(v) * 24 * time.Hour, nil
			}
		case 'w', 'W':
			v, err := strconv.Atoi(s[:n-1])
			if err == nil && v > 0 {
				return time.Duration(v) * 7 * 24 * time.Hour, nil
			}
		case 'y', 'Y':
			v, err := strconv.Atoi(s[:n-1])
			if err == nil && v > 0 {
				return time.Duration(v) * 365 * 24 * time.Hour, nil
			}
		}
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (expected e.g. 30d, 12w, 1y, or Go-syntax like 720h)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive (got %q)", s)
	}
	return d, nil
}

// namesOf flattens a slice of Snapshot to the slice of names. Used by Prune
// so the result struct carries names rather than the full snapshot objects —
// a kept JSON record with hundreds of nested entries is not the audit trail
// operators want.
func namesOf(rows []types.Snapshot) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}
