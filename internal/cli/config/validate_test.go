package config

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/output"
)

// writeConfig drops body at a temp path with the requested mode and
// returns the absolute path. Mirrors safety_test.go's helper so the two
// suites read consistently.
func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// os.WriteFile respects umask — force the requested mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

// clearValidateEnv wipes the loader's escape hatches so prior tests'
// settings don't bleed in. Mirrors safety_test.go.
func clearValidateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "")
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "")
}

// findByPath returns the first finding whose Path matches, or nil if
// none — keeps the assertions readable without indexing into a slice.
func findByPath(findings []finding, path string) *finding {
	for i := range findings {
		if findings[i].Path == path {
			return &findings[i]
		}
	}
	return nil
}

func TestCollectFindings_resolveFailure(t *testing.T) {
	// Point Resolve at a path that doesn't exist — the operator named
	// a bogus file explicitly.
	bogus := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Setenv("ESOPS_CONFIG", bogus)

	path, findings := collectFindings("")
	// Resolve treats an explicitly-supplied path as authoritative even
	// when missing, so the actual error fires later — at Parse time.
	if len(findings) == 0 {
		t.Fatal("expected at least one finding for missing config")
	}
	if findings[0].Severity != severityError {
		t.Errorf("expected error severity, got %s", findings[0].Severity)
	}
	// Either Resolve or Parse rejected the file — both are acceptable
	// signals of "your config isn't here". What matters is exactly one
	// finding came back so the rest of the lint isn't run.
	if len(findings) != 1 {
		t.Errorf("expected exactly 1 finding when load fails, got %d: %+v", len(findings), findings)
	}
	_ = path
}

func TestCollectFindings_yamlParseError(t *testing.T) {
	// Malformed YAML must short-circuit with a single error — the rest
	// of the lint has nothing to inspect.
	path := writeConfig(t, "contexts: [unterminated\n", 0o600)
	gotPath, findings := collectFindings(path)
	if gotPath != path {
		t.Errorf("path = %q, want %q", gotPath, path)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Severity != severityError {
		t.Errorf("severity = %s, want error", findings[0].Severity)
	}
}

func TestCollectFindings_validConfig(t *testing.T) {
	clearValidateEnv(t)
	t.Setenv("ESOPS_TEST_VALIDATE_PW", "ok")

	path := writeConfig(t, `
current-context: dev
contexts:
  dev:
    url: http://localhost:9200
    protection: none
  api:
    url: https://api.example.com:9200
    auth:
      type: basic
      username: u
      password: ${env:ESOPS_TEST_VALIDATE_PW}
    protection: staging
`, 0o600)
	_, findings := collectFindings(path)
	if len(findings) != 0 {
		t.Errorf("expected zero findings for a clean config, got %d: %+v", len(findings), findings)
	}
}

func TestCollectFindings_currentContextMissing(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
current-context: ghost
contexts:
  real:
    url: http://x:9200
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "current-context")
	if f == nil {
		t.Fatalf("expected finding on current-context, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
	if !strings.Contains(f.Message, "ghost") {
		t.Errorf("message should name the missing context: %q", f.Message)
	}
}

func TestCollectFindings_missingURL(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  empty: {}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.empty.url")
	if f == nil {
		t.Fatalf("expected finding on contexts.empty.url, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		bad  bool
	}{
		{"empty", "", true},
		{"no scheme", "localhost:9200", true},
		{"unsupported scheme", "ftp://x:9200", true},
		{"missing host", "http://", true},
		{"http ok", "http://localhost:9200", false},
		{"https ok", "https://es.example.com:9200", false},
		{"https with path ok", "https://es.example.com:9200/path", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateURL(tc.in)
			if tc.bad && msg == "" {
				t.Errorf("validateURL(%q) accepted, want rejection", tc.in)
			}
			if !tc.bad && msg != "" {
				t.Errorf("validateURL(%q) rejected: %s", tc.in, msg)
			}
		})
	}
}

func TestCollectFindings_authStructural(t *testing.T) {
	// basic with no username is the canonical "you forgot a required
	// field" case — the loader's ValidateAuth is the source of truth
	// for the message, so we just assert the path and severity.
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: basic
      password: ${env:NOPE_NOT_SET}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth")
	if f == nil {
		t.Fatalf("expected finding on contexts.bad.auth, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
	if !strings.Contains(f.Message, "username") {
		t.Errorf("message should mention the missing 'username' field: %q", f.Message)
	}
}

func TestCollectFindings_secretAvailability_envSet(t *testing.T) {
	clearValidateEnv(t)
	t.Setenv("ESOPS_TEST_VALIDATE_TOKEN", "sentinel")
	path := writeConfig(t, `
contexts:
  ok:
    url: http://x:9200
    auth:
      type: bearer
      token: ${env:ESOPS_TEST_VALIDATE_TOKEN}
`, 0o600)
	_, findings := collectFindings(path)
	if len(findings) != 0 {
		t.Errorf("expected no findings (env var is set), got %+v", findings)
	}
}

func TestCollectFindings_secretAvailability_envUnset(t *testing.T) {
	clearValidateEnv(t)
	_ = os.Unsetenv("ESOPS_TEST_VALIDATE_UNSET")
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${env:ESOPS_TEST_VALIDATE_UNSET}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
	if !strings.Contains(f.Message, "ESOPS_TEST_VALIDATE_UNSET") {
		t.Errorf("message should name the missing env var: %q", f.Message)
	}
}

func TestCollectFindings_secretAvailability_fileExists(t *testing.T) {
	clearValidateEnv(t)
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "tok")
	// The lint must not read the contents, but the file has to exist
	// and be openable.
	if err := os.WriteFile(secretPath, []byte("must-not-leak"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := writeConfig(t, `
contexts:
  ok:
    url: http://x:9200
    auth:
      type: bearer
      token: ${file:`+secretPath+`}
`, 0o600)
	_, findings := collectFindings(path)
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
	for _, f := range findings {
		if strings.Contains(f.Message, "must-not-leak") {
			t.Errorf("finding leaked the secret contents: %+v", f)
		}
	}
}

func TestCollectFindings_secretAvailability_fileMissing(t *testing.T) {
	clearValidateEnv(t)
	bogus := filepath.Join(t.TempDir(), "does-not-exist")
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${file:`+bogus+`}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestCollectFindings_secretAvailability_keyringPresent(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("esops-validate-test", "ok", "must-not-leak"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  ok:
    url: http://x:9200
    auth:
      type: bearer
      token: ${keyring:esops-validate-test/ok}
`, 0o600)
	_, findings := collectFindings(path)
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
	for _, f := range findings {
		if strings.Contains(f.Message, "must-not-leak") {
			t.Errorf("finding leaked the keyring secret: %+v", f)
		}
	}
}

func TestCollectFindings_secretAvailability_keyringMissing(t *testing.T) {
	keyring.MockInit()
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${keyring:esops-validate-test/missing}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestCollectFindings_secretAvailability_keyringBadFormat(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${keyring:no-slash}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if !strings.Contains(f.Message, "service/account") {
		t.Errorf("message should explain the expected form: %q", f.Message)
	}
}

func TestCollectFindings_unknownSecretScheme(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${bogus:xyz}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if !strings.Contains(f.Message, "unknown") {
		t.Errorf("message should mention 'unknown' scheme: %q", f.Message)
	}
}

func TestCollectFindings_protectionTierInvalid(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    protection: critical
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.protection")
	if f == nil {
		t.Fatalf("expected finding on contexts.bad.protection, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestCollectFindings_protectionTierValid(t *testing.T) {
	clearValidateEnv(t)
	// All four documented tiers — including the empty string — should
	// load without complaint.
	for _, tier := range []string{"", "none", "staging", "prod"} {
		body := `
contexts:
  ok:
    url: http://x:9200
    protection: ` + tier + `
`
		path := writeConfig(t, body, 0o600)
		_, findings := collectFindings(path)
		if findByPath(findings, "contexts.ok.protection") != nil {
			t.Errorf("tier %q should be accepted, got: %+v", tier, findings)
		}
	}
}

func TestCollectFindings_plaintextWarnsAt0600(t *testing.T) {
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  weak:
    url: http://x:9200
    auth:
      type: basic
      username: u
      password: hunter2
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.weak.auth")
	if f == nil {
		t.Fatalf("expected plaintext warning, got: %+v", findings)
	}
	if f.Severity != severityWarning {
		t.Errorf("severity = %s, want warning", f.Severity)
	}
}

func TestCollectFindings_plaintextSilencedByOverride(t *testing.T) {
	clearValidateEnv(t)
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "1")
	path := writeConfig(t, `
contexts:
  weak:
    url: http://x:9200
    auth:
      type: basic
      username: u
      password: hunter2
`, 0o600)
	_, findings := collectFindings(path)
	if f := findByPath(findings, "contexts.weak.auth"); f != nil {
		t.Errorf("override should silence plaintext warning, got: %+v", f)
	}
}

func TestCollectFindings_worldReadablePlaintextIsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	clearValidateEnv(t)
	path := writeConfig(t, `
contexts:
  weak:
    url: http://x:9200
    auth:
      type: basic
      username: u
      password: hunter2
`, 0o644)
	_, findings := collectFindings(path)
	if len(findings) == 0 {
		t.Fatal("expected world-readable error, got none")
	}
	// The world-readable finding lives at the file level (empty path).
	var got *finding
	for i := range findings {
		if findings[i].Path == "" && strings.Contains(findings[i].Message, "world-readable") {
			got = &findings[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected a world-readable finding, got: %+v", findings)
	}
	if got.Severity != severityError {
		t.Errorf("severity = %s, want error", got.Severity)
	}
	if !strings.Contains(got.Message, "weak") {
		t.Errorf("message should name the offending context: %q", got.Message)
	}
}

func TestCollectFindings_worldReadableOverrideDowngradesToWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	clearValidateEnv(t)
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "1")
	path := writeConfig(t, `
contexts:
  weak:
    url: http://x:9200
    auth:
      type: basic
      username: u
      password: hunter2
`, 0o644)
	_, findings := collectFindings(path)
	var got *finding
	for i := range findings {
		if findings[i].Path == "" && strings.Contains(findings[i].Message, "world-readable") {
			got = &findings[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected world-readable finding even with override, got: %+v", findings)
	}
	if got.Severity != severityWarning {
		t.Errorf("severity = %s, want warning (override active)", got.Severity)
	}
}

// makeCertPEM writes a self-signed cert + EC key to certPath/keyPath
// and returns both paths. The lint should parse this as valid TLS
// material via tls.LoadX509KeyPair / AppendCertsFromPEM.
func makeCertPEM(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "esops-validate"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestCollectFindings_tlsCACertValid(t *testing.T) {
	clearValidateEnv(t)
	certPath, _ := makeCertPEM(t)
	path := writeConfig(t, `
contexts:
  ok:
    url: https://x:9200
    tls:
      cacert: `+certPath+`
`, 0o600)
	_, findings := collectFindings(path)
	if f := findByPath(findings, "contexts.ok.tls.cacert"); f != nil {
		t.Errorf("valid CA should produce no finding: %+v", f)
	}
}

func TestCollectFindings_tlsCACertMissing(t *testing.T) {
	clearValidateEnv(t)
	bogus := filepath.Join(t.TempDir(), "absent.pem")
	path := writeConfig(t, `
contexts:
  bad:
    url: https://x:9200
    tls:
      cacert: `+bogus+`
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.tls.cacert")
	if f == nil {
		t.Fatalf("expected finding on cacert, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestCollectFindings_tlsCACertBogusContents(t *testing.T) {
	clearValidateEnv(t)
	bogusPEM := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(bogusPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	path := writeConfig(t, `
contexts:
  bad:
    url: https://x:9200
    tls:
      cacert: `+bogusPEM+`
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.tls.cacert")
	if f == nil {
		t.Fatalf("expected finding on cacert, got %+v", findings)
	}
	if !strings.Contains(f.Message, "no valid PEM") {
		t.Errorf("message should call out invalid PEM: %q", f.Message)
	}
}

func TestCollectFindings_mtlsKeypairValid(t *testing.T) {
	clearValidateEnv(t)
	certPath, keyPath := makeCertPEM(t)
	path := writeConfig(t, `
contexts:
  ok:
    url: https://x:9200
    auth:
      type: mtls
      client_cert: `+certPath+`
      client_key: `+keyPath+`
`, 0o600)
	_, findings := collectFindings(path)
	if f := findByPath(findings, "contexts.ok.auth.client_cert"); f != nil {
		t.Errorf("valid keypair should produce no finding: %+v", f)
	}
}

func TestCollectFindings_mtlsKeypairUnreadable(t *testing.T) {
	clearValidateEnv(t)
	missingCert := filepath.Join(t.TempDir(), "missing.crt")
	missingKey := filepath.Join(t.TempDir(), "missing.key")
	path := writeConfig(t, `
contexts:
  bad:
    url: https://x:9200
    auth:
      type: mtls
      client_cert: `+missingCert+`
      client_key: `+missingKey+`
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.client_cert")
	if f == nil {
		t.Fatalf("expected finding on client_cert, got %+v", findings)
	}
	if f.Severity != severityError {
		t.Errorf("severity = %s, want error", f.Severity)
	}
}

func TestCollectFindings_urlsListMember(t *testing.T) {
	clearValidateEnv(t)
	// Index encoded into the path field so a bad entry in a multi-URL
	// context is unambiguous in the finding.
	path := writeConfig(t, `
contexts:
  multi:
    urls:
      - http://a:9200
      - ftp://b:9200
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.multi.urls[1]")
	if f == nil {
		t.Fatalf("expected finding on urls[1], got %+v", findings)
	}
	if !strings.Contains(f.Message, "ftp") {
		t.Errorf("message should mention the offending URL: %q", f.Message)
	}
}

func TestValidateExitStatus(t *testing.T) {
	cases := []struct {
		name     string
		findings []finding
		strict   bool
		wantErr  bool
	}{
		{"no findings", nil, false, false},
		{"warning, default mode", []finding{{Severity: severityWarning}}, false, false},
		{"warning, strict", []finding{{Severity: severityWarning}}, true, true},
		{"error, default mode", []finding{{Severity: severityError}}, false, true},
		{"error, strict", []finding{{Severity: severityError}}, true, true},
		{"mix, default mode", []finding{{Severity: severityWarning}, {Severity: severityError}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExitStatus(tc.findings, tc.strict)
			if tc.wantErr && err == nil {
				t.Errorf("want error, got nil")
				return
			}
			if !tc.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
				return
			}
			if err != nil && !errors.Is(err, client.ErrUsage) {
				t.Errorf("err should wrap client.ErrUsage, got %v", err)
			}
		})
	}
}

func TestRenderFindings_jsonShape(t *testing.T) {
	findings := []finding{
		{Severity: severityError, File: "/cfg.yaml", Path: "contexts.x.url", Message: "url is empty"},
		{Severity: severityWarning, File: "/cfg.yaml", Path: "contexts.y.auth", Message: "plaintext"},
	}
	var buf bytes.Buffer
	if err := renderFindings(&buf, "/cfg.yaml", findings, output.FormatJSON); err != nil {
		t.Fatalf("render: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded length = %d, want 2", len(decoded))
	}
	if decoded[0]["severity"] != "error" {
		t.Errorf("[0].severity = %v, want error", decoded[0]["severity"])
	}
	if decoded[0]["path"] != "contexts.x.url" {
		t.Errorf("[0].path = %v", decoded[0]["path"])
	}
}

func TestRenderFindings_jsonEmptyArray(t *testing.T) {
	// CI annotation tooling reads the array shape directly — nil must
	// serialize as `[]`, not `null`.
	var buf bytes.Buffer
	if err := renderFindings(&buf, "/cfg.yaml", nil, output.FormatJSON); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "[]") {
		t.Errorf("empty findings should render as [], got: %s", buf.String())
	}
}

func TestRenderFindings_tableSuccess(t *testing.T) {
	var buf bytes.Buffer
	if err := renderFindings(&buf, "/cfg.yaml", nil, output.FormatTable); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "valid") {
		t.Errorf("clean lint should announce success, got: %q", out)
	}
	if !strings.Contains(out, "/cfg.yaml") {
		t.Errorf("success message should reference the file path, got: %q", out)
	}
}

func TestRenderFindings_tableWithFindings(t *testing.T) {
	findings := []finding{
		{Severity: severityError, File: "/cfg.yaml", Path: "contexts.x.url", Message: "url is empty"},
		{Severity: severityWarning, File: "/cfg.yaml", Path: "", Message: "world-readable"},
	}
	var buf bytes.Buffer
	if err := renderFindings(&buf, "/cfg.yaml", findings, output.FormatTable); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"SEVERITY", "FILE", "PATH", "MESSAGE", "error", "warning", "url is empty"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestParseValidateFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    output.Format
		wantErr bool
	}{
		{"", output.FormatTable, false},
		{"table", output.FormatTable, false},
		{"TABLE", output.FormatTable, false},
		{"json", output.FormatJSON, false},
		{"yaml", "", true},
		{"wide", "", true},
		{"ndjson", "", true},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseValidateFormat(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseValidateFormat(%q): want error, got format=%q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseValidateFormat(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseValidateFormat(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCollectFindings_secretAvailability_fileIsDirectory(t *testing.T) {
	// A ${file:DIR} reference would succeed with naive os.Open but the
	// runtime's os.ReadFile would fail at first command. The lint must
	// reject directories at validate time so CI catches this before
	// merge.
	clearValidateEnv(t)
	dir := t.TempDir()
	path := writeConfig(t, `
contexts:
  bad:
    url: http://x:9200
    auth:
      type: bearer
      token: ${file:`+dir+`}
`, 0o600)
	_, findings := collectFindings(path)
	f := findByPath(findings, "contexts.bad.auth.token")
	if f == nil {
		t.Fatalf("expected finding on token, got %+v", findings)
	}
	if !strings.Contains(f.Message, "regular file") {
		t.Errorf("message should call out non-regular-file, got: %q", f.Message)
	}
}

func TestCheckDefaults(t *testing.T) {
	clearValidateEnv(t)

	cases := []struct {
		name string
		body string
		want string // expected path of the finding; "" means no finding
	}{
		{
			name: "valid output",
			body: "defaults:\n  output: ndjson\ncontexts: {x: {url: http://x:9200}}\n",
			want: "",
		},
		{
			name: "bad output",
			body: "defaults:\n  output: ymal\ncontexts: {x: {url: http://x:9200}}\n",
			want: "defaults.output",
		},
		{
			name: "bad log_level",
			body: "defaults:\n  log_level: trace\ncontexts: {x: {url: http://x:9200}}\n",
			want: "defaults.log_level",
		},
		{
			name: "bad log_format",
			body: "defaults:\n  log_format: ymal\ncontexts: {x: {url: http://x:9200}}\n",
			want: "defaults.log_format",
		},
		{
			name: "empty fields pass",
			body: "defaults: {}\ncontexts: {x: {url: http://x:9200}}\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body, 0o600)
			_, findings := collectFindings(path)
			f := findByPath(findings, tc.want)
			if tc.want == "" {
				// No defaults-related finding should exist; check all
				// three known defaults paths.
				for _, p := range []string{"defaults.output", "defaults.log_level", "defaults.log_format"} {
					if findByPath(findings, p) != nil {
						t.Errorf("unexpected finding on %s: %+v", p, findings)
					}
				}
				return
			}
			if f == nil {
				t.Fatalf("expected finding on %s, got %+v", tc.want, findings)
			}
			if f.Severity != severityError {
				t.Errorf("severity = %s, want error", f.Severity)
			}
		})
	}
}

func TestCollectFindings_exampleConfigValidates(t *testing.T) {
	// The shipped example config must lint cleanly — operators copy it
	// as their starting point, and any new check that breaks it needs
	// either the check or the example updated in the same change.
	clearValidateEnv(t)
	// Run against the in-tree copy. Tests run with the package as cwd,
	// so the path is relative to internal/cli/config/.
	exPath := filepath.Join("..", "..", "..", "config", "esops.example.yaml")
	if _, err := os.Stat(exPath); err != nil {
		t.Skipf("example config not found at %s: %v", exPath, err)
	}
	// The example sets `password: ${env:ESOPS_LOCAL_OS_PASSWORD}` on
	// local-os — seed it so the availability check passes.
	t.Setenv("ESOPS_LOCAL_OS_PASSWORD", "dev")
	_, findings := collectFindings(exPath)
	for _, f := range findings {
		// The example references throw-away mTLS material at
		// scripts/certs/out/* which only exists after `make certs`.
		// Skip TLS-load errors so this regression net isn't tied to
		// a developer's local state.
		if strings.Contains(f.Path, "tls.cacert") || strings.Contains(f.Path, "client_cert") {
			continue
		}
		// Plaintext warnings on the example are also acceptable —
		// it documents both indirected and inline forms.
		if f.Severity == severityWarning {
			continue
		}
		t.Errorf("example config produced unexpected finding: %+v", f)
	}
}
