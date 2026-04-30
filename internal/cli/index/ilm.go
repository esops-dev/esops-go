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

// ilmCommand is the parent for `esops index ilm <list|get|put|delete|
// explain>` against an Elasticsearch cluster's ILM admin surface.
//
// ILM is Elasticsearch-only. On an OpenSearch context every subcommand
// returns ErrUnsupported with a clear "use index ism instead" message
// (exit code 6). The CLI surface is parallel to `index ism` on purpose
// so muscle memory transfers between dialects — the wire contract
// underneath is what diverges, not the operator's mental model.
func ilmCommand() *cli.Command {
	return &cli.Command{
		Name:  "ilm",
		Usage: "Manage Elasticsearch index lifecycle policies (Elasticsearch only)",
		Commands: []*cli.Command{
			ilmListCommand(),
			ilmGetCommand(),
			ilmPutCommand(),
			ilmDeleteCommand(),
			ilmExplainCommand(),
		},
	}
}

func ilmListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List ILM policies",
		Description: `List ILM policies via GET /_ilm/policy. The default view shows
name, version, modified date, and how many indices each policy
currently manages; -o wide adds reference counts for data streams
and composable templates.

Read-only and safe to run against a prod-protected context at any
time. --name takes one or more policy names or patterns ("logs-*")
to narrow the response.

ILM is Elasticsearch-only — running this against an OpenSearch
context fails with exit code 6 and a hint to use 'index ism' instead.`,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:  "name",
				Usage: "Limit to specific policy name(s) or pattern(s); repeatable",
			},
		},
		Action: runILMList,
	}
}

func runILMList(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	rows, err := esopsindex.ListILMPolicies(ctx, s.Client.ILM, esopsindex.ILMListOptions{
		Names: cmd.StringSlice("name"),
	})
	if err != nil {
		return err
	}

	s.Log("index.ilm.list", "rows", len(rows))
	return output.ILMPolicies(os.Stdout, rows, s.Format)
}

func ilmGetCommand() *cli.Command {
	return &cli.Command{
		Name:  "get",
		Usage: "Get a single ILM policy",
		Description: `Get one ILM policy by name via GET /_ilm/policy/<name>. --name
must be a literal policy name (no patterns); for pattern matching,
use 'index ilm list --name'.

In table mode the default view is a one-line header; -o wide
appends the in-use-by index list and the full policy body
(phases / actions) as indented JSON. JSON/YAML emit the neutral
ILMPolicy shape directly.

Read-only. ILM is Elasticsearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal)",
			},
		},
		Action: runILMGet,
	}
}

func runILMGet(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	p, err := esopsindex.GetILMPolicy(ctx, s.Client.ILM, cmd.String("name"))
	if err != nil {
		return err
	}

	s.Log("index.ilm.get", "name", p.Name)
	return output.ILMPolicy(os.Stdout, p, s.Format)
}

func ilmPutCommand() *cli.Command {
	return &cli.Command{
		Name:  "put",
		Usage: "Create or update an ILM policy",
		Description: `Create or update an ILM policy via PUT /_ilm/policy/<name>. The
body must be a JSON object that wraps the phases under a top-level
"policy" key — without it the cluster rejects the call, so esops
refuses up-front rather than letting the cluster reject a request
that's already in its audit log.

Body input is exactly one of:
  --body JSON         Literal JSON object on the command line
  --from-file PATH    Read the JSON object from a file

Mutates cluster state. The cluster has no native dry-run for
/_ilm/policy, so --dry-run is a client-side "validate + report"
path. Against a prod-protected context, --i-know-this-is-prod is
required. ILM is Elasticsearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "body",
				Usage: "Raw JSON object (policy definition)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON policy body",
			},
		},
		Action: runILMPut,
	}
}

func runILMPut(ctx context.Context, cmd *cli.Command) error {
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

	opts := esopsindex.ILMPutOptions{
		Name:   cmd.String("name"),
		Body:   body,
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopsindex.PutILMPolicy(ctx, s.Client.ILM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ilm.put",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.ILMPolicyPut(os.Stdout, result, s.Format)
}

func ilmDeleteCommand() *cli.Command {
	return &cli.Command{
		Name:  "delete",
		Usage: "Delete an ILM policy",
		Description: `Delete an ILM policy by name via DELETE /_ilm/policy/<name>. The
cluster refuses (with the list of attached indices in the error)
when the policy is still in use; that error surfaces verbatim.

Mutates cluster state. The cluster has no native dry-run for the
delete endpoint; --dry-run is a client-side "validate + report"
path. Against a prod-protected context, --i-know-this-is-prod is
required. ILM is Elasticsearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Policy name (literal, no patterns)",
			},
		},
		Action: runILMDelete,
	}
}

func runILMDelete(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		Destructive:      true,
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.ILMDeleteOptions{
		Name:   cmd.String("name"),
		DryRun: cmd.Bool("dry-run"),
	}
	result, err := esopsindex.DeleteILMPolicy(ctx, s.Client.ILM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ilm.delete",
		"name", result.Name,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.ILMPolicyDelete(os.Stdout, result, s.Format)
}

func ilmExplainCommand() *cli.Command {
	return &cli.Command{
		Name:  "explain",
		Usage: "Explain ILM state for one or more indices",
		Description: `Show each index's current lifecycle phase / action / step via
GET /<index>/_ilm/explain. The default table includes the policy
attached, where the index sits in the state machine, and how long
it has been at the current step (computed from the cluster's
lifecycle_date_millis). -o wide adds the failed-step name and a
compact step_info preview for stuck indices.

--only-errors maps to the cluster's ?only_errors=true and returns
only indices stuck in the ERROR step — the right starting point
for "what is broken right now?". On large clusters the cluster
side of /_ilm/explain can take more than the global 30s default;
raise --timeout if you hit a deadline.

Read-only and safe to run on a prod-protected context. ILM is
Elasticsearch-only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "index",
				Usage: "Comma-separated index names or patterns (default: '*')",
			},
			&cli.BoolFlag{
				Name:  "only-errors",
				Usage: "Forward as ?only_errors=true (return only indices stuck in the ERROR step)",
			},
			&cli.BoolFlag{
				Name:  "only-managed",
				Usage: "Forward as ?only_managed=true (omit indices with no policy attached)",
			},
		},
		Action: runILMExplain,
	}
}

func runILMExplain(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStreaming,
	})
	if err != nil {
		return err
	}

	opts := esopsindex.ILMExplainOptions{
		Index:       cmd.String("index"),
		OnlyErrors:  cmd.Bool("only-errors"),
		OnlyManaged: cmd.Bool("only-managed"),
	}

	if s.Format == output.FormatNDJSON {
		count := 0
		err := output.ILMExplainNDJSONStream(os.Stdout, func(yield func(types.ILMExplainEntry) error) error {
			return esopsindex.ExplainILMStream(ctx, s.Client.ILM, opts, func(e types.ILMExplainEntry) error {
				count++
				return yield(e)
			})
		})
		if err != nil {
			return err
		}
		s.Log("index.ilm.explain",
			"index", cmd.String("index"),
			"rows", count,
			"stream", true,
		)
		return nil
	}

	rows, err := esopsindex.ExplainILM(ctx, s.Client.ILM, opts)
	if err != nil {
		return err
	}

	s.Log("index.ilm.explain",
		"index", cmd.String("index"),
		"rows", len(rows),
	)
	return output.ILMExplain(os.Stdout, rows, s.Format)
}
