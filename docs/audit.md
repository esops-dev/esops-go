# Audit log

`esops` can append one NDJSON record per invocation to a local file. The audit log is off by default; turning it on is a deliberate choice for environments with audit obligations (SOC2, ISO 27001, internal change-management) where *who ran what against which cluster, when, with what outcome* needs to be reconstructable from the operator's workstation or bastion.

The audit log is **local-only**. There is no remote sink, no telemetry, no auto-export. Ship it to a central system with whatever log pipeline you already run.

## Enabling

Two equivalent forms — pick whichever fits the operator's workflow:

```sh
# Per-invocation
esops --audit-file ~/.local/state/esops/audit.log ops health
```

```yaml
# Persistent, via the config file
defaults:
  audit_file: ~/.local/state/esops/audit.log
```

Empty / unset disables auditing entirely (no default path). The CLI flag overrides the config key. `~` and `$VAR` expansion are supported.

The file is created on first write with mode `0600`. The parent directory must exist; a missing parent disables auditing with a warning rather than failing the command — auditing must never block work.

## Record shape

One JSON object per line. Stable schema; consumers can rely on `schema_version` to detect format changes.

```json
{
  "schema_version": 1,
  "timestamp": "2026-05-28T19:14:02Z",
  "duration_ms": 482,
  "tool_version": "v0.4.1",
  "tool_commit": "fb97953",
  "os_user": "wdijkerman",
  "hostname": "ops-bastion-eu-01",
  "command": "migrate reindex",
  "args": ["esops", "--context", "prod-eu", "migrate", "reindex", "--indices", "logs-*"],
  "context": "prod-eu",
  "cluster_url": "https://es-prod-eu.example.com:9200",
  "dry_run": false,
  "exit_code": 0,
  "summary": {
    "source": "prod-eu", "target": "prod-eu-v2",
    "docs": 1284512, "created": 1284512, "updated": 0, "deleted": 0,
    "failed": 0, "requests_per_second": 5000, "indices": 12,
    "status": "ok", "blockers": 0, "warnings": 0
  }
}
```

| Field | Notes |
|---|---|
| `schema_version` | Integer. Starts at `1`. Bumps only on a breaking field change. |
| `timestamp` | RFC3339, UTC. Command start time. |
| `duration_ms` | Wall clock from `cmdsetup.Prepare` to exit. |
| `tool_version` / `tool_commit` | From the ldflags-stamped build metadata. |
| `os_user` / `hostname` | Local identity. Omitted on lookup failure. |
| `command` | Subcommand path, e.g. `ops health`, `migrate reindex`. |
| `args` | Full argv. Secret-bearing flags (`--password`, `--api-key`, `--token`, `--authorization`, `--client-key`, `--encoded`, …) have their values replaced with `<redacted>`. |
| `context` | Resolved context name when one is used. |
| `cluster_url` | Scheme + host + port only. Userinfo, paths, and query strings are stripped. |
| `dry_run` | Boolean, mirrors `--dry-run`. |
| `exit_code` | Process exit code. |
| `error` | Short class string when `exit_code != 0`: `auth`, `unreachable`, `forbidden`, `unsupported`, `dry-run-blocker`, `blast-radius`, `protected-context`, `unknown-product`, `usage`, `interrupted`, `on-error-policy`, `error`. Full error text stays in the regular log. |
| `summary` | Optional per-command structured payload. `migrate reindex` records `{source, target, docs, requests_per_second, …}`; `--on-error` commands record `{policy, succeeded, failed, rolled_back, rollback_failed}`. |

## Redaction

The audit log never carries a credential:

- Sensitive-flag values in `args` are replaced with `<redacted>` before the record is built. The list lives in `internal/audit/audit.go` (`sensitiveFlagNames`).
- Credentials embedded in `cluster_url` (`https://user:pw@host`) are stripped by URL sanitisation; only `scheme://host[:port]` survives.
- `${env:VAR}`, `${file:/path}`, `${keyring:svc/acct}` secret indirection is never resolved by the audit code, so a resolved secret value cannot reach the file even if a future field added one.

A regression test (`TestRedaction_endToEnd_*` in `internal/audit/audit_test.go`) covers both halves of §11's redaction guarantee.

## Coverage

A record is written for any command that goes through `cmdsetup.Prepare` — every `ops` / `index` / `datastream` / `snapshot` / `migrate` / `pipeline` / `transform` action. `config use-context` also audits explicitly because it mutates the config file (and changes the cluster the next invocation will hit).

Read-only local commands (`config view`, `config get-contexts`, `config validate`, `--help`, shell completion) do not produce records. Their `duration_ms` would be meaningless and they touch no cluster.

## Concurrency

Records are written with `os.File.Write` against a file opened with `O_APPEND`. POSIX guarantees atomic appends up to `PIPE_BUF` (typically 4096 bytes). Records run well under that; two concurrent `esops` processes writing to the same audit file will not interleave.

## Rotation

`esops` does not rotate the audit file. Hand it to `logrotate` or equivalent. The recommended `copytruncate` config:

```
/var/log/esops/audit.log {
    daily
    rotate 90
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su esops-svc esops-svc
    create 0600 esops-svc esops-svc
}
```

`copytruncate` is chosen because `esops` keeps the file handle only for the duration of one invocation — by the time `logrotate` runs there is nothing to SIGHUP. Mode `0600` matches what `esops` creates the file with.

## Out of scope

- **Remote sinks.** Use your existing log pipeline (`vector`, `fluent-bit`, etc.).
- **Tamper-evidence** (signed-chain audit logs). Not in v1.
- **Auto-export to syslog / journald.** Append to the local file; let the host's log shipper handle the rest.
