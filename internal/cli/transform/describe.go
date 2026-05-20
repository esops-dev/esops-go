package transform

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	"github.com/esops-dev/esops-go/internal/output"
	esopstransform "github.com/esops-dev/esops-go/internal/transform"
	"github.com/esops-dev/esops-go/pkg/client"
)

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one transform (definition + runtime stats)",
		ArgsUsage: "NAME",
		Description: `Aggregate the transform's configured definition (source/dest/pivot
/ sync) with its runtime stats (state, docs processed, search /
index time, last-checkpoint age, failure reason) into one
sectioned summary. Operators inherit transforms and find them
silently lagging or stopped — describe folds both halves of the
"what is this supposed to do, what is it actually doing" question
into one screen.

A freshly-created transform that hasn't ticked yet has no stats
to report; the section is omitted in that case rather than
surfaced as a warning.

Read-only and safe against a prod-protected context.

--watch repaints the summary on a fixed interval so an operator
can sit on the output and watch docs_processed climb during a
re-indexing pass.`,
		Flags:         watch.Flags(),
		Action:        runDescribe,
		ShellComplete: completion.ForPositional(1, completion.SuggestTransforms),
	}
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return client.UsageError("describe takes exactly one positional argument (NAME); got %d", args.Len())
	}
	name := args.First()

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsNoWide,
	})
	if err != nil {
		return err
	}

	caps := esopstransform.DescribeCapabilities{
		Transform: s.Client.Transform,
		Dialect:   string(s.Client.Info.Dialect),
	}

	return watch.Run(ctx, cmd, "esops transform describe", s.Format, func(ctx context.Context) error {
		desc, err := esopstransform.Describe(ctx, caps, esopstransform.DescribeOptions{Name: name})
		if err != nil {
			return err
		}
		var state string
		if desc.Stats != nil {
			state = desc.Stats.State
		}
		s.Log("transform.describe",
			"name", desc.Info.Name,
			"state", state,
			"warnings", len(desc.SectionWarnings),
		)
		return output.TransformDescribe(os.Stdout, desc, s.Format)
	})
}
