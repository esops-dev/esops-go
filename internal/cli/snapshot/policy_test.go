package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePolicyBody_literal(t *testing.T) {
	got, err := resolvePolicyBody(`{"schedule":"0 0 * * * ?"}`, "")
	if err != nil {
		t.Fatalf("resolvePolicyBody: %v", err)
	}
	if !strings.Contains(string(got), "schedule") {
		t.Errorf("body = %q, want literal pass-through", got)
	}
}

func TestResolvePolicyBody_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, []byte(`{"schedule":"0 0 * * * ?"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolvePolicyBody("", path)
	if err != nil {
		t.Fatalf("resolvePolicyBody: %v", err)
	}
	if !strings.Contains(string(got), "schedule") {
		t.Errorf("body = %q, want file contents", got)
	}
}

func TestResolvePolicyBody_bothRejected(t *testing.T) {
	_, err := resolvePolicyBody(`{}`, "/some/path")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want mutually-exclusive error", err)
	}
}

func TestResolvePolicyBody_neitherRejected(t *testing.T) {
	_, err := resolvePolicyBody("", "")
	if err == nil || !strings.Contains(err.Error(), "policy body is required") {
		t.Errorf("err = %v, want 'policy body is required'", err)
	}
}

func TestResolvePolicyBody_missingFile(t *testing.T) {
	_, err := resolvePolicyBody("", "/no/such/path/policy.json")
	if err == nil || !strings.Contains(err.Error(), "reading --from-file") {
		t.Errorf("err = %v, want 'reading --from-file' wrap", err)
	}
}

func TestParseKeyValueSettings(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]any
		wantErr string
	}{
		{
			name: "single",
			in:   []string{"bucket=my-backups"},
			want: map[string]any{"bucket": "my-backups"},
		},
		{
			name: "multiple",
			in:   []string{"bucket=my-backups", "region=eu-west-1"},
			want: map[string]any{"bucket": "my-backups", "region": "eu-west-1"},
		},
		{
			name: "value with equals",
			in:   []string{"role=arn:aws:iam::1234:role/x=y"},
			want: map[string]any{"role": "arn:aws:iam::1234:role/x=y"},
		},
		{
			name:    "missing equals rejected",
			in:      []string{"oops"},
			wantErr: "KEY=VALUE",
		},
		{
			name:    "empty key rejected",
			in:      []string{"=v"},
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
			got, err := parseKeyValueSettings(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("err = %v, want substring %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeyValueSettings: %v", err)
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
