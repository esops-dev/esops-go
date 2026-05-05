# ops — day-to-day cluster operations

`esops ops` provides `kubectl`-style commands for quick Elasticsearch / OpenSearch inspection and light maintenance.

All commands are read-only and safe to run anytime **except** `rebalance`, `drain`, `uncordon`, and `tasks cancel`, which modify cluster state and require `--i-know-this-is-prod` on protected environments.

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

One-command security posture check: TLS settings, security feature status, native users and roles.

```sh
$ esops --context local-es-mtls ops audit
TLS
  scheme:     https
  insecure:   false
  auth:       mtls

SECURITY
  dialect:  elasticsearch
  enabled:  true
```

**Common use cases**
- Verify who can access the cluster
- Audit for user-defined roles with broad permissions

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

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run`, and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.