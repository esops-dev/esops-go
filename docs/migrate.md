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
- `--dry-run`

**Common use cases**
- Version upgrade or cross-dialect migration
- Controlled reindex with throttling

---

## esops migrate verify

Compares document counts between source and target after reindex.

```sh
$ esops migrate verify --source local-es --target local-os --to-suffix=-v2
```

**Common use cases**
- Confirm reindex completed successfully before cutover
- Quick post-reindex sanity check

---

## esops migrate cutover

Atomic alias swap on a single cluster (typically the target).

```sh
$ esops migrate cutover --context local-es --alias logs \
  --from logs --to logs-v2 --block-writes
```

**Flags**
- `--alias`, `--from`, `--to` (all required, literal names)
- `--block-writes` — temporarily block writes on old index
- `--dry-run`

**Common use cases**
- Zero-downtime switch after successful reindex
- Swap production alias to new index version

---

**All migrate commands** support `--output` (table/json/yaml), `--dry-run` where applicable, and global flags.  
`context=…` lines go to stderr so pipelines remain clean.