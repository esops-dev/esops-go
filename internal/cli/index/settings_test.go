package index

import (
	"strings"
	"testing"
)

func TestParseKeyValuePairs(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr string
	}{
		{
			name: "single pair",
			in:   []string{"number_of_replicas=2"},
			want: map[string]string{"number_of_replicas": "2"},
		},
		{
			name: "multiple pairs preserve all",
			in:   []string{"a=1", "b=2"},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "value containing equals",
			in:   []string{"q=name=foo"},
			want: map[string]string{"q": "name=foo"},
		},
		{
			name: "empty input is nil",
			in:   nil,
			want: nil,
		},
		{
			name:    "missing equals rejected",
			in:      []string{"oops"},
			wantErr: "key=value",
		},
		{
			name:    "empty key rejected",
			in:      []string{"=value"},
			wantErr: "key=value",
		},
		{
			name:    "duplicate raw key rejected",
			in:      []string{"a=1", "a=2"},
			wantErr: "given twice",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseKeyValuePairs(c.in)
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
					t.Errorf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestResolveBody_mutuallyExclusive(t *testing.T) {
	_, err := resolveBody("{}", "/some/file")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually-exclusive error, got: %v", err)
	}
}

func TestResolveBody_emptyReturnsNil(t *testing.T) {
	got, err := resolveBody("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got %q, want nil", string(got))
	}
}

func TestResolveBody_literal(t *testing.T) {
	got, err := resolveBody(`{"x":1}`, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `{"x":1}` {
		t.Errorf("got %q", string(got))
	}
}
