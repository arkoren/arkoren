#!/usr/bin/env bash
# Protect fresh-import retries, no-op convergence and App credential confinement.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-notes.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
mkdir -p "$scratch/bin" "$scratch/runner"
cat > "$scratch/bin/git" <<'TOOL'
#!/usr/bin/env bash
set -euo pipefail
[[ -z ${NOTES_APP_TOKEN:-} && -z ${app_token:-} && ${GH_TOKEN:-} == read-token ]]
root=${!#}
mkdir -p "$root/website"
printf '%s\n' "$root" >> "$TRACE"
TOOL
cat > "$scratch/bin/npm" <<'TOOL'
#!/usr/bin/env bash
set -euo pipefail
[[ -z ${NOTES_APP_TOKEN:-} && -z ${app_token:-} && ${GH_TOKEN:-} == read-token ]]
[[ ${FAIL_IMPORT:-no} != yes ]]
TOOL
cat > "$scratch/bin/sleep" <<'TOOL'
#!/usr/bin/env bash
exit 0
TOOL
cat > "$scratch/runner/arkoren-ci" <<'TOOL'
#!/usr/bin/env bash
set -euo pipefail
case "$3" in
  prepare)
    [[ -z ${NOTES_APP_TOKEN:-} && -z ${app_token:-} && $GH_TOKEN == read-token ]]
    ;;
  inspect)
    [[ -z ${NOTES_APP_TOKEN:-} && -z ${app_token:-} && $GH_TOKEN == read-token ]]
    printf '{"needed":%s}\n' "${NEEDED:-true}"
    ;;
  publish)
    [[ -z ${NOTES_APP_TOKEN:-} && -z ${app_token:-} && $GH_TOKEN == write-token ]]
    printf 'publish\n' >> "$WRITES"
    if [[ ${FAIL_ALWAYS:-no} == yes ]] || [[ ${FAIL_ONCE:-no} == yes && $(wc -l < "$WRITES") -eq 1 ]]; then
      exit 1
    fi
    ;;
  *) exit 1 ;;
esac
TOOL
chmod +x "$scratch/bin/"* "$scratch/runner/arkoren-ci"
export PATH="$scratch/bin:$PATH" RUNNER_TEMP="$scratch/runner"
export TRACE="$scratch/trace" WRITES="$scratch/writes" GITHUB_STEP_SUMMARY="$scratch/summary"
export GH_TOKEN=read-token NOTES_APP_TOKEN=write-token
: > "$WRITES"
FAIL_ONCE=yes bash "$script_dir/release-notes.sh" > "$scratch/output" 2>&1
[[ $(wc -l < "$TRACE") -eq 2 && $(sort -u "$TRACE" | wc -l) -eq 2 ]]
[[ $(wc -l < "$WRITES") -eq 2 ]]

: > "$TRACE"
: > "$WRITES"
NEEDED=false bash "$script_dir/release-notes.sh" > "$scratch/output" 2>&1
[[ $(wc -l < "$TRACE") -eq 1 && ! -s "$WRITES" ]]

: > "$TRACE"
if FAIL_IMPORT=yes bash "$script_dir/release-notes.sh" > "$scratch/output" 2>&1; then
  printf 'Failed import was accepted\n' >&2
  exit 1
fi
[[ $(wc -l < "$TRACE") -eq 3 && ! -s "$WRITES" ]]

: > "$TRACE"
if FAIL_ALWAYS=yes bash "$script_dir/release-notes.sh" > "$scratch/output" 2>&1; then
  printf 'Failed publication was accepted\n' >&2
  exit 1
fi
[[ $(wc -l < "$TRACE") -eq 3 && $(wc -l < "$WRITES") -eq 3 ]]
printf 'Release-note reconciliation fixtures passed\n'
