# esops

Operations CLI for self-managed Elasticsearch and OpenSearch clusters.

`esops` is a single, statically linked Go binary aimed at SRE/DevOps
workflows: day-to-day operations and high-stakes migrations against
long-lived ES and OS deployments. The CLI surface is identical across
both; the implementation branches where the products actually differ
(ILM vs ISM, security models, snapshot keys, reindex dialects).

> Status: early scaffolding. Not production ready.

## Install

```sh
# Build from source (requires Go 1.23+)
make build
./bin/esops --help
```

Binary releases (linux/darwin/windows, amd64/arm64) are produced by
GoReleaser on tagged releases.

## Usage

```sh
esops --config ./config/esops.example.yaml hello
# → hello world
```

Global flags:

| Flag | Description |
|---|---|
| `-c, --config PATH` | YAML config file (env: `ESOPS_CONFIG`) |
| `-v, --version` | Print version |
| `-h, --help` | Show help |

## Development

```sh
make help              # list targets
make build             # build ./bin/esops with version ldflags
make test              # unit tests (-race)
make lint              # golangci-lint
make security          # gosec + govulncheck
make update            # bump dependencies and tidy
make release-snapshot  # local goreleaser snapshot
```

## License

[Apache License 2.0](LICENSE).
