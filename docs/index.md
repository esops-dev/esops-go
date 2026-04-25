# index — inspect and manage indices

The `esops index` command group covers the per-index operations an
operator reaches for day to day: listing indices, inspecting their size
and health, and (in later releases) rolling them over, shrinking,
opening, closing, and template work.

All `esops index list` output is read-only — safe to run against
production at any time. Mutating subcommands (`rollover`, `shrink`,
`close`, `open`, `delete`) land in later releases and will respect the
project's blast-radius limits: destructive operations refuse to run
when their resolved pattern exceeds a safety threshold unless the
caller opts in with an explicit flag (e.g. `--max-indices N`).

See [`config.md`](./config.md) for how contexts and the active cluster
are resolved.

## `esops index list`

Lists indices with the columns an operator usually wants first: name,
health, open/close status, primary and replica counts, document count,
and size. Works identically against Elasticsearch and OpenSearch —
`esops` probes the cluster first, picks the right upstream client, and
normalises `/_cat/indices` into a neutral shape.

```sh
$ esops index list
context=local-es dialect=elasticsearch version=9.3.3 indices=3
NAME               HEALTH  STATUS  PRI  REP  DOCS  STORE
esops-logs-000001  green   open    1    0    200   34.2kb
esops-products     green   open    1    0    50    13.0kb
esops-users        green   open    1    0    20    9.0kb
```

The `context=…` banner is written to stderr; the table goes to stdout,
so pipelines stay parseable regardless of `--output`.

Hidden (dot-prefixed) indices are excluded by default — operators
almost always want to see their own data first, and `.kibana*`,
`.opendistro_security`, and similar system indices clutter the triage
view. Pass `--hidden` to include them.

### Filters

Filters compose. Index patterns are pushed down to the cluster as a
path parameter; health, status, and the hidden toggle are applied
client-side so behaviour is identical on Elasticsearch and OpenSearch.

| Flag         | Purpose                                                     |
| ------------ | ----------------------------------------------------------- |
| `--index`    | Comma-separated index patterns (e.g. `logs-*,users`).       |
| `--health`   | `green` / `yellow` / `red`. Case-insensitive.               |
| `--status`   | `open` / `close`. Case-insensitive.                         |
| `--hidden`   | Include hidden (dot-prefixed) indices.                      |
| `--sort`     | `name` (default) / `size` / `docs`. Size and docs sort descending. |

Invalid values for `--health`, `--status`, or `--sort` exit with a
clear error rather than silently falling through.

### Output formats

| Flag              | Intended for                                                 |
| ----------------- | ------------------------------------------------------------ |
| `-o table` (def)  | Human eyes. `NAME HEALTH STATUS PRI REP DOCS STORE`.         |
| `-o wide`         | Adds `DELETED`, raw `PRI_STORE`, `UUID`, and `CREATED` (RFC3339 UTC). |
| `-o json`         | Scripts. Stable keys (`name`, `health`, `status`, `primaries`, `replicas`, `docs_count`, `docs_deleted`, `store_bytes`, `primary_store_bytes`, `uuid`, `creation_date_millis`). |
| `-o yaml`         | Same shape as JSON.                                          |
| `-o ndjson`       | One JSON object per line — right for clusters with thousands of indices where the downstream consumer wants to process records without buffering. |

`STORE` in the table columns is human-friendly (`34.2kb`). `store_bytes`
in JSON/NDJSON/YAML and the `PRI_STORE` / `STORE` wide columns are
computed from the same raw-bytes payload (`bytes=b`) so they are always
exact.

`uuid` and `creation_date_millis` are `omitempty` in JSON/YAML — some
dialects (and closed indices) omit them, and the neutral types follow
the cluster rather than inventing placeholders. The wide table renders
`-` for both to keep rows rectangular.

### Examples

```sh
# Quick triage: everything that isn't green.
esops index list --health yellow
esops index list --health red

# All closed indices across a cluster (including hidden ones).
esops index list --status close --hidden

# The ten largest user-visible indices, by bytes.
esops index list --sort size -o json | jq '.[:10] | .[] | {name, store_bytes}'

# A specific data-stream backing pattern, with full detail.
esops index list --index '.ds-logs-*' --hidden -o wide

# Total disk usage across every user-visible index on a cluster.
esops index list -o json | jq '[.[].store_bytes] | add'

# Stream thousands of indices line-by-line (no buffering downstream).
esops index list --hidden -o ndjson | while read -r line; do
  echo "$line" | jq -r '[.name, .docs_count, .store_bytes] | @tsv'
done

# Cross-dialect: same command, OpenSearch cluster.
esops --context local-os index list
```

### Hidden indices

ES 8+ and OS both hide dot-prefixed indices from default `/_cat/indices`
responses unless `expand_wildcards=hidden` is set. `esops` always asks
for `open,closed,hidden` on the wire so results are complete, then
filters dot-prefixed names out business-side. That's the thing `--hidden`
switches off, and it means the two dialects produce the same
"user-visible" view — the OpenSearch security plugin's `.opendistro_security`
and the Elasticsearch `.kibana*` indices both behave the same way.

Non-dot system indices that some plugins create (OpenSearch's
`security-auditlog-*` and `top_queries-*`, for example) are **not**
hidden — they appear in the default view because they do not follow the
dot-prefix convention. Use `--index` to exclude them from a narrow
query if they are noise for your workflow.

### Exit codes

`index list` follows the project-wide exit-code contract:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Cluster reachable; rows rendered (including zero rows).  |
| 1    | Generic error (config parse, invalid filter value, etc.).|
| 3    | Cluster unreachable.                                     |
| 4    | Authentication failed (HTTP 401).                        |
| 5    | Authorization failed (HTTP 403).                         |

A cluster with zero user-visible indices still exits 0 — `index list`
reports the allocation truthfully, it does not judge it. Alerting on
"no indices" is the caller's job.

### Known limitations

- The adapter still buffers the full cluster response before handing
  rows to the renderer. NDJSON output is one record per line, but the
  network fetch is not streamed yet — acceptable for tens of thousands
  of indices, revisit when a user hits the limit.
- No `--context` protection gate is enforced on `index list` because it
  is read-only. Mutating subcommands (future) will gate on the
  per-context `protection: prod` tier.
