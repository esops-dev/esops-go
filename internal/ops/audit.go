package ops

import (
	"context"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/pkg/types"
)

// Audit builds the full `ops audit` view: it asks the cluster for its
// identity model (users, roles, role-mappings, plus API keys on ES /
// tenants on OS) and layers the local TLS posture computed from the
// context config. The TLS block can't come from the cluster — whether
// we asked it over HTTPS, whether we set `insecure: true`, whether
// we're presenting a client cert are all facts about our side of the
// connection.
//
// Every collection is sorted: reserved (built-in) entries last, then
// by name. This keeps user-defined entities at the top of the table,
// which is what an operator auditing "what did somebody add here?"
// wants to see first. API keys sort by name then ID since multiple
// keys can share a name; tenants sort by name with reserved last.
func Audit(ctx context.Context, a client.SecurityAuditor, cc config.Context) (types.SecurityAudit, error) {
	report, err := a.Audit(ctx)
	if err != nil {
		return types.SecurityAudit{}, err
	}
	sortSecurityUsers(report.Users)
	sortSecurityRoles(report.Roles)
	sortSecurityRoleMappings(report.RoleMappings)
	sortSecurityAPIKeys(report.APIKeys)
	sortSecurityTenants(report.Tenants)
	return types.SecurityAudit{
		TLS:          tlsFromContext(cc),
		Status:       report.Status,
		Anonymous:    report.Anonymous,
		Users:        report.Users,
		Roles:        report.Roles,
		RoleMappings: report.RoleMappings,
		APIKeys:      report.APIKeys,
		Tenants:      report.Tenants,
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

func sortSecurityRoleMappings(mappings []types.SecurityRoleMapping) {
	sort.SliceStable(mappings, func(i, j int) bool {
		if mappings[i].Reserved != mappings[j].Reserved {
			return !mappings[i].Reserved
		}
		return mappings[i].Name < mappings[j].Name
	})
}

// sortSecurityAPIKeys orders by Name then ID. Multiple keys can share
// a name (rotation pattern: "deploy-prod" issued, then re-issued before
// the old one expires) — the ID disambiguates so the same Name doesn't
// reorder unstably between runs.
func sortSecurityAPIKeys(keys []types.SecurityAPIKey) {
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].Name != keys[j].Name {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].ID < keys[j].ID
	})
}

func sortSecurityTenants(tenants []types.SecurityTenant) {
	sort.SliceStable(tenants, func(i, j int) bool {
		if tenants[i].Reserved != tenants[j].Reserved {
			return !tenants[i].Reserved
		}
		return tenants[i].Name < tenants[j].Name
	})
}
