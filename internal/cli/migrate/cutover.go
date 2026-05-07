package migrate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsmigrate "github.com/esops-dev/esops-go/internal/migrate"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/types"
)

func cutoverCommand() *cli.Command {
	return &cli.Command{
		Name:  "cutover",
		Usage: "Atomic alias swap on a single cluster: alias FromIndex → ToIndex",
		Description: `Atomic alias swap on a single cluster — the post-reindex,
zero-downtime cutover step.

Single-cluster command (no --source / --target). Use the global
--context flag to name the cluster the alias lives on. For
cross-cluster migrations, --context is the target (where the reindex
landed); for in-place upgrades it's the same cluster throughout.

The sequence:

  1. Verify --to exists and is open on the cluster.
  2. Read the alias's current bindings; warn if --from is not in
     the bound set (manual half-swaps already in place are accepted).
  3. (Optional) PUT /<from>/_settings index.blocks.write=true so any
     in-flight writer on the alias gets a clear refusal during the
     swap rather than silently appending to the soon-to-be-orphan.
  4. POST /_aliases with {remove: alias←from, add: alias→to} as one
     body — atomic from every reader's perspective.
  5. Clear the write-block unless --keep-write-block is set.

If step 4 fails after step 3 set the block, esops attempts a
best-effort rollback on the block and surfaces both outcomes in the
steps[] log.

--alias, --from, --to must be literal names. Patterns are rejected at
the CLI layer — the swap touches exactly what the operator typed,
which is the entire point of having a purpose-built command for this.

Mutates cluster state; honours protection-tier checks. --dry-run runs
the read-only steps (verify-target-exists, inspect-alias) and prints
"would-do" details for every mutation step.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "alias",
				Required: true,
				Usage:    "Alias to swap (literal name)",
			},
			&cli.StringFlag{
				Name:     "from",
				Required: true,
				Usage:    "Index the alias currently fronts (literal name)",
			},
			&cli.StringFlag{
				Name:     "to",
				Required: true,
				Usage:    "Index the alias will front after the swap (literal name)",
			},
			&cli.BoolFlag{
				Name:  "block-writes",
				Usage: "Set index.blocks.write=true on --from before the swap (recommended for live aliases)",
			},
			&cli.DurationFlag{
				Name:  "drain-delay",
				Usage: "Pause between the write-block and the alias swap so in-flight bulk requests drain (e.g. '500ms', '2s'). 0 = no pause. Only honoured when --block-writes is set.",
			},
			&cli.BoolFlag{
				Name:  "keep-write-block",
				Usage: "Do not clear the write-block after the swap (leaves --from read-only). The cutover surfaces a manual-cleanup reminder; no background TTL applies.",
			},
		},
		Action: runCutover,
	}
}

func runCutover(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	ep := esopsmigrate.Endpoint{
		Context:          s.ContextName,
		URL:              s.Context.URL,
		Info:             s.Client.Info,
		Indices:          s.Client.Indices,
		IndexSettingsSet: s.Client.IndexSettingsSet,
		AliasUpdate:      s.Client.AliasUpdate,
		AliasInspect:     s.Client.AliasInspect,
		Health:           s.Client.Health,
	}
	res, err := esopsmigrate.Cutover(ctx, ep, types.MigrationCutoverRequest{
		Context:        s.ContextName,
		Alias:          cmd.String("alias"),
		FromIndex:      cmd.String("from"),
		ToIndex:        cmd.String("to"),
		BlockWrites:    cmd.Bool("block-writes"),
		KeepWriteBlock: cmd.Bool("keep-write-block"),
		DryRun:         cmd.Bool("dry-run"),
		DrainDelay:     cmd.Duration("drain-delay"),
	})
	if err != nil {
		return err
	}

	s.Log("migrate.cutover",
		"alias", res.Alias,
		"from", res.FromIndex,
		"to", res.ToIndex,
		"dry_run", res.DryRun,
		"acknowledged", res.Acknowledged,
		"status", res.Status,
	)

	if err := output.MigrationCutover(os.Stdout, res, s.Format); err != nil {
		return err
	}
	if res.Status == types.MigrationStatusBlock {
		return client.ErrDryRunBlocker
	}
	return nil
}
