package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeLogFileConfig drops a minimal config pointing log_file at an
// absolute path inside the test's temp dir and returns both paths.
func writeLogFileConfig(t *testing.T) (cfgPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "esops.log")
	cfgPath = filepath.Join(dir, "config.yaml")
	body := "current-context: es\n" +
		"defaults:\n" +
		"  log_file: " + logPath + "\n" +
		"contexts:\n" +
		"  es:\n" +
		"    url: https://localhost:9200\n" +
		"    protection: none\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, logPath
}

// TestInitLogger_completionSuppressesLogFile guards the property an
// operator depends on: sourcing the completion script at shell startup —
// and every TAB-press thereafter — must not open the configured log_file.
// urfave runs the Before chain (and thus initLogger) during completion, so
// the suppression has to live in initLogger, not in any command Action.
func TestInitLogger_completionSuppressesLogFile(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"script generation", []string{"esops", "completion", "zsh"}},
		{"tab press", []string{"esops", "ops", "--generate-shell-completion"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, logPath := writeLogFileConfig(t)
			args := append([]string{tc.args[0], "--config", cfgPath}, tc.args[1:]...)
			// In production main.go calls Run(ctx, os.Args), so the global
			// and the Run arguments are identical; the TAB-press detector
			// reads os.Args because urfave strips its completion sentinel
			// from the parsed command before Before runs. Mirror that here.
			saved := os.Args
			os.Args = args
			defer func() { os.Args = saved }()
			// Completion writes to stdout; we only care about the side effect.
			_ = Run(context.Background(), args)
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("completion opened log file %s (stat err=%v); it must stay free of filesystem side effects", logPath, err)
			}
		})
	}
}

// TestInitLogger_normalCommandOpensLogFile is the companion guard: a real
// command must still honour log_file. Without it, a too-broad completion
// check would silently disable logging for everyone.
func TestInitLogger_normalCommandOpensLogFile(t *testing.T) {
	cfgPath, logPath := writeLogFileConfig(t)
	// `config view` runs no network I/O but still passes through initLogger,
	// which is all this test needs. Its own success/failure is irrelevant.
	_ = Run(context.Background(), []string{"esops", "--config", cfgPath, "config", "view"})
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("normal command did not open configured log file %s: %v", logPath, err)
	}
}
