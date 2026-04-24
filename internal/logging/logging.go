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
// values. It writes to stderr so stdout stays reserved for the requested
// data, per the output contract in CLAUDE.md §6.
func Init(level, format string) error {
	lvl, err := parseLevel(level)
	if err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var inner slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		inner = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		inner = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("unknown log format %q (supported: text, json)", format)
	}

	mu.Lock()
	logger = slog.New(newRedactingHandler(inner))
	mu.Unlock()
	return nil
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
// replaced before reaching the underlying handler. Matches CLAUDE.md §8.
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
