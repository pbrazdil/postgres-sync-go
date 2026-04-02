#!/usr/bin/env bash

set -euo pipefail

BASE_URL=${BASE_URL:-http://127.0.0.1:3100}
SECRET=${SECRET:-test-secret}
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SUBSET_POST_BODY="$SCRIPT_DIR/subset-post.json"

usage() {
  cat <<EOF
usage:
  $(basename "$0") <command> [args]

commands:
  health
  initial-items
  filtered-items
  subquery-items
  columns-items
  subset-get
  subset-post
  offset-now
  continuation <handle> <offset>
  live-longpoll <handle> [offset]
  live-sse <handle> [offset]
  partition-root

environment:
  BASE_URL   default: $BASE_URL
  SECRET     default: $SECRET
EOF
}

curl_json() {
  curl --silent --show-error -i "$@"
}

auth_query() {
  printf 'secret=%s' "$SECRET"
}

command_name=${1:-}
case "$command_name" in
  health)
    curl_json "$BASE_URL/v1/health"
    ;;
  initial-items)
    curl_json "$BASE_URL/v1/shape?table=items&offset=-1&$(auth_query)"
    ;;
  filtered-items)
    curl_json "$BASE_URL/v1/shape?table=items&offset=-1&where=priority%20%3E%3D%202&$(auth_query)"
    ;;
  subquery-items)
    curl_json "$BASE_URL/v1/shape?table=items&offset=-1&where=id%20IN%20%28SELECT%20item_id%20FROM%20item_flags%20WHERE%20enabled%20%3D%20true%29&$(auth_query)"
    ;;
  columns-items)
    curl_json "$BASE_URL/v1/shape?table=items&offset=-1&columns=id,value&$(auth_query)"
    ;;
  subset-get)
    curl_json "$BASE_URL/v1/shape?table=items&offset=-1&subset__order_by=priority%20ASC&subset__limit=2&$(auth_query)"
    ;;
  subset-post)
    curl --silent --show-error -i \
      -X POST \
      -H 'content-type: application/json' \
      --data "@$SUBSET_POST_BODY" \
      "$BASE_URL/v1/shape?table=items&offset=-1&$(auth_query)"
    ;;
  offset-now)
    curl_json "$BASE_URL/v1/shape?table=items&offset=now&$(auth_query)"
    ;;
  continuation)
    handle=${2:-}
    offset=${3:-}
    if [ -z "$handle" ] || [ -z "$offset" ]; then
      usage
      exit 2
    fi
    curl_json "$BASE_URL/v1/shape?table=items&handle=${handle}&offset=${offset}&$(auth_query)"
    ;;
  live-longpoll)
    handle=${2:-}
    offset=${3:-0_0}
    if [ -z "$handle" ]; then
      usage
      exit 2
    fi
    curl_json "$BASE_URL/v1/shape?table=items&handle=${handle}&offset=${offset}&live=true&$(auth_query)"
    ;;
  live-sse)
    handle=${2:-}
    offset=${3:-0_0}
    if [ -z "$handle" ]; then
      usage
      exit 2
    fi
    curl --silent --show-error -i -N \
      "$BASE_URL/v1/shape?table=items&handle=${handle}&offset=${offset}&live=true&live_sse=true&$(auth_query)"
    ;;
  partition-root)
    curl_json "$BASE_URL/v1/shape?table=partitioned_items&offset=-1&$(auth_query)"
    ;;
  *)
    usage
    exit 2
    ;;
esac
