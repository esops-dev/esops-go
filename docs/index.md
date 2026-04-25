# index — inspect and manage indices

`esops index` gives you `kubectl`-style commands for day-to-day index operations: listing, health checks, sizing, and (in future releases) rollover, shrink, close, open, and delete.

`esops index list` is read-only and safe to run on production at any time.

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
