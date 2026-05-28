// Package audit appends one NDJSON record per esops invocation to a
// local audit log when --audit-file (or defaults.audit_file) is set.
//
// The goal is reconstructable "who ran what against which cluster, when,
// with what outcome" telemetry for environments with audit obligations
// (SOC2, ISO 27001, internal change-management). It is off by default
// and writes nothing unless the operator opts in by pointing at a path.
//
// Lifecycle:
//
//   - Init(path) opens the file in append mode with 0600. Empty path
//     keeps auditing disabled. Open failures log a warn and continue —
//     a broken audit file must never block a command (§Local audit log
//     in ROADMAP.md).
//   - Begin(...) is called from cmdsetup.Prepare once context, cluster
//     URL, and dry-run are resolved. It captures the start time and
//     the per-invocation snapshot.
//   - Summary(v) attaches optional per-command structured data
//     (reindex throughput, on-error counts, …). Safe to call before
//     Begin — it's recorded against the active record or dropped.
//   - Finish(exitCode, errClass) writes the record. Called once at
//     process exit from main.go. No-ops when Begin wasn't reached
//     (e.g. --help, flag-parse errors) since there's no command to
//     audit.
//
// Writing is a single os.File.Write of the marshalled record plus
// newline. POSIX O_APPEND keeps concurrent writers from interleaving.
package audit

import (
	"encoding/json"
	"net/url"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/esops-dev/esops-go/internal/logging"
	"github.com/esops-dev/esops-go/internal/version"
)

// SchemaVersion is the integer stamped on every emitted record. Bump
// only when consumer-visible fields change in a way that breaks
// downstream parsers.
const SchemaVersion = 1

// Record is the on-disk shape of one audit line. JSON tags are the
// stable contract — keep field names in lock-step with ROADMAP.md's
// Local audit log story.
type Record struct {
	SchemaVersion int      `json:"schema_version"`
	Timestamp     string   `json:"timestamp"`
	DurationMS    int64    `json:"duration_ms"`
	ToolVersion   string   `json:"tool_version"`
	ToolCommit    string   `json:"tool_commit"`
	OSUser        string   `json:"os_user,omitempty"`
	Hostname      string   `json:"hostname,omitempty"`
	Command       string   `json:"command"`
	Args          []string `json:"args"`
	Context       string   `json:"context,omitempty"`
	ClusterURL    string   `json:"cluster_url,omitempty"`
	DryRun        bool     `json:"dry_run"`
	ExitCode      int      `json:"exit_code"`
	Error         string   `json:"error,omitempty"`
	Summary       any      `json:"summary,omitempty"`
}

// state is the package-private audit handle: the opened file (nil when
// disabled), and a pending in-progress Record waiting for Finish to
// stamp the exit code and write it out. A single mu guards both.
var (
	mu      sync.Mutex
	file    *os.File
	pending *Record
	started time.Time
)

// Init opens path in append/create mode with 0600 permissions and
// stashes the handle for later writes. Empty path disables auditing.
// On any failure (expansion, open) it logs a warning and leaves
// auditing disabled — per the spec, a broken audit destination must
// not fail the command.
//
// Calling Init again with a different path closes the previous file
// and opens the new one. Tests rely on this to swap destinations.
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()

	closeLocked()

	if strings.TrimSpace(path) == "" {
		return
	}

	expanded, err := ExpandPath(path)
	if err != nil {
		logging.Logger().Warn("audit.disabled", "path", path, "error", err.Error())
		return
	}

	// #nosec G302,G304 -- audit file path is user-supplied via --audit-file / audit_file; 0600 is intentional.
	f, err := os.OpenFile(expanded, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logging.Logger().Warn("audit.open_failed", "path", expanded, "error", err.Error())
		return
	}
	file = f
}

// Enabled reports whether auditing has been initialised with a writable
// destination. Used by tests and by paths that want to skip preparing
// per-command summaries when no one is listening.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return file != nil
}

// Begin records the per-invocation snapshot. Called from
// cmdsetup.Prepare once the resolved context name, cluster URL, and
// dry-run flag are known. argv is the raw os.Args slice from main —
// Begin redacts known sensitive flag values before storing.
//
// No-op when auditing is disabled; that is the common case (no
// --audit-file, no defaults.audit_file).
func Begin(cmdPath string, argv []string, contextName, clusterURL string, dryRun bool) {
	mu.Lock()
	defer mu.Unlock()

	if file == nil {
		return
	}

	hostname, _ := os.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	started = time.Now()
	pending = &Record{
		SchemaVersion: SchemaVersion,
		Timestamp:     started.UTC().Format(time.RFC3339),
		ToolVersion:   version.Version,
		ToolCommit:    version.Commit,
		OSUser:        username,
		Hostname:      hostname,
		Command:       cmdPath,
		Args:          RedactArgs(argv),
		Context:       contextName,
		ClusterURL:    SanitiseURL(clusterURL),
		DryRun:        dryRun,
	}
}

// Summary attaches the optional per-command structured payload. Safe
// to call before Begin (drops silently) and after Finish (same). The
// payload is whatever the command wants to record — reindex stats,
// on-error counts, snapshot identifiers — and is marshalled as JSON
// alongside the rest of the record.
func Summary(v any) {
	mu.Lock()
	defer mu.Unlock()
	if pending == nil {
		return
	}
	pending.Summary = v
}

// Finish stamps the exit code and error class, marshals the record,
// and appends it to the audit file. errClass is the short string
// recorded under the "error" field (e.g. "auth", "unreachable"); pass
// "" for successful invocations. Always closes the file handle so
// the OS releases the inode as soon as the command exits.
//
// No-op when auditing is disabled or Begin was not reached.
func Finish(exitCode int, errClass string) {
	mu.Lock()
	defer mu.Unlock()

	if file == nil || pending == nil {
		closeLocked()
		return
	}

	pending.ExitCode = exitCode
	pending.Error = errClass
	pending.DurationMS = time.Since(started).Milliseconds()

	data, err := json.Marshal(pending)
	if err != nil {
		logging.Logger().Warn("audit.marshal_failed", "error", err.Error())
		closeLocked()
		return
	}

	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		logging.Logger().Warn("audit.write_failed", "error", err.Error())
	}
	closeLocked()
}

// Reset drops any pending record and closes the file. Tests call it
// to reset the package between cases; production code does not need
// it because Init reopens cleanly.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	closeLocked()
}

func closeLocked() {
	if file != nil {
		_ = file.Close()
		file = nil
	}
	pending = nil
	started = time.Time{}
}

// SanitiseURL returns scheme://host[:port] only. Userinfo, paths, and
// query strings are stripped so a credential that snuck into the
// configured URL never lands in the audit log. Returns "" on a parse
// failure or a URL without a host.
func SanitiseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	clean := url.URL{Scheme: u.Scheme, Host: u.Host}
	return clean.String()
}

// sensitiveFlagNames is the set of long-flag names (without the
// leading dashes) whose value should be redacted from the recorded
// argv. Mirrors internal/logging.redactKeys for the obvious overlaps
// and adds the flag aliases ES/OS commands accept. Lowercased and
// matched against the canonical "--name" form.
var sensitiveFlagNames = map[string]struct{}{
	"password":      {},
	"api-key":       {},
	"api_key":       {},
	"apikey":        {},
	"token":         {},
	"secret":        {},
	"client-key":    {},
	"client_key":    {},
	"authorization": {},
	"encoded":       {},
}

// RedactArgs walks argv and replaces values for sensitive flags with
// the placeholder. Handles both `--flag value` and `--flag=value`
// forms.
//
// Short-form aliases (`-p`) are not redacted: esops exposes no
// sensitive single-letter flag today, and a blanket short-form match
// would catch innocent flags that share a letter with a secret one
// (e.g. -t for tag vs -t for token in a hypothetical command). When a
// new sensitive flag is introduced anywhere in the CLI tree, add its
// canonical long-form name to sensitiveFlagNames above — that single
// map is the audit redactor's source of truth.
//
// Exported for tests; called from Begin.
func RedactArgs(argv []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := 0; i < len(out); i++ {
		tok := out[i]
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, val, hasEq := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
		if _, hit := sensitiveFlagNames[strings.ToLower(name)]; !hit {
			continue
		}
		if hasEq {
			_ = val
			out[i] = "--" + name + "=<redacted>"
			continue
		}
		if i+1 < len(out) {
			out[i+1] = "<redacted>"
			i++
		}
	}
	return out
}

// ExpandPath expands a leading ~ to the user's home directory and
// resolves $VAR / ${VAR} references. Mirrors what operators expect
// from a "PATH" config field and keeps the audit-file lookup honest
// when the operator writes ~/audit.log in their config.
func ExpandPath(path string) (string, error) {
	expanded := os.ExpandEnv(path)
	if strings.HasPrefix(expanded, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch {
		case expanded == "~":
			expanded = home
		case strings.HasPrefix(expanded, "~/"):
			expanded = home + expanded[1:]
		}
	}
	return expanded, nil
}
