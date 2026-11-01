#!/usr/bin/env bash
# Check failure accumulation with isolated fake tools, never provider services.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-audit.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$scratch/bin" "$scratch/core" "$scratch/cmd/arkoren" "$scratch/driver/sample"
cat > "$scratch/bin/make" <<'TOOL'
#!/usr/bin/env bash
printf 'make %s\n' "$*" >> "$TRACE"
[[ ${FAIL_AUDIT:-} != yes || "$*" != 'vet MODULE=core' ]]
TOOL
cat > "$scratch/bin/govulncheck" <<'TOOL'
#!/usr/bin/env bash
printf 'scan %s\n' "${PWD##*/}" >> "$TRACE"
[[ ${FAIL_AUDIT:-} != yes || ${PWD##*/} != sample ]]
TOOL
cat > "$scratch/bin/go" <<'TOOL'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >> "$TRACE"
[[ ${FAIL_AUDIT:-} != yes ]]
TOOL
chmod +x "$scratch/bin/"*
export PATH="$scratch/bin:$PATH" TRACE="$scratch/trace"
export GITHUB_STEP_SUMMARY="$scratch/summary" MODULES='["core","driver/sample"]'
cd "$scratch"
bash "$script_dir/audit.sh" > "$scratch/output" 2>&1
grep -Fq 'All vet, vulnerability, and generated-project checks passed.' "$GITHUB_STEP_SUMMARY"
: > "$TRACE"
if FAIL_AUDIT=yes bash "$script_dir/audit.sh" > "$scratch/output" 2>&1; then
  printf 'Audit accepted failed commands\n' >&2
  exit 1
fi
for failure in core:vet driver/sample:scan cmd/arkoren:generated-project; do
  grep -Fq -- "$failure" "$GITHUB_STEP_SUMMARY"
done
# A failed vet must not suppress its scan or the following module's commands.
grep -Fxq 'scan core' "$TRACE"
grep -Fxq 'make vet MODULE=driver/sample' "$TRACE"
grep -Fxq 'scan sample' "$TRACE"
if MODULES='[]' bash "$script_dir/audit.sh" > "$scratch/output" 2>&1; then
  printf 'Audit accepted an empty inventory\n' >&2
  exit 1
fi
printf 'Audit fixtures passed\n'
