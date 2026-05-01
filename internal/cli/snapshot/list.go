package snapshot

import (
	"context"
	"fmt"
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

By default the repository listing redacts known-secret keys
(access_key, secret_key, etc.) from the settings cell. Pass
--show-secrets to include the raw values; useful for reproducing a
config and dangerous to feed into shell history or shared captures.

--slim asks the cluster for the lightweight snapshot envelope
(?verbose=false on ES 7.15+/OS 2.x). Drops indices, per-shard counters
and failures from each row — saves an order of magnitude of bandwidth
on repositories with thousands of snapshots and is a no-op on the
default table view, which doesn't render those fields.

--output ndjson streams each snapshot as soon as its repository
responds: large fleets stop blocking on the slowest repository.

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
				Usage: "Sort snapshots by: start_time (default), name, duration. Ignored on --output ndjson (streaming preserves arrival order).",
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
			&cli.BoolFlag{
				Name:  "slim",
				Usage: "Ask the cluster for the lightweight envelope (verbose=false). ES 7.15+ / OS 2.x.",
			},
			&cli.BoolFlag{
				Name:  "show-secrets",
				Usage: "Print repository settings verbatim including credential keys (default: redacted)",
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

func runRepositoriesList(ctx context.Context, cmd *cli.Command, s cmdsetup.Setup) error {
	rows, err := esopssnapshot.ListRepositories(ctx, s.Client.Snapshot, types.SnapshotRepositoryFilter{})
	if err != nil {
		return err
	}
	if !cmd.Bool("show-secrets") {
		rows = esopssnapshot.RedactRepositorySettings(rows)
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
		Slim:              cmd.Bool("slim"),
	}

	if s.Format == output.FormatNDJSON {
		return runSnapshotsListStream(ctx, s, filter)
	}

	rows, err := esopssnapshot.List(ctx, s.Client.Snapshot, filter)
	if err != nil {
		return err
	}
	s.Log("snapshot.list", "snapshots", len(rows), "repository", repository)
	return output.Snapshots(os.Stdout, rows, s.Format)
}

// runSnapshotsListStream is the NDJSON-streaming path: each snapshot
// encodes to stdout as soon as its repository's response decodes.
// Per-repo errors land on stderr (one warn line per repo) so a 20-repo
// fleet with one S3 outage still surfaces the rest of the inventory.
// The first per-repo error becomes the command's exit code so a CI
// pipeline doesn't silently miss a broken repository.
func runSnapshotsListStream(ctx context.Context, s cmdsetup.Setup, filter types.SnapshotFilter) error {
	stream, err := output.NewSnapshotsStream(os.Stdout, s.Format)
	if err != nil {
		return err
	}

	var repoErrs []string
	streamErr := esopssnapshot.Stream(ctx, s.Client.Snapshot, filter, func(snap types.Snapshot) error {
		return stream.Write(snap)
	}, func(repo string, err error) {
		fmt.Fprintf(os.Stderr, "snapshot list: repo %q: %v\n", repo, err)
		repoErrs = append(repoErrs, repo)
	})
	if streamErr != nil {
		return streamErr
	}
	s.Log("snapshot.list.stream", "repository", filter.Repository, "repo_errors", len(repoErrs))
	if len(repoErrs) > 0 {
		return fmt.Errorf("snapshot list: %d repo(s) failed: %v", len(repoErrs), repoErrs)
	}
	return nil
}
