package snapshot

import (
	"strings"
	"testing"
)

func TestParseIndexSettingFlags(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]any
		wantErr string
	}{
		{
			name: "single setting",
			in:   []string{"index.number_of_replicas=0"},
			want: map[string]any{"index.number_of_replicas": "0"},
		},
		{
			name: "multiple settings preserve all",
			in:   []string{"index.number_of_replicas=0", "index.refresh_interval=5s"},
			want: map[string]any{"index.number_of_replicas": "0", "index.refresh_interval": "5s"},
		},
		{
			name: "value containing equals",
			in:   []string{"index.routing.allocation.require._name=node-a=primary"},
			want: map[string]any{"index.routing.allocation.require._name": "node-a=primary"},
		},
		{
			name: "empty input is nil",
			in:   nil,
			want: nil,
		},
		{
			name:    "missing equals rejected",
			in:      []string{"oops"},
			wantErr: "KEY=VALUE",
		},
		{
			name:    "empty key rejected",
			in:      []string{"=value"},
			wantErr: "KEY=VALUE",
		},
		{
			name:    "duplicate key rejected",
			in:      []string{"index.number_of_replicas=0", "index.number_of_replicas=1"},
			wantErr: "more than once",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseIndexSettingFlags(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("err = %v, want substring %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(c.want), got)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("got[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
