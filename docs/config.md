# Configuration

`esops` stores cluster definitions, defaults, and the active context pointer in a single YAML file — exactly like `kubectl`’s `~/.kube/config`.

One user-owned file. The `esops config` commands edit it safely in place.

## Where `esops` looks for the file

The config path is resolved in this order (first match wins):

- `--config PATH` / `-c PATH`
- `$ESOPS_CONFIG` environment variable
- `./esops.yaml` (per-repo)
- `$XDG_CONFIG_HOME/esops/config.yaml`
- `~/.config/esops/config.yaml`

If none exist, `esops` shows the searched paths and exits.

## First-time setup

```sh
mkdir -p ~/.config/esops
cp config/esops.example.yaml ~/.config/esops/config.yaml
$EDITOR ~/.config/esops/config.yaml
```

For quick experiments use a local file:
```sh
cp config/esops.example.yaml ./esops.yaml
```

Secrets should **never** be written in plaintext. Use `${env:VAR}`, `${file:/path}`, or `${keyring:service/account}`.

## File shape

```yaml
current-context: local-es

defaults:
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

See `config/esops.example.yaml` for all auth types (`api_key`, `bearer`, `mtls`, `sigv4`), TLS options, and protection tiers.

---

## esops config get-contexts

Lists all contexts. Current context is marked with `*`.

```sh
$ esops config get-contexts
CURRENT  NAME      URL                    AUTH   PROTECTION
*        local-es  http://localhost:9200  none   none
         local-os  http://localhost:9202  basic  none
```

Supports `-o table` (default), `-o wide`, `-o json`, `-o yaml`.

---

## esops config use-context NAME

Switches the active context in the config file.

```sh
$ esops config use-context local-os
Switched to context "local-os".
```

Fails (and lists available contexts) if the name does not exist.

---

## esops config view

Shows the effective config (defaults + selected context).

```sh
$ esops config view
current-context: local-es
defaults:
  output: table
  ...
contexts:
  local-es:
    ...
```

**Useful flags**
- `--all` → show every context in the file
- `--show-secrets` → disable redaction of inline passwords / keys

Default output is YAML. Use `-o json` for scripting.

---

## Per-invocation overrides

- `--context NAME` — use a different context for this command only
- `--url URL` — bypass config entirely (one-off clusters)
- `--config PATH` — use a different config file

**Relationship to kubectl**

| kubectl                              | esops                                      |
|--------------------------------------|--------------------------------------------|
| `~/.kube/config`                     | `~/.config/esops/config.yaml`              |
| `kubectl config get-contexts`        | `esops config get-contexts`                |
| `kubectl config use-context`         | `esops config use-context`                 |
| `kubectl config view`                | `esops config view`                        |
| `kubectl --context NAME ...`         | `esops --context NAME ...`                 |

All `ops` commands also accept these overrides.
