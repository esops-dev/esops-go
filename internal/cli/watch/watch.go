// Package watch implements the `--watch` flag set shared by read-only
// inspection commands. It re-runs a tick callback on a fixed interval,
// kubectl-style: table/wide output clears the screen and prints a one
// line header, NDJSON appends per-tick records to stdout, and JSON/YAML
// are refused up front because they expect a single complete document.
//
// Watch is opt-in per command. Commands that opt in append Flags() to
// their flag set and wrap their body in Run.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/client"
)

// Default and bounds documented in the user story: watch is meant for
// incident-room polling, not high-frequency probing or hour-long stares.
const (
	DefaultInterval = 2 * time.Second
	MinInterval     = 500 * time.Millisecond
	MaxInterval     = 5 * time.Minute
)

// Tick renders a single iteration of the watched command. The watch
// loop calls it once per interval; the tick is responsible for writing
// its own output to stdout (or wherever the command normally writes).
type Tick func(ctx context.Context) error

// Options carries the per-command inputs to Run. Grouped into a struct
// so the call site stays readable as the watch surface grows — a flat
// positional arg list would otherwise bury the tick closure behind a
// handful of strings.
type Options struct {
	// Label is the kubectl-style banner text (e.g. "esops ops health").
	// Keep it short and stable across releases — operators read it to
	// disambiguate stacked panes.
	Label string

	// ContextName is the resolved cluster context (from
	// cmdsetup.Setup.ContextName). It's surfaced in the table/wide
	// header inline and emitted as a watch.start structured log on
	// entry so NDJSON pipelines — which can't take an inline banner
	// without breaking parsers — still leave a stderr trace of which
	// cluster the loop is hitting. The watch loop holds the connection
	// it was given; `esops config use-context` elsewhere does not
	// retarget a running watch.
	ContextName string

	// Format gates the rendering style: table/wide clear and repaint,
	// NDJSON appends, JSON/YAML are refused up front.
	Format output.Format
}

// Flags returns the CLI flag set every watch-capable command appends to
// its own flags. Kept in one place so the help text, defaults, and
// behaviour stay consistent across commands.
func Flags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:    "watch",
			Aliases: []string{"w"},
			Usage:   "Re-render the result on a fixed interval until interrupted (table/wide clear the screen; ndjson appends)",
		},
		&cli.DurationFlag{
			Name:  "watch-interval",
			Usage: "Interval between watch ticks (min 500ms, max 5m)",
			Value: DefaultInterval,
		},
		&cli.IntFlag{
			Name:  "watch-iterations",
			Usage: "Cap the number of ticks (0 = unlimited; useful for CI smoke tests)",
		},
	}
}

// Run executes tick once, or repeatedly under watch mode. Commands call
// it after cmdsetup.Prepare has resolved --output, so opts.Format has
// already been allowlisted by the command.
func Run(ctx context.Context, cmd *cli.Command, opts Options, tick Tick) error {
	if !cmd.Bool("watch") {
		return tick(ctx)
	}
	cfg, err := parseConfig(cmd, opts.Format, isTTY(os.Stdout), inCI())
	if err != nil {
		return err
	}
	// One stderr-bound structured log on entry so NDJSON pipelines (and
	// any log-archival tail) record which cluster this watch is hitting.
	// Table/wide also get this in addition to the in-band header — cheap,
	// and useful for post-incident review when the terminal scrollback
	// is gone.
	logging.Logger().Info("watch.start",
		"label", opts.Label,
		"context", opts.ContextName,
		"format", string(opts.Format),
		"interval", cfg.Interval.String(),
	)
	return loop(ctx, os.Stdout, opts.Label, opts.ContextName, cfg, tick)
}

// config carries validated watch state. Internal so callers stay on the
// public Run entry point.
type config struct {
	Interval    time.Duration
	Iterations  int
	Format      output.Format
	ShowHeader  bool // table/wide
	ClearScreen bool // table/wide on a real terminal — never write ANSI to a pipe
}

func parseConfig(cmd *cli.Command, format output.Format, stdoutIsTTY, ci bool) (config, error) {
	if ci {
		return config{}, client.UsageError("--watch is refused under CI=true; watch loops in CI are almost always a mistake")
	}
	switch format {
	case output.FormatJSON, output.FormatYAML:
		return config{}, client.UsageError("--watch is not supported with --output %s; use ndjson for a streaming feed or table/wide for a repainting view", format)
	case output.FormatTable, output.FormatWide, output.FormatNDJSON:
		// supported
	default:
		return config{}, client.UsageError("--watch is not supported with --output %s", format)
	}

	interval := cmd.Duration("watch-interval")
	if interval < MinInterval {
		return config{}, client.UsageError("--watch-interval %s is below the minimum %s", interval, MinInterval)
	}
	if interval > MaxInterval {
		return config{}, client.UsageError("--watch-interval %s exceeds the maximum %s", interval, MaxInterval)
	}

	iterations := cmd.Int("watch-iterations")
	if iterations < 0 {
		return config{}, client.UsageError("--watch-iterations must be >= 0 (0 means unlimited)")
	}

	repaint := format == output.FormatTable || format == output.FormatWide
	return config{
		Interval:    interval,
		Iterations:  iterations,
		Format:      format,
		ShowHeader:  repaint,
		ClearScreen: repaint && stdoutIsTTY,
	}, nil
}

// loop is the watch driver. Kept package-private so tests can drive it
// with a fake writer and a synthetic tick without going through cli
// flag plumbing.
func loop(ctx context.Context, w io.Writer, label, contextName string, cfg config, tick Tick) error {
	for iteration := 0; ; iteration++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		if cfg.ShowHeader {
			if cfg.ClearScreen {
				if _, err := io.WriteString(w, clearScreenSeq); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(w, header(label, contextName, cfg.Interval, time.Now().UTC())); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}

		if err := tick(ctx); err != nil {
			if isTerminal(err) {
				return err
			}
			// Transient: log to the structured sink (stderr by default)
			// and keep going. The next tick may succeed.
			logging.Logger().Warn("watch.tick_error", "err", err.Error(), "iteration", iteration)
		}

		if cfg.Iterations > 0 && iteration+1 >= cfg.Iterations {
			return nil
		}

		// time.After is safe under Go 1.23+: timers no longer pin
		// themselves and get reclaimed when no longer referenced, so
		// the one-timer-per-iteration form costs nothing on a 500ms-5m
		// loop and avoids the Stop/Drain dance of a reused timer.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.Interval):
		}
	}
}

// isTerminal classifies an error as one that won't recover on the next
// tick. Auth, authorization, dialect-unsupported, and unknown-product
// errors fall here; transport blips, 5xx, and timeouts do not.
//
// context.Canceled / DeadlineExceeded are terminal too — they're the
// shape SIGINT and the ctx-level timeout take, and operators expect
// Ctrl-C to exit immediately, not get swallowed as "transient".
func isTerminal(err error) bool {
	switch {
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.Is(err, client.ErrAuth),
		errors.Is(err, client.ErrForbidden),
		errors.Is(err, client.ErrUnsupported),
		errors.Is(err, client.ErrUnknownProduct),
		errors.Is(err, client.ErrUsage):
		return true
	}
	return false
}

// clearScreenSeq is the ANSI sequence to clear the screen and move the
// cursor to (0,0). Modern terminals (xterm/iTerm/Windows Terminal/tmux)
// all honor it; only emitted when stdout is a real terminal so pipes,
// tee, and file redirects don't end up with literal escape bytes.
const clearScreenSeq = "\x1b[2J\x1b[H"

// isTTY reports whether w is a *os.File wrapping a character device. A
// non-File writer (bytes.Buffer in tests, anything custom) is treated
// as non-TTY, which is the safe default — never emit ANSI to something
// we can't confirm is a terminal.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// header renders the kubectl-style banner: "Every 2s — esops ops
// health — context: prod-eu — 2026-05-13T19:14:02Z". The em dash
// separator matches the rest of the CLI's help text style. The context
// segment is mandatory — cmdsetup.Prepare always resolves a non-empty
// name (config.ResolveContext errors otherwise), so the production
// callers can never legitimately reach here with an empty string.
func header(label, contextName string, interval time.Duration, now time.Time) string {
	return fmt.Sprintf("Every %s — %s — context: %s — %s", interval, label, contextName, now.Format(time.RFC3339))
}

// inCI mirrors internal/cli.inCI. Kept local to avoid pulling the
// watch package into a cli/root import cycle for one env lookup.
func inCI() bool {
	v := os.Getenv("CI")
	return v == "true" || v == "1"
}
