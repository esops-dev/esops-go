# esops-go

`esops` is your operations CLI for (self-managed) Elasticsearch and OpenSearch clusters. "es" (Elasticsearch, and its fork OpenSearch), "ops" for operations: because when the clusters need work, it’s time to go esops.

[![CI](https://github.com/esops-dev/esops-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/esops-dev/esops-go/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/esops-dev/esops-go?include_prereleases&sort=semver)](https://github.com/esops-dev/esops-go/releases)
[![License](https://img.shields.io/github/license/esops-dev/esops-go)](LICENSE)

> [!WARNING]
> Early scaffolding. Interfaces and exit codes are not yet stable. Not yet production ready.

`esops` is a Go based binary for Site Reliability Engineering (SRE) and DevOps processes. These processes can be both day-to-day maintenance and large scale migration efforts on a long-term basis against both Elasticsearch and OpenSearch. Both product implementations have the same Command Line Interface (CLI) as the rest of its counterparts. The actual implementations only differ in implementation (i.e. ILM & ISM etc.) or through snapshot key differences and/or re-indexing dialect.

## Why esops

The older the ES/OS cluster gets, the more often you will find yourself typing command lines using `curl` and `jq` to perform different operations like checking shard imbalance, explaining the allocation of requests, driving re-indexing, cleaning up snapshots, and swapping aliases. Rather than having a bunch of different command lines, `esops` is one binary that does all of the above.

* Adaptive, resumable reindexing (coming soon).
* Scriptable output in table, JSON, NDJSON, or YAML with stable schemas and standard exit codes.
* kubectl-style named contexts, with protection tiers (`none` / `prod`). Destructive ops on `prod` require `--i-know-this-is-prod`; dry-runs and read-only commands are always allowed.
* Secrets loaded securely via indirection (`${env:VAR}`, `${file:...}`, `${keyring:...}`); inline plaintext secrets are refused and auth headers are redacted from logs.
* Pipeline-friendly by design: `CI=true` auto-enables non-interactive mode and JSON logging. Stdout is pure data; stderr gets logs and progress. NDJSON streams large results without buffering.
* One-command diagnostics: `esops ops diag` bundles health, shards, hot threads, tasks, ILM/ISM, snapshots, etc. into a credential-free tarball: perfect for support tickets.
* Single static binary, `CGO_ENABLED=0`. No JVM, no Python runtime, no Docker. Drops into Alpine or scratch images without dragging a runtime along.

----

## Features

* **Cluster Operations**  - Health, Shard Allocation Explanation, Shard Distribution, Hot Threads, Security Audit, Rebalance Helper, Redacted Diagnostic Bundle
* **Migrating databases** - Pre-flight Check, Mapping Plan One with Incompatibility Diffs, Task based Reindex With Resume + Auto Throttle, Doc Count And Sampled Checksum Verify, Atomic Alias-swap Cutover
* **Manage your Indexes** - List, Optimise, Rollover, Shrink, Open/Close, Aliases, Settings and Template ILM (ES) and ISM (OS) Policies 
* **Manage your Snapshots**  - List, Create, Verify, Restore, Retention Prune 
* **Multiple Settings** - Named contexts like kubectl's with per-context protection levels; Explicit acknowledgement is needed for destructive operations against prod context.
* **Authentication** - Basic, API key, bearer, mTLS, AWS SigV4 

## Install

### Binary releases

Pre-built binaries for Linux (amd64 and arm64), Darwin (amd64 and arm64) and Windows (amd64) are available on the [release page](https://github.com/esops-dev/esops-go/releases). There are also .deb and .rpm packages. When a release is created, cosign signatures, SBOM, and SLSA provenance are generated.

### From source

Requires Go matching the floor in [go.mod](go.mod).

```sh
git clone https://github.com/esops-dev/esops-go
cd esops-go
make build
./bin/esops --help
```

## Quickstart

Drop a config file at `~/.config/esops/config.yaml` (see
[config/esops.example.yaml](config/esops.example.yaml) for the full
schema):

```yaml
current-context: prod-eu

defaults:
  output: table
  timeout: 30s

contexts:
  prod-eu:
    url: https://es-prod-eu.example.com:9200
    protection: prod
    username: esops
    password: ${keyring:esops/prod-eu}
  dev:
    url: http://localhost:9200
    protection: none
```

Then:

```sh
esops ops health
esops index list --health red
esops migrate plan --from idx-v1 --to idx-v2
esops migrate reindex --from idx-v1 --to idx-v2 --auto-throttle
esops migrate verify --source idx-v1 --target idx-v2
esops migrate cutover --alias idx --from idx-v1 --to idx-v2
```

## Commands

```
ops         health · nodes · shards · allocation-explain · hot-threads
            audit · rebalance · drain · uncordon · tasks · diag
migrate     plan · check · reindex · verify · cutover
index       list · optimize · rollover · shrink · open · close
            aliases · settings · template · ilm (ES) · ism (OS)
snapshot    list · create · verify · restore · prune · register · deregister
config      use-context · get-contexts · view
```

See [docs/](docs/) for per-command reference.

## Global flags

| Flag | Description |
|---|---|
| `--cacert PATH` | Custom CA bundle |
| `-c, --config PATH` | Config file (env: `ESOPS_CONFIG`) |
| `--context NAME` | Named cluster from config (alias: `--cluster`) |
| `--dry-run` | Where applicable |
| `--i-know-this-is-prod` | Required for destructive operations against a prod-protected context |
| `--insecure` | Skip TLS verification (last resort) |
| `--log-file PATH` | Append log lines to PATH instead of stderr (file created with mode 0600) |
| `--log-format FORMAT` | `text` \| `json` |
| `--log-level LEVEL` | `debug` \| `info` \| `warn` \| `error` |
| `--no-prompt` | Fail instead of prompting (CI-safe) |
| `-o, --output FORMAT` | `table` \| `json` \| `ndjson` \| `yaml` \| `wide` |
| `--timeout DURATION` | HTTP timeout (default `30s`) |
| `--url URL` | Override: single cluster URL |

`CI=true` in the environment auto-enables `--no-prompt` and defaults `--log-format=json`.

## Documentation

- [docs/index.md](docs/index.md) — index management
- [docs/ops.md](docs/ops.md) — cluster operations
- [docs/snapshot.md](docs/snapshot.md) — snapshot and repository inspection
- [docs/config.md](docs/config.md) — config schema, contexts, secrets
- [docs/completion.md](docs/completion.md) — shell completion
- [docs/auth.md](docs/auth.md) — Authentication

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, testing against the local ES/OS dev clusters, and PR conventions.

## Security

Found a vulnerability? See [SECURITY.md](SECURITY.md). Please do not open public issues for security reports.

## Non goals

* No need for AI usage in the tool. Doesn't help this tool in any way;
* No OpenTelemetry tooling/metrics will be enabled in this tool. This is a standalone tool and is not connected to anything else;
* No usage of `--force` or anything else that could destroy the cluster or its data.

## License

[Apache License 2.0](LICENSE).
