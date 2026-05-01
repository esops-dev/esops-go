package snapshot

import (
	"strings"
	"testing"
)

func TestParseMetadataFlags(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]any
		wantErr string
	}{
		{
			name: "single pair",
			in:   []string{"reason=pre-upgrade"},
			want: map[string]any{"reason": "pre-upgrade"},
		},
		{
			name: "multiple pairs preserve all",
			in:   []string{"reason=pre-upgrade", "ticket=OPS-1234"},
			want: map[string]any{"reason": "pre-upgrade", "ticket": "OPS-1234"},
		},
		{
			name: "value containing equals",
			in:   []string{"query=name=foo"},
			want: map[string]any{"query": "name=foo"},
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
			in:      []string{"a=1", "a=2"},
			wantErr: "more than once",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseMetadataFlags(c.in)
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
