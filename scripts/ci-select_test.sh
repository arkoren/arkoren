#!/usr/bin/env bash
# Real Git merge-tree fixtures protect selection from silently omitting PR work.
set -euo pipefail

if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  printf '%s\n' 'Usage: bash scripts/ci-select_test.sh' \
    'Requires Bash, jq, and Git. Uses disposable repositories; no Go or services.'
  exit 0
fi
[[ $# -eq 0 ]] || exit 2
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
selector="$script_dir/ci-select.sh"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-ci-select-test.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=Fixture GIT_AUTHOR_EMAIL=fixture@example.invalid
export GIT_COMMITTER_NAME=Fixture GIT_COMMITTER_EMAIL=fixture@example.invalid

fail() {
  printf 'FAIL: %s: %s\n' "$case_name" "$*" >&2
  cat "$scratch/output" "$scratch/error" >&2
  exit 1
}

fixture() {
  case_name="$1"
  printf '  %s\n' "$case_name"
  cd "$scratch"
  rm -rf tree
  mkdir tree
  cd tree
  git init -q -b base
  mkdir .github
  printf '%s\n' '{"schema":1,"public":["core","cmd/arkoren","driver/a","driver/z","foundation/f"],"development":["cmd/arkoren-ci"]}' > .github/release-modules.json
  for module in core cmd/arkoren cmd/arkoren-ci driver/a driver/z foundation/f; do
    mkdir -p "$module"
    printf 'module fixture\n' > "$module/go.mod"
    printf 'fixture\n' > "$module/payload"
  done
  touch driver/a/compose.yaml
  mkdir website
  touch website/page.md
  git add .
  git commit -qm base
  export CI_PR_BASE_SHA
  CI_PR_BASE_SHA=$(git rev-parse HEAD)
  export CI_EVENT_NAME=pull_request
  : > "$scratch/output"
  : > "$scratch/error"
}

merge_pr() {
  git add -A
  git commit --allow-empty -qm head
  export CI_PR_HEAD_SHA CI_TESTED_SHA
  CI_PR_HEAD_SHA=$(git rev-parse HEAD)
  git checkout -qb tested "$CI_PR_BASE_SHA"
  git merge --no-ff --no-edit -qm merge "$CI_PR_HEAD_SHA"
  CI_TESTED_SHA=$(git rev-parse HEAD)
}

select_work() {
  bash "$selector" > "$scratch/output" 2> "$scratch/error" || fail 'selector failed'
  jq -e 'type == "object" and .schema == 1' "$scratch/output" >/dev/null || fail 'invalid plan'
}

expect() {
  jq -e "$1" "$scratch/output" >/dev/null || fail "unexpected plan: $1"
}

expect_full() {
  select_work
  local inventory modules integration
  inventory=$(bash "$script_dir/ci-modules.sh")
  modules=$(sed -n 's/^modules=//p' <<< "$inventory")
  integration=$(sed -n 's/^integration=//p' <<< "$inventory")
  jq -e --argjson modules "$modules" --argjson integration "$integration" \
    '.mode == "full" and .modules == $modules and .integration == $integration and .website' \
    "$scratch/output" >/dev/null || fail 'full selection omitted inventory work'
}

printf 'CI selection fixtures\n'
fixture 'driver payload selects its check, integration, and website'
printf 'changed\n' >> driver/a/payload
merge_pr
select_work
expect '.mode == "selective" and .modules == ["driver/a"] and .integration == ["driver/a"] and .website'

fixture 'non-Compose driver has no integration work'
touch driver/z/new_test.go
merge_pr
select_work
expect '.modules == ["driver/z"] and .integration == [] and .website'

fixture 'embedded guides select CLI and website'
mkdir -p cmd/arkoren/skills/arkoren/references/guides
touch cmd/arkoren/skills/arkoren/references/guides/example.md
merge_pr
select_work
expect '.modules == ["cmd/arkoren"] and .integration == [] and .website'

fixture 'template changes select CLI and website'
mkdir cmd/arkoren/templates
touch cmd/arkoren/templates/main.go.tmpl
merge_pr
select_work
expect '.modules == ["cmd/arkoren"] and .website'

fixture 'maintenance CLI does not install website dependencies'
touch cmd/arkoren-ci/new_test.go
merge_pr
select_work
expect '.modules == ["cmd/arkoren-ci"] and .integration == [] and (.website | not)'

fixture 'website-only PR has empty Go selections'
touch website/new.md
merge_pr
select_work
expect '.mode == "selective" and .modules == [] and .integration == [] and .website'

fixture 'empty tree diff has no expensive work'
merge_pr
select_work
expect '.mode == "selective" and .modules == [] and .integration == [] and (.website | not)'

fixture 'mixed changes union and deduplicate module work'
touch driver/a/one driver/a/two cmd/arkoren-ci/new website/new.md
merge_pr
select_work
expect '.modules == ["cmd/arkoren-ci","driver/a"] and .integration == ["driver/a"] and .website'

fixture 'renames select both old and new owners'
git mv driver/a/payload driver/z/moved
merge_pr
select_work
expect '.modules == ["driver/a","driver/z"] and .integration == ["driver/a"]'

fixture 'deleted fixtures still select their owner'
rm driver/a/payload
merge_pr
select_work
expect '.modules == ["driver/a"] and .integration == ["driver/a"]'

fixture 'spaces and newlines in paths cannot split ownership'
touch "driver/a/space file" $'driver/z/line\nfile'
merge_pr
select_work
expect '.modules == ["driver/a","driver/z"]'

for path in core/payload foundation/f/payload Makefile .golangci.yml \
  scripts/new.sh go.work .github/workflows/ci.yml README.md driver/a/go.mod \
  driver/z/compose.yaml; do
  fixture "conservative full selection: $path"
  mkdir -p "$(dirname "$path")"
  printf 'changed\n' >> "$path"
  merge_pr
  expect_full
done

fixture 'new canonical module joins full validation automatically'
mkdir driver/new
touch driver/new/go.mod
merge_pr
select_work
expect '.mode == "full" and (.modules | index("driver/new")) != null'

fixture 'removed module with updated policy selects remaining full inventory'
rm -r driver/z
jq '.public -= ["driver/z"]' .github/release-modules.json > policy
mv policy .github/release-modules.json
merge_pr
select_work
expect '.mode == "full" and (.modules | length) == 5 and (.modules | index("driver/z")) == null'

fixture 'removed Compose marker selects full remaining suites'
touch driver/z/compose.yaml
git add .
git commit -qm 'second integration baseline'
CI_PR_BASE_SHA=$(git rev-parse HEAD)
rm driver/a/compose.yaml
merge_pr
select_work
expect '.mode == "full" and .integration == ["driver/z"]'

fixture 'push and manual dispatch always validate everything'
touch website/new.md
merge_pr
for event in push workflow_dispatch merge_group; do
  CI_EVENT_NAME="$event" expect_full
done

fixture 'missing or mismatched immutable revisions select full work'
merge_pr
CI_PR_HEAD_SHA='' expect_full
CI_PR_HEAD_SHA=invalid expect_full
CI_PR_HEAD_SHA="$CI_PR_BASE_SHA" expect_full
CI_PR_BASE_SHA=1111111111111111111111111111111111111111 expect_full
CI_TESTED_SHA="$CI_PR_BASE_SHA" expect_full

fixture 'shallow checkout without merge parents falls back to full work'
merge_pr
git clone -q --depth=1 --branch tested "file://$PWD" "$scratch/shallow"
cd "$scratch/shallow"
expect_full

fixture 'workflow checkout depth two retains enough comparison history'
touch driver/a/new
merge_pr
git clone -q --depth=2 --branch tested "file://$PWD" "$scratch/depth-two"
cd "$scratch/depth-two"
select_work
expect '.mode == "selective" and .modules == ["driver/a"]'

fixture 'base-only changes do not become part of the PR selection'
git checkout -qb pr
touch driver/a/new
git add .
git commit -qm head
CI_PR_HEAD_SHA=$(git rev-parse HEAD)
git checkout -q base
touch driver/z/upstream
git add .
git commit -qm upstream
CI_PR_BASE_SHA=$(git rev-parse HEAD)
git checkout -qb tested
git merge --no-ff --no-edit -qm merge "$CI_PR_HEAD_SHA"
CI_TESTED_SHA=$(git rev-parse HEAD)
select_work
expect '.mode == "selective" and .modules == ["driver/a"]'

fixture 'invalid inventory fails without publishing a successful empty plan'
rm core/go.mod
merge_pr
if bash "$selector" > "$scratch/output" 2> "$scratch/error"; then
  fail 'invalid inventory was accepted'
fi
[[ ! -s "$scratch/output" ]] || fail 'failure published a plan'

printf 'CI selection fixtures passed\n'
