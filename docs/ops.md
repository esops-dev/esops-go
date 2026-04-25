```markdown
# ops — day-to-day cluster operations

`esops ops` provides `kubectl`-style commands for quick Elasticsearch / OpenSearch inspection and light maintenance.

All commands are read-only and safe to run anytime **except** `rebalance`, which modifies cluster routing and requires `--i-know-this-is-prod` on protected environments.

See [config.md](./config.md) for context resolution, authentication, and protection settings.

**Commands**
| Command                | Purpose                                      |
|------------------------|----------------------------------------------|
| `health`               | Cluster status summary                       |
| `shards`               | Shard allocation table                       |
| `allocation-explain`   | Why a shard is unassigned or placed          |
| `audit`                | Security posture (TLS + users/roles)         |
| `hot-threads`          | JVM hot threads                              |
| `diag`                 | Diagnostic bundle (tarball)                  |
| `rebalance`            | Manual shard move / cancel / allocate        |

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

Works identically on Elasticsearch and OpenSearch.

---

## esops ops allocation-explain

Explains why the cluster placed or refused to place a shard (`/_cluster/allocation/explain`).

Two modes:
- No flags → cluster picks first unassigned shard
- Targeted → `--index`, `--shard`, `--primary`

```sh
$ esops ops allocation-explain --index esops-users --primary
context=local-es dialect=elasticsearch version=9.3.3
INDEX        SHARD  ROLE  STATE    NODE  CAN_ALLOCATE
esops-users  0      p     started  es01  -
```

**Useful flags**
- `--include-disk-info`
- `--include-yes-decisions`
- `--explain` (adds per-decider breakdown)

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

USERS / ROLES tables follow…
```

**Common use cases**
- Verify who can access the cluster
- Audit for user-defined roles with broad permissions
- Quick check before sharing cluster access

Gracefully handles security-disabled clusters. Works on both dialects (minor field differences only).

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

Output is the cluster’s native text format.

---

## esops ops diag

Collects a diagnostic bundle as a gzipped tarball (cluster health, shards, nodes, hot threads, policies, etc.).

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

Manual `/_cluster/reroute` for shard movement (the **only** mutating command).

**Required flag**  
`--action` (one of): `move`, `cancel`, `allocate-replica`, `allocate-empty-primary`, `allocate-stale-primary`, `retry-failed`

**Examples**
```sh
# Drain node before reboot
esops ops rebalance --action move --index logs-2026.04 --shard 0 --from-node es03 --to-node es02

# Fix stuck replica
esops ops rebalance --action allocate-replica --index orders --shard 0 --to-node es02

# Last resort (data loss)
esops ops rebalance --action allocate-empty-primary --index orphaned --shard 0 --to-node es01 --accept-data-loss
```

Always test first with `--dry-run`. Production contexts also require `--i-know-this-is-prod`.

**Common use cases**
- Node drain / reboot
- Stuck recoveries or unassigned replicas
- Retry failed allocations after fixing the root cause

---

**All commands** support `--context`, `--url`, `--output` (json/table/wide), and global timeout / auth flags.  
The `context=…` line always prints to stderr so pipelines stay clean.
```