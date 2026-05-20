// Package datastream holds the CLI wiring for the
// `esops datastream ...` command tree. Command actions are thin:
// parse flags, load config, build a client, call internal/datastream,
// render output.
package datastream

import (
	"github.com/urfave/cli/v3"
)

// Command returns the `datastream` command group.
//
// Data streams sit next to indices, not inside them, so the verbs
// mirror `index` where it makes sense (list / get / stats / recovery /
// segments / optimize / rollover) and add the data-stream-specific
// modify / delete on top. `modify`, `delete`, and `optimize` are
// mutating; `delete` removes the stream **and** all of its backing
// indices in one shot — guarded by --allow-data-loss and a lower
// blast-radius cap (5 streams) than the index commands use.
//
// `optimize`, `recovery`, and `segments` resolve --name to the
// stream's backing indices and forward to the existing index-level
// capabilities — same wire shape, rendered output mirrors the index
// verbs row-for-row, so an operator can read the two side-by-side.
func Command() *cli.Command {
	return &cli.Command{
		Name:  "datastream",
		Usage: "Inspect and manage data streams (backing-index lifecycle, rollover, deletion)",
		Commands: []*cli.Command{
			listCommand(),
			getCommand(),
			describeCommand(),
			statsCommand(),
			recoveryCommand(),
			segmentsCommand(),
			optimizeCommand(),
			rolloverCommand(),
			modifyCommand(),
			deleteCommand(),
		},
	}
}
