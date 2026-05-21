package datastream

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	"github.com/esops-dev/esops-go/internal/cli/completion"
	"github.com/esops-dev/esops-go/internal/cli/watch"
	esopsdatastream "github.com/esops-dev/esops-go/internal/datastream"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

func describeCommand() *cli.Command {
	return &cli.Command{
		Name:      "describe",
		Usage:     "Show a detailed summary of one data stream",
		ArgsUsage: "NAME",
		Description: `Aggregate read-only views of a single data stream — metadata
(generation, write index, backing-index range), lifecycle (ILM /
ISM / data-stream lifecycle), the matched composable template,
backing indices with health/size, any in-flight recovery on those
backing indices, and tasks touching the stream — into one
sectioned summary.

Read-only and safe against a prod-protected context.

NAME is required and must be a literal stream name; patterns are
rejected (use 'datastream list --name' for fan-out).

On streams with very high backing-index counts the recovery probe
is capped at the most recent 200 generations to keep the cluster
URL inside proxy limits; older generations skipped this way are
called out in the section warnings. For a full sweep across every
generation, use 'esops index recovery --index <stream>-*' directly.

--watch repaints the summary on a fixed interval so an operator
can sit on the output during a rollover or migration.`,
		Flags: append([]cli.Flag{
			&cli.BoolFlag{
				Name:  "no-indices",
				Usage: "Hide the per-backing-index listing (the rest of the sections still render)",
			},
		}, watch.Flags()...),
		Action:        runDescribe,
		ShellComplete: completion.ForPositional(1, completion.SuggestDataStreams),
	}
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return client.UsageError("describe takes exactly one positional argument (NAME); got %d", args.Len())
	}
	name := args.First()

	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	caps := esopsdatastream.DescribeCapabilities{
		DataStream: s.Client.DataStream,
		Indices:    s.Client.Indices,
		Templates:  s.Client.IndexTemplateGet,
		Recovery:   s.Client.Recovery,
		Tasks:      s.Client.Tasks,
		Dialect:    string(s.Client.Info.Dialect),
	}

	var showIndices *bool
	if cmd.Bool("no-indices") {
		v := false
		showIndices = &v
	}

	return watch.Run(ctx, cmd, watch.Options{Label: "esops datastream describe", ContextName: s.ContextName, Format: s.Format}, func(ctx context.Context) error {
		desc, err := esopsdatastream.Describe(ctx, caps, esopsdatastream.DescribeOptions{Name: name})
		if err != nil {
			return err
		}
		s.Log("datastream.describe",
			"name", desc.Stream.Name,
			"generation", desc.Stream.Generation,
			"backing_indices", desc.Stream.IndexCount,
			"warnings", len(desc.SectionWarnings),
		)
		return output.DataStreamDescribe(os.Stdout, desc, output.DataStreamDescribeOptions{
			ShowIndices: showIndices,
		}, s.Format)
	})
}
