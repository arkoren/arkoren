#!/usr/bin/env bash
# Audit every discovered module, accumulating failures without suppressing scans.
# Run from the checkout root with MODULES set to discovery's JSON array.
set -euo pipefail

modules=$(jq -er 'if type == "array" and length > 0 and all(.[]; type == "string") then .[] else error("expected nonempty module array") end' <<< "${MODULES:?MODULES is required}")
failed=()
while IFS= read -r module; do
  echo "::group::$module"
  # Validate canonical roots before either command enters a module directory.
  if make module-root MODULE="$module"; then
    make vet MODULE="$module" || failed+=("$module:vet")
    (cd "$module" && govulncheck ./...) || failed+=("$module:scan")
  else
    failed+=("$module:inventory")
  fi
  echo '::endgroup::'
done <<< "$modules"

# This deliberately exercises the generator's released public dependency pins.
(cd cmd/arkoren && CGO_ENABLED=1 go test -count=1 -run '^TestNewGeneratedOrdinaryProject$' .) || failed+=("cmd/arkoren:generated-project")
{
  printf '\nAudited %s modules.\n\n' "$(jq length <<< "$MODULES")"
  if [[ ${#failed[@]} -eq 0 ]]; then
    printf 'All vet, vulnerability, and generated-project checks passed.\n'
  else
    printf '### Failures\n\n'
    printf -- '- %s\n' "${failed[@]}"
  fi
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
if [[ ${#failed[@]} -gt 0 ]]; then
  printf '::error::audit failures: %s\n' "${failed[*]}" >&2
  exit 1
fi
