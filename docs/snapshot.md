# snapshot — inspect and manage snapshots

`esops snapshot` provides `kubectl`-style commands for snapshot repositories and operations: listing, creating, verifying, restoring, pruning, registering, and deregistering.

**Read-only** commands (`list`, `verify`) are safe on production anytime.  
**Mutating** commands (`create`, `restore`, `prune`, `register`, `deregister`) support `--dry-run` and require `--i-know-this-is-prod` on protected contexts.

Works identically on Elasticsearch and OpenSearch.  
See [config.md](./config.md) for context resolution and authentication.

---

## esops snapshot list

Lists registered repositories (no flags) or snapshots inside them (`--repository` or `--all`).

```sh
$ esops snapshot list --repository s3
REPOSITORY  NAME                 STATE     INDICES  SHARDS  DURATION
s3          nightly-2026-04-30   SUCCESS   12       60/60   2m30s
```

**Filters**
| Flag           | Purpose                                      |
|----------------|----------------------------------------------|
| `--repository` | Repo name(s) — switches to snapshot mode     |
| `--all`        | Snapshots from every registered repo         |
| `--snapshot`   | Snapshot name pattern(s)                     |
| `--state`      | `success`/`failed`/`partial`/…               |
| `--sort`       | `start_time` (default), `name`, `duration`   |
| `--slim`       | Lightweight response (`?verbose=false`)      |
| `--show-secrets` | Show credential keys (default: redacted)   |

**Common use cases**
- `esops snapshot list` – see all repositories
- `esops snapshot list --all --state failed` – find broken snapshots
- `esops snapshot list --repository s3 --snapshot 'nightly-*' -o json`
- `esops snapshot list -o wide` – repository settings preview

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

## esops snapshot register

Register a new snapshot repository.

```sh
$ esops snapshot register --repository s3-prod --type s3 \
    --setting bucket=my-backups --setting region=eu-west-1
```

**Flags**
- `--repository` / `--type` (both required)
- `--setting KEY=VALUE` (repeatable)
- `--no-verify` (skip connectivity check)
- `--dry-run`

**Common use cases**
- Register S3, Azure, GCS, or fs repositories
- Use `--no-verify` when backing store is not yet ready

---

## esops snapshot deregister

Remove a repository registration (snapshots on disk are untouched).

```sh
$ esops snapshot deregister --repository s3-old
```

**Flags**
- `--repository` (required)
- `--dry-run`

**Common use cases**
- Clean up old repositories after pruning snapshots

---

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run`, and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.