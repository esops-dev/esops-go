# Authentication

`esops` is a **token consumer**, not an issuer. It sends whatever credentials the cluster expects (native users, LDAP, OIDC, API keys, mTLS, SigV4, …).

See [config.md](./config.md) for context resolution and file shape.

---

## Mapping cluster realms to `auth.type`

| Cluster-side realm                         | `auth.type`     | Notes                                      |
|--------------------------------------------|-----------------|--------------------------------------------|
| Native / Internal users, LDAP, AD          | `basic`         | Username + password                        |
| OIDC / JWT / Service tokens                | `bearer`        | Pre-minted token                           |
| Elasticsearch API keys                     | `api_key`       | ES only                                    |
| PKI / mTLS                                 | `mtls`          | Client certificate + key                   |
| AWS IAM (SigV4)                            | `sigv4`         | OpenSearch only                            |

SAML and browser flows are out of scope, mint an API key or service token instead.

---

## Auth-type reference

| `auth.type` | Required fields                  | Dialects   | When to use                                      |
|-------------|----------------------------------|------------|--------------------------------------------------|
| `basic`     | `username` + `password`          | ES, OS     | Native users, LDAP, AD                           |
| `api_key`   | `api_key`                        | ES only    | Long-lived non-human credentials                 |
| `bearer`    | `token`                          | ES, OS     | OIDC/JWT, ES service tokens                      |
| `mtls`      | `client_cert`, `client_key`      | ES, OS     | Client certificate authentication                |
| `sigv4`     | `region`, `service`              | OS only    | AWS IAM (self-managed or managed OpenSearch)     |

### `basic`
```yaml
auth:
  type: basic
  username: ops-svc
  password: ${env:ESOPS_PROD_PASSWORD}
```

### `api_key` (ES only)
```yaml
auth:
  type: api_key
  api_key: ${env:ESOPS_STAGING_API_KEY}
```

### `bearer`
```yaml
auth:
  type: bearer
  token: ${env:ESOPS_BEARER_TOKEN}
```

### `mtls`
```yaml
auth:
  type: mtls
  client_cert: /etc/esops/prod.crt
  client_key:  /etc/esops/prod.key
```

### `sigv4` (OS only)
```yaml
auth:
  type: sigv4
  region: eu-west-1
  service: es        # or "aoss" for Serverless
```

---

## TLS

```yaml
tls:
  cacert: /etc/ssl/certs/company-ca.pem   # custom CA
  insecure: false                         # last resort only
```

CLI overrides: `--cacert PATH` or `--insecure`.

---

## Secret indirection

Use references instead of plaintext:

| Scheme                | Example                              |
|-----------------------|--------------------------------------|
| `${env:VAR}`          | `password: ${env:ESOPS_PROD_PASSWORD}` |
| `${file:/path}`       | `password: ${file:/run/secrets/pw}`  |
| `${keyring:svc/acct}` | `password: ${keyring:esops/prod-eu}` |

`client_cert` and `client_key` are always filesystem paths.

---

## Safety rules

- World-readable config + inline plaintext secret → hard error (or set `ESOPS_ALLOW_INSECURE_CONFIG=1`).
- Inline plaintext secrets → warning (silence with `ESOPS_ALLOW_PLAINTEXT_SECRETS=1`).
- `config view` redacts secrets by default (`--show-secrets` to see them).
- All `Authorization` headers and secrets are redacted from logs.

---

## Protection tiers

Per-context `protection: none|staging|prod` gates destructive operations.

- `prod` → requires `--i-know-this-is-prod`
- Read-only commands and `--dry-run` are always allowed

---

## Quick reference

| Your setup                                      | Use `auth.type` |
|-------------------------------------------------|-----------------|
| Local dev / security disabled                   | (omit `auth`)   |
| Native users / LDAP / AD                        | `basic`         |
| OIDC / JWT / service token                      | `bearer`        |
| Elasticsearch API key                           | `api_key`       |
| Client certificate (PKI)                        | `mtls`          |
| OpenSearch on AWS IAM                           | `sigv4`         |

**All commands** support `--context`, `--url`, and global auth/timeout flags.
