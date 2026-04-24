# Configuration

`esops` reads cluster definitions, defaults, and the active-cluster
pointer from a single YAML file — the same layout `kubectl` uses for
`~/.kube/config`. One user-owned file; `esops config use-context`
edits it in place.

## Where `esops` looks for the file

On every invocation the config path is resolved in this order. The
first match wins:

1. `--config PATH` / `-c PATH` — explicit flag, always wins.
2. `$ESOPS_CONFIG` — env var. If set but the file does not exist,
   `esops` errors out rather than silently falling through.
3. `./esops.yaml` — per-repo config in the current directory.
4. `$XDG_CONFIG_HOME/esops/config.yaml` — XDG config dir.
5. `~/.config/esops/config.yaml` — XDG fallback.

If none match, `esops` prints the paths it searched and exits with a
non-zero status.

## First-time setup

Copy the shipped example into the user location and edit it:

```sh
mkdir -p ~/.config/esops
cp config/esops.example.yaml ~/.config/esops/config.yaml
$EDITOR ~/.config/esops/config.yaml
```

Prefer a per-repo config for throwaway experiments:

```sh
cp config/esops.example.yaml ./esops.yaml
```

Or point `esops` at an arbitrary file for a single invocation:

```sh
esops --config /path/to/cluster.yaml ops health
ESOPS_CONFIG=/path/to/cluster.yaml esops ops health
```

## File shape

```yaml
current-context: local-es          # the default cluster; managed by `esops config use-context`

defaults:                          # applied to every context unless overridden
  output: table
  log_level: info
  timeout: 30s

contexts:
  local-es:
    url: http://localhost:9200
    protection: none
  local-os:
    url: http://localhost:9202
    auth:
      type: basic
      username: admin
      password: ${env:ESOPS_LOCAL_OS_PASSWORD}
    protection: none
```

See [`config/esops.example.yaml`](../config/esops.example.yaml) for
the fully annotated version, including auth variants (`api_key`,
`bearer`, `mtls`, `sigv4`), TLS settings, and protection tiers.

Secrets should use indirection — `${env:VAR}`, `${file:/path}`,
`${keyring:service/account}` — rather than inline plaintext. See the
header comments in the example for the full story.

## Commands

### `esops config get-contexts`

Lists every context in the resolved config file. The currently active
context is flagged with `*` in the `CURRENT` column.

```sh
$ esops config get-contexts
CURRENT  NAME      URL                    AUTH   PROTECTION
*        local-es  http://localhost:9200  none   none
         local-os  http://localhost:9202  basic  none
```

Output formats: `-o table` (default), `-o wide` (adds `MAX_IN_FLIGHT`,
`TLS_INSECURE`, `CACERT`), `-o json`, `-o yaml`.

### `esops config use-context NAME`

Sets `current-context: NAME` in the resolved config file. The write
is surgical — only that single line is rewritten — so comments,
ordering, and formatting of the rest of the file are preserved.

```sh
$ esops config use-context local-os
Switched to context "local-os".
```

Fails if `NAME` is not defined in the file, listing the available
contexts.

### `esops config view`

Prints the effective config — defaults plus the currently selected
context. Useful for sanity-checking which file `esops` resolved,
verifying that secret indirection refs are wired up the way you think
they are, or piping the shape into another tool.

```sh
$ esops config view
current-context: local-es
defaults:
  output: table
  log_level: info
  timeout: 30s
contexts:
  local-es:
    url: http://localhost:9200
    protection: none
```

**Context filtering.** By default only the current context is shown;
every other context in the file is hidden. This matches the "show me
what I'm about to operate against" read of `config view`.

- `--context NAME` (global flag) selects a different context to show.
- `--all` dumps every context in the file — the full-file audit view.
  Use this when comparing clusters or debugging an unfamiliar config.
- If `current-context` points at a context the file doesn't define,
  the default view errors out so the broken pointer isn't silently
  hidden. `--all` bypasses that check.

**Redaction.** Inline plaintext secrets (`password`, `api_key`,
`token`) are replaced with `REDACTED` by default. Indirection refs
(`${env:VAR}`, `${file:/path}`, `${keyring:service/account}`) are kept
verbatim — they point at secrets but aren't secrets themselves, and
hiding them would strip information needed to audit config shape.

- `--show-secrets` disables redaction for the current invocation.
  Inline plaintext values come through unchanged. Indirection refs are
  **not** resolved: the output reflects the file, not the runtime
  secret. Use sparingly.

**Output formats.** `-o yaml` (default) matches the on-disk shape.
`-o json` is the scriptable alternative. Other formats (`table`,
`wide`, `ndjson`) are rejected — a nested heterogeneous document
doesn't map cleanly onto a table, and the other commands already
cover tabular views.

## Per-invocation overrides

Sometimes you want to target a different cluster without flipping the
default:

- `--context NAME` — pick a context for this invocation only.
- `--url URL` — bypass context resolution entirely; useful for
  ad-hoc one-offs or scripting against a cluster you don't want in
  the config file.
- `--config PATH` — point at a different file for this invocation.

## Relationship to `kubectl`

If you know `kubectl`, the mental model maps one-to-one:

| kubectl                              | esops                                  |
| ------------------------------------ | -------------------------------------- |
| `~/.kube/config`                     | `~/.config/esops/config.yaml`          |
| `$KUBECONFIG`                        | `$ESOPS_CONFIG`                        |
| `kubectl config current-context`     | `esops config get-contexts` (the `*`)  |
| `kubectl config get-contexts`        | `esops config get-contexts`            |
| `kubectl config use-context NAME`    | `esops config use-context NAME`        |
| `kubectl config view`                | `esops config view`                    |
| `kubectl --context NAME ...`         | `esops --context NAME ...`             |
