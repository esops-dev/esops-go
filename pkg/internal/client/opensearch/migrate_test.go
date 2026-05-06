package opensearch

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Mirror of the elasticsearch adapter's migrate_test.go — the helpers
// are duplicated across adapters by design (per the parse_test.go
// header in this package) and we want both copies to exhibit the same
// edge-case behaviour. A future refactor that unifies them should
// preserve every assertion below.

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRemoteWhitelist(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

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
			if got := decodeDynamic(raw); got != tc.want {
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
			got := splitTrim(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
