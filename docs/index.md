# index — inspect and manage indices

`esops index` gives you `kubectl`-style commands for day-to-day index operations: listing, opening and closing, and (in future releases) rollover, shrink, and delete.

`esops index list` is read-only and safe to run on production at any time. `esops index open` and `esops index close` mutate cluster state — see the safety notes per command.

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
