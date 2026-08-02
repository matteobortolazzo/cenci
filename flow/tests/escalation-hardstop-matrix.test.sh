#!/usr/bin/env bash
# Behavioral suite for ticket #913, "pipeline: crash-test refine checkpoint
# recovery and the escalation hard-stop matrix (2/5)" -- AC2 only (Lane 2 of
# the ticket's Parallel Lanes; AC1's ensure-issue.sh real-kill crash test is
# flow/skills/refine/scripts/ensure-issue.test.sh, Lane 1, untouched here).
#
# Truncate-and-re-enter replay matrix over the 21 qualifying `HS-*` rows of
# flow/skills/implement/phases/phase-1-plan.md's `## Hard-Stop Inventory`
# table (read-only source of truth, never modified by this suite): for each
# row, truncate that row's owning canonical command stream at the row's own
# boundary, replay the doc-prescribed recovery continuation, then assert the
# stateful anchor ledger's active-anchor count -- exactly one for 20 rows,
# zero for the documented `HS-U0` non-restoring exception.
#
# Library: flow/tests/adversarial-chain/hardstop-lib.sh (row extraction/
# classification, the three canonical per-section command streams, the
# anchor ledger, truncate/replay). This suite ALSO sources
# flow/tests/adversarial-chain/lib.sh directly (for GOLDEN_GRAPH_FIXTURE /
# extract_golden_field_raw reuse -- Assumption: no second fixture) --
# NEVER flow/tests/refine-write-order/lib.sh or
# flow/tests/parent-close-reconcile/lib.sh, so the two colliding
# sub-libraries documented in adversarial-chain/lib.sh's header are never a
# concern here.
#
# Read before writing/modifying this suite (per flow/AGENTS.md's Critical
# Rules): flow/docs/shell-scripting-gotchas.md (fence-aware extraction, the
# raw/require purity split, the "prove the check itself rejects bad input via
# a deliberately broken COPY, never the real doc/fixture" non-vacuity
# discipline) and flow/tests/adversarial-chain/lib.sh's own header (the
# sub-library-collision hard constraint, inherited unchanged here since this
# suite never touches either colliding library).
#
# Auto-discovered by scripts/run-checks.sh's *.test.sh glob -- no
# registration needed.
#
# Sections:
#   0 -- self-tests / non-vacuity proofs: extract_hard_stop_rows_raw and
#        classify_hard_stop_kind_raw reject/derive correctly, including the
#        mandatory injected-30th-row proof (a broken in-memory COPY of
#        phase-1-plan.md, never the real committed file); count_active_
#        anchors_raw's basic 0/1 counting plus the mandatory HS-R4
#        post-before-persist-nonce inversion proof (must report TWO);
#        truncate_stream_at_boundary_raw's fail-closed behavior.
#   1 -- pinned 21-row qualifying case list, derived from the real doc.
#   2 -- stream/doc fidelity: every authored stream's boundary-label set
#        equals exactly its section's doc-derived qualifying HS-* IDs.
#   3 -- wire all 21 cases: truncate + replay + assert the ledger's active-
#        anchor count per the case table's explicit expected-count column.
#   4 -- pinned scenario count.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "escalation-hardstop-matrix.test.sh: failed to resolve script directory." >&2; exit 2; }
LIB_DIR="${SCRIPT_DIR}/adversarial-chain"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# shellcheck source=./adversarial-chain/lib.sh
if ! source "${LIB_DIR}/lib.sh"; then
  echo "escalation-hardstop-matrix.test.sh: failed to source ${LIB_DIR}/lib.sh" >&2
  exit 2
fi
# shellcheck source=./adversarial-chain/hardstop-lib.sh
if ! source "${LIB_DIR}/hardstop-lib.sh"; then
  echo "escalation-hardstop-matrix.test.sh: failed to source ${LIB_DIR}/hardstop-lib.sh" >&2
  exit 2
fi

assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ok() { [[ "$1" -eq 0 ]] || fail "$2: expected success (exit 0), got exit $1"; }

WORK_DIR="$(mktemp -d)" || { echo "escalation-hardstop-matrix.test.sh: failed to create work directory." >&2; exit 2; }
trap 'rm -rf "${WORK_DIR}"' EXIT

HARDSTOP_CASES_DRIVEN=0
drive_case() {
  HARDSTOP_CASES_DRIVEN=$((HARDSTOP_CASES_DRIVEN + 1))
  "$@"
}

if ! PHASE1_CONTENT="$(cat "${PHASE1_PLAN_DOC}" 2>/dev/null)"; then
  echo "escalation-hardstop-matrix.test.sh: phase-1-plan.md not found/unreadable: ${PHASE1_PLAN_DOC}" >&2
  exit 2
fi

# =====================================================================
# Section 0 -- self-tests / non-vacuity proofs.
# =====================================================================

# --- 0a. extract_hard_stop_rows_raw fail-closed (synthetic content only,
# never the real doc for the reject side).
if extract_hard_stop_rows_raw "" >/dev/null 2>&1; then
  fail "extract_hard_stop_rows_raw: empty content must be rejected fail-closed, never a vacuous pass"
fi
NO_SECTION_DOC="$(printf '# Some Doc\n\nNo hard-stop table here at all.\n')"
if extract_hard_stop_rows_raw "${NO_SECTION_DOC}" >/dev/null 2>&1; then
  fail "extract_hard_stop_rows_raw: a doc missing the ## Hard-Stop Inventory section must be rejected fail-closed"
fi
EMPTY_TABLE_DOC="$(printf '## Hard-Stop Inventory\n\nNo rows here, just prose.\n\n## Next Section\n')"
if extract_hard_stop_rows_raw "${EMPTY_TABLE_DOC}" >/dev/null 2>&1; then
  fail "extract_hard_stop_rows_raw: a present section with zero table rows must be rejected fail-closed"
fi

# Real-doc sanity (never asserted as a self-test; a smoke check the real
# extraction path resolves at all before Section 1 relies on it).
if ! extract_hard_stop_rows_raw "${PHASE1_CONTENT}" >/dev/null; then
  fail "extract_hard_stop_rows_raw: failed to extract rows from the real committed ${PHASE1_PLAN_DOC}"
fi

# --- 0b. classify_hard_stop_kind_raw: Decision 2's two explicit ID-based
# exclusions, checked against the REAL doc's own trigger text, plus a few
# representative positive classifications.
REAL_ROWS="$(extract_hard_stop_rows_raw "${PHASE1_CONTENT}")"
trigger_for_id() { awk -F'\t' -v want="$1" '$1==want{print $3; exit}' <<<"${REAL_ROWS}"; }

assert_eq "$(classify_hard_stop_kind_raw HS-U1 "$(trigger_for_id HS-U1)")" "none" \
  "classify_hard_stop_kind_raw: HS-U1 must be excluded from nonce persistence (Decision 2)"
assert_eq "$(classify_hard_stop_kind_raw HS-U4 "$(trigger_for_id HS-U4)")" "none" \
  "classify_hard_stop_kind_raw: HS-U4 must be excluded from label mutation (Decision 2)"
assert_eq "$(classify_hard_stop_kind_raw HS-U0 "$(trigger_for_id HS-U0)")" "nonce persistence" \
  "classify_hard_stop_kind_raw: HS-U0 (mint) must classify as nonce persistence"
assert_eq "$(classify_hard_stop_kind_raw HS-U5 "$(trigger_for_id HS-U5)")" "label mutation" \
  "classify_hard_stop_kind_raw: HS-U5 (--transition input-needed) must classify as label mutation"
assert_eq "$(classify_hard_stop_kind_raw HS-R10 "$(trigger_for_id HS-R10)")" "planner delegation" \
  "classify_hard_stop_kind_raw: HS-R10 must classify as planner delegation"
assert_eq "$(classify_hard_stop_kind_raw HS-R11 "$(trigger_for_id HS-R11)")" "candidate-plan validation" \
  "classify_hard_stop_kind_raw: HS-R11 must classify as candidate-plan validation"
assert_eq "$(classify_hard_stop_kind_raw HS-R4 "$(trigger_for_id HS-R4)")" "nonce persistence" \
  "classify_hard_stop_kind_raw: HS-R4 (persist-nonce-and-clear) must classify as nonce persistence"

# --- 0c. Non-vacuity proof #1: derive the case list from a deliberately-
# broken IN-MEMORY COPY of phase-1-plan.md's content (never the real file on
# disk -- no write to the committed doc, ever) carrying an injected 30th
# qualifying row. The derived list must change and the pinned-21 count must
# fail loudly if asserted against it.
INJECTED_ROW='| HS-U99 | `## Unattended Escalation Path` step 0 | `openssl rand -hex 16` mint/validate fails (synthetic injected row for this suite'"'"'s own non-vacuity self-test only; never present in the real doc) | Yes |'
FIRST_REAL_ROW='| HS-U0 | `## Unattended Escalation Path` step 0 |'
if [[ "${PHASE1_CONTENT}" != *"${FIRST_REAL_ROW}"* ]]; then
  fail "non-vacuity proof #1: could not locate the real doc's first Hard-Stop Inventory row to build the broken in-memory copy against"
else
  BROKEN_PHASE1_CONTENT="${PHASE1_CONTENT/${FIRST_REAL_ROW}/${INJECTED_ROW}$'\n'${FIRST_REAL_ROW}}"
  if [[ "${BROKEN_PHASE1_CONTENT}" == "${PHASE1_CONTENT}" ]]; then
    fail "non-vacuity proof #1: the injected-row substitution did not change the in-memory copy -- the proof would be vacuous"
  fi
fi

# =====================================================================
# Section 1 -- pinned 21-row qualifying case list, derived from the real doc.
# =====================================================================

PINNED_QUALIFYING_COUNT=21
REAL_QUALIFYING="$(_hardstop_qualifying_ids_raw "${REAL_ROWS}")"
REAL_QUALIFYING_COUNT="$(grep -c . <<<"${REAL_QUALIFYING}" 2>/dev/null || echo 0)"
assert_eq "${REAL_QUALIFYING_COUNT}" "${PINNED_QUALIFYING_COUNT}" \
  "pinned qualifying-row count: the real doc must derive exactly ${PINNED_QUALIFYING_COUNT} qualifying HS-* rows"

EXPECTED_QUALIFYING="$(printf '%s\n' HS-U0 HS-U2 HS-U3 HS-U5 HS-A1 HS-A3 HS-A4 HS-A5 HS-A7 HS-A8 HS-A9 HS-A10 HS-A11 HS-R3 HS-R4 HS-R6 HS-R7 HS-R8 HS-R10 HS-R11 HS-R12 | LC_ALL=C sort -u)"
assert_eq "${REAL_QUALIFYING}" "${EXPECTED_QUALIFYING}" \
  "pinned qualifying-row SET: the real doc's derived qualifying IDs must equal exactly the pinned 21-row set from the plan's Decision 2"

# Non-vacuity proof #1, continued: the broken copy's derived qualifying count
# must NOT equal the pinned 21 -- it must be 22 (21 real + 1 injected), so
# reusing the same "assert == 21" check against it would fail loudly.
if [[ -n "${BROKEN_PHASE1_CONTENT:-}" ]]; then
  BROKEN_ROWS="$(extract_hard_stop_rows_raw "${BROKEN_PHASE1_CONTENT}")"
  BROKEN_QUALIFYING="$(_hardstop_qualifying_ids_raw "${BROKEN_ROWS}")"
  BROKEN_QUALIFYING_COUNT="$(grep -c . <<<"${BROKEN_QUALIFYING}" 2>/dev/null || echo 0)"
  assert_eq "${BROKEN_QUALIFYING_COUNT}" "22" \
    "non-vacuity proof #1: an injected 30th qualifying row must change the derived count away from the pinned 21 (would fail loudly if asserted against 21)"
fi

# =====================================================================
# Section 0d -- count_active_anchors_raw: basic counting plus the mandatory
# HS-R4 post-before-persist-nonce inversion proof (must report TWO).
# =====================================================================

LEDGER_SELFTEST_DIR="${WORK_DIR}/ledger-selftest"
mkdir -p "${LEDGER_SELFTEST_DIR}" || fail "mkdir failed for ledger self-test dir"
ledger_init_raw "${LEDGER_SELFTEST_DIR}" || fail "ledger_init_raw failed for self-test dir"
ledger_set_draft_raw "${LEDGER_SELFTEST_DIR}" "n1" ""
COUNT0="$(count_active_anchors_raw "${LEDGER_SELFTEST_DIR}")"
assert_eq "${COUNT0}" "0" "count_active_anchors_raw: no comment id persisted yet must count zero active anchors"

ledger_post_comment_raw "${LEDGER_SELFTEST_DIR}" "500" "n1"
ledger_set_draft_raw "${LEDGER_SELFTEST_DIR}" "n1" "500"
COUNT1="$(count_active_anchors_raw "${LEDGER_SELFTEST_DIR}")"
assert_eq "${COUNT1}" "1" "count_active_anchors_raw: a comment matching both draft nonce and id must count exactly one active anchor"

if count_active_anchors_raw "${WORK_DIR}/does-not-exist" >/dev/null 2>&1; then
  fail "count_active_anchors_raw: an unreadable ledger directory must be rejected fail-closed, never a vacuous pass"
fi

# Mandatory non-vacuity proof #2 (Test Strategy item 2 / Risks: circular
# model): models the ledger-level consequence of the HS-R4
# "post-before-persist-nonce" ordering inversion (#880) -- the correct order
# persists the fresh nonce (clearing the stale escalationCommentId) BEFORE
# posting, so a duplicate-processed retry can never register two comments
# both satisfying "nonce == draft.nonce AND id == draft.id" simultaneously.
# Skipping that atomic clear-then-post ordering lets a non-idempotent retry
# append the SAME (nonce, id) pair into the comments ledger twice. This
# proves count_active_anchors_raw performs a genuine COUNT of matching rows
# -- not a capped/boolean "at least one" check that could never distinguish
# 1 from 2 -- so the matrix does not merely pass by construction.
R4_INVERSION_DIR="${WORK_DIR}/r4-inversion"
mkdir -p "${R4_INVERSION_DIR}" || fail "mkdir failed for HS-R4 inversion self-test dir"
ledger_init_raw "${R4_INVERSION_DIR}" || fail "ledger_init_raw failed for HS-R4 inversion self-test dir"
ledger_post_comment_raw "${R4_INVERSION_DIR}" "700" "nonce-dup"
ledger_post_comment_raw "${R4_INVERSION_DIR}" "700" "nonce-dup"
ledger_set_draft_raw "${R4_INVERSION_DIR}" "nonce-dup" "700"
R4_COUNT="$(count_active_anchors_raw "${R4_INVERSION_DIR}")"
assert_eq "${R4_COUNT}" "2" \
  "count_active_anchors_raw: HS-R4 post-before-persist-nonce inversion non-vacuity proof -- a duplicated (nonce,id) ledger row pair must be counted as TWO active anchors"

# =====================================================================
# Section 0e -- truncate_stream_at_boundary_raw self-tests.
# =====================================================================

SAMPLE_STREAM="$(printf 'HS-X1\tnoop\nHS-X2\tpersist_nonce\tn1\nHS-X3\tnoop\n')"
SAMPLE_PREFIX="$(truncate_stream_at_boundary_raw "${SAMPLE_STREAM}" "HS-X2")"
assert_eq "${SAMPLE_PREFIX}" "HS-X1	noop" \
  "truncate_stream_at_boundary_raw: prefix must include exactly the lines strictly before the boundary"

if truncate_stream_at_boundary_raw "${SAMPLE_STREAM}" "HS-NOPE" >/dev/null 2>&1; then
  fail "truncate_stream_at_boundary_raw: an unresolvable boundary must be rejected fail-closed, never a vacuous pass"
fi

# =====================================================================
# Section 2 -- stream/doc fidelity: every authored stream's boundary-label
# set must equal exactly its section's doc-derived qualifying HS-* IDs, so a
# future doc row addition fails loudly instead of silently going uncovered.
# =====================================================================

assert_stream_fidelity() {
  local group="$1" stream="$2" label="$3"
  local doc_ids stream_ids
  doc_ids="$(_hardstop_group_qualifying_ids_raw "${REAL_ROWS}" "${group}")"
  stream_ids="$(_hardstop_stream_ids_raw "${stream}")"
  assert_eq "${stream_ids}" "${doc_ids}" \
    "stream/doc fidelity (${label}): the authored stream's boundary-label set must equal exactly the doc-derived qualifying HS-* IDs for this group"
}

assert_stream_fidelity unattended "${UNATTENDED_ESCALATION_STREAM}" "Unattended-Escalation"
assert_stream_fidelity repair-i "${REPAIR_CASE_I_STREAM}" "Repair-Anchor case (i)"
assert_stream_fidelity repair-ii "${REPAIR_CASE_II_STREAM}" "Repair-Anchor case (ii)"
assert_stream_fidelity repair-iii "${REPAIR_CASE_III_STREAM}" "Repair-Anchor case (iii)"
assert_stream_fidelity resume "${RESUME_FROM_DRAFT_STREAM}" "Resume-From-Draft"

# =====================================================================
# Section 3 -- wire all 21 cases. Per-row explicit expected-count column
# (never a special-cased `if`): 20 rows expect exactly one active anchor
# after the truncate+recovery replay completes; HS-U0 is the sole documented
# exception, expecting zero, because no draft or comment exists yet at that
# boundary (## Hard-Stop Inventory's own note: HS-U0 "stays a bare-`Working`
# stop").
# =====================================================================

declare -a CASE_TABLE=(
  "HS-U0:unattended:0"    # non-restoring carve-out (inventory note above)
  "HS-U2:unattended:1"
  "HS-U3:unattended:1"
  "HS-U5:unattended:1"
  "HS-A1:repair-i:1"
  "HS-A9:repair-i:1"
  "HS-A3:repair-ii:1"
  "HS-A4:repair-ii:1"
  "HS-A10:repair-ii:1"
  "HS-A5:repair-iii:1"
  "HS-A7:repair-iii:1"
  "HS-A8:repair-iii:1"
  "HS-A11:repair-iii:1"
  "HS-R3:resume:1"
  "HS-R4:resume:1"
  "HS-R6:resume:1"
  "HS-R7:resume:1"
  "HS-R8:resume:1"
  "HS-R10:resume:1"
  "HS-R11:resume:1"
  "HS-R12:resume:1"
)

CASE_TABLE_COUNT="${#CASE_TABLE[@]}"
assert_eq "${CASE_TABLE_COUNT}" "${PINNED_QUALIFYING_COUNT}" \
  "CASE_TABLE: must carry exactly the pinned ${PINNED_QUALIFYING_COUNT} qualifying rows"

CASE_TABLE_IDS="$(printf '%s\n' "${CASE_TABLE[@]}" | cut -d: -f1 | LC_ALL=C sort -u)"
assert_eq "${CASE_TABLE_IDS}" "${REAL_QUALIFYING}" \
  "CASE_TABLE: its ID set must equal exactly the doc-derived qualifying set (never a hand-copied list disconnected from the doc)"

for entry in "${CASE_TABLE[@]}"; do
  IFS=':' read -r case_id case_group case_expected <<<"${entry}"
  seed_nonce="" seed_id="" seed_comments="" stream=""
  case "${case_group}" in
    unattended)
      stream="${UNATTENDED_ESCALATION_STREAM}"
      ;;
    repair-i)
      stream="${REPAIR_CASE_I_STREAM}"
      seed_nonce="nonce-repair-i"
      seed_comments="$(printf '%s\t%s\n' "${HARDSTOP_GOLDEN_CHILD1}" "nonce-repair-i")"
      ;;
    repair-ii)
      stream="${REPAIR_CASE_II_STREAM}"
      seed_nonce="nonce-repair-ii"
      ;;
    repair-iii)
      stream="${REPAIR_CASE_III_STREAM}"
      ;;
    resume)
      stream="${RESUME_FROM_DRAFT_STREAM}"
      seed_nonce="nonce-orig"
      seed_id="${HARDSTOP_GOLDEN_PARENT}"
      seed_comments="$(printf '%s\t%s\n' "${HARDSTOP_GOLDEN_PARENT}" "nonce-orig")"
      ;;
    *)
      fail "CASE_TABLE: unrecognized group [${case_group}] for ${case_id}"
      continue
      ;;
  esac

  CASE_DIR="${WORK_DIR}/case-${case_id}"
  mkdir -p "${CASE_DIR}" || { fail "mkdir failed for ${case_id} case dir"; continue; }

  drive_case replay_hardstop_case "${CASE_DIR}" "${stream}" "${case_id}" "${seed_nonce}" "${seed_id}" "${seed_comments}"
  assert_ok "$?" "replay_hardstop_case(${case_id}): must replay cleanly"

  count="$(count_active_anchors_raw "${CASE_DIR}")"; rc=$?
  if [[ ${rc} -ne 0 ]]; then
    fail "${case_id}: count_active_anchors_raw failed: ${count}"
  else
    assert_eq "${count}" "${case_expected}" "${case_id}: expected ${case_expected} active anchor(s) after truncate+recovery replay completes"
  fi
done

# =====================================================================
# Section 4 -- pinned scenario count: a fixed non-vacuity floor independent
# of #912's own SCENARIO_COUNT (flow/tests/adversarial-chain.test.sh, left
# untouched).
# =====================================================================

HARDSTOP_SCENARIO_COUNT=21
assert_eq "${HARDSTOP_CASES_DRIVEN}" "${HARDSTOP_SCENARIO_COUNT}" \
  "pinned HARDSTOP_SCENARIO_COUNT: expected exactly ${HARDSTOP_SCENARIO_COUNT} hard-stop cases actually driven"

echo "escalation-hardstop-matrix.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
