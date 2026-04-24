// Package logging wraps log/slog behind a tiny facade: Init sets the
// process-wide logger, Logger returns it. Keeping this in one place
// means every command picks up --log-level / --log-format changes
// without plumbing a *slog.Logger through every call.
//
// All handlers installed here pass through a redactingHandler that
// replaces the values of sensitive-named attributes with "<redacted>"
// so a future command that logs an auth-carrying struct can't leak the
// credential. The denylist is not exhaustive — it's a second line of
// defence; the first is not logging secrets in the first place.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	mu     sync.RWMutex
	logger = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// Init configures the process-wide logger from --log-level / --log-format
// / --log-file values. Empty logFile keeps the default destination of
// stderr so stdout stays reserved for the requested data — the output
// contract is that stdout only ever carries the data the user asked for,
// and everything else (progress, logs, errors) lands on stderr or in an
// explicit log file. A non-empty logFile is opened in append mode with
// 0600 permissions — log lines commonly include context/cluster names,
// and there's no reason for other system users to read them.
func Init(level, format, logFile string) error {
	lvl, err := parseLevel(level)
	if err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	w, err := resolveWriter(logFile)
	if err != nil {
		return err
	}

	var inner slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		inner = slog.NewTextHandler(w, opts)
	case "json":
		inner = slog.NewJSONHandler(w, opts)
	default:
		return fmt.Errorf("unknown log format %q (supported: text, json)", format)
	}

	mu.Lock()
	logger = slog.New(newRedactingHandler(inner))
	mu.Unlock()
	return nil
}

// resolveWriter returns os.Stderr when path is empty, otherwise opens
// (creating if needed) the file in append mode with 0600 permissions.
// The parent directory must already exist; creating it implicitly would
// silently hide typos in --log-file.
func resolveWriter(path string) (io.Writer, error) {
	if path == "" {
		return os.Stderr, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G302,G304 -- path is user-supplied via --log-file / log_file; 0600 is intentional
	if err != nil {
		return nil, fmt.Errorf("opening log file %q: %w", path, err)
	}
	return f, nil
}

// Logger returns the current process-wide logger. Safe to call before
// Init: returns a logger that discards everything.
func Logger() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (supported: debug, info, warn, error)", s)
	}
}

// redactedValue is the placeholder written in place of any attribute
// whose key hits the denylist. Kept short and recognisable so operators
// grepping logs see at a glance that redaction fired.
const redactedValue = "<redacted>"

// redactKeys is the lowercase set of attribute keys whose values are
// replaced before reaching the underlying handler. Second line of
// defence behind "never log a secret in the first place".
var redactKeys = map[string]struct{}{
	"authorization": {},
	"password":      {},
	"api_key":       {},
	"apikey":        {},
	"token":         {},
	"secret":        {},
	"client_key":    {},
}

// redactingHandler wraps a slog.Handler and replaces values of
// sensitive-named attributes. The inner handler never sees the raw
// values, so even switching log formats or swapping handlers keeps
// redaction in force.
type redactingHandler struct {
	inner slog.Handler
}

func newRedactingHandler(inner slog.Handler) slog.Handler {
	return &redactingHandler{inner: inner}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	cleaned := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		cleaned.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, cleaned)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr replaces an attribute's value with the redaction placeholder
// when its key is in the denylist. Group values recurse so nested
// attributes (slog.Group("auth", ...)) get the same treatment.
func redactAttr(a slog.Attr) slog.Attr {
	if _, hit := redactKeys[strings.ToLower(a.Key)]; hit {
		return slog.Attr{Key: a.Key, Value: slog.StringValue(redactedValue)}
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := a.Value.Group()
		out := make([]any, 0, len(inner)*2)
		for _, g := range inner {
			r := redactAttr(g)
			out = append(out, r)
		}
		return slog.Group(a.Key, out...)
	}
	return a
}
