package config

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/esops-dev/esops-go/internal/audit"
	esopsconfig "github.com/esops-dev/esops-go/pkg/config"
)

func useContextCommand() *cli.Command {
	return &cli.Command{
		Name:          "use-context",
		Usage:         "Set the default context (writes to the config file)",
		ArgsUsage:     "NAME",
		Action:        runUseContext,
		ShellComplete: completeContextNames,
	}
}

// completeContextNames prints known contexts from the loaded config,
// one per line, for shell tab-completion. Errors are swallowed — a
// failed lookup during completion should silently offer nothing rather
// than spew diagnostics into the user's shell.
func completeContextNames(_ context.Context, cmd *cli.Command) {
	if cmd.NArg() > 0 {
		return
	}
	cfg, _, err := esopsconfig.LoadDefault(cmd.String("config"))
	if err != nil {
		return
	}
	for _, name := range sortedNames(cfg.Contexts) {
		_, _ = fmt.Fprintln(cmd.Writer, name)
	}
}

func runUseContext(_ context.Context, cmd *cli.Command) error {
	args := cmd.Args()
	if args.Len() != 1 {
		return fmt.Errorf("expected 1 argument (context name), got %d", args.Len())
	}
	name := args.First()

	// use-context mutates the on-disk config file and so changes the
	// next esops invocation's cluster target. It does not go through
	// cmdsetup.Prepare (no cluster involved), so audit.Begin has to
	// be called explicitly. cluster_url stays empty — there's no
	// cluster touched here, only a local file write.
	audit.Begin("config use-context", os.Args, name, "", false)

	cfg, path, err := esopsconfig.LoadDefault(cmd.String("config"))
	if err != nil {
		return err
	}
	if _, ok := cfg.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found in %s (available: %s)",
			name, path, strings.Join(sortedNames(cfg.Contexts), ", "))
	}
	if name == cfg.CurrentContext {
		_, _ = fmt.Fprintf(os.Stdout, "Already on context %q.\n", name)
		return nil
	}
	if err := esopsconfig.SetCurrentContext(path, name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "Switched to context %q.\n", name)
	return nil
}

func sortedNames(m map[string]esopsconfig.Context) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
