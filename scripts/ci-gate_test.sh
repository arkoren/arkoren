#!/usr/bin/env bash
# Exercise the same result predicate used by the required CI gate.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
gate="$script_dir/ci-gate.sh"
success='{"discovery":{"result":"success"},"check":{"result":"success"},"integration":{"result":"success"},"tooling":{"result":"success"}}'

# reject requires a non-successful exit for an incomplete or failed result set.
reject() {
  if bash "$gate" <<< "$1" >/dev/null 2>&1; then
    printf 'Gate accepted invalid results: %s\n' "$1" >&2
    exit 1
  fi
}

bash "$gate" <<< "$success" >/dev/null
for job in discovery check integration tooling; do
  for result in failure cancelled skipped; do
    reject "$(jq --arg job "$job" --arg result "$result" '.[$job].result = $result' <<< "$success")"
  done
  reject "$(jq --arg job "$job" 'del(.[$job])' <<< "$success")"
done
reject '{}'
reject ''
reject "$success $success"
reject 'null'
reject 'invalid json'
reject "$(jq '.extra = {result: "success"}' <<< "$success")"
printf 'CI gate fixtures passed\n'
