package elasticsearch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/esops-dev/esops-go/pkg/types"
)

// readBoolSetting tolerates the cluster reporting bools as real
// JSON booleans or the strings "true"/"false" depending on which
// subsystem set the key. Both shapes are real-world: a key set via
// `_cluster/settings` arrives as a string while one parsed from
// elasticsearch.yml may pass through as a real bool.
func TestReadBoolSetting(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		key  string
		want bool
	}{
		{name: "nil map", in: nil, key: "x", want: false},
		{name: "absent key", in: map[string]any{"y": true}, key: "x", want: false},
		{name: "real bool true", in: map[string]any{"x": true}, key: "x", want: true},
		{name: "real bool false", in: map[string]any{"x": false}, key: "x", want: false},
		{name: "string true", in: map[string]any{"x": "true"}, key: "x", want: true},
		{name: "string false", in: map[string]any{"x": "false"}, key: "x", want: false},
		{name: "string TRUE", in: map[string]any{"x": "TRUE"}, key: "x", want: true},
		{name: "non-bool string", in: map[string]any{"x": "yes"}, key: "x", want: false},
		{name: "wrong type", in: map[string]any{"x": 42}, key: "x", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readBoolSetting(tc.in, tc.key); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadStringSetting(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		key  string
		want string
	}{
		{name: "nil map", in: nil, key: "x", want: ""},
		{name: "absent key", in: map[string]any{"y": "z"}, key: "x", want: ""},
		{name: "string value", in: map[string]any{"x": "full"}, key: "x", want: "full"},
		{name: "non-string value", in: map[string]any{"x": true}, key: "x", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readStringSetting(tc.in, tc.key); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// types.NodeBootstrap, NodeAllocation, ClusterSettingsFull, and
// TransportTLS are part of the public pkg/types surface that
// esops-doctor reads via json.Marshal. The tags are load-bearing —
// doctor's CEL evaluates against the marshalled JSON keys, and a
// silent rename here would break a downstream consumer with no
// compile error. Pin the tags as a unit test so the rename surfaces
// loudly during PR review.
func TestPublicTypeJSONTagsArePinned(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want map[string]json.RawMessage
	}{
		{
			name: "NodeBootstrap",
			in: types.NodeBootstrap{
				Name:               "es01",
				MlockallEnabled:    true,
				MaxFileDescriptors: 65535,
				MaxMapCount:        262144,
				BootstrapWarnings:  []string{"max_map_count"},
			},
			want: map[string]json.RawMessage{
				"name":                 json.RawMessage(`"es01"`),
				"mlockall_enabled":     json.RawMessage(`true`),
				"max_file_descriptors": json.RawMessage(`65535`),
				"max_map_count":        json.RawMessage(`262144`),
				"bootstrap_warnings":   json.RawMessage(`["max_map_count"]`),
			},
		},
		{
			name: "NodeAllocation",
			in: types.NodeAllocation{
				Name:             "es01",
				Shards:           42,
				DiskIndicesBytes: 1024,
				DiskUsedBytes:    2048,
				DiskAvailBytes:   4096,
				DiskTotalBytes:   8192,
				DiskPercent:      25,
				Host:             "host",
				IP:               "10.0.0.1",
			},
			want: map[string]json.RawMessage{
				"name":               json.RawMessage(`"es01"`),
				"shards":             json.RawMessage(`42`),
				"disk_indices_bytes": json.RawMessage(`1024`),
				"disk_used_bytes":    json.RawMessage(`2048`),
				"disk_avail_bytes":   json.RawMessage(`4096`),
				"disk_total_bytes":   json.RawMessage(`8192`),
				"disk_percent":       json.RawMessage(`25`),
				"host":               json.RawMessage(`"host"`),
				"ip":                 json.RawMessage(`"10.0.0.1"`),
			},
		},
		{
			name: "TransportTLS",
			in:   types.TransportTLS{Enabled: true, Verified: true},
			want: map[string]json.RawMessage{
				"transport_tls_enabled":  json.RawMessage(`true`),
				"transport_tls_verified": json.RawMessage(`true`),
			},
		},
		{
			name: "IndexCacheClearResult",
			in: types.IndexCacheClearResult{
				Action:     "cache-clear",
				DryRun:     false,
				Indices:    []string{"logs-*"},
				Caches:     []string{"fielddata"},
				Fields:     []string{"title"},
				Total:      4,
				Successful: 3,
				Failed:     1,
			},
			want: map[string]json.RawMessage{
				"action":            json.RawMessage(`"cache-clear"`),
				"dry_run":           json.RawMessage(`false`),
				"indices":           json.RawMessage(`["logs-*"]`),
				"caches":            json.RawMessage(`["fielddata"]`),
				"fields":            json.RawMessage(`["title"]`),
				"shards_total":      json.RawMessage(`4`),
				"shards_successful": json.RawMessage(`3`),
				"shards_failed":     json.RawMessage(`1`),
			},
		},
		{
			name: "IndexUnblockResult",
			in: types.IndexUnblockResult{
				Action:       "unblock",
				DryRun:       false,
				Indices:      []string{"logs-2024"},
				Block:        "read_only_allow_delete",
				Acknowledged: true,
			},
			want: map[string]json.RawMessage{
				"action":       json.RawMessage(`"unblock"`),
				"dry_run":      json.RawMessage(`false`),
				"indices":      json.RawMessage(`["logs-2024"]`),
				"block":        json.RawMessage(`"read_only_allow_delete"`),
				"acknowledged": json.RawMessage(`true`),
			},
		},
		{
			name: "ClusterSettingsPutResult",
			in: types.ClusterSettingsPutResult{
				Acknowledged: true,
				Persistent:   map[string]any{"cluster.foo": "bar"},
				Transient:    map[string]any{},
			},
			want: map[string]json.RawMessage{
				"acknowledged": json.RawMessage(`true`),
				"persistent":   json.RawMessage(`{"cluster.foo":"bar"}`),
				"transient":    json.RawMessage(`{}`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tags drifted:\n  got:  %s\n  want: %#v", string(b), tc.want)
			}
		})
	}
}

// TLSPosture's two new transport_* fields are omitempty: the existing
// callers (internal/ops.Audit) populate only the client-side fields,
// and a silent change to omitempty would inject "transport_tls_enabled":
// false into every audit's JSON output even on contexts that never
// asked the cluster — a real regression for downstream JSON consumers.
func TestTLSPostureTransportFieldsOmitEmpty(t *testing.T) {
	posture := types.TLSPosture{
		Scheme:        "https",
		AuthType:      "basic",
		ClientCertSet: false,
	}
	b, err := json.Marshal(posture)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["transport_tls_enabled"]; present {
		t.Errorf("transport_tls_enabled should be omitted when zero, payload=%s", string(b))
	}
	if _, present := got["transport_tls_verified"]; present {
		t.Errorf("transport_tls_verified should be omitted when zero, payload=%s", string(b))
	}
}

// SecurityUser.PasswordChangedAt is also omitempty: the field is
// populated only when the cluster surfaces it, and existing audit
// JSON output (consumed by scripts) must not start carrying an empty
// string for users where the cluster never reported the field.
func TestSecurityUserPasswordChangedAtOmitEmpty(t *testing.T) {
	u := types.SecurityUser{Username: "elastic", Enabled: true}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := got["password_changed_at"]; present {
		t.Errorf("password_changed_at should be omitted when empty, payload=%s", string(b))
	}
}
