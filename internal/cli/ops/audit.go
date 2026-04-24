package ops

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/dj-wasabi/esops-go/internal/cli/cmdsetup"
	"github.com/dj-wasabi/esops-go/internal/logging"
	esopsops "github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/output"
)

func auditCommand() *cli.Command {
	return &cli.Command{
		Name:  "audit",
		Usage: "Summarise the cluster's security posture — TLS, users, roles",
		Description: `Collects the parts of the cluster's identity model that esops can
reach over HTTPS: native users and roles on Elasticsearch, internal
users and roles on OpenSearch (via the security plugin). Adds the
locally-observable TLS posture (scheme, insecure flag, CA override,
auth type, client cert) so the output is a single audit snapshot
rather than "ask the cluster, then check the config by hand".

When security is disabled on the cluster (ES with xpack.security.enabled=false,
or an OS without the security plugin), the command still succeeds —
it reports "security disabled" in the status block so the absence is
explicit, not silent.`,
		Action: runAudit,
	}
}

func runAudit(ctx context.Context, cmd *cli.Command) error {
	s, err := cmdsetup.Prepare(ctx, cmd, cmdsetup.Spec{
		SupportedFormats: cmdsetup.FormatsStandard,
	})
	if err != nil {
		return err
	}

	audit, err := esopsops.Audit(ctx, s.Client.Security, s.Context)
	if err != nil {
		return err
	}

	logging.Logger().Info("ops.audit",
		"context", s.ContextName,
		"dialect", string(s.Client.Info.Dialect),
		"version", s.Client.Info.Version,
		"security_enabled", audit.Status.Enabled,
		"users", len(audit.Users),
		"roles", len(audit.Roles),
	)
	return output.Audit(os.Stdout, audit, s.Format)
}
