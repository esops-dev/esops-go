# index — inspect and manage indices

`esops index` gives you `kubectl`-style commands for day-to-day index operations: listing, opening and closing, force-merging (optimize), shrinking, rolling aliases over, managing alias→index bindings, reading and updating settings, and managing v2 (composable) index templates.

The read-only commands — `esops index list`, `esops index aliases list`, `esops index settings get`, `esops index template list`, and `esops index template get` — are safe to run on production at any time. `esops index open`, `esops index close`, `esops index optimize`, `esops index rollover`, `esops index shrink`, `esops index aliases add` / `esops index aliases remove`, `esops index settings set`, `esops index template put`, and `esops index template delete` mutate cluster state — see the safety notes per command.

See [config.md](./config.md) for context resolution and authentication.

---

## esops index list

Lists indices with the columns operators need first: name, health, status, primary/replica count, document count, and size.

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index list
context=local-es dialect=elasticsearch version=9.3.3 indices=3
NAME               HEALTH  STATUS  PRI  REP  DOCS  STORE
esops-logs-000001  green   open    1    0    200   34.2kb
esops-products     green   open    1    0    50    13.0kb
esops-users        green   open    1    0    20    9.0kb
```

Hidden (dot-prefixed) indices are excluded by default. Use `--hidden` to include them.

**Filters**
| Flag       | Purpose                                      |
|------------|----------------------------------------------|
| `--index`  | Comma-separated patterns (`logs-*,users`)    |
| `--health` | `green` / `yellow` / `red`                   |
| `--status` | `open` / `close`                             |
| `--hidden` | Include dot-prefixed system indices          |
| `--sort`   | `name` (default), `size`, or `docs`          |

**Output formats**
- `-o table` (default) – human readable
- `-o wide` – adds UUID, creation date, deleted docs
- `-o json` / `-o yaml` – script-friendly
- `-o ndjson` – one object per line (large clusters)

**Common use cases**
- `esops index list --health yellow` – quick triage
- `esops index list --status close --hidden` – see closed indices
- `esops index list --sort size -o json | jq '.[0:10]'` – largest indices
- `esops index list -o json | jq '[.[].store_bytes] | add'` – total disk usage
- `esops --context local-os index list` – works the same on OpenSearch

The `context=…` line always prints to stderr so pipelines stay clean.

---

## esops index close

Closes one or more open indices via `POST /{index}/_close`. A closed index frees its shard-level resources (heap, file handles) while remaining recoverable on disk — the standard pattern for rotating cold indices off live serving without deleting them.

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index close --index 'logs-2023-*' --dry-run
ACTION  DRY_RUN  ACKNOWLEDGED  SHARDS_ACK  CHANGED  SKIPPED
close   true     true          true        3        1

INDICES
  logs-2023-10
  logs-2023-11
  logs-2023-12
```

`--dry-run` resolves the pattern client-side (via `/_cat/indices`) and reports what would change without contacting the close endpoint. Re-run without `--dry-run` to apply.

**Safety**

| Behaviour                                | Detail                                                                                                |
|------------------------------------------|-------------------------------------------------------------------------------------------------------|
| Blast-radius cap                          | Refuses when the resolved pattern matches more than **20** indices unless `--max-indices N` is set. Exit code 8. |
| Already-closed indices                    | Skipped (reported under `skipped`); the resolved name list sent to the cluster never includes them.   |
| Match-everything patterns                 | `*` and `_all` are refused outright with exit code 8 — name a narrower pattern.                       |
| Protected contexts                        | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.               |

**Flags**

| Flag                          | Purpose                                                                              |
|-------------------------------|--------------------------------------------------------------------------------------|
| `--index` (required)           | Comma-separated index names or patterns                                              |
| `--max-indices N`              | Override the 20-index blast-radius cap                                               |
| `--ignore-unavailable`         | Treat missing indices as a no-op (`?ignore_unavailable=true`)                        |
| `--wait-for-active-shards V`   | Forwarded to the cluster (`?wait_for_active_shards=V`; numeric or `all`)             |
| `--dry-run` (global)           | Resolve and report only — do not contact the close endpoint                          |

**Common use cases**

- `esops index close --index 'logs-2023-*' --dry-run` – preview a cold-rotation sweep
- `esops index close --index logs-2024-12 --i-know-this-is-prod` – close one index in prod
- `esops index close --index 'archive-*' --max-indices 200` – larger sweep with explicit opt-in
- `esops index close --index 'logs-*' -o json | jq '.indices'` – capture the changed list

---

## esops index open

Opens one or more closed indices via `POST /{index}/_open`. Use this to bring an archive index back online for queries or to recover from a cold-rotation that closed too aggressively.

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index open --index 'logs-2023-*'
ACTION  DRY_RUN  ACKNOWLEDGED  SHARDS_ACK  CHANGED  SKIPPED
open    false    true          true        3        0

INDICES
  logs-2023-10
  logs-2023-11
  logs-2023-12
```

Already-open indices are reported under `skipped` and not sent to the cluster, so re-running the command is a safe no-op.

**Safety**

The match-everything refusal (`*` / `_all`) and the 20-index blast-radius cap apply here as well — opening a thousand closed archive indices at once can spike heap and recovery traffic on the cluster master. Set `--max-indices` explicitly when you mean it.

**Flags**

Same as `index close`: `--index` (required), `--max-indices`, `--ignore-unavailable`, `--wait-for-active-shards`, `--dry-run`.

**Common use cases**

- `esops index open --index logs-2023-12` – bring one archive back online for an investigation
- `esops index open --index 'archive-2023-*' --dry-run` – preview which closed indices a pattern would resurrect
- `esops index open --index logs-2024-Q1 --wait-for-active-shards all` – open and wait until every replica is allocated before returning

---

## Output formats (open / close)

`-o table` (default) shows the action header followed by the changed `INDICES` block. `-o wide` adds a `SKIPPED` block for indices already in the target state. `-o json` and `-o yaml` emit the neutral [`IndexStateChange`](../internal/types/types.go) shape:

```json
{
  "action": "close",
  "dry_run": false,
  "acknowledged": true,
  "shards_acknowledged": true,
  "indices": ["logs-2023-10", "logs-2023-11"],
  "skipped": ["logs-2023-12"]
}
```

`indices` is the changed set; `skipped` is omitted when empty.

---

## esops index optimize

Force-merges open indices via `POST /{index}/_forcemerge` to reduce their segment count. The classic use case is sealing a rotated index after it stops receiving writes — merging to one segment recovers heap, reduces file-handle pressure, and speeds up small-shard searches.

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index optimize --index 'esops-logs-2024-*' --max-num-segments 1 --dry-run
ACTION    DRY_RUN  INDICES  MAX_SEGMENTS  SHARDS_OK  SHARDS_FAILED
optimize  true     3        1             0          0

INDICES
  esops-logs-2024-10
  esops-logs-2024-11
  esops-logs-2024-12
```

`--dry-run` resolves the pattern client-side via `/_cat/indices` and reports what would be merged without contacting the force-merge endpoint. Re-run without `--dry-run` to apply.

A force-merge is **I/O-intensive**: it can saturate disk on the affected nodes and slow reads while it runs. Run during a quiet window, or against a dedicated archive tier.

**Closed indices** in the resolved pattern are surfaced under `failures` and skipped — `_forcemerge` against a closed index is a 400 on both dialects, so we report the skip rather than letting one bad index break the whole sweep. Open them first with `esops index open` if you intended to merge them.

**Safety**

| Behaviour                   | Detail                                                                                                              |
|-----------------------------|---------------------------------------------------------------------------------------------------------------------|
| Blast-radius cap            | Refuses when the resolved pattern matches more than **20** open indices unless `--max-indices N` is set. Exit code 8. |
| Match-everything patterns   | `*` and `_all` are refused outright with exit code 8 — name a narrower pattern.                                     |
| Closed indices              | Skipped (reported under `failures`); the merge call sent to the cluster only includes open indices.                 |
| Protected contexts          | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.                             |

**Flags**

| Flag                       | Purpose                                                                                              |
|----------------------------|------------------------------------------------------------------------------------------------------|
| `--index` (required)       | Comma-separated index names or patterns                                                              |
| `--max-indices N`          | Override the 20-index blast-radius cap                                                               |
| `--max-num-segments N`     | Merge each shard down to N segments (1 is the typical sealed-archive value; omit to use the cluster default) |
| `--only-expunge-deletes`   | Only rewrite segments holding tombstoned docs (`?only_expunge_deletes=true`) — cheap variant after a delete-by-query |
| `--no-flush`               | Skip the post-merge flush (`?flush=false`; the cluster default is to flush)                          |
| `--ignore-unavailable`     | Treat missing indices as a no-op (`?ignore_unavailable=true`)                                        |
| `--dry-run` (global)       | Resolve and report only — do not contact the force-merge endpoint                                    |

**Common use cases**

- `esops index optimize --index 'esops-logs-2024-*' --max-num-segments 1 --dry-run` – preview a sealed-archive merge
- `esops index optimize --index esops-archive-2024 --max-num-segments 1 --i-know-this-is-prod` – seal one rotated index in prod
- `esops index optimize --index esops-events --only-expunge-deletes` – cheap cleanup after a delete-by-query
- `esops index optimize --index 'esops-cold-*' --max-indices 200` – larger archive sweep with explicit opt-in
- `esops index optimize --index 'esops-logs-*' -o json | jq '.shards_failed'` – check for per-shard merge failures

**Output (`-o json`)**

```json
{
  "action": "optimize",
  "dry_run": false,
  "indices": ["esops-logs-2024-10", "esops-logs-2024-11"],
  "max_num_segments": 1,
  "shards_total": 6,
  "shards_successful": 6,
  "shards_failed": 0
}
```

`failures` is omitted when empty; per-shard cluster errors and closed-index skips are surfaced there.

---

## esops index rollover

Rolls an alias's current write index over to a new generation via `POST /<alias>/_rollover`. The cluster evaluates the submitted conditions against the alias's current write index and either creates the next generation (rolling the alias forward) or reports which condition(s) didn't match.

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index rollover --alias esops-logs --max-age 7d --max-docs 1000000 --dry-run
ALIAS       ROLLED_OVER  DRY_RUN  ACKNOWLEDGED  OLD_INDEX           NEW_INDEX
esops-logs  true         true     true          esops-logs-000001   esops-logs-000002

CONDITIONS
CONDITION  MET
max_age    true
max_docs   false
```

The cluster has a **native `?dry_run=true`** for `_rollover`, so unlike `index open` / `index close` the dry-run isn't synthesised client-side: the cluster evaluates conditions against real metadata and reports which ones matched without creating the new index.

**Conditions**

| Flag                     | Wire param                | Purpose                                                                |
|--------------------------|---------------------------|------------------------------------------------------------------------|
| `--max-age DURATION`     | `max_age`                 | Roll over when the current write index is older than DURATION (`7d`, `1h`) |
| `--max-docs N`           | `max_docs`                | Roll over when the current write index reaches N documents             |
| `--max-size SIZE`        | `max_size`                | Roll over when the index reaches SIZE (`50gb`)                         |
| `--max-primary-size SIZE`| `max_primary_shard_size`  | Roll over when any primary shard reaches SIZE (`25gb`)                 |

Either at least one condition must be set, or `--force` must be passed. An unconditional rollover is a real lever — it's used when an external scheduler decides cutover timing — but a typo like `index rollover --alias logs` shouldn't quietly roll a fresh index. The flag forces the operator to acknowledge the intent in shell history.

**Other flags**

| Flag                          | Purpose                                                                                                |
|-------------------------------|--------------------------------------------------------------------------------------------------------|
| `--alias` (required)          | Alias whose current write index will be rolled over                                                    |
| `--new-index NAME`            | Override the cluster's default name for the new write index (rarely needed; the cluster auto-increments names ending in `-NNNNNN`) |
| `--force`                     | Acknowledge an unconditional rollover when no `--max-*` flag is set                                    |
| `--wait-for-active-shards V`  | Forwarded as `?wait_for_active_shards=V` (numeric or `all`)                                            |
| `--dry-run` (global)          | Forwarded to the cluster's native `?dry_run=true`; conditions are evaluated, no new index is created   |

**Safety**

| Behaviour                   | Detail                                                                                                  |
|-----------------------------|---------------------------------------------------------------------------------------------------------|
| Unconditional rollover      | Refused unless `--force` is set, since a no-condition rollover always creates a new write index         |
| Protected contexts          | Against `protection: prod`, `--i-know-this-is-prod` is required for the live rollover. Exit code 9.     |
| Negative `--max-docs`       | Refused with a usage error — the cluster would reject it anyway, but failing fast keeps the message specific |

**Common use cases**

- `esops index rollover --alias esops-logs --max-age 7d --max-docs 1000000 --dry-run` – preview a daily/size-capped rollover policy
- `esops index rollover --alias esops-logs --max-primary-size 25gb --i-know-this-is-prod` – production rollover gated on shard size
- `esops index rollover --alias esops-events --force --i-know-this-is-prod` – manual cutover scheduled by an external job
- `esops index rollover --alias esops-logs --max-age 7d -o json | jq '.rolled_over'` – check whether the alias actually rolled

**Output (`-o json`)**

```json
{
  "alias": "esops-logs",
  "old_index": "esops-logs-000001",
  "new_index": "esops-logs-000002",
  "rolled_over": true,
  "dry_run": false,
  "acknowledged": true,
  "shards_acknowledged": true,
  "conditions": {
    "max_age": true,
    "max_docs": false
  }
}
```

`old_index` / `new_index` come from the cluster — on a no-match dry-run, `new_index` may be empty. `conditions` echoes each submitted trigger and the cluster's verdict; an unconditional rollover (`--force`) returns it empty.

---

## esops index shrink

Shrinks a source index into a new target index with fewer primary shards via `POST /<source>/_shrink/<target>`. The cluster hard-links segments rather than copying them — much cheaper than a reindex when the operator just needs to reduce primary shard count (the classic case is sealing an over-sharded daily index for archive).

Works identically on Elasticsearch and OpenSearch.

```sh
$ esops index shrink --source esops-logs-2024-12 --target esops-logs-2024-12-shrunk --shards 1 --dry-run
ACTION  DRY_RUN  SOURCE              TARGET                     SHARDS  ACKNOWLEDGED  SHARDS_ACK
shrink  true     esops-logs-2024-12  esops-logs-2024-12-shrunk  1       true          true
```

`--dry-run` validates the request and reports what would be sent without contacting the shrink endpoint. The cluster has no native dry-run for `_shrink`, so dry-run is a client-side "validate + report" path.

**Pre-flight requirements (operator-driven, not automated)**

The cluster requires the source to be:

- **read-only** (`index.blocks.write: true`)
- **allocated with all primaries on a single node** (set `index.routing.allocation.require._name: <node>` and wait for the moves to complete)
- have a primary shard count that is a **factor of `--shards`** (4→2, 4→1, 8→4, …)

`esops` does **not** flip those settings automatically — they are policy decisions. The cluster surfaces its own canonical error if the source isn't ready, and that error is the right thing to read.

**Safety**

| Behaviour                  | Detail                                                                                                |
|----------------------------|-------------------------------------------------------------------------------------------------------|
| Literal names only         | `--source` and `--target` reject patterns (`*`, `_all`, `?`, etc.) — shrink is a per-index operation. |
| Identical names            | `--source` and `--target` must differ.                                                                |
| Negative `--shards` / `--replicas` | Refused with a usage error before the cluster sees the request.                               |
| Protected contexts         | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.               |

**Flags**

| Flag                          | Purpose                                                                                          |
|-------------------------------|--------------------------------------------------------------------------------------------------|
| `--source` (required)         | Existing index to shrink (literal name)                                                          |
| `--target` (required)         | New index name to create from the shrink (literal name)                                          |
| `--shards N`                  | Number of primary shards on the target (must be a factor of source shards; 0 = cluster default) |
| `--replicas N`                | Number of replicas on the target (omit to leave the cluster default; `0` keeps shrink cheap)     |
| `--copy-settings`             | Copy compatible source settings to the target (`?copy_settings=true`)                            |
| `--wait-for-active-shards V`  | Forwarded as `?wait_for_active_shards=V` (numeric or `all`)                                      |
| `--dry-run` (global)          | Validate and report only — do not contact the shrink endpoint                                    |

**Common use cases**

- `esops index shrink --source esops-logs-2024-12 --target esops-logs-2024-12-shrunk --shards 1 --dry-run` – preview a sealed-archive shrink
- `esops index shrink --source esops-events --target esops-events-1p --shards 1 --replicas 0 --i-know-this-is-prod` – minimise heap on a sealed prod archive
- `esops index shrink --source esops-logs --target esops-logs-half --shards 2 -o json | jq '.acknowledged'` – capture a scriptable verdict

**Output (`-o json`)**

```json
{
  "action": "shrink",
  "dry_run": false,
  "source": "esops-logs-2024-12",
  "target": "esops-logs-2024-12-shrunk",
  "acknowledged": true,
  "shards_acknowledged": true,
  "target_shards": 1
}
```

`target_shards` is omitted when 0 (cluster default).

---

## esops index aliases

`esops index aliases` is the alias→index binding surface: `list` reads `/_cat/aliases`; `add` and `remove` post atomic actions to `POST /_aliases`.

Works identically on Elasticsearch and OpenSearch.

### esops index aliases list

Lists alias→index bindings as flat rows: an alias fronting N indices materialises as N rows. Read-only and safe to run on a prod-protected context at any time.

```sh
$ esops index aliases list
ALIAS                 INDEX                 WRITE_INDEX  FILTERED
esops-events-tenant-x esops-events-2024-12  false        yes
esops-logs            esops-logs-000001     false        no
esops-logs            esops-logs-000002     true         no
```

**Filters**

| Flag       | Purpose                                                                  |
|------------|--------------------------------------------------------------------------|
| `--alias`  | Limit to alias name(s) or pattern(s), comma-separated                    |
| `--index`  | Limit to alias rows whose index matches one of these literal names       |

**Output formats**

- `-o table` (default) – human readable
- `-o wide` – adds routing columns and the raw filter JSON
- `-o json` / `-o yaml` – the neutral [`Alias`](../internal/types/types.go) shape
- `-o ndjson` – one record per line (large clusters)

**Common use cases**

- `esops index aliases list --alias esops-logs` – inspect every backing index of a write alias
- `esops index aliases list -o json | jq '.[] | select(.is_write_index)'` – find every write target across the cluster
- `esops index aliases list -o wide` – inspect filtered/routed aliases at a glance

### esops index aliases add

Binds an alias to a single index. Mutates cluster state.

```sh
$ esops index aliases add --alias esops-logs --index esops-logs-2024-12 --is-write-index --dry-run
ACTION        DRY_RUN  ACKNOWLEDGED  ACTIONS
alias-update  true     true          1

ACTION  ALIAS       INDEX
add     esops-logs  esops-logs-2024-12
```

`--filter` accepts a JSON object (an Elasticsearch / OpenSearch query DSL fragment) to install a filtered alias. The fragment is parsed client-side — bad JSON is refused before the cluster sees the request — and otherwise embedded verbatim:

```sh
$ esops index aliases add --alias esops-tenant-x \
    --index esops-events-2024-12 \
    --filter '{"term":{"tenant":"x"}}' \
    --routing tenant-x
```

**Safety**

| Behaviour                | Detail                                                                                                  |
|--------------------------|---------------------------------------------------------------------------------------------------------|
| Literal names only       | `--alias` and `--index` reject patterns. For bulk operations, use the cluster's raw API.               |
| Filter shape             | `--filter` must be a JSON object; arrays/strings are refused with a usage error.                        |
| Protected contexts       | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.                 |

**Flags**

| Flag                  | Purpose                                                                          |
|-----------------------|----------------------------------------------------------------------------------|
| `--alias` (required)  | Alias name (literal)                                                              |
| `--index` (required)  | Index name to bind (literal)                                                      |
| `--filter`            | JSON filter object to install on the alias                                        |
| `--routing`           | Routing value applied to indexing and search via this alias                       |
| `--is-write-index`    | Mark this alias→index binding as the alias's write target                         |
| `--dry-run` (global)  | Validate and report only — do not contact the cluster                             |

### esops index aliases remove

Removes a single alias→index binding.

```sh
$ esops index aliases remove --alias esops-logs --index esops-logs-2023-12
ACTION        DRY_RUN  ACKNOWLEDGED  ACTIONS
alias-update  false    true          1

ACTION  ALIAS       INDEX
remove  esops-logs  esops-logs-2023-12
```

To remove every binding for an alias, run remove once per index (or script over `esops index aliases list -o json`).

**Flags**

| Flag                  | Purpose                                                |
|-----------------------|--------------------------------------------------------|
| `--alias` (required)  | Alias name (literal)                                   |
| `--index` (required)  | Index name to unbind (literal)                         |
| `--dry-run` (global)  | Validate and report only — do not contact the cluster  |

**Output (`-o json`)**

```json
{
  "action": "alias-update",
  "dry_run": false,
  "acknowledged": true,
  "actions": [
    {
      "action": "add",
      "alias": "esops-logs",
      "index": "esops-logs-2024-12",
      "is_write_index": true
    }
  ]
}
```

`actions` echoes the request's actions list back so a captured JSON record is interpretable on its own.

---

## esops index settings

`esops index settings` is the index-settings surface: `get` reads `GET /<indices>/_settings`; `set` issues `PUT /<indices>/_settings`.

Works identically on Elasticsearch and OpenSearch.

### esops index settings get

Reads the current settings for one or more indices. The default table shows one row per `(index, dotted-key, value)` triple, so a deeply-nested cluster setting (`index.routing.allocation.require._name`) lines up with the simple ones in a single grep-friendly view.

```sh
$ esops index settings get --index esops-logs-2024-12 --name index.number_of_replicas --name index.refresh_interval
INDEX                  KEY                                 VALUE
esops-logs-2024-12     index.number_of_replicas            1
esops-logs-2024-12     index.refresh_interval              30s
```

`--index` is required — the cluster's "no-index" form returns every index, which on a busy prod cluster is a multi-megabyte payload that is almost never what an operator asked for.

**Flags**

| Flag                  | Purpose                                                                                  |
|-----------------------|------------------------------------------------------------------------------------------|
| `--index` (required)  | Comma-separated index names or patterns                                                  |
| `--name`              | Limit to specific dotted setting key prefix(es); repeatable                              |
| `--include-defaults`  | Merge in the cluster's built-in defaults so configured-vs-inherited values both surface  |
| `--flat-settings`     | Forward as `?flat_settings=true` (cluster returns dotted keys instead of nested objects) |
| `--ignore-unavailable`| Treat missing indices as a no-op                                                         |

**Output formats**

- `-o table` (default) — one row per (index, key, value) triple
- `-o wide` — same shape, no value truncation
- `-o json` / `-o yaml` — the neutral [`IndexSettings`](../internal/types/types.go) shape (one record per index, full nested settings map)
- `-o ndjson` — one record per line (large clusters)

**Common use cases**

- `esops index settings get --index esops-logs-2024-12` — full settings dump for one index
- `esops index settings get --index 'esops-logs-*' --name index.number_of_replicas` — replica counts across a rotation
- `esops index settings get --index esops-logs --include-defaults -o json | jq '.[].settings'` — full effective view for an audit
- `esops index settings get --index esops-logs-2024-* --name index.lifecycle -o json` — ILM/ISM policy attachment in JSON

### esops index settings set

Updates one or more index settings. Three input forms — pick exactly one:

```sh
# 1. --set key=value (repeatable). Bare keys are auto-prefixed with "index.".
$ esops index settings set --index 'esops-logs-2024-*' --set number_of_replicas=2 --dry-run
ACTION            DRY_RUN  ACKNOWLEDGED  INDICES
settings-update   true     true          1

INDICES
  esops-logs-2024-*

# 2. --body JSON literal (full envelope including the "index" wrapper).
$ esops index settings set --index esops-logs-2024-12 \
    --body '{"index":{"number_of_replicas":"2","refresh_interval":"30s"}}'

# 3. --from-file PATH for CI-rendered JSON.
$ esops index settings set --index esops-logs --from-file /etc/esops/seal-archive.json
```

The cluster has **no native dry-run** for `_settings`, so `--dry-run` is a client-side "validate + report" path that echoes the body that would have been sent. Use `-o wide` to see the body block in table mode.

**Safety**

| Behaviour                      | Detail                                                                                                |
|--------------------------------|-------------------------------------------------------------------------------------------------------|
| Match-everything refusal       | `--index *` and `--index _all` are refused outright — name a pattern that matches the indices you intend to mutate. |
| Mutually exclusive inputs      | `--set` and `--body`/`--from-file` cannot be combined; pick one.                                     |
| JSON shape validation          | `--body`/`--from-file` content must be a JSON object. Arrays/strings/null are refused.                |
| Protected contexts             | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.               |

**Flags**

| Flag                       | Purpose                                                                                       |
|----------------------------|-----------------------------------------------------------------------------------------------|
| `--index` (required)       | Comma-separated index names or patterns                                                       |
| `--set key=value`          | Repeatable; bare keys are auto-prefixed with `index.`                                         |
| `--body`                   | Raw JSON object (literal)                                                                     |
| `--from-file PATH`         | Read the JSON object from a file                                                              |
| `--preserve-existing`      | Forward as `?preserve_existing=true` (cluster keeps current value where the body would overwrite) |
| `--ignore-unavailable`     | Treat missing indices as a no-op                                                              |
| `--dry-run` (global)       | Validate and report the body that would be sent — do not contact the cluster                  |

**Common use cases**

- `esops index settings set --index 'esops-logs-2024-*' --set number_of_replicas=2 --dry-run` — preview a replica bump
- `esops index settings set --index esops-archive-2024 --set blocks.write=true --i-know-this-is-prod` — flip an index read-only before shrink
- `esops index settings set --index esops-logs-2024-12 --set refresh_interval=-1 --i-know-this-is-prod` — disable refresh during a bulk reindex
- `esops index settings set --index esops-logs --from-file /etc/esops/seal-archive.json --preserve-existing` — apply a CI-rendered profile without overwriting drift-corrected values

**Output (`-o json`)**

```json
{
  "action": "settings-update",
  "dry_run": true,
  "indices": ["esops-logs-2024-*"],
  "acknowledged": true,
  "body": "{\"index\":{\"number_of_replicas\":\"2\"}}"
}
```

`body` is included only on dry-run (so the operator can verify what would have been sent); a live call drops it from the output.

---

## esops index template

`esops index template` is the v2 (composable) index-template surface: `list`, `get`, `put`, and `delete` against `/_index_template`. The legacy `/_template` API is deliberately out of scope; both Elasticsearch and OpenSearch consider it deprecated.

Works identically on Elasticsearch and OpenSearch.

### esops index template list

Lists the cluster's index templates. The default view shows the columns operators reach for first (name, priority, version, the index_patterns the template matches); `-o wide` adds the `composed_of` chain and a `data_stream` flag.

```sh
$ esops index template list
NAME                  PRIORITY  VERSION  INDEX_PATTERNS
esops-events          100       -        events-*,metrics-*
esops-logs-v2         200       3        esops-logs-*
```

Read-only and safe to run against a prod-protected context at any time.

**Flags**

| Flag       | Purpose                                                                  |
|------------|--------------------------------------------------------------------------|
| `--name`   | Limit to specific template name(s) or pattern(s); repeatable             |

**Output formats**

- `-o table` (default) — human readable
- `-o wide` — adds `composed_of` and a data-stream flag
- `-o json` / `-o yaml` — the neutral [`IndexTemplate`](../internal/types/types.go) shape
- `-o ndjson` — one record per line (large clusters)

### esops index template get

Get a single template by literal name. The default table renders a one-line header and the index_patterns the template matches; `-o wide` appends the full `template` body (settings/mappings/aliases) as indented JSON, ready to paste into a ticket or feed back into `template put --from-file`.

```sh
$ esops index template get --name esops-logs-v2 -o wide
NAME           PRIORITY  VERSION  INDEX_PATTERNS  COMPOSED_OF
esops-logs-v2  200       3        esops-logs-*    esops-logs-mappings,esops-logs-settings

TEMPLATE
  {
    "settings": {
      "number_of_shards": "1",
      "number_of_replicas": "1"
    }
  }
```

For wildcard matching, use `template list --name`.

**Flags**

| Flag                   | Purpose                                |
|------------------------|----------------------------------------|
| `--name` (required)    | Template name (literal, no patterns)   |

### esops index template put

Create or update a v2 index template. Body input is exactly one of `--body` (literal JSON) or `--from-file` (path to a JSON file).

```sh
$ cat /etc/esops/templates/logs-v2.json
{
  "index_patterns": ["esops-logs-*"],
  "priority": 200,
  "version": 3,
  "template": {
    "settings": {
      "number_of_shards": "1",
      "number_of_replicas": "1"
    }
  }
}

$ esops index template put --name esops-logs-v2 --from-file /etc/esops/templates/logs-v2.json --dry-run
ACTION        DRY_RUN  NAME           ACKNOWLEDGED
template-put  true     esops-logs-v2  true
```

**Safety**

| Behaviour                  | Detail                                                                                                  |
|----------------------------|---------------------------------------------------------------------------------------------------------|
| `index_patterns` required  | A v2 template without `index_patterns` never matches an index — esops refuses up-front. Exit code 7.    |
| Literal name only          | `--name` rejects patterns; templates are managed one at a time.                                         |
| `--create` opt-in          | When set, the cluster refuses with a 400 if the template already exists. Safe default for first-time installs in CI. |
| Protected contexts         | Against `protection: prod`, `--i-know-this-is-prod` is required. Exit code 9 otherwise.                 |

**Flags**

| Flag                       | Purpose                                                                                       |
|----------------------------|-----------------------------------------------------------------------------------------------|
| `--name` (required)        | Template name (literal)                                                                       |
| `--body`                   | Raw JSON template definition                                                                  |
| `--from-file PATH`         | Read the JSON template body from a file                                                       |
| `--create`                 | Forward as `?create=true` (refuse if a template with this name already exists)                |
| `--dry-run` (global)       | Validate and report — do not contact the cluster                                              |

**Common use cases**

- `esops index template put --name esops-logs-v2 --from-file logs-v2.json --create --dry-run` — preview the install of a fresh template
- `esops index template put --name esops-logs-v2 --from-file logs-v2.json --i-know-this-is-prod` — apply an updated template version in prod
- `esops index template list -o json | jq '.[] | select(.priority > 100)'` — inventory of high-priority templates

### esops index template delete

Removes a single template by literal name. The cluster's own 404 surfaces a missing template with a useful message — esops does not pre-check existence.

```sh
$ esops index template delete --name esops-logs-v1 --dry-run
ACTION           DRY_RUN  NAME           ACKNOWLEDGED
template-delete  true     esops-logs-v1  true
```

**Flags**

| Flag                   | Purpose                                       |
|------------------------|-----------------------------------------------|
| `--name` (required)    | Template name (literal, no patterns)          |
| `--dry-run` (global)   | Validate and report — do not contact the cluster |

**Output (`-o json`)**

```json
{
  "action": "template-delete",
  "dry_run": false,
  "name": "esops-logs-v1",
  "acknowledged": true
}
```

`name` is echoed so a captured JSON record is self-describing.
