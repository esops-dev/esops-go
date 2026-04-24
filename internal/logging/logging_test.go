package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// newBufHandler returns a text slog handler writing to a bytes.Buffer,
// wrapped in the redactingHandler. Info level matches the production
// default so tests exercise the same level selection as users.
func newBufHandler() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return &buf, slog.New(newRedactingHandler(inner))
}

func TestRedact_topLevelSensitiveKeys(t *testing.T) {
	buf, log := newBufHandler()
	log.Info("auth",
		"password", "hunter2",
		"api_key", "abcd-1234",
		"token", "secret-jwt",
		"authorization", "Bearer xyz",
	)
	got := buf.String()

	for _, leak := range []string{"hunter2", "abcd-1234", "secret-jwt", "Bearer xyz"} {
		if strings.Contains(got, leak) {
			t.Errorf("log output leaked %q: %s", leak, got)
		}
	}
	if count := strings.Count(got, redactedValue); count < 4 {
		t.Errorf("expected at least 4 %q placeholders, got %d: %s", redactedValue, count, got)
	}
}

func TestRedact_keyIsCaseInsensitive(t *testing.T) {
	buf, log := newBufHandler()
	log.Info("auth", "Password", "hunter2", "API_KEY", "abcd")
	got := buf.String()
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abcd") {
		t.Errorf("case-variant keys leaked: %s", got)
	}
}

func TestRedact_nonSensitiveKeysPassThrough(t *testing.T) {
	buf, log := newBufHandler()
	log.Info("request", "context", "prod-eu", "status", 200)
	got := buf.String()
	if !strings.Contains(got, "prod-eu") {
		t.Errorf("expected context=prod-eu in output: %s", got)
	}
	if !strings.Contains(got, "status=200") {
		t.Errorf("expected status=200 in output: %s", got)
	}
	if strings.Contains(got, redactedValue) {
		t.Errorf("no attrs should have been redacted: %s", got)
	}
}

func TestRedact_groupedAttrs(t *testing.T) {
	buf, log := newBufHandler()
	log.Info("auth", slog.Group("creds",
		slog.String("username", "admin"),
		slog.String("password", "hunter2"),
	))
	got := buf.String()
	if strings.Contains(got, "hunter2") {
		t.Errorf("grouped password leaked: %s", got)
	}
	if !strings.Contains(got, "admin") {
		t.Errorf("username should pass through: %s", got)
	}
}

func TestRedact_withAttrsIsApplied(t *testing.T) {
	buf, log := newBufHandler()
	// slog.With uses Handler.WithAttrs — redaction has to apply there too,
	// otherwise `logger.With("password", x).Info(...)` would leak.
	log = log.With("password", "hunter2")
	log.Info("auth")
	got := buf.String()
	if strings.Contains(got, "hunter2") {
		t.Errorf("WithAttrs let password leak: %s", got)
	}
}

func TestInit_rejectsUnknownLevel(t *testing.T) {
	if err := Init("chatty", "text"); err == nil {
		t.Error("expected error for unknown level")
	}
}

func TestInit_rejectsUnknownFormat(t *testing.T) {
	if err := Init("info", "protobuf"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestLogger_safeBeforeInit(t *testing.T) {
	// Zero-value global must be callable; asking for a logger before
	// Init returned a discard-logger.
	l := Logger()
	l.Info("smoke-check", "k", "v")
	// No observable effect, but no panic either.
	if l == nil {
		t.Fatal("Logger() returned nil")
	}
}

func TestRedactingHandler_enabledDelegates(t *testing.T) {
	// Debug messages should be dropped when the inner handler is at info.
	_, log := newBufHandler()
	if log.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected debug to be disabled under info-level handler")
	}
	if !log.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("expected warn to be enabled under info-level handler")
	}
}
