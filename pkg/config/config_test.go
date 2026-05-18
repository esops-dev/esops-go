package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/config"
)

// isolateEnv empties the env vars Resolve inspects and redirects HOME
// into a per-test tempdir so the user's real ~/.config/esops/config.yaml
// never leaks into the test.
func isolateEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ESOPS_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	return home
}

func writeEmpty(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_explicitWinsOverEverything(t *testing.T) {
	t.Setenv("ESOPS_CONFIG", "/this/should/be/ignored")
	got, err := config.Resolve("/explicit/path.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/path.yaml" {
		t.Errorf("got %q, want explicit path", got)
	}
}

func TestResolve_envVarHit(t *testing.T) {
	isolateEnv(t)
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	writeEmpty(t, path)
	t.Setenv("ESOPS_CONFIG", path)

	got, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("got %q, want %q", got, path)
	}
}

func TestResolve_envVarMissingFileIsHardError(t *testing.T) {
	isolateEnv(t)
	t.Setenv("ESOPS_CONFIG", "/does/not/exist.yaml")

	_, err := config.Resolve("")
	if err == nil {
		t.Fatal("expected error for missing ESOPS_CONFIG file")
	}
	if !strings.Contains(err.Error(), "ESOPS_CONFIG") {
		t.Errorf("error should mention ESOPS_CONFIG: %v", err)
	}
}

func TestResolve_perRepoEsopsYaml(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	writeEmpty(t, filepath.Join(dir, "esops.yaml"))

	got, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "esops.yaml" {
		t.Errorf("got %q, want esops.yaml", got)
	}
}

func TestResolve_xdgConfigHome(t *testing.T) {
	isolateEnv(t)
	xdg := t.TempDir()
	cfgPath := filepath.Join(xdg, "esops", "config.yaml")
	writeEmpty(t, cfgPath)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != cfgPath {
		t.Errorf("got %q, want %q", got, cfgPath)
	}
}

func TestResolve_homeDotConfigFallback(t *testing.T) {
	home := isolateEnv(t)
	cfgPath := filepath.Join(home, ".config", "esops", "config.yaml")
	writeEmpty(t, cfgPath)

	got, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != cfgPath {
		t.Errorf("got %q, want %q", got, cfgPath)
	}
}

func TestResolve_perRepoBeatsXDG(t *testing.T) {
	isolateEnv(t)

	work := t.TempDir()
	t.Chdir(work)
	perRepo := filepath.Join(work, "esops.yaml")
	writeEmpty(t, perRepo)

	xdg := t.TempDir()
	writeEmpty(t, filepath.Join(xdg, "esops", "config.yaml"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := config.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "esops.yaml" {
		t.Errorf("per-repo should win; got %q", got)
	}
}

func TestResolve_nothingFoundListsSearchedPaths(t *testing.T) {
	home := isolateEnv(t)

	_, err := config.Resolve("")
	if err == nil {
		t.Fatal("expected error when nothing matches")
	}
	msg := err.Error()
	for _, want := range []string{
		"esops.yaml",
		filepath.Join(home, ".config", "esops", "config.yaml"),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestLoadDefault_resolvesAndParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `current-context: foo
contexts:
  foo:
    url: http://example:9200
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, gotPath, err := config.LoadDefault(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Errorf("returned path %q, want %q", gotPath, path)
	}
	if cfg.CurrentContext != "foo" {
		t.Errorf("current-context %q, want foo", cfg.CurrentContext)
	}
	if cfg.Contexts["foo"].URL != "http://example:9200" {
		t.Errorf("context url mismatch: %+v", cfg.Contexts["foo"])
	}
}

func TestLoadDefault_resolveErrorPropagates(t *testing.T) {
	isolateEnv(t)
	_, _, err := config.LoadDefault("")
	if err == nil {
		t.Fatal("expected error from failed resolve")
	}
}

func TestIsSecretRef(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"hunter2", false},
		{"${env:VAR}", true},
		{"${file:/run/secret}", true},
		{"${keyring:svc/acct}", true},
		{"${env:VAR}suffix", false},
		{"prefix${env:VAR}", false},
		{"${ENV:VAR}", false}, // scheme is lowercase only
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := config.IsSecretRef(tc.in); got != tc.want {
				t.Errorf("IsSecretRef(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseSecretRef(t *testing.T) {
	cases := []struct {
		in         string
		wantScheme string
		wantArg    string
		wantOK     bool
	}{
		{"", "", "", false},
		{"plaintext", "", "", false},
		{"${env:VAR}", "env", "VAR", true},
		{"${file:/run/secret}", "file", "/run/secret", true},
		{"${keyring:svc/acct}", "keyring", "svc/acct", true},
		{"${keyring:svc/host:9200/path}", "keyring", "svc/host:9200/path", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			scheme, arg, ok := config.ParseSecretRef(tc.in)
			if ok != tc.wantOK || scheme != tc.wantScheme || arg != tc.wantArg {
				t.Errorf("ParseSecretRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, scheme, arg, ok, tc.wantScheme, tc.wantArg, tc.wantOK)
			}
		})
	}
}

func TestHasPlaintextSecret(t *testing.T) {
	cases := []struct {
		name string
		auth config.Auth
		want bool
	}{
		{"empty", config.Auth{}, false},
		{"plaintext password", config.Auth{Password: "hunter2"}, true},
		{"plaintext api_key", config.Auth{APIKey: "ABC"}, true},
		{"plaintext token", config.Auth{Token: "tok"}, true},
		{"env-ref password", config.Auth{Password: "${env:PW}"}, false},
		{"file-ref token", config.Auth{Token: "${file:/run/secret}"}, false},
		{"client-cert path is not a secret", config.Auth{ClientCert: "/etc/cert.pem", ClientKey: "/etc/key.pem"}, false},
		{"mix: ref + plaintext", config.Auth{Password: "${env:PW}", Token: "literal"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := config.HasPlaintextSecret(tc.auth); got != tc.want {
				t.Errorf("HasPlaintextSecret(%+v) = %v, want %v", tc.auth, got, tc.want)
			}
		})
	}
}

func TestContextsWithPlaintextSecret_sorted(t *testing.T) {
	cfg := config.Config{
		Contexts: map[string]config.Context{
			"zulu":  {Auth: config.Auth{Password: "literal"}},
			"alpha": {Auth: config.Auth{APIKey: "literal"}},
			"clean": {Auth: config.Auth{Password: "${env:PW}"}},
		},
	}
	got := config.ContextsWithPlaintextSecret(cfg)
	want := []string{"alpha", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	dir := t.TempDir()
	tight := filepath.Join(dir, "tight")
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(tight, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(open, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile respects umask — force modes explicitly.
	_ = os.Chmod(tight, 0o600)
	_ = os.Chmod(open, 0o644)

	for _, tc := range []struct {
		path string
		want bool
	}{
		{tight, false},
		{open, true},
	} {
		got, err := config.IsWorldReadable(tc.path)
		if err != nil {
			t.Fatalf("IsWorldReadable(%s): %v", tc.path, err)
		}
		if got != tc.want {
			t.Errorf("IsWorldReadable(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}

	if _, err := config.IsWorldReadable(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for missing path")
	}
}
