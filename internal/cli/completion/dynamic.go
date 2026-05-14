// Dynamic flag-value completion. The shell scripts in this package
// re-invoke esops with `--generate-shell-completion` whenever the
// operator hits TAB, and urfave/cli routes that re-entry to the
// matched command's ShellComplete callback.
//
// urfave/cli's DefaultCompleteWithFlags only emits subcommand and
// flag *names*; it has no per-flag value hook. ForFlags fills the
// gap: it inspects os.Args to find the flag the operator is currently
// completing a value for, looks the flag up in a per-command map, and
// runs the matching Suggester.
//
// Failures inside a Suggester (cluster unreachable, auth denied,
// timeout) must produce zero suggestions — never an error in the
// operator's terminal. Callers honour this contract; the dispatcher
// does not propagate panics or write to stderr.
package completion

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
)

// completionFlag is the sentinel urfave/cli appends to os.Args when
// the shell completion script re-invokes esops. Kept verbatim from
// upstream so the look-back logic in PreviousFlag stays consistent if
// the upstream completion machinery is refactored.
const completionFlag = "--generate-shell-completion"

// Suggester returns shell-completion candidates for a flag value.
// Errors are not returned: a completion failure must produce zero
// suggestions and never surface anything into the user's shell.
type Suggester func(ctx context.Context, cmd *cli.Command) []string

// ForFlags returns a ShellComplete callback for the given command.
// perFlag maps a flag name (without leading dashes) to its Suggester.
// When the operator is mid-typing a value for one of those flags,
// the matching Suggester runs and its strings are written one per
// line to the root command's writer. Otherwise the call falls back
// to urfave/cli's default behaviour (subcommand and flag-name
// suggestions).
func ForFlags(perFlag map[string]Suggester) cli.ShellCompleteFunc {
	return func(ctx context.Context, cmd *cli.Command) {
		flag := PreviousFlag()
		if flag == "" {
			cli.DefaultCompleteWithFlags(ctx, cmd)
			return
		}
		sug, ok := perFlag[flag]
		if !ok {
			cli.DefaultCompleteWithFlags(ctx, cmd)
			return
		}
		w := cmd.Root().Writer
		for _, v := range sug(ctx, cmd) {
			_, _ = fmt.Fprintln(w, v)
		}
	}
}

// PreviousFlag returns the bare flag name (no leading dashes, no
// `=value` suffix) that the operator is currently completing a value
// for, or "" if not in flag-value position.
//
// The bash/zsh/fish/pwsh templates in this package shape the
// re-invocation so that `--generate-shell-completion` is always the
// final argv token. The token immediately before it is therefore the
// most recently typed user word — which is the flag name when the
// operator is at `--foo <TAB>` and an empty cur, or the partial value
// when the operator typed `--foo=bar<TAB>`.
//
// Cases handled:
//   - `--foo --generate-shell-completion`      → "foo"
//   - `-f --generate-shell-completion`         → "f"   (short alias)
//   - `--foo=val --generate-shell-completion`  → "foo" (= splits name from value)
//   - anything else                            → ""
//
// Reads os.Args directly because urfave/cli strips the completion
// sentinel from the parsed argv at the root and the per-command
// cmd.Args() view varies by depth — os.Args is the only place the
// full original command line is reliably visible.
func PreviousFlag() string {
	return previousFlagIn(os.Args)
}

func previousFlagIn(args []string) string {
	n := len(args)
	if n < 2 {
		return ""
	}
	last := args[n-1]
	if last != completionFlag {
		return ""
	}
	prev := args[n-2]
	if !strings.HasPrefix(prev, "-") {
		return ""
	}
	prev = strings.TrimLeft(prev, "-")
	if eq := strings.IndexByte(prev, '='); eq >= 0 {
		prev = prev[:eq]
	}
	if prev == "" {
		return ""
	}
	return prev
}
