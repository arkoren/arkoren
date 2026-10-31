#!/usr/bin/env bash
# Test Make's validation, teardown, and exit statuses with stub Go/Docker tools.
set -euo pipefail

# Print usage before checking dependencies or creating temporary files.
usage() {
  cat <<'HELP'
Usage: bash scripts/make_test.sh [--help]

Run from any directory. Requires Bash, GNU Make, and standard Unix utilities.
Go and Docker are stubbed: no toolchain, containers, or services are used.
All temporary files are removed on exit.
HELP
}

if [[ $# -eq 1 && ( "$1" == --help || "$1" == -h ) ]]; then
  usage
  exit 0
fi
[[ $# -eq 0 ]] || { printf 'Unexpected arguments; use --help for usage\n' >&2; exit 2; }
command -v make >/dev/null 2>&1 || { printf 'Tests require GNU Make; install make and run again\n' >&2; exit 1; }

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/arkoren-make.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
case_name='fixture setup'

# Print the scenario and captured evidence rather than a bare grep exit status.
fail() {
  printf 'FAIL: %s: %s\n' "$case_name" "$*" >&2
  if [[ -f "$scratch/output" ]]; then
    printf '\nMake output:\n' >&2
    cat "$scratch/output" >&2
  fi
  if [[ -f "$TOOL_LOG" ]]; then
    printf '\nTool calls:\n' >&2
    cat "$TOOL_LOG" >&2
  fi
  exit 1
}

# Only this suite's private PATH sees the substitutes. The real Makefile runs
# unchanged in a minimal checkout; cleanup must not invoke the Go stub at all.
mkdir -p "$scratch/bin" "$scratch/driver/test"
cp "$script_dir/../Makefile" "$scratch/Makefile"
touch "$scratch/driver/test/go.mod" "$scratch/driver/test/compose.yaml"
cd "$scratch"
export TOOL_LOG="$scratch/tools"
export PATH="$scratch/bin:$PATH"

cat > bin/go <<'SH'
#!/bin/sh
printf 'go %s\n' "$*" >> "$TOOL_LOG"
case "$1" in
  list) printf 'fixture\n' ;;
  env) printf 'sh\n' ;;
  test)
    if [ "${TEST_SIGNAL:-}" = TERM ]; then
      kill -TERM "$PPID"
    fi
    exit "${TEST_STATUS:-0}"
    ;;
  *) exit 99 ;;
esac
SH

cat > bin/docker <<'SH'
#!/bin/sh
printf '%s docker %s\n' "$COMPOSE_PROJECT_NAME" "$*" >> "$TOOL_LOG"
case "$2" in
  down) exit "${CLEANUP_STATUS:-0}" ;;
esac
SH
chmod +x bin/go bin/docker

# Capture Make's output and report the command when its exit status is wrong.
expect_success() {
  "$@" > output 2>&1 || fail "expected success: $*"
}

# An expected failure still leaves output available for diagnostic assertions.
expect_failure() {
  if "$@" > output 2>&1; then
    fail "expected failure: $*"
  fi
}

# Match stable diagnostic text, not platform-specific Make prefixes.
expect_output() {
  grep -Fq -- "$1" output || fail "missing output: $1"
}

# Reset captured evidence before each named case; the checkout stays unchanged.
begin_case() {
  case_name="$1"
  : > "$TOOL_LOG"
  : > output
  printf '  %s\n' "$case_name"
}

# Keep inherited developer/CI overrides from changing the fixture's expectations.
unset COMPOSE_PROJECT_NAME TEST_STATUS TEST_SIGNAL CLEANUP_STATUS

printf 'Make teardown fixtures\n'

begin_case 'cleanup needs neither Go nor a valid package'
expect_success make cleanup MODULE=driver/test PKG=invalid
if grep -q '^go ' "$TOOL_LOG"; then
  fail 'cleanup invoked Go'
fi
grep -Fxq 'arkoren-manual-driver-test docker compose down --volumes --remove-orphans' "$TOOL_LOG" \
  || fail 'cleanup did not use the manual project and volume policy'

begin_case 'cleanup honors an explicit project name'
expect_success env COMPOSE_PROJECT_NAME=explicit make cleanup MODULE=driver/test
grep -q '^explicit docker compose down' "$TOOL_LOG" \
  || fail 'cleanup ignored COMPOSE_PROJECT_NAME'

begin_case 'reject a noncanonical module'
expect_failure make cleanup MODULE=example
expect_output 'canonical checkout-relative module root'
[[ ! -s "$TOOL_LOG" ]] || fail 'invalid module invoked tools'

begin_case 'reject a missing module'
expect_failure make cleanup MODULE=driver/missing
expect_output 'MODULE has no go.mod'
[[ ! -s "$TOOL_LOG" ]] || fail 'invalid module invoked tools'

begin_case 'reject a nonrelative package'
expect_failure make test MODULE=driver/test PKG=invalid
expect_output 'PKG must be module-relative'
[[ ! -s "$TOOL_LOG" ]] || fail 'invalid package invoked tools'

begin_case 'test failure takes precedence over cleanup failure'
expect_failure env TEST_STATUS=7 CLEANUP_STATUS=9 make integration MODULE=driver/test
expect_output 'Compose cleanup failed (9)'
expect_output 'Error 7'

begin_case 'termination status takes precedence over cleanup failure'
expect_failure env TEST_SIGNAL=TERM CLEANUP_STATUS=9 make integration MODULE=driver/test
expect_output 'Compose cleanup failed (9)'
expect_output 'Error 143'

begin_case 'cleanup failure fails an otherwise successful run'
expect_failure env CLEANUP_STATUS=9 make integration MODULE=driver/test
expect_output 'Compose cleanup failed (9)'
expect_output 'Error 9'

begin_case 'successful integration tears down its services'
expect_success make integration MODULE=driver/test
grep -q ' docker compose down --volumes --remove-orphans$' "$TOOL_LOG" \
  || fail 'successful integration skipped teardown'

printf 'Make teardown fixtures passed\n'
