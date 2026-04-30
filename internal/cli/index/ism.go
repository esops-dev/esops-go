package index

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	esopsindex "github.com/dj-wasabi/esops-go/internal/index"
	"github.com/dj-wasabi/esops-go/internal/output"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// ismCommand is the parent for `esops index ism <list|get|put|delete|
// explain>` against an OpenSearch cluster's ISM plugin admin surface.
//
// ISM is OpenSearch-only. On an Elasticsearch context every subcommand
// returns ErrUnsupported with a clear "use index ilm instead" message
// (exit code 6). The CLI surface is parallel to `index ilm` on purpose
// so muscle memory transfers between dialects.
func ismCommand() *cli.Command {
	return &cli.Command{
		Name:  "ism",
		Usage: "Manage OpenSearch index state management policies (OpenSearch only)",
		Commands: []*cli.Command{
			ismListCommand(),
			ismGetCommand(),
			ismPutCommand(),
			ismDeleteCommand(),
			ismExplainCommand(),
		},
	}
}

func ismListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List ISM policies",
		Description: `List ISM policies via GET /_plugins/_ism/policies. The default
view shows id, the optimistic-concurrency tokens (seq_no /
primary_term) the plugin uses, and the policy description on -o
wide.

Read-only and safe to run against a prod-protected context at any
time. --id takes one or more policy ids; the plugin does not honour
comma-separated ids in a single call, so multi-id requests fan out
to one HTTP call per id.

ISM is OpenSearch-only — running this against an Elasticsearch
context fails with exit code 6 and a hint to use 'index ilm' instead.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "id",
				Usage: "Limit to specific policy id(s); repeatable",
			},
		},
		Action: runISMList,
	}
}

func runISMList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopsindex.ListISMPolicies(ctx, s.Client.ISM, esopsindex.ISMListOptions{
		IDs: cmd.StringSlice("id"),
	})
	if err != nil {
		return err
	}

	s.Log("index.ism.list", "rows", len(rows))
	return output.ISMPolicies(os.Stdout, rows, s.Format)
}

func ismGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single ISM policy",
		Description: `Get one ISM policy by id via GET /_plugins/_ism/policies/<id>.
--id must be a literal policy id (no patterns).

In table mode the default view is a one-line header; -o wide
appends the full policy body (states / transitions / ism_template)
as indented JSON. JSON/YAML emit the neutral ISMPolicy shape
directly.

Read-only. ISM is OpenSearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "id",
				Required: true,
				Usage:    "Policy id (literal)",
			},
		},
		Action: runISMGet,
	}
}

func runISMGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	p, err := esopsindex.GetISMPolicy(ctx, s.Client.ISM, cmd.String("id"))
	if err != nil {
		return err
	}

	s.Log("index.ism.get", "id", p.ID)
	return output.ISMPolicy(os.Stdout, p, s.Format)
}

func ismPutCommand() *cli.Command {
	return &cli.Command{
		Name:  "put",
		Usage: "Create or update an ISM policy",
		Description: `Create or update an ISM policy via
PUT /_plugins/_ism/policies/<id>. The body must be a JSON object
that wraps the states/transitions under a top-level "policy" key.

Body input is exactly one of:
  --body JSON         Literal JSON object on the command line
  --from-file PATH    Read the JSON object from a file

Optimistic concurrency: pass --if-seq-no and --if-primary-term
together (matching the plugin's ?if_seq_no=N&if_primary_term=M
parameters) to refuse the write when the policy has been
concurrently modified. When omitted, the request is a create-or-
overwrite — safe for first-time installs in a CI pipeline.

Mutates cluster state. The plugin has no native dry-run, so
--dry-run is a client-side "validate + report" path. Against a
prod-protected context, --i-know-this-is-prod is required. ISM is
OpenSearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "id",
				Required: true,
				Usage:    "Policy id (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object (policy definition)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON policy body",
			},
			&cli.Int64Flag{
				Name:  "if-seq-no",
				Usage: "Optimistic-concurrency seq_no (refuse the write when the policy has been concurrently modified). Must be set together with --if-primary-term.",
			},
			&cli.Int64Flag{
				Name:  "if-primary-term",
				Usage: "Optimistic-concurrency primary_term. Must be set together with --if-seq-no.",
			},
		},
		Action: runISMPut,
	}
}

func runISMPut(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	body, err := resolveBody(cmd.String("body"), cmd.String("from-file"))
	if err != nil {
		return err
	}

	opts := esopsindex.ISMPutOptions{
		ID:          cmd.String("id"),
		Body:        body,
		SeqNo:       cmd.Int64("if-seq-no"),
		PrimaryTerm: cmd.Int64("if-primary-term"),
		DryRun:      cmd.Bool("dry-run"),
	}
	result, err := esopsindex.PutISMPolicy(ctx, s.Client.ISM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ism.put",
		"id", result.ID,
		"dry_run", result.DryRun,
		"seq_no", result.SeqNo,
		"primary_term", result.PrimaryTerm,
	)
	return output.ISMPolicyPut(os.Stdout, result, s.Format)
}

func ismDeleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete an ISM policy",
		Description: `Delete an ISM policy by id via
DELETE /_plugins/_ism/policies/<id>. The plugin refuses (typically
409) when the policy is still attached to managed indices; that
error surfaces verbatim.

Mutates cluster state. The plugin has no native dry-run, so
--dry-run is a client-side "validate + report" path. Against a
prod-protected context, --i-know-this-is-prod is required. ISM is
OpenSearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "id",
				Required: true,
				Usage:    "Policy id (literal, no patterns)",
			},
		},
		Action: runISMDelete,
	}
}

func runISMDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.ISMDeleteOptions{
		ID:     cmd.String("id"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopsindex.DeleteISMPolicy(ctx, s.Client.ISM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ism.delete",
		"id", result.ID,
		"dry_run", result.DryRun,
		"ok", result.OK,
	)
	return output.ISMPolicyDelete(os.Stdout, result, s.Format)
}

func ismExplainCommand() *cli.Command {
	return &cli.Command{
		Name:  "explain",
		Usage: "Explain ISM state for one or more indices",
		Description: `Show each index's current ISM state / action / step via
GET /_plugins/_ism/explain[/<index>]. The default table includes
the policy id attached, where the index sits in the state machine,
and whether the index's ISM management is enabled. -o wide adds a
compact step_info preview for stuck indices.

--show-policy maps to the plugin's ?show_policy=true and embeds
the full policy body in each entry — useful for one-shot audits,
but the response can be megabytes on a large fleet. Pair with a
larger --timeout (the global default is 30s) if you hit a timeout
on a multi-thousand-index fleet.

Read-only and safe to run on a prod-protected context. ISM is
OpenSearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Comma-separated index names or patterns (default: every managed index)",
			},
			&cli.BoolFlag{
				Name:  "show-policy",
				Usage: "Forward as ?show_policy=true (embed each managed index's policy body in the response)",
			},
		},
		Action: runISMExplain,
	}
}

func runISMExplain(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.ISMExplainOptions{
		Index:      cmd.String("index"),
		ShowPolicy: cmd.Bool("show-policy"),
	}

	if s.Format == output.FormatNDJSON {
		count := 0
		err := output.ISMExplainNDJSONStream(os.Stdout, func(yield func(types.ISMExplainEntry) error) error {
			return esopsindex.ExplainISMStream(ctx, s.Client.ISM, opts, func(e types.ISMExplainEntry) error {
				count++
				return yield(e)
			})
		})
		if err != nil {
			return err
		}
		s.Log("index.ism.explain",
			"index", cmd.String("index"),
			"rows", count,
			"stream", true,
		)
		return nil
	}

	rows, err := esopsindex.ExplainISM(ctx, s.Client.ISM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ism.explain",
		"index", cmd.String("index"),
		"rows", len(rows),
	)
	return output.ISMExplain(os.Stdout, rows, s.Format)
}
