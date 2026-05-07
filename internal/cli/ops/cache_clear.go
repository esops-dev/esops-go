package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func cacheClearCommand() *cli.Command {
	return &cli.Command{
		Name:  "cache-clear",
		Usage: "Clear fielddata / query / request caches by index pattern",
		Description: `Clear caches via POST /<indices>/_cache/clear. Without any of
--fielddata / --query / --request the cluster's default applies,
which is to clear every cache on the named indices.

--fields narrows fielddata clearing to specific field names (only
honoured when --fielddata is set; the cluster ignores --fields
otherwise).

Mutates cluster state. The cluster has no native dry-run for the
endpoint, so --dry-run is a client-side "validate + report" path that
echoes the resolved indices and cache labels. Against a prod-protected
context, --i-know-this-is-prod is required.

The match-everything shortcuts ('*' and '_all') are refused outright;
name a pattern that matches the indices whose cache you intend to
clear. A fielddata flush evicts the heap in one go on busy clusters,
so an explicit pattern keeps grep-for-intent in shell history.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "index",
				Required: true,
				Usage:    "Comma-separated index names or patterns (e.g. 'logs-2024-*'); '*' / '_all' refused",
			},
			&cli.BoolFlag{
				Name:  "fielddata",
				Usage: "Clear fielddata cache",
			},
			&cli.BoolFlag{
				Name:  "query",
				Usage: "Clear query cache",
			},
			&cli.BoolFlag{
				Name:  "request",
				Usage: "Clear request cache",
			},
			&cli.StringSliceFlag{
				Name:  "fields",
				Usage: "Narrow fielddata clear to specific field name(s); requires --fielddata",
			},
			&cli.BoolFlag{
				Name:  "ignore-unavailable",
				Usage: "Treat missing indices as a no-op instead of a 404 error",
			},
		},
		Action: runCacheClear,
	}
}

func runCacheClear(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsops.CacheClearOptions{
		Index:             cmd.String("index"),
		Fielddata:         cmd.Bool("fielddata"),
		Query:             cmd.Bool("query"),
		Request:           cmd.Bool("request"),
		Fields:            cmd.StringSlice("fields"),
		IgnoreUnavailable: cmd.Bool("ignore-unavailable"),
		DryRun:            cmd.Bool("dry-run"),
	}
	result, err := esopsops.CacheClear(ctx, s.Client.IndexCacheClear, opts)
	if err != nil {
		return err
	}
	s.Log("ops.cache_clear",
		"index", opts.Index,
		"caches", result.Caches,
		"dry_run", result.DryRun,
		"shards_total", result.Total,
		"shards_failed", result.Failed,
	)
	return output.IndexCacheClear(os.Stdout, result, s.Format)
}
