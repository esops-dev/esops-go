#!/usr/bin/env bash
# Seed a local dev cluster (ES or OS) with a small amount of realistic data.
#
# Idempotent: deletes and recreates each index. Safe to re-run.
# Works against both Elasticsearch and OpenSearch over the same APIs.
#
# Usage:
#   scripts/seed-dev.sh --url http://localhost:9200
#   scripts/seed-dev.sh --url http://localhost:9202 --user 'admin:EsopsDev123!'
#
# Flags:
#   --with-unassigned    Also create `esops-stuck-replicas`, an index
#                        over-replicated on purpose (replicas=3) so it stays
#                        YELLOW on the 2-node ES cluster and on the 1-node
#                        OS cluster. Exercises `ops allocation-explain`
#                        and the unassigned-reason path in `ops shards`.
#
#   --with-data-streams  Also create `esops-logs-stream` and `esops-metrics-stream`
#                        as data streams (via an index template that names them
#                        in `index_patterns`), bootstrap one backing index per
#                        stream, and roll `esops-logs-stream` once so the stream
#                        has a multi-generation chain. Gives `esops datastream
#                        list / get / rollover / delete` and `esops index list
#                        --exclude-managed` something realistic to render.
#
#   --with-snapshots     Register an `fs` repository (`local-fs`) at the
#                        cluster's path.repo and take two snapshots:
#                        `seed-products-<ts>` (esops-products only) and
#                        `seed-all-<ts>` (every esops-* index). Also
#                        registers a scheduled-snapshot policy
#                        (`seed-nightly`) — SLM on Elasticsearch, SM
#                        on OpenSearch — so `snapshot policy list/get`
#                        has something to render. The policy will
#                        never actually fire because the schedule is
#                        well in the future, but the wire shape is real.
#                        Requires path.repo to be configured cluster-
#                        side — the local docker-compose sets this
#                        automatically. Exercises `snapshot list`,
#                        `snapshot repository list`, `snapshot policy
#                        list` and gives `snapshot create/verify/
#                        restore/prune` something to work with later.

set -euo pipefail

URL=""
AUTH=""
WITH_UNASSIGNED=0
WITH_SNAPSHOTS=0
WITH_DATA_STREAMS=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url)               URL="$2";  shift 2 ;;
    --user)              AUTH="$2"; shift 2 ;;
    --with-unassigned)   WITH_UNASSIGNED=1; shift ;;
    --with-snapshots)    WITH_SNAPSHOTS=1;  shift ;;
    --with-data-streams) WITH_DATA_STREAMS=1; shift ;;
    -h|--help)
      sed -n '2,38p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$URL" ]] || { echo "--url is required" >&2; exit 2; }

AUTH_ARG=()
[[ -n "$AUTH" ]] && AUTH_ARG=(-u "$AUTH")

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

# http METHOD PATH [BODY] [CONTENT_TYPE]
# Captures the response body in $TMP and surfaces it on HTTP >= 400.
http() {
  local method=$1 path=$2 body=${3:-} ctype=${4:-application/json}
  local code
  if [[ -n "$body" ]]; then
    code=$(curl -sS "${AUTH_ARG[@]}" -o "$TMP" -w '%{http_code}' \
      -H "Content-Type: $ctype" -X "$method" "$URL$path" --data-binary "$body")
  else
    code=$(curl -sS "${AUTH_ARG[@]}" -o "$TMP" -w '%{http_code}' \
      -X "$method" "$URL$path")
  fi
  if (( code >= 400 )); then
    echo "HTTP $code on $method $path" >&2
    head -c 4000 "$TMP" >&2; echo >&2
    return 1
  fi
}

bulk() {
  local path=$1 body=$2
  http POST "$path" "$body" application/x-ndjson
  if grep -q '"errors":true' "$TMP"; then
    echo "bulk errors on $path:" >&2
    head -c 4000 "$TMP" >&2; echo >&2
    return 1
  fi
}

# Portable ISO-8601 UTC from epoch seconds (BSD date on macOS, GNU date on Linux).
iso_date() {
  date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ
}

echo ">>> waiting for cluster ready at $URL"
for _ in $(seq 1 30); do
  status=$(curl -sS "${AUTH_ARG[@]}" -o /dev/null -w '%{http_code}' \
    "$URL/_cluster/health?wait_for_status=yellow&timeout=5s" || true)
  [[ "$status" == "200" ]] && break
  sleep 1
done

# Probe dialect once — both products advertise themselves in GET /.
# A handful of fields diverge in the seed payloads below (ingest
# pipeline `_meta`, snapshot policy shape); set the flag here so each
# section can consult it without re-probing.
IS_OS=0
if curl -sS "${AUTH_ARG[@]}" "$URL/" | grep -q '"distribution"[[:space:]]*:[[:space:]]*"opensearch"'; then
  IS_OS=1
fi

# Note on names: ES 9 ships a built-in `logs-*-*` data-stream index template,
# so a regular-index PUT on `logs-app-000001` is rejected. Prefix with `esops-`
# to stay out of that (and any Elastic-managed) namespace.
INDEXES=(esops-logs-000001 esops-products esops-users)

echo ">>> (re)creating indexes"
for idx in "${INDEXES[@]}"; do
  curl -sS "${AUTH_ARG[@]}" -o /dev/null -X DELETE "$URL/$idx" || true
done

http PUT /esops-logs-000001 '{
  "settings": {"number_of_shards": 1, "number_of_replicas": 0},
  "mappings": {
    "properties": {
      "@timestamp":  {"type": "date"},
      "level":       {"type": "keyword"},
      "service":     {"type": "keyword"},
      "host":        {"type": "keyword"},
      "message":     {"type": "text"},
      "status":      {"type": "short"},
      "duration_ms": {"type": "integer"}
    }
  }
}'
http POST /_aliases '{"actions":[{"add":{"index":"esops-logs-000001","alias":"esops-logs","is_write_index":true}}]}'

http PUT /esops-products '{
  "settings": {"number_of_shards": 1, "number_of_replicas": 0},
  "mappings": {
    "properties": {
      "sku":      {"type": "keyword"},
      "name":     {"type": "text", "fields": {"keyword": {"type": "keyword"}}},
      "category": {"type": "keyword"},
      "price":    {"type": "scaled_float", "scaling_factor": 100},
      "in_stock": {"type": "boolean"},
      "tags":     {"type": "keyword"}
    }
  }
}'

http PUT /esops-users '{
  "settings": {"number_of_shards": 1, "number_of_replicas": 0},
  "mappings": {
    "properties": {
      "user_id":     {"type": "keyword"},
      "name":        {"type": "text"},
      "email":       {"type": "keyword"},
      "country":     {"type": "keyword"},
      "signup_date": {"type": "date"}
    }
  }
}'

NOW=$(date -u +%s)

echo ">>> seeding esops-logs-000001 (200 docs)"
LEVELS=(INFO INFO INFO INFO WARN WARN ERROR DEBUG)
SERVICES=(api worker scheduler auth payments)
HOSTS=(host-a host-b host-c host-d)
MESSAGES=(
  "request completed"
  "cache miss"
  "connection reset by peer"
  "token refreshed"
  "slow query detected"
  "retry scheduled"
  "payload rejected"
  "downstream timeout"
)
body=""
for i in $(seq 1 200); do
  ts=$(iso_date $((NOW - i*60)))
  lvl=${LEVELS[$((RANDOM % ${#LEVELS[@]}))]}
  svc=${SERVICES[$((RANDOM % ${#SERVICES[@]}))]}
  host=${HOSTS[$((RANDOM % ${#HOSTS[@]}))]}
  msg=${MESSAGES[$((RANDOM % ${#MESSAGES[@]}))]}
  status=$(( 200 + RANDOM % 300 ))
  dur=$(( RANDOM % 2000 ))
  body+='{"index":{}}'$'\n'
  body+="{\"@timestamp\":\"$ts\",\"level\":\"$lvl\",\"service\":\"$svc\",\"host\":\"$host\",\"message\":\"$msg\",\"status\":$status,\"duration_ms\":$dur}"$'\n'
done
bulk "/esops-logs-000001/_bulk?refresh=wait_for" "$body"

echo ">>> seeding esops-products (50 docs)"
CATEGORIES=(books electronics clothing home kitchen toys)
NAMES=(Widget Gadget Sprocket Thingamajig Doohickey Gizmo Contraption Gubbins)
body=""
for i in $(seq 1 50); do
  sku=$(printf "SKU-%04d" "$i")
  cat=${CATEGORIES[$(( i % ${#CATEGORIES[@]} ))]}
  name="${NAMES[$((RANDOM % ${#NAMES[@]}))]} $i"
  price=$(awk -v s=$RANDOM 'BEGIN{printf "%.2f", 4.99 + (s%10000)/100.0}')
  if (( RANDOM % 4 == 0 )); then stock=false; else stock=true; fi
  body+='{"index":{}}'$'\n'
  body+="{\"sku\":\"$sku\",\"name\":\"$name\",\"category\":\"$cat\",\"price\":$price,\"in_stock\":$stock,\"tags\":[\"$cat\",\"new\"]}"$'\n'
done
bulk "/esops-products/_bulk?refresh=wait_for" "$body"

echo ">>> seeding esops-users (20 docs)"
COUNTRIES=(NL DE FR US UK JP BR CA AU ES)
FIRSTS=(Alex Sam Jordan Taylor Robin Casey Morgan River Quinn Skyler)
LASTS=(Smith Jones Mueller Dubois Silva Tanaka Kim Patel Novak Santos)
body=""
for i in $(seq 1 20); do
  uid=$(printf "U-%03d" "$i")
  fn=${FIRSTS[$((RANDOM % ${#FIRSTS[@]}))]}
  ln=${LASTS[$((RANDOM % ${#LASTS[@]}))]}
  country=${COUNTRIES[$((RANDOM % ${#COUNTRIES[@]}))]}
  days=$(( 1 + RANDOM % 730 ))
  signup=$(iso_date $((NOW - days*86400)))
  email=$(echo "${fn}.${ln}@example.com" | tr '[:upper:]' '[:lower:]')
  body+='{"index":{}}'$'\n'
  body+="{\"user_id\":\"$uid\",\"name\":\"$fn $ln\",\"email\":\"$email\",\"country\":\"$country\",\"signup_date\":\"$signup\"}"$'\n'
done
bulk "/esops-users/_bulk?refresh=wait_for" "$body"

# Register a small ingest-pipeline catalogue so `pipeline list` /
# `pipeline get` have something to render. Both Elasticsearch and
# OpenSearch share the path and wire shape, so the same bodies work
# against either dialect. PUT semantics are upsert — safe to re-run.
echo ">>> registering ingest pipelines"
# `_meta` on ingest pipelines is Elasticsearch 8.x-only; OpenSearch
# rejects it with parse_exception. Build the body so `_meta` is the
# last key and append it conditionally to keep the JSON valid on
# either dialect.
ENRICH_BODY='{
  "description": "esops dev seed: normalise log level + tag the host with a datacenter id",
  "version": 1,
  "processors": [
    {"lowercase": {"field": "level"}},
    {"set": {"field": "datacenter", "value": "dev-local"}},
    {"remove": {"field": "duration_ms", "ignore_missing": true, "if": "ctx.duration_ms == null"}}
  ],
  "on_failure": [
    {"set": {"field": "_ingest_error", "value": "{{ _ingest.on_failure_message }}"}}
  ]'
if (( IS_OS == 0 )); then
  ENRICH_BODY+=',
  "_meta": {"managed-by": "esops-seed"}'
fi
ENRICH_BODY+='
}'
http PUT /_ingest/pipeline/esops-logs-enrich "$ENRICH_BODY"

http PUT /_ingest/pipeline/esops-products-rename '{
  "description": "esops dev seed: rename legacy product fields to the current schema",
  "version": 2,
  "processors": [
    {"rename": {"field": "title", "target_field": "name", "ignore_missing": true}},
    {"rename": {"field": "cost",  "target_field": "price", "ignore_missing": true}}
  ]
}'

if (( WITH_UNASSIGNED )); then
  # Over-replicate on purpose: 3 replicas + 1 primary = 4 copies needed.
  # ES dev (2 data nodes) places 2 copies, leaves 2 unassigned.
  # OS dev (1 data node) places 1 primary,  leaves 3 unassigned.
  # Either way the cluster goes YELLOW and `ops allocation-explain` has
  # a real unassigned shard to reason about.
  echo ">>> creating esops-stuck-replicas (replicas=3, deliberately unallocatable)"
  curl -sS "${AUTH_ARG[@]}" -o /dev/null -X DELETE "$URL/esops-stuck-replicas" || true
  http PUT /esops-stuck-replicas '{
    "settings": {"number_of_shards": 1, "number_of_replicas": 3},
    "mappings": {"properties": {"note": {"type": "text"}}}
  }'
  # One doc so the index shows a size > 0 in `ops shards` / `index list`.
  http POST /esops-stuck-replicas/_doc '{"note":"seeded to force unassigned replicas — see ops allocation-explain"}'
  INDEXES+=(esops-stuck-replicas)
fi

if (( WITH_DATA_STREAMS )); then
  # Register an index template that names data streams in its
  # index_patterns and carries the `data_stream` marker — the cluster
  # spins up a backing index on first write. Both Elasticsearch and
  # OpenSearch share the v2 (composable) template wire shape, so the
  # same body works against either dialect.
  echo ">>> registering composable template 'esops-streams-tmpl' for data streams"
  http PUT /_index_template/esops-streams-tmpl '{
    "index_patterns": ["esops-logs-stream", "esops-metrics-stream"],
    "data_stream": {},
    "priority": 500,
    "template": {
      "settings": {"number_of_shards": 1, "number_of_replicas": 0},
      "mappings": {
        "properties": {
          "@timestamp": {"type": "date"},
          "level":      {"type": "keyword"},
          "service":    {"type": "keyword"},
          "value":      {"type": "double"}
        }
      }
    }
  }'

  # Tear down any previous run so re-runs stay idempotent. DELETE
  # against a non-existent stream returns 404 — swallow the error.
  for ds in esops-logs-stream esops-metrics-stream; do
    curl -sS "${AUTH_ARG[@]}" -o /dev/null -X DELETE "$URL/_data_stream/$ds" || true
  done

  echo ">>> bootstrapping data stream 'esops-logs-stream' (50 docs)"
  body=""
  for i in $(seq 1 50); do
    ts=$(iso_date $((NOW - i*60)))
    body+='{"create":{}}'$'\n'
    body+="{\"@timestamp\":\"$ts\",\"level\":\"info\",\"service\":\"seed-dev\"}"$'\n'
  done
  bulk "/esops-logs-stream/_bulk?refresh=wait_for" "$body"

  # Roll the stream once so it has a multi-generation backing-index
  # chain — exercises `esops datastream list` (oldest != newest) and
  # the rollover/cutover paths.
  echo ">>> rolling 'esops-logs-stream' to generation 2"
  http POST /esops-logs-stream/_rollover '{}'

  echo ">>> bootstrapping data stream 'esops-metrics-stream' (20 docs)"
  body=""
  for i in $(seq 1 20); do
    ts=$(iso_date $((NOW - i*120)))
    body+='{"create":{}}'$'\n'
    body+="{\"@timestamp\":\"$ts\",\"service\":\"seed-dev\",\"value\":$(( RANDOM % 1000 ))}"$'\n'
  done
  bulk "/esops-metrics-stream/_bulk?refresh=wait_for" "$body"
fi

if (( WITH_SNAPSHOTS )); then
  # Discover the cluster's path.repo rather than hard-coding it, so a
  # non-default bootstrap config (e.g. a self-managed cluster) still
  # works. /_nodes/_all/settings carries the resolved path even when
  # nothing else uses it; filter_path keeps the response tiny.
  PATH_REPO=$(curl -sS "${AUTH_ARG[@]}" \
    "$URL/_nodes/_all/settings?filter_path=nodes.*.settings.path.repo" \
    | grep -oE '"/[^"]+"' | head -1 | tr -d '"')
  if [[ -z "$PATH_REPO" ]]; then
    echo "!!! cluster has no path.repo set — register an fs repo manually or rebuild dev clusters" >&2
    echo "!!! (the local docker-compose configures path.repo automatically; run 'make dev-down && make dev-up')" >&2
    exit 1
  fi

  REPO_NAME=local-fs
  echo ">>> registering fs repository '$REPO_NAME' at $PATH_REPO"
  http PUT "/_snapshot/$REPO_NAME" "{\"type\":\"fs\",\"settings\":{\"location\":\"$PATH_REPO\",\"compress\":true}}"

  TS=$(date -u +%Y%m%d-%H%M%S)
  SNAP_PRODUCTS="seed-products-$TS"
  SNAP_ALL="seed-all-$TS"

  echo ">>> taking snapshot '$SNAP_PRODUCTS' (esops-products only)"
  http PUT "/_snapshot/$REPO_NAME/$SNAP_PRODUCTS?wait_for_completion=true" \
    '{"indices":"esops-products","include_global_state":false}'

  echo ">>> taking snapshot '$SNAP_ALL' (esops-* wildcard)"
  http PUT "/_snapshot/$REPO_NAME/$SNAP_ALL?wait_for_completion=true" \
    '{"indices":"esops-*","include_global_state":false}'

  # Register a scheduled-snapshot policy. The wire format diverges
  # between products: ES wants /_slm/policy/<name> with SLM's flat
  # schedule+config shape; OS wants /_plugins/_sm/policies/<name> with
  # SM's creation+deletion+snapshot_config block.
  echo ">>> registering scheduled snapshot policy 'seed-nightly'"
  if (( IS_OS )); then
    # OpenSearch — Snapshot Management plugin (OS 2.1+).
    # OS SM uses Quartz 5-part cron (minute hour day-of-month month
    # day-of-week), not the 6-part form ES SLM accepts. Mismatch is the
    # most common reason a hand-rolled SM policy bounces with HTTP 400.
    http PUT "/_plugins/_sm/policies/seed-nightly" '{
      "description": "esops dev seed: nightly snapshot of esops-* into local-fs",
      "creation": {
        "schedule": { "cron": { "expression": "30 1 * * *", "timezone": "UTC" } },
        "time_limit": "1h"
      },
      "deletion": {
        "schedule": { "cron": { "expression": "0 2 * * *", "timezone": "UTC" } },
        "condition": { "max_age": "30d", "max_count": 50, "min_count": 5 },
        "time_limit": "1h"
      },
      "snapshot_config": {
        "date_format": "yyyy-MM-dd",
        "indices": "esops-*",
        "repository": "local-fs"
      }
    }' || echo "!!! SM plugin may not be available on this OS version — skipping policy"
  else
    # Elasticsearch — Snapshot Lifecycle Management.
    http PUT "/_slm/policy/seed-nightly" '{
      "schedule": "0 30 1 * * ?",
      "name": "<seed-nightly-{now/d}>",
      "repository": "local-fs",
      "config": { "indices": ["esops-*"], "include_global_state": false },
      "retention": { "expire_after": "30d", "min_count": 5, "max_count": 50 }
    }'
  fi
fi

echo ">>> done. doc counts:"
for idx in "${INDEXES[@]}"; do
  count=$(curl -sS "${AUTH_ARG[@]}" "$URL/$idx/_count" | sed -E 's/.*"count":([0-9]+).*/\1/')
  printf "  %-22s %s\n" "$idx" "$count"
done

if (( WITH_SNAPSHOTS )); then
  echo ">>> snapshots in $REPO_NAME:"
  curl -sS "${AUTH_ARG[@]}" "$URL/_snapshot/$REPO_NAME/_all?filter_path=snapshots.snapshot,snapshots.state" \
    | sed -E 's/[][{}"]//g; s/,/\n  /g; s/^/  /'
  echo
fi
