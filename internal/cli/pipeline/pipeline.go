// Package pipeline holds the CLI wiring for the `esops pipeline ...`
// command tree. Command actions stay thin: parse flags, load config,
// build a client, call internal/pipeline, render output.
//
// Only the read-only verbs (`list`, `get`) ship today. The destructive
// `apply` / `delete` and the no-op `simulate` verbs from the MVP
// command tree land in follow-ups so the read-only surface can be
// stabilised first — same pattern that `snapshot policy` followed.
package pipeline

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `pipeline` command group.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "pipeline",
		Usage: "Inspect and manage ingest pipelines",
		Commands: []*cli.Command{
			listCommand(),
			getCommand(),
		},
	}
}
