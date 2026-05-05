package config_test

import (
	"os"
	"path/filepath"
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
