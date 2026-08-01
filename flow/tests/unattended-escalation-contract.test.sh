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

if ! IMPLEMENT_SKILL_CONTENT="$(cat "${IMPLEMENT_SKILL}" 2>/dev/null)"; then
  fail "SKILL.md: doc not found/unreadable: ${IMPLEMENT_SKILL}"
  IMPLEMENT_SKILL_CONTENT=""
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
# PIN_COMMENT_ANCHOR (#849): the marker template is now nonce-bearing --
# `<nonce>` is the literal placeholder token the phase text substitutes the
# validated nonce into, mirroring escalation-anchor-contract.test.sh's own
# PIN_MARKER_PREFIX/producer-template literal.
PIN_COMMENT_ANCHOR='<!-- cenci-planner-escalation:<nonce> -->'
PIN_NONCE_MINT='openssl rand -hex 16'
PIN_NONCE_REGEX='^[0-9a-f]{32}$'
PIN_NONCE_STOP='stop with an error'
PIN_REST_CREATE='gh api repos/<owner>/<repo>/issues/<number>/comments -F body=@<questions-file> --jq .id'
PIN_FM_NONCE_KEY='escalationNonce'
PIN_FM_COMMENTID_KEY='escalationCommentId'
PIN_PERSIST_ID_RECOVERY='the comment is already posted (step 2 succeeded), so do not re-post it'
PIN_READBACK="gh api repos/<owner>/<repo>/issues/<number>/comments/<id> --jq '{id, body}'"

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
  "must name the pinned nonce-bearing comment anchor template verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_NONCE_MINT}" \
  "must name the openssl rand -hex 16 mint command verbatim (restated, not merely referenced)"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_NONCE_REGEX}" \
  "must restate the nonce format validation regex verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_NONCE_STOP}" \
  "must restate the stop-on-mismatch/failure rule (never a weaker fallback)"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_REST_CREATE}" \
  "must name the REST comment-create call verbatim"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_FM_NONCE_KEY}" \
  "must name the escalationNonce front-matter key"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_FM_COMMENTID_KEY}" \
  "must name the escalationCommentId front-matter key"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_PERSIST_ID_RECOVERY}" \
  "must document the persist-ID-then-verify step's recovery (never re-post a duplicate comment)"
assert_section_contains "${ESCALATION_SECTION}" "${PIN_READBACK}" \
  "must name step 2's comment body read-back verification call verbatim"

# --- Ordering + restated recovery/idempotency per step (flow/docs/pipeline-safety.md
#     rules 1-2: restate, don't merely reference; document recovery for every
#     downstream step) ---------------------------------------------------

# Step numbering shifted (#849): step 0 mints/validates the nonce, step 3
# persists the returned comment ID, so the two pipeline calls that were
# steps 3/4 pre-#849 are now steps 4/5.
ORDERING_MARKER='the ticket comment (step 2) must post before either pipeline call (steps 4 and 5)'
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

# =====================================================================
# #880: the named `## Restore Awaiting-Input State` routine, its
# `## Hard-Stop Inventory` IDs (HS-U0..HS-U5), and step 0's explicit
# non-restoring exception. Bidirectional one-to-one hard-stop coverage
# across all three escalating sections is pinned in
# flow/tests/resume-abort-contract.test.sh; these assertions pin that steps
# 1-5's hard stops are routed through the named routine here too, per the
# plan's Files to Modify entry for this suite.
# =====================================================================

RESTORE_ROUTINE_NAME_MARKER='## Restore Awaiting-Input State'
STEP0_EXCEPTION_MARKER='sole justified exception'
STEP0_NOTHING_TO_RESTORE_MARKER='nothing to restore'

assert_section_contains "${ESCALATION_SECTION}" "${RESTORE_ROUTINE_NAME_MARKER}" \
  "steps 1-5's hard stops must route through the named ## Restore Awaiting-Input State routine (#880)"
assert_section_contains "${ESCALATION_SECTION}" "${STEP0_EXCEPTION_MARKER}" \
  "step 0 must state it is the sole justified exception to the restoration routine (#880)"
assert_section_contains "${ESCALATION_SECTION}" "${STEP0_NOTHING_TO_RESTORE_MARKER}" \
  "step 0's exception must state explicitly that there is nothing to restore before any draft exists (#880)"

HS_U_COUNT=$(grep -coE 'HS-U[0-9]+' <<<"${ESCALATION_SECTION}")
if [[ "${HS_U_COUNT}" -lt 6 ]]; then
  fail "phase-1-plan.md (## Unattended Escalation Path) must cite a distinct HS-U<n> hard-stop inventory ID at each of steps 0-5 (found ${HS_U_COUNT} occurrence(s), want >= 6)"
fi

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
#
# #827 (dispatch auto-resume) splits this branch into an Answered
# sub-bullet (routes to the new `## Resume From Draft` section instead of
# hard-stopping -- see flow/tests/dispatch-resume-contract.test.sh) and a
# Not answered sub-bullet (today's report-and-STOP, byte-unchanged). The
# hard-STOP assertions below are therefore retargeted (section-scoped) to
# the Not answered sub-bullet specifically -- a whole-branch or whole-file
# match would also vacuously pass if the STOP text were ever accidentally
# duplicated into the Answered sub-bullet, and would not fail if the STOP
# text were removed from Not answered but left present elsewhere.
# =====================================================================

AWAITING_INPUT_BRANCH_MARKER='**`awaiting-input`**'
HASPLANFILE_UNSET_STOP_MARKER='leave `hasPlanFile` unset, and **STOP**'
NEVER_FALL_THROUGH_MARKER='never fall through into `## New Plan`'
REPLAN_ESCAPE_HATCH_MARKER='`replan` (passed as user context) is the deliberate escape hatch'

assert_file_contains "${IMPLEMENT_SKILL}" "${AWAITING_INPUT_BRANCH_MARKER}" \
  "Plan Verification must add a bolded awaiting-input decision branch"
assert_file_contains "${IMPLEMENT_SKILL}" "${REPLAN_ESCAPE_HATCH_MARKER}" \
  "awaiting-input branch must name replan as the deliberate discard escape hatch, same mechanism as stale/multiple"

# extract_awaiting_input_branch <content> -- bounds the awaiting-input bullet
# through (not including) the next top-level Plan Verification bullet, so a
# hard-STOP match cannot be vacuously satisfied by an unrelated bullet.
# Pure extraction, no fail() side effect: safe to call inside $(...).
extract_awaiting_input_branch() {
  awk '
    /^- \*\*`awaiting-input`\*\*/ { on=1; print; next }
    on && /^- \*\*Unrecognized/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_not_answered_subbranch <awaiting-input-branch-content> -- returns
# only the Not answered sub-bullet (the last sub-bullet in the branch, so no
# further boundary is needed once it starts). Pure, no fail() side effect.
extract_not_answered_subbranch() {
  awk '
    /^  - \*\*Not answered\*\*/ { on=1; print; next }
    on { print }
  ' <<<"$1"
}

AWAITING_INPUT_BRANCH="$(extract_awaiting_input_branch "${IMPLEMENT_SKILL_CONTENT}")"
if [[ -z "${AWAITING_INPUT_BRANCH}" ]]; then
  fail "SKILL.md: could not locate the awaiting-input branch (extract_awaiting_input_branch returned empty)"
else
  NOT_ANSWERED_SUBBRANCH="$(extract_not_answered_subbranch "${AWAITING_INPUT_BRANCH}")"
  if [[ -z "${NOT_ANSWERED_SUBBRANCH}" ]]; then
    fail "SKILL.md: could not locate the awaiting-input branch's Not answered sub-bullet (extract_not_answered_subbranch returned empty)"
  else
    [[ "${NOT_ANSWERED_SUBBRANCH}" == *"${HASPLANFILE_UNSET_STOP_MARKER}"* ]] || \
      fail "SKILL.md (awaiting-input branch, Not answered sub-bullet) must leave hasPlanFile unset and hard STOP (expected to contain: ${HASPLANFILE_UNSET_STOP_MARKER})"
    [[ "${NOT_ANSWERED_SUBBRANCH}" == *"${NEVER_FALL_THROUGH_MARKER}"* ]] || \
      fail "SKILL.md (awaiting-input branch, Not answered sub-bullet) must explicitly forbid falling through into ## New Plan (expected to contain: ${NEVER_FALL_THROUGH_MARKER})"
  fi
fi

# --- Hard-stop / session-shape rule gains the escalation stop as a third
#     named session shape -------------------------------------------------

THIRD_SESSION_SHAPE_MARKER='Escalation stop'

assert_file_contains "${IMPLEMENT_SKILL}" "${THIRD_SESSION_SHAPE_MARKER}" \
  "hard-stop-after-planning rule must name the escalation stop as a third session shape"

echo "unattended-escalation-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
