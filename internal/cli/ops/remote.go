package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func remoteCommand() *cli.Command {
	return &cli.Command{
		Name:  "remote",
		Usage: "Show configured remote clusters and CCR follower status",
		Description: `Read-only listing of cross-cluster remotes — the CCS and (where
licensed) CCR surface the cluster reports.

Sources:

- GET /_remote/info — every dialect. The CCS half: configured remotes,
  their connection mode (sniff/proxy), connectivity, and seed hosts.
- GET /_ccr/stats — Elasticsearch only, requires platinum/enterprise
  licence. The CCR half: per-follower-index lag, pause state, last
  read failure.

The CCR half is best-effort. On OpenSearch it short-circuits with a
"unsupported" status header. On a basic-licence Elasticsearch cluster
the endpoint 404s and the header reads "unsupported". When the auth
user lacks the cluster privilege required to read /_ccr/stats the
header reads "forbidden" — the CCS half still renders. Any other
cluster error fails the whole command.

Use -o wide to surface seed hosts (CCS) and last-read-failure messages
(CCR).`,
		Action: runRemote,
	}
}

func runRemote(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	summary, err := esopsops.ListRemotes(ctx, s.Client.RemoteClusters)
	if err != nil {
		return err
	}

	connected := 0
	for _, r := range summary.Remotes {
		if r.Connected {
			connected++
		}
	}
	pausedFollowers := 0
	for _, fl := range summary.Followers {
		if fl.Paused {
			pausedFollowers++
		}
	}
	inactiveAutoFollows := 0
	for _, p := range summary.AutoFollows {
		if !p.Active {
			inactiveAutoFollows++
		}
	}
	s.Log("ops.remote",
		"remotes", len(summary.Remotes),
		"connected", connected,
		"followers", len(summary.Followers),
		"paused_followers", pausedFollowers,
		"auto_follows", len(summary.AutoFollows),
		"inactive_auto_follows", inactiveAutoFollows,
		"ccr_status", summary.CCRStatus,
	)
	return output.RemoteSummary(os.Stdout, summary.Remotes, summary.Followers, summary.AutoFollows, summary.CCRStatus, summary.AutoFollowError, s.Format)
}
