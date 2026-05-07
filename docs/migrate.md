# migrate — move data between clusters

`esops migrate` helps you safely move data between Elasticsearch or OpenSearch clusters — for version upgrades (same dialect) or cross-dialect migrations (ES ↔ OS).

**Read-only** commands (`check`, `plan`, `verify`) are safe on production.  
`reindex` writes to the target; `cutover` mutates one cluster (alias swap).

Use `--source CTX` and `--target CTX` for cross-cluster commands (both must exist in your config).  
See [config.md](./config.md) for context setup.

**Commands**
| Command           | Purpose                                      |
|-------------------|----------------------------------------------|
| `check`           | Pre-flight connectivity and compatibility    |
| `plan`            | Estimate size, duration, and mapping issues  |
| `reindex`         | Run remote reindex from source to target     |
| `verify`          | Compare document counts post-reindex         |
| `cutover`         | Atomic alias swap (zero-downtime switch)     |

---

## esops migrate check

Pre-flight connectivity, version compatibility, and `reindex.remote.whitelist`.

```sh
$ esops migrate check --source local-es --target local-os
SOURCE  context=local-es  dialect=elasticsearch  version=9.3.3
TARGET  context=local-os  dialect=opensearch    version=3.6.0

STATUS  CHECK                    DETAIL
OK      source.connectivity      reachable
WARN    dialect                  cross-dialect migration
BLOCK   target.remote_whitelist  source not whitelisted
```

**Common use cases**
- Validate before starting a migration
- Check if target allows remote reindex from source

---

## esops migrate plan

Estimates reindex effort and flags mapping incompatibilities.

```sh
$ esops migrate plan --source local-es --target local-os
STATUS  INDEX              DOCS   PRIMARY_STORE  ISSUES  EST_DURATION
OK      esops-logs-000001  200    32.0kb         0       0s
```

**Flags**
- `--source`, `--target` (required)
- `--indices` — limit to specific pattern
- `--throughput-docs-per-sec` — adjust duration estimate

**Common use cases**
- Understand migration size and potential issues
- Plan capacity on the target cluster

---

## esops migrate reindex

Performs remote reindex from source to target. Runs one index at a time.

```sh
$ esops migrate reindex --source local-es --target local-os --to-suffix=-v2 \
  --slices auto --requests-per-second 5000
```

**Key flags**
- `--source`, `--target` (required)
- `--to-suffix` or `--to-index` — destination naming
- `--slices auto`
- `--requests-per-second N`
- `--conflicts proceed` (default: abort)
- `--poll-interval` — how often `/_tasks/<id>` is polled (minimum `1s`; sub-second polling is rejected up-front)
- `--resume` — pick up an interrupted run from `$XDG_STATE_HOME/esops/migrate`. SIGINT during a reindex cancels the cluster-side task (`POST /_tasks/<id>/_cancel`) and persists state so a subsequent `--resume` continues polling instead of starting fresh.
- `--dry-run`

**Common use cases**
- Version upgrade or cross-dialect migration
- Controlled reindex with throttling
- Resume a long-running reindex after a network blip or accidental Ctrl-C

---

## esops migrate verify

Compares document counts between source and target after reindex.

```sh
$ esops migrate verify --source local-es --target local-os --to-suffix=-v2
```

**Key flags**
- `--source`, `--target` (required)
- `--to-suffix` or `--to-index` — same pairing rules as `migrate reindex`. Use `--to-index` when the original reindex used a literal target name.

**Common use cases**
- Confirm reindex completed successfully before cutover
- Quick post-reindex sanity check

---

## esops migrate cutover

Atomic alias swap on a single cluster (typically the target).

```sh
$ esops migrate cutover --context local-es --alias logs \
  --from logs --to logs-v2 --block-writes --drain-delay=500ms
```

**Flags**
- `--alias`, `--from`, `--to` (all required, literal names)
- `--block-writes` — set `index.blocks.write=true` on `--from` before the swap so any in-flight writers get a clear refusal. The block applies the moment the cluster commits the settings update; bulk requests already in flight on individual shards may still complete after that, so combine with `--drain-delay` if traffic is high.
- `--drain-delay DURATION` — pause between the write-block and the alias swap so in-flight writes drain (e.g. `500ms`, `2s`). Only honoured when `--block-writes` is set; default 0.
- `--keep-write-block` — leave `--from` write-blocked after the swap (read-only). The cutover surfaces a manual-cleanup reminder in the steps log; there is no background TTL.
- `--dry-run`

**Common use cases**
- Zero-downtime switch after successful reindex
- Swap production alias to new index version

---

**All migrate commands** support `--output` (table/json/yaml), `--dry-run` where applicable, and global flags.  
`context=…` lines go to stderr so pipelines remain clean.