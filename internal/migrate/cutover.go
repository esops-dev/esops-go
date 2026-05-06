package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Cutover performs an atomic alias-swap on a single cluster. The
// sequence:
//
//  1. Verify ToIndex exists and is open.
//  2. Verify FromIndex exists and currently fronts Alias (or warn —
//     `migrate cutover` against an alias that does not yet point at
//     FromIndex is a real flow during a recovery).
//  3. (Optional) Block writes on FromIndex via index.blocks.write=true
//     so any writer on the alias gets a clear refusal during the
//     swap, rather than silently appending to the soon-to-be-orphan.
//  4. POST /_aliases with {add: alias→ToIndex, remove: alias←FromIndex}
//     in a single body. The cluster applies both actions in one
//     cluster-state update — atomic from every reader's perspective.
//  5. (Optional) Remove the write-block from FromIndex unless
//     KeepWriteBlock is set.
//
// Each step produces one MigrationCutoverStep row; a failed step
// short-circuits the rest of the sequence and the result's Status is
// "block". Steps that *would* run in a dry-run carry "skipped" with
// a "would do X" detail so the operator sees the full intended path.
//
// The cutover lives on the *target* cluster's connection — the alias
// belongs to whatever cluster the operator is swapping over. For
// cross-cluster migrations the target context is the cluster that
// received the reindex; for in-place upgrades it's the same cluster
// throughout.
func Cutover(ctx context.Context, ep Endpoint, req types.MigrationCutoverRequest) (types.MigrationCutoverResult, error) {
	if err := validateCutoverEndpoint(ep); err != nil {
		return types.MigrationCutoverResult{}, err
	}
	if err := validateCutoverRequest(req); err != nil {
		return types.MigrationCutoverResult{}, err
	}

	result := types.MigrationCutoverResult{
		Action:    "migrate-cutover",
		Context:   ep.Context,
		Alias:     req.Alias,
		FromIndex: req.FromIndex,
		ToIndex:   req.ToIndex,
		DryRun:    req.DryRun,
	}

	// Step 1 — verify ToIndex exists and is open.
	step, ok := verifyTargetIndex(ctx, ep, req.ToIndex)
	result.Steps = append(result.Steps, step)
	if !ok {
		result.Status = types.MigrationStatusBlock
		return result, nil
	}

	// Step 2 — read current alias bindings; warn if FromIndex is not
	// currently bound to Alias (the operator may be running cutover
	// after a separate manual swap, which is fine, but we surface it).
	result.Steps = append(result.Steps, inspectAliasBinding(ctx, ep, req))

	// Step 3 — block writes on FromIndex.
	if req.BlockWrites {
		step, ok := setWriteBlock(ctx, ep, req, true)
		result.Steps = append(result.Steps, step)
		if !ok {
			result.Status = types.MigrationStatusBlock
			return result, nil
		}
	} else {
		result.Steps = append(result.Steps, types.MigrationCutoverStep{
			Name:   "block-writes",
			Status: skippedStatus,
			Detail: "skipped (--block-writes not set)",
		})
	}

	// Step 4 — atomic alias swap.
	swap, ok := performAliasSwap(ctx, ep, req)
	result.Steps = append(result.Steps, swap)
	if !ok {
		// If the swap failed and we set a write-block, attempt to
		// clear it so the operator isn't left with a write-blocked
		// FromIndex and an unswapped alias. Best-effort — the
		// rollback step is recorded as its own row.
		if req.BlockWrites && !req.DryRun {
			result.Steps = append(result.Steps, rollbackWriteBlock(ctx, ep, req))
		}
		result.Status = types.MigrationStatusBlock
		return result, nil
	}
	result.Acknowledged = swap.Status == types.MigrationStatusOK && !req.DryRun

	// Step 5 — clear the write-block unless KeepWriteBlock is set.
	if req.BlockWrites && !req.KeepWriteBlock {
		step, _ := setWriteBlock(ctx, ep, req, false)
		result.Steps = append(result.Steps, step)
	} else if req.BlockWrites && req.KeepWriteBlock {
		result.Steps = append(result.Steps, types.MigrationCutoverStep{
			Name:   "unblock-writes",
			Status: skippedStatus,
			Detail: "skipped (--keep-write-block set)",
		})
	}

	// Status is the worst of the steps. A swap that succeeded but the
	// inspect step warned ("FromIndex is not currently bound to
	// Alias") is a warn, not a block — the operator's own context
	// changes the meaning.
	result.Status = worstStepStatus(result.Steps)
	return result, nil
}

// skippedStatus is the per-step value for a step that did not run —
// either because the dry-run path took it or because a guard flag
// turned it off. Distinct from "ok" so the table renderer can show
// "—" without claiming success.
const skippedStatus = "skipped"

func validateCutoverEndpoint(ep Endpoint) error {
	if ep.Indices == nil {
		return errors.New("cutover endpoint missing IndexInspector — wiring bug")
	}
	if ep.AliasUpdate == nil {
		return errors.New("cutover endpoint missing AliasUpdater — wiring bug")
	}
	if ep.IndexSettingsSet == nil {
		return errors.New("cutover endpoint missing IndexSettingsUpdater — wiring bug")
	}
	if ep.AliasInspect == nil {
		return errors.New("cutover endpoint missing AliasInspector — wiring bug")
	}
	return nil
}

func validateCutoverRequest(req types.MigrationCutoverRequest) error {
	if strings.TrimSpace(req.Alias) == "" {
		return client.UsageError("cutover: --alias is required")
	}
	if strings.TrimSpace(req.FromIndex) == "" {
		return client.UsageError("cutover: --from is required")
	}
	if strings.TrimSpace(req.ToIndex) == "" {
		return client.UsageError("cutover: --to is required")
	}
	if req.FromIndex == req.ToIndex {
		return client.UsageError("cutover: --from and --to must name different indices (got %q for both)", req.FromIndex)
	}
	if client.IsPatternish(req.Alias) || client.IsPatternish(req.FromIndex) || client.IsPatternish(req.ToIndex) {
		return client.UsageError("cutover: --alias, --from, --to must be literal names (no patterns)")
	}
	return nil
}

// verifyTargetIndex resolves /_cat/indices?index=<to> via the slim
// (name+status) inspector path so a blast-radius cutover doesn't pull
// the full 12-column row to throw 11 of them away.
func verifyTargetIndex(ctx context.Context, ep Endpoint, name string) (types.MigrationCutoverStep, bool) {
	rows, err := ep.Indices.IndicesNamesStatus(ctx, types.IndexFilter{Index: name})
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   "verify-target-exists",
			Status: types.MigrationStatusBlock,
			Detail: fmt.Sprintf("listing target index %q: %v", name, err),
		}, false
	}
	for _, r := range rows {
		if r.Name != name {
			continue
		}
		if !strings.EqualFold(r.Status, "open") {
			return types.MigrationCutoverStep{
				Name:   "verify-target-exists",
				Status: types.MigrationStatusBlock,
				Detail: fmt.Sprintf("target index %q is %s; cutover requires it open", name, r.Status),
			}, false
		}
		return types.MigrationCutoverStep{
			Name:   "verify-target-exists",
			Status: types.MigrationStatusOK,
			Detail: fmt.Sprintf("%s is open", name),
		}, true
	}
	return types.MigrationCutoverStep{
		Name:   "verify-target-exists",
		Status: types.MigrationStatusBlock,
		Detail: fmt.Sprintf("target index %q does not exist on %s", name, ep.Context),
	}, false
}

// inspectAliasBinding reads /_cat/aliases?alias=<a> to confirm
// FromIndex is currently bound. Returns warn (not block) when the
// alias does not point at FromIndex — the operator may be running
// cutover with a manual half-swap already in place.
func inspectAliasBinding(ctx context.Context, ep Endpoint, req types.MigrationCutoverRequest) types.MigrationCutoverStep {
	aliases, err := ep.AliasInspect.Aliases(ctx, types.AliasFilter{Alias: req.Alias})
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   "inspect-alias",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("could not read alias bindings: %v", err),
		}
	}
	if len(aliases) == 0 {
		return types.MigrationCutoverStep{
			Name:   "inspect-alias",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("alias %q does not currently exist; the swap will create it pointing at %s", req.Alias, req.ToIndex),
		}
	}
	bound := make([]string, 0, len(aliases))
	fromBound := false
	for _, a := range aliases {
		bound = append(bound, a.Index)
		if a.Index == req.FromIndex {
			fromBound = true
		}
	}
	if fromBound {
		return types.MigrationCutoverStep{
			Name:   "inspect-alias",
			Status: types.MigrationStatusOK,
			Detail: fmt.Sprintf("alias %q currently fronts: %s", req.Alias, strings.Join(bound, ", ")),
		}
	}
	return types.MigrationCutoverStep{
		Name:   "inspect-alias",
		Status: types.MigrationStatusWarn,
		Detail: fmt.Sprintf("alias %q does not currently include %s (bound to: %s)", req.Alias, req.FromIndex, strings.Join(bound, ", ")),
	}
}

// setWriteBlock toggles index.blocks.write on FromIndex. enable=true
// blocks; enable=false unblocks (sends `null` so the cluster removes
// the key rather than leaving a literal "false" in /_settings).
func setWriteBlock(ctx context.Context, ep Endpoint, req types.MigrationCutoverRequest, enable bool) (types.MigrationCutoverStep, bool) {
	verb := "block-writes"
	if !enable {
		verb = "unblock-writes"
	}
	if req.DryRun {
		detail := "would set index.blocks.write=true on " + req.FromIndex
		if !enable {
			detail = "would clear index.blocks.write on " + req.FromIndex
		}
		return types.MigrationCutoverStep{Name: verb, Status: skippedStatus, Detail: detail}, true
	}

	body, err := buildWriteBlockBody(enable)
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   verb,
			Status: types.MigrationStatusBlock,
			Detail: fmt.Sprintf("encoding settings body: %v", err),
		}, false
	}
	_, err = ep.IndexSettingsSet.PutSettings(ctx, types.IndexSettingsUpdateRequest{
		Indices: []string{req.FromIndex},
		Body:    body,
	})
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   verb,
			Status: types.MigrationStatusBlock,
			Detail: fmt.Sprintf("PUT /%s/_settings failed: %v", req.FromIndex, err),
		}, false
	}
	detail := fmt.Sprintf("set index.blocks.write=true on %s", req.FromIndex)
	if !enable {
		detail = fmt.Sprintf("cleared index.blocks.write on %s", req.FromIndex)
	}
	return types.MigrationCutoverStep{Name: verb, Status: types.MigrationStatusOK, Detail: detail}, true
}

// performAliasSwap issues the atomic add+remove on /_aliases. On a
// dry-run the action is skipped and the would-be body is summarised in
// the step detail.
func performAliasSwap(ctx context.Context, ep Endpoint, req types.MigrationCutoverRequest) (types.MigrationCutoverStep, bool) {
	if req.DryRun {
		return types.MigrationCutoverStep{
			Name:   "alias-swap",
			Status: skippedStatus,
			Detail: fmt.Sprintf("would atomically remove %s ←%s and add %s →%s", req.Alias, req.FromIndex, req.Alias, req.ToIndex),
		}, true
	}
	actions := []types.AliasUpdateAction{
		{Action: types.AliasActionRemove, Alias: req.Alias, Index: req.FromIndex},
		{Action: types.AliasActionAdd, Alias: req.Alias, Index: req.ToIndex},
	}
	res, err := ep.AliasUpdate.UpdateAliases(ctx, types.AliasUpdateRequest{Actions: actions})
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   "alias-swap",
			Status: types.MigrationStatusBlock,
			Detail: fmt.Sprintf("POST /_aliases failed: %v", err),
		}, false
	}
	if !res.Acknowledged {
		return types.MigrationCutoverStep{
			Name:   "alias-swap",
			Status: types.MigrationStatusBlock,
			Detail: "cluster did not acknowledge the alias swap",
		}, false
	}
	return types.MigrationCutoverStep{
		Name:   "alias-swap",
		Status: types.MigrationStatusOK,
		Detail: fmt.Sprintf("%s now fronts %s", req.Alias, req.ToIndex),
	}, true
}

// rollbackWriteBlock is the post-failure cleanup for a cutover that
// blocked writes and then failed before swapping. We do not raise an
// error if the rollback itself fails — the operator is already
// looking at a failed swap, surfacing it as a warn keeps the original
// failure as the headline.
func rollbackWriteBlock(ctx context.Context, ep Endpoint, req types.MigrationCutoverRequest) types.MigrationCutoverStep {
	body, _ := buildWriteBlockBody(false)
	_, err := ep.IndexSettingsSet.PutSettings(ctx, types.IndexSettingsUpdateRequest{
		Indices: []string{req.FromIndex},
		Body:    body,
	})
	if err != nil {
		return types.MigrationCutoverStep{
			Name:   "rollback-write-block",
			Status: types.MigrationStatusWarn,
			Detail: fmt.Sprintf("rollback failed; %s remains write-blocked: %v", req.FromIndex, err),
		}
	}
	return types.MigrationCutoverStep{
		Name:   "rollback-write-block",
		Status: types.MigrationStatusOK,
		Detail: fmt.Sprintf("write-block on %s rolled back", req.FromIndex),
	}
}

// buildWriteBlockBody renders the {"index":{"blocks":{"write":<v>}}}
// body PUT /<index>/_settings expects. enable=true sets the block;
// enable=false sends `null` so the cluster removes the key rather
// than leaving a literal "false" in /_settings — a subsequent GET
// returns to the pre-cutover shape.
func buildWriteBlockBody(enable bool) ([]byte, error) {
	var v any
	if enable {
		v = true
	} else {
		v = nil
	}
	body := map[string]any{
		"index": map[string]any{
			"blocks": map[string]any{
				"write": v,
			},
		},
	}
	return json.Marshal(body)
}

// worstStepStatus returns the worst severity across the step list.
// "skipped" is treated as "ok" for the overall status — a skipped
// step on the dry-run path is intentional, not a failure mode.
func worstStepStatus(steps []types.MigrationCutoverStep) string {
	worst := types.MigrationStatusOK
	for _, s := range steps {
		switch s.Status {
		case types.MigrationStatusBlock:
			return types.MigrationStatusBlock
		case types.MigrationStatusWarn:
			if worst == types.MigrationStatusOK {
				worst = types.MigrationStatusWarn
			}
		}
	}
	return worst
}
