package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReindexState is the on-disk record of an in-progress `migrate
// reindex`. Persisted under $XDG_STATE_HOME/esops/migrate/ so a SIGINT
// or crashed shell at hour 6 of an 8-hour reindex doesn't strand the
// operator without a way to resume the wait or surface the eventual
// outcome. `migrate reindex --resume` reads this file and continues
// polling the task IDs that did not finish.
//
// The file is named by (source-context, target-context, suffix-or-
// literal) so a parallel reindex into a different target index doesn't
// stomp on its sibling's state. Multiple operators on different shells
// resuming the same key would race; the contract is one long-running
// reindex per (source, target, suffix) tuple at a time, the same
// constraint the cluster itself imposes on the destination indices.
type ReindexState struct {
	Schema         int                 `json:"schema"`
	Action         string              `json:"action"`
	SourceContext  string              `json:"source_context"`
	TargetContext  string              `json:"target_context"`
	TargetSuffix   string              `json:"target_suffix,omitempty"`
	TargetLiteral  string              `json:"target_literal,omitempty"`
	StartedAt      time.Time           `json:"started_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	Entries        []ReindexStateEntry `json:"entries"`
}

// ReindexStateEntry is the per-source-index record. TaskID is empty
// when the cluster never accepted the submission — the resume path
// treats those rows as "nothing to poll, run a fresh submission" if
// the operator passes the same flags. Completed rows survive in the
// state so a captured `--resume` listing knows what already finished.
//
// Status and Detail capture the orchestrator's final verdict for the
// row (one of MigrationStatusOK / Warn / Block plus a one-line
// rationale) so a `--resume` after a partial run reproduces what the
// original output reported, rather than upgrading every Completed row
// to OK. Older state files (schema 1, written before this field set)
// have empty Status; the reader treats that as the legacy "previously
// completed: OK" behaviour for backwards compatibility.
type ReindexStateEntry struct {
	SourceIndex string `json:"source_index"`
	TargetIndex string `json:"target_index"`
	TaskID      string `json:"task_id,omitempty"`
	Completed   bool   `json:"completed"`
	Status      string `json:"status,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// reindexStateSchemaVersion is bumped if the on-disk format changes.
// A loader that sees a newer schema refuses with a clear error rather
// than silently misinterpreting the file — operators can `rm` the file
// to recover.
const reindexStateSchemaVersion = 1

// ErrNoReindexState signals "nothing to resume" so the CLI can surface
// a clean message rather than a generic file-not-found.
var ErrNoReindexState = errors.New("no reindex resume state found")

// ReindexStateDir returns the directory under which `migrate reindex`
// state files live. Honours XDG_STATE_HOME (Linux convention), then
// HOME on macOS, falling back to os.TempDir() on stranger setups so
// the resume path never panics on a missing env. Mirrors
// internal/snapshot/state.go's StateDir; kept independent so a
// future schema change in either tree doesn't ripple through both.
func ReindexStateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "esops", "migrate")
	}
	if home, err := os.UserHomeDir(); err == nil {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", "esops", "migrate")
		case "windows":
			if appData := os.Getenv("APPDATA"); appData != "" {
				return filepath.Join(appData, "esops", "migrate")
			}
			return filepath.Join(home, ".esops", "state", "migrate")
		default:
			return filepath.Join(home, ".local", "state", "esops", "migrate")
		}
	}
	return filepath.Join(os.TempDir(), "esops", "migrate")
}

// ReindexStatePath returns the file path for a reindex's state. The
// suffix or literal is part of the key so a `--to-suffix=-v2` and a
// `--to-suffix=-v3` against the same context pair don't share a file.
func ReindexStatePath(sourceContext, targetContext, suffix, literal string) string {
	key := "reindex_" +
		sanitiseStateKey(sourceContext) + "_" +
		sanitiseStateKey(targetContext) + "_" +
		sanitiseStateKey(targetTuple(suffix, literal)) + ".json"
	return filepath.Join(ReindexStateDir(), key)
}

// targetTuple folds the suffix-or-literal pair into one stable key
// component. Empty (default = "use source name as-is") is rendered as
// "default" so operators with several state files can grep for the
// lookup name they recognise.
func targetTuple(suffix, literal string) string {
	switch {
	case literal != "":
		return "literal-" + literal
	case suffix != "":
		return "suffix" + suffix
	default:
		return "default"
	}
}

func sanitiseStateKey(s string) string {
	if s == "" {
		s = "default"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', ' ':
			return '_'
		default:
			return r
		}
	}, s)
}

// SaveReindexState writes the state file atomically (write to temp,
// rename). The schema is stamped on every write so an old loader
// reading a newer file refuses cleanly. UpdatedAt is rewritten on
// every save so an operator can spot a stale resume file at a glance.
func SaveReindexState(state ReindexState) error {
	dir := ReindexStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	state.Schema = reindexStateSchemaVersion
	state.UpdatedAt = time.Now().UTC()
	final := ReindexStatePath(state.SourceContext, state.TargetContext, state.TargetSuffix, state.TargetLiteral)
	tmp, err := os.CreateTemp(dir, ".reindex-state-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if err := json.NewEncoder(tmp).Encode(state); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

// LoadReindexState reads the state file for a given key. ErrNoReindexState
// is returned when the file doesn't exist — distinguishable from a parse
// error so the CLI can say "nothing to resume" vs "your state file is
// corrupt".
func LoadReindexState(sourceContext, targetContext, suffix, literal string) (ReindexState, error) {
	path := ReindexStatePath(sourceContext, targetContext, suffix, literal)
	data, err := os.ReadFile(path) // #nosec G304 -- path is StateDir() + sanitised tuple, not operator input
	if err != nil {
		if os.IsNotExist(err) {
			return ReindexState{}, ErrNoReindexState
		}
		return ReindexState{}, err
	}
	var state ReindexState
	if err := json.Unmarshal(data, &state); err != nil {
		return ReindexState{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if state.Schema > reindexStateSchemaVersion {
		return ReindexState{}, fmt.Errorf("state file %s has schema %d (this build understands %d); rm the file to recover", path, state.Schema, reindexStateSchemaVersion)
	}
	return state, nil
}

// ClearReindexState removes the state file. Errors other than "file
// already gone" propagate; the caller chooses whether to log or fail.
// Used both on successful completion (every entry Completed=true) and
// on explicit operator request.
func ClearReindexState(sourceContext, targetContext, suffix, literal string) error {
	path := ReindexStatePath(sourceContext, targetContext, suffix, literal)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
