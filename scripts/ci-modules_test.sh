#!/usr/bin/env bash
# Test discovery with disposable checkouts. No Go, Git, or services are used.
set -euo pipefail

# Print usage before checking dependencies or creating temporary files.
usage() {
  cat <<'HELP'
Usage: bash scripts/ci-modules_test.sh [--help]

Run from any directory. Requires Bash, jq, and standard Unix utilities.
Each case gets a fresh checkout fixture; all temporary files are removed on exit.
HELP
}

# Include the active case and discovery's stderr so failures remain actionable.
fail() {
  printf 'FAIL: %s: %s\n' "$case_name" "$*" >&2
  if [[ -s "$scratch/error" ]]; then
    cat "$scratch/error" >&2
  fi
  exit 1
}

if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  usage
  exit 0
fi
[[ $# -eq 0 ]] || { printf 'Unexpected arguments; use --help for usage\n' >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { printf 'Tests require jq; install jq and run again\n' >&2; exit 1; }

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/ci-modules.sh"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-ci-modules.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
case_name='fixture setup'

# Start a named case, replacing only the tree owned by this suite.
# Extra manifests model paths discovery must ignore, including installed skills
# and temporary agent work.
fixture() {
  case_name="$1"
  printf '  %s\n' "$case_name"
  cd "$scratch"
  rm -rf -- tree outside
  mkdir tree
  cd tree
  mkdir .github
  printf '%s\n' '{"schema":1,"public":["core","cmd/arkoren","driver/z","foundation/a"],"development":["cmd/arkoren-ci"]}' > .github/release-modules.json
  local module
  for module in core cmd/arkoren cmd/arkoren-ci driver/z foundation/a example .claude/tmp/x cmd/arkoren/skills/x; do
    mkdir -p "$module"
    printf 'module fixture\n' > "$module/go.mod"
  done
  touch driver/z/compose.yaml driver/.editorconfig foundation/README.md
  : > "$scratch/error"
}

# Capture a successful inventory for exact output comparisons below.
run_inventory() {
  bash "$script" > "$scratch/output" 2> "$scratch/error" \
    || fail 'valid inventory was rejected'
}

# Rejections must explain the invalid path and publish no partial CI matrix.
expect_rejected() {
  local diagnostic="$1"
  if bash "$script" > "$scratch/output" 2> "$scratch/error"; then
    fail 'invalid inventory was accepted'
  fi
  [[ ! -s "$scratch/output" ]] || fail 'failure published a partial matrix'
  grep -Fq -- "$diagnostic" "$scratch/error" \
    || fail "expected diagnostic: $diagnostic"
}

printf 'CI inventory fixtures\n'

fixture 'canonical, sorted inventory'
run_inventory
printf '%s\n' \
  'modules=["cmd/arkoren","cmd/arkoren-ci","core","driver/z","foundation/a"]' \
  'integration=["driver/z"]' > "$scratch/expected"
diff -u "$scratch/expected" "$scratch/output" || fail 'unexpected inventory'

fixture 'empty optional ark'
mkdir ark
run_inventory
diff -u "$scratch/expected" "$scratch/output" || fail 'empty ark changed inventory'

fixture 'new canonical modules and Compose marker are included automatically'
mkdir -p driver/new-driver foundation/new-foundation
touch driver/new-driver/go.mod foundation/new-foundation/go.mod driver/new-driver/compose.yaml
run_inventory
grep -Fxq 'modules=["cmd/arkoren","cmd/arkoren-ci","core","driver/new-driver","driver/z","foundation/a","foundation/new-foundation"]' "$scratch/output" \
  || fail 'new modules were not included'
grep -Fxq 'integration=["driver/new-driver","driver/z"]' "$scratch/output" \
  || fail 'new integration was not included'

fixture 'optional ark and ignored nested driver'
mkdir -p ark/b
touch ark/b/go.mod ark/b/compose.yaml
run_inventory
grep -Fxq 'integration=["ark/b","driver/z"]' "$scratch/output" \
  || fail 'optional ark was not included'

# Unlike core, nested driver manifests are outside the discovery boundary.
mkdir driver/z/nested
touch driver/z/nested/go.mod
cp "$scratch/output" "$scratch/expected"
run_inventory
diff -u "$scratch/expected" "$scratch/output" || fail 'nested driver changed inventory'

# Keep each mutation next to its expected diagnostic. Every case starts clean.
fixture 'missing core manifest'
rm core/go.mod
expect_rejected 'expected real manifest: core/go.mod'

fixture 'missing CLI'
rm -r cmd/arkoren
expect_rejected 'expected real directory: cmd/arkoren'

fixture 'nested core module'
mkdir core/nested
touch core/nested/go.mod
expect_rejected 'nested core module'

fixture 'missing driver family'
rm -r driver
expect_rejected 'expected real directory: driver'

fixture 'empty foundation family'
rm -r foundation/a
expect_rejected 'empty family: foundation'

fixture 'empty integration inventory'
rm driver/z/compose.yaml
expect_rejected 'empty integration inventory'

fixture 'noncanonical module name'
mkdir driver/Bad
touch driver/Bad/go.mod
expect_rejected 'invalid module: driver/Bad'

fixture 'missing driver manifest'
mkdir driver/missing
expect_rejected 'expected real manifest: driver/missing/go.mod'

fixture 'linked module'
ln -s "$scratch/tree/driver/z" driver/link
expect_rejected 'expected real directory: driver/link'

fixture 'linked family'
mv driver "$scratch/outside"
ln -s "$scratch/outside" driver
expect_rejected 'expected real directory: driver'

fixture 'linked manifest'
rm core/go.mod
ln -s ../foundation/a/go.mod core/go.mod
expect_rejected 'expected real manifest: core/go.mod'

fixture 'broken Compose link'
rm driver/z/compose.yaml
ln -s "$scratch/nonexistent" driver/z/compose.yaml
expect_rejected 'invalid Compose file: driver/z/compose.yaml'

fixture 'broken ark link'
ln -s "$scratch/nonexistent" ark
expect_rejected 'expected real directory: ark'

fixture 'development command missing manifest'
rm cmd/arkoren-ci/go.mod
expect_rejected 'expected real manifest: cmd/arkoren-ci/go.mod'

fixture 'unknown command needs explicit policy review'
mkdir cmd/unreviewed
touch cmd/unreviewed/go.mod
expect_rejected 'unreviewed command module'

fixture 'overlapping module classifications'
printf '%s\n' '{"schema":1,"public":["core","cmd/arkoren"],"development":["cmd/arkoren"]}' > .github/release-modules.json
expect_rejected 'invalid module policy'

printf 'CI inventory fixtures passed\n'
