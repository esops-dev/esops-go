package transform

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
)

func applyCommand() *cli.Command {
	return &cli.Command{
		Name:  "apply",
		Usage: "Create or update a transform from file or stdin",
		Description: `Create or update a transform from the supplied JSON body. Re-apply
with an unchanged body is a no-op — the business layer fetches the
existing definition and short-circuits when the submitted body
matches byte-for-byte (after canonical-JSON re-encode).

Body input is exactly one of:
  --from-file PATH     Read the JSON body from a file
  -                    Read the JSON body from stdin

The two products' transform body shapes diverge — ES expects top-
level 'source'/'dest'/'pivot'; OS wraps the definition in
'{"transform": {...}}' with 'source_index'/'target_index'/'groups'.
The bodies are NOT portable across dialects today; apply detects an
obvious mismatch and refuses with a clear message rather than
letting the cluster reject with a generic 400.

CREATE vs UPDATE protection-tier behaviour: a clean create against
a 'protection: prod' context proceeds without --i-know-this-is-prod
(the cluster has nothing to lose). An overwrite of an existing
definition is treated as destructive and requires the flag.

ES UPDATE LIMITATION: Elasticsearch's _update endpoint only accepts
a limited set of changeable fields (description / frequency /
settings / source.query / sync). To change pivot, source.index or
dest.index, delete the transform and re-create it — the cluster
will return a clear error if you try to change something that's not
in-place-updatable.

--dry-run reports what would change without contacting the
destructive PUT.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "name",
				Required: true,
				Usage:    "Transform id (literal, no patterns)",
			},
			&cli.StringFlag{
				Name:  "from-file",
				Usage: "Path to a file containing the JSON transform body (use - for stdin)",
			},
		},
		Action:        runApply,
		ShellComplete: completion.ForFlags(nameSuggesters),
	}
}

func runApply(ctx context.Context, cmd *cli.Command) error {
	// Destructive=false here: we apply the protection-tier check
	// ourselves *after* the existence probe, so a clean create
	// against a prod context proceeds without --i-know-this-is-prod.
	// An overwrite calls back into cmdsetup.CheckProtection.
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	body, err := readBody(cmd.String("from-file"))
	if err != nil {
		return err
	}

	confirmOverwrite := func() error {
		return cmdsetup.CheckProtection(s.Context, cmd, true)
	}

	result, err := esopstransform.Apply(ctx, s.Client.Transform, s.Client.TransformManage, esopstransform.ApplyOptions{
		Name:             cmd.String("name"),
		Body:             body,
		DryRun:           cmd.Bool("dry-run"),
		ConfirmOverwrite: confirmOverwrite,
		Dialect:          s.Client.Info.Dialect,
	})
	if err != nil {
		return err
	}

	s.Log("transform.apply",
		"name", result.Name,
		"created", result.Created,
		"updated", result.Updated,
		"unchanged", result.Unchanged,
		"dry_run", result.DryRun,
		"acknowledged", result.Acknowledged,
	)
	return output.TransformApply(os.Stdout, result, s.Format)
}
