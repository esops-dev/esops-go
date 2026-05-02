package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/esops-dev/esops-go/internal/config"
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

func TestSecretScheme_fileAndKeyringReportNotImplemented(t *testing.T) {
	for _, ref := range []string{"${file:/tmp/x}", "${keyring:svc/acct}"} {
		cfg := loadString(t, `
contexts:
  x:
    auth:
      password: `+ref+`
`)
		_, _, err := cfg.ResolveContext("x")
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("%s: err = %v, want 'not implemented'", ref, err)
		}
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
