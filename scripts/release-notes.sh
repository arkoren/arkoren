#!/usr/bin/env bash
# Reconcile from fresh reviewed main on every attempt. Never reset the caller's
# checkout, reuse stale import bytes, or give npm/Git the App write credential.
set -euo pipefail

app_token=${NOTES_APP_TOKEN:?missing release App token}
unset NOTES_APP_TOKEN
maintenance="$RUNNER_TEMP/arkoren-ci"

reconcile() {
  local root=$1
  git clone --quiet --depth=1 --branch main https://github.com/arkoren/arkoren "$root"
  (
    cd "$root/website"
    npm ci
    npm run releases:sync
  )
  "$maintenance" release notes prepare --repo "$root" --output "$root-state.json"
  local inspection
  inspection=$("$maintenance" release notes inspect --repo "$root" --manifest "$root-state.json")
  if ! jq -e '.needed == true' <<< "$inspection" >/dev/null; then
    jq -e '.needed == false' <<< "$inspection" >/dev/null
    printf 'Release notes already converged; no App write needed.\n'
    return 0
  fi
  GH_TOKEN="$app_token" "$maintenance" release notes publish --repo "$root" --manifest "$root-state.json"
}

for attempt in 1 2 3; do
  # Each attempt owns a new directory. A failed attempt is retained for runner
  # diagnostics and cannot contaminate the next import or branch inspection.
  directory=$(mktemp -d "$RUNNER_TEMP/release-notes.XXXXXXXX")
  printf 'Release-note reconciliation attempt %s/3\n' "$attempt"
  # A separate shell keeps errexit active inside reconcile, including npm and
  # validation failures. Bash disables it for functions used directly in `if`.
  export -f reconcile
  export maintenance
  if NOTES_APP_TOKEN="$app_token" bash -euo pipefail -c 'app_token=$NOTES_APP_TOKEN; unset NOTES_APP_TOKEN; reconcile "$1/checkout"' _ "$directory"; then
    printf '## Release-note reconciliation\n\nConverged or native protected auto-merge enabled. Required CI checks still govern merging.\n' >> "$GITHUB_STEP_SUMMARY"
    exit 0
  fi
  if [ "$attempt" -lt 3 ]; then
    sleep 10
  fi
done

printf 'Reconciliation failed after three fresh-main attempts; scheduled recovery will retry.\n' >&2
exit 1
