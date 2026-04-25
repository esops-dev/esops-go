package ops_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/config"
	"github.com/dj-wasabi/esops-go/internal/ops"
	"github.com/dj-wasabi/esops-go/internal/types"
)

// fakeSecurityAuditor returns a canned SecurityReport, same pattern as
// the other fake capabilities. No context dependency — the business
// layer doesn't touch the ctx it receives.
type fakeSecurityAuditor struct {
	report types.SecurityReport
	err    error
}

func (f *fakeSecurityAuditor) Audit(_ context.Context) (types.SecurityReport, error) {
	return f.report, f.err
}

func sampleReport() types.SecurityReport {
	return types.SecurityReport{
		Status: types.SecurityStatus{
			Dialect: "elasticsearch",
			Enabled: true,
		},
		Users: []types.SecurityUser{
			// Deliberately jumbled order so sortSecurityUsers has something
			// to do: reserved true/false mixed, usernames not in order.
			{Username: "elastic", Enabled: true, Reserved: true, Roles: []string{"superuser"}},
			{Username: "app-writer", Enabled: true, Reserved: false, Roles: []string{"writer"}},
			{Username: "kibana_system", Enabled: true, Reserved: true, Roles: []string{"kibana_system"}},
			{Username: "alice", Enabled: false, Reserved: false, Roles: []string{"reader"}},
		},
		Roles: []types.SecurityRole{
			{Name: "superuser", Reserved: true, ClusterPrivileges: []string{"all"}, IndexPatterns: []string{"*"}},
			{Name: "writer", Reserved: false, ClusterPrivileges: []string{"monitor"}, IndexPatterns: []string{"logs-*"}},
			{Name: "reader", Reserved: false, ClusterPrivileges: []string{"monitor"}, IndexPatterns: []string{"*"}},
		},
		RoleMappings: []types.SecurityRoleMapping{
			{Name: "kibana_user", Reserved: true, Enabled: true, Roles: []string{"kibana_user"}},
			{Name: "deploy", Reserved: false, Enabled: true, Roles: []string{"writer"}, BackendRoles: []string{"cn=ops,ou=groups,dc=example,dc=com"}},
		},
		APIKeys: []types.SecurityAPIKey{
			{ID: "k2", Name: "deploy-prod", Username: "alice", Realm: "native", Creation: "2026-04-01T10:00:00Z"},
			{ID: "k1", Name: "deploy-prod", Username: "alice", Realm: "native", Invalidated: true, Creation: "2026-03-01T10:00:00Z"},
			{ID: "k3", Name: "ci-runner", Username: "ci", Realm: "native"},
		},
	}
}

func sampleContext() config.Context {
	return config.Context{
		URL: "https://es.example.com:9200",
		Auth: config.Auth{
			Type:     "basic",
			Username: "elastic",
		},
		TLS: config.TLS{
			CACert: "/etc/ssl/ca.crt",
		},
	}
}

func TestAudit_passesReportThrough(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got.Status.Dialect != "elasticsearch" || !got.Status.Enabled {
		t.Errorf("status = %+v, want dialect=elasticsearch enabled=true", got.Status)
	}
	if len(got.Users) != 4 || len(got.Roles) != 3 {
		t.Errorf("got %d users / %d roles, want 4 / 3", len(got.Users), len(got.Roles))
	}
}

func TestAudit_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeSecurityAuditor{err: sentinel}
	_, err := ops.Audit(context.Background(), fake, sampleContext())
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestAudit_fillsTLSFromContext(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got.TLS.Scheme != "https" {
		t.Errorf("scheme = %q, want https", got.TLS.Scheme)
	}
	if got.TLS.InsecureSkipVerify {
		t.Errorf("insecure = true; sample config did not set it")
	}
	if !got.TLS.CACertSet {
		t.Errorf("cacert_set = false; sample config sets CACert")
	}
	if got.TLS.AuthType != "basic" {
		t.Errorf("auth_type = %q, want basic", got.TLS.AuthType)
	}
	if got.TLS.ClientCertSet {
		t.Errorf("client_cert_set = true; sample config did not set it")
	}
}

func TestAudit_httpURLYieldsHTTPScheme(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	cc := sampleContext()
	cc.URL = "http://localhost:9200"
	got, err := ops.Audit(context.Background(), fake, cc)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got.TLS.Scheme != "http" {
		t.Errorf("scheme = %q, want http", got.TLS.Scheme)
	}
}

func TestAudit_authTypeDefaultsToNone(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	cc := sampleContext()
	cc.Auth.Type = ""
	got, err := ops.Audit(context.Background(), fake, cc)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got.TLS.AuthType != "none" {
		t.Errorf("auth_type = %q, want 'none' when unset", got.TLS.AuthType)
	}
}

func TestAudit_mtlsSurfacesClientCert(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	cc := sampleContext()
	cc.Auth = config.Auth{
		Type:       "mtls",
		ClientCert: "/certs/client.crt",
		ClientKey:  "/certs/client.key",
	}
	got, err := ops.Audit(context.Background(), fake, cc)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if !got.TLS.ClientCertSet {
		t.Errorf("client_cert_set = false; mtls config has a client_cert")
	}
	if got.TLS.AuthType != "mtls" {
		t.Errorf("auth_type = %q, want mtls", got.TLS.AuthType)
	}
}

func TestAudit_sortsUsersUserDefinedFirst(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Expected order: user-defined (alice, app-writer) alphabetical first,
	// then reserved (elastic, kibana_system) alphabetical.
	wantOrder := []string{"alice", "app-writer", "elastic", "kibana_system"}
	for i, name := range wantOrder {
		if got.Users[i].Username != name {
			t.Errorf("users[%d] = %q, want %q (full order: %v)",
				i, got.Users[i].Username, name, userNames(got.Users))
		}
	}
}

func TestAudit_sortsRolesUserDefinedFirst(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	wantOrder := []string{"reader", "writer", "superuser"}
	for i, name := range wantOrder {
		if got.Roles[i].Name != name {
			t.Errorf("roles[%d] = %q, want %q (full order: %v)",
				i, got.Roles[i].Name, name, roleNames(got.Roles))
		}
	}
}

func TestAudit_passesRoleMappingsAndAPIKeys(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(got.RoleMappings) != 2 {
		t.Errorf("got %d role mappings, want 2", len(got.RoleMappings))
	}
	if len(got.APIKeys) != 3 {
		t.Errorf("got %d api keys, want 3", len(got.APIKeys))
	}
}

func TestAudit_sortsRoleMappingsUserDefinedFirst(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Expected: deploy (user-defined) before kibana_user (reserved).
	if got.RoleMappings[0].Name != "deploy" {
		t.Errorf("role_mappings[0] = %q, want deploy (user-defined sorts first)", got.RoleMappings[0].Name)
	}
}

func TestAudit_sortsAPIKeysByNameThenID(t *testing.T) {
	fake := &fakeSecurityAuditor{report: sampleReport()}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	// Expected order: ci-runner then deploy-prod (k1 before k2 for the
	// shared "deploy-prod" name — older invalidated key first).
	wantOrder := []string{"ci-runner/k3", "deploy-prod/k1", "deploy-prod/k2"}
	for i, want := range wantOrder {
		got := got.APIKeys[i].Name + "/" + got.APIKeys[i].ID
		if got != want {
			t.Errorf("api_keys[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestAudit_passesTenantsThrough(t *testing.T) {
	report := sampleReport()
	report.Tenants = []types.SecurityTenant{
		{Name: "global_tenant", Reserved: true},
		{Name: "team-platform", Reserved: false, Description: "Platform team space"},
	}
	fake := &fakeSecurityAuditor{report: report}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(got.Tenants) != 2 {
		t.Fatalf("got %d tenants, want 2", len(got.Tenants))
	}
	// Reserved last after sorting.
	if got.Tenants[0].Name != "team-platform" {
		t.Errorf("tenants[0] = %q, want team-platform (user-defined sorts first)", got.Tenants[0].Name)
	}
}

func TestAudit_propagatesPartialFailureNotes(t *testing.T) {
	// A 403 on api_keys lands in Status.Notes — it does not abort
	// the audit. Operator-facing signal: "we tried, we couldn't".
	report := sampleReport()
	report.Status.Notes = []string{"api_keys: forbidden — needs manage_api_key cluster privilege"}
	fake := &fakeSecurityAuditor{report: report}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(got.Status.Notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(got.Status.Notes))
	}
	if !strings.Contains(got.Status.Notes[0], "api_keys") {
		t.Errorf("note = %q, want it to mention api_keys", got.Status.Notes[0])
	}
}

func TestAudit_securityDisabledKeepsStatus(t *testing.T) {
	fake := &fakeSecurityAuditor{
		report: types.SecurityReport{
			Status: types.SecurityStatus{
				Dialect: "elasticsearch",
				Enabled: false,
				Note:    "security is disabled on this cluster",
			},
		},
	}
	got, err := ops.Audit(context.Background(), fake, sampleContext())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if got.Status.Enabled {
		t.Errorf("enabled = true, want false")
	}
	if got.Status.Note == "" {
		t.Errorf("note = empty, want explanation")
	}
	if len(got.Users) != 0 || len(got.Roles) != 0 {
		t.Errorf("got users=%d roles=%d, want both empty when security disabled",
			len(got.Users), len(got.Roles))
	}
}

func userNames(us []types.SecurityUser) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Username
	}
	return out
}

func roleNames(rs []types.SecurityRole) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}
