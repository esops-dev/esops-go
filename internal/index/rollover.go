package index

import (
	"context"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// RolloverOptions are the knobs the CLI layer hands to Rollover. Alias
// is the rollover target (the cluster looks up the current write index
// behind it); NewIndex overrides the cluster's default name derivation
// for clusters whose write indices don't end in `-NNNNNN`.
//
// Conditions are the rollover triggers — when all are zero the call
// is unconditional and Force must be true. Force exists so a typo
// like `index rollover --alias logs` doesn't roll over a fresh index;
// it forces the operator to acknowledge the intent in shell history.
type RolloverOptions struct {
	Alias               string
	NewIndex            string
	Conditions          types.IndexRolloverConditions
	DryRun              bool
	Force               bool
	WaitForActiveShards string
}

// Rollover is the business-layer entry point for `esops index rollover`.
// Validation:
//
//  1. Alias is required.
//  2. Conditions must be non-empty unless Force is set — an
//     unconditional rollover is a real lever, but it has to be opt-in.
//  3. Numeric conditions must be non-negative; size strings are passed
//     through to the cluster, which has the authoritative parser.
//
// The cluster has a native dry-run for /_rollover, so unlike open/close
// the business layer doesn't synthesise it client-side: we forward the
// flag and trust the cluster's own evaluator. When DryRun is true and
// no conditions match, RolledOver=false and the per-condition map
// shows which trigger fell short.
func Rollover(ctx context.Context, ro client.IndexRollover, opts RolloverOptions) (types.IndexRolloverResult, error) {
	if err := validateRolloverOptions(opts); err != nil {
		return types.IndexRolloverResult{}, err
	}

	req := types.IndexRolloverRequest{
		Alias:               opts.Alias,
		NewIndex:            opts.NewIndex,
		Conditions:          opts.Conditions,
		DryRun:              opts.DryRun,
		WaitForActiveShards: opts.WaitForActiveShards,
	}
	return ro.Rollover(ctx, req)
}

// validateRolloverOptions enforces the alias-required + non-empty-or-
// forced-conditions rule. The condition values themselves are
// forwarded as-is — duration parsing is the cluster's job and
// duplicating it here would risk drift between us and what the
// cluster actually accepts.
func validateRolloverOptions(opts RolloverOptions) error {
	alias := strings.TrimSpace(opts.Alias)
	if alias == "" {
		return client.UsageError("--alias is required")
	}
	if opts.Conditions.IsEmpty() && !opts.Force {
		return client.UsageError("rollover with no conditions would always roll over; pass at least one of --max-age / --max-docs / --max-size / --max-primary-size, or --force to acknowledge an unconditional rollover")
	}
	if opts.Conditions.MaxDocs < 0 {
		return client.UsageError("--max-docs must be >= 0 (got %d)", opts.Conditions.MaxDocs)
	}
	return nil
}
