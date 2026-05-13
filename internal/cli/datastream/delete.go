package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
)

func deleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete one or more data streams and all of their backing indices",
		Description: `Delete data streams via DELETE /_data_stream/<names>. Removes the
data stream object **and** every backing index it owns, so the
blast radius is much larger than a single 'index delete' — one
stream can hold dozens of generation indices.

--allow-data-loss is required (the verbose flag name is a feature:
grep-for-intent in shell history is a real auditing workflow).
The resolved match is capped at 5 streams by default — pass
--max-streams N for larger sweeps. Use --dry-run first; against a
prod-protected context, --i-know-this-is-prod is required.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Comma-separated data stream names or patterns (e.g. 'logs-*,metrics-eu')",
			},
			&cli.IntFlag{
				Name:  "max-streams",
				Usage: "Override the default 5-stream blast-radius cap on the resolved pattern",
			},
			&cli.BoolFlag{
				Name:  "allow-data-loss",
				Usage: "Confirm that deleting the matched streams also destroys every backing index they own",
			},
		},
		Action: runDelete,
	}
}

func runDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsdatastream.DeleteOptions{
		Pattern:       cmd.String("name"),
		MaxStreams:    cmd.Int("max-streams"),
		AllowDataLoss: cmd.Bool("allow-data-loss"),
		DryRun:        cmd.Bool("dry-run"),
	}

	result, err := esopsdatastream.Delete(ctx, s.Client.DataStream, s.Client.DataStreamManage, opts)
	if err != nil {
		return err
	}

	s.Log("datastream.delete",
		"streams", len(result.Names),
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.DataStreamDelete(os.Stdout, result, s.Format)
}
