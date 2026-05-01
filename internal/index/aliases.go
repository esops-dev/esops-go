package index

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// AliasListOptions narrow ListAliases — both fields are forwarded to
// the cluster. Zero value = "every alias on every index". A non-empty
// Index is filtered client-side to match the AliasFilter contract on
// both dialects from one place.
type AliasListOptions struct {
	Alias string
	Index string
}

// ListAliasesStream is the streaming companion to ListAliases. The
// adapter sorts server-side via /_cat/aliases?s=alias,index, so a
// streamed NDJSON output is already in the same order a buffered
// table render would show. No client-side sort happens here — sort
// would buffer the whole result and defeat the point.
func ListAliasesStream(ctx context.Context, ai client.AliasInspector, opts AliasListOptions, yield func(types.Alias) error) error {
	return ai.AliasesStream(ctx, types.AliasFilter{
		Alias: opts.Alias,
		Index: opts.Index,
	}, yield)
}

// ListAliases returns a sorted list of alias→index bindings. Both
// adapters sort server-side via /_cat/aliases?s=alias,index. We
// re-sort client-side as defense-in-depth — older cluster versions or
// proxies that strip query params would otherwise produce non-stable
// output, and the cost is negligible against an alias list.
func ListAliases(ctx context.Context, ai client.AliasInspector, opts AliasListOptions) ([]types.Alias, error) {
	rows, err := ai.Aliases(ctx, types.AliasFilter{
		Alias: opts.Alias,
		Index: opts.Index,
	})
	if err != nil {
		return nil, err
	}

	// Defensive: cluster sort can be skipped on older ES versions or by
	// proxies that strip query params. Re-sort client-side; a no-op if
	// the cluster already did the work.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Alias != rows[j].Alias {
			return rows[i].Alias < rows[j].Alias
		}
		return rows[i].Index < rows[j].Index
	})
	return rows, nil
}

// AliasUpdateOptions are the knobs the CLI layer hands to AddAlias /
// RemoveAlias. Alias and Index are required and must be literal
// names — patterns on the update path are a real footgun (one typo
// can bind an alias to a hundred indices), so esops requires literals
// and lets operators reach for the cluster's raw API for the bulk
// case.
//
// Filter is a raw JSON object string; the adapter parses and
// embeds it as a JSON object in the wire body. Routing is the cluster's
// optional routing knob. IsWriteIndex is a *bool because "demote this
// alias from being the write index" (false) is a real flow that's
// distinct from "leave the cluster default" (nil).
//
// DryRun is the global flag — when true, the cluster is not
// contacted; the function returns a simulated AliasUpdateResult with
// Acknowledged=true.
type AliasUpdateOptions struct {
	Alias        string
	Index        string
	Filter       string
	Routing      string
	IsWriteIndex *bool
	DryRun       bool
}

// AddAlias is the business-layer entry point for `esops index aliases
// add`. Validation enforces literal alias+index names and (when set)
// well-formed filter JSON; the cluster's own validators catch deeper
// issues (unknown fields in the filter, malformed routing values).
func AddAlias(ctx context.Context, au client.AliasUpdater, opts AliasUpdateOptions) (types.AliasUpdateResult, error) {
	if err := validateAliasUpdateOptions(opts); err != nil {
		return types.AliasUpdateResult{}, err
	}
	action := types.AliasUpdateAction{
		Action:       types.AliasActionAdd,
		Alias:        strings.TrimSpace(opts.Alias),
		Index:        strings.TrimSpace(opts.Index),
		Filter:       strings.TrimSpace(opts.Filter),
		Routing:      strings.TrimSpace(opts.Routing),
		IsWriteIndex: opts.IsWriteIndex,
	}
	return runAliasUpdate(ctx, au, opts.DryRun, action)
}

// RemoveAlias is the business-layer entry point for `esops index
// aliases remove`. Same validation as AddAlias on alias+index; filter
// and routing are not relevant on a remove and are ignored if set.
func RemoveAlias(ctx context.Context, au client.AliasUpdater, opts AliasUpdateOptions) (types.AliasUpdateResult, error) {
	// The "filter must be valid JSON if set" check doesn't apply on
	// remove — it's a no-op field there. Validate alias/index only.
	if err := validateAliasIdentity(opts.Alias, opts.Index); err != nil {
		return types.AliasUpdateResult{}, err
	}
	action := types.AliasUpdateAction{
		Action: types.AliasActionRemove,
		Alias:  strings.TrimSpace(opts.Alias),
		Index:  strings.TrimSpace(opts.Index),
	}
	return runAliasUpdate(ctx, au, opts.DryRun, action)
}

// runAliasUpdate is the shared dry-run / live dispatch behind add and
// remove. On dry-run we return a synthetic Acknowledged=true result
// echoing the would-be action — the cluster has no native dry-run for
// /_aliases.
func runAliasUpdate(ctx context.Context, au client.AliasUpdater, dryRun bool, action types.AliasUpdateAction) (types.AliasUpdateResult, error) {
	if dryRun {
		return types.AliasUpdateResult{
			Action:       "alias-update",
			DryRun:       true,
			Acknowledged: true,
			Actions:      []types.AliasUpdateAction{action},
		}, nil
	}
	res, err := au.UpdateAliases(ctx, types.AliasUpdateRequest{
		Actions: []types.AliasUpdateAction{action},
	})
	if err != nil {
		return types.AliasUpdateResult{}, err
	}
	res.DryRun = false
	return res, nil
}

// validateAliasUpdateOptions enforces the add-path contract: literal
// alias+index, and (when set) a Filter that parses as a JSON object.
// We fail fast on filter shape rather than letting the cluster reject
// it — the error message "invalid alias filter JSON" is more
// actionable than the cluster's generic 400.
func validateAliasUpdateOptions(opts AliasUpdateOptions) error {
	if err := validateAliasIdentity(opts.Alias, opts.Index); err != nil {
		return err
	}
	if filter := strings.TrimSpace(opts.Filter); filter != "" {
		var probe any
		if err := json.Unmarshal([]byte(filter), &probe); err != nil {
			return client.UsageError("--filter must be a JSON object (got: %v)", err)
		}
		if _, ok := probe.(map[string]any); !ok {
			return client.UsageError("--filter must be a JSON object, not a %T", probe)
		}
	}
	return nil
}

// validateAliasIdentity is the alias+index name check shared by add
// and remove. Patterns are refused on both sides.
func validateAliasIdentity(alias, index string) error {
	a := strings.TrimSpace(alias)
	i := strings.TrimSpace(index)
	if a == "" {
		return client.UsageError("--alias is required")
	}
	if i == "" {
		return client.UsageError("--index is required (a single literal index name)")
	}
	if client.IsPatternish(a) {
		return client.UsageError("--alias must be a literal name, not a pattern (got %q)", a)
	}
	if client.IsPatternish(i) {
		return client.UsageError("--index must be a literal name, not a pattern (got %q)", i)
	}
	return nil
}
