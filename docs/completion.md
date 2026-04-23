# Shell completion

`esops` ships tab-completion for bash, zsh, fish, and PowerShell. The
binary prints the completion script for your shell; you source it from
your shell rc file (or drop it into a completion directory) and get
subcommand, flag, and context-name completion out of the box.

```
esops completion <shell>
```

Available shells: `bash`, `zsh`, `fish`, `pwsh`.

Package installers (deb, rpm, Homebrew) ship these scripts pre-rendered
into the system completion directories, so end-users on those
distributions generally don't need to run the command manually. For
source builds and interactive use, the instructions below apply.

## Installing

### bash

One-shot, per-shell (survives until the session ends):

```sh
source <(esops completion bash)
```

Persistent — write the script once, source it from `.bashrc`:

```sh
esops completion bash > /etc/bash_completion.d/esops     # system-wide
# or
esops completion bash > ~/.local/share/bash-completion/completions/esops
```

Requires the `bash-completion` package. On macOS install it via
Homebrew (`brew install bash-completion@2`) and make sure your
`.bash_profile` sources it.

### zsh

One-shot:

```sh
source <(esops completion zsh)
```

Persistent — drop the script into a directory on `$fpath` and rebuild
the completion cache:

```sh
esops completion zsh > "${fpath[1]}/_esops"
compinit
```

If `compinit` isn't already enabled, add this to `.zshrc`:

```zsh
autoload -Uz compinit && compinit
```

### fish

```sh
esops completion fish > ~/.config/fish/completions/esops.fish
```

Fish picks it up on next shell start — no sourcing needed.

### PowerShell

```powershell
esops completion pwsh > $PROFILE.CurrentUserAllHosts.d\esops.ps1
# Or save anywhere and dot-source it from your $PROFILE:
. path\to\esops.ps1
```

## What completes

### Subcommands and flags

Everything in the command tree is discoverable by `<TAB>`:

```sh
$ esops <TAB>
config     -- Inspect and modify esops configuration
ops        -- Day-to-day cluster operations
completion -- Output a shell completion script for bash, zsh, fish, or PowerShell
help

$ esops ops <TAB>
health -- Cluster health snapshot
```

Global and subcommand flags complete the same way:

```sh
$ esops --<TAB>
--config   --context  --output   --timeout  ...
```

### Context names

`esops config use-context <TAB>` suggests the contexts defined in your
resolved config file:

```sh
$ esops config use-context <TAB>
local-es  local-os
```

The completion respects `--config PATH` and `$ESOPS_CONFIG`, so you'll
see the right contexts regardless of which file is active. Failures
during completion (missing file, parse errors) are suppressed — you
get no suggestions rather than error spam mid-keystroke.

## How it works

The shim scripts emitted by `esops completion <shell>` invoke the
binary with a hidden `--generate-shell-completion` flag on every
`<TAB>`. The binary replies with newline-separated candidates, and the
shell filters them against the word the cursor is on. Since the
candidates are computed by the binary, they stay in sync with the
installed version — no stale generated files to update after upgrades.
