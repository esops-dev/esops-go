// Package completion renders shell completion scripts for the esops
// CLI. It replaces the auto-registered urfave/cli completion command to
// work around an upstream bug in v3.8.0 where the fish template is
// rendered via fmt.Sprintf with a placeholder the formatter cannot
// parse, producing a script full of %!(MISSING) tokens.
//
// We own the templates (copied from upstream under MIT) and substitute
// the app name via plain string replacement so no fmt verb is involved.
package completion

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"
)

//go:embed templates/bash
var bashScript string

//go:embed templates/zsh
var zshScript string

//go:embed templates/fish
var fishScript string

//go:embed templates/pwsh
var pwshScript string

var scripts = map[string]string{
	"bash": bashScript,
	"zsh":  zshScript,
	"fish": fishScript,
	"pwsh": pwshScript,
}

const description = `Output a shell completion script for bash, zsh, fish, or PowerShell.

Source the output to enable completion. Examples:

  # bash
  source <(esops completion bash)

  # zsh
  source <(esops completion zsh)

  # fish
  esops completion fish > ~/.config/fish/completions/esops.fish

  # PowerShell
  esops completion pwsh > esops.ps1

Package installers (deb, rpm, Homebrew) ship these scripts pre-rendered,
so this subcommand is primarily for source builds and interactive use.`

// Configure mutates the auto-registered completion command in place:
// un-hides it, swaps in a friendlier description, and overrides the
// action so fish renders correctly.
func Configure(c *cli.Command) {
	c.Hidden = false
	c.Description = description
	c.Action = action
}

func action(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() == 0 {
		return cli.Exit(fmt.Sprintf("no shell provided; available: %s", shellList()), 1)
	}
	shell := cmd.Args().First()
	tmpl, ok := scripts[shell]
	if !ok {
		return cli.Exit(fmt.Sprintf("unknown shell %q; available: %s", shell, shellList()), 1)
	}
	_, err := fmt.Fprint(cmd.Writer, Render(tmpl, cmd.Root().Name))
	return err
}

// Render substitutes the app name placeholder. Exported for tests.
func Render(tmpl, appName string) string {
	return strings.ReplaceAll(tmpl, "{{APP}}", appName)
}

func shellList() string {
	names := make([]string, 0, len(scripts))
	for k := range scripts {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
