# Shell completion

`esops` ships tab-completion for bash, zsh, fish, and PowerShell.

```sh
esops completion <shell>
```

Supported shells: `bash`, `zsh`, `fish`, `pwsh`.

Package installers (Homebrew, deb, rpm) usually place the scripts automatically. For source builds or manual setup, use the commands below.

## Installation

### bash

```sh
# One-shot
source <(esops completion bash)

# Persistent
esops completion bash > ~/.local/share/bash-completion/completions/esops
```

Requires the `bash-completion` package.

### zsh

```sh
# One-shot
source <(esops completion zsh)

# Persistent
esops completion zsh > "${fpath[1]}/_esops"
compinit
```

Add `autoload -Uz compinit && compinit` to `.zshrc` if not already present.

### fish

```sh
esops completion fish > ~/.config/fish/completions/esops.fish
```

Loaded automatically on next shell start.

### PowerShell

```powershell
esops completion pwsh > $PROFILE.CurrentUserAllHosts.d\esops.ps1
```

Dot-source it from your `$PROFILE` if saved elsewhere.

## What completes

Tab completion works for:

- Subcommands (`ops`, `config`, `completion`, `help`)
- All flags (`--context`, `--output`, `--timeout`, `--config`, …)
- Context names (`esops config use-context <TAB>` shows contexts from your active config)

Completion respects `--config` and `$ESOPS_CONFIG` and stays up-to-date with the installed binary (no stale scripts).

---

**Quick start (bash example)**  
```sh
esops completion bash > ~/.local/share/bash-completion/completions/esops
source ~/.local/share/bash-completion/completions/esops
```