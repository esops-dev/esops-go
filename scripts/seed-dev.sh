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

set -euo pipefail

URL=""
AUTH=""
WITH_UNASSIGNED=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url)             URL="$2";  shift 2 ;;
    --user)            AUTH="$2"; shift 2 ;;
    --with-unassigned) WITH_UNASSIGNED=1; shift ;;
    -h|--help)
      sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
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

echo ">>> done. doc counts:"
for idx in "${INDEXES[@]}"; do
  count=$(curl -sS "${AUTH_ARG[@]}" "$URL/$idx/_count" | sed -E 's/.*"count":([0-9]+).*/\1/')
  printf "  %-22s %s\n" "$idx" "$count"
done
