# ops — day-to-day cluster operations

The `esops ops` command group covers read-only, incident-friendly
operations against a live cluster: the things you need when a page
fires at 03:00 and you'd otherwise be reaching for `curl | jq`.

All `ops` subcommands are safe by design: none of them mutate the
target cluster. They can be run against production at any time.

See [`config.md`](./config.md) for how contexts and the active cluster
are resolved.

## `esops ops health`

Prints the cluster-health snapshot returned by `/_cluster/health`:
cluster name, overall status (`green`/`yellow`/`red`), node and data
node counts, active/unassigned shard totals, and the pending-task
queue size. Works identically against Elasticsearch and OpenSearch —
`esops` probes the cluster first, picks the right upstream client, and
renders a neutral view.

```sh
$ esops ops health
context=local-es dialect=elasticsearch version=9.3.3
NAME          STATUS  NODES  DATA_NODES  SHARDS  UNASSIGNED  PENDING
esops-dev-es  green   2      2           3       0           0
```

The line with `context=…` is written to stderr; the table goes to
stdout, so pipelines stay parseable regardless of format.

### Targeting a specific cluster

By default `health` uses the context named in `current-context`. Point
it at something else for a one-off:

```sh
esops --context local-os ops health          # named context from config
esops --url http://localhost:9200 ops health # bypass the config (v0.1.0: still uses current context's auth)
```

### Output formats

| Flag             | Intended for                                       |
| ---------------- | -------------------------------------------------- |
| `-o table` (def) | Human eyes.                                        |
| `-o wide`        | Adds `DIALECT`, `VERSION`, primaries, relocating / initializing / pending counts, active-shard %. |
| `-o json`        | Scripts. Stable field names, matches the `/_cluster/health` shape. |
| `-o yaml`        | Same shape as JSON, for config-style consumers.    |

NDJSON is not yet implemented and will come with the first streaming
command (`index list`).

### Exit codes

`health` follows the project-wide exit-code contract:

| Code | Meaning                                                         |
| ---- | --------------------------------------------------------------- |
| 0    | Cluster reachable; status was rendered.                         |
| 1    | Generic error (config parse, unknown flag value, etc.).         |
| 3    | Cluster unreachable (DNS, TCP refuse, HTTP 5xx on `GET /`).     |
| 4    | Authentication failed (HTTP 401).                               |
| 5    | Authorization failed (HTTP 403 — auth OK, not allowed).         |

A non-zero exit code does **not** mean the cluster is unhealthy; it
means `esops` could not retrieve the health. A `red`-status cluster
that answers successfully still exits 0. This keeps the "can we reach
it?" question separate from the "is it happy?" question, which is the
distinction a monitoring script usually wants.

### How the probe works

On every invocation `esops ops health` issues a plain `GET /` to the
first address in the context before picking a client implementation.
The response's `version.distribution` field (for OpenSearch) or the
`You Know, for Search` tagline (for Elasticsearch) determines which
upstream SDK is wired up. The `type:` field in the config is not
trusted as the sole source of truth — misconfigured `type: elasticsearch`
against an OS cluster still produces the correct client.

### Known limitations (v0.1.0)

- Only `basic` auth is wired up. `api_key`, `bearer`, `mtls`, and
  `sigv4` fields in the config are accepted by the parser but rejected
  at connect time. These land in subsequent releases.
- Only `${env:VAR}` secret indirection works; `${file:…}` and
  `${keyring:…}` return "not implemented" errors so a literal
  placeholder is never passed as a credential.
- Allocation summary (shard skew, over-sized shards) will be added
  once `ops shards` lands — `health` currently shows only the raw
  cluster-health payload.
