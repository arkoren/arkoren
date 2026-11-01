#!/usr/bin/env bash
# Discover canonical modules in the current directory, not the script directory.
# CI consumes stdout; send diagnostics to stderr and publish nothing on failure.
set -euo pipefail
shopt -s nullglob dotglob
export LC_ALL=C

# Print usage without inspecting the checkout or requiring jq.
usage() {
  cat <<'HELP'
Usage: bash scripts/ci-modules.sh [--help]

Run from the checkout root. Requires Bash, jq, and standard Unix utilities;
neither Go nor Git is required.

Prints two sorted, compact JSON arrays for GitHub Actions:
  modules=[...]       All canonical module roots
  integration=[...]   Module roots with a compose.yaml file

All paths are validated before either output line is printed.
HELP
}

# fail reports an invalid checkout on stderr; stdout remains output-only.
fail() {
  printf 'CI inventory: %s\n' "$*" >&2
  exit 1
}

# require_directory rejects links, including parent roots that could redirect
# the bounded inventory outside this checkout. Empty families fail separately.
require_directory() {
  [[ -d "$1" && ! -L "$1" ]] || fail "expected real directory: $1"
}

# validate_module applies Make's canonical naming rule and rejects linked
# manifests. A Compose marker must also be a real file, not a redirected path.
validate_module() {
  local module="$1"
  local canonical='^(core|cmd/arkoren|cmd/arkoren-ci|(driver|foundation|ark)/[a-z0-9][a-z0-9-]*)$'

  [[ "$module" =~ $canonical ]] || fail "invalid module: $module"
  require_directory "$module"
  [[ -f "$module/go.mod" && ! -L "$module/go.mod" ]] \
    || fail "expected real manifest: $module/go.mod"

  if [[ -e "$module/compose.yaml" || -L "$module/compose.yaml" ]]; then
    [[ -f "$module/compose.yaml" && ! -L "$module/compose.yaml" ]] \
      || fail "invalid Compose file: $module/compose.yaml"
  fi
}

# Handle help before checking tools or inspecting the checkout.
if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  usage
  exit 0
fi
[[ $# -eq 0 ]] || fail 'unexpected arguments; use --help for usage'
command -v jq >/dev/null 2>&1 || fail 'jq is required; install jq and run again'
[[ -f .github/release-modules.json && ! -L .github/release-modules.json && ! -L .github ]] \
  || fail 'expected real module policy'
jq -e 'keys == ["development","public","schema"] and .schema == 1 and
  (.public | type == "array" and length > 0) and
  (.development | type == "array") and
  all(.public[]; type == "string" and test("^(core|cmd/arkoren|(driver|foundation|ark)/[a-z0-9][a-z0-9-]*)$")) and
  all(.development[]; . == "cmd/arkoren-ci") and
  ((.public + .development) | length == (unique | length))' \
  .github/release-modules.json >/dev/null || fail 'invalid module policy'
development=$(jq -r '.development[]' .github/release-modules.json)

# Core must remain a single module; discovery elsewhere stops at direct children.
require_directory cmd
require_directory core
nested_core=$(find core -mindepth 2 -name go.mod -print) || fail 'cannot inspect core manifests'
[[ -z "$nested_core" ]] || fail 'nested core module'

# Never traverse examples or skill trees.
# Driver and foundation must be populated; ark is optional and may be empty.
candidates=(core cmd/arkoren)
while IFS= read -r module; do
  [[ -z "$module" ]] || candidates+=("$module")
done <<< "$development"
for root in cmd/*; do
  if [[ -d "$root" || -L "$root" ]]; then
    [[ "$root" == cmd/arkoren || "$root" == "$development" ]] \
      || fail "unreviewed command module: $root"
  fi
done
for family in driver foundation ark; do
  if [[ "$family" == ark && ! -e ark && ! -L ark ]]; then
    continue
  fi
  require_directory "$family"
  roots=()
  for root in "$family"/*; do
    if [[ -d "$root" || -L "$root" ]]; then
      roots+=("$root")
    fi
  done
  if [[ ${#roots[@]} -eq 0 ]]; then
    [[ "$family" == ark ]] || fail "empty family: $family"
    continue
  fi
  candidates+=("${roots[@]}")
done

# Validate every candidate before selecting the integration subset.
integration=()
for module in "${candidates[@]}"; do
  validate_module "$module"
  if [[ -f "$module/compose.yaml" ]]; then
    integration+=("$module")
  fi
done
[[ ${#integration[@]} -gt 0 ]] || fail 'empty integration inventory'

# Encode both arrays before publishing either one. jq's unique also sorts.
modules_json=$(printf '%s\n' "${candidates[@]}" | jq -Rsc 'split("\n")[:-1] | unique')
integration_json=$(printf '%s\n' "${integration[@]}" | jq -Rsc 'split("\n")[:-1] | unique')
printf 'modules=%s\nintegration=%s\n' "$modules_json" "$integration_json"
