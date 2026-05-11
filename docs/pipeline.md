# pipeline — inspect ingest pipelines

`esops pipeline` provides `kubectl`-style commands to list and inspect ingest pipelines.

All commands are read-only and safe to run on production at any time.

Works identically on Elasticsearch and OpenSearch.

See [config.md](./config.md) for context resolution and authentication.

---

## esops pipeline list

Lists all ingest pipelines (or a filtered subset).

```sh
$ esops pipeline list
NAME                       PROCESSORS  DESCRIPTION
esops-logs-enrich          3           normalise log level + tag host with datacenter
esops-products-rename      2           rename legacy product fields
```

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Limit to specific pipeline name(s)           |
| `-o`     | `table` (default), `wide`, `json`, `ndjson` |

`-o wide` adds version, `on_failure` count, and processor types.

**Common use cases**
- `esops pipeline list` — quick inventory
- `esops pipeline list --name esops-logs-enrich -o json`
- `esops pipeline list -o ndjson | jq 'select(.processors|length > 5)'`

---

## esops pipeline get

Shows full details of a single pipeline.

```sh
$ esops pipeline get --name esops-logs-enrich
NAME               VERSION  PROCESSORS  ON_FAILURE  DESCRIPTION
esops-logs-enrich  1        3           1           normalise log level + tag host with datacenter

PROCESSORS
  [ { "lowercase": { "field": "level" } }, … ]
```

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Pipeline name (required, literal)            |
| `-o`     | `table` (default), `wide`, `json`, `yaml`   |

`-o wide` includes the `_meta` block when present.

**Common use cases**
- Inspect processor logic before editing
- Export a pipeline: `esops pipeline get --name foo -o json`

---

**All commands** support `--context`, `--url`, `--output`, and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.