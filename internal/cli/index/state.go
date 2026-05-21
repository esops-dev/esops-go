package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/onerror"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func openCommand() *cli.Command {
	return &cli.Command{
		Name:  "open",
		Usage: "Open one or more closed indices",
		Description: `Transition closed indices back to the open state via /{index}/_open.
Indices already open are skipped (reported under "skipped" in the
result), so re-running the command is a safe no-op.

Mutates cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.

--on-error abort (default) issues a single bulk /_open; skip dispatches
per-index and continues past failures; rollback re-closes
already-opened indices when a later one fails.`,
		Flags:  stateChangeFlags(),
		Action: runStateChange(types.IndexStateOpen),
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
	}
}

func closeCommand() *cli.Command {
	return &cli.Command{
		Name:  "close",
		Usage: "Close one or more open indices",
		Description: `Transition open indices to the closed state via /{index}/_close.
Closed indices stop accepting reads and writes and free their shard-
level resources, while staying recoverable on disk.

Mutates cluster state. The resolved pattern is capped at 20 indices
by default — pass --max-indices N for larger sweeps. Use --dry-run
first; against a prod-protected context, --i-know-this-is-prod is
required.

--on-error abort (default) issues a single bulk /_close; skip
dispatches per-index and continues past failures; rollback re-opens
already-closed indices when a later one fails.`,
		Flags:  stateChangeFlags(),
		Action: runStateChange(types.IndexStateClose),
		ShellComplete: completion.ForFlags(map[string]completion.Suggester{
			"index": completion.SuggestIndices,
		}),
	}
}

// stateChangeFlags is the per-subcommand flag set shared by `index open`
// and `index close`. --index is required (refused at the business layer
// with a clear message anyway, but urfave's required:true gives the
// faster "missing flag" message). --max-indices is the explicit opt-in
// past the 20-index blast-radius cap; --ignore-unavailable and
// --wait-for-active-shards are passed through to the cluster.
func stateChangeFlags() []cli.Flag {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:     "index",
			Required: true,
			Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*,archive-2023')",
		},
		&cli.IntFlag{
			Name:  "max-indices",
			Usage: "Override the default 20-index blast-radius cap on the resolved pattern",
		},
		&cli.BoolFlag{
			Name:  "ignore-unavailable",
			Usage: "Treat missing indices as a no-op instead of a 404 error",
		},
		&cli.StringFlag{
			Name:  "wait-for-active-shards",
			Usage: "Forward to the cluster as ?wait_for_active_shards=<value> (numeric or 'all')",
		},
	}
	return append(flags, cmdsetup.OnErrorFlags()...)
}

// runStateChange returns an Action closure for the requested action.
// Sharing the body keeps the open/close subcommands symmetric — there
// is no command-specific behaviour beyond which verb is logged and
// passed to the business layer.
func runStateChange(action types.IndexStateAction) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
			Destructive:      true,
			SupportedFormats: cmdsetup.FormatsStandard,
		})
		if err != nil {
			return err
		}

		policy, err := cmdsetup.OnErrorPolicy(cmd)
		if err != nil {
			return err
		}

		opts := esopsindex.StateChangeOptions{
			Action:              action,
			Pattern:             cmd.String("index"),
			MaxIndices:          cmd.Int("max-indices"),
			DryRun:              cmd.Bool("dry-run"),
			IgnoreUnavailable:   cmd.Bool("ignore-unavailable"),
			WaitForActiveShards: cmd.String("wait-for-active-shards"),
			OnError:             policy,
		}

		result, err := esopsindex.ChangeState(ctx, s.Client.Indices, s.Client.IndexState, opts)
		// Rollback path returns the triggering error alongside a
		// populated result (the per-item summary recorded the
		// rollback outcomes). Render the result first; then report.
		if err != nil && result.OnError == nil {
			return err
		}

		s.Log("index.state-change",
			"action", string(action),
			"dry_run", result.DryRun,
			"acknowledged", result.Acknowledged,
			"changed", len(result.Indices),
			"skipped", len(result.Skipped),
		)
		if rerr := output.IndexStateChange(os.Stdout, result, s.Format); rerr != nil {
			return rerr
		}
		output.OnErrorSummaryFooter(os.Stdout, result.OnError, s.Format, cmdsetup.Verbose(cmd))
		if err != nil {
			return err
		}
		if result.OnError != nil {
			return onerror.SummaryError(*result.OnError)
		}
		return nil
	}
}
