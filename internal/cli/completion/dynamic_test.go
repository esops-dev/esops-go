package completion

import (
	"bytes"
	"context"
	"os"
	"strings"
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

func TestCompletingFlagName(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"no sentinel", []string{"esops", "describe", "lo"}, false},
		{"positional being typed (excluded by template)", []string{"esops", "describe", completionFlag}, false},
		{"completed positional", []string{"esops", "describe", "foo", completionFlag}, false},
		{"single dash", []string{"esops", "describe", "-", completionFlag}, true},
		{"double dash", []string{"esops", "describe", "--", completionFlag}, true},
		{"long flag prefix", []string{"esops", "describe", "--inc", completionFlag}, true},
		{"short flag", []string{"esops", "describe", "-c", completionFlag}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := completingFlagName(tc.argv); got != tc.want {
				t.Errorf("completingFlagName(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// primedCommand builds a cli.Command and runs it once with the given
// args so cmd.NArg() / cmd.Args() are populated. urfave/cli's Args()
// method nil-derefs on an un-Run command, so any test exercising the
// positional-count branch must prime via Run first. The Action is a
// no-op — we only care about parsed state after Run returns.
func primedCommand(t *testing.T, w *bytes.Buffer, runArgs []string) *cli.Command {
	t.Helper()
	cmd := &cli.Command{
		Name:   "describe",
		Writer: w,
		Action: func(_ context.Context, _ *cli.Command) error { return nil },
	}
	if err := cmd.Run(context.Background(), runArgs); err != nil {
		t.Fatalf("priming Run(%v): %v", runArgs, err)
	}
	return cmd
}

func TestForPositional_emitsSuggestionsAndDedupes(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	// Simulate `esops describe <TAB>` — no positional typed, no
	// trailing flag-name candidate.
	os.Args = []string{"esops", "describe", completionFlag}

	var buf bytes.Buffer
	cmd := primedCommand(t, &buf, []string{"describe"})

	a := func(_ context.Context, _ *cli.Command) []string { return []string{"logs", "users"} }
	b := func(_ context.Context, _ *cli.Command) []string { return []string{"users", "billing"} } // dup

	fn := ForPositional(1, a, b)
	fn(context.Background(), cmd)

	got := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{"logs", "users", "billing"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d = %q, want %q", i, got[i], w)
		}
	}
}

func TestForPositional_silentOnFlagNameCompletion(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"esops", "describe", "--", completionFlag}

	var buf bytes.Buffer
	cmd := primedCommand(t, &buf, []string{"describe"})

	called := false
	sug := func(_ context.Context, _ *cli.Command) []string {
		called = true
		return []string{"never"}
	}
	fn := ForPositional(1, sug)
	fn(context.Background(), cmd)

	if called {
		t.Error("positional suggester ran while operator was completing a flag name")
	}
}

func TestForPositional_silentAfterMaxPositionalReached(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"esops", "describe", "foo", completionFlag}

	var buf bytes.Buffer
	// One positional consumed.
	cmd := primedCommand(t, &buf, []string{"describe", "foo"})

	called := false
	sug := func(_ context.Context, _ *cli.Command) []string {
		called = true
		return []string{"never"}
	}
	fn := ForPositional(1, sug)
	fn(context.Background(), cmd)

	if called {
		t.Error("positional suggester ran after the command's positional contract was already satisfied")
	}
}

func TestForPositional_silentInFlagValuePosition(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	// `--context <TAB>`: previous arg is a flag name; operator is in
	// flag-value position.
	os.Args = []string{"esops", "describe", "--context", completionFlag}

	var buf bytes.Buffer
	cmd := primedCommand(t, &buf, []string{"describe"})

	called := false
	sug := func(_ context.Context, _ *cli.Command) []string {
		called = true
		return []string{"never"}
	}
	fn := ForPositional(1, sug)
	fn(context.Background(), cmd)

	if called {
		t.Error("positional suggester ran in flag-value position")
	}
}
