#!/usr/bin/env bash
# Temporary partial-import inventory. Run from the checkout root; unlike
# ci-modules.sh, absent module areas are valid. Remove with the import workflow
# when the complete source inventory and permanent CI are published.
set -euo pipefail
shopt -s nullglob dotglob
export LC_ALL=C

# Fail before publishing either matrix, so CI cannot test a partial inventory.
fail() {
  printf 'Imported modules: %s\n' "$*" >&2
  exit 1
}

# Reject redirected directory roots before any bounded glob enters them.
require_directory() {
  [[ -d "$1" && ! -L "$1" ]] || fail "expected real directory: $1"
}

# Validate each selected manifest and optional service marker. Canonical names
# are safe to pass as matrix values and Make MODULE arguments without evaluation.
validate_module() {
  local canonical='^(core|cmd/arkoren|(driver|foundation|ark)/[a-z0-9][a-z0-9-]*)$'
  [[ "$1" =~ $canonical ]] || fail "invalid module: $1"
  require_directory "$1"
  [[ -f "$1/go.mod" && ! -L "$1/go.mod" ]] || fail "expected real manifest: $1/go.mod"
  if [[ -e "$1/compose.yaml" || -L "$1/compose.yaml" ]]; then
    [[ -f "$1/compose.yaml" && ! -L "$1/compose.yaml" ]] || fail "invalid Compose file: $1/compose.yaml"
  fi
}

[[ $# -eq 0 ]] || fail 'run without arguments from the checkout root'
command -v jq >/dev/null 2>&1 || fail 'jq is required'

candidates=()
if [[ -e core || -L core ]]; then
  candidates+=(core)
  require_directory core
  nested_core=$(find core -mindepth 2 -name go.mod -print) || fail 'cannot inspect core manifests'
  [[ -z "$nested_core" ]] || fail 'nested core module'
fi
# The command's documentation is published before its executable module.
if [[ -e cmd || -L cmd ]]; then
  require_directory cmd
  if [[ -e cmd/arkoren || -L cmd/arkoren ]]; then
    require_directory cmd/arkoren
    if [[ -e cmd/arkoren/go.mod || -L cmd/arkoren/go.mod ]]; then
      candidates+=(cmd/arkoren)
    fi
  fi
fi
for family in foundation driver ark; do
  if [[ ! -e "$family" && ! -L "$family" ]]; then continue; fi
  require_directory "$family"
  for root in "$family"/*; do
    if [[ -d "$root" || -L "$root" ]]; then candidates+=("$root"); fi
  done
done

integration=()
# Bash 3 treats an initialized empty array as unset under nounset.
for module in ${candidates[@]+"${candidates[@]}"}; do
  validate_module "$module"
  if [[ -f "$module/compose.yaml" ]]; then integration+=("$module"); fi
done
# jq -n handles zero arguments without turning an empty list into [""] or null.
modules_json=$(jq -cn --args '$ARGS.positional | unique' ${candidates[@]+"${candidates[@]}"})
integration_json=$(jq -cn --args '$ARGS.positional | unique' ${integration[@]+"${integration[@]}"})
printf 'modules=%s\nintegration=%s\n' "$modules_json" "$integration_json"
