package snapshot

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

// RestoreState is the on-disk record of an in-progress `snapshot
// restore`. Mirrors CreateState in shape; the extra TargetIndices
// field carries the post-rename index list resolved at restore time so
// `--resume` knows which indices to poll `_recovery` for. Without it,
// the resume path would have to re-resolve the snapshot's stored
// indices and re-apply the rename pair — fragile if the snapshot was
// pruned in the interim.
type RestoreState struct {
	Schema            int       `json:"schema"`
	Action            string    `json:"action"`
	Context           string    `json:"context"`
	Repository        string    `json:"repository"`
	Snapshot          string    `json:"snapshot"`
	StartedAt         time.Time `json:"started_at"`
	SubmittedAccepted bool      `json:"submitted_accepted"`
	TargetIndices     []string  `json:"target_indices"`
}

// RestoreStatePath returns the file path for a given restore tuple.
// Distinct from CreateStatePath so a parallel `snapshot create` and
// `snapshot restore` against the same name don't stomp on each other's
// state — the prefix `restore_` keeps them separated under one
// directory.
func RestoreStatePath(contextName, repository, snapshot string) string {
	key := "restore_" + sanitiseStateKey(contextName) + "_" + sanitiseStateKey(repository) + "_" + sanitiseStateKey(snapshot) + ".json"
	return filepath.Join(StateDir(), key)
}

// SaveRestoreState writes the restore state file atomically. Mirrors
// SaveCreateState; the schema field is stamped from the same constant
// so a future schema bump applies to both.
func SaveRestoreState(state RestoreState) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	state.Schema = stateSchemaVersion
	final := RestoreStatePath(state.Context, state.Repository, state.Snapshot)
	tmp, err := os.CreateTemp(dir, ".restore-state-*.tmp")
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

// LoadRestoreState reads the restore state for a given key.
// ErrNoState is returned when the file doesn't exist — same sentinel
// as the create-state loader so callers can branch with one
// errors.Is check.
func LoadRestoreState(contextName, repository, snapshot string) (RestoreState, error) {
	path := RestoreStatePath(contextName, repository, snapshot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RestoreState{}, ErrNoState
		}
		return RestoreState{}, err
	}
	var state RestoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return RestoreState{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if state.Schema > stateSchemaVersion {
		return RestoreState{}, fmt.Errorf("state file %s has schema %d (this build understands %d); rm the file to recover", path, state.Schema, stateSchemaVersion)
	}
	return state, nil
}

// ClearRestoreState removes the restore state file. Errors other than
// "file already gone" propagate; the caller chooses whether to log or
// fail.
func ClearRestoreState(contextName, repository, snapshot string) error {
	path := RestoreStatePath(contextName, repository, snapshot)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CreateState is the on-disk record of an in-progress `snapshot create`.
// Persisted under $XDG_STATE_HOME/esops/snapshot/ so a SIGINT or crashed
// shell at hour 6 of an 8-hour snapshot doesn't strand the operator
// without a way to resume the wait or surface the eventual outcome.
//
// The file is named by the operator's request key (context + repository
// + snapshot name) so a second invocation of `snapshot create
// --resume` finds the right state. Multiple operators on different
// shells writing to the same key would race; the contract is one
// long-running create per (context, repo, name) tuple at a time, the
// same constraint the cluster itself imposes.
type CreateState struct {
	Schema     int       `json:"schema"`
	Action     string    `json:"action"`
	Context    string    `json:"context"`
	Repository string    `json:"repository"`
	Snapshot   string    `json:"snapshot"`
	StartedAt  time.Time `json:"started_at"`
	// SubmittedAccepted is true once the cluster has acked the create
	// request. A `--resume` finding a state with SubmittedAccepted=false
	// knows the original run died before the cluster saw the request,
	// and re-issues it; SubmittedAccepted=true means the cluster has the
	// snapshot in flight and `--resume` should poll, not re-submit.
	SubmittedAccepted bool `json:"submitted_accepted"`
}

// stateSchemaVersion is bumped if the on-disk format changes. A loader
// that sees a newer schema refuses with a clear error rather than
// silently misinterpreting the file — operators can `rm` the file to
// recover.
const stateSchemaVersion = 1

// StateDir returns the directory under which `snapshot create` state
// files live. Honours XDG_STATE_HOME (Linux convention), then HOME on
// macOS, falling back to os.TempDir() on stranger setups so the
// resume path never panics on a missing env. The directory is created
// lazily on the first SaveCreateState call.
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "esops", "snapshot")
	}
	if home, err := os.UserHomeDir(); err == nil {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", "esops", "snapshot")
		case "windows":
			if appData := os.Getenv("APPDATA"); appData != "" {
				return filepath.Join(appData, "esops", "snapshot")
			}
			return filepath.Join(home, ".esops", "state", "snapshot")
		default:
			return filepath.Join(home, ".local", "state", "esops", "snapshot")
		}
	}
	return filepath.Join(os.TempDir(), "esops", "snapshot")
}

// CreateStatePath returns the file path for a given (context, repo,
// snapshot) tuple. Spaces and path separators in the snapshot name are
// replaced so a name like `nightly-2026 04-30` doesn't escape the state
// directory.
func CreateStatePath(contextName, repository, snapshot string) string {
	key := sanitiseStateKey(contextName) + "_" + sanitiseStateKey(repository) + "_" + sanitiseStateKey(snapshot) + ".json"
	return filepath.Join(StateDir(), key)
}

func sanitiseStateKey(s string) string {
	if s == "" {
		s = "default"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == os.PathSeparator, r == '/', r == '\\', r == ':', r == ' ':
			return '_'
		default:
			return r
		}
	}, s)
}

// SaveCreateState writes the state file atomically (write to temp,
// rename) so a crashed write never leaves a partially-overwritten file
// the resume path would mis-parse.
func SaveCreateState(state CreateState) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	state.Schema = stateSchemaVersion
	final := CreateStatePath(state.Context, state.Repository, state.Snapshot)
	tmp, err := os.CreateTemp(dir, ".create-state-*.tmp")
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

// LoadCreateState reads the state file for a given key. ErrNoState is
// returned when the file doesn't exist — distinguishable from a parse
// error so the CLI can say "nothing to resume" vs "your state file is
// corrupt".
var ErrNoState = errors.New("no resume state found")

func LoadCreateState(contextName, repository, snapshot string) (CreateState, error) {
	path := CreateStatePath(contextName, repository, snapshot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CreateState{}, ErrNoState
		}
		return CreateState{}, err
	}
	var state CreateState
	if err := json.Unmarshal(data, &state); err != nil {
		return CreateState{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if state.Schema > stateSchemaVersion {
		return CreateState{}, fmt.Errorf("state file %s has schema %d (this build understands %d); rm the file to recover", path, state.Schema, stateSchemaVersion)
	}
	return state, nil
}

// ClearCreateState removes the state file. Errors other than "file
// already gone" propagate; the caller chooses whether to log or fail.
// Used both on successful completion (snapshot reached SUCCESS) and on
// explicit operator request.
func ClearCreateState(contextName, repository, snapshot string) error {
	path := CreateStatePath(contextName, repository, snapshot)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
