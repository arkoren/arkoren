#!/usr/bin/env bash
# Exercise the same result predicate used by the required CI gate.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
gate="$script_dir/ci-gate.sh"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-ci-gate.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cd "$scratch"
mkdir .github
printf '%s\n' '{"schema":1,"public":["core","cmd/arkoren","driver/a","foundation/f"],"development":["cmd/arkoren-ci"]}' > .github/release-modules.json
for module in core cmd/arkoren cmd/arkoren-ci driver/a foundation/f; do
  mkdir -p "$module"
  touch "$module/go.mod"
done
touch driver/a/compose.yaml
export PLAN
export CI_TESTED_SHA=1111111111111111111111111111111111111111
PLAN='{"schema":1,"mode":"full","tested_sha":"1111111111111111111111111111111111111111","base_sha":"","modules":["cmd/arkoren","cmd/arkoren-ci","core","driver/a","foundation/f"],"integration":["driver/a"],"website":true,"reason":"fixture"}'
full_plan="$PLAN"
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

# Empty selections must explain skips; successful or skipped unrelated jobs cannot
# turn a failed/missing required job into a passing gate.
PLAN=$(jq '.mode="selective" | .base_sha=.tested_sha | .modules=[] | .integration=[] | .website=false' <<< "$full_plan")
empty_plan="$PLAN"
empty_results=$(jq '.check.result="skipped" | .integration.result="skipped"' <<< "$success")
bash "$gate" <<< "$empty_results" >/dev/null
reject "$success"
for job in discovery tooling check integration; do
  for result in failure cancelled; do
    reject "$(jq --arg job "$job" --arg result "$result" '.[$job].result=$result' <<< "$empty_results")"
  done
  reject "$(jq --arg job "$job" 'del(.[$job])' <<< "$empty_results")"
done
for job in discovery tooling; do
  reject "$(jq --arg job "$job" '.[$job].result="skipped"' <<< "$empty_results")"
done

PLAN=$(jq '.modules=["cmd/arkoren-ci"]' <<< "$empty_plan")
cli_results=$(jq '.integration.result="skipped"' <<< "$success")
bash "$gate" <<< "$cli_results" >/dev/null
reject "$empty_results"
reject "$success"

PLAN=$(jq '.modules=["driver/a"] | .integration=["driver/a"] | .website=true' <<< "$empty_plan")
driver_plan="$PLAN"
bash "$gate" <<< "$success" >/dev/null
reject "$cli_results"

# Malformed plans, unknown roots, omitted integrations and full-mode subsets fail.
for change in '.schema=2' '.mode="other"' '.tested_sha="invalid"' \
  '.tested_sha="2222222222222222222222222222222222222222"' '.base_sha=""' \
  '.modules=["driver/missing"]' '.modules=["driver/a","driver/a"]' \
  '.modules=["driver/a","cmd/arkoren"]' '.integration=[]' \
  '.integration=["driver/missing"]' '.website=false' '.website="true"' \
  '.mode="full" | .base_sha=""' 'del(.modules)' '.extra=true' '.reason=""'; do
  PLAN=$(jq "$change" <<< "$driver_plan")
  reject "$success"
done
for bad_plan in '' '{}' null 'invalid json'; do
  PLAN="$bad_plan" reject "$empty_results"
done
printf 'CI gate fixtures passed\n'
