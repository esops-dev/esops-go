package snapshot

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/output"
	esopssnapshot "github.com/esops-dev/esops-go/internal/snapshot"
	"github.com/esops-dev/esops-go/pkg/types"
)

// listCommand is `esops snapshot list` — list snapshots inside one or
// more repositories. The parallel verb `snapshot repository list`
// lists registered repositories; the no-flag form here used to fall
// back to repository listing, but that hand-rolled overload was
// removed when `snapshot repository` landed. A bare `snapshot list`
// now refuses with a usage error pointing at the right verb — two
// verbs, two behaviours, no surprises.
func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List snapshots in one or more repositories",
		Description: `List snapshots in matching repositories. Requires --repository
<name|pattern> to narrow the scope, or --all to fan out across every
registered repository. To list registered repositories themselves use
'esops snapshot repository list'.

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
		},
		Action: runList,
	}
}

func runList(ctx context.Context, cmd *cli.Command) error {
	repository := cmd.String("repository")
	all := cmd.Bool("all")
	snapshotPattern := cmd.String("snapshot")

	// `snapshot list` lists snapshots; repositories have their own
	// verb (`snapshot repository list`). A bare `snapshot list` is
	// therefore a usage error rather than a silent repo-listing
	// fallback, so muscle memory built around the old behaviour
	// surfaces loudly instead of producing different data.
	if repository == "" && !all {
		return client.UsageError("snapshot list requires --repository or --all; use 'esops snapshot repository list' to list registered repositories")
	}
	// --snapshot only makes sense in snapshot-listing mode; refuse it
	// without --repository/--all so a typo surfaces here rather than
	// producing a confusing "no rows" output.
	if snapshotPattern != "" && repository == "" && !all {
		return client.UsageError("--snapshot requires --repository or --all")
	}

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	return runSnapshotsList(ctx, cmd, s, repository, snapshotPattern)
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
