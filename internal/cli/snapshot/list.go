package snapshot

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/output"
	esopssnapshot "github.com/dj-wasabi/esops-go/internal/snapshot"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// listCommand is `esops snapshot list`. By default lists registered
// repositories; with --repository (or --all) it lists snapshots inside
// matching repos. Two operator-facing questions ("what backups exist?"
// and "where can backups go?") merit one verb because both wear the
// same hat — read-only inventory of the cluster's snapshot surface.
func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List snapshot repositories or snapshots",
		Description: `Without --repository, lists registered snapshot repositories.
With --repository <name|pattern>, lists snapshots in matching repos.
Use --all to fan out across every repository.

Read-only and safe to run against a prod-protected context at any time.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "repository",
				Usage: "Limit to repository name(s), comma-separated. Implies snapshot listing.",
			},
			&cli.StringFlag{
				Name:  "snapshot",
				Usage: "Limit to snapshot name pattern(s), comma-separated (e.g. 'nightly-*'); requires --repository or --all",
			},
			&cli.StringFlag{
				Name:  "state",
				Usage: "Filter by snapshot state: success | failed | partial | in_progress | incompatible",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "Sort snapshots by: start_time (default), name, duration",
				Value: "start_time",
			},
			&cli.BoolFlag{
				Name:  "all",
				Usage: "List snapshots across every registered repository (fan-out)",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Pass ignore_unavailable=true so a missing-snapshot pattern is a no-op rather than a 404",
			},
		},
		Action: runList,
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	repository := cmd.String("repository")
	all := cmd.Bool("all")
	snapshotPattern := cmd.String("snapshot")

	// --snapshot only makes sense in snapshot-listing mode; refuse it in
	// the default repository-listing mode so a typo surfaces here rather
	// than producing a confusing "no rows" output.
	if snapshotPattern != "" && repository == "" && !all {
		return client.UsageError("--snapshot requires --repository or --all")
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	if repository == "" && !all {
		return runRepositoriesList(ctx, cmd, s)
	}
	return runSnapshotsList(ctx, cmd, s, repository, snapshotPattern)
}

func runRepositoriesList(ctx context.Context, _ *cli.Command, s cmdsetup.Setup) error {
	rows, err := esopssnapshot.ListRepositories(ctx, s.Client.Snapshot, types.SnapshotRepositoryFilter{})
	if err != nil {
		return err
	}
	s.Log("snapshot.list.repositories", "repositories", len(rows))
	return output.SnapshotRepositories(os.Stdout, rows, s.Format)
}

func runSnapshotsList(ctx context.Context, cmd *cli.Command, s cmdsetup.Setup, repository, snapshotPattern string) error {
	if state := cmd.String("state"); state != "" {
		if err := esopssnapshot.ValidateState(state); err != nil {
			return err
		}
	}
	sortKey, err := esopssnapshot.ParseSort(cmd.String("sort"))
	if err != nil {
		return err
	}

	filter := types.SnapshotFilter{
		Repository:        repository,
		Snapshot:          snapshotPattern,
		State:             cmd.String("state"),
		Sort:              sortKey,
		IgnoreUnavailable: cmd.Bool("ignore-unavailable"),
	}

	rows, err := esopssnapshot.List(ctx, s.Client.Snapshot, filter)
	if err != nil {
		return err
	}
	s.Log("snapshot.list", "snapshots", len(rows), "repository", repository)
	return output.Snapshots(os.Stdout, rows, s.Format)
}
