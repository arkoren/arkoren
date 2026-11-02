#!/usr/bin/env bash
# Select PR work from the tested merge tree. Inventory validation always runs.
set -euo pipefail
export LC_ALL=C

if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  cat <<'HELP'
Usage: bash scripts/ci-select.sh

Run from the checkout root. Requires Bash, jq, and Git. Prints one JSON plan.
CI_EVENT_NAME=pull_request enables selection only when CI_TESTED_SHA is HEAD
and its two parents match CI_PR_BASE_SHA and CI_PR_HEAD_SHA (full commit IDs).
Other events or unavailable comparison history select the full suite.
Invalid inventory fails without publishing a plan. Selection never loads Go.
HELP
  exit 0
fi
[[ $# -eq 0 ]] || { printf 'CI selection: unexpected arguments\n' >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
inventory=$(bash "$script_dir/ci-modules.sh")
modules=$(sed -n 's/^modules=//p' <<< "$inventory")
integration=$(sed -n 's/^integration=//p' <<< "$inventory")
tested=${CI_TESTED_SHA:-}
base=${CI_PR_BASE_SHA:-}
head=${CI_PR_HEAD_SHA:-}

# Every uncertain comparison runs all work; a partial diff is never a plan.
full() {
  jq -cn --argjson modules "$modules" --argjson integration "$integration" \
    --arg tested "$tested" --arg reason "$1" \
    '{schema:1, mode:"full", tested_sha:$tested, base_sha:"", modules:$modules,
      integration:$integration, website:true, reason:$reason}'
  exit 0
}

[[ ${CI_EVENT_NAME:-} == pull_request ]] || full 'Non-PR event: full validation'
sha='^[0-9a-f]{40}$'
[[ "$tested" =~ $sha && "$base" =~ $sha && "$head" =~ $sha ]] \
  || full 'Missing or invalid immutable PR revisions'
actual=$(git rev-parse --verify HEAD 2>/dev/null) || full 'Cannot resolve tested checkout'
[[ "$actual" == "$tested" ]] || full 'Checkout does not match tested revision'
parents=$(git show -s --format=%P "$tested" 2>/dev/null) || full 'Cannot resolve merge parents'
[[ "$parents" == "$base $head" ]] || full 'Checkout is not the expected PR merge tree'

scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-ci-select.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git diff --no-ext-diff --no-renames --name-only -z "$base" "$tested" -- > "$scratch/paths" \
  || full 'Cannot compare PR trees: full validation'

selected=()
website=false
while IFS= read -r -d '' path; do
  case "$path" in
    # Module/payload ownership can change when manifests or suite markers change.
    */go.mod|*/compose.yaml) full 'Module manifest or integration inventory changed' ;;
    core/*|foundation/*) full 'Core or foundation changed' ;;
    website/*) website=true; continue ;;
  esac

  owner=''
  while IFS= read -r module; do
    if [[ "$path" == "$module/"* ]]; then
      owner="$module"
      break
    fi
  done < <(jq -r '.[]' <<< "$modules")
  case "$owner" in
    driver/*|cmd/arkoren) selected+=("$owner"); website=true ;;
    cmd/arkoren-ci) selected+=("$owner") ;;
    # Arks and all unclassified paths stay conservative until explicitly supported.
    *) full 'Shared or unclassified path changed' ;;
  esac
done < "$scratch/paths"

selected_json='[]'
if [[ ${#selected[@]} -gt 0 ]]; then
  selected_json=$(printf '%s\n' "${selected[@]}" | jq -Rsc 'split("\n")[:-1] | unique')
fi
jq -cn --argjson modules "$selected_json" --argjson all_integration "$integration" \
  --argjson website "$website" --arg tested "$tested" --arg base "$base" \
  '{schema:1, mode:"selective", tested_sha:$tested, base_sha:$base, modules:$modules,
    integration:($all_integration | map(select(. as $m | $modules | index($m)))),
    website:$website, reason:"PR merge-tree path ownership"}'
