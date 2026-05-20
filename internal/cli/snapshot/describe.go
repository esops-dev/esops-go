package snapshot

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one snapshot (metadata + live status)",
		ArgsUsage: "REPO/NAME",
		Description: `Aggregate the snapshot's metadata (state, indices, duration, shard
totals, version, includes-global-state, per-shard failures) with the
cluster's live /_status block (per-shard init/started/done/failed) into
one sectioned summary.

Read-only and safe against a prod-protected context.

REPO/NAME is required — a literal repository plus snapshot name
separated by '/'. Patterns are rejected on either side. The cluster's
/_status endpoint may 404 on a recently-deleted snapshot; the
metadata view above still surfaces every fact we got from the listing
call.

--watch repaints the summary on a fixed interval so an operator can
sit on the output while a long snapshot creates or restores and
watch the per-shard counters tick.`,
		Flags:         watch.Flags(),
		Action:        runDescribe,
		ShellComplete: completion.ForPositional(1, completion.SuggestSnapshotSpecs),
	}
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return client.UsageError("describe takes exactly one positional argument (REPO/NAME); got %d", args.Len())
	}
	spec := args.First()

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	caps := esopssnapshot.DescribeCapabilities{
		Snapshot: s.Client.Snapshot,
		Dialect:  string(s.Client.Info.Dialect),
	}

	return watch.Run(ctx, cmd, "esops snapshot describe", s.Format, func(ctx context.Context) error {
		desc, err := esopssnapshot.Describe(ctx, caps, esopssnapshot.DescribeOptions{Spec: spec})
		if err != nil {
			return err
		}
		s.Log("snapshot.describe",
			"repository", desc.Repository,
			"snapshot", desc.Name,
			"state", desc.Info.State,
			"shards_total", desc.Info.ShardsTotal,
			"warnings", len(desc.SectionWarnings),
		)
		return output.SnapshotDescribe(os.Stdout, desc, s.Format)
	})
}
