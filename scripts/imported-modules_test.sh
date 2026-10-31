#!/usr/bin/env bash
# Exercise dependency-ordered imports and unsafe trees without Go or services.
# Each fixture lives under this suite's disposable root; user source is untouched.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-imports.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Compare complete outputs, including the empty integration matrix. No output
# may be silently lost when a new import does not provide service-backed tests.
expect_inventory() {
  bash "$script_dir/imported-modules.sh" > "$scratch/output"
  printf 'modules=%s\nintegration=%s\n' "$1" "$2" > "$scratch/expected"
  diff -u "$scratch/expected" "$scratch/output"
}

# Rejected checkouts must publish no matrix, even after a valid earlier module.
expect_rejected() {
  if bash "$script_dir/imported-modules.sh" > "$scratch/output" 2> "$scratch/error"; then
    printf 'Expected rejection: %s\n' "$1" >&2
    exit 1
  fi
  test ! -s "$scratch/output"
  grep -Fq "$1" "$scratch/error"
}

mkdir "$scratch/tree"
cd "$scratch/tree"
mkdir -p cmd/arkoren/skills/example example
touch cmd/arkoren/skills/example/go.mod example/go.mod
expect_inventory '[]' '[]'
mkdir -p foundation/a
touch foundation/a/go.mod
expect_inventory '["foundation/a"]' '[]'
mkdir core
touch core/go.mod
expect_inventory '["core","foundation/a"]' '[]'
mkdir -p driver/plain driver/service
touch driver/plain/go.mod driver/service/go.mod driver/service/compose.yaml
expect_inventory '["core","driver/plain","driver/service","foundation/a"]' '["driver/service"]'
touch cmd/arkoren/go.mod
mkdir -p ark/domain driver/plain/testdata/ignored
touch ark/domain/go.mod driver/plain/testdata/ignored/go.mod
expect_inventory '["ark/domain","cmd/arkoren","core","driver/plain","driver/service","foundation/a"]' '["driver/service"]'

mkdir foundation/missing
expect_rejected 'expected real manifest: foundation/missing/go.mod'
rmdir foundation/missing
mkdir -p core/nested
touch core/nested/go.mod
expect_rejected 'nested core module'
rm core/nested/go.mod
rmdir core/nested
mkdir driver/Bad
touch driver/Bad/go.mod
expect_rejected 'invalid module: driver/Bad'
rm driver/Bad/go.mod
rmdir driver/Bad
mv foundation "$scratch/outside"
ln -s "$scratch/outside" foundation
expect_rejected 'expected real directory: foundation'
rm foundation
mv "$scratch/outside" foundation
ln -s a foundation/linked
expect_rejected 'expected real directory: foundation/linked'
rm foundation/linked
mv foundation/a/go.mod "$scratch/manifest"
ln -s "$scratch/manifest" foundation/a/go.mod
expect_rejected 'expected real manifest: foundation/a/go.mod'
rm foundation/a/go.mod
mv "$scratch/manifest" foundation/a/go.mod
rm driver/service/compose.yaml
ln -s "$scratch/manifest" driver/service/compose.yaml
expect_rejected 'invalid Compose file: driver/service/compose.yaml'
rm driver/service/compose.yaml
mv cmd "$scratch/command"
ln -s "$scratch/command" cmd
expect_rejected 'expected real directory: cmd'
printf 'Imported module fixtures passed\n'
