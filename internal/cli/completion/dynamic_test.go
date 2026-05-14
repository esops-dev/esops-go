package completion

import (
	"bytes"
	"context"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestPreviousFlagIn_completingFlagValue(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "empty value for long flag",
			argv: []string{"esops", "--context", completionFlag},
			want: "context",
		},
		{
			name: "value-with-equals splits on =",
			argv: []string{"esops", "--context=loc", completionFlag},
			want: "context",
		},
		{
			name: "short alias is honoured",
			argv: []string{"esops", "-c", completionFlag},
			want: "c",
		},
		{
			name: "deep command flag",
			argv: []string{"esops", "index", "list", "--index", completionFlag},
			want: "index",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previousFlagIn(tc.argv); got != tc.want {
				t.Errorf("previousFlagIn(%v) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

func TestPreviousFlagIn_notInFlagPosition(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"no completion sentinel", []string{"esops", "index", "list"}},
		{"sentinel without preceding flag", []string{"esops", completionFlag}},
		{"previous arg is positional", []string{"esops", "index", "list", "foo", completionFlag}},
		{"empty argv", []string{}},
		{"just program name", []string{"esops"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previousFlagIn(tc.argv); got != "" {
				t.Errorf("previousFlagIn(%v) = %q, want empty", tc.argv, got)
			}
		})
	}
}

func TestForFlags_unmatchedFlagFallsBackToDefault(t *testing.T) {
	// Build a stub root command. We don't actually trigger the
	// completion machinery; we just confirm ForFlags returns a
	// callable that does the right thing when invoked directly.
	var buf bytes.Buffer
	cmd := &cli.Command{
		Name:   "esops",
		Writer: &buf,
	}

	called := false
	suggester := func(_ context.Context, _ *cli.Command) []string {
		called = true
		return []string{"never"}
	}
	fn := ForFlags(map[string]Suggester{"context": suggester})

	// previousFlagIn() reads os.Args; since the test process didn't
	// invoke completion, no flag is "previous" — fn should fall
	// back to DefaultCompleteWithFlags rather than calling our
	// suggester.
	fn(context.Background(), cmd)
	if called {
		t.Error("suggester ran for an unrelated invocation; expected fallback")
	}
}
