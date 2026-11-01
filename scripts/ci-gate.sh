#!/usr/bin/env bash
# Require the complete routine Go/tooling job set; Website is required separately.
# Read GitHub Actions' toJSON(needs) object from stdin. Missing jobs fail closed.
set -euo pipefail

jq -es '
  length == 1 and (.[0] |
    keys == ["check", "discovery", "integration", "tooling"] and
    all(.[]; .result == "success"))
'
