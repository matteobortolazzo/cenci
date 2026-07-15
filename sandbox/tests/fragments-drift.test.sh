#!/usr/bin/env bash
# Guards the byte-identity invariant documented in sandbox/CLAUDE.md:
# every fragments/*.dockerfile block must appear verbatim inside Dockerfile
# (the monolith), including the Codex runtime required by per-repo images.
#
# Runs on the host — no Docker required, plain grep/diff.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCKERFILE="${SANDBOX_DIR}/Dockerfile"

FAILURES=0
PASSES=0

fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

echo "fragments-drift.test.sh"

shopt -s nullglob
FRAGMENTS=("${SANDBOX_DIR}"/fragments/*.dockerfile)
shopt -u nullglob

if [[ "${#FRAGMENTS[@]}" -eq 0 ]]; then
    fail "no fragments found under ${SANDBOX_DIR}/fragments — expected at least one"
fi

for fragment in "${FRAGMENTS[@]}"; do
    name="$(basename "${fragment}")"
    echo "case: ${name} is byte-identical to its block in Dockerfile"
    if ! content="$(cat "${fragment}")"; then
        fail "${name} could not be read"
        continue
    fi
    if [[ -z "${content}" ]]; then
        fail "${name} is empty"
        continue
    fi
    if grep -zqF "${content}" "${DOCKERFILE}"; then
        pass
    else
        fail "${name} is not verbatim inside ${DOCKERFILE} — hand-duplicate the edit (see sandbox/CLAUDE.md)"
    fi
done

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
