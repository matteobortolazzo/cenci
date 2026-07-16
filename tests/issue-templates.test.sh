#!/usr/bin/env bash
# Static assertions over .github/ISSUE_TEMPLATE/*.yml content. No network, no
# GitHub API calls -- pure grep/sed/awk against the checked-in YAML files.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || { echo "failed to resolve repo root" >&2; exit 1; }
BUG_REPORT="${ROOT}/.github/ISSUE_TEMPLATE/bug_report.yml"
FEATURE_REQUEST="${ROOT}/.github/ISSUE_TEMPLATE/feature_request.yml"

FAILURES=0
PASSES=0
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

template_name() {
    # Print the value of the top-level "name:" field. Fails loudly (non-zero
    # exit, no output) if the field is missing or empty, rather than letting
    # callers silently compare against an empty string.
    local n
    n="$(grep -m1 -E '^name:' "$1" | sed -E 's/^name:[[:space:]]*//; s/[[:space:]]+$//')" || return 1
    if [[ -z "${n}" ]]; then
        return 1
    fi
    printf '%s\n' "${n}"
}

doctor_output_block() {
    # Print the lines of the doctor-output textarea block (bounded by the
    # "id: doctor-output" marker and the next sibling "- type:" entry).
    sed -n '/id: doctor-output/,/^  - type:/p' "$1"
}

echo "issue-templates.test.sh"

echo "case: bug_report.yml name does not collide with GitHub's stock 'Bug report' template"
if ! NAME="$(template_name "${BUG_REPORT}")"; then
    fail "could not read name: field from ${BUG_REPORT}"
else
    if [[ "${NAME}" != "Bug report" ]]; then pass; else fail "bug_report.yml name is still exactly 'Bug report' (collides with GitHub's stock template)"; fi
fi

echo "case: feature_request.yml name does not collide with GitHub's stock 'Feature request' template"
if ! NAME="$(template_name "${FEATURE_REQUEST}")"; then
    fail "could not read name: field from ${FEATURE_REQUEST}"
else
    if [[ "${NAME}" != "Feature request" ]]; then pass; else fail "feature_request.yml name is still exactly 'Feature request' (collides with GitHub's stock template)"; fi
fi

echo "case: bug_report.yml doctor-output description references 'cenci doctor' as the primary command"
BLOCK="$(doctor_output_block "${BUG_REPORT}")"
if grep -Fq -- 'cenci doctor' <<<"${BLOCK}"; then pass; else fail "doctor-output description does not mention 'cenci doctor'"; fi

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
