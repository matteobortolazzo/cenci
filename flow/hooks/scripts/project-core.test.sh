#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d)"
trap 'rm -rf "${ROOT}"' EXIT
failures=0

assert_contains() { [[ "$1" == *"$2"* ]] || { echo "FAIL: expected $2" >&2; failures=$((failures+1)); }; }

mkdir -p "${ROOT}/neutral/.cenci"
touch "${ROOT}/neutral/.cenci/config.json"
out="$(cd "${ROOT}/neutral" && sh "${SCRIPT_DIR}/preserve-context.sh")"
assert_contains "${out}" ".cenci/config.json"

mkdir -p "${ROOT}/legacy/.claude"
touch "${ROOT}/legacy/.claude/config.json"
out="$(cd "${ROOT}/legacy" && sh "${SCRIPT_DIR}/preserve-context.sh")"
assert_contains "${out}" "legacy"

mkdir -p "${ROOT}/missing"
out="$(cd "${ROOT}/missing" && sh "${SCRIPT_DIR}/check-lessons-file.sh")"
assert_contains "${out}" ".cenci/config.json not found"

echo "project-core.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
