package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

// cmdWith runs a stub urfave/cli app long enough to parse the given
// argv and hand back the resulting *cli.Command. Mirrors the helper
// used in the cmdsetup tests so flag state under test matches what the
// production harness exposes.
func cmdWith(t *testing.T, args ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	app := &cli.Command{
		Name:  "test",
		Flags: Flags(),
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

func TestRun_watchDisabled_runsTickOnce(t *testing.T) {
	cmd := cmdWith(t)
	var calls int
	err := Run(context.Background(), cmd, "test", output.FormatTable, func(ctx context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRun_watchDisabled_propagatesError(t *testing.T) {
	cmd := cmdWith(t)
	sentinel := errors.New("boom")
	err := Run(context.Background(), cmd, "test", output.FormatTable, func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("Run err = %v, want %v", err, sentinel)
	}
}

func TestParseConfig_rejectsJSON(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	_, err := parseConfig(cmd, output.FormatJSON, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig with json: err = %v, want ErrUsage", err)
	}
}

func TestParseConfig_rejectsYAML(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	_, err := parseConfig(cmd, output.FormatYAML, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig with yaml: err = %v, want ErrUsage", err)
	}
}

func TestParseConfig_acceptsTable(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	cfg, err := parseConfig(cmd, output.FormatTable, true)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.ShowHeader {
		t.Errorf("table should show header")
	}
	if !cfg.ClearScreen {
		t.Errorf("table on a TTY should clear screen")
	}
}

func TestParseConfig_acceptsWide(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	cfg, err := parseConfig(cmd, output.FormatWide, true)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.ShowHeader {
		t.Errorf("wide should show header")
	}
	if !cfg.ClearScreen {
		t.Errorf("wide on a TTY should clear screen")
	}
}

func TestParseConfig_tableOnNonTTY_dropsClearScreen(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	cfg, err := parseConfig(cmd, output.FormatTable, false)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.ShowHeader {
		t.Errorf("table off a TTY should still show the header (operators tee to log files)")
	}
	if cfg.ClearScreen {
		t.Errorf("ANSI clear must not be written to non-TTY stdout")
	}
}

func TestParseConfig_acceptsNDJSON_noHeader(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	cfg, err := parseConfig(cmd, output.FormatNDJSON, true)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ShowHeader {
		t.Errorf("ndjson must not render header")
	}
	if cfg.ClearScreen {
		t.Errorf("ndjson must not clear screen")
	}
}

func TestParseConfig_rejectsIntervalBelowMin(t *testing.T) {
	cmd := cmdWith(t, "--watch", "--watch-interval", "100ms")
	_, err := parseConfig(cmd, output.FormatTable, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig with 100ms: err = %v, want ErrUsage", err)
	}
}

func TestParseConfig_rejectsIntervalAboveMax(t *testing.T) {
	cmd := cmdWith(t, "--watch", "--watch-interval", "10m")
	_, err := parseConfig(cmd, output.FormatTable, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig with 10m: err = %v, want ErrUsage", err)
	}
}

func TestParseConfig_rejectsNegativeIterations(t *testing.T) {
	cmd := cmdWith(t, "--watch", "--watch-iterations", "-1")
	_, err := parseConfig(cmd, output.FormatTable, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig with -1 iterations: err = %v, want ErrUsage", err)
	}
}

func TestParseConfig_defaultInterval(t *testing.T) {
	cmd := cmdWith(t, "--watch")
	cfg, err := parseConfig(cmd, output.FormatTable, true)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Interval != DefaultInterval {
		t.Errorf("Interval = %s, want %s", cfg.Interval, DefaultInterval)
	}
}

func TestParseConfig_rejectsUnderCI(t *testing.T) {
	t.Setenv("CI", "true")
	cmd := cmdWith(t, "--watch")
	_, err := parseConfig(cmd, output.FormatTable, true)
	if !errors.Is(err, client.ErrUsage) {
		t.Errorf("parseConfig under CI: err = %v, want ErrUsage", err)
	}
}

// testTickInterval is the inter-tick sleep used by tests that drive
// loop directly. parseConfig enforces MinInterval (500ms) for the real
// CLI; tests bypass that and use a tiny duration so iteration-count
// assertions don't burn wall time.
const testTickInterval = 5 * time.Millisecond

func TestLoop_iterationsCap(t *testing.T) {
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 3,
		Format:     output.FormatTable,
	}
	var calls int32
	err := loop(context.Background(), io.Discard, "lbl", cfg, func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("ticks = %d, want 3", got)
	}
}

func TestLoop_contextCancelExits(t *testing.T) {
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 0, // unbounded
		Format:     output.FormatNDJSON,
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	done := make(chan error, 1)
	go func() {
		done <- loop(ctx, io.Discard, "lbl", cfg, func(ctx context.Context) error {
			atomic.AddInt32(&calls, 1)
			return nil
		})
	}()
	// Let at least one tick happen, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("loop err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit within 2s of context cancel")
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("loop exited without firing any tick")
	}
}

func TestLoop_terminalErrorStops(t *testing.T) {
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 0,
		Format:     output.FormatTable,
	}
	var calls int32
	err := loop(context.Background(), io.Discard, "lbl", cfg, func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return client.ErrAuth
	})
	if !errors.Is(err, client.ErrAuth) {
		t.Errorf("loop err = %v, want ErrAuth", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (terminal error must stop immediately)", calls)
	}
}

func TestLoop_transientErrorContinues(t *testing.T) {
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 3,
		Format:     output.FormatTable,
	}
	var calls int32
	transient := errors.New("transient blip")
	err := loop(context.Background(), io.Discard, "lbl", cfg, func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return transient
	})
	if err != nil {
		t.Errorf("loop err = %v, want nil (transient must not propagate)", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (transient errors must not stop the loop)", calls)
	}
}

func TestLoop_unreachableIsTransient(t *testing.T) {
	// Network blips are transient under watch — operators expect the
	// loop to recover when the cluster comes back. Only auth-level
	// problems should bail out.
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 2,
		Format:     output.FormatTable,
	}
	var calls int32
	err := loop(context.Background(), io.Discard, "lbl", cfg, func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return fmt.Errorf("wrap: %w", client.ErrUnreachable)
	})
	if err != nil {
		t.Errorf("loop err = %v, want nil", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestLoop_rendersHeaderAndClearForTTY(t *testing.T) {
	cfg := config{
		Interval:    testTickInterval,
		Iterations:  1,
		Format:      output.FormatTable,
		ShowHeader:  true,
		ClearScreen: true,
	}
	var buf bytes.Buffer
	err := loop(context.Background(), &buf, "esops ops health", cfg, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Every ") {
		t.Errorf("header missing 'Every ': %q", out)
	}
	if !strings.Contains(out, "esops ops health") {
		t.Errorf("header missing label: %q", out)
	}
	if !strings.Contains(out, clearScreenSeq) {
		t.Errorf("ClearScreen=true should emit ANSI clear: %q", out)
	}
}

func TestLoop_headerWithoutClearForNonTTY(t *testing.T) {
	cfg := config{
		Interval:    testTickInterval,
		Iterations:  1,
		Format:      output.FormatTable,
		ShowHeader:  true,
		ClearScreen: false,
	}
	var buf bytes.Buffer
	err := loop(context.Background(), &buf, "esops ops health", cfg, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "esops ops health") {
		t.Errorf("non-TTY table should still show the header: %q", out)
	}
	if strings.Contains(out, clearScreenSeq) {
		t.Errorf("ClearScreen=false must not write ANSI to the writer: %q", out)
	}
}

func TestLoop_noHeaderForNDJSON(t *testing.T) {
	cfg := config{
		Interval:   testTickInterval,
		Iterations: 1,
		Format:     output.FormatNDJSON,
	}
	var buf bytes.Buffer
	err := loop(context.Background(), &buf, "esops ops health", cfg, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("ndjson loop wrote header bytes (%q); should let the tick own stdout", buf.String())
	}
}

func TestHeader_format(t *testing.T) {
	now := time.Date(2026, 5, 13, 19, 14, 2, 0, time.UTC)
	got := header("esops ops health", 2*time.Second, now)
	want := "Every 2s — esops ops health — 2026-05-13T19:14:02Z"
	if got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestIsTTY_nonFileWriter(t *testing.T) {
	if isTTY(&bytes.Buffer{}) {
		t.Error("bytes.Buffer must not be reported as a TTY")
	}
}

func TestIsTTY_regularFile(t *testing.T) {
	// A plain temp file is the shape `esops ops health --watch > out.log`
	// hits; the test would have leaked ANSI bytes into the file if isTTY
	// returned true here.
	f, err := os.CreateTemp(t.TempDir(), "watch-tty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = f.Close() }()
	if isTTY(f) {
		t.Error("regular file must not be reported as a TTY")
	}
}

func TestIsTerminal(t *testing.T) {
	cases := []struct {
		err  error
		want bool
		name string
	}{
		{nil, false, "nil"},
		{errors.New("transient"), false, "plain"},
		{context.Canceled, true, "canceled"},
		{context.DeadlineExceeded, true, "deadline"},
		{client.ErrAuth, true, "auth"},
		{client.ErrForbidden, true, "forbidden"},
		{client.ErrUnsupported, true, "unsupported"},
		{client.ErrUnknownProduct, true, "unknown-product"},
		{client.ErrUnreachable, false, "unreachable"},
		{client.ErrDryRunBlocker, false, "dry-run-blocker"},
		{fmt.Errorf("wrap: %w", client.ErrAuth), true, "wrapped-auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminal(tc.err); got != tc.want {
				t.Errorf("isTerminal(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
