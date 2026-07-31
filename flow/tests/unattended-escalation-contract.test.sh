#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #826 (6/8 of #661): the
# unattended lean-mode planner escalation path — a draft `awaiting-input`
# plan persisted to `.plans/`, the questions posted as a ticket comment
# carrying the `<!-- cenci-planner-escalation -->` anchor, the
# `waiting_for_input` stage recorded via `cenci pipeline await-input <id>`,
# the `Input Needed` board label applied via `--transition input-needed`,
# and a clean stop (skills/implement/phases/phase-1-plan.md's new
# `## Unattended Escalation Path` section) — plus the matching
# `awaiting-input` `plan-check` decision branch in the implement skill's
# Plan Verification (skills/implement/SKILL.md), which must leave
# `hasPlanFile` unset and hard-stop rather than falling through into
# `## New Plan`.
#
# Fixture-free, grep-based idiom of flow/tests/lean-planning-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, `assert_file_contains`/
# `assert_file_lacks` helpers built on `grep -qF`, markers kept on a single
# source line (per docs/shell-scripting-gotchas.md), auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob — no registration needed. No
# `read_*`-named helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan.
#
# Section-scoped extraction (awk, bounded to the next `## `-level heading)
# is used wherever a marker could otherwise be vacuously satisfied by
# unrelated text elsewhere in the file, per
# docs/shell-scripting-gotchas.md's marker-precision rule and the
# `extract_*_section` idiom already used by
# flow/tests/pipeline-cutover-contract.test.sh.
#
# Covered files:
#   - flow/skills/implement/phases/phase-1-plan.md (## Unattended Escalation
#     Path — the 7 pinned cross-lane contract strings, restated
#     recovery/idempotency per step; ## Route Planner Output's lean-mode
#     routing bullet; the interactive branch's unchanged AskUserQuestion
#     wording)
#   - flow/skills/implement/SKILL.md (Plan Verification's `awaiting-input`
#     branch: hasPlanFile left unset, hard STOP, never falls through to
#     ## New Plan)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "unattended-escalation-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "unattended-escalation-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}
assert_file_occurs_at_most_once() {
  # $1=file $2=needle $3=description — protects a marker string already
  # owned (and pinned to exactly one file-wide occurrence) by
  # plan-persist-sections-contract.test.sh's assert_occurs_once from being
  # duplicated by this ticket's restated error handling.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  local count
  count="$(grep -cF -- "$2" "$1")"
  [[ "${count}" -le 1 ]] || fail "$(basename "$1") $3 (expected at most one occurrence, found ${count}: $2)"
}

# extract_section_raw <content> <heading-line> -- pure extractor, safe to
# call inside $(...): returns the named "## <heading>" section body through
# the next "## "-level heading (or EOF). No fail() side effect here.
extract_section_raw() {
  awk -v want="$2" '
    $0 == want { on=1; next }
    on && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# require_section <result-var> <content> <heading-line> <label> -- nameref
# wrapper: assigns the extracted section body into <result-var>, or fails
# closed with a distinct "section not found" message and assigns "" (so a
# missing section never masquerades as an empty-but-present one). Must NOT
# be invoked via $(...).
require_section() {
  local -n _result="$1"
  local _body
  _body="$(extract_section_raw "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _result=""
    return 1
  fi
  _result="${_body}"
}

if ! PHASE1_CONTENT="$(cat "${PHASE1_PLAN}" 2>/dev/null)"; then
  fail "phase-1-plan.md: doc not found/unreadable: ${PHASE1_PLAN}"
  PHASE1_CONTENT=""
fi

# =====================================================================
# ## Unattended Escalation Path -- must exist and name all 7 pinned
# cross-lane contract strings verbatim (see the plan's "Pinned cross-lane
# contract" line), scoped to this section so a match elsewhere in the file
# cannot vacuously satisfy it.
# =====================================================================

require_section ESCALATION_SECTION "${PHASE1_CONTENT}" "## Unattended Escalation Path" "phase-1-plan.md" || true

assert_section_contains() {
  # $1=section-body $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" == *"$2"* ]] || fail "phase-1-plan.md (## Unattended Escalation Path) $3 (expected to contain: $2)"
}

PIN_AWAIT_INPUT_CMD='cenci pipeline await-input <id>'
PIN_LABEL_TRANSITION='--transition input-needed'
PIN_LABEL_NAME='Input Needed'
PIN_STAGE='waiting_for_input'
PIN_PLAN_STATUS='status: awaiting-input'
PIN_PLANCHECK_DECISION='awaiting-input'
PIN_COMMENT_ANCHOR='<!-- cenci-planner-escalation -->'

assert_section_contains "${ESCALATION_SECTION}" "${PIN_AWAIT_INPUT_CMD}" \
  "must name the pinned command cenci pipeline await-input <id> verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_LABEL_TRANSITION}" \
  "must name the pinned label transition --transition input-needed verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_LABEL_NAME}" \
  "must name the pinned label Input Needed verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_STAGE}" \
  "must name the pinned stage waiting_for_input verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_PLAN_STATUS}" \
  "must name the pinned plan front matter status: awaiting-input verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_PLANCHECK_DECISION}" \
  "must name the pinned plan-check decision awaiting-input verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_COMMENT_ANCHOR}" \
  "must name the pinned comment anchor <!-- cenci-planner-escalation --> verbatim"

# --- Ordering + restated recovery/idempotency per step (flow/docs/pipeline-safety.md
#     rules 1-2: restate, don't merely reference; document recovery for every
#     downstream step) ---------------------------------------------------

ORDERING_MARKER='the ticket comment (step 2) must post before either pipeline call (steps 3 and 4)'
STEP2_RECOVERY_MARKER='the very next `/cenci:implement <id>` attempt retries cleanly from step 1'
STEP3_NOOP_MARKER='This call is a monotonic no-op on retry'
STEP4_RECOVERY_MARKER='re-running this one step alone is the correct recovery'
STEP1_IDEMPOTENT_MARKER='this step is idempotent'
RESTATED_NOT_REFERENCED_MARKER='**Restated for this path, not referenced**'

assert_section_contains "${ESCALATION_SECTION}" "${ORDERING_MARKER}" \
  "must state the comment-before-pipeline-calls ordering requirement"
assert_section_contains "${ESCALATION_SECTION}" "${STEP1_IDEMPOTENT_MARKER}" \
  "must document step 1 (persist draft) recovery/idempotency on retry"
assert_section_contains "${ESCALATION_SECTION}" "${STEP2_RECOVERY_MARKER}" \
  "must document step 2 (post comment) recovery on a failed gh issue comment call"
assert_section_contains "${ESCALATION_SECTION}" "${STEP3_NOOP_MARKER}" \
  "must document step 3 (await-input) recovery/idempotency on retry"
assert_section_contains "${ESCALATION_SECTION}" "${STEP4_RECOVERY_MARKER}" \
  "must document step 4 (label swap) recovery on a failed label call"
assert_section_contains "${ESCALATION_SECTION}" "${RESTATED_NOT_REFERENCED_MARKER}" \
  "must restate (not merely reference) ## Persist the Plan's verification/error handling, per flow/docs/pipeline-safety.md"

# --- Negative: must not duplicate plan-persist-sections-contract.test.sh's
#     assert_occurs_once markers, which are pinned to exactly one
#     file-wide occurrence each (in ## Persist the Plan) ------------------

assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** post the `planComment` audit comment' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-post-planComment marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** run the `Planned` label transition' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-run-label-transition marker (restate with distinct wording)"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** record the plan artifact' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-record-artifact marker (restate with distinct wording)"

# =====================================================================
# ## Route Planner Output -- lean mode routes to ## Unattended Escalation
# Path from the "questions exist" bullet; interactive mode is unaffected.
# =====================================================================

if require_section ROUTE_SECTION "${PHASE1_CONTENT}" "## Route Planner Output" "phase-1-plan.md"; then
  assert_section_route_contains() {
    # $1=needle $2=label
    [[ -n "$1" ]] || { fail "$2: empty needle"; return; }
    [[ "${ROUTE_SECTION}" == *"$1"* ]] || fail "phase-1-plan.md (## Route Planner Output) $2 (expected to contain: $1)"
  }
  assert_section_route_lacks() {
    # $1=needle $2=label
    [[ -n "$1" ]] || { fail "$2: empty needle"; return; }
    [[ "${ROUTE_SECTION}" != *"$1"* ]] || fail "phase-1-plan.md (## Route Planner Output) $2 (expected to NOT contain: $1)"
  }

  # Positive: interactive mode's AskUserQuestion wording is present and
  # unchanged -- the negative marker for "interactive planning stays
  # byte-unchanged" (per the plan's Architectural Context constraint).
  assert_section_route_contains "AskUserQuestion" \
    "interactive-mode questions-exist branch must still use AskUserQuestion"
  # Positive: the escalation route is explicitly gated on lean autonomy.
  assert_section_route_contains 'planning.autonomy` is exactly `"lean"`' \
    "lean-mode routing must be explicitly conditioned on planning.autonomy being exactly \"lean\""
  assert_section_route_contains "## Unattended Escalation Path" \
    "questions-exist bullet must route lean mode to ## Unattended Escalation Path"
  # Negative: the routing bullet stays a thin dispatcher -- the escalation
  # mechanics (the pinned await-input command) live only in
  # ## Unattended Escalation Path itself, not duplicated inline here.
  assert_section_route_lacks "${PIN_AWAIT_INPUT_CMD}" \
    "routing bullet must not inline the await-input mechanics (they belong to ## Unattended Escalation Path only)"
fi

# =====================================================================
# skills/implement/SKILL.md -- Plan Verification's awaiting-input branch:
# hasPlanFile left unset, hard STOP, never falls through to ## New Plan.
# =====================================================================

AWAITING_INPUT_BRANCH_MARKER='**`awaiting-input`**'
HASPLANFILE_UNSET_STOP_MARKER='leave `hasPlanFile` unset, and **STOP**'
NEVER_FALL_THROUGH_MARKER='never fall through into `## New Plan`'
REPLAN_ESCAPE_HATCH_MARKER='`replan` (passed as user context) is the deliberate escape hatch'

assert_file_contains "${IMPLEMENT_SKILL}" "${AWAITING_INPUT_BRANCH_MARKER}" \
  "Plan Verification must add a bolded awaiting-input decision branch"
assert_file_contains "${IMPLEMENT_SKILL}" "${HASPLANFILE_UNSET_STOP_MARKER}" \
  "awaiting-input branch must leave hasPlanFile unset and hard STOP"
assert_file_contains "${IMPLEMENT_SKILL}" "${NEVER_FALL_THROUGH_MARKER}" \
  "awaiting-input branch must explicitly forbid falling through into ## New Plan"
assert_file_contains "${IMPLEMENT_SKILL}" "${REPLAN_ESCAPE_HATCH_MARKER}" \
  "awaiting-input branch must name replan as the deliberate discard escape hatch, same mechanism as stale/multiple"

# --- Hard-stop / session-shape rule gains the escalation stop as a third
#     named session shape -------------------------------------------------

THIRD_SESSION_SHAPE_MARKER='Escalation stop'

assert_file_contains "${IMPLEMENT_SKILL}" "${THIRD_SESSION_SHAPE_MARKER}" \
  "hard-stop-after-planning rule must name the escalation stop as a third session shape"

echo "unattended-escalation-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
