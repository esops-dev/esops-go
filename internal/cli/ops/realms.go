package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/cli/cmdsetup"
	esopsops "github.com/esops-dev/esops-go/internal/ops"
	"github.com/esops-dev/esops-go/internal/output"
)

func realmsCommand() *cli.Command {
	return &cli.Command{
		Name:  "realms",
		Usage: "List configured authentication realms",
		Description: `Read-only listing of the cluster's authentication realm chain — the
realms ES/OS consults to identify a request, in evaluation order
(lower order = tried first).

Sources differ per dialect:

- Elasticsearch reads /_xpack/usage?filter_path=security.realms (the
  authoritative list of loaded realms, including yaml-only ones)
  merged with /_cluster/settings for any per-realm runtime overrides.
- OpenSearch reads /_plugins/_security/api/securityconfig (the
  security plugin's authc/authz domains).

The OpenSearch endpoint is admin-cert-gated on most production
deployments — a basic-auth or non-admin caller hits 403 and the
command exits with code 5 (authorization failed) rather than
reporting "no realms configured". A 403 is inconclusive, not
absence: connect with admin TLS certs to run the listing.

Default columns: order, type, name, enabled, deprecated. The
deprecated column tracks the cluster's reported version — no
realm types are flagged today (the built-in chain is supported
across all current ES/OS majors); future deprecations land
without a CLI bump.`,
		Action: runRealms,
	}
}

func runRealms(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	realms, err := esopsops.ListRealms(ctx, s.Client.Realms)
	if err != nil {
		return err
	}

	deprecated := 0
	disabled := 0
	for _, r := range realms {
		if r.Deprecated {
			deprecated++
		}
		if !r.Enabled {
			disabled++
		}
	}
	s.Log("ops.realms",
		"realms", len(realms),
		"deprecated", deprecated,
		"disabled", disabled,
	)
	return output.Realms(os.Stdout, realms, s.Format)
}
