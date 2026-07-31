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
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

# =====================================================================
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================
MILESTONE_LABELS_FETCH='--json milestone,labels'
LIFECYCLE_EXCLUSION_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed"'
# #848: a followup is an untriaged capture-queue item (flow/docs/
# followup-triage.md) that leaves the queue only via triage or promotion
# through /cenci:refine, so it must not arrive pre-carrying
# refinement-granted markers — least of all a hands-off-merge grant. This
# extends the exclusion set from 7 to 10 entries; LIFECYCLE_EXCLUSION_MARKER
# above (a substring of this extended list) keeps passing unchanged.
EXTENDED_EXCLUSION_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"'
MILESTONE_NUMBER_MARKER='.milestone.number'
FOLLOWUP_LABEL_ARRAY_MARKER='["Followup"]'
TICKET_MODE_GUARD_MARKER="Ticket mode only: before creating the follow-up issue, fetch the original ticket's milestone and labels"
GRACEFUL_DEGRADE_MARKER='milestone/label inheritance was skipped'
POST_INPUT_MARKER='-X POST --input'
NUMBER_JQ_MARKER='--jq .number'

# #756: title/body/labels/milestone move into a single jq -n --rawfile
# payload sent via `gh api ... -X POST --input`, replacing
# `gh issue create --title "$TITLE" --label ... --milestone ...` built from
# a `TITLE=$(cat ...)` read-back and `mapfile`-collected label array args.
# The milestone anchor moves from the `--milestone` CLI flag to the
# `.milestone.number` jq source field (REST requires the numeric id, not the
# title); the Followup label anchor moves from `--label "Followup"` to a
# `["Followup"]` JSON array element inside the jq filter.
for FILE in "${PHASE_9}" "${ADDRESS_REVIEW_SKILL}"; do
  assert_file_contains "${FILE}" "${MILESTONE_LABELS_FETCH}" \
    "must fetch the original ticket's milestone and labels via gh issue view"
  assert_file_contains "${FILE}" "${LIFECYCLE_EXCLUSION_MARKER}" \
    "must exclude the 7 lifecycle labels on a single source line"
  assert_file_contains "${FILE}" "${EXTENDED_EXCLUSION_MARKER}" \
    "must extend the exclusion array to 10 entries so automerge:ok, Browser, and ui:visual-check are never inherited by a followup ticket (#848)"
  assert_file_contains "${FILE}" "${MILESTONE_NUMBER_MARKER}" \
    "must source the inherited milestone as the numeric .milestone.number, not the title"
  assert_file_contains "${FILE}" "${FOLLOWUP_LABEL_ARRAY_MARKER}" \
    "must apply Followup via a [\"Followup\"] JSON labels array"
  assert_file_contains "${FILE}" "${TICKET_MODE_GUARD_MARKER}" \
    "must gate inheritance to ticket mode only"
  assert_file_contains "${FILE}" "${GRACEFUL_DEGRADE_MARKER}" \
    "must graceful-degrade and note the skip when the metadata fetch fails"
  assert_file_contains "${FILE}" "${POST_INPUT_MARKER}" \
    "must create the followup ticket via gh api ... -X POST --input"
  assert_file_contains "${FILE}" "${NUMBER_JQ_MARKER}" \
    "must parse the new issue number via --jq .number, not the create command's output URL"
  assert_file_lacks "${FILE}" "gh issue create" \
    "must not use gh issue create (migrated to gh api ... -X POST --input)"
  assert_file_lacks "${FILE}" 'TITLE=$(cat' \
    "must not read the title back via TITLE=\$(cat ...)"
  assert_file_lacks "${FILE}" "mapfile" \
    "must not use mapfile (forbidden by shell-rules for Codex portability)"
done

echo "followup-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
