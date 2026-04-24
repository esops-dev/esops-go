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
