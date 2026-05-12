package reindexwire

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// ParseRemoteWhitelist accepts both wire shapes the cluster emits — a
// comma-separated string when set via the shorthand, a JSON array when
// set via the array form — plus a few real-world hand-edited values.
// These cases are the ones a live cluster will not exercise on every
// run, so the unit test is what catches a future regression.
func TestParseRemoteWhitelist(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want []string
	}{
		{name: "nil map", in: nil, want: nil},
		{name: "key absent", in: map[string]any{"unrelated": "x"}, want: nil},
		{name: "empty string", in: map[string]any{"reindex.remote.whitelist": ""}, want: nil},
		{
			name: "single literal",
			in:   map[string]any{"reindex.remote.whitelist": "host:9200"},
			want: []string{"host:9200"},
		},
		{
			name: "comma-separated string with stray spaces",
			in:   map[string]any{"reindex.remote.whitelist": " host1:9200 , host2:9200 ,, "},
			want: []string{"host1:9200", "host2:9200"},
		},
		{
			name: "json array",
			in:   map[string]any{"reindex.remote.whitelist": []any{"a:9200", "b:9200"}},
			want: []string{"a:9200", "b:9200"},
		},
		{
			name: "json array with empty strings dropped",
			in:   map[string]any{"reindex.remote.whitelist": []any{"a:9200", "", " "}},
			want: []string{"a:9200"},
		},
		{
			name: "empty array",
			in:   map[string]any{"reindex.remote.whitelist": []any{}},
			want: nil,
		},
		{
			name: "wrong type returns nil",
			in:   map[string]any{"reindex.remote.whitelist": 42},
			want: nil,
		},
		{
			name: "opensearch allowlist key string",
			in:   map[string]any{"reindex.remote.allowlist": "host:9200"},
			want: []string{"host:9200"},
		},
		{
			name: "opensearch allowlist key json array",
			in:   map[string]any{"reindex.remote.allowlist": []any{"a:9200", "b:9200"}},
			want: []string{"a:9200", "b:9200"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRemoteWhitelist(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// DecodeDynamic flattens the cluster's `dynamic` field, which arrives
// as either a JSON string or a JSON boolean depending on how the
// operator set the index template. Both shapes must collapse to the
// same string representation.
func TestDecodeDynamic(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "absent", raw: "", want: ""},
		{name: "json null", raw: "null", want: ""},
		{name: "string strict", raw: `"strict"`, want: "strict"},
		{name: "string runtime", raw: `"runtime"`, want: "runtime"},
		{name: "bool true", raw: "true", want: "true"},
		{name: "bool false", raw: "false", want: "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := DecodeDynamic(raw); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSplitTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: ",", want: nil},
		{in: " , ", want: nil},
		{in: "a", want: []string{"a"}},
		{in: "a,b", want: []string{"a", "b"}},
		{in: "  a , b  ", want: []string{"a", "b"}},
		{in: "a,,b", want: []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SplitTrim(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNormaliseRPS(t *testing.T) {
	cases := []struct {
		in   float64
		want int
		ok   bool
	}{
		{in: -1, want: -1, ok: true},
		{in: -42, want: -1, ok: true},
		{in: 0, want: 0, ok: false},
		{in: 1, want: 1, ok: true},
		{in: 1500.7, want: 1500, ok: true},
	}
	for _, tc := range cases {
		got, ok := NormaliseRPS(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormaliseRPS(%v) = (%d,%v); want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBuildReindexBody_RequiresSchemeOnRemoteHost(t *testing.T) {
	_, err := BuildReindexBody(types.ReindexSubmitRequest{
		SourceIndex:      "src",
		TargetIndex:      "tgt",
		SourceRemoteHost: "host:9200",
	})
	if !errors.Is(err, ErrSchemeRequired) {
		t.Fatalf("expected ErrSchemeRequired, got %v", err)
	}
}

func TestBuildReindexBody_LocalReindexNoRemoteBlock(t *testing.T) {
	body, err := BuildReindexBody(types.ReindexSubmitRequest{
		SourceIndex: "src",
		TargetIndex: "tgt",
	})
	if err != nil {
		t.Fatalf("BuildReindexBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	src, ok := got["source"].(map[string]any)
	if !ok {
		t.Fatalf("expected source object, got %T", got["source"])
	}
	if _, has := src["remote"]; has {
		t.Fatalf("local reindex must not include remote block; body=%s", body)
	}
}

func TestBuildReindexBody_RemoteCarriesAuthAndHeaders(t *testing.T) {
	body, err := BuildReindexBody(types.ReindexSubmitRequest{
		SourceIndex:      "src",
		TargetIndex:      "tgt",
		SourceRemoteHost: "https://es-src:9200",
		SourceUsername:   "u",
		SourcePassword:   "p",
		SourceHeaders:    map[string]string{"Authorization": "Bearer t"},
		Conflicts:        "proceed",
		SizeLimit:        10,
	})
	if err != nil {
		t.Fatalf("BuildReindexBody: %v", err)
	}
	var got struct {
		Source struct {
			Index  string `json:"index"`
			Remote struct {
				Host     string            `json:"host"`
				Username string            `json:"username"`
				Password string            `json:"password"`
				Headers  map[string]string `json:"headers"`
			} `json:"remote"`
		} `json:"source"`
		Dest struct {
			Index string `json:"index"`
		} `json:"dest"`
		Conflicts string `json:"conflicts"`
		MaxDocs   int64  `json:"max_docs"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got.Source.Index != "src" || got.Dest.Index != "tgt" {
		t.Errorf("source/dest index mismatch: %+v", got)
	}
	if got.Source.Remote.Host != "https://es-src:9200" || got.Source.Remote.Username != "u" || got.Source.Remote.Password != "p" {
		t.Errorf("remote block mismatch: %+v", got.Source.Remote)
	}
	if got.Source.Remote.Headers["Authorization"] != "Bearer t" {
		t.Errorf("Authorization header dropped: %+v", got.Source.Remote.Headers)
	}
	if got.Conflicts != "proceed" || got.MaxDocs != 10 {
		t.Errorf("conflicts/max_docs mismatch: conflicts=%q max_docs=%d", got.Conflicts, got.MaxDocs)
	}
}

func TestTaskWire_ToTaskStatus_FoldsErrorIntoFailures(t *testing.T) {
	wire := TaskWire{
		Completed: true,
	}
	wire.Task.Status.Total = 100
	wire.Task.Status.Created = 100
	wire.Error = &struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}{Type: "search_phase_execution", Reason: "no shards"}

	got := wire.ToTaskStatus("node:42")
	if got.TaskID != "node:42" || !got.Completed || got.Total != 100 {
		t.Fatalf("envelope mismatch: %+v", got)
	}
	if len(got.Failures) != 1 || got.Failures[0] != "task error: search_phase_execution: no shards" {
		t.Errorf("failures not folded: %+v", got.Failures)
	}
}

func TestFormatFailures_HandlesShardAndDocShapes(t *testing.T) {
	docFailure := json.RawMessage(`{"index":"logs","id":"42","cause":{"type":"version_conflict_engine_exception","reason":"already exists"}}`)
	shardFailure := json.RawMessage(`{"index":"logs","shard":3,"reason":{"type":"unassigned_shard_exception","reason":"all shards failed"}}`)
	bogus := json.RawMessage(`not-json`)

	got := FormatFailures([]json.RawMessage{docFailure, shardFailure, bogus})
	want := []string{
		"logs[42]: version_conflict_engine_exception: already exists",
		"logs[shard 3]: unassigned_shard_exception: all shards failed",
		"not-json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FormatFailures mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCountWire_ToCountResult(t *testing.T) {
	in := CountWire{Count: 12345}
	in.Shards.Total = 5
	in.Shards.Successful = 5
	in.Shards.Failed = 0
	in.Shards.Skipped = 0
	got := in.ToCountResult()
	if got.Count != 12345 || got.Shards.Total != 5 || got.Shards.Successful != 5 {
		t.Errorf("ToCountResult mismatch: %+v", got)
	}
}
