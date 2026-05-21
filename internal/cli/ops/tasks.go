package ops

import (
	"context"
	"errors"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func tasksCommand() *cli.Command {
	return &cli.Command{
		Name:  "tasks",
		Usage: "List or cancel running cluster tasks",
		Description: `Read and mutate the cluster's /_tasks surface — reindex, force-merge,
search, and any other transport task the cluster reports. Broader than
'esops migrate reindex --resume': anything visible in /_tasks is
listed and (for cancellable types) cancellable here.`,
		Commands: []*cli.Command{
			tasksListCommand(),
			tasksCancelCommand(),
		},
	}
}

func tasksListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List in-flight cluster tasks (filtered by action, node, or parent)",
		Description: `Read-only listing over GET /_tasks. Default columns
answer the on-call's first question — what's running, on which node,
how long has it been going, and is it cancellable. -o wide adds the
description (only populated with --detailed), the parent task ID, and
the start time.

Filtering is server-side. --actions takes the cluster's dotted action
names with shell-style wildcards ("*search*", "indices:data/write/*").
--nodes accepts node names or IDs. --parent-task-id narrows to the
descendants of one composite ID (the same shape ops tasks cancel
expects).`,
		Flags: append([]cli.Flag{
			&cli.StringSliceFlag{
				Name:  "actions",
				Usage: "Filter by cluster action name(s). Repeatable, or comma-separated. Wildcards allowed",
			},
			&cli.StringSliceFlag{
				Name:  "nodes",
				Usage: "Filter by node name(s) or ID(s). Repeatable, or comma-separated",
			},
			&cli.StringFlag{
				Name:  "parent-task-id",
				Usage: "Filter to descendants of this composite task ID (<node_id>:<task_number>)",
			},
			&cli.BoolFlag{
				Name:  "detailed",
				Usage: "Ask the cluster for the per-task description field (slower; useful with -o wide)",
			},
		}, watch.Flags()...),
		Action:        runTasksList,
		ShellComplete: completion.ForFlags(nodeFlagSuggesters),
	}
}

func runTasksList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	filter := types.TasksFilter{
		Actions:      cmd.StringSlice("actions"),
		Nodes:        cmd.StringSlice("nodes"),
		ParentTaskID: cmd.String("parent-task-id"),
		Detailed:     cmd.Bool("detailed"),
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops ops tasks list", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		// NDJSON takes the streaming path: rows reach stdout as the
		// cluster's response is decoded so a fleet running thousands
		// of concurrent tasks doesn't have to be buffered before the
		// first row prints. Other formats (table/json/yaml/wide) need
		// the full sorted set, so they go through ListTasks which
		// buffers, sorts, and renders.
		if s.Format == output.FormatNDJSON {
			count := 0
			err := output.TasksNDJSONStream(os.Stdout, func(yield func(types.Task) error) error {
				return esopsops.ListTasksStream(ctx, s.Client.Tasks, filter, func(t types.Task) error {
					count++
					return yield(t)
				})
			})
			if err != nil {
				return err
			}
			s.Log("ops.tasks.list", "tasks", count, "stream", true,
				"actions", filter.Actions, "nodes", filter.Nodes,
				"parent_task_id", filter.ParentTaskID, "detailed", filter.Detailed,
			)
			return nil
		}

		tasks, err := esopsops.ListTasks(ctx, s.Client.Tasks, filter)
		if err != nil {
			return err
		}
		s.Log("ops.tasks.list", "tasks", len(tasks),
			"actions", filter.Actions, "nodes", filter.Nodes,
			"parent_task_id", filter.ParentTaskID, "detailed", filter.Detailed,
		)
		return output.Tasks(os.Stdout, tasks, s.Format)
	})
}

func tasksCancelCommand() *cli.Command {
	return &cli.Command{
		Name:      "cancel",
		Usage:     "Cancel one or more tasks by ID, or a filtered set by --actions/--nodes/--parent-task-id",
		ArgsUsage: "[<node_id>:<task_number> [<node_id>:<task_number> ...]]",
		Description: `Cancel one or more tasks by composite ID (positional arguments, the
same shape ops tasks list prints) or a filtered set via --actions/
--nodes/--parent-task-id. The two modes are mutually exclusive — pass
one or the other — and an empty invocation is rejected so a typo can't
accidentally fire the cluster's bare /_tasks/_cancel that would target
every task.

Multi-ID input accepts either repeated positionals (` + "`cancel id1 id2`" + `)
or a comma-separated list (` + "`cancel id1,id2`" + `). The cluster's cancel
endpoint takes one ID per request, so multi-ID calls fan out
client-side: each ID gets its own POST and per-ID results are merged
into a single output. A mid-fan-out failure surfaces the partial
success so the operator can see which IDs landed.

The cluster silently ignores cancel signals on tasks it considers
non-cancellable (force_merge, snapshot create, several internal
transports). --dry-run lists the matched tasks first and refuses with
exit code 7 if any are not cancellable, so the operator sees the
mismatch before re-invoking without --dry-run.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "actions",
				Usage: "Cancel tasks matching cluster action name(s). Repeatable / comma-separated. Wildcards allowed",
			},
			&cli.StringSliceFlag{
				Name:  "nodes",
				Usage: "Cancel tasks running on these node(s). Repeatable / comma-separated",
			},
			&cli.StringFlag{
				Name:  "parent-task-id",
				Usage: "Cancel tasks whose parent matches this composite task ID",
			},
			&cli.BoolFlag{
				Name:  "wait",
				Usage: "Block until the cluster reports the cancel landed (forwards wait_for_completion=true)",
			},
		},
		Action:        runTasksCancel,
		ShellComplete: completion.ForFlags(nodeFlagSuggesters),
	}
}

func runTasksCancel(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	// Multi-ID support: accept any number of positionals. The business
	// layer additionally comma-splits each token so `cancel id1,id2`
	// and `cancel id1 id2` are equivalent.
	taskIDs := cmd.Args().Slice()

	req := types.TasksCancelRequest{
		TaskIDs: taskIDs,
		Filter: types.TasksFilter{
			Actions:      cmd.StringSlice("actions"),
			Nodes:        cmd.StringSlice("nodes"),
			ParentTaskID: cmd.String("parent-task-id"),
		},
		WaitForCompletion: cmd.Bool("wait"),
	}
	deps := esopsops.CancelTasksDeps{
		Inspector: s.Client.Tasks,
		Canceller: s.Client.TasksCancel,
	}
	result, opErr := esopsops.CancelTasks(ctx, deps, req, cmd.Bool("dry-run"))

	// Render whenever we reached the cluster: result.Action is set by
	// CancelTasks once validation has passed and the live or dry-run
	// path was actually attempted. Bare validation errors leave the
	// result zero-valued — main prints the validation message and we
	// skip the empty render.
	if result.Action != "" || errors.Is(opErr, client.ErrDryRunBlocker) {
		s.Log("ops.tasks.cancel",
			"task_ids", result.TaskIDs,
			"dry_run", result.DryRun,
			"wait", result.WaitForCompletion,
			"cancelled", len(result.Cancelled),
			"node_failures", len(result.NodeFailures),
		)
		if renderErr := output.TasksCancel(os.Stdout, result, s.Format); renderErr != nil {
			return renderErr
		}
	}
	return opErr
}
