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

### Known limitations

- Only `basic` auth is wired up. `api_key`, `bearer`, `mtls`, and
  `sigv4` fields in the config are accepted by the parser but rejected
  at connect time. These land in subsequent releases.
- Only `${env:VAR}` secret indirection works; `${file:…}` and
  `${keyring:…}` return "not implemented" errors so a literal
  placeholder is never passed as a credential.
- `health` shows only the raw cluster-health payload. Use
  [`ops shards`](#esops-ops-shards) for the allocation view (shard
  distribution, unassigned shards, per-index sizes).

## `esops ops shards`

Prints the shard-allocation view returned by `/_cat/shards`: which
shards live on which nodes, which are primary vs replica, which are
unassigned, and how big each one is. The first thing you reach for when
a cluster goes yellow — "where are my unassigned shards?" — or when you
need a quick sanity check on shard distribution across nodes.

```sh
$ esops ops shards
context=local-es dialect=elasticsearch version=9.3.3 shards=3
INDEX              SHARD  ROLE  STATE    STORE   NODE
esops-logs-000001  0      p     STARTED  34.2kb  es01
esops-products     0      p     STARTED  13.0kb  es02
esops-users        0      p     STARTED  9.0kb   es01
```

Sort order is stable: index name, then shard number, then primaries
before replicas. Diffs across runs stay readable.

Like `ops health`, the `context=…` banner goes to stderr and the table
to stdout, so pipelines keep working regardless of `--output`.

### Filters

Filters compose. Index patterns are pushed down to the cluster as a
path parameter; the rest are applied client-side so behaviour is
identical on Elasticsearch and OpenSearch.

| Flag            | Purpose                                                    |
| --------------- | ---------------------------------------------------------- |
| `--index`       | Comma-separated index patterns (e.g. `logs-*,users`).      |
| `--state`       | Shard state (`STARTED`, `UNASSIGNED`, `RELOCATING`, …). Case-insensitive. |
| `--node`        | Exact node name.                                           |
| `--primary`     | Primaries only.                                            |
| `--replica`     | Replicas only.                                             |
| `--unassigned`  | Shortcut for `--state UNASSIGNED`.                         |

`--primary` and `--replica` are mutually exclusive, as are
`--unassigned` and `--state`. Conflicting flags exit with code 2.

### Output formats

| Flag             | Intended for                                        |
| ---------------- | --------------------------------------------------- |
| `-o table` (def) | Human eyes. `INDEX SHARD ROLE STATE STORE NODE`, plus `REASON` when the result contains any unassigned shards. |
| `-o wide`        | Adds `DOCS`, raw `STORE_BYTES`, `IP`, `REASON`, and `SINCE`. |
| `-o json`        | Scripts. Array of shard records with stable keys (`index`, `shard`, `primary`, `state`, `docs`, `store_bytes`, `node`, `ip`, plus the `unassigned_*` quartet when applicable). |
| `-o yaml`        | Same shape as JSON.                                 |

`STORE` in the table columns is human-friendly (`34.2kb`). `store_bytes`
in JSON/YAML and the `STORE_BYTES` wide column are always in bytes so
you can feed them straight into a numeric script.

Unassigned shards render `-` in the `NODE` column and omit `node`/`ip`
from JSON/YAML output (they're not meaningful). `docs` and
`store_bytes` are zero for unassigned shards — the `state` column is
the authoritative signal, not the size.

### Why a shard is unassigned

When the result includes at least one unassigned shard, the default
table appends a `REASON` column so `ops shards --unassigned` is
self-explanatory without reaching for `-o wide`. Four fields are
populated for unassigned shards and omitted (via `omitempty`) for
healthy ones:

| Field                 | Meaning                                             |
| --------------------- | --------------------------------------------------- |
| `unassigned_reason`   | Cluster-assigned reason code (`NODE_LEFT`, `INDEX_CREATED`, `ALLOCATION_FAILED`, `CLUSTER_RECOVERED`, …). |
| `unassigned_at`       | RFC3339 timestamp of when the shard became unassigned. |
| `unassigned_for`      | Human-readable duration (`12.3s`, `4.7m`).          |
| `unassigned_details`  | Free-form detail string the cluster attached to the reason. JSON/YAML only — too long for a table column. |

These values come straight from `_cat/shards?h=unassigned.reason,...`
and are identical across dialects. The `unassigned_details` field is
especially useful for `ALLOCATION_FAILED` shards, where the cluster
embeds the last allocation error. For deeper analysis reach for
`ops allocation-explain` (not yet wired up).

### Examples

```sh
# Only unassigned shards — the on-call use case. REASON column is
# automatically appended.
esops ops shards --unassigned

# Extract the unassigned reasons as a frequency table.
esops ops shards --unassigned -o json \
  | jq -r '.[].unassigned_reason' | sort | uniq -c

# Primaries of a specific index, with byte-accurate sizes.
esops ops shards --index esops-logs-* --primary -o wide

# Script-friendly view of one node's workload.
esops ops shards --node es01 -o json | jq '[.[].store_bytes] | add'

# Cross-dialect: same command, OpenSearch cluster.
esops --context local-os ops shards --index esops-*
```

### Exit codes

`shards` follows the project-wide exit-code contract:

| Code | Meaning                                                 |
| ---- | ------------------------------------------------------- |
| 0    | Cluster reachable; rows rendered (including zero rows). |
| 1    | Generic error (config parse, etc.).                     |
| 2    | Usage error (conflicting filter flags, bad `--output`). |
| 3    | Cluster unreachable.                                    |
| 4    | Authentication failed (HTTP 401).                       |
| 5    | Authorization failed (HTTP 403).                        |

A cluster full of unassigned shards still exits 0 — `shards` reports
the allocation truthfully, it doesn't judge it. Alerting on
`UNASSIGNED` rows is the caller's job.
