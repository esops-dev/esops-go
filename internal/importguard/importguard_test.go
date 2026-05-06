package importguard_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/esops-dev/esops-go"

// TestPkgDoesNotImportInternal — pkg/ is the public surface consumed
// by esops-doctor (separate repo) and any future commercial product.
// It must not transitively reach into internal/, otherwise the public
// surface accidentally drags private implementation along with it
// and a future internal/ rename becomes a downstream breaking change.
func TestPkgDoesNotImportInternal(t *testing.T) {
	root := repoRoot(t)
	pkgRoot := filepath.Join(root, "pkg")
	internalPrefix := modulePath + "/internal/"

	fset := token.NewFileSet()
	var violations []string
	walkGo(t, pkgRoot, func(path string) {
		for _, imp := range fileImports(t, fset, path) {
			if strings.HasPrefix(imp, internalPrefix) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+" → "+imp)
			}
		}
	})
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("pkg/ must not import internal/*:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestClientutilOnlyConsumedByAdapters — pkg/internal/clientutil holds
// HTTP plumbing private to the ES/OS adapters: BoundedReader,
// ClassifyStatus, RunParallel, _cat decoders. Business logic must not
// import it; it talks to the cluster only through capability
// interfaces in pkg/client. A business-logic file reaching for HTTP
// error classification is the signal that a capability has a gap.
func TestClientutilOnlyConsumedByAdapters(t *testing.T) {
	root := repoRoot(t)
	target := modulePath + "/pkg/internal/clientutil"
	allowed := map[string]bool{
		"pkg/internal/client/elasticsearch": true,
		"pkg/internal/client/opensearch":    true,
	}

	fset := token.NewFileSet()
	var violations []string
	walkGo(t, root, func(path string) {
		for _, imp := range fileImports(t, fset, path) {
			if imp != target {
				continue
			}
			pkgDir, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				t.Fatalf("rel: %v", err)
			}
			if !allowed[pkgDir] {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel)
			}
		}
	})
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("pkg/internal/clientutil should only be imported by ES/OS adapters:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// repoRoot walks up from the test's working directory until it finds
// a go.mod, so the tests don't depend on which `go test` invocation
// path is used (`./...`, package-relative, IDE-driven).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked off the filesystem looking for go.mod (cwd=%s)", dir)
		}
		dir = parent
	}
}

// fileImports returns every quoted import path declared in path. We
// use parser.ImportsOnly so build tags / generated-file comments
// don't matter — every import recorded in the source is a constraint
// regardless of whether the file is in the current build.
func fileImports(t *testing.T, fset *token.FileSet, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, "\""))
	}
	return out
}

// walkGo invokes fn for every .go file under root, skipping the
// build/dist artefacts and VCS metadata so the tests don't take a
// detour through dist/ snapshots.
func walkGo(t *testing.T, root string, fn func(path string)) {
	t.Helper()
	skip := map[string]bool{
		".git":         true,
		"dist":         true,
		"bin":          true,
		"vendor":       true,
		"node_modules": true,
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fn(path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
