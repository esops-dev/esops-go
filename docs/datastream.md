# datastream — inspect and manage data streams

`esops datastream` provides `kubectl`-style commands for data-stream lifecycle: listing, stats, recovery, force-merge, rollover, splicing backing indices, and deletion.

**Read-only** commands (`list`, `get`, `stats`, `recovery`, `segments`) are safe on production anytime.  
**Mutating** commands (`optimize`, `rollover`, `modify`, `delete`) require `--dry-run` preview and `--i-know-this-is-prod` on protected contexts.

Works identically on Elasticsearch and OpenSearch.

See [config.md](./config.md) for context resolution and authentication.

---

## esops datastream list

Lists data streams with status, generation, backing indices, lifecycle, size, and doc count.

```sh
$ esops datastream list
NAME                  STATUS  GEN  INDICES  OLDEST                                   NEWEST                                   SIZE   DOCS
esops-logs-stream     GREEN   2    2        .ds-esops-logs-stream-…-000001           .ds-esops-logs-stream-…-000002           6.7kb  50
```

**Flags**
| Flag       | Purpose                                      |
|------------|----------------------------------------------|
| `--name`   | Comma-separated names or patterns            |
| `--hidden` | Include hidden (integration-managed) streams |

**Common use cases**
- `esops datastream list --name 'logs-*'`
- `esops datastream list -o json`

---

## esops datastream get

Shows details for one data stream (backing indices, lifecycle, template).

```sh
$ esops datastream get --name esops-logs-stream
NAME               STATUS  GEN  INDICES  WRITE_INDEX                              LIFECYCLE  SIZE   DOCS
esops-logs-stream  GREEN   2    2        .ds-esops-logs-stream-…-000002           -          6.7kb  50
```

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Data stream name (required, literal)         |

---

## esops datastream stats

Aggregate stats across backing indices.

```sh
$ esops datastream stats --name esops-logs-stream
NAME                  INDICES  SIZE   MAX_TIMESTAMP
esops-logs-stream     2        6.7kb  2026-05-19T09:48:41Z
```

**Common use cases**
- Quick size and freshness check
- `esops datastream stats -o json`

---

## esops datastream recovery / segments

Show in-flight recoveries or segment stats for a stream’s backing indices.

```sh
$ esops datastream recovery --name esops-logs-stream
$ esops datastream segments --name esops-logs-stream
```

(Forwards to the same output as `esops index recovery` / `esops index segments`.)

**Common use cases**
- Monitor restores or force-merge candidates

---

## esops datastream optimize

Force-merge backing indices (skips write index by default).

```sh
$ esops datastream optimize --name esops-logs-stream --max-num-segments 1 --dry-run
```

**Flags**
- `--name` (required)
- `--max-num-segments`
- `--only-expunge-deletes`
- `--include-write-index`
- `--dry-run`

**Common use cases**
- Seal rotated generations after rollover

---

## esops datastream rollover

Roll a data stream to a new generation.

```sh
$ esops datastream rollover --name esops-logs-stream --max-age 7d --dry-run
```

**Flags**
- `--name` (required)
- `--max-age`, `--max-docs`, `--max-size`, `--max-primary-size`
- `--force`
- `--dry-run`

---

## esops datastream modify

Splice backing indices into or out of a stream.

```sh
$ esops datastream modify --name esops-logs-stream --add-index reindexed-logs-2025-12 --dry-run
```

**Flags**
- `--name` (required)
- `--add-index`, `--remove-index`
- `--dry-run`

---

## esops datastream delete

Delete a data stream and all its backing indices.

```sh
$ esops datastream delete --name 'test-*' --allow-data-loss --dry-run
```

**Flags**
- `--name` (required)
- `--allow-data-loss` (required for live delete)
- `--dry-run`

---

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run` (where applicable), and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.