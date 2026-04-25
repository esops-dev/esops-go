# ops — day-to-day cluster operations

The `esops ops` command group covers day-to-day cluster operations:
the things you need when a page fires at 03:00 and you'd otherwise be
reaching for `curl | jq`.

Most `ops` subcommands are read-only and safe to run against production
at any time. The exception is [`ops rebalance`](#esops-ops-rebalance),
which submits `/_cluster/reroute` commands and mutates routing state —
it honours `--dry-run` and the per-context `protection:` tier, and is
the only command in this group that requires `--i-know-this-is-prod`
against a prod-protected context.

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

NDJSON is implemented for commands that emit lots of rows; see
[`index list`](./index.md). `ops health` is a single-record command, so
it does not offer `-o ndjson`.

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
embeds the last allocation error. For deeper analysis — the
per-decider breakdown of why a shard landed where it did — reach for
[`ops allocation-explain`](#esops-ops-allocation-explain).

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

## `esops ops allocation-explain`

Wraps `/_cluster/allocation/explain`. Where [`ops shards`](#esops-ops-shards)
tells you **which** shards are unassigned, `allocation-explain` tells
you **why** — the per-decider breakdown the cluster evaluated (disk
watermark, `same_shard`, `awaiting_info`, shard-awareness filter, …)
on each node.

Two invocation shapes:

```sh
# No flags: the cluster picks the first unassigned shard it finds.
# Fails with HTTP 400 if the cluster has no unassigned shards.
esops ops allocation-explain

# Targeted: name the specific shard to explain. Works for BOTH
# unassigned ("why can't this go anywhere?") and assigned
# ("why did the cluster land it here, and could it rebalance?").
esops ops allocation-explain --index esops-users --shard 0 --primary
```

```sh
$ esops ops allocation-explain --index esops-users --primary
context=local-es dialect=elasticsearch version=9.3.3 index=esops-users shard=0 state=started
INDEX        SHARD  ROLE  STATE    NODE  CAN_ALLOCATE
esops-users  0      p     started  es01  -

REBALANCE: This shard is in a well-balanced location and satisfies all allocation rules so it will remain on this node. [...]

NODE  DECISION       DECIDERS
es02  worse_balance  -
```

For an unassigned shard the top block gains an `UNASSIGNED_REASON`
section and a prose `EXPLANATION:` line, and the node-decision
summary lists the NO-rulings that blocked allocation:

```
INDEX            SHARD  ROLE  STATE       NODE  CAN_ALLOCATE
test-unassigned  0      r     unassigned  -     no

UNASSIGNED_REASON: INDEX_CREATED
LAST_STATUS:       no_attempt

EXPLANATION: cannot allocate because all nodes already hold a copy

NODE  DECISION  DECIDERS
es01  no        same_shard=no
es02  no        same_shard=no
```

Like every `ops` command, the `context=…` banner lands on stderr and
the table on stdout, so `| jq` and shell redirections keep working
regardless of `--output`.

### Flags

| Flag                      | Purpose                                                                              |
| ------------------------- | ------------------------------------------------------------------------------------ |
| `--index`                 | Index to explain. Omit to let the cluster pick any unassigned shard.                 |
| `--shard`                 | Shard number (requires `--index`). Defaults to `0`.                                  |
| `--primary`               | Explain the primary copy (requires `--index`). Default is replica.                   |
| `--include-disk-info`     | Include per-node disk info (free space, watermarks) in the response.                 |
| `--include-yes-decisions` | Include YES decider rulings in the output — noisier, useful for "why did it pick X". |

`--shard` and `--primary` only make sense with an `--index` — the
business layer rejects them up-front with exit code 2 if you forget.
Shard `0` is a valid value, so the existence of `--index` (not `--shard 0`)
is the gate.

### Output formats

| Flag             | Intended for                                                                                         |
| ---------------- | ---------------------------------------------------------------------------------------------------- |
| `-o table` (def) | Human eyes. Header block + node-decisions summary, with NO-rulings compressed into one cell per node. |
| `-o wide`        | Same header, but the node-decisions table expands into one row per decider with the full explanation prose. |
| `-o json`        | Scripts. Stable schema: `index`, `shard`, `primary`, `current_state`, optional `current_node` / `unassigned_info`, plus `node_decisions[].deciders[]`. |
| `-o yaml`        | Same shape as JSON.                                                                                  |

NDJSON is not supported: the endpoint returns exactly one record per
call, so streaming adds no value.

Empty fields are omitted. An assigned shard carries no `unassigned_info`
key; an unassigned shard carries no `current_node` key. The cluster
is the source of truth — when it doesn't report a field, esops emits
nothing rather than a synthetic default.

### Examples

```sh
# "Something's unassigned, I don't know what" — cluster picks one.
esops ops allocation-explain

# Ask the full decider breakdown for one shard, including YES deciders.
esops ops allocation-explain --index esops-logs-000001 --primary \
  --include-yes-decisions -o wide

# Is it a disk issue? Watermark info is opt-in.
esops ops allocation-explain --index big-index --shard 0 \
  --include-disk-info -o json | jq '.cluster_info // "disk info not returned"'

# Frequency of NO-rulings across all nodes for one shard.
esops ops allocation-explain --index big-index --shard 0 -o json \
  | jq -r '.node_decisions[].deciders[]? | select(.decision=="NO") | .decider' \
  | sort | uniq -c
```

### Exit codes

`allocation-explain` follows the project-wide exit-code contract:

| Code | Meaning                                                      |
| ---- | ------------------------------------------------------------ |
| 0    | Cluster answered; explanation rendered.                      |
| 1    | Generic error (including HTTP 400 from the cluster — e.g. "no unassigned shards" when called with no flags). |
| 2    | Usage error (`--shard` or `--primary` without `--index`).    |
| 3    | Cluster unreachable.                                         |
| 4    | Authentication failed (HTTP 401).                            |
| 5    | Authorization failed (HTTP 403).                             |

A `no` answer from the cluster is still a successful query: the
explanation prints and the process exits 0. Scripts should look at
`can_allocate` / `current_state` in the JSON output, not at the exit
code.

## `esops ops audit`

Summarises the security posture of the cluster and the connection
esops is making to it — a single command to answer "who can talk to
this cluster, with what, and how am I reaching it?". Combines three
sources:

1. **TLS posture.** Computed from the context config, not from the
   cluster: the URL scheme, whether `insecure: true` is set, whether
   a custom CA is in play, which auth type is configured, and whether
   a client cert is being presented.
2. **Security status.** Dialect-aware probe. Reports whether the
   cluster actually has a security feature turned on at all —
   because an ES cluster with `xpack.security.enabled=false` or an OS
   without the security plugin is a valid (and worth surfacing)
   state, not an error.
3. **Users and roles.** Native-realm users on Elasticsearch
   (`/_security/user`, `/_security/role`); internal users and roles on
   OpenSearch via the security plugin (`/_plugins/_security/api/internalusers`,
   `/_plugins/_security/api/roles`).

```sh
$ esops --context local-es-mtls ops audit
TLS
  scheme:     https
  insecure:   false
  cacert_set: true
  auth:       mtls
  client_cert: true

SECURITY
  dialect:  elasticsearch
  enabled:  true

USERS
USERNAME                ENABLED  RESERVED  ROLES
elastic                 true     true      superuser
kibana_system           true     true      kibana_system
...

ROLES
NAME                     RESERVED  CLUSTER    INDEX_PATTERNS
kibana_admin             true      -          -
kibana_system            true      monitor,…  .kibana*,.reindexed-v8-kibana*,…
superuser                true      all        *
...
```

User-defined entities sort before reserved (built-in) ones, so an
operator scanning for "did somebody add a user here?" reads the top of
each table and stops. Within each group, entries are sorted by name.

### Security disabled clusters

When the cluster runs without a security feature, `ops audit` still
succeeds and reports the status rather than erroring:

```sh
$ esops --context local-es ops audit          # xpack.security.enabled=false
TLS
  scheme:     http
  insecure:   false
  cacert_set: false
  auth:       none

SECURITY
  dialect:  elasticsearch
  enabled:  false
  note:     no handler found for uri [/_security/user] and method [GET]
```

The note quotes the cluster's own error string so you can distinguish
between "feature disabled by config" and "endpoint not registered"
without shelling into the box. The `USERS` and `ROLES` sections are
omitted — there's nothing for the cluster to return.

The same graceful-degradation applies to OpenSearch if the security
plugin is absent (rare on production builds, but possible on stripped
distributions).

### Output formats

| Flag             | Intended for                                                                                         |
| ---------------- | ---------------------------------------------------------------------------------------------------- |
| `-o table` (def) | Human eyes. Four-section layout: TLS → SECURITY → USERS → ROLES. Long cluster-privilege and index-pattern cells are truncated with `…`; use `-o wide` for the full strings. |
| `-o wide`        | Adds `BACKEND_ROLES`, `FULL_NAME`, `EMAIL` to USERS and `DESCRIPTION` to ROLES, and drops the cluster/pattern truncation. |
| `-o json`        | Scripts. One top-level object with `tls`, `status`, `users`, `roles` keys. Stable field names, matches the neutral domain types. |
| `-o yaml`        | Same shape as JSON.                                                                                  |

NDJSON is not supported: `audit` emits one structured report, not a
stream of records.

Empty fields are omitted from JSON/YAML (`backend_roles` is absent for
ES users, `email` is absent for OS users, etc.). Scripts should default
to an empty array when the key is missing.

### Dialect differences worth knowing

The CLI surface is identical, but the data behind the columns isn't —
the two products don't model identity the same way, and `ops audit`
does not pretend otherwise:

| Field              | Elasticsearch source                         | OpenSearch source                       |
| ------------------ | -------------------------------------------- | ----------------------------------------|
| `enabled`          | `enabled` on the user record                 | Always `true` (OS has no disable flag; delete the user to disable). |
| `reserved`         | `metadata._reserved`                         | `reserved` on the record                |
| `roles`            | `roles` (native-realm role names)            | `opendistro_security_roles`             |
| `backend_roles`    | — (always empty)                             | `backend_roles` (external LDAP/OIDC)    |
| `full_name` / `email` | Native-realm metadata                     | OS has no email; `description` is mapped to `full_name` when present |
| role `cluster_privileges` | `cluster` array                       | `cluster_permissions` array             |
| role `index_patterns` | Union of `indices[].names`                | Union of `index_permissions[].index_patterns` |

Per-action privilege detail, field-level security (FLS), and
document-level security (DLS) are deliberately not in the MVP output —
they'd double the column width without helping the "is anything
user-defined with wildcard access?" question most operators are
asking. A future `--deep` flag can expand that; the JSON schema is
designed to grow compatibly.

### What `ops audit` does **not** do

- **No identity migration.** `ops audit` reads security state; it does
  not copy users or roles between clusters. ES and OS security models
  are not interchangeable. See the non-goals in the project brief.
- **No API keys, role mappings, tenants, or action groups (yet).**
  Scoped out of v0.1.0 to keep the output readable. These are well-defined
  endpoints and will be added when a concrete user asks.
- **No privilege analysis.** The tool surfaces what the cluster reports;
  it does not score "is this dangerous?" A wildcard index pattern on a
  user-defined role is information — judging it is your job.

### Examples

```sh
# Every user-defined entity across the cluster, scripted.
esops ops audit -o json | jq '{
  users: [.users[] | select(.reserved == false)],
  roles: [.roles[] | select(.reserved == false)]
}'

# Anyone with cluster_privileges including "all"? (superuser auditing.)
esops ops audit -o json | jq '.roles[]
  | select(.cluster_privileges // [] | index("all"))
  | .name'

# mTLS-only: prove what's being presented on the wire.
esops --context local-es-mtls ops audit -o json | jq .tls

# Cross-dialect: same command, OpenSearch — watch `backend_roles`
# appear in -o wide.
esops --context local-os ops audit -o wide
```

### Exit codes

`audit` follows the project-wide exit-code contract:

| Code | Meaning                                                      |
| ---- | ------------------------------------------------------------ |
| 0    | Cluster reachable; audit rendered (including when security is disabled). |
| 1    | Generic error (unexpected cluster response, decode failure). |
| 3    | Cluster unreachable.                                         |
| 4    | Authentication failed (HTTP 401) — check the configured credentials. |
| 5    | Authorization failed (HTTP 403) — the caller is authenticated but cannot read the security endpoints. On ES this typically means the user lacks `manage_security`; on OS it means the account is not mapped to an admin backend role. |

A "security disabled" cluster exits 0 — the absence is the finding, not
an error. Scripts that want to fail on disabled security should assert
`jq -e '.status.enabled'` on the JSON output.

## `esops ops hot-threads`

Wraps `/_nodes/hot_threads` — the JVM introspection endpoint both
Elasticsearch and OpenSearch expose in the same text format. The
canonical "what is this node actually doing?" view when CPU is pinned,
a thread is blocking on a lock, or the cluster is waiting on I/O.

```sh
$ esops ops hot-threads --threads 1 --snapshots 2
::: {es01}{…}{…}{es01}{172.22.0.2}{…}{…}{9.3.3}{…}{…}
   Hot threads at 2026-04-24T19:24:02.965Z, interval=500ms, busiestThreads=1, ignoreIdleThreads=true:

::: {es02}{…}{…}{es02}{172.22.0.4}{…}{…}{9.3.3}{…}{…}
   Hot threads at 2026-04-24T19:24:02.969Z, interval=500ms, busiestThreads=1, ignoreIdleThreads=true:
```

The output is the cluster's own text block: a `::: {node}` header per
node, followed by the sampled stack frames for each of the busiest
threads it saw. esops does not reshape or parse this — the format is
a cluster-version implementation detail, and the canonical form is the
one a support engineer will expect in a ticket paste.

### Flags

| Flag              | Purpose                                                                      |
| ----------------- | ---------------------------------------------------------------------------- |
| `--node NAME`     | Limit to one or more nodes (repeatable, or comma-separated). Omit = all.     |
| `--threads N`     | Number of busy threads per node (cluster default: 3).                        |
| `--interval D`    | Sampling interval between snapshots (cluster default: 500ms).                |
| `--snapshots N`   | Number of stacktrace snapshots per thread (cluster default: 10).             |
| `--type T`        | Sampling dimension: `cpu` (default), `wait`, `block`, or `mem`.              |
| `--show-idle`     | Include threads parked on idle socket/queue selectors (default: hide them).  |

The valid `--type` values are validated client-side — a typo fails fast
with exit code 1 rather than round-tripping to the cluster for an HTTP
400. `--threads` and `--snapshots` must be positive.

### Output formats

| Flag             | Intended for                                                                |
| ---------------- | --------------------------------------------------------------------------- |
| `-o table` (def) | Human eyes. The cluster's raw text, verbatim.                               |
| `-o wide`        | Same as `table` — the text block has no "narrow vs. wide" dimension.        |
| `-o json`        | Scripts. `{ "text": "...\n..." }` — one object, one string field.           |
| `-o yaml`        | Same shape as JSON.                                                         |

NDJSON is not supported: hot-threads emits one text blob, not a stream
of records.

The JSON/YAML envelope keeps the scripted contract ("JSON output is a
single object") intact without pretending there's a per-node schema
that can be queried. For per-node triage, rely on the `::: {node}`
markers in the text — `grep -B1 -A30 ':::'` is the standard idiom.

### Dialect differences worth knowing

The four `--type` values are all accepted on Elasticsearch. On
OpenSearch, the typed client's parameter struct only wires `cpu`;
esops falls back to a raw GET with the query parameter set for
`wait`/`block`/`mem` so the CLI surface stays identical. The cluster
itself supports all four dimensions — only the SDK wrapping differs.

### Examples

```sh
# Default bundle: all nodes, cluster defaults (3 threads, 10 snapshots, 500ms).
esops ops hot-threads

# Single node, higher-resolution sample.
esops ops hot-threads --node es01 --threads 5 --snapshots 20 --interval 1s

# Find wait/block contention, not CPU.
esops ops hot-threads --type wait

# Script-friendly: grab the text blob only.
esops ops hot-threads -o json | jq -r '.text' > hot.txt

# OpenSearch — same CLI.
esops --context local-os ops hot-threads --type block
```

### Exit codes

`hot-threads` follows the project-wide exit-code contract:

| Code | Meaning                                                          |
| ---- | ---------------------------------------------------------------- |
| 0    | Cluster answered; output rendered.                               |
| 1    | Generic error (invalid `--type`, non-positive `--threads`, etc.). |
| 2    | Usage error (unknown flag, bad `--output`).                      |
| 3    | Cluster unreachable.                                             |
| 4    | Authentication failed (HTTP 401).                                |
| 5    | Authorization failed (HTTP 403).                                 |

## `esops ops diag`

Collects a diagnostic bundle for the active cluster and writes it as a
single gzipped tarball. The default capture answers "why is this cluster
behaving this way?" — cluster health, node stats, hot threads, pending
and running tasks, shard allocation, snapshot repositories, and the
dialect-specific lifecycle policies (ILM on Elasticsearch, ISM on
OpenSearch). Every response is captured verbatim so Elastic/OpenSearch
support can parse it with their own tools without second-guessing
esops's normalised view.

```sh
$ esops ops diag
wrote esops-diag-esops-dev-es-20260424T190352Z.tar.gz (18 artifacts, 0 failed)

$ tar -tzf esops-diag-*.tar.gz | head
manifest.json
cluster_info.json
cluster_health.json
cluster_state.json
cluster_stats.json
cluster_settings.json
cluster_pending_tasks.json
cluster_allocation_explain.json
nodes_info.json
nodes_stats.json
```

Read the manifest without unpacking:

```sh
$ tar -xOzf esops-diag-*.tar.gz manifest.json | jq '{cluster, artifact_count: (.artifacts|length)}'
{
  "cluster": { "name": "esops-dev-es", "dialect": "elasticsearch", "version": "9.3.3" },
  "artifact_count": 18
}
```

The progress line (`wrote …`) goes to stderr so `--out -` remains safe
for piping straight into an upload.

### What's in the bundle

Always captured (when the endpoint exists on this dialect):

| File                         | Endpoint                                   | Purpose                                   |
| ---------------------------- | ------------------------------------------ | ----------------------------------------- |
| `manifest.json`              | — (tool-generated)                         | Index: tool version, cluster identity, per-artifact status and size. |
| `cluster_info.json`          | `GET /`                                    | Product, version, build.                  |
| `cluster_health.json`        | `/_cluster/health?level=indices`           | Overall + per-index health.               |
| `cluster_state.json`         | `/_cluster/state/_all`                     | Routing, metadata, master node — usually the bulk of the bundle by size. |
| `cluster_stats.json`         | `/_cluster/stats`                          | Rolled-up cluster-level numbers.          |
| `cluster_settings.json`      | `/_cluster/settings?include_defaults=true&flat_settings=true` | Effective settings (persistent + transient + defaults). |
| `cluster_pending_tasks.json` | `/_cluster/pending_tasks`                  | Queue of cluster-manager tasks not yet applied. |
| `cluster_allocation_explain.json` | `/_cluster/allocation/explain`        | First unassigned shard explained; 400 when cluster is healthy (recorded). |
| `nodes_info.json`            | `/_nodes`                                  | Per-node settings, plugins, JVM, OS.      |
| `nodes_stats.json`           | `/_nodes/stats`                            | Per-node indexing, search, JVM, FS, thread pool counters. |
| `nodes_hot_threads.txt`      | `/_nodes/hot_threads`                      | Sampled hot threads (text, not JSON).     |
| `tasks.json`                 | `/_tasks?detailed=true`                    | Currently-running tasks (reindex, force merge, snapshot, …). |
| `cat_allocation.json`        | `/_cat/allocation?format=json&bytes=b`     | Per-node shard/disk allocation.           |
| `cat_shards.json`            | `/_cat/shards?format=json&bytes=b`         | One row per shard.                        |
| `cat_indices.json`           | `/_cat/indices?format=json&bytes=b&expand_wildcards=open,closed,hidden` | One row per index, including hidden/closed. |
| `cat_recovery.json`          | `/_cat/recovery?format=json&bytes=b&active_only=true` | In-flight shard recoveries. |
| `snapshot_repositories.json` | `/_snapshot`                               | Registered snapshot repositories.         |
| `ilm_policies.json` *(ES)*   | `/_ilm/policy`                             | Every ILM policy in the cluster.          |
| `ilm_status.json` *(ES)*     | `/_ilm/status`                             | ILM service running/stopped.              |
| `ism_policies.json` *(OS)*   | `/_plugins/_ism/policies`                  | Every ISM policy in the cluster.          |

Opt-in (off by default):

| Flag                      | File                              | Endpoint                                 | Why it's opt-in                         |
| ------------------------- | --------------------------------- | ---------------------------------------- | --------------------------------------- |
| `--include-slowlog`       | `index_slowlog_settings.json`     | `/_all/_settings?…slowlog…&include_defaults=true` | Exposes which queries the cluster would log — review before sharing. |
| `--include-mappings`      | `index_mappings.json`             | `/_all/_mapping`                         | Can be hundreds of MB on clusters with many indices. |
| `--include-idle-threads`  | `nodes_hot_threads_with_idle.txt` | `/_nodes/hot_threads?ignore_idle_threads=false` | Noisy by default (the cluster hides idle threads for a reason), but the right next step when the busy-only capture doesn't explain the symptom — parked threads in contended locks look "idle" to the default filter. |

The HTTP slow-log settings are captured, not the log *file* — the cluster
doesn't expose log contents over its API, only the configuration that
controls them.

### Redaction

The bundle never contains the cluster URL, an `Authorization` header,
a password, or a client-certificate path. The only place such values
could appear is the manifest, and the manifest is tool-generated — we
simply don't write connection details into it. Response bodies come
from the cluster and do not include the credentials used to reach it.

If an endpoint's natural response embeds something you consider
sensitive for your environment (IP addresses in `nodes_info`, index
names in `cat_indices`, user-identifying fields in `cluster_state`),
review the bundle before sharing it upstream. esops does not
hash-replace cluster-returned content — operators need to see it for
triage, and judging "is this sensitive *here*?" is site-specific.

### Flags

| Flag                 | Purpose                                                            |
| -------------------- | ------------------------------------------------------------------ |
| `--out PATH`, `-O`   | Write the tarball to PATH. Use `-` for stdout. Default: `./esops-diag-<cluster>-<timestamp>.tar.gz`. |
| `--include-slowlog`  | Also capture per-index slow-log settings.                          |
| `--include-mappings` | Also capture `/_all/_mapping` (large on big clusters).             |

`ops diag` does not accept `-o`/`--output`: the command produces a
tarball, not a tabular view. Global flags (`--context`, `--url`,
`--timeout`, `--insecure`, `--cacert`, `--log-*`) work as usual.

### Streaming the bundle

`--out -` writes the tarball to stdout and the progress line to stderr,
so the bundle can be piped straight into an upload without touching
disk:

```sh
# Attach the bundle to a GitHub issue.
esops ops diag --out - | gh issue create --title 'Cluster issue' --body-file -

# Pipe into S3 without ever landing on the laptop.
esops ops diag --out - | aws s3 cp - s3://support-bucket/$(hostname)-diag-$(date -u +%Y%m%d).tar.gz
```

### Bundle shape

```
manifest.json                     Tool-generated index (always first).
cluster_info.json                 One file per collected endpoint, verbatim.
cluster_health.json
...
```

Files are ordered "broad → narrow" in the tar stream (cluster → nodes →
shards → features), so `tar -tzf` lists reads top-down from high-level
context. The manifest's `artifacts` array mirrors the same order.

### Examples

```sh
# Default bundle for support tickets.
esops ops diag

# Full bundle including mappings and slow-log settings.
esops ops diag --include-mappings --include-slowlog

# Targeted capture of an OpenSearch context.
esops --context local-os ops diag --out os-triage.tar.gz

# Inspect a bundle in place without unpacking.
tar -xOzf esops-diag-*.tar.gz manifest.json | jq '.artifacts[] | select(.status != 200)'

# Extract a single artifact for inspection.
tar -xOzf esops-diag-*.tar.gz nodes_hot_threads.txt | less
```

### Exit codes

`diag` follows the project-wide exit-code contract:

| Code | Meaning                                                      |
| ---- | ------------------------------------------------------------ |
| 0    | Bundle written; some artifacts may have non-2xx status but the collection completed. |
| 1    | Generic error (output file could not be opened, tar write failure, etc.). |
| 3    | Cluster unreachable.                                         |
| 4    | Authentication failed — stops early, does not write a half-empty bundle. |
| 5    | Authorization failed — same rationale as 4.                  |

A 400 or 404 from a single endpoint is recorded in `manifest.json` and
does not fail the run: a healthy cluster legitimately returns 400 on
`/_cluster/allocation/explain` (nothing is unassigned), and lifecycle
endpoints 404 on the wrong dialect. Scripts that need to gate on these
should iterate `manifest.artifacts[].status` rather than rely on the
exit code.

## `esops ops rebalance`

Submits a single `/_cluster/reroute` command — the manual lever for
shard movement when the cluster's automatic balancer isn't doing what
you need. The bread-and-butter use cases:

- **Drain a node before a reboot** — `move` a shard to another node.
- **Cancel a stuck recovery** — `cancel` an in-flight relocation that
  is making things worse, not better.
- **Manually allocate a replica** — `allocate-replica` when the
  balancer keeps refusing to place one (typical "yellow with no
  obvious reason" case).
- **Recover after a kicked-the-bucket primary** — `allocate-empty-primary`
  or `allocate-stale-primary` to restore service when the cluster
  cannot find a recoverable copy. Both can lose data; both require
  `--accept-data-loss`.
- **Retry max-retried allocations** — `retry-failed` after fixing the
  underlying issue (disk freed, plugin reinstalled, network restored).

```sh
$ esops --context local-es ops rebalance \
    --action move --index esops-logs-000001 --shard 0 \
    --from-node es01 --to-node es02
ACKNOWLEDGED  DRY_RUN  COMMANDS
true          false    1

ACTION  INDEX              SHARD  FROM  TO    FLAGS
move    esops-logs-000001  0      es01  es02  -
```

`rebalance` is the only `ops` subcommand that mutates cluster state.
Always pair the first invocation against an unfamiliar cluster with
`--dry-run`; the cluster simulates the routing change and reports
exactly what would happen, without applying it.

### Actions

Exactly one `--action` value is required.

| Action | Effect | Required flags |
| --- | --- | --- |
| `move` | Move a `STARTED` shard from one node to another. The balancer is allowed to override this later. | `--index`, `--shard`, `--from-node`, `--to-node` |
| `cancel` | Cancel an in-flight relocation/recovery on a specific node. | `--index`, `--shard`, `--from-node` (the node currently holding the copy). `--allow-primary` to cancel a primary's recovery. |
| `allocate-replica` | Allocate an unassigned replica on a chosen node. | `--index`, `--shard`, `--to-node` |
| `allocate-empty-primary` | Force-allocate an **empty** primary on a node. **Loses any data the cluster could have recovered.** | `--index`, `--shard`, `--to-node`, `--accept-data-loss` |
| `allocate-stale-primary` | Force-allocate a **known-stale** primary copy on a node. **Loses anything written after the stale copy.** | `--index`, `--shard`, `--to-node`, `--accept-data-loss` |
| `retry-failed` | Re-trigger allocation for shards that have hit `index.allocation.max_retries`. Body-less; targeting flags are rejected. | (none) |

### Flags

| Flag | Purpose |
| --- | --- |
| `--action NAME` (required) | One of the actions above. Underscored aliases (`allocate_replica`, `retry_failed`) match the cluster's wire names so a copy-paste from the docs also works. |
| `--index NAME` | Index to act on. Required for everything except `retry-failed`. |
| `--shard N` | Shard number. Defaults to `0`; `0` is a valid shard so the existence of `--index` (not `--shard 0`) is the gate. |
| `--from-node NAME` | Source node. Used by `move` and `cancel` (where it identifies the copy to cancel). |
| `--to-node NAME` | Destination node. Used by `move` and the three `allocate-*` actions. |
| `--allow-primary` | `cancel` only. Allow cancelling a primary's recovery; off by default because it's the riskier shape. |
| `--accept-data-loss` | Required for `allocate-empty-primary` / `allocate-stale-primary`. Verbose by design — grep-for-intent in shell history is a real auditing workflow. |
| `--explain` | Ask the cluster to attach its per-decider ruling for each command in the response. |
| `--dry-run` (global) | Submit the command with `dry_run=true`. The cluster simulates the cluster-state update and returns what would happen without applying it. |
| `--i-know-this-is-prod` (global) | Required when the resolved context has `protection: prod`. Refused with exit code 9 otherwise. |

The business layer rejects flag combinations that the cluster would
also reject — `move` without `--from-node`/`--to-node`, `move` with the
same `--from-node` and `--to-node`, `allocate-empty-primary` without
`--accept-data-loss`, `retry-failed` paired with shard-targeting flags
— so the round trip to the master is not wasted on shape errors.

### `--explain`

When the cluster refuses a command, `--explain` is the next step.
Asking the cluster to explain its decision attaches a per-decider
breakdown:

```sh
$ esops --context local-es ops rebalance --dry-run --explain \
    --action allocate-replica --index esops-stuck-replicas --shard 0 --to-node es01
ACKNOWLEDGED  DRY_RUN  COMMANDS
true          true     1

ACTION            INDEX                 SHARD  FROM  TO    FLAGS
allocate_replica  esops-stuck-replicas  0      -     es01  -

EXPLANATIONS
COMMAND           DECISIONS
allocate_replica  same_shard=no, throttling=throttle
```

The default table compresses the deciders list into one cell per
command (`-o wide` expands it into one row per decider with the full
explanation prose). Only `NO`/`THROTTLE` rulings are surfaced — `YES`
rulings are noise unless the operator asked for them.

`--explain` does **not** imply `--dry-run`: a real reroute can still
be acknowledged, and the explanation block tells you which deciders
allowed it. Pair the two for the safe-first read-only inspection.

### Output formats

| Flag | Intended for |
| --- | --- |
| `-o table` (def) | Human eyes. One header line (`ACKNOWLEDGED DRY_RUN COMMANDS`) plus a per-command table; `EXPLANATIONS` block when the cluster returned one. |
| `-o wide` | Same layout, but `EXPLANATIONS` expands to one row per decider with the full explanation prose. |
| `-o json` | Scripts. One top-level object: `acknowledged`, `dry_run`, `commands`, optional `explanations`. Stable schema. |
| `-o yaml` | Same shape as JSON. |

NDJSON is not supported: `rebalance` emits one structured outcome
report, not a stream of records.

### Dialect differences worth knowing

The CLI surface is identical on Elasticsearch and OpenSearch — both
expose `/_cluster/reroute` with the same body shape, the same query
flags (`dry_run`, `explain`, `retry_failed`), and the same per-action
keys (`move`, `cancel`, `allocate_replica`, `allocate_empty_primary`,
`allocate_stale_primary`). The only practical difference is wording in
the cluster's per-decider explanations — esops surfaces them verbatim
rather than rephrasing.

`metric=none` is set on every request so the response carries only
`acknowledged` (and `explanations[]` when asked). The cluster otherwise
returns the full routing-table dump, which can be many MB on large
clusters and is data esops doesn't surface.

### Examples

```sh
# Drain a node before a reboot — confirm with --dry-run, then commit.
esops ops rebalance --dry-run \
  --action move --index logs-2026.04 --shard 0 --from-node es03 --to-node es02
esops ops rebalance \
  --action move --index logs-2026.04 --shard 0 --from-node es03 --to-node es02

# Stuck unassigned replica — allocate it explicitly.
esops ops rebalance --explain \
  --action allocate-replica --index orders --shard 0 --to-node es02

# Cancel a relocation that is making things worse.
esops ops rebalance \
  --action cancel --index hot-index --shard 5 --from-node es01

# After freeing disk: retry every shard that hit max_retries.
esops ops rebalance --action retry-failed

# Last-resort recovery: data is gone on the lost node, accept the loss.
esops ops rebalance \
  --action allocate-empty-primary --index orphaned --shard 0 \
  --to-node es01 --accept-data-loss

# Production cluster: the protection tier requires the verbose opt-in.
esops --context prod-eu ops rebalance \
  --action move --index logs --shard 0 --from-node es-prod-01 --to-node es-prod-02 \
  --i-know-this-is-prod

# Cross-dialect: same command, OpenSearch.
esops --context local-os ops rebalance --dry-run --action retry-failed
```

### Exit codes

`rebalance` follows the project-wide exit-code contract:

| Code | Meaning |
| --- | --- |
| 0 | Cluster acknowledged the command (including a successful `--dry-run`). |
| 1 | Generic error — most commonly an HTTP 400 from the cluster (e.g. "shard not on that node"). The cluster's own reason string is in the error message. |
| 2 | Usage error — missing `--action`, conflicting flags. |
| 3 | Cluster unreachable. |
| 4 | Authentication failed (HTTP 401). |
| 5 | Authorization failed (HTTP 403). |
| 9 | Protected context refused the destructive operation (use `--dry-run` or `--i-know-this-is-prod`). |
| 130 | Interrupted (SIGINT). |

A cluster that returns `acknowledged: false` (the master rejected the
command without a 4xx — rare but possible) still exits 0; the JSON
payload carries the truth. Scripts asserting "the move actually
applied" should check `.acknowledged` rather than rely on the exit
code.

### What `ops rebalance` does **not** do

- **No multi-command batches.** The cluster supports submitting
  multiple commands in one request; `ops rebalance` deliberately sends
  one at a time. Single commands are easier to reason about in shell
  history, easier to undo, and easier to attribute when something goes
  wrong. Bulk operations (drain a whole node, rebalance a hot index
  across the cluster) are a future helper composed on top of this
  primitive.
- **No automatic balancing.** The cluster's allocator is the source of
  truth for "where should this shard live?". `ops rebalance` is the
  manual override for cases where the operator knows something the
  allocator doesn't — pending hardware swap, planned drain, recovery
  after a partial outage. If you find yourself reaching for it
  routinely, the underlying allocation rules are probably the wrong
  shape.
- **No `--force` escape hatch.** Each potentially destructive action
  has a purpose-built confirmation flag (`--accept-data-loss`,
  `--allow-primary`) so grep-for-intent works in shell history. That
  is a feature.
