package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dj-wasabi/esops-go/internal/config"
)

func TestSetCurrentContext_replacesLinePreservingRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := `# top-of-file comment
# another

current-context: old-one

defaults:
  output: table           # inline comment
  timeout: 30s

contexts:
  old-one:
    url: http://old
  new-one:
    url: http://new
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.SetCurrentContext(path, "new-one"); err != nil {
		t.Fatalf("SetCurrentContext: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, "current-context: old-one", "current-context: new-one", 1)
	if string(got) != want {
		t.Errorf("file content mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestSetCurrentContext_insertsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := `contexts:
  only-one:
    url: http://one
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.SetCurrentContext(path, "only-one"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "current-context: only-one\n") {
		t.Errorf("expected prepended current-context line, got:\n%s", got)
	}
	if !strings.Contains(string(got), original) {
		t.Errorf("expected original content preserved, got:\n%s", got)
	}
}

func TestSetCurrentContext_preservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("current-context: a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// WriteFile respects umask; force the mode we actually want to assert on.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := config.SetCurrentContext(path, "b"); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("file mode changed: got %o want 0640", mode)
	}
}

func TestSetCurrentContext_idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "current-context: same\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.SetCurrentContext(path, "same"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("no-op write mutated content\nwant: %q\ngot:  %q", original, got)
	}
}

func TestSetCurrentContext_fileMissing(t *testing.T) {
	err := config.SetCurrentContext(filepath.Join(t.TempDir(), "nope.yaml"), "x")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
