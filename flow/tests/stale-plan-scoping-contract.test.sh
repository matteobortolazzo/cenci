#!/usr/bin/env bash
# Contract test for ticket #643 — fix the always-stale plan gate.
#
# Two behaviors are pinned in skills/implement/phases/phase-1-plan.md:
#   AC1 — `stalenessPaths` is sourced from the plan's DEPENDENCY FILE SET
#         (the planner's `### Files to Modify` / `### Files to Create` plus
#         flagged contract files), NOT the enclosing project directory. A
#         directory value counts commits-behind on every commit anywhere in
#         that project (`git rev-list -- <dir>`), so on a busy repo the plan
#         is perpetually "stale"; file-level paths count only commits that
#         touch what the plan depends on.
#   AC2 — on the `stale` verdict the skill analyzes WHY it is stale
#         (`git log --stat <planCommitSha>..HEAD -- <stalenessPaths>`), reads
#         the diffs against the plan, and asks with an informed, `(Recommended)`
#         default instead of a blind AskUserQuestion. The deterministic CLI
#         verdict is unchanged; the agent judgment only picks the default.
#
# Idiom mirrors flow/tests/pipeline-cutover-contract.test.sh: a `failures=`
# counter, small assert_* helpers, self-contained, auto-discovered by the flow
# gate's `*.test.sh` glob (see skills/maintain/scripts/check.sh
# check_structural_tests). It greps the real committed doc directly.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the
# specific replacement text at the edit site, never a generic marker that may
# already match unfixed prose elsewhere): each positive marker below occurs
# exactly once in the file today, and the removed dir-level phrase is asserted
# absent so a regression that restores it fails loudly.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "stale-plan-scoping-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "stale-plan-scoping-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # Prints the real committed file, or fails closed with a distinct "not
  # found" message so a missing/unreadable file can never masquerade as empty
  # content (which would make assert_not_contains trivially pass).
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected marker not found: [${pattern}]"
}

assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden pre-fix text still present: [${pattern}]"
}

# extract_existing_plan_section <phase-1-plan-md-content>
# Returns the "## Existing Plan" section body only (through the next "## "
# heading) so the AC2 markers cannot be satisfied by unrelated text elsewhere.
extract_existing_plan_section() {
  awk '
    /^## Existing Plan$/ { on=1 }
    on && /^## / && !/^## Existing Plan$/ { exit }
    on { print }
  ' <<<"$1"
}

FILE="skills/implement/phases/phase-1-plan.md"
if CONTENT="$(read_doc "${FILE}")"; then
  # --- AC1: stalenessPaths sourced from the plan's dependency file set ------
  # The sourcing guidance must point at the planner's file lists and must NOT
  # instruct sourcing the enclosing project directory.
  assert_contains "${CONTENT}" "dependency file set" "${FILE} (AC1 stalenessPaths sourcing)"
  assert_contains "${CONTENT}" "### Files to Modify" "${FILE} (AC1 stalenessPaths sourcing)"
  assert_not_contains "${CONTENT}" "repo-relative project directory/directories the plan touches" \
    "${FILE} (AC1 stalenessPaths sourcing)"
  # The example front matter must be file-level, not a bare project dir.
  assert_not_contains "${CONTENT}" $'\nstalenessPaths: cenci\n' "${FILE} (AC1 example front matter)"

  # --- AC2: analyzed, recommended default on the stale verdict --------------
  EXISTING_PLAN_SECTION="$(extract_existing_plan_section "${CONTENT}")"
  if [[ -z "${EXISTING_PLAN_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Existing Plan' section"
  else
    assert_contains "${EXISTING_PLAN_SECTION}" "git log --stat" "${FILE} (AC2 stale analysis)"
    assert_contains "${EXISTING_PLAN_SECTION}" "(Recommended)" "${FILE} (AC2 recommended default)"
    # The multiple-disambiguation sub-case must NOT run the commit analysis.
    assert_contains "${EXISTING_PLAN_SECTION}" "there is no commits-behind signal to analyze" \
      "${FILE} (AC2 multiple-disambiguation carve-out)"
  fi
fi

echo "stale-plan-scoping-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
