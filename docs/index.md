# index — inspect and manage indices

`esops index` provides `kubectl`-style commands for day-to-day index operations: listing, opening/closing, force-merging, shrinking, rolling over aliases, managing aliases/settings/templates, and lifecycle policies (ILM on Elasticsearch, ISM on OpenSearch).

**Read-only commands** (`list`, `aliases list`, `settings get`, `template list/get`, `ilm`/`ism` commands) are safe on production at any time.  
**Mutating commands** require `--dry-run` for preview and `--i-know-this-is-prod` on protected contexts.

`ilm` works only on Elasticsearch; `ism` only on OpenSearch (wrong dialect → exit code 6 with hint).

See [config.md](./config.md) for context resolution and authentication.

---

## esops index list

Lists indices: name, health, status, primary/replica count, docs, and size.

```sh
$ esops index list
context=local-es dialect=elasticsearch version=9.3.3 indices=3
NAME               HEALTH  STATUS  PRI  REP  DOCS  STORE
esops-logs-000001  green   open    1    0    200   34.2kb
```

Hidden (dot-prefixed) indices excluded by default. Use `--hidden` to include them.

**Filters**
| Flag       | Purpose                              |
|------------|--------------------------------------|
| `--index`  | Comma-separated patterns             |
| `--health` | `green`/`yellow`/`red`               |
| `--status` | `open`/`close`                       |
| `--hidden` | Include dot-prefixed indices         |
| `--sort`   | `name` (default), `size`, `docs`     |

**Output formats**
- `-o table` (default)
- `-o wide` (adds UUID, creation date, deleted docs)
- `-o json` / `-o yaml` / `-o ndjson`

**Common use cases**
- `esops index list --health yellow`
- `esops index list --status close --hidden`
- `esops index list --sort size -o json | jq '.[0:10]'`
- `esops index list -o json | jq '[.[].store_bytes] | add'`

---

## esops index close / open

Close or open indices (`POST /{index}/_close` or `/_open`).

```sh
$ esops index close --index 'logs-2023-*' --dry-run
ACTION  DRY_RUN  ACKNOWLEDGED  CHANGED  SKIPPED
close   true     true          3        1
```

Already-closed/open indices are skipped automatically.

**Safety**
- Refuses `*` / `_all` and >20 indices unless `--max-indices N` is set (exit 8).
- Protected contexts require `--i-know-this-is-prod`.

**Flags** (both commands)
- `--index` (required)
- `--max-indices N`
- `--ignore-unavailable`
- `--wait-for-active-shards`
- `--dry-run`

**Common use cases**
- `esops index close --index 'logs-2023-*' --dry-run`
- `esops index open --index logs-2023-12 --wait-for-active-shards all`

---

## esops index optimize

Force-merge open indices (`POST /{index}/_forcemerge`).

```sh
$ esops index optimize --index 'logs-2024-*' --max-num-segments 1 --dry-run
ACTION    DRY_RUN  INDICES  MAX_SEGMENTS
optimize  true     3        1
```

Closed indices are skipped.

**Safety**
- Same blast-radius rules as `close`/`open`.
- I/O intensive — run during quiet periods.

**Flags**
- `--index` (required)
- `--max-num-segments N`
- `--only-expunge-deletes`
- `--no-flush`
- `--dry-run`

**Common use cases**
- Seal a rotated index: `--max-num-segments 1`
- Cheap cleanup after delete-by-query: `--only-expunge-deletes`

---

## esops index rollover

Roll over an alias’s write index (`POST /<alias>/_rollover`).

```sh
$ esops index rollover --alias esops-logs --max-age 7d --max-docs 1000000 --dry-run
ALIAS       ROLLED_OVER  OLD_INDEX           NEW_INDEX
esops-logs  true         esops-logs-000001   esops-logs-000002
```

**Conditions** (at least one required, or use `--force`)
- `--max-age DURATION`
- `--max-docs N`
- `--max-size SIZE`
- `--max-primary-size SIZE`

**Flags**
- `--alias` (required)
- `--force` (for unconditional rollover)
- `--dry-run` (native cluster support)

**Common use cases**
- `esops index rollover --alias esops-logs --max-age 7d --max-docs 1000000 --dry-run`
- Manual cutover: `--force`

---

## esops index shrink

Shrink an index to fewer primary shards (`POST /<source>/_shrink/<target>`).

```sh
$ esops index shrink --source esops-logs-2024-12 --target esops-logs-2024-12-shrunk --shards 1 --dry-run
ACTION  DRY_RUN  SOURCE              TARGET
shrink  true     esops-logs-2024-12  esops-logs-2024-12-shrunk
```

Source must be read-only and all primaries on one node (operator responsibility).

**Flags**
- `--source` / `--target` (required, literal names only)
- `--shards N`
- `--replicas N`
- `--copy-settings`
- `--dry-run`

**Common use cases**
- Reduce shard count on sealed archives.

---

## esops index aliases

Manage alias → index bindings.

### esops index aliases list
```sh
$ esops index aliases list --alias esops-logs
```

**Common use cases**
- Inspect write targets: `esops index aliases list -o json | jq '.[] | select(.is_write_index)'`

### esops index aliases add / remove
```sh
$ esops index aliases add --alias esops-logs --index esops-logs-2024-12 --is-write-index
$ esops index aliases remove --alias esops-logs --index esops-logs-2023-12
```

Supports `--filter` (JSON) and `--routing` on `add`. Literal names only. `--dry-run` supported.

---

## esops index settings

Get or set index settings.

### esops index settings get
```sh
$ esops index settings get --index esops-logs-2024-12 --name index.number_of_replicas
```

### esops index settings set
Three input styles:
- `--set key=value` (repeatable, auto-prefixes `index.`)
- `--body '{"index":{…}}'`
- `--from-file PATH`

**Common use cases**
- Make index read-only before shrink: `--set blocks.write=true`
- Disable refresh during bulk load: `--set refresh_interval=-1`

---

## esops index template

Manage v2 (composable) index templates (`/_index_template`).

### esops index template list / get
```sh
$ esops index template list
$ esops index template get --name esops-logs-v2 -o wide
```

### esops index template put / delete
```sh
$ esops index template put --name esops-logs-v2 --from-file template.json
$ esops index template delete --name esops-logs-v1
```

`--create` and `--dry-run` supported on `put`. Literal names only.

---

## esops index ilm (Elasticsearch only)

ILM policy management.

```sh
$ esops index ilm list
$ esops index ilm get --name logs-30d
$ esops index ilm put --name logs-30d --from-file policy.json
$ esops index ilm explain --only-errors
```

---

## esops index ism (OpenSearch only)

ISM policy management (mirrors `ilm` commands).

```sh
$ esops index ism list
$ esops index ism get --id logs-rollover
$ esops index ism put --id logs-rollover --from-file policy.json
$ esops index ism explain
```

Supports optimistic concurrency (`--if-seq-no` + `--if-primary-term`).

---

**All commands** support `--context`, `--url`, `--output`, `--dry-run`, and global auth/timeout flags.  
The `context=…` line prints to stderr so pipelines stay clean.