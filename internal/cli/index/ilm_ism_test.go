package index

import (
	"testing"

	"github.com/urfave/cli/v3"
)

// TestIndexCommandRegistersILMAndISM is a tree-shape sanity check: the
// `index` parent must expose both `ilm` and `ism` subcommands so script
// users get a stable path. A future refactor that drops one of them
// would silently break recipes in the wild without this guard.
func TestIndexCommandRegistersILMAndISM(t *testing.T) {
	root := Command()
	want := map[string]bool{"ilm": false, "ism": false}
	for _, sub := range root.Commands {
		if _, ok := want[sub.Name]; ok {
			want[sub.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("index command tree is missing %q", name)
		}
	}
}

// TestILMSubcommandsRegistered locks down the `index ilm` subtree so a
// future drift (renaming get → fetch, dropping explain) is caught here
// instead of in a user's CI pipeline.
func TestILMSubcommandsRegistered(t *testing.T) {
	want := []string{"list", "get", "put", "delete", "explain"}
	got := subNames(ilmCommand())
	for _, name := range want {
		if !containsName(got, name) {
			t.Errorf("ilm subcommand %q missing; have %v", name, got)
		}
	}
}

// TestISMSubcommandsRegistered mirrors the ILM check. The two trees are
// deliberately parallel — same operator-facing question, dialect-
// specific implementation.
func TestISMSubcommandsRegistered(t *testing.T) {
	want := []string{"list", "get", "put", "delete", "explain"}
	got := subNames(ismCommand())
	for _, name := range want {
		if !containsName(got, name) {
			t.Errorf("ism subcommand %q missing; have %v", name, got)
		}
	}
}

// TestILMPutFlags verifies the destructive subcommand exposes the body-
// input pair operators expect. A regression that drops --from-file
// would push every CI pipeline to inline a multi-kilobyte JSON literal.
func TestILMPutFlags(t *testing.T) {
	cmd := findSub(ilmCommand(), "put")
	if cmd == nil {
		t.Fatal("ilm put subcommand not registered")
	}
	flags := flagNamesOf(cmd)
	for _, want := range []string{"name", "body", "from-file"} {
		if !containsName(flags, want) {
			t.Errorf("ilm put missing --%s flag; have %v", want, flags)
		}
	}
}

// TestISMPutFlags additionally verifies the optimistic-concurrency
// pair, which is OS-specific and easy to drop accidentally on a refactor.
func TestISMPutFlags(t *testing.T) {
	cmd := findSub(ismCommand(), "put")
	if cmd == nil {
		t.Fatal("ism put subcommand not registered")
	}
	flags := flagNamesOf(cmd)
	for _, want := range []string{"id", "body", "from-file", "if-seq-no", "if-primary-term"} {
		if !containsName(flags, want) {
			t.Errorf("ism put missing --%s flag; have %v", want, flags)
		}
	}
}

// TestILMExplainHasErrorFilter — `--only-errors` is the operator's
// fast-path for "what is broken right now?". Its absence would force
// piping through jq on every triage call.
func TestILMExplainHasErrorFilter(t *testing.T) {
	cmd := findSub(ilmCommand(), "explain")
	if cmd == nil {
		t.Fatal("ilm explain subcommand not registered")
	}
	flags := flagNamesOf(cmd)
	if !containsName(flags, "only-errors") {
		t.Errorf("ilm explain missing --only-errors; have %v", flags)
	}
}

func subNames(cmd *cli.Command) []string {
	out := make([]string, 0, len(cmd.Commands))
	for _, sub := range cmd.Commands {
		out = append(out, sub.Name)
	}
	return out
}

func findSub(cmd *cli.Command, name string) *cli.Command {
	for _, sub := range cmd.Commands {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

func flagNamesOf(cmd *cli.Command) []string {
	out := make([]string, 0, len(cmd.Flags))
	for _, f := range cmd.Flags {
		// urfave/cli flag types each carry a Names() method that
		// returns aliases first; the canonical name is always the
		// first entry.
		if names := f.Names(); len(names) > 0 {
			out = append(out, names[0])
		}
	}
	return out
}

func containsName(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
