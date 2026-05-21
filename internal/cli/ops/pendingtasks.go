package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func pendingTasksCommand() *cli.Command {
	return &cli.Command{
		Name:  "pending-tasks",
		Usage: "List the master's pending cluster-state queue",
		Description: `Read-only listing over GET /_cluster/pending_tasks. The cluster
reports work the master has accepted but not yet committed to cluster
state — anything sitting here at depth or for a long time is usually
a master-overload smell.

ClusterHealth.NumberOfPendingTasks (visible on 'esops ops health')
gives the count; this command shows the per-task detail (priority,
source, time-in-queue, executing-vs-queued) you need to tell a
healthy burst from a stuck master.

The cluster's own queue ordering is preserved — entries with lower
insert_order are older.`,
		Flags:  watch.Flags(),
		Action: runPendingTasks,
	}
}

func runPendingTasks(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops ops pending-tasks", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		tasks, err := esopsops.ListPendingTasks(ctx, s.Client.PendingTasks)
		if err != nil {
			return err
		}
		s.Log("ops.pending_tasks", "tasks", len(tasks))
		return output.PendingTasks(os.Stdout, tasks, s.Format)
	})
}
