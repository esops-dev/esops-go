package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// ListTasksStream is the streaming companion to ListTasks: invokes
// yield per row as the cluster's response is decoded. Filter
// normalisation matches ListTasks byte-for-byte (comma-split, dedupe,
// whitespace reject); sorting is *not* applied — sort buffers the
// whole result, which defeats the streaming purpose. NDJSON consumers
// that want a stable order should pipe through `sort` themselves.
func ListTasksStream(ctx context.Context, c client.TasksInspector, filter types.TasksFilter, yield func(types.Task) error) error {
	cleaned, err := normaliseTasksFilter(filter)
	if err != nil {
		return err
	}
	return c.TasksStream(ctx, cleaned, yield)
}

// ListTasks returns the cluster's currently-running tasks, filtered by
// the supplied actions/nodes/parent IDs. Filtering is server-side; the
// business layer's only post-processing is the input normalisation
// (whitespace trim, comma-split, dedupe of repeated `--actions` /
// `--nodes` flags) so a caller passing the raw urfave/cli StringSlice
// does not have to clean it up first.
//
// `ops tasks list` is read-only on every cluster — no protection-tier
// gate, no dry-run mode. The filter shape is intentionally narrow:
// time-since-start filtering is left to the renderer (the cluster does
// not surface a `running_for_at_least` query param) and free-text
// description matching belongs in shell-side `grep`.
func ListTasks(ctx context.Context, c client.TasksInspector, filter types.TasksFilter) ([]types.Task, error) {
	cleaned, err := normaliseTasksFilter(filter)
	if err != nil {
		return nil, err
	}
	tasks, err := c.Tasks(ctx, cleaned)
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// CancelTasksDeps bundles the capabilities the cancel path needs. The
// inspector is required for the dry-run preview (list-then-report
// without firing the POST) and for the post-cancel listing the
// renderer uses to surface tasks that survived because they were not
// `cancellable` — the cluster accepts the POST silently in that case,
// so we have to compare before/after to give the operator a useful
// answer.
type CancelTasksDeps struct {
	Inspector client.TasksInspector
	Canceller client.TasksCanceller
}

// CancelTasks cancels one or more tasks by ID, or a filtered set by
// actions/nodes/parent. Mutually exclusive at validation: passing both
// TaskIDs and any filter field is rejected. An empty request (no IDs,
// no filter) is rejected too — the cluster's bare POST /_tasks/_cancel
// cancels every cancellable task in the cluster, which is a foot-gun
// this command refuses to expose.
//
// Multi-ID requests fan out across the capability one ID at a time
// (the cluster's POST /_tasks/<id>/_cancel takes a single ID; there is
// no batched endpoint to fold this into). Per-ID results are
// aggregated: Cancelled and NodeFailures are concatenated, TaskIDs
// echoes the full input set. A mid-fan-out error returns the partial
// aggregate plus the error so the operator can see which IDs landed
// before the failure.
//
// For DryRun, the function lists the tasks the live call would match
// and returns them in TasksCancelResult.Cancelled with DryRun=true and
// no POST fired. The renderer treats this exactly like a live cancel
// result, so a captured `--dry-run -o json` record is the same shape a
// live run produces.
func CancelTasks(ctx context.Context, deps CancelTasksDeps, req types.TasksCancelRequest, dryRun bool) (types.TasksCancelResult, error) {
	cleanedFilter, err := normaliseTasksFilter(req.Filter)
	if err != nil {
		return types.TasksCancelResult{}, err
	}
	cleanedIDs, err := normaliseTaskIDs(req.TaskIDs)
	if err != nil {
		return types.TasksCancelResult{}, err
	}
	if err := validateCancelRequest(cleanedIDs, cleanedFilter); err != nil {
		return types.TasksCancelResult{}, err
	}

	result := types.TasksCancelResult{
		Action:            "cancel",
		DryRun:            dryRun,
		TaskIDs:           cleanedIDs,
		WaitForCompletion: req.WaitForCompletion,
		Cancelled:         []types.Task{},
	}

	if dryRun {
		matches, err := matchTasksForCancel(ctx, deps.Inspector, cleanedIDs, cleanedFilter)
		if err != nil {
			return result, err
		}
		// Surface non-cancellable matches as a blocker so the dry-run
		// fails closed with exit code 7 — the operator typed an
		// `--actions` pattern that hits a force_merge / snapshot task,
		// the live run would silently leave it running, and we want
		// that mismatch loud before they re-invoke without --dry-run.
		nonCancellable := pickNonCancellable(matches)
		result.Cancelled = matches
		if len(nonCancellable) > 0 {
			return result, fmt.Errorf("%w: %d task(s) match but are not cancellable: %s",
				client.ErrDryRunBlocker, len(nonCancellable), strings.Join(taskIDs(nonCancellable), ", "))
		}
		return result, nil
	}

	// Filter mode: one capability call.
	if len(cleanedIDs) == 0 {
		out, err := deps.Canceller.CancelTasks(ctx, types.TasksCancelRequest{
			Filter:            cleanedFilter,
			WaitForCompletion: req.WaitForCompletion,
		})
		if err != nil {
			return result, err
		}
		result.Cancelled = nonNilTasks(out.Cancelled)
		result.NodeFailures = out.NodeFailures
		return result, nil
	}

	// ID mode: fan out one POST per ID, fold the per-call result into
	// the aggregate. Stop on the first per-call error and return what
	// we have so far so the operator sees the partial success.
	for _, id := range cleanedIDs {
		out, err := deps.Canceller.CancelTasks(ctx, types.TasksCancelRequest{
			TaskIDs:           []string{id},
			WaitForCompletion: req.WaitForCompletion,
		})
		result.Cancelled = append(result.Cancelled, out.Cancelled...)
		result.NodeFailures = append(result.NodeFailures, out.NodeFailures...)
		if err != nil {
			return result, fmt.Errorf("cancel %s: %w", id, err)
		}
	}
	return result, nil
}

// nonNilTasks normalises a possibly-nil slice from the capability into
// the empty-slice convention the renderer prefers: a captured JSON
// record always carries `cancelled: []` rather than `cancelled: null`,
// which scripts can iterate without an extra null check.
func nonNilTasks(in []types.Task) []types.Task {
	if in == nil {
		return []types.Task{}
	}
	return in
}

// validateCancelRequest enforces the "exactly one of TaskIDs or Filter"
// rule. The cluster has no opinion (it'll happily ignore TaskID when
// `actions=…` is set, or fall back to the bare cancel-all when both are
// empty); the foot-gun control belongs at the business layer where the
// rejection is loud. Each ID must carry the cluster's "<node>:<num>"
// shape — a malformed input is rejected before any fan-out so a typo on
// the third of five IDs doesn't leave the first two cancelled.
func validateCancelRequest(taskIDs []string, filter types.TasksFilter) error {
	hasFilter := len(filter.Actions) > 0 || len(filter.Nodes) > 0 || filter.ParentTaskID != ""
	hasIDs := len(taskIDs) > 0
	switch {
	case !hasIDs && !hasFilter:
		return fmt.Errorf("either a task ID or --actions/--nodes/--parent-task-id is required (refusing to cancel every task in the cluster)")
	case hasIDs && hasFilter:
		return fmt.Errorf("task IDs and --actions/--nodes/--parent-task-id are mutually exclusive")
	}
	for _, id := range taskIDs {
		if !strings.Contains(id, ":") {
			return fmt.Errorf("task ID %q is malformed; expected the cluster's <node_id>:<task_number> shape (run 'esops ops tasks list' to find one)", id)
		}
	}
	return nil
}

// matchTasksForCancel runs the dry-run list-then-report flow. Filter
// matching is server-side; for ID-based cancellations we list once and
// pick the rows whose composite IDs match. Missing IDs return an error
// so a typo'd dry-run doesn't quietly skip a row.
func matchTasksForCancel(ctx context.Context, inspector client.TasksInspector, taskIDs []string, filter types.TasksFilter) ([]types.Task, error) {
	if len(taskIDs) > 0 {
		all, err := inspector.Tasks(ctx, types.TasksFilter{})
		if err != nil {
			return nil, err
		}
		byID := make(map[string]types.Task, len(all))
		for _, t := range all {
			byID[t.ID] = t
		}
		var (
			out     []types.Task
			missing []string
		)
		for _, id := range taskIDs {
			t, ok := byID[id]
			if !ok {
				missing = append(missing, id)
				continue
			}
			out = append(out, t)
		}
		if len(missing) == 1 {
			return nil, fmt.Errorf("task %q not found (it may have completed already)", missing[0])
		}
		if len(missing) > 1 {
			return nil, fmt.Errorf("tasks not found (may have completed already): %s", strings.Join(missing, ", "))
		}
		return out, nil
	}
	return inspector.Tasks(ctx, filter)
}

// normaliseTaskIDs trims and comma-splits the user-supplied positional
// arguments. Mirrors the --node normalisation done in drain so the
// operator can pass `cancel id1 id2 id3` or `cancel id1,id2,id3`
// interchangeably. Whitespace inside a token is rejected — task IDs
// never contain spaces, and a space here is almost certainly a missing
// comma.
func normaliseTaskIDs(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	return splitCSV("task ID", in)
}

// pickNonCancellable returns the subset of tasks the cluster will not
// honour a cancel signal on — typically force_merge, snapshot create,
// and a few internal transports. The operator's `--actions` pattern can
// match these inadvertently, and the cluster's silent no-op is exactly
// the surprise we want to surface up-front.
func pickNonCancellable(in []types.Task) []types.Task {
	var out []types.Task
	for _, t := range in {
		if !t.Cancellable {
			out = append(out, t)
		}
	}
	return out
}

// taskIDs is the projection helper used by error messages. Returning
// just IDs (not the full Task slice) keeps the message scannable when
// 20 tasks match a wildcard `--actions`.
func taskIDs(in []types.Task) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, t.ID)
	}
	return out
}

// normaliseTasksFilter trims, comma-splits, and de-duplicates the
// caller's actions/nodes slices. Mirrors the same shape `ops drain`
// does for --node so the operator can pass `--actions a,b --actions c`
// and get the same result as `--actions a,b,c`.
func normaliseTasksFilter(in types.TasksFilter) (types.TasksFilter, error) {
	actions, err := splitCSV("--actions", in.Actions)
	if err != nil {
		return types.TasksFilter{}, err
	}
	nodes, err := splitCSV("--nodes", in.Nodes)
	if err != nil {
		return types.TasksFilter{}, err
	}
	return types.TasksFilter{
		Actions:      actions,
		Nodes:        nodes,
		ParentTaskID: strings.TrimSpace(in.ParentTaskID),
		Detailed:     in.Detailed,
	}, nil
}

// splitCSV is the comma-split + trim + dedupe used by both --actions
// and --nodes. Whitespace inside an entry is rejected (cluster action
// names never contain spaces; a space here is almost certainly a typo
// where the operator forgot a comma).
func splitCSV(flag string, in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		for _, part := range strings.Split(raw, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			if strings.ContainsAny(p, " \t\n") {
				return nil, fmt.Errorf("%s %q contains whitespace; entries must be single tokens", flag, p)
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}
