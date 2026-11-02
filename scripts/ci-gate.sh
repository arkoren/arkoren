#!/usr/bin/env bash
# Require unconditional tooling and exactly the selected Go work. Website is separate.
# Read toJSON(needs) from stdin and the discovery plan from PLAN. Fail closed.
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
inventory=$(bash "$script_dir/ci-modules.sh")
modules=$(sed -n 's/^modules=//p' <<< "$inventory")
integration=$(sed -n 's/^integration=//p' <<< "$inventory")

jq -es --argjson plan "${PLAN:?PLAN is required}" --arg tested "${CI_TESTED_SHA:?CI_TESTED_SHA is required}" \
  --argjson modules "$modules" --argjson integration "$integration" '
  def subset($all):
    type == "array" and . == (unique | sort) and all(.[]; . as $m | $all | index($m));
  ($plan | keys == ["base_sha","integration","mode","modules","reason","schema","tested_sha","website"] and
    .schema == 1 and (.mode == "full" or .mode == "selective") and
    (.website | type == "boolean") and (.reason | type == "string" and length > 0) and
    (.tested_sha | test("^[0-9a-f]{40}$")) and .tested_sha == $tested and
    (.modules | subset($modules)) and (.integration | subset($integration)) and
    .integration == ($integration | map(select(. as $m | $plan.modules | index($m)))) and
    (if .mode == "full" then
      .base_sha == "" and .modules == $modules and .integration == $integration and .website
    else
      (.base_sha | test("^[0-9a-f]{40}$")) and
      ((.modules | length) == 0 or .website or .modules == ["cmd/arkoren-ci"])
    end)) and
  length == 1 and (.[0] |
    keys == ["check", "discovery", "integration", "tooling"] and
    .discovery.result == "success" and .tooling.result == "success" and
    .check.result == (if $plan.modules | length > 0 then "success" else "skipped" end) and
    .integration.result == (if $plan.integration | length > 0 then "success" else "skipped" end))
  '
