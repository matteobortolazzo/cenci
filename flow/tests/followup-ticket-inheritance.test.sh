#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #635: follow-up ticket
# creation must inherit the original ticket's milestone and non-lifecycle
# labels, in ticket mode only, with a graceful degrade on fetch failure.
# Follows the fixture-free, grep-based idiom of
# flow/tests/implement-review-artifacts.test.sh: `failures` counter,
# `assert_*`/`grep -qF` helpers, plain `#!/usr/bin/env bash`.
#
# Both call sites are asserted identically:
#   - flow/skills/implement/phases/phase-9-pr.md -> ## Followup Ticket
#   - flow/skills/address-review/SKILL.md -> ## Followup Ticket for
#     Acknowledged Comments ("If absent" create path)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
ADDRESS_REVIEW_SKILL="${FLOW_DIR}/skills/address-review/SKILL.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}

# =====================================================================
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================
MILESTONE_LABELS_FETCH='--json milestone,labels'
LIFECYCLE_EXCLUSION_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed"'
MILESTONE_FLAG_MARKER='--milestone'
FOLLOWUP_LABEL_MARKER='--label "Followup"'
TICKET_MODE_GUARD_MARKER="Ticket mode only: before creating the follow-up issue, fetch the original ticket's milestone and labels"
GRACEFUL_DEGRADE_MARKER='milestone/label inheritance was skipped'

for FILE in "${PHASE_9}" "${ADDRESS_REVIEW_SKILL}"; do
  assert_file_contains "${FILE}" "${MILESTONE_LABELS_FETCH}" \
    "must fetch the original ticket's milestone and labels via gh issue view"
  assert_file_contains "${FILE}" "${LIFECYCLE_EXCLUSION_MARKER}" \
    "must exclude the 7 lifecycle labels on a single source line"
  assert_file_contains "${FILE}" "${MILESTONE_FLAG_MARKER}" \
    "must pass the inherited milestone via --milestone"
  assert_file_contains "${FILE}" "${FOLLOWUP_LABEL_MARKER}" \
    "must retain --label \"Followup\" on the created issue"
  assert_file_contains "${FILE}" "${TICKET_MODE_GUARD_MARKER}" \
    "must gate inheritance to ticket mode only"
  assert_file_contains "${FILE}" "${GRACEFUL_DEGRADE_MARKER}" \
    "must graceful-degrade and note the skip when the metadata fetch fails"
done

echo "followup-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
