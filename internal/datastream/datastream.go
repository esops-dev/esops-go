// Package datastream holds the business logic behind the
// `esops datastream ...` command tree. Like its sibling packages, it
// depends only on the small capability interfaces from pkg/client —
// never on a specific dialect's adapter — so the same code path
// covers Elasticsearch and OpenSearch.
package datastream

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// DefaultMaxStreams is the blast-radius cap for `datastream delete`.
// Lower than the index cap (20) because each stream represents many
// backing indices — deleting 6 streams routinely removes dozens of
// indices in one shot. Operators wanting a larger sweep pass
// --max-streams N explicitly so the intent is visible in shell
// history.
const DefaultMaxStreams = 5

// ListOptions narrows a `datastream list` call. Names is a repeatable
// filter forwarded to the inspector; both ES and OS honour a
// comma-joined name list (including patterns) at the path segment.
// Empty Names returns every configured stream. Hidden mirrors the
// `index list` toggle — false (the default) drops streams the cluster
// flagged as hidden (Fleet, APM and other integration-managed streams
// that clutter operator output); true keeps every row the cluster
// returned. Both ES and OS surface every stream on the bare endpoint,
// so the filter is applied client-side using each DataStream.Hidden
// field.
type ListOptions struct {
	Names  []string
	Hidden bool
}

// List returns the cluster's data streams, sorted by name for
// deterministic output, with each stream's StoreBytes / DocsCount
// aggregated from its backing indices and each backing index's
// per-row Health / StoreBytes / DocsCount populated via a single
// /_cat/indices call.
//
// The /_data_stream envelope itself does not carry size or doc
// counts; the enrichment step joins per-backing /_cat/indices rows
// into the stream view. Best-effort: an enrichment failure logs at
// debug and leaves the affected fields zero rather than failing the
// whole list — operators expect "show me my streams" to keep
// working even when /_cat/indices is temporarily slow or unavailable.
//
// indexes may be nil — the function skips enrichment in that case
// (useful for tests that mock only the data-stream inspector).
func List(ctx context.Context, c client.DataStreamInspector, indexes client.IndexInspector, opts ListOptions) ([]types.DataStream, error) {
	rows, err := c.DataStreams(ctx, types.DataStreamFilter{Names: opts.Names})
	if err != nil {
		return nil, err
	}
	if !opts.Hidden {
		filtered := rows[:0]
		for _, r := range rows {
			if r.Hidden {
				continue
			}
			filtered = append(filtered, r)
		}
		rows = filtered
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	enrichBackingIndexStats(ctx, indexes, rows)
	return rows, nil
}

// enrichBackingIndexStats fetches /_cat/indices for every backing
// index across rows and merges health, store, and docs back onto
// both the per-backing entries and the per-stream aggregates. One
// round trip regardless of how many streams rows holds, because
// /_cat/indices accepts a comma-joined name list at the path.
//
// Bounded by the cardinality of resolved backing indices: realistic
// clusters have dozens of streams with single-digit backings each,
// which keeps the URL well inside ES/OS default limits. If a future
// caller demands O(thousands) of backings in one shot, switch to
// the `.ds-*` wildcard plus a client-side filter — same single
// round trip, fixed URL size.
func enrichBackingIndexStats(ctx context.Context, indexes client.IndexInspector, rows []types.DataStream) {
	if len(rows) == 0 || indexes == nil {
		return
	}
	var backings []string
	for _, r := range rows {
		for _, b := range r.Indices {
			backings = append(backings, b.Name)
		}
	}
	if len(backings) == 0 {
		return
	}
	out, err := indexes.Indices(ctx, types.IndexFilter{
		Index:  strings.Join(backings, ","),
		Hidden: true,
	})
	if err != nil {
		logging.Logger().Debug("datastream.list.enrich_failed", "err", err, "backings", len(backings))
		return
	}
	byName := make(map[string]types.Index, len(out))
	for _, ix := range out {
		byName[ix.Name] = ix
	}
	// Bind pointers per iteration so gosec G602 doesn't have to trace
	// the nested `rows[i].Indices[j]` access through two layers of
	// indexing — each pointer is a single bounded `&slice[idx]` step.
	for i := range rows {
		r := &rows[i]
		for j := range r.Indices {
			b := &r.Indices[j]
			ix, ok := byName[b.Name]
			if !ok {
				continue
			}
			b.Health = ix.Health
			b.StoreBytes = ix.StoreBytes
			b.DocsCount = ix.DocsCount
			r.StoreBytes += ix.StoreBytes
			r.DocsCount += ix.DocsCount
		}
	}
}

// Get fetches one data stream by literal name. Patterns are refused
// up-front — both products would happily return a multi-stream map
// for a wildcard, but `datastream get` is the single-name verb. Use
// `datastream list --name a,b` for multi-name lookups.
//
// indexes may be nil — Get skips the backing-index enrichment in
// that case. Otherwise the resulting DataStream carries per-backing
// Health/StoreBytes/DocsCount and the aggregated StoreBytes /
// DocsCount on the stream itself.
func Get(ctx context.Context, c client.DataStreamInspector, indexes client.IndexInspector, name string) (types.DataStream, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return types.DataStream{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.DataStream{}, client.UsageError("--name must be a literal data stream name, not a pattern (got %q)", name)
	}
	d, err := c.GetDataStream(ctx, name)
	if err != nil {
		return types.DataStream{}, err
	}
	rows := []types.DataStream{d}
	enrichBackingIndexStats(ctx, indexes, rows)
	return rows[0], nil
}

// RolloverOptions are the knobs the CLI hands to Rollover.
// Name is required and literal. Force acknowledges an unconditional
// rollover when no condition is set (mirrors `index rollover`'s
// --force gate so an accidental empty-conditions invocation does not
// silently roll over a production stream).
type RolloverOptions struct {
	Name                string
	Conditions          types.IndexRolloverConditions
	DryRun              bool
	Force               bool
	WaitForActiveShards string
}

// Rollover is the business-layer entry point for
// `datastream rollover`. Validates name, refuses an unconditional
// call without --force, and forwards to the capability.
func Rollover(ctx context.Context, c client.DataStreamManager, opts RolloverOptions) (types.DataStreamRolloverResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.DataStreamRolloverResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.DataStreamRolloverResult{}, client.UsageError("--name must be a literal data stream name, not a pattern (got %q)", name)
	}
	if opts.Conditions.IsEmpty() && !opts.Force {
		return types.DataStreamRolloverResult{}, client.UsageError(
			"refusing unconditional rollover: pass a --max-* condition or --force to acknowledge")
	}
	return c.RolloverDataStream(ctx, types.DataStreamRolloverRequest{
		Name:                name,
		Conditions:          opts.Conditions,
		DryRun:              opts.DryRun,
		WaitForActiveShards: opts.WaitForActiveShards,
	})
}

// ModifyOptions are the knobs the CLI hands to Modify. Add and Remove
// are the parallel slices of backing-index names the operator wants
// spliced into or out of the stream.
type ModifyOptions struct {
	Name   string
	Add    []string
	Remove []string
	DryRun bool
}

// Modify is the business-layer entry point for `datastream modify`.
// Builds the {actions:[...]} body from the parallel add/remove
// slices and forwards to the capability. Refuses an empty action set
// up-front so a typo doesn't reach the cluster as a no-op write.
func Modify(ctx context.Context, c client.DataStreamManager, opts ModifyOptions) (types.DataStreamModifyResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return types.DataStreamModifyResult{}, client.UsageError("--name is required")
	}
	if client.IsPatternish(name) {
		return types.DataStreamModifyResult{}, client.UsageError("--name must be a literal data stream name, not a pattern (got %q)", name)
	}

	actions := make([]types.DataStreamModifyEntry, 0, len(opts.Add)+len(opts.Remove))
	for _, ix := range opts.Add {
		ix = strings.TrimSpace(ix)
		if ix == "" {
			continue
		}
		actions = append(actions, types.DataStreamModifyEntry{
			Action:     types.DataStreamModifyAdd,
			DataStream: name,
			Index:      ix,
		})
	}
	for _, ix := range opts.Remove {
		ix = strings.TrimSpace(ix)
		if ix == "" {
			continue
		}
		actions = append(actions, types.DataStreamModifyEntry{
			Action:     types.DataStreamModifyRemove,
			DataStream: name,
			Index:      ix,
		})
	}
	if len(actions) == 0 {
		return types.DataStreamModifyResult{}, client.UsageError("at least one --add-index or --remove-index is required")
	}
	return c.ModifyDataStream(ctx, types.DataStreamModifyRequest{
		Actions: actions,
		DryRun:  opts.DryRun,
	})
}

// DeleteOptions are the knobs the CLI hands to Delete. Pattern is
// the operator's input (literal name(s) or patterns); MaxStreams is
// the explicit opt-in past the default 5-stream blast-radius cap.
// AllowDataLoss is the named confirmation flag — DELETE
// /_data_stream/<name> removes the stream **and** all of its backing
// indices, so the verbose flag name is a feature (grep-for-intent in
// shell history is a real auditing workflow).
type DeleteOptions struct {
	Pattern       string
	MaxStreams    int
	AllowDataLoss bool
	DryRun        bool
}

// Delete is the business-layer entry point for `datastream delete`.
//
//  1. Validates the pattern (non-empty; rejects bare "*" / "_all").
//  2. Refuses without --allow-data-loss — the cluster destroys
//     backing indices, not just the stream object.
//  3. Resolves the pattern via DataStreamInspector.
//  4. Enforces the blast-radius cap on the resolved set.
//  5. On dry-run, returns the resolved would-be deletion without
//     contacting the cluster on the destructive path.
//  6. Otherwise calls DeleteDataStream with the resolved list.
//
// Empty match against a pattern (the pattern matched yesterday but
// is empty today) returns a no-op result with Acknowledged=true so
// scripts treat "nothing to delete" as success.
func Delete(ctx context.Context, inspector client.DataStreamInspector, manager client.DataStreamManager, opts DeleteOptions) (types.DataStreamDeleteResult, error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return types.DataStreamDeleteResult{}, client.UsageError("--name is required")
	}
	switch pattern {
	case "*", "_all":
		return types.DataStreamDeleteResult{}, fmt.Errorf(
			"%w: refusing pattern %q — it would delete every data stream; name a narrower pattern",
			client.ErrBlastRadius, pattern)
	}
	if !opts.AllowDataLoss && !opts.DryRun {
		return types.DataStreamDeleteResult{}, client.UsageError(
			"datastream delete also removes every backing index of the matched streams; pass --allow-data-loss to confirm")
	}

	rows, err := inspector.DataStreams(ctx, types.DataStreamFilter{
		Names: client.SplitCSV(pattern),
	})
	if err != nil {
		return types.DataStreamDeleteResult{}, err
	}

	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	sort.Strings(names)

	limit := opts.MaxStreams
	if limit <= 0 {
		limit = DefaultMaxStreams
	}
	if len(names) > limit {
		return types.DataStreamDeleteResult{}, fmt.Errorf(
			"%w: pattern %q resolves to %d data streams (limit %d); pass --max-streams N to override",
			client.ErrBlastRadius, pattern, len(names), limit)
	}

	if len(names) == 0 {
		return types.DataStreamDeleteResult{
			Action:       "data_stream-delete",
			DryRun:       opts.DryRun,
			Names:        names,
			Acknowledged: true,
		}, nil
	}

	if opts.DryRun {
		return types.DataStreamDeleteResult{
			Action:       "data_stream-delete",
			DryRun:       true,
			Names:        names,
			Acknowledged: true,
		}, nil
	}

	result, err := manager.DeleteDataStream(ctx, types.DataStreamDeleteRequest{Names: names})
	if err != nil {
		return types.DataStreamDeleteResult{}, err
	}
	// Echo the resolved name list back to the caller — the cluster's
	// response is just an acknowledged flag, but a captured JSON
	// record should be self-describing.
	result.Names = names
	result.DryRun = opts.DryRun
	return result, nil
}
