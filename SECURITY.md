# Security Policy

## Reporting a vulnerability

Please do **not** open public GitHub issues for security problems.

Report privately via GitHub Security Advisories:
https://github.com/dj-wasabi/esops-go/security/advisories/new

Include:

- Affected version, OS, and architecture
- Steps to reproduce
- Impact assessment (what an attacker can do)

We aim to acknowledge within 72 hours and to ship a fix or coordinate
disclosure within 30 days for confirmed vulnerabilities.

## Scope

In scope:

- The `esops` binary and anything under `internal/` or `cmd/`
- Release artifacts published on GitHub Releases
- Supply-chain concerns: build pipeline, signing, SBOM

Out of scope:

- Vulnerabilities in Elasticsearch or OpenSearch themselves — report
  those to the upstream projects
- Misconfigurations of clusters `esops` connects to

## Supported versions

While the project is pre-1.0, only the latest minor release receives
security fixes.
