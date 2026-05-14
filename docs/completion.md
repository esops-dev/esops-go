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
- **Flag values** drawn from the active config or the live cluster — see below.

Completion respects `--config` and `$ESOPS_CONFIG` and stays up-to-date with the installed binary (no stale scripts).

## Dynamic flag-value completion

Pressing TAB after a flag that takes a name suggests live values. Two categories:

**Local (no cluster call)**

| Flag | Source |
|---|---|
| `--context`, `--cluster` | Context names from the active config |
| `--source`, `--target` (on `migrate *`) | Context names from the active config |

**Cluster-backed**

| Flag | What it suggests |
|---|---|
| `--index` (on `index *`, `ops *`, `migrate cutover --from/--to`) | Index names (hidden / `.ds-*` filtered out) |
| `--alias` (on `index aliases`, `index rollover`, `migrate cutover`) | Alias names |
| `--name` (on `index template *`) | Index template names |
| `--name` (on `index ilm *`) | ILM policy names (Elasticsearch only) |
| `--id` (on `index ism *`) | ISM policy IDs (OpenSearch only) |
| `--name` (on `snapshot policy *`) | SLM (ES) / SM (OS) policy names |
| `--name` (on `pipeline *`) | Ingest pipeline IDs |
| `--name` (on `datastream *`) | Data stream names |
| `--repository` (on `snapshot *`) | Registered repository names |
| `--snapshot` (on `snapshot *`) | Snapshots inside the repo named by `--repository` (set it first) |
| `--node`, `--nodes` (on `ops *`) | Cluster node names |
| `--from-node`, `--to-node` (on `ops rebalance`) | Cluster node names |

### Performance and safety

- **Hard timeout**: cluster-backed lookups are bounded by `--complete-timeout` (default 500ms). On timeout, completion returns nothing and the shell falls back to no suggestions — your terminal never hangs.
- **Cache**: results are cached at `$XDG_CACHE_HOME/esops/complete/` (macOS: `~/Library/Caches/esops/complete/`) for `--complete-cache` duration (default 30s). The cache is keyed by `(context, kind)` and is automatically invalidated when you switch contexts. Pass `--complete-cache 0` to disable for an invocation; the cache files are mode `0600`.
- **Silent failures**: any error in a completion lookup (cluster unreachable, auth denied, deadline exceeded) produces zero suggestions and exit code 0. Completion never writes diagnostics to your terminal.
- **Prod-protected contexts**: completion is read-only and runs against `prod`-tier contexts without prompting for `--i-know-this-is-prod`.

### Tuning

```sh
# Tighter deadline on a high-latency cluster
esops --complete-timeout 1s ...

# Longer cache TTL if your index inventory is stable
esops --complete-cache 5m ...

# Disable caching entirely (fresh lookup every TAB)
esops --complete-cache 0 ...

# Wipe the cache on demand (e.g. after creating new indices that
# completion should pick up before the TTL expires)
esops config cache-clear
esops config cache-clear --dry-run   # preview without deleting
```

---

**Quick start (bash example)**  
```sh
esops completion bash > ~/.local/share/bash-completion/completions/esops
source ~/.local/share/bash-completion/completions/esops
```