```markdown
# ops — day-to-day cluster operations

`esops ops` provides `kubectl`-style commands for quick Elasticsearch / OpenSearch inspection and light maintenance.

All commands are read-only and safe to run anytime **except** `rebalance`, `drain`, and `uncordon`, which modify cluster state and require `--i-know-this-is-prod` on protected environments.

See [config.md](./config.md) for context resolution, authentication, and protection settings.

**Commands**
| Command                | Purpose                                      |
|------------------------|----------------------------------------------|
| `health`               | Cluster status summary                       |
| `nodes`                | Node list with role, version, heap, disk     |
| `shards`               | Shard allocation table                       |
| `allocation-explain`   | Why a shard is unassigned or placed          |
| `audit`                | Security posture (TLS + users/roles)         |
| `hot-threads`          | JVM hot threads                              |
| `diag`                 | Diagnostic bundle (tarball)                  |
| `rebalance`            | Manual shard move / cancel / allocate        |
| `drain`                | Exclude a node from allocation               |
| `uncordon`             | Remove a node's allocation exclusion         |
| `tasks list`           | List in-flight cluster tasks                 |
| `tasks cancel`         | Cancel a task by ID, or a filtered set       |

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

Lists cluster nodes from `/_cat/nodes`. Default columns answer the questions an on-call asks first: who's in the cluster, what version, how much heap and disk are they sitting on.

```sh
$ esops ops nodes
context=local-es dialect=elasticsearch version=9.3.3 nodes=3
NAME  ROLES                  VERSION  HEAP%  DISK_USED  DISK_AVAIL  DISK%
es01  data,master            9.3.3    42%    100.0gb    900.0gb     10%
es02  data                   9.3.3    38%    98.5gb     901.5gb     10%
es03  master                 9.3.3    12%    -          -           -
```

`-o wide` adds the master flag (`*` for the elected master), IP, CPU, 1m load, and uptime.

```sh
$ esops ops nodes -o wide
NAME  ROLES        VERSION  IP         MASTER  HEAP%  HEAP_USED  HEAP_MAX  DISK_USED  DISK_AVAIL  DISK%  CPU  LOAD_1M  UPTIME
es01  data,master  9.3.3    10.0.0.1   *       42%    2.0gb      8.0gb     100.0gb    900.0gb     10%    4%   0.5      3d
```

Coordinating-only nodes legitimately omit disk/heap columns — the cluster does not report them, so the cell renders as `-`. JSON / YAML / NDJSON output preserves the full neutral `Node` shape; `omitempty` keeps absent values out of the encoded record.

**Common use cases**
- Check version skew across the cluster before an upgrade
- Spot a node sitting near the disk high watermark before allocation stops
- See the freshly-added or drained data node that holds zero shards (invisible to `ops shards`)

Works identically on Elasticsearch and OpenSearch. The same `/_cat/nodes` pull powers `ops shards analyze` for skew detection.

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

## esops ops drain

Adds one or more nodes to `cluster.routing.allocation.exclude._name`. The cluster refuses to assign new shards to the named nodes and starts relocating the existing ones away — the canonical "kubectl drain" equivalent for Elasticsearch / OpenSearch.

Multi-node drains write a single PUT — adding three nodes is one request, not three. The wait loop counts shards across the entire set and reports a per-node breakdown.

```sh
# Pre-flight: see the would-be transition without touching cluster state
$ esops ops drain --node es03 --dry-run
ACTION  NODES  SCOPE      DRY_RUN  ACK    ALREADY_EXCLUDED  WAITED  SHARDS_LEFT
drain   es03   transient  true     false  -                 false   0

EXCLUDE._NAME
  before: -
  after:  es03

# Multi-node drain (e.g. retiring a rack), block until both are empty
$ esops ops drain --node es03,es04 --wait --wait-timeout 1h
ACTION  NODES      SCOPE      DRY_RUN  ACK   ALREADY_EXCLUDED  WAITED  SHARDS_LEFT
drain   es03,es04  transient  false    true  -                 true    0

NODE  SHARDS_LEFT
es03  0
es04  0

# Master-eligible node retirement: also exclude from voting config
$ esops ops drain --node es01 --also-exclude-voting --persistent
ACTION  NODES  SCOPE       DRY_RUN  ACK   ALREADY_EXCLUDED  WAITED  SHARDS_LEFT
drain   es01   persistent  false    true  -                 false   12

VOTING_CONFIG_EXCLUSIONS
  added:        es01
  acknowledged: true

EXCLUDE._NAME
  before: -
  after:  es01

# Read-only listing: what's drained right now?
$ esops ops drain --list
ALLOCATION_EXCLUDE
  persistent: es01
  transient:  -

VOTING_CONFIG_EXCLUSIONS
  nodes: es01
```

**Flags**
| Flag                    | Purpose                                                                    |
|-------------------------|----------------------------------------------------------------------------|
| `--node NAME[,NAME...]` | Node name(s) to drain. Repeatable, or comma-separated. Required unless `--list` |
| `--persistent`          | Use the persistent settings scope (survives master restart)                |
| `--wait`                | Block until the named nodes hold zero shards                               |
| `--wait-timeout D`      | Cap the wait (default `30m`; `0` = no timeout)                             |
| `--wait-interval D`     | Poll cadence while waiting (default `5s` — matches the allocator)          |
| `--also-exclude-voting` | Also POST to `/_cluster/voting_config_exclusions` (safe master retirement) |
| `--list`                | Read-only: print current drained nodes + voting exclusions, then exit. Mutually exclusive with `--node` |

**Behaviour**
- Refuses if any named node is not currently in the cluster (typo guard — the cluster accepts any string for `exclude._name`). All unknown names are reported in one error so you fix typos in one pass.
- **Per-node idempotent.** Names already in the exclude list are recorded under `ALREADY_EXCLUDED` and not re-added. When *every* requested node is already excluded, no PUT is issued.
- Default scope is **transient** (clears on master restart — the safer choice for a temporary drain). `--persistent` flips it to persistent; pair with `ops uncordon --persistent` to remove later.
- `--also-exclude-voting` runs *after* the allocation PUT acknowledges, so the voting state never leads the allocation state. Non-master nodes are accepted as a no-op by the cluster, so the flag is safe to set on a mixed-role drain. A failure of the voting POST is surfaced as a partial-success result + non-zero exit (allocation landed, voting did not).
- The result always carries the latest shard count plus a per-node breakdown so you see "es03 done, es04 still has 12" without a second invocation.

**Common use cases**
- Quiesce a node before a rolling restart or hardware swap
- Retire a master-eligible node safely (`--also-exclude-voting --persistent`)
- Drain a whole rack: `--node es01,es02,es03 --wait`
- Pair with `ops shards --node es03` to track drain progress in another terminal

> **GET-then-PUT race.** The drain reads `/_cluster/settings` and writes back the merged list. Two operators draining different nodes concurrently can clobber each other's exclude list. The cluster has no CAS primitive for settings; in practice a runbook-level lock is sufficient.

Mutates cluster state — `--dry-run` first; production contexts require `--i-know-this-is-prod`.

---

## esops ops uncordon

The inverse of `drain`: removes nodes from `cluster.routing.allocation.exclude._name`. Idempotent at the per-node level — names not in the list are recorded under `NOT_IN_LIST` and the cluster is not touched on their account.

```sh
$ esops ops uncordon --node es03
ACTION    NODES  SCOPE      DRY_RUN  ACK   NOT_IN_LIST  VOTING_CLEARED
uncordon  es03   transient  false    true  -            false

EXCLUDE._NAME
  before: es03
  after:  -

# End-of-maintenance cleanup: bring nodes back AND wipe stale voting exclusions
$ esops ops uncordon --node es01,es02 --persistent --clear-voting
ACTION    NODES      SCOPE       DRY_RUN  ACK   NOT_IN_LIST  VOTING_CLEARED
uncordon  es01,es02  persistent  false    true  -            true

EXCLUDE._NAME
  before: es01, es02
  after:  -

# Just clear stale voting exclusions, no allocation change
$ esops ops uncordon --clear-voting
ACTION    NODES  SCOPE      DRY_RUN  ACK   NOT_IN_LIST  VOTING_CLEARED
uncordon  -      transient  false    true  -            true
```

**Flags**
| Flag                    | Purpose                                                                |
|-------------------------|------------------------------------------------------------------------|
| `--node NAME[,NAME...]` | Node name(s) to remove from the exclude list. Repeatable, or comma-separated |
| `--persistent`          | Operate on the persistent scope (must match the drain scope)           |
| `--clear-voting`        | Also `DELETE /_cluster/voting_config_exclusions` (cluster-wide; the API only supports clear-all) |

Either `--node` or `--clear-voting` (or both) must be set.

**Behaviour**
- Skips the "node still in cluster" check the drain path enforces — a node may already have been decommissioned, leaving an exclusion behind. Cleaning that up is exactly what `uncordon` is for.
- The `--persistent` flag must match the scope the drain used. A node drained transiently and uncordoned persistently produces a clear `NOT_IN_LIST` outcome rather than a silent no-op.
- `--clear-voting` is **cluster-wide** because the cluster API only supports clear-all (no per-node delete). If only some entries are stale, the operator must accept the all-or-nothing trade-off.

**Common use cases**
- Bring a drained node back online after a maintenance window: `--node es03`
- End-of-rolling-restart sweep: `--node es01,es02,es03 --clear-voting`
- Clean up stale exclusions left behind by decommissioned nodes
- Pure voting-cleanup: `--clear-voting` without `--node`

Mutates cluster state — `--dry-run` first; production contexts require `--i-know-this-is-prod`.

---

## esops ops tasks list

Lists in-flight cluster tasks from `GET /_tasks?group_by=none` — reindex, force-merge, search, snapshot, and any other transport task the cluster reports. Broader than `migrate reindex --resume`: anything visible in `/_tasks` is listed here.

```sh
$ esops ops tasks list
ID        NODE   ACTION                       RUNNING  CANCELLABLE
es01:42   es01   indices:data/write/reindex   1m30s    true
es02:7    es02   indices:admin/forcemerge     45s      false
```

`-o wide` adds the description (only populated with `--detailed`), the parent task ID, and the start time.

**Filters**
| Flag                  | Purpose                                                                  |
|-----------------------|--------------------------------------------------------------------------|
| `--actions`           | Cluster action name(s). Repeatable / comma-separated. Wildcards allowed (`*search*`, `indices:data/write/*`) |
| `--nodes`             | Node name(s) or ID(s). Repeatable / comma-separated                      |
| `--parent-task-id`    | Composite `<node_id>:<task_number>` — limits to descendants of one task  |
| `--detailed`          | Ask the cluster for the per-task description (slower; useful with `-o wide`) |

**Common use cases**
- `esops ops tasks list` – quick "what's hot right now" sweep
- `esops ops tasks list --actions 'indices:data/write/reindex'` – every running reindex across the fleet
- `esops ops tasks list --parent-task-id es01:42 -o wide` – descendants of a known task (e.g. all child shards of one reindex)
- `esops ops tasks list -o ndjson | jq …` – stream rows as the cluster's response is decoded; rows reach stdout one at a time so a fleet running thousands of concurrent tasks doesn't have to be buffered before the first row prints. Sort order is the cluster's iteration order on this path; pipe through `sort` if you need stable output.

Filtering is server-side. Works identically on Elasticsearch and OpenSearch.

> **Not on `/_tasks`:** `snapshot create` (after acceptance) does not show up here — the cluster runs snapshot shard work outside the transport-task framework. Monitor those via `snapshot list` / `snapshot create --wait` instead. See [`snapshot.md`](./snapshot.md#watching-in-flight-snapshot-operations).

---

## esops ops tasks cancel

Cancel one or more tasks by composite ID (positional arguments, the same shape `tasks list` prints) or a filtered set via `--actions`/`--nodes`/`--parent-task-id`. The two modes are mutually exclusive.

```sh
# Cancel one task by its ID
$ esops ops tasks cancel es01:42
ACTION  TARGET    DRY_RUN  WAIT   CANCELLED  NODE_FAILURES
cancel  es01:42   false    false  1          0

ID       NODE  ACTION                       RUNNING  CANCELLABLE
es01:42  es01  indices:data/write/reindex   1m30s    true

# Cancel a small batch — repeated positionals or comma-separated, both work.
# The cluster has no batched cancel endpoint, so the call fans out to one
# POST per ID; partial successes are aggregated in the result.
$ esops ops tasks cancel es01:42 es02:99
$ esops ops tasks cancel es01:42,es02:99,es03:7
ACTION  TARGET                    DRY_RUN  WAIT   CANCELLED  NODE_FAILURES
cancel  es01:42,es02:99,es03:7    false    false  3          0

# Bulk cancel by action filter (preview first)
$ esops ops tasks cancel --actions 'indices:data/write/reindex' --dry-run
ACTION  TARGET  DRY_RUN  WAIT   CANCELLED  NODE_FAILURES
cancel  filter  true     false  3          0
…

# Live bulk cancel, blocking until the cluster reports the cancel landed
$ esops ops tasks cancel --actions 'indices:data/write/reindex' --wait
```

**Flags**
| Flag                  | Purpose                                                                  |
|-----------------------|--------------------------------------------------------------------------|
| (positional)          | One or more composite task IDs `<node_id>:<task_number>` (repeated args or comma-separated) |
| `--actions`           | Cancel tasks matching cluster action name(s). Wildcards allowed           |
| `--nodes`             | Cancel tasks running on these node(s)                                    |
| `--parent-task-id`    | Cancel tasks whose parent matches this composite ID                      |
| `--wait`              | Forward `wait_for_completion=true` (block until the cancel lands)        |

**Behaviour**
- An empty invocation is **refused**. `POST /_tasks/_cancel` with no filter would target every cancellable task in the cluster — explicitly out of scope.
- A malformed task ID (no `:`) is rejected up-front, **before** any per-ID POST fires, so a typo on the third of five IDs cannot leave the first two cancelled.
- Multi-ID calls fan out to one `POST /_tasks/<id>/_cancel` per ID (the cluster has no batched cancel endpoint). Cancelled tasks and node failures are aggregated into a single result; if a mid-fan-out call fails, the partial success is rendered first and the error names the failing ID.
- The cluster silently ignores cancel signals on tasks it considers non-cancellable (`force_merge`, snapshot create, several internal transports). `--dry-run` lists the matched tasks first and refuses with **exit code 7** if any are not cancellable, surfacing the mismatch before the live run.
- The cluster echoes the cancelled set in the response, so the per-task list shown after the header is the cluster's own view of what got the signal.
- Per-node failures (e.g. one node refused to commit the cancel state) are surfaced verbatim in a `NODE_FAILURES` section rather than masking them under a single error.
- The TARGET cell collapses to `<N> ids` for >3 IDs to keep the column readable; the full slice is always available in `-o json` / `-o yaml`.

**Common use cases**
- Cancel a runaway reindex: `esops ops tasks cancel <id>`
- Cancel a batch of stale reindexes: `esops ops tasks list --actions 'indices:data/write/reindex' -o json | jq -r '.[].id' | xargs esops ops tasks cancel`
- Roll back a botched mass-reindex: `esops ops tasks cancel --actions 'indices:data/write/reindex' --dry-run` then re-run live

Mutates cluster state — `--dry-run` first; production contexts require `--i-know-this-is-prod`.

---

**All commands** support `--context`, `--url`, `--output` (json/table/wide), and global timeout / auth flags.  
The `context=…` line always prints to stderr so pipelines stay clean.
```