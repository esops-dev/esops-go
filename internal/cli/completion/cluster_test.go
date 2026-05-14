package completion

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

// TestSuggestContexts_emitsConfiguredNames covers the local-only
// suggester. It writes a minimal config to a tempdir and points
// --config at it, exercising the same code path the root ShellComplete
// uses without depending on $HOME or XDG state.
func TestSuggestContexts_emitsConfiguredNames(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "esops.yaml")
	const yaml = `current-context: dev
contexts:
  prod:
    url: http://prod.example.com:9200
  staging:
    url: http://staging.example.com:9200
  dev:
    url: http://localhost:9200
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := newCmd(t, "--config", cfgPath)
	got := SuggestContexts(context.Background(), cmd)
	want := []string{"dev", "prod", "staging"}
	if !stringsEqual(got, want) {
		t.Errorf("SuggestContexts = %v, want %v (sorted)", got, want)
	}
}

func TestSuggestContexts_silentOnMissingConfig(t *testing.T) {
	// Point --config at a path that doesn't exist. Completion must
	// produce zero suggestions instead of an error.
	cmd := newCmd(t, "--config", "/nope/does-not-exist.yaml")
	if got := SuggestContexts(context.Background(), cmd); len(got) != 0 {
		t.Errorf("expected empty suggestions on missing config, got %v", got)
	}
}

func TestCache_roundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	want := []string{"alpha", "beta", "gamma"}
	writeCache("ctx-1", "indices", want, time.Hour)

	got, ok := readCache("ctx-1", "indices", time.Hour)
	if !ok {
		t.Fatal("readCache reported miss after writeCache")
	}
	if !stringsEqual(got, want) {
		t.Errorf("readCache = %v, want %v", got, want)
	}
}

func TestCache_expiresPastTTL(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeCache("ctx-1", "indices", []string{"alpha"}, time.Hour)

	// Backdate the file so the TTL check trips.
	dir := cacheDir()
	if dir == "" {
		t.Fatal("cacheDir returned empty in test")
	}
	path := filepath.Join(dir, cacheKey("ctx-1", "indices"))
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, ok := readCache("ctx-1", "indices", time.Hour); ok {
		t.Error("readCache returned a stale entry; expected miss")
	}
}

func TestCache_keyedByContext(t *testing.T) {
	// Two contexts caching the same shape must not see each other's
	// suggestions — that's the invalidation-on-context-switch the
	// roadmap calls for.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeCache("ctx-prod", "indices", []string{"prod-idx"}, time.Hour)
	writeCache("ctx-dev", "indices", []string{"dev-idx"}, time.Hour)

	prod, ok := readCache("ctx-prod", "indices", time.Hour)
	if !ok || !stringsEqual(prod, []string{"prod-idx"}) {
		t.Errorf("prod cache = %v ok=%v; want [prod-idx]", prod, ok)
	}
	dev, ok := readCache("ctx-dev", "indices", time.Hour)
	if !ok || !stringsEqual(dev, []string{"dev-idx"}) {
		t.Errorf("dev cache = %v ok=%v; want [dev-idx]", dev, ok)
	}
}

func TestCache_disabledOnZeroTTL(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeCache("ctx-1", "indices", []string{"alpha"}, 0)
	if _, ok := readCache("ctx-1", "indices", 0); ok {
		t.Error("readCache returned a hit when TTL=0; want disabled")
	}
}

func TestCacheKey_stableForSameInput(t *testing.T) {
	a := cacheKey("prod", "indices")
	b := cacheKey("prod", "indices")
	c := cacheKey("staging", "indices")
	d := cacheKey("prod", "snapshots")

	if a != b {
		t.Errorf("cacheKey unstable for same input: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("cacheKey collided across contexts: prod and staging both %q", a)
	}
	if a == d {
		t.Errorf("cacheKey collided across shapes: indices and snapshots both %q", a)
	}
	if !strings.HasSuffix(a, "-indices") {
		t.Errorf("cacheKey lost the human-readable suffix: %q", a)
	}
}

// newCmd builds a stub root cli.Command whose flag set matches what
// the real root command exposes — enough so the suggesters under
// test can call cmd.String/Duration on the flags they read.
func newCmd(t *testing.T, args ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	app := &cli.Command{
		Name:   "test",
		Writer: &bytes.Buffer{},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config"},
			&cli.StringFlag{Name: "context"},
			&cli.StringFlag{Name: "url"},
			&cli.DurationFlag{Name: "timeout"},
			&cli.BoolFlag{Name: "insecure"},
			&cli.StringFlag{Name: "cacert"},
			&cli.DurationFlag{Name: "complete-timeout", Value: DefaultCompleteTimeout},
			&cli.DurationFlag{Name: "complete-cache", Value: DefaultCompleteCacheTTL},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := app.Run(context.Background(), append([]string{"test"}, args...)); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if captured == nil {
		t.Fatal("test stub did not capture cmd")
	}
	return captured
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
