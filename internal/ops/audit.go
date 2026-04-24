package ops

import (
	"context"
	"sort"
	"strings"

	"github.com/dj-wasabi/esops-go/internal/client"
	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// Audit builds the full `ops audit` view: it asks the cluster for its
// identity model (users/roles, dialect-aware) and layers the local TLS
// posture computed from the context config. The TLS block can't come
// from the cluster — whether we asked it over HTTPS, whether we set
// `insecure: true`, whether we're presenting a client cert are all
// facts about our side of the connection.
//
// Users and roles are sorted: reserved (built-in) last, then by name.
// This keeps user-defined entities at the top of the table, which is
// what an operator auditing "what did somebody add here?" wants to see
// first.
func Audit(ctx context.Context, a client.SecurityAuditor, cc config.Context) (types.SecurityAudit, error) {
	report, err := a.Audit(ctx)
	if err != nil {
		return types.SecurityAudit{}, err
	}
	sortSecurityUsers(report.Users)
	sortSecurityRoles(report.Roles)
	return types.SecurityAudit{
		TLS:    tlsFromContext(cc),
		Status: report.Status,
		Users:  report.Users,
		Roles:  report.Roles,
	}, nil
}

// tlsFromContext derives the transport posture from the context config.
// Scheme comes from the first configured URL — the rest from explicit
// config knobs. AuthType defaults to "none" when unset; InsecureSkipVerify
// and CACertSet surface the two TLS trust overrides separately so "I
// added a CA and did NOT go insecure" reads obviously from the output.
func tlsFromContext(cc config.Context) types.TLSPosture {
	scheme := "http"
	addrs := cc.Addresses()
	if len(addrs) > 0 {
		if strings.HasPrefix(strings.ToLower(addrs[0]), "https://") {
			scheme = "https"
		}
	}
	authType := strings.ToLower(strings.TrimSpace(cc.Auth.Type))
	if authType == "" {
		authType = "none"
	}
	return types.TLSPosture{
		Scheme:             scheme,
		InsecureSkipVerify: cc.TLS.Insecure,
		CACertSet:          cc.TLS.CACert != "",
		AuthType:           authType,
		ClientCertSet:      cc.Auth.ClientCert != "",
	}
}

// sortSecurityUsers orders user-defined entries first, then reserved
// (built-in) ones, and alphabetically within each group. Operators
// scanning for "did somebody add a user here?" read the top of the
// table; the long tail of defaults sits below.
func sortSecurityUsers(users []types.SecurityUser) {
	sort.SliceStable(users, func(i, j int) bool {
		if users[i].Reserved != users[j].Reserved {
			return !users[i].Reserved
		}
		return users[i].Username < users[j].Username
	})
}

func sortSecurityRoles(roles []types.SecurityRole) {
	sort.SliceStable(roles, func(i, j int) bool {
		if roles[i].Reserved != roles[j].Reserved {
			return !roles[i].Reserved
		}
		return roles[i].Name < roles[j].Name
	})
}
