# ops — day-to-day cluster operations

`esops ops` provides `kubectl`-style commands for quick Elasticsearch / OpenSearch inspection and light maintenance.

All commands are read-only and safe to run anytime **except** `rebalance`, `drain`, `uncordon`, `tasks cancel`, `settings set`, `unblock`, and `cache-clear`, which modify cluster state and require `--i-know-this-is-prod` on protected environments.

See [config.md](./config.md) for context resolution, authentication, and protection settings.

**Commands**
| Command                | Purpose                                      |
|------------------------|----------------------------------------------|
| `health`               | Cluster status summary                       |
| `nodes`                | Node list with roles, heap, disk             |
| `shards`               | Shard allocation table                       |
| `allocation-explain`   | Why a shard is unassigned or placed          |
| `audit`                | Security posture (TLS + users/roles)         |
| `hot-threads`          | JVM hot threads                              |
| `diag`                 | Diagnostic bundle (tarball)                  |
| `rebalance`            | Manual shard move / cancel / allocate        |
| `drain`                | Exclude node(s) from allocation              |
| `uncordon`             | Remove node allocation exclusion             |
| `tasks list`           | List in-flight cluster tasks                 |
| `tasks cancel`         | Cancel task(s) by ID or filter               |
| `pending-tasks`        | Master’s pending cluster-state queue         |
| `deprecations`         | Pre-upgrade deprecation warnings (ES only)   |
| `settings get`         | Cluster settings (persistent + transient)    |
| `settings set`         | Update cluster settings by key/value or body |
| `settings diff`        | Drift between persistent and transient scope |
| `unblock`              | Clear an index block (e.g. disk-watermark)   |
| `cache-clear`          | Clear fielddata / query / request caches    |
| `realms`               | Configured authentication realms             |
| `remote`               | Cross-cluster remotes + CCR follower status  |
| `license`              | Installed commercial licence (ES only)       |

---

## esops ops health

Prints a compact cluster health summary from `/_cluster/health`.

```sh
$ esops ops health
context=local-es dialect=elasticsearch version=9.3.3
NAME          STATUS  NODES  DATA_NODES  SHARDS  UNASSIGNED  PENDING
esops-dev-es  green   2      2           3       0           0
```

**Common use cases**
- Quick “is the cluster green?” check
- First command when paged at 3 a.m.

---

## esops ops nodes

Lists nodes from `/_cat/nodes` with the columns on-call engineers need first.

```sh
$ esops ops nodes
context=local-es dialect=elasticsearch version=9.3.3 nodes=3
NAME  ROLES                  VERSION  HEAP%  DISK_USED  DISK_AVAIL  DISK%
es01  data,master            9.3.3    42%    100.0gb    900.0gb     10%
```

`-o wide` adds master flag, IP, CPU, load, and uptime.

**Common use cases**
- Check version skew before upgrades
- Spot nodes near disk watermarks
- Verify a newly-added or drained node

Works identically on Elasticsearch and OpenSearch.

---

## esops ops shards

Shows shard allocation from `/_cat/shards`. Run this first when the cluster turns yellow.

```sh
$ esops ops shards
context=local-es dialect=elasticsearch version=9.3.3 shards=3
INDEX              SHARD  ROLE  STATE    STORE   NODE
esops-logs-000001  0      p     STARTED  34.2kb  es01
```

**Filters**
| Flag           | Purpose                              |
|----------------|--------------------------------------|
| `--index`      | Comma-separated patterns             |
| `--state`      | `STARTED`, `UNASSIGNED`, …           |
| `--node`       | Exact node name                      |
| `--unassigned` | Shortcut for `--state UNASSIGNED`    |
| `--primary`    | Primaries only                       |
| `--replica`    | Replicas only                        |

**Common use cases**
- `esops ops shards --unassigned`
- `esops ops shards --node es01 -o json`
- `esops ops shards --index logs-* --primary`

---

## esops ops allocation-explain

Explains why the cluster placed or refused a shard (`/_cluster/allocation/explain`).

```sh
$ esops ops allocation-explain --index esops-users --primary
context=local-es dialect=elasticsearch version=9.3.3
INDEX        SHARD  ROLE  STATE    NODE  CAN_ALLOCATE
esops-users  0      p     started  es01  -
```

**Useful flags**
- `--include-disk-info`
- `--include-yes-decisions`
- `--explain` (per-decider breakdown)

**Common use cases**
- Diagnose yellow cluster / unassigned shards
- Understand why a shard cannot move

---

## esops ops audit

One-command security posture check: TLS settings, security feature status, anonymous-access posture, native users and roles.

```sh
$ esops --context local-es-mtls ops audit
TLS
  scheme:     https
  insecure:   false
  auth:       mtls

SECURITY
  dialect:  elasticsearch
  enabled:  true

ANONYMOUS
  enabled:         false
  username:        _anonymous
  source:          default
```

The `ANONYMOUS` block reports whether unauthenticated requests are accepted, the identity they would inherit, and which cluster-settings scope (`transient`, `persistent`, `default`) supplied the effective value. Section is omitted when the cluster has security disabled or the audit user can't read `/_cluster/settings`.

**Common use cases**
- Verify who can access the cluster
- Audit for user-defined roles with broad permissions
- Detect anonymous access left enabled on a production cluster

Gracefully handles security-disabled clusters. Works on both dialects.

---

## esops ops hot-threads

Shows JVM hot threads from `/_nodes/hot_threads`.

```sh
$ esops ops hot-threads --threads 1 --snapshots 2
::: {es01}...
   Hot threads at …
```

**Flags**
| Flag            | Purpose                          |
|-----------------|----------------------------------|
| `--node NAME`   | Limit to specific node(s)        |
| `--threads N`   | Busy threads per node (default 3)|
| `--type`        | `cpu` (default), `wait`, `block` |
| `--show-idle`   | Include idle threads             |

**Common use cases**
- CPU pinned or high latency
- Thread contention or I/O waits

---

## esops ops diag

Collects a diagnostic bundle as a gzipped tarball (health, shards, nodes, hot threads, policies, etc.).

```sh
$ esops ops diag
wrote esops-diag-esops-dev-es-20260424T190352Z.tar.gz (18 artifacts, 0 failed)
```

**Opt-in flags**
- `--include-slowlog`
- `--include-mappings`

**Common use cases**
- Support ticket bundles
- `esops ops diag --out - | gh issue create …`

Bundle contains only cluster responses — no credentials.

---

## esops ops rebalance

Manual `/_cluster/reroute` for shard movement.

**Required flag**  
`--action` (one of): `move`, `cancel`, `allocate-replica`, `allocate-empty-primary`, `allocate-stale-primary`, `retry-failed`

**Examples**
```sh
# Drain node before reboot
esops ops rebalance --action move --index logs-2026.04 --shard 0 --from-node es03 --to-node es02

# Fix stuck replica
esops ops rebalance --action allocate-replica --index orders --shard 0 --to-node es02
```

Always test first with `--dry-run`. Production contexts require `--i-know-this-is-prod`.

---

## esops ops drain

Excludes node(s) from allocation (`cluster.routing.allocation.exclude._name`).

```sh
$ esops ops drain --node es03 --dry-run
```

**Flags**
- `--node NAME[,NAME...]` (required unless `--list`)
- `--persistent`
- `--wait`
- `--also-exclude-voting` (for master retirement)
- `--list` (read-only view of current exclusions)

**Common use cases**
- Quiesce a node before restart or hardware swap
- Drain a whole rack: `--node es01,es02,es03 --wait`
- Safe master retirement: `--also-exclude-voting --persistent`

Mutates cluster state — use `--dry-run` first.

---

## esops ops uncordon

Removes nodes from allocation exclusion (inverse of `drain`).

```sh
$ esops ops uncordon --node es03
```

**Flags**
- `--node NAME[,NAME...]`
- `--persistent`
- `--clear-voting` (removes voting config exclusions)

**Common use cases**
- Bring drained node back online after maintenance
- End-of-rolling-restart cleanup: `--node es01,es02,es03 --clear-voting`

Mutates cluster state — use `--dry-run` first.

---

## esops ops tasks list

Lists in-flight cluster tasks (`/_tasks`).

```sh
$ esops ops tasks list
ID        NODE   ACTION                       RUNNING  CANCELLABLE
es01:42   es01   indices:data/write/reindex   1m30s    true
```

**Filters**
- `--actions` (wildcards supported)
- `--nodes`
- `--parent-task-id`
- `--detailed`

**Common use cases**
- Quick “what’s running?” sweep
- `esops ops tasks list --actions 'indices:data/write/reindex'`

---

## esops ops tasks cancel

Cancels task(s) by ID or filter.

```sh
$ esops ops tasks cancel es01:42
```

**Flags**
- Positional task IDs or `--actions` / `--nodes` / `--parent-task-id`
- `--wait`

**Common use cases**
- Cancel a runaway reindex
- Bulk cancel: `esops ops tasks list … | xargs esops ops tasks cancel`

Mutates cluster state — use `--dry-run` first.

---

## esops ops pending-tasks

Shows the master’s pending cluster-state queue.

```sh
$ esops ops pending-tasks
ORDER  PRIORITY  IN_QUEUE  EXECUTING  SOURCE
12     URGENT    230ms     true       create-index [logs-2026.05.05]
```

**Common use cases**
- Diagnose a slow or stuck master
- `esops ops pending-tasks -o json | jq '[.[] | select(.time_in_queue_millis > 30000)]'`

Read-only.

---

## esops ops deprecations

Lists pre-upgrade deprecation warnings (Elasticsearch only).

```sh
$ esops ops deprecations
LEVEL     CATEGORY          TARGET     MESSAGE
critical  cluster_settings  -          setting cluster.foo will be removed in 9.x
```

**Common use cases**
- Pre-upgrade check: `esops ops deprecations -o json | jq '[.[] | select(.level=="critical")]'`

---

## esops ops settings get

Reads `/_cluster/settings` (with `flat_settings=true`) and prints the persistent and transient scopes side by side.

```sh
$ esops ops settings get
SCOPE       KEY                                             VALUE
persistent  cluster.routing.allocation.enable               primaries
transient   cluster.routing.allocation.disk.watermark.high  85%
```

**Flags**
- `--name` — repeatable; restrict output to keys whose dotted path begins with the given prefix (e.g. `cluster.routing.allocation`).
- `--include-defaults` — also include the cluster's built-in default values (`?include_defaults=true`).

**Common use cases**
- Drift check after an incident
- Pre-upgrade snapshot of operator-set keys: `esops ops settings get -o json > before.json`

Read-only. Works on both dialects.

---

## esops ops settings set

Updates `/_cluster/settings` in either the persistent or transient scope. The default scope is **transient** — values clear on master restart, the safer default for an emergency override.

```sh
# transient by default
esops ops settings set --set cluster.routing.allocation.enable=primaries

# persist a value across restarts
esops ops settings set --persistent --set cluster.routing.allocation.enable=primaries

# remove a key (cluster honours JSON null as 'remove')
esops ops settings set --persistent --body '{"cluster.routing.allocation.enable":null}'
```

**Body shapes** (mutually exclusive)
- `--set key=value` — repeatable; bare keys are forwarded as-is (no `index.` auto-prefix — cluster-settings keys span multiple top-level prefixes: `cluster.`, `action.`, `indices.`, `logger.`).
- `--body JSON` — bare key/value object. The surrounding `"persistent"`/`"transient"` wrapper is **rejected** here; pick scope with `--persistent`.
- `--from-file PATH` — read the JSON object from a file.

**Other flags**
- `--persistent` — operate on the persistent scope.
- `--dry-run` — client-side validate + report; the cluster has no native dry-run for `/_cluster/settings`.

**Common use cases**
- Quiesce shard movement before maintenance: `--set cluster.routing.allocation.enable=primaries`
- Roll back a stuck setting: `--body '{"cluster.foo":null}'`
- CI-driven config change with a JSON file rendered upstream

Mutates cluster state. Production contexts require `--i-know-this-is-prod`.

---

## esops ops settings diff

Reports keys whose **persistent** and **transient** values disagree (including keys set in only one scope). A key set the same in both scopes is omitted: the diff is for finding drift.

```sh
$ esops ops settings diff
KEY                                PERSISTENT  TRANSIENT
cluster.routing.allocation.enable  -           primaries
```

When persistent and transient match exactly the renderer prints `no persistent/transient drift`.

**Common use cases**
- Confirm an operator forgot to re-flip a transient kill-switch back to persistent
- Pre-upgrade hygiene before a master restart that will drop transient values

Read-only.

---

## esops ops unblock

Clears a cluster-side index block by writing JSON null to the matching `index.blocks.<name>` setting. Default block is **`read_only_allow_delete`** — the disk-watermark block the cluster auto-applies under disk pressure and does *not* auto-clear when the disk returns under threshold.

```sh
# typical: clear the disk-watermark block on this week's logs
esops ops unblock --index 'logs-2026.05.*'

# or any other named cluster block
esops ops unblock --index archive-2023 --block read_only
```

**Flags**
- `--index NAME[,NAME...]` (required; `*` / `_all` refused — name a pattern).
- `--block NAME` — one of `read_only_allow_delete` (default), `read_only`, `write`, `metadata`, `read`. Other names are rejected at validation time.
- `--ignore-unavailable` — treat missing indices as a no-op instead of a 404.
- `--dry-run` — client-side echo; cluster has no native dry-run for `/_settings`.

**Common use cases**
- Recovery after the disk-watermark trip ("indices went read-only at 90% disk and stayed there")
- End-of-incident sweep across affected indices

Mutates cluster state. Production contexts require `--i-know-this-is-prod`.

---

## esops ops cache-clear

Clears caches via `POST /<indices>/_cache/clear`. Without any of `--fielddata` / `--query` / `--request` the cluster's default applies (clear every cache on the named indices).

```sh
# clear every cache for matching indices
esops ops cache-clear --index 'logs-2026.05.*'

# only the query and request caches
esops ops cache-clear --index 'logs-*' --query --request

# fielddata flush for specific fields
esops ops cache-clear --index 'logs-*' --fielddata --fields user.id,trace.id
```

**Flags**
- `--index NAME[,NAME...]` (required; `*` / `_all` refused).
- `--fielddata` / `--query` / `--request` — per-cache toggles.
- `--fields NAME[,NAME...]` — narrow fielddata clearing; requires `--fielddata`.
- `--ignore-unavailable` — treat missing indices as a no-op instead of a 404.
- `--dry-run` — client-side echo; cluster has no native dry-run for `_cache/clear`.

The output table reports `SHARDS_OK / SHARDS_TOTAL` and a `SHARDS_FAILED` count so a partial failure is grep-able.

**Common use cases**
- After a mapping change, free fielddata heap pinned to the old shape
- Reclaim heap on a hot data tier before rolling restart

Mutates cluster state (heap and disk-cache state, not data). Production contexts require `--i-know-this-is-prod`. Use carefully on busy clusters: a fielddata flush evicts heap in one go.

## esops ops remote

Lists configured remote clusters (`/_remote/info`), CCR follower lag (`/_ccr/stats`), and configured CCR auto-follow patterns (`/_ccr/auto_follow`).

```sh
$ esops ops remote
NAME       MODE   CONNECTED  NODES  SKIP_UNAVAIL
leader-eu  sniff  true       3      false

FOLLOWERS (ok)
INDEX             LEADER     CLUSTER    LAG_OPS  PAUSED
logs-eu-follower  logs-eu    leader-eu  4        false

AUTO_FOLLOW (ok)
NAME      REMOTE     LEADER_PATTERNS  FOLLOW_PATTERN              ACTIVE
eu-logs   leader-eu  logs-eu-*        {{leader_index}}-follower   true
```

The `FOLLOWERS` and `AUTO_FOLLOW` headers carry a status hint that explains an empty table:

- `(ok)` — CCR is licensed and the table reflects every entry, including zero-entry clusters.
- `(unsupported)` — OpenSearch dialect, or 404 on `/_ccr/*` from a basic-licence Elasticsearch cluster.
- `(forbidden)` / `(unauthorized)` — the auth user lacks privilege on `/_ccr/stats`. The CCS half still renders.

When `FOLLOWERS` is anything other than `ok`, the `AUTO_FOLLOW` section is suppressed (its absence is implicit). On a healthy cluster where only the auto-follow call diverges (e.g. a proxy strips the path), the section header reads `AUTO_FOLLOW (error)` with the underlying reason inline.

`-o wide` adds seed hosts (CCS) and last-read-failure messages (CCR).

**Common use cases**
- Verify cross-cluster search (CCS) connectivity
- Monitor CCR lag and pause state
- Audit auto-follow patterns before a cutover

CCS works on both dialects; CCR is Elasticsearch-only.

---

## esops ops license

Shows the cluster's installed commercial licence (Elasticsearch only).

```sh
$ esops ops license
STATUS  TYPE   ISSUED_TO   EXPIRES               MAX_NODES
active  trial  ACME Corp   2026-06-06T06:34:41Z  1000

# CI guard: exit non-zero if the licence is within 30 days of expiry
$ esops ops license --warn-days 30
STATUS  TYPE     ISSUED_TO   EXPIRES               MAX_NODES
active  platinum ACME Corp   2026-05-20T00:00:00Z  100
license expires in 13 days (2026-05-20T00:00:00Z) — within --warn-days=30 threshold
$ echo $?
1
```

**Flags**

- `--warn-days N` — exit non-zero (code 1) when `ExpiresAt` is within N days of now. Already-expired licences trigger too. Basic licences (no expiry) are never flagged. Default 0 (disabled).

`-o wide` adds issuer, issue date, and UID.

**Common use cases**

- Pre-upgrade check: `esops ops license -o json | jq .expires_at`
- Capacity planning: confirm `max_nodes` against the actual node count from `esops ops nodes`
- CI guard: `esops ops license --warn-days 30` in a daily scheduled job

OpenSearch returns "operation not supported" (exit code 6).

---

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run`, and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.