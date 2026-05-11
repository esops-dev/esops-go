# snapshot — inspect and manage snapshots

`esops snapshot` provides `kubectl`-style commands for snapshots, repositories, and policies: listing, creating, verifying, restoring, pruning, plus nested `repository` and `policy` subtrees.

**Read-only** commands (`list`, `verify`, `repository list`, `policy list/get`) are safe on production anytime.  
**Mutating** commands (`create`, `restore`, `prune`, `repository register/remove`, `policy put/delete/execute`) support `--dry-run` and require `--i-know-this-is-prod` on protected contexts.

Works identically on Elasticsearch and OpenSearch — except `policy`, which dispatches to ES SLM or OS Snapshot Management depending on the cluster's dialect (the CLI surface is the same, the wire underneath diverges).

See [config.md](./config.md) for context resolution and authentication.

---

## esops snapshot list

Lists snapshots inside one or more repositories. Requires `--repository` (or `--all` for every registered repository). To list the registered repositories themselves, use `esops snapshot repository list`.

```sh
$ esops snapshot list --repository s3
REPOSITORY  NAME                 STATE     INDICES  SHARDS  DURATION
s3          nightly-2026-04-30   SUCCESS   12       60/60   2m30s
```

**Filters**
| Flag           | Purpose                                      |
|----------------|----------------------------------------------|
| `--repository` | Repo name(s) (comma-separated)               |
| `--all`        | Snapshots from every registered repo         |
| `--snapshot`   | Snapshot name pattern(s)                     |
| `--state`      | `success`/`failed`/`partial`/…               |
| `--sort`       | `start_time` (default), `name`, `duration`   |
| `--slim`       | Lightweight response (`?verbose=false`)      |

**Common use cases**
- `esops snapshot list --all --state failed` – find broken snapshots
- `esops snapshot list --repository s3 --snapshot 'nightly-*' -o json`
- `esops snapshot list --all -o ndjson` – stream rows across a 20-repo fleet

---

## esops snapshot create

Create a new snapshot in a registered repository.

```sh
$ esops snapshot create --repository s3 --snapshot nightly-2026-04-30 --indices 'logs-*' --wait
ACTION  REPOSITORY  SNAPSHOT             STATE
create  s3          nightly-2026-04-30   SUCCESS
```

**Flags**
- `--repository` / `--snapshot` (both required)
- `--indices` (default: all open indices)
- `--include-global-state`
- `--partial`
- `--wait` (poll until finished; Ctrl-C aborts on cluster)
- `--resume` (continue a previous `--wait`)
- `--metadata KEY=VALUE` (repeatable)
- `--dry-run`

**Common use cases**
- `esops snapshot create --repository fs --snapshot pre-upgrade-$(date +%Y%m%d) --wait`
- `esops snapshot create --repository s3 --snapshot daily-$(date +%Y%m%d) --indices 'logs-*'`
- `esops snapshot create … --dry-run -o json` – preview only

---

## esops snapshot verify

Check repository connectivity (`_verify`) or run deep integrity test (`--deep`).

```sh
$ esops snapshot verify --repository s3
ACTION  REPOSITORY  NODES
verify  s3          3
```

**Flags**
- `--repository` (required)
- `--deep` (writes/reads test blobs — requires `--i-know-this-is-prod` on prod)
- `--blob-count`, `--max-blob-size`, etc. (for `--deep`)

**Common use cases**
- `esops snapshot verify --repository s3` – quick connectivity check
- `esops snapshot verify --repository s3 --deep --i-know-this-is-prod` – full integrity probe

---

## esops snapshot restore

Restore indices (and optionally global state) from a snapshot.

```sh
$ esops snapshot restore --repository fs --snapshot pre-upgrade-20260430 \
    --rename-pattern '(.+)' --rename-replacement 'restored-$1' --wait
```

**Flags**
- `--repository` / `--snapshot` (both required)
- `--indices`
- `--rename-pattern` + `--rename-replacement`
- `--index-setting KEY=VALUE`
- `--wait` (poll `/_recovery` until every target shard reaches DONE)
- `--resume` (continue a previous `--wait`)
- `--poll-interval` (default 10s)
- `--dry-run` (collision pre-flight)

**Common use cases**
- Restore with renamed indices for zero-downtime testing
- `esops snapshot restore … --index-setting index.number_of_replicas=0` – speed up recovery
- Long restores: `--wait` polls `/_recovery` and prints per-shard progress; SIGINT stops the poller but the restore *continues server-side*

> Restore can't be cleanly cancelled at the cluster — there is no
> `_cancel` endpoint and deleting the recovering index loses the
> partial recovery. SIGINT during `--wait` therefore stops the local
> poller only; the cluster keeps recovering. Clean up partial indices
> afterwards with `esops index list` / `esops index close` if needed.

---

## esops snapshot prune

Apply retention: keep last N snapshots and/or delete anything older than DURATION.

```sh
$ esops snapshot prune --repository s3 --keep-last 14 --state success --dry-run
ACTION  REPOSITORY  CONSIDERED  KEPT  DELETED
prune   s3          30          14    16
```

**Flags**
- `--repository` (required)
- `--keep-last N` or `--older-than DUR` (at least one required)
- `--snapshot` (pattern to consider)
- `--state success` (common filter)
- `--allow-large-prune` (when deleting >50% of eligible snapshots)
- `--dry-run`

**Common use cases**
- `esops snapshot prune --repository s3 --keep-last 14 --state success`
- `esops snapshot prune --repository s3 --keep-last 7 --older-than 90d --dry-run`

---

## esops snapshot repository

Manage snapshot repositories. The subcommands group everything that touches `/_snapshot/<repo>` — registration, removal, and listing — under one verb so the top-level `esops snapshot` tree stays focused on snapshots.

```
esops snapshot repository list                # GET  /_snapshot
esops snapshot repository register …          # PUT  /_snapshot/<repo>
esops snapshot repository remove   …          # DELETE /_snapshot/<repo>
```

### list

```sh
$ esops snapshot repository list
NAME      TYPE
local-fs  fs
s3-prod   s3
```

**Flags**
- `--repository NAME[,NAME…]` – limit to specific names
- `--show-secrets` – print credential keys verbatim (default: redacted)
- `-o wide` – appends a flat `key=value` settings preview

Read-only and safe on production anytime.

### register

```sh
$ esops snapshot repository register --repository s3-prod --type s3 \
    --setting bucket=my-backups --setting region=eu-west-1
```

**Flags**
- `--repository` / `--type` (both required)
- `--setting KEY=VALUE` (repeatable)
- `--settings-file PATH` (YAML/JSON — merges under `settings`)
- `--no-verify` (skip connectivity check)
- `--dry-run`

**Common use cases**
- Register S3, Azure, GCS, or `fs` repositories
- Use `--no-verify` when the backing store is not yet ready
- `--settings-file` for repositories with nested settings (e.g. role assumption)

### remove

Remove a repository registration. Snapshots stored in the backing store are not touched — `snapshot prune` is the right verb for that.

```sh
$ esops snapshot repository remove --repository s3-old
```

**Flags**
- `--repository` (required)
- `--dry-run`

The cluster refuses to remove a repository with in-progress snapshots; that 4xx is surfaced verbatim.

---

## esops snapshot policy

Manage scheduled snapshot policies — Snapshot Lifecycle Management on Elasticsearch (`/_slm/policy`) and the Snapshot Management plugin on OpenSearch (`/_plugins/_sm/policies`). Same CLI surface, different cluster wire underneath; esops doesn't translate between the two body shapes — feed each cluster its own dialect's body and the operator stays in control.

```
esops snapshot policy list                    # GET    /_slm/policy           or /_plugins/_sm/policies
esops snapshot policy get      --name N       # GET    /_slm/policy/<N>       or /_plugins/_sm/policies/<N>
esops snapshot policy put      --name N --body '{…}'   # PUT
esops snapshot policy delete   --name N       # DELETE
esops snapshot policy execute  --name N       # POST   /_slm/policy/<N>/_execute
                                              # or POST /_plugins/_sm/policies/<N>/_start
```

> **OpenSearch versions:** the Snapshot Management plugin landed in OpenSearch 2.1. On older OS clusters `/_plugins/_sm/policies` returns 404; that error surfaces verbatim — esops doesn't fabricate a "this product doesn't support policies" diagnosis when the cluster is just running an older version.

### list

```sh
$ esops snapshot policy list
NAME             REPOSITORY  SCHEDULE         ENABLED  LAST_SUCCESS
nightly-s3       s3-prod     0 30 1 * * ?     true     11h
hourly-archive   s3-archive  0 0 * * * ?      true     45m
```

**Flags**
- `--name` (repeatable; comma-joined on ES, fanned out on OS)
- `-o wide` adds modified date and next-execution time

### get

```sh
$ esops snapshot policy get --name nightly-s3 -o wide
NAME        REPOSITORY  SCHEDULE      ENABLED  VERSION  MODIFIED              LAST_SUCCESS  NEXT_RUN
nightly-s3  s3-prod     0 30 1 * * ?  true     2        2026-04-29T08:00:00Z  11h           2026-05-11T01:30:00Z

POLICY
  {
    "schedule": "0 30 1 * * ?",
    "name": "<nightly-{now/d}>",
    "repository": "s3-prod",
    "config": { "indices": ["*"] },
    "retention": { "expire_after": "30d", "min_count": 5, "max_count": 50 }
  }
```

### put

`put` forwards the body verbatim — esops only checks it parses as a JSON object, the cluster validates the inner shape.

```sh
# Elasticsearch SLM
$ esops snapshot policy put --name nightly-s3 --from-file ./slm-nightly.json

# OpenSearch SM (different body shape!)
$ esops snapshot policy put --name nightly-s3 --from-file ./sm-nightly.json
```

**Flags**
- `--name` (required, literal)
- exactly one of `--body JSON` or `--from-file PATH`
- `--dry-run` (client-side validate-and-report; the cluster has no native dry-run)

### delete

```sh
$ esops snapshot policy delete --name nightly-s3
```

Deleting a policy removes the schedule only — existing snapshots stay in the repository. Use `snapshot prune` for retention.

### execute

The cluster-side semantics differ by dialect — the CLI exposes both faithfully rather than papering over the difference:

| | ES SLM (`/_execute`) | OS SM (`/_start`) |
|---|---|---|
| Effect | Synthesises a one-off snapshot from the policy's template | Toggles the policy's `enabled` flag on |
| Snapshot timing | Immediate | Next scheduled cron tick |
| Result `snapshot_name` | Returned | Empty |

```sh
# Elasticsearch — synthesised name comes back in the result
$ esops snapshot policy execute --name nightly-s3
ACTION          DRY_RUN  NAME        SNAPSHOT                            ACKNOWLEDGED
policy-execute  false    nightly-s3  nightly-s3-2026.05.10-1y9jzq…       true

# OpenSearch — confirm with `snapshot list` after the next tick
$ esops snapshot policy execute --name nightly-s3
ACTION          DRY_RUN  NAME        SNAPSHOT  ACKNOWLEDGED
policy-execute  false    nightly-s3  -         true
```

---

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run`, and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.

---

## Watching in-flight snapshot operations

`snapshot create --wait` and `snapshot restore --wait` poll the
cluster's snapshot/recovery surface directly:

- `create --wait` → `_snapshot/<repo>/<name>/_status` per shard.
- `restore --wait` → `_recovery` per target index.

Neither uses `/_tasks`. **The cluster's snapshot service runs shard
work outside the transport-task framework**, so `esops ops tasks list`
will *not* reliably show an in-flight `snapshot create` once the
initial PUT has been accepted — the request task completes in
milliseconds and the per-shard work is invisible to `/_tasks`.

If you need to interrupt a runaway snapshot, use the cluster's own
delete-during-progress: `esops snapshot prune --repository <repo>
--snapshot <name>`. The cluster aborts the in-flight snapshot and
removes the partial blobs from the repository.

Restore has no `_cancel` endpoint at all. `snapshot restore --wait` is
a *local* poller: SIGINT stops it, but the cluster keeps recovering
shards. Clean up partial indices afterwards with `esops index list` /
`esops index close` if needed.

`esops ops tasks list` is still useful for cluster work that *does*
register as transport tasks: reindex, search, `_recovery` for an
in-progress restore, persistent maintenance tasks (`geoip-downloader`,
`health-node`), and any other op the cluster surfaces in `/_tasks`.
See [`ops.md`](./ops.md#esops-ops-tasks-list) for the full surface.