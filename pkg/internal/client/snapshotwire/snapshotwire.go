// Package snapshotwire holds the wire shapes and translation helpers the
// Elasticsearch and OpenSearch adapters share for the /_snapshot surface.
//
// Both products expose identical envelopes for repositories, snapshot
// entries, create/restore/verify responses; only the path-level routing
// (multi-repo lookup, query-param conventions) and the typed-client
// dependency differ. Centralising the wire shape here keeps the per-
// adapter code limited to "build the request, parse the response with
// these helpers" and makes a future field addition a one-file change.
//
// Settings on a SnapshotRepository are kept as map[string]any so the
// cluster's full plugin shape (s3 endpoint overrides, GCS credentials
// blocks, fs locations) round-trips without a translation table that
// would drift with new plugin versions. Failures on a snapshot entry
// are kept as []json.RawMessage so `--output json` consumers can drill
// into the cluster's reasons with jq instead of re-parsing JSON-encoded
// strings.
package snapshotwire

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/esops-dev/esops-go/pkg/types"
)

// RepoEntry mirrors a single value in the GET /_snapshot[/<repos>]
// response. The cluster keys the envelope by repository name; we decode
// the value side and surface Name from the key.
type RepoEntry struct {
	Type     string         `json:"type"`
	Settings map[string]any `json:"settings"`
}

// Entry mirrors one object inside the `snapshots` array of
// GET /_snapshot/<repo>/<pattern>. Both ES and OS share these fields;
// product-specific extras (slm policy on ES, remote_store_index_shallow_copy
// on OS) are not modeled — `--output json/yaml` consumers who need them
// fall back to the cluster's raw API.
type Entry struct {
	Snapshot           string            `json:"snapshot"`
	UUID               string            `json:"uuid"`
	State              string            `json:"state"`
	Indices            []string          `json:"indices"`
	IncludeGlobalState bool              `json:"include_global_state"`
	StartTimeMillis    int64             `json:"start_time_in_millis"`
	EndTimeMillis      int64             `json:"end_time_in_millis"`
	DurationMillis     int64             `json:"duration_in_millis"`
	Version            string            `json:"version"`
	Failures           []json.RawMessage `json:"failures"`
	Shards             struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
	} `json:"shards"`
}

// EntriesResponse is the envelope returned by the snapshot get endpoint.
type EntriesResponse struct {
	Snapshots []Entry `json:"snapshots"`
}

// CreateResponse mirrors PUT /_snapshot/<repo>/<snap>. Two shapes share
// one decoder: when wait_for_completion=true the cluster returns
// `{"snapshot":{<entry>}}`; when false it returns `{"accepted":true}`.
type CreateResponse struct {
	Accepted bool   `json:"accepted"`
	Snapshot *Entry `json:"snapshot"`
}

// VerifyResponse mirrors POST /_snapshot/<repo>/_verify. Same shape on
// both dialects.
type VerifyResponse struct {
	Nodes map[string]struct {
		Name string `json:"name"`
	} `json:"nodes"`
}

// RestoreResponse mirrors POST /_snapshot/<repo>/<snap>/_restore. When
// wait_for_completion=true the cluster echoes the post-rename index
// list and shard counts; when false only `{"accepted":true}`.
type RestoreResponse struct {
	Accepted bool `json:"accepted"`
	Snapshot *struct {
		Snapshot string   `json:"snapshot"`
		Indices  []string `json:"indices"`
		Shards   struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Failed     int `json:"failed"`
		} `json:"shards"`
	} `json:"snapshot"`
}

// DeleteResponse mirrors DELETE /_snapshot/<repo>/<snap[,snap…]>.
type DeleteResponse struct {
	Acknowledged bool `json:"acknowledged"`
}

// StatusResponse mirrors GET /_snapshot/<repo>/<snap>/_status. The
// cluster wraps a single snapshot in a `snapshots` array even when one
// name is requested; we surface the first entry (a missing snapshot
// returns a 404, handled at the adapter layer).
type StatusResponse struct {
	Snapshots []StatusEntry `json:"snapshots"`
}

// StatusEntry is one row inside the StatusResponse. `shards_stats`
// breaks the per-shard tally down into init/started/done/failed/total —
// exactly what a progress reporter wants. State follows the same
// vocabulary as the regular Snapshot record's State.
type StatusEntry struct {
	Snapshot           string `json:"snapshot"`
	Repository         string `json:"repository"`
	State              string `json:"state"`
	IncludeGlobalState bool   `json:"include_global_state"`
	ShardsStats        struct {
		Initializing int `json:"initializing"`
		Started      int `json:"started"`
		Finalizing   int `json:"finalizing"`
		Done         int `json:"done"`
		Failed       int `json:"failed"`
		Total        int `json:"total"`
	} `json:"shards_stats"`
	Stats struct {
		StartTimeMillis int64 `json:"start_time_in_millis"`
		TimeMillis      int64 `json:"time_in_millis"`
	} `json:"stats"`
}

// StatusFromWire flattens a single StatusEntry into the neutral
// SnapshotStatus.
func StatusFromWire(repo, name string, e StatusEntry) types.SnapshotStatus {
	return types.SnapshotStatus{
		Repository:         repo,
		Snapshot:           name,
		State:              e.State,
		IncludeGlobalState: e.IncludeGlobalState,
		StartTimeMillis:    e.Stats.StartTimeMillis,
		DurationMillis:     e.Stats.TimeMillis,
		ShardsTotal:        e.ShardsStats.Total,
		ShardsInit:         e.ShardsStats.Initializing,
		ShardsStarted:      e.ShardsStats.Started,
		// Cluster's `done` already excludes failed; we surface them as
		// separate counters so the operator sees both. `Finalizing` is
		// in-flight so it groups under started for the operator's
		// "in progress" mental model.
		ShardsDone:   e.ShardsStats.Done,
		ShardsFailed: e.ShardsStats.Failed,
	}
}

// AnalyzeResponse mirrors POST /_snapshot/<repo>/_analyze. ES 7.4+ and
// OS 2.x return a structured report; we surface the cluster's full
// shape verbatim so operators see the per-blob result detail without
// per-version decoder churn.
type AnalyzeResponse struct {
	Coordinating struct {
		NodeID   string `json:"id"`
		NodeName string `json:"name"`
	} `json:"coordinating_node"`
	BlobCount         int    `json:"blob_count"`
	ConcurrencyLevels int    `json:"concurrency"`
	ReadNodeCount     int    `json:"read_node_count"`
	EarlyReadNodes    int    `json:"early_read_node_count"`
	Status            string `json:"status"`
}

// SnapshotFromWire converts the wire shape into the neutral type. The
// Repository field is populated from the caller because the cluster's
// per-snapshot record only carries the snapshot name — the repo it
// lives in is implicit in the request path.
func SnapshotFromWire(repo string, e Entry) types.Snapshot {
	out := types.Snapshot{
		Repository:         repo,
		Name:               e.Snapshot,
		UUID:               e.UUID,
		State:              e.State,
		Indices:            e.Indices,
		IncludeGlobalState: e.IncludeGlobalState,
		StartTimeMillis:    e.StartTimeMillis,
		EndTimeMillis:      e.EndTimeMillis,
		DurationMillis:     e.DurationMillis,
		ShardsTotal:        e.Shards.Total,
		ShardsSuccessful:   e.Shards.Successful,
		ShardsFailed:       e.Shards.Failed,
		Version:            e.Version,
	}
	if len(e.Failures) > 0 {
		// Copy so a later append by the cluster decoder doesn't mutate
		// the neutral type's slice.
		failures := make([]json.RawMessage, len(e.Failures))
		copy(failures, e.Failures)
		out.Failures = failures
	}
	return out
}

// CreateResultFromWire builds the neutral result. When the cluster echoes
// a `snapshot` block (wait_for_completion=true completed, or some
// versions even on the async path) we populate the full Snapshot record
// and treat that as acknowledgement. Otherwise the Snapshot is a
// placeholder so a captured JSON record stays interpretable on its own.
func CreateResultFromWire(req types.SnapshotCreateRequest, wire CreateResponse) types.SnapshotCreateResult {
	out := types.SnapshotCreateResult{
		Action:            "create",
		Repository:        req.Repository,
		Name:              req.Snapshot,
		WaitForCompletion: req.WaitForCompletion,
		Accepted:          wire.Accepted,
	}
	switch {
	case wire.Snapshot != nil:
		out.Snapshot = SnapshotFromWire(req.Repository, *wire.Snapshot)
		out.Accepted = true
	case req.WaitForCompletion:
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot}
	default:
		out.Snapshot = types.Snapshot{Repository: req.Repository, Name: req.Snapshot, State: "STARTED"}
	}
	return out
}

// RestoreResultFromWire mirrors CreateResultFromWire for the restore
// surface. The synchronous path carries the post-rename index list and
// shard counts; the async path leaves the operator to poll list /
// _recovery for progress.
func RestoreResultFromWire(req types.SnapshotRestoreRequest, wire RestoreResponse) types.SnapshotRestoreResult {
	out := types.SnapshotRestoreResult{
		Action:            "restore",
		Repository:        req.Repository,
		Name:              req.Snapshot,
		WaitForCompletion: req.WaitForCompletion,
		Accepted:          wire.Accepted,
		RenamePattern:     req.RenamePattern,
		RenameReplacement: req.RenameReplacement,
	}
	if wire.Snapshot != nil {
		out.Snapshot = types.SnapshotRestoreInfo{
			Snapshot:         wire.Snapshot.Snapshot,
			Indices:          wire.Snapshot.Indices,
			ShardsTotal:      wire.Snapshot.Shards.Total,
			ShardsSuccessful: wire.Snapshot.Shards.Successful,
			ShardsFailed:     wire.Snapshot.Shards.Failed,
		}
		out.Accepted = true
	} else {
		out.Snapshot = types.SnapshotRestoreInfo{Snapshot: req.Snapshot}
	}
	return out
}

// VerifyResultFromWire flattens the cluster's nodes map into two parallel
// slices sorted by node id for deterministic output across runs.
func VerifyResultFromWire(repo string, wire VerifyResponse) types.SnapshotVerifyResult {
	ids := make([]string, 0, len(wire.Nodes))
	for id := range wire.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, wire.Nodes[id].Name)
	}
	return types.SnapshotVerifyResult{
		Action:        "verify",
		Repository:    repo,
		VerifiedNodes: ids,
		Names:         names,
	}
}

// BuildCreateBody renders the PUT /_snapshot/<repo>/<snap> body. Empty
// fields are omitted entirely so the cluster falls back to its own
// defaults — encoding `"indices":""` would over-specify the request and
// risks drift if a future cluster version interprets the empty pattern
// differently from an absent field.
func BuildCreateBody(req types.SnapshotCreateRequest) ([]byte, error) {
	body := make(map[string]any)
	if indices := strings.TrimSpace(req.Indices); indices != "" {
		body["indices"] = indices
	}
	if req.IncludeGlobalState != nil {
		body["include_global_state"] = *req.IncludeGlobalState
	}
	if req.Partial != nil {
		body["partial"] = *req.Partial
	}
	if req.IgnoreUnavailable != nil {
		body["ignore_unavailable"] = *req.IgnoreUnavailable
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	return json.Marshal(body)
}

// BuildRestoreBody renders the POST /_snapshot/<repo>/<snap>/_restore
// body. Mirrors BuildCreateBody — empty fields are omitted so cluster
// defaults apply, and the rename pair is forwarded as-is (the business
// layer already enforces the all-or-nothing pairing).
func BuildRestoreBody(req types.SnapshotRestoreRequest) ([]byte, error) {
	body := make(map[string]any)
	if indices := strings.TrimSpace(req.Indices); indices != "" {
		body["indices"] = indices
	}
	if req.IncludeGlobalState != nil {
		body["include_global_state"] = *req.IncludeGlobalState
	}
	if req.Partial != nil {
		body["partial"] = *req.Partial
	}
	if req.IgnoreUnavailable != nil {
		body["ignore_unavailable"] = *req.IgnoreUnavailable
	}
	if req.RenamePattern != "" {
		body["rename_pattern"] = req.RenamePattern
	}
	if req.RenameReplacement != "" {
		body["rename_replacement"] = req.RenameReplacement
	}
	if len(req.IndexSettings) > 0 {
		body["index_settings"] = req.IndexSettings
	}
	if len(req.IgnoreIndexSettings) > 0 {
		body["ignore_index_settings"] = req.IgnoreIndexSettings
	}
	return json.Marshal(body)
}

// RepositoriesFromWire flattens the cluster's name-keyed envelope into a
// sorted slice of neutral SnapshotRepository records. The result is
// stable across runs so a captured JSON record diffs cleanly.
func RepositoriesFromWire(wire map[string]RepoEntry) []types.SnapshotRepository {
	out := make([]types.SnapshotRepository, 0, len(wire))
	for name, r := range wire {
		out = append(out, types.SnapshotRepository{
			Name:     name,
			Type:     r.Type,
			Settings: r.Settings,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
