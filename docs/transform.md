# transform — inspect and manage transforms

`esops transform` provides `kubectl`-style commands for pivot/aggregation transforms (batch or continuous).

**Read-only** commands (`list`, `get`, `stats`, `preview`) are safe on production anytime.  
**Mutating** commands (`apply`, `delete`, `start`, `stop`) support `--dry-run` and require `--i-know-this-is-prod` on protected contexts for destructive actions.

Works on both Elasticsearch and OpenSearch (CLI surface is identical).

See [config.md](./config.md) for context resolution and authentication.

---

## esops transform list

Lists transforms with current state.

```sh
$ esops transform list
NAME           STATE    CONTINUOUS  SOURCE     DEST              DOCS_PROCESSED
audit-rollup   stopped  no          audit-*    audit-rollup      0
users-pivot    started  yes         users-*    users-pivot       982341
```

`-o wide` adds checkpoint age, failures, and description.

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Limit to specific transform id(s)            |

**Common use cases**
- `esops transform list` — quick inventory
- `esops transform list -o json | jq 'select(.failures > 0)'`

---

## esops transform get

Shows full definition of a transform.

```sh
$ esops transform get --name users-pivot
NAME         CONTINUOUS  SOURCE   DEST          DESCRIPTION
users-pivot  yes         users-*  users-pivot   daily user activity rollup
```

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Transform id (required, literal)             |

**Common use cases**
- Inspect before editing: `esops transform get --name foo -o json`

---

## esops transform stats

Shows runtime stats for a transform.

```sh
$ esops transform stats --name users-pivot
NAME         STATE    DOCS_PROCESSED  DOCS_INDEXED  CHECKPOINT_AGE
users-pivot  started  982341          979812        2m0s
```

**Flags**
| Flag     | Purpose                                      |
|----------|----------------------------------------------|
| `--name` | Transform id (required, literal)             |

**Common use cases**
- Monitor progress or failures

---

## esops transform preview

Previews output of a transform body (sample only).

```sh
$ esops transform preview --from-file ./pivot.json
DOCUMENTS: 12 (sample)

[ { … }, … ]
```

**Flags**
| Flag          | Purpose                                      |
|---------------|----------------------------------------------|
| `--from-file` | JSON body (`-` for stdin)                    |

**Common use cases**
- Test a pivot before applying

---

## esops transform apply

Create or update a transform from JSON.

```sh
$ esops transform apply --name users-pivot --from-file ./users-pivot.json
ACTION            NAME         UNCHANGED  ACK
transform-apply   users-pivot  false      true
```

**Flags**
| Flag          | Purpose                                      |
|---------------|----------------------------------------------|
| `--name`      | Transform id (required)                      |
| `--from-file` | JSON body (`-` for stdin)                    |
| `--dry-run`   | Preview only                                 |

Overwriting an existing transform is destructive on `prod` contexts.

**Common use cases**
- Idempotent declarative transforms in CI

---

## esops transform delete

Delete a transform (stops first if running).

```sh
$ esops transform delete --name users-pivot
```

**Flags**
| Flag           | Purpose                                      |
|----------------|----------------------------------------------|
| `--name`       | Transform id(s) or pattern                   |
| `--force-stop` | Stop before delete                           |
| `--dry-run`    | Preview only                                 |

**Common use cases**
- Cleanup old transforms

---

## esops transform start / stop

Start or stop a transform.

```sh
$ esops transform start --name users-pivot
$ esops transform stop --name users-pivot --wait-for-checkpoint
```

**Flags**
| Flag                    | Purpose                                      |
|-------------------------|----------------------------------------------|
| `--name`                | Transform id (required)                      |
| `--wait-for-checkpoint` | (stop only) Finish current checkpoint        |
| `--dry-run`             | Preview only                                 |

---

**All commands** support `--context`, `--url`, `--output` (table/wide/json/yaml/ndjson), `--dry-run` (where applicable), and global auth/timeout flags.  
The `context=…` line always prints to stderr so pipelines stay clean.

**Dialect note**: Body shapes differ (ES uses top-level `source`/`dest`/`pivot`; OS wraps in `{"transform": {...}}`). `apply` refuses obvious mismatches.