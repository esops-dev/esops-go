package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// TestCacheClear_removesRegularFiles is the golden-path test: a cache
// directory with a couple of files, run cache-clear, see them gone.
func TestCacheClear_removesRegularFiles(t *testing.T) {
	dir := pointCacheAt(t)
	writeFile(t, filepath.Join(dir, "indices"), "alpha\nbeta\n")
	writeFile(t, filepath.Join(dir, "repositories"), "backups\n")

	if err := runCacheClearWith(t); err != nil {
		t.Fatalf("runCacheClear: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, found %d entries", len(entries))
	}
}

// TestCacheClear_dryRunLeavesFiles confirms --dry-run is non-mutating.
// A successful dry-run on a non-empty cache directory must keep the
// files intact — otherwise the preview is a lie.
func TestCacheClear_dryRunLeavesFiles(t *testing.T) {
	dir := pointCacheAt(t)
	writeFile(t, filepath.Join(dir, "indices"), "alpha\n")
	writeFile(t, filepath.Join(dir, "nodes"), "es01\nes02\n")

	if err := runCacheClearWith(t, "--dry-run"); err != nil {
		t.Fatalf("runCacheClear --dry-run: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("dry-run modified the cache: expected 2 files, got %d", len(entries))
	}
}

// TestCacheClear_missingDirectoryIsNoOp covers the case where the
// operator runs cache-clear before completion has ever written a file
// — a clean install. The command must succeed and print a sensible
// message; failing here would surprise an operator who has nothing to
// clear yet.
func TestCacheClear_missingDirectoryIsNoOp(t *testing.T) {
	// Point at a deliberately-uncreated subdir of a temp dir.
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	// Don't create $root/esops/complete/.

	if err := runCacheClearWith(t); err != nil {
		t.Errorf("runCacheClear on missing dir: %v", err)
	}
}

// TestCacheClear_skipsSubdirectories defends in depth against the case
// where some other tool drops a directory under our cache root. The
// command must not descend into it — keeps the blast radius pinned to
// files this package wrote at the top level.
func TestCacheClear_skipsSubdirectories(t *testing.T) {
	dir := pointCacheAt(t)
	writeFile(t, filepath.Join(dir, "indices"), "alpha\n")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "subdir", "stranger"), "data\n")

	if err := runCacheClearWith(t); err != nil {
		t.Fatalf("runCacheClear: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "indices")); !os.IsNotExist(err) {
		t.Error("regular cache file was not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "subdir", "stranger")); err != nil {
		t.Errorf("subdirectory contents were touched: %v", err)
	}
}

// pointCacheAt creates and returns a fresh cache directory rooted in
// a TempDir, and installs XDG_CACHE_HOME so completion.CacheDir()
// resolves to it on Linux. On macOS the function used here honours
// XDG_CACHE_HOME first, so the same setup works without per-OS
// branching.
func pointCacheAt(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	dir := filepath.Join(root, "esops", "complete")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runCacheClearWith spins up a stub urfave/cli app around the cache-
// clear action so the test exercises the production wiring (flag
// parsing, dry-run resolution) instead of calling the action directly
// with a hand-rolled *cli.Command.
func runCacheClearWith(t *testing.T, args ...string) error {
	t.Helper()
	app := &cli.Command{
		Name: "test",
		Commands: []*cli.Command{
			cacheClearCommand(),
		},
	}
	return app.Run(context.Background(), append([]string{"test", "cache-clear"}, args...))
}

// TestCacheClear_outputMentionsDirectory makes sure the human-readable
// output names the directory and the file count. Operators rely on
// this to confirm they cleared the right cache; a silent "done" would
// be ambiguous when troubleshooting.
func TestCacheClear_outputMentionsDirectory(t *testing.T) {
	dir := pointCacheAt(t)
	writeFile(t, filepath.Join(dir, "indices"), "x\n")

	stdout, stderr, err := captureStdio(t, func() error {
		return runCacheClearWith(t)
	})
	if err != nil {
		t.Fatalf("cache-clear: %v (stderr=%q)", err, stderr)
	}
	if !strings.Contains(stdout, dir) {
		t.Errorf("expected output to mention cache dir %q, got: %q", dir, stdout)
	}
	if !strings.Contains(stdout, "1 file") {
		t.Errorf("expected count in output, got: %q", stdout)
	}
}

// captureStdio runs fn with os.Stdout/Stderr redirected to pipes and
// returns the captured strings. Keeps the cache-clear output test
// hermetic without depending on a custom Writer field the production
// command doesn't expose.
func captureStdio(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW

	err = fn()
	_ = outW.Close()
	_ = errW.Close()

	stdout = readAll(t, outR)
	stderr = readAll(t, errR)
	return stdout, stderr, err
}

func readAll(t *testing.T, r *os.File) string {
	t.Helper()
	defer func() { _ = r.Close() }()
	buf := make([]byte, 4096)
	var sb strings.Builder
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}
