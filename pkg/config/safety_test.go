package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/config"
)

// writeCfg creates a config file at path with the given mode and body.
// Returns the path so callers can feed it to config.Load.
func writeCfg(t *testing.T, body string, mode os.FileMode) string {
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

// clearSafetyEnv wipes the two escape hatches before each test so prior
// runs' settings don't leak into the matrix.
func clearSafetyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "")
	t.Setenv("ESOPS_ALLOW_PLAINTEXT_SECRETS", "")
}

const onePlaintextContext = `
contexts:
  prod:
    url: https://es.example:9200
    auth:
      type: basic
      username: admin
      password: hunter2
`

const oneIndirectedContext = `
contexts:
  prod:
    url: https://es.example:9200
    auth:
      type: basic
      username: admin
      password: ${env:ESOPS_TEST_PW}
`

func TestSafety_worldReadablePlaintextRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	clearSafetyEnv(t)

	path := writeCfg(t, onePlaintextContext, 0o644)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected refusal for world-readable config with plaintext secret")
	}
	if !strings.Contains(err.Error(), "world-readable") {
		t.Errorf("error should mention 'world-readable': %v", err)
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("error should name the offending context: %v", err)
	}
}

func TestSafety_worldReadablePlaintextOverrideAllows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	clearSafetyEnv(t)
	t.Setenv("ESOPS_ALLOW_INSECURE_CONFIG", "1")

	path := writeCfg(t, onePlaintextContext, 0o644)
	if _, err := config.Load(path); err != nil {
		t.Errorf("override should allow load, got: %v", err)
	}
}

func TestSafety_tightPermissionsPlaintextWarnsOnly(t *testing.T) {
	clearSafetyEnv(t)

	path := writeCfg(t, onePlaintextContext, 0o600)
	if _, err := config.Load(path); err != nil {
		t.Errorf("0o600 + plaintext should succeed with a warning, got: %v", err)
	}
}

func TestSafety_indirectionNeverFlagged(t *testing.T) {
	clearSafetyEnv(t)
	t.Setenv("ESOPS_TEST_PW", "p")

	// Even at 0o644, ${env:...} is not plaintext — load must succeed.
	path := writeCfg(t, oneIndirectedContext, 0o644)
	if _, err := config.Load(path); err != nil {
		t.Errorf("${env:...} should not trigger plaintext refusal, got: %v", err)
	}
}

func TestSafety_emptyAuthBlockOK(t *testing.T) {
	clearSafetyEnv(t)

	path := writeCfg(t, `
contexts:
  plain:
    url: http://es:9200
`, 0o644)
	if _, err := config.Load(path); err != nil {
		t.Errorf("context with no auth should load at any mode, got: %v", err)
	}
}

func TestSafety_namesMultipleContexts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-mode check skipped on Windows")
	}
	clearSafetyEnv(t)

	path := writeCfg(t, `
contexts:
  zulu:
    url: http://z:9200
    auth: {password: p1}
  alpha:
    url: http://a:9200
    auth: {api_key: k1}
`, 0o644)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error listing both contexts")
	}
	// Alphabetical ordering makes the warning stable to grep against.
	idxA := strings.Index(err.Error(), "alpha")
	idxZ := strings.Index(err.Error(), "zulu")
	if idxA < 0 || idxZ < 0 {
		t.Fatalf("error should list both contexts, got: %v", err)
	}
	if idxA > idxZ {
		t.Errorf("contexts should be alphabetically ordered in message: %v", err)
	}
}
