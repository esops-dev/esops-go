package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	esopsconfig "github.com/esops-dev/esops-go/pkg/config"
	"github.com/esops-dev/esops-go/internal/output"
)

func sampleConfig() esopsconfig.Config {
	return esopsconfig.Config{
		CurrentContext: "local-es",
		Defaults: esopsconfig.Defaults{
			Output:   "table",
			LogLevel: "info",
			Timeout:  30 * time.Second,
		},
		Contexts: map[string]esopsconfig.Context{
			"local-es": {
				URL:        "http://localhost:9200",
				Protection: "none",
			},
			"local-os": {
				URL: "http://localhost:9202",
				Auth: esopsconfig.Auth{
					Type:     "basic",
					Username: "admin",
					Password: "s3cret-plaintext",
				},
				Protection: "none",
			},
			"staging-eu": {
				URL: "https://es-staging.example.com:9200",
				Auth: esopsconfig.Auth{
					Type:   "api_key",
					APIKey: "${env:ESOPS_STAGING_API_KEY}",
				},
				TLS: esopsconfig.TLS{
					CACert: "/etc/ssl/certs/company-ca.pem",
				},
				Protection: "staging",
			},
		},
	}
}

// mustBuild is a test helper that fails the test rather than threading
// an error through every assertion. Filtering error paths have their
// own dedicated tests below.
func mustBuild(t *testing.T, cfg esopsconfig.Config, opts viewOptions) viewDoc {
	t.Helper()
	doc, err := buildViewDoc(cfg, opts)
	if err != nil {
		t.Fatalf("buildViewDoc: %v", err)
	}
	return doc
}

func TestBuildViewDoc_redactsSecretsByDefault(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true})
	auth := doc.Contexts["local-os"].Auth
	if auth == nil {
		t.Fatal("expected auth block on local-os")
	}
	if auth.Password != redactionPlaceholder {
		t.Errorf("password = %q, want %q (redaction default)", auth.Password, redactionPlaceholder)
	}
	if auth.Username != "admin" {
		t.Errorf("username should NOT be redacted: %q", auth.Username)
	}
}

func TestBuildViewDoc_showSecretsPreservesValues(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true, showSecrets: true})
	auth := doc.Contexts["local-os"].Auth
	if auth == nil {
		t.Fatal("expected auth block on local-os")
	}
	if auth.Password != "s3cret-plaintext" {
		t.Errorf("password = %q, want the real plaintext value (show-secrets on)", auth.Password)
	}
}

func TestBuildViewDoc_preservesIndirectionRefs(t *testing.T) {
	// Indirection refs are pointers, not secrets — leave them visible
	// in both modes so operators can audit config shape without the
	// secret value leaking.
	for _, showSecrets := range []bool{false, true} {
		doc := mustBuild(t, sampleConfig(), viewOptions{all: true, showSecrets: showSecrets})
		auth := doc.Contexts["staging-eu"].Auth
		if auth == nil {
			t.Fatalf("showSecrets=%v: expected auth on staging-eu", showSecrets)
		}
		want := "${env:ESOPS_STAGING_API_KEY}"
		if auth.APIKey != want {
			t.Errorf("showSecrets=%v: api_key = %q, want %q (indirection ref preserved)",
				showSecrets, auth.APIKey, want)
		}
	}
}

func TestBuildViewDoc_omitsEmptyAuthAndTLS(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true})
	if doc.Contexts["local-es"].Auth != nil {
		t.Errorf("local-es has no auth in source, projection should be nil")
	}
	if doc.Contexts["local-es"].TLS != nil {
		t.Errorf("local-es has no tls in source, projection should be nil")
	}
	if doc.Contexts["staging-eu"].TLS == nil {
		t.Errorf("staging-eu has tls in source, projection should be populated")
	}
}

func TestBuildViewDoc_emptyConfig(t *testing.T) {
	doc := mustBuild(t, esopsconfig.Config{}, viewOptions{})
	if doc.CurrentContext != "" {
		t.Errorf("empty config should have empty current-context, got %q", doc.CurrentContext)
	}
	if doc.Defaults != nil {
		t.Errorf("empty config should have nil defaults, got %+v", doc.Defaults)
	}
	if len(doc.Contexts) != 0 {
		t.Errorf("empty config should have no contexts, got %d", len(doc.Contexts))
	}
}

func TestBuildViewDoc_durationRoundTripsAsString(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true})
	if doc.Defaults == nil || doc.Defaults.Timeout != "30s" {
		t.Errorf("defaults.timeout should render as \"30s\", got %+v", doc.Defaults)
	}
}

func TestBuildViewDoc_defaultFiltersToCurrentContext(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{})
	if _, ok := doc.Contexts["local-es"]; !ok {
		t.Errorf("current context local-es missing: %+v", doc.Contexts)
	}
	for _, unexpected := range []string{"local-os", "staging-eu"} {
		if _, ok := doc.Contexts[unexpected]; ok {
			t.Errorf("non-current context %q should be filtered out: %+v", unexpected, doc.Contexts)
		}
	}
	if len(doc.Contexts) != 1 {
		t.Errorf("expected exactly 1 context in default view, got %d", len(doc.Contexts))
	}
	if doc.CurrentContext != "local-es" {
		t.Errorf("current-context field should still be populated, got %q", doc.CurrentContext)
	}
}

func TestBuildViewDoc_allIncludesEveryContext(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true})
	for _, name := range []string{"local-es", "local-os", "staging-eu"} {
		if _, ok := doc.Contexts[name]; !ok {
			t.Errorf("--all should include %q: %+v", name, doc.Contexts)
		}
	}
}

func TestBuildViewDoc_selectedOverridesCurrent(t *testing.T) {
	doc := mustBuild(t, sampleConfig(), viewOptions{selected: "local-os"})
	if _, ok := doc.Contexts["local-os"]; !ok {
		t.Errorf("selected context missing: %+v", doc.Contexts)
	}
	if _, ok := doc.Contexts["local-es"]; ok {
		t.Errorf("current context should be filtered when --context overrides: %+v", doc.Contexts)
	}
}

func TestBuildViewDoc_selectedMissingErrors(t *testing.T) {
	_, err := buildViewDoc(sampleConfig(), viewOptions{selected: "ghost"})
	if err == nil {
		t.Fatal("expected error for unknown selected context")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the missing name: %v", err)
	}
}

func TestBuildViewDoc_brokenCurrentContextErrors(t *testing.T) {
	// current-context points at a context the file doesn't define;
	// default view should surface the broken pointer.
	cfg := esopsconfig.Config{
		CurrentContext: "ghost",
		Contexts:       map[string]esopsconfig.Context{"real": {URL: "http://x"}},
	}
	if _, err := buildViewDoc(cfg, viewOptions{}); err == nil {
		t.Error("expected error when current-context points at an undefined context")
	}
	// --all should show everything regardless of the dangling pointer.
	doc, err := buildViewDoc(cfg, viewOptions{all: true})
	if err != nil {
		t.Fatalf("--all should tolerate a broken current-context pointer: %v", err)
	}
	if _, ok := doc.Contexts["real"]; !ok {
		t.Errorf("--all should still emit real contexts: %+v", doc.Contexts)
	}
}

func TestBuildViewDoc_noSelectionNoCurrentRendersDefaultsOnly(t *testing.T) {
	cfg := sampleConfig()
	cfg.CurrentContext = ""
	doc := mustBuild(t, cfg, viewOptions{})
	if len(doc.Contexts) != 0 {
		t.Errorf("no selection and no current: contexts should be empty, got %+v", doc.Contexts)
	}
	if doc.Defaults == nil {
		t.Errorf("defaults should still render, got %+v", doc)
	}
}

func TestMaybeRedact(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		showSecrets bool
		want        string
	}{
		{"empty stays empty", "", false, ""},
		{"plaintext redacted", "hunter2", false, redactionPlaceholder},
		{"plaintext passes when show-secrets", "hunter2", true, "hunter2"},
		{"env ref preserved", "${env:FOO}", false, "${env:FOO}"},
		{"file ref preserved", "${file:/run/secret}", false, "${file:/run/secret}"},
		{"keyring ref preserved", "${keyring:svc/acct}", false, "${keyring:svc/acct}"},
		{"malformed ref still redacted", "${env:}incomplete", false, redactionPlaceholder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maybeRedact(tc.in, tc.showSecrets); got != tc.want {
				t.Errorf("maybeRedact(%q, %v) = %q, want %q", tc.in, tc.showSecrets, got, tc.want)
			}
		})
	}
}

func TestParseViewFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    output.Format
		wantErr bool
	}{
		{"", output.FormatYAML, false},
		{"yaml", output.FormatYAML, false},
		{"YAML", output.FormatYAML, false},
		{"yml", output.FormatYAML, false},
		{"json", output.FormatJSON, false},
		{"table", "", true},
		{"wide", "", true},
		{"ndjson", "", true},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseViewFormat(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseViewFormat(%q): want error, got format=%q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseViewFormat(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseViewFormat(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderView_yamlRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := renderView(&buf, mustBuild(t, sampleConfig(), viewOptions{all: true}), output.FormatYAML); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"current-context: local-es",
		"defaults:",
		"timeout: 30s",
		"contexts:",
		"local-os:",
		"password: " + redactionPlaceholder,
		"api_key: ${env:ESOPS_STAGING_API_KEY}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("yaml output missing %q:\n%s", want, out)
		}
	}
	// The real plaintext must not leak.
	if strings.Contains(out, "s3cret-plaintext") {
		t.Errorf("yaml output leaked plaintext secret:\n%s", out)
	}
}

func TestRenderView_jsonShape(t *testing.T) {
	var buf bytes.Buffer
	doc := mustBuild(t, sampleConfig(), viewOptions{all: true, showSecrets: true})
	if err := renderView(&buf, doc, output.FormatJSON); err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded["current-context"] != "local-es" {
		t.Errorf("current-context = %v, want local-es", decoded["current-context"])
	}
	contexts, ok := decoded["contexts"].(map[string]any)
	if !ok {
		t.Fatalf("contexts missing or wrong type: %T", decoded["contexts"])
	}
	localOS, ok := contexts["local-os"].(map[string]any)
	if !ok {
		t.Fatalf("local-os context missing: %+v", contexts)
	}
	auth, ok := localOS["auth"].(map[string]any)
	if !ok {
		t.Fatalf("local-os.auth missing: %+v", localOS)
	}
	// With --show-secrets the plaintext should round-trip verbatim.
	if auth["password"] != "s3cret-plaintext" {
		t.Errorf("local-os.auth.password = %v, want real plaintext (show-secrets=true)", auth["password"])
	}
}

func TestRenderView_unsupportedFormat(t *testing.T) {
	var buf bytes.Buffer
	err := renderView(&buf, mustBuild(t, sampleConfig(), viewOptions{all: true}), output.Format("weirdness"))
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output on unknown format, got:\n%s", buf.String())
	}
}
