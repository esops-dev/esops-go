package migrate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/logging"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

// twinCompleteSuggesters drives tab-completion for the source/target
// flags shared across every twin-context migrate subcommand. Both flags
// take *context names* (not indices); --indices stays cluster-backed
// against whichever endpoint is being typed for, which is impractical
// to attribute correctly during completion, so it's left
// unsuggested.
var twinCompleteSuggesters = map[string]completion.Suggester{
	"source": completion.SuggestContexts,
	"target": completion.SuggestContexts,
}

func checkCommand() *cli.Command {
	return &cli.Command{
		Name:  "check",
		Usage: "Pre-flight a remote reindex: connectivity, version, and target reindex.remote.whitelist",
		Description: `Read-only pre-flight against the source and target clusters.

Surfaces the cluster-side conditions that decide whether a subsequent
'migrate reindex' will start at all:

  • source.connectivity   the GET / probe outcome (auth + version)
  • target.connectivity   the GET / probe outcome (auth + version)
  • dialect               same-dialect (ok) vs. cross-dialect (warn)
  • version               version-bridge sanity check
  • target.remote_whitelist  whether the source's host:port is allowed
                              by the target's reindex.remote.whitelist

Read-only on both clusters. The command exits non-zero (code 7) when
any check is reported as a blocker, so a CI pipeline can chain
'migrate check && migrate reindex' without an explicit grep.`,
		Flags:         twinFlags(),
		Action:        runCheck,
		ShellComplete: completion.ForFlags(twinCompleteSuggesters),
	}
}

func runCheck(ctx context.Context, cmd *cli.Command) error {
	s, err := prepareTwin(ctx, cmd, cmdsetup.FormatsStandard)
	if err != nil {
		return err
	}

	report, err := esopsmigrate.Check(ctx, s.Source, s.Target, types.MigrationCheckRequest{
		SourceContext: s.Source.Context,
		TargetContext: s.Target.Context,
		Indices:       cmd.String("indices"),
	})
	if err != nil {
		return err
	}

	logging.Logger().Info("migrate.check",
		"source", s.Source.Context,
		"target", s.Target.Context,
		"status", report.Status,
		"blockers", report.Blockers,
		"warnings", report.Warnings,
	)

	if err := output.MigrationCheck(os.Stdout, report, s.Format); err != nil {
		return err
	}

	if report.Status == types.MigrationStatusBlock {
		// Mirror the convention the reroute / restore paths use: a
		// dry-run blocker exits 7 so a CI script can chain check
		// and reindex without parsing stdout.
		return client.ErrDryRunBlocker
	}
	return nil
}
