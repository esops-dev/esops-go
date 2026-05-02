# Contributing to esops

Thanks for the interest. This document covers how to set up a dev
environment, run the tests, and shape changes so they land cleanly.

## Ground rules

- **Single static binary.** No CGO, no JVM, no Python. `CGO_ENABLED=0`,
  works on Alpine and scratch.
- **Same UX, dialect-aware internals.** ES and OS share the CLI surface;
  the implementation branches where it has to. We do not invent a
  lowest common denominator that's wrong on both sides.
- **Small `go.mod`.** Direct dependencies are capped (≤ 12). Each
  addition must be justified in the PR description. Prefer the standard
  library; never replace it for cosmetics (no `logrus`, `zap`,
  `zerolog` — `log/slog` is in stdlib).
- **Production-first.** Every command must be safe to run against a
  live prod cluster. Destructive ops require `--dry-run` support, an
  explicit opt-in flag past the blast-radius threshold, and protected-
  context handling.
- **No telemetry.** Not opt-in, not opt-out. The user-owned OTLP tracing
  hook is a different thing — it stays in the user's infrastructure.

If your change conflicts with any of the above, open an issue first to
discuss before writing the code.

## Prerequisites

- Go matching the version pinned in [go.mod](go.mod).
- `make`, `git`, `docker` (for the local dev clusters).
- Optional: `golangci-lint`, `gosec`, `govulncheck`, `goreleaser` —
  the relevant `make` targets install or guide you through these.

## Quickstart

```sh
git clone https://github.com/esops-dev/esops-go
cd esops-go
make build
./bin/esops --help
```

`make help` lists all targets.

## Local dev clusters

Three clusters run in Docker for hands-on testing — Elasticsearch (no
TLS), OpenSearch with the security plugin, and Elasticsearch with mTLS:

```sh
make certs        # generate throw-away dev certs (idempotent)
make dev-up       # start ES (:9200/:9201), OS (:9202), ES-mTLS (:9203)
make dev-seed     # seed sample indices into both
make dev-logs     # tail
make dev-down     # tear everything down (removes ephemeral state)
```

Use `make dev-seed-unassigned` to add an over-replicated index so
`ops allocation-explain` has something interesting to chew on.

## Testing

```sh
make test                  # unit tests with the race detector
make test-integration      # build tag: integration; uses testcontainers
make lint                  # golangci-lint
make security              # gosec + govulncheck
```

Test layout:

- **Unit tests** live next to the code and use fake capability
  implementations. Fast, hermetic.
- **Contract tests** in `internal/client/` run identical assertions
  against both ES and OS implementations. This is how dialect drift
  gets caught before users see it.
- **Import-graph tests** in `internal/importguard/` enforce the
  public/private boundary: `pkg/...` may not import `internal/...`,
  and `internal/clientutil` may only be imported by the ES/OS adapters.
  These guard the surface that any future module consumes.
- **Integration tests** are gated by `//go:build integration` and use
  `testcontainers-go`. The CI fast-path runs ES latest and OS latest;
  the nightly runs the full version matrix.

A redaction test must pass: fire a request with a known-bad auth
header and assert it never appears in any log sink.

## Making a change

1. **Open an issue first** for anything beyond a docs typo or trivial
   fix. Helps avoid wasted work on something we'd reject for scope.
2. Fork, branch off `main`, keep the branch focused. One logical
   change per PR.
3. Run `make lint test security` locally before pushing.
4. Add or update tests for the behaviour you changed. Bug fixes
   include a regression test.
5. Update [docs/](docs/) when you change user-visible behaviour. The
   `README.md` exit-code and global-flags tables are the source of
   truth — keep them stable.

### Architectural rules to know before you start

- `cmd/esops/main.go` contains no logic. Wiring only.
- `pkg/` is the public API consumed by external modules. Promote with
  discipline; treat additions as semver commitments. `pkg/...` may not
  import `internal/...` — CI enforces this.
- Nothing outside `internal/client/` and `internal/clientutil/` may
  import the upstream ES or OS client packages. Domain types live in
  `pkg/types` and have no dialect-specific fields.
- Business logic depends on small capability interfaces in
  `pkg/client/capabilities.go`. There is no god `Client` interface —
  please don't introduce one. The read/mutate split (read-only
  inspectors vs mutating writers) is the contract that lets external
  read-only consumers stay read-only by construction; keep it.
- `internal/clientutil` is HTTP plumbing private to the ES/OS
  adapters. Don't import it from business logic — if you need HTTP
  error classification, that's a missing capability, not a missing
  import.
- Every command Action begins with `cmdsetup.Prepare(ctx, cmd, destructive)`.
  It loads config, resolves the context, applies CLI overrides, enforces
  the protection tier, parses `--output`, and builds a connected client.
  Don't re-implement those steps per command.
- No packages named `utils`, `helpers`, `common`, or `misc`.
- No global state. Pass a context and a configured client through the
  call chain.

### Destructive operations

If your change adds or extends a destructive op:

- Support `--dry-run` and exit `7` when the dry-run reports a blocker.
- Compute a blast radius and refuse past the threshold (default: 20
  indices for bulk ops, 50% for snapshot prune) unless the caller sets
  the explicit opt-in flag.
- Use a purpose-built confirmation flag (`--confirm-delete`,
  `--allow-data-loss`). No general `--force`.
- Honour the per-context protection tier: `prod` requires
  `--i-know-this-is-prod`; otherwise exit `9`.

## Commit and PR style

- Small, focused commits. Imperative mood in subject lines
  (`add allocation-explain output`, not `added` or `adds`).
- Reference the issue: `Closes #42`.
- PR description: what changed, why, and a short test plan. If you
  added a dependency, justify it.
- A bot will run CI on the PR. Keep it green.

## Reporting bugs and proposing features

Use the issue templates — they collect the version output and cluster
context we need to reproduce. For security issues, see
[SECURITY.md](SECURITY.md) — please do not open public issues.

## License

By contributing you agree that your contributions are licensed under
the [Apache License 2.0](LICENSE).
