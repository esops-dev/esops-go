package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsindex "github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

// aliasesCommand is the parent for `esops index aliases <list|add|remove>`.
// list is read-only; add and remove mutate cluster state and trigger
// the protection-tier check.
func aliasesCommand() *cli.Command {
	return &cli.Command{
		Name:  "aliases",
		Usage: "List, add, and remove alias→index bindings",
		Commands: []*cli.Command{
			aliasesListCommand(),
			aliasesAddCommand(),
			aliasesRemoveCommand(),
		},
	}
}

func aliasesListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List alias→index bindings",
		Description: `List the cluster's aliases via /_cat/aliases. One row per
alias→index pair: an alias fronting N indices materialises as N rows.

Read-only and safe to run against a prod-protected context at any time.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "alias",
				Usage: "Limit to alias name(s) or pattern(s), comma-separated (e.g. 'esops-logs,esops-events-*')",
			},
			&cli.StringFlag{
				Name:  "index",
				Usage: "Limit to alias rows whose index matches one of these names, comma-separated",
			},
		},
		Action: runAliasesList,
	}
}

func runAliasesList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.AliasListOptions{
		Alias: cmd.String("alias"),
		Index: cmd.String("index"),
	}

	if s.Format == output.FormatNDJSON {
		count := 0
		err := output.AliasesNDJSONStream(os.Stdout, func(yield func(types.Alias) error) error {
			return esopsindex.ListAliasesStream(ctx, s.Client.AliasInspect, opts, func(a types.Alias) error {
				count++
				return yield(a)
			})
		})
		if err != nil {
			return err
		}
		s.Log("index.aliases.list", "rows", count, "stream", true)
		return nil
	}

	rows, err := esopsindex.ListAliases(ctx, s.Client.AliasInspect, opts)
	if err != nil {
		return err
	}

	s.Log("index.aliases.list", "rows", len(rows))
	return output.Aliases(os.Stdout, rows, s.Format)
}

func aliasesAddCommand() *cli.Command {
	return &cli.Command{
		Name:  "add",
		Usage: "Bind an alias to an index",
		Description: `Bind an alias to a single index via POST /_aliases. Mutates cluster
state. Use --dry-run first; against a prod-protected context,
--i-know-this-is-prod is required.

Both --alias and --index must be literal names — patterns on the
update path are a real footgun (one typo can bind an alias to a
hundred indices). For bulk operations, drop down to the cluster's
raw API.

The optional --filter accepts a JSON object (an Elasticsearch /
OpenSearch query DSL fragment) to install a filtered alias; the
cluster validates it. --routing forwards as the alias's routing
value; --is-write-index marks this binding as the alias's write
target (used for rolling-write aliases).`,
		Flags:  aliasesUpdateFlags(true),
		Action: runAliasesAdd,
	}
}

func aliasesRemoveCommand() *cli.Command {
	return &cli.Command{
		Name:  "remove",
		Usage: "Remove an alias→index binding",
		Description: `Remove a single alias→index binding via POST /_aliases. Mutates
cluster state. Use --dry-run first; against a prod-protected
context, --i-know-this-is-prod is required.

Both --alias and --index must be literal names. To remove every
binding for an alias, run remove once per index (or use a script
on top of 'index aliases list -o json').`,
		Flags:  aliasesUpdateFlags(false),
		Action: runAliasesRemove,
	}
}

// aliasesUpdateFlags is the per-subcommand flag set shared between
// add and remove. add carries the optional knobs (filter, routing,
// is_write_index); remove only takes alias+index.
func aliasesUpdateFlags(isAdd bool) []cli.Flag {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:     "alias",
			Required: true,
			Usage:    "Alias name (literal, no patterns)",
		},
		&cli.StringFlag{
			Name:     "index",
			Required: true,
			Usage:    "Index name to bind/unbind (literal, no patterns)",
		},
	}
	if isAdd {
		flags = append(flags,
			&cli.StringFlag{
				Name:  "filter",
				Usage: "JSON filter object to install on the alias (e.g. '{\"term\":{\"tenant\":\"x\"}}')",
			},
			&cli.StringFlag{
				Name:  "routing",
				Usage: "Routing value applied to indexing and search via this alias",
			},
			&cli.BoolFlag{
				Name:  "is-write-index",
				Usage: "Mark this alias→index binding as the alias's write target",
			},
		)
	}
	return flags
}

func runAliasesAdd(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.AliasUpdateOptions{
		Alias:   cmd.String("alias"),
		Index:   cmd.String("index"),
		Filter:  cmd.String("filter"),
		Routing: cmd.String("routing"),
		DryRun:  cmd.Bool("dry-run"),
	}
	if cmd.IsSet("is-write-index") {
		v := cmd.Bool("is-write-index")
		opts.IsWriteIndex = &v
	}

	return runAliasesUpdate(ctx, s, types.AliasActionAdd, opts)
}

func runAliasesRemove(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.AliasUpdateOptions{
		Alias:  cmd.String("alias"),
		Index:  cmd.String("index"),
		DryRun: cmd.Bool("dry-run"),
	}
	return runAliasesUpdate(ctx, s, types.AliasActionRemove, opts)
}

// runAliasesUpdate is the shared dispatcher behind add and remove. The
// per-action call site stays small and the logging shape stays
// consistent across both subcommands.
func runAliasesUpdate(ctx context.Context, s cmdsetup.Setup, action types.AliasAction, opts esopsindex.AliasUpdateOptions) error {
	var (
		result types.AliasUpdateResult
		err    error
	)
	switch action {
	case types.AliasActionAdd:
		result, err = esopsindex.AddAlias(ctx, s.Client.AliasUpdate, opts)
	case types.AliasActionRemove:
		result, err = esopsindex.RemoveAlias(ctx, s.Client.AliasUpdate, opts)
	}
	if err != nil {
		return err
	}

	s.Log("index.aliases."+string(action),
		"alias", opts.Alias,
		"index", opts.Index,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.AliasUpdate(os.Stdout, result, s.Format)
}
