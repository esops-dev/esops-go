package config_test

// NOTE: tests in this file mutate package-global state via
// keyring.MockInit(). They must not call t.Parallel() — concurrent
// goroutines would race on the mock backend. Add a
// `t.Setenv` / file-fixture-only test instead if parallel coverage is
// needed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/esops-dev/esops-go/pkg/config"
)

const twoContexts = `
current-context: a
defaults:
  timeout: 5s
contexts:
  a:
    url: http://a.example:9200
  b:
    url: http://b.example:9200
    auth:
      type: basic
      username: u
      password: ${env:ESOPS_TEST_PW}
    timeout: 1s
`

func loadString(t *testing.T, body string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestResolveContext_defaultsToCurrentContext(t *testing.T) {
	cfg := loadString(t, twoContexts)
	name, cc, err := cfg.ResolveContext("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if name != "a" {
		t.Errorf("name = %q, want a", name)
	}
	if cc.URL != "http://a.example:9200" {
		t.Errorf("url = %q, want a.example", cc.URL)
	}
}

func TestResolveContext_explicitNameOverridesCurrent(t *testing.T) {
	t.Setenv("ESOPS_TEST_PW", "p")
	cfg := loadString(t, twoContexts)
	name, _, err := cfg.ResolveContext("b")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if name != "b" {
		t.Errorf("name = %q, want b", name)
	}
}

func TestResolveContext_inheritsDefaultTimeout(t *testing.T) {
	cfg := loadString(t, twoContexts)
	_, cc, err := cfg.ResolveContext("a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want inherited 5s", cc.Timeout)
	}
}

func TestResolveContext_perContextTimeoutWins(t *testing.T) {
	t.Setenv("ESOPS_TEST_PW", "p")
	cfg := loadString(t, twoContexts)
	_, cc, err := cfg.ResolveContext("b")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Timeout != 1*time.Second {
		t.Errorf("timeout = %v, want override 1s", cc.Timeout)
	}
}

func TestResolveContext_resolvesEnvSecret(t *testing.T) {
	t.Setenv("ESOPS_TEST_PW", "s3cret")
	cfg := loadString(t, twoContexts)
	_, cc, err := cfg.ResolveContext("b")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "s3cret" {
		t.Errorf("password = %q, want resolved value", cc.Auth.Password)
	}
}

func TestResolveContext_unsetEnvSecretErrors(t *testing.T) {
	_ = os.Unsetenv("ESOPS_TEST_PW")
	cfg := loadString(t, twoContexts)
	_, _, err := cfg.ResolveContext("b")
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
	if !strings.Contains(err.Error(), "ESOPS_TEST_PW") {
		t.Errorf("error should name the missing variable: %v", err)
	}
}

func TestResolveContext_unsetEnvInOtherContextDoesNotBlock(t *testing.T) {
	// Context a doesn't reference any secret. Resolving it must succeed
	// even when context b's env var is unset — otherwise a misconfigured
	// unrelated context would block every command.
	_ = os.Unsetenv("ESOPS_TEST_PW")
	cfg := loadString(t, twoContexts)
	if _, _, err := cfg.ResolveContext("a"); err != nil {
		t.Errorf("resolving unrelated context should succeed, got: %v", err)
	}
}

func TestResolveContext_unknownName(t *testing.T) {
	cfg := loadString(t, twoContexts)
	_, _, err := cfg.ResolveContext("nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("expected not-found error naming the context, got %v", err)
	}
}

func TestResolveContext_noneSetNoCurrent(t *testing.T) {
	cfg := loadString(t, `contexts:
  solo: {url: http://example:9200}
`)
	_, _, err := cfg.ResolveContext("")
	if err == nil {
		t.Fatal("expected error when no context specified and no current-context set")
	}
}

func TestAddresses_prefersURLsListOverSingleURL(t *testing.T) {
	cfg := loadString(t, `
contexts:
  multi:
    url: http://single:9200
    urls:
      - http://a:9200
      - http://b:9200
`)
	cc := cfg.Contexts["multi"]
	got := cc.Addresses()
	want := []string{"http://a:9200", "http://b:9200"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAddresses_emptyWhenNeitherSet(t *testing.T) {
	cfg := loadString(t, `
contexts:
  empty: {}
`)
	if got := cfg.Contexts["empty"].Addresses(); got != nil {
		t.Errorf("Addresses() = %v, want nil for empty context", got)
	}
}

func TestSecretScheme_fileReadsContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${file:`+path+`}
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "s3cret" {
		t.Errorf("password = %q, want %q (trailing newline stripped)", cc.Auth.Password, "s3cret")
	}
}

func TestSecretScheme_fileStripsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("s3cret\r\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${file:`+path+`}
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "s3cret" {
		t.Errorf("password = %q, want %q", cc.Auth.Password, "s3cret")
	}
}

func TestSecretScheme_filePreservesInternalNewlines(t *testing.T) {
	// PEM-encoded tokens and the like contain internal newlines. Only
	// the trailing run should be stripped, not interior structure.
	dir := t.TempDir()
	path := filepath.Join(dir, "pem")
	body := "-----BEGIN TOKEN-----\nabc\ndef\n-----END TOKEN-----\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${file:`+path+`}
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := "-----BEGIN TOKEN-----\nabc\ndef\n-----END TOKEN-----"
	if cc.Auth.Password != want {
		t.Errorf("password = %q, want %q", cc.Auth.Password, want)
	}
}

func TestSecretScheme_fileMissingErrors(t *testing.T) {
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${file:/does/not/exist/esops-secret}
`)
	_, _, err := cfg.ResolveContext("x")
	if err == nil || !strings.Contains(err.Error(), "/does/not/exist/esops-secret") {
		t.Errorf("err = %v, want error naming the missing path", err)
	}
}

func TestSecretScheme_keyringReadsEntry(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("esops", "prod-eu", "k3yring-secret"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${keyring:esops/prod-eu}
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "k3yring-secret" {
		t.Errorf("password = %q, want %q", cc.Auth.Password, "k3yring-secret")
	}
}

func TestSecretScheme_keyringMissingEntryErrors(t *testing.T) {
	keyring.MockInit()
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${keyring:esops/absent}
`)
	_, _, err := cfg.ResolveContext("x")
	if err == nil || !strings.Contains(err.Error(), "esops/absent") {
		t.Errorf("err = %v, want error naming the missing keyring entry", err)
	}
}

func TestSecretScheme_keyringBadFormat(t *testing.T) {
	for _, ref := range []string{"${keyring:no-slash}", "${keyring:/no-service}", "${keyring:no-account/}"} {
		cfg := loadString(t, `
contexts:
  x:
    auth:
      password: `+ref+`
`)
		_, _, err := cfg.ResolveContext("x")
		if err == nil || !strings.Contains(err.Error(), "service/account") {
			t.Errorf("%s: err = %v, want 'service/account' format error", ref, err)
		}
	}
}

func TestSecretScheme_keyringAccountMayContainSlashes(t *testing.T) {
	// Only the first slash separates service from account; a URL-shaped
	// account like "host:port/path" must round-trip unchanged.
	keyring.MockInit()
	if err := keyring.Set("esops", "host:9200/path", "v"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${keyring:esops/host:9200/path}
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "v" {
		t.Errorf("password = %q, want %q", cc.Auth.Password, "v")
	}
}

// TestSecretScheme_appliesToAllSecretFields covers the three secret
// fields (password, api_key, token) for both file and keyring schemes
// in a single matrix. Resolution shares one code path; this test is a
// regression net for accidentally narrowing it.
func TestSecretScheme_appliesToAllSecretFields(t *testing.T) {
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw")
	keyFile := filepath.Join(dir, "key")
	tokFile := filepath.Join(dir, "tok")
	for path, body := range map[string]string{pwFile: "pw-from-file", keyFile: "key-from-file", tokFile: "tok-from-file"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	keyring.MockInit()
	for acct, val := range map[string]string{"pw": "pw-from-keyring", "key": "key-from-keyring", "tok": "tok-from-keyring"} {
		if err := keyring.Set("esops", acct, val); err != nil {
			t.Fatalf("seed keyring %s: %v", acct, err)
		}
	}

	cases := []struct {
		name     string
		field    string
		ref      string
		want     string
		extract  func(config.Context) string
	}{
		{"password file", "password", "${file:" + pwFile + "}", "pw-from-file", func(c config.Context) string { return c.Auth.Password }},
		{"api_key file", "api_key", "${file:" + keyFile + "}", "key-from-file", func(c config.Context) string { return c.Auth.APIKey }},
		{"token file", "token", "${file:" + tokFile + "}", "tok-from-file", func(c config.Context) string { return c.Auth.Token }},
		{"password keyring", "password", "${keyring:esops/pw}", "pw-from-keyring", func(c config.Context) string { return c.Auth.Password }},
		{"api_key keyring", "api_key", "${keyring:esops/key}", "key-from-keyring", func(c config.Context) string { return c.Auth.APIKey }},
		{"token keyring", "token", "${keyring:esops/tok}", "tok-from-keyring", func(c config.Context) string { return c.Auth.Token }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadString(t, `
contexts:
  x:
    auth:
      `+tc.field+`: `+tc.ref+`
`)
			_, cc, err := cfg.ResolveContext("x")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got := tc.extract(cc); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.field, got, tc.want)
			}
		})
	}
}

func TestSecretScheme_unknown(t *testing.T) {
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: ${bogus:xyz}
`)
	_, _, err := cfg.ResolveContext("x")
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("err = %v, want 'unknown' secret scheme", err)
	}
}

func TestSecretScheme_literalPasswordUnchanged(t *testing.T) {
	cfg := loadString(t, `
contexts:
  x:
    auth:
      password: plaintext-value
`)
	_, cc, err := cfg.ResolveContext("x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cc.Auth.Password != "plaintext-value" {
		t.Errorf("literal password was rewritten to %q", cc.Auth.Password)
	}
}
