#!/usr/bin/env bash
# Behavioral suite for ticket #912, "pipeline: build the cross-layer
# adversarial chain driver and shared post-refinement fixture (1/5)".
# Orchestrates flow/tests/refine-write-order/lib.sh (#878) and
# flow/tests/parent-close-reconcile/lib.sh (#879) as SUBSHELL-ISOLATED stage
# drivers over one ordered recorded gh/git stream spanning refine ->
# escalation/resume -> parent-close, asserted against the committed golden
# fixture flow/tests/adversarial-chain/fixtures/post-refinement-graph.json --
# the same fixture watch/internal/dispatch/chaingolden_test.go asserts
# independently, cross-module.
#
# Read before writing this suite (per flow/AGENTS.md's Critical Rules):
#   - flow/docs/shell-scripting-gotchas.md -- fence-aware extraction, the
#     read_*_raw/require_read_* purity split, the "prove the check itself
#     rejects bad input via a deliberately broken COPY, never the real
#     doc/fixture" non-vacuity discipline.
#   - flow/tests/adversarial-chain/lib.sh's own header -- the hard
#     sub-library-collision constraint this suite's driver is built around.
#
# Library: flow/tests/adversarial-chain/lib.sh (extractors, fixture
# projectors, subshell-isolated stage drivers, oracle wrappers). This suite
# ALSO sources flow/tests/refine-write-order/lib.sh directly at its own top
# level (for section 1's doc-side extract_write_order_ops_raw reuse) --
# never flow/tests/parent-close-reconcile/lib.sh, which is reached only
# through adversarial-chain/lib.sh's subshell-isolated wrappers, so the two
# colliding sub-libraries are never sourced into the same shell.
#
# Auto-discovered by scripts/run-checks.sh's *.test.sh glob -- no
# registration needed.
#
# Sections:
#   0 -- self-tests: every new driver helper rejects bad input via a
#        deliberately broken COPY/synthetic fixture, never the real
#        docs/committed fixture; plus a "stage driver used its own
#        library's fixture root" non-collision proof.
#   1 -- fixture<->doc agreement: the golden fixture's writeOrder must equal
#        extract_write_order_ops_raw's real extraction from refine/SKILL.md
#        and refine/codex.md.
#   2 -- AC2: one ordered recorded gh/git stream across refine ->
#        escalation/resume -> parent-close, asserted on exact argv, call
#        counts, and ordering.
#   3 -- AC3: declined refinement and every pre-confirmation interruption
#        produce zero write-classified commands (classify_gh_call's
#        fail-closed "unknown" counts as a write).
#   4 -- AC4: parent-audit close<->hold retries, and an open sibling
#        projected from the fixture forces hold.
#   5 -- a pinned SCENARIO_COUNT asserted against the number of scenarios
#        actually driven.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "adversarial-chain.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "adversarial-chain.test.sh: failed to resolve flow directory." >&2; exit 2; }
LIB_DIR="${SCRIPT_DIR}/adversarial-chain"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# shellcheck source=./adversarial-chain/lib.sh
if ! source "${LIB_DIR}/lib.sh"; then
  echo "adversarial-chain.test.sh: failed to source ${LIB_DIR}/lib.sh" >&2
  exit 2
fi
# shellcheck source=./refine-write-order/lib.sh
if ! source "${SCRIPT_DIR}/refine-write-order/lib.sh"; then
  echo "adversarial-chain.test.sh: failed to source ${SCRIPT_DIR}/refine-write-order/lib.sh" >&2
  exit 2
fi

assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_ok() { [[ "$1" -eq 0 ]] || fail "$2: expected success (exit 0), got exit $1"; }
assert_nonzero() { [[ "$1" -ne 0 ]] || fail "$2: expected non-zero exit, got 0"; }
assert_reason_contains() {
  # $1=exit-code $2=printed-reason $3=required-substring $4=label
  if [[ "$1" -eq 0 ]]; then
    fail "$4: expected the check to reject this input, but it accepted it"
    return
  fi
  [[ "$2" == *"$3"* ]] || fail "$4: expected failure reason to contain [$3], got [$2]"
}

WORK_DIR="$(mktemp -d)" || { echo "adversarial-chain.test.sh: failed to create work directory." >&2; exit 2; }
trap 'rm -rf "${WORK_DIR}"' EXIT

SCENARIOS_DRIVEN=0
drive_scenario() {
  SCENARIOS_DRIVEN=$((SCENARIOS_DRIVEN + 1))
  "$@"
}

# =====================================================================
# Section 0 -- self-tests: every new driver helper rejects bad input via a
# deliberately broken COPY/synthetic fixture, never the real committed
# fixture or docs (docs/adapter-contract.md's non-vacuity rule; mirrors
# refine-write-order.test.sh / parent-close-reconcile.test.sh's established
# idiom).
# =====================================================================

# --- 0a. extract_golden_field_raw ----------------------------------------

BROKEN_FIXTURE_DIR="$(mktemp -d)" || { fail "mktemp -d failed for broken-fixture self-test"; BROKEN_FIXTURE_DIR=""; }
if [[ -n "${BROKEN_FIXTURE_DIR}" ]]; then
  BROKEN_FIXTURE="${BROKEN_FIXTURE_DIR}/broken.json"
  printf 'not valid json{{{' > "${BROKEN_FIXTURE}"

  if ( GOLDEN_GRAPH_FIXTURE="${BROKEN_FIXTURE}"; extract_golden_field_raw '.repo' ) >/dev/null 2>&1; then
    fail "extract_golden_field_raw non-vacuity self-test: a malformed COPY of the fixture must be rejected, not silently parsed"
  fi

  if ( GOLDEN_GRAPH_FIXTURE="${BROKEN_FIXTURE_DIR}/does-not-exist.json"; extract_golden_field_raw '.repo' ) >/dev/null 2>&1; then
    fail "extract_golden_field_raw non-vacuity self-test: a missing fixture path must be rejected, never a vacuous pass"
  fi

  if extract_golden_field_raw '' >/dev/null 2>&1; then
    fail "extract_golden_field_raw: an empty filter must be rejected, never a vacuous pass"
  fi

  rm -rf "${BROKEN_FIXTURE_DIR}"
fi

# Real-fixture sanity (never asserted as a self-test, just a smoke check
# that the real path resolves): proves the constant itself is wired.
if ! extract_golden_field_raw '.schemaVersion' >/dev/null; then
  fail "extract_golden_field_raw: failed to read the real committed golden fixture at ${GOLDEN_GRAPH_FIXTURE}"
fi

# --- 0b. merge_stage_stream_raw -------------------------------------------

MERGE_SELFTEST_DIR="$(mktemp -d)" || { fail "mktemp -d failed for merge_stage_stream_raw self-test"; MERGE_SELFTEST_DIR=""; }
if [[ -n "${MERGE_SELFTEST_DIR}" ]]; then
  GOOD_LOG="${MERGE_SELFTEST_DIR}/good.log"
  printf 'issue edit 100 --repo o/r --add-label Working\nissue edit 100 --repo o/r --add-label Refined\n' > "${GOOD_LOG}"
  GOOD_EXPECTED="$(printf 'gh issue edit 100 --repo o/r --add-label Working\ngh issue edit 100 --repo o/r --add-label Refined\n')"
  GOOD_STREAM="${MERGE_SELFTEST_DIR}/good-stream.tsv"
  : > "${GOOD_STREAM}"
  merge_stage_stream_raw "selftest" "gh" "${GOOD_EXPECTED}" "${GOOD_LOG}" "${GOOD_STREAM}"
  assert_ok "$?" "merge_stage_stream_raw: a log that exactly matches its expected subsequence must be accepted"
  GOOD_STREAM_LINES="$(grep -c . "${GOOD_STREAM}" 2>/dev/null || echo 0)"
  assert_eq "${GOOD_STREAM_LINES}" "2" \
    "merge_stage_stream_raw: the stream file must carry exactly the 2 normalized records"

  # Reject side (non-vacuity): a deliberately-broken COPY of the log with one
  # invocation dropped must be rejected on count mismatch.
  SHORT_LOG="${MERGE_SELFTEST_DIR}/short.log"
  printf 'issue edit 100 --repo o/r --add-label Working\n' > "${SHORT_LOG}"
  SHORT_STREAM="${MERGE_SELFTEST_DIR}/short-stream.tsv"
  : > "${SHORT_STREAM}"
  reason="$(merge_stage_stream_raw "selftest" "gh" "${GOOD_EXPECTED}" "${SHORT_LOG}" "${SHORT_STREAM}")"; rc=$?
  assert_reason_contains "${rc}" "${reason}" "logged 1 invocation" \
    "merge_stage_stream_raw: a log missing an expected invocation must be rejected on count mismatch"

  # Reject side (non-vacuity): a deliberately-broken COPY of the log with a
  # reordered/altered invocation must be rejected on content mismatch.
  WRONG_LOG="${MERGE_SELFTEST_DIR}/wrong.log"
  printf 'issue edit 100 --repo o/r --add-label Refined\nissue edit 100 --repo o/r --add-label Working\n' > "${WRONG_LOG}"
  WRONG_STREAM="${MERGE_SELFTEST_DIR}/wrong-stream.tsv"
  : > "${WRONG_STREAM}"
  reason="$(merge_stage_stream_raw "selftest" "gh" "${GOOD_EXPECTED}" "${WRONG_LOG}" "${WRONG_STREAM}")"; rc=$?
  assert_reason_contains "${rc}" "${reason}" "mismatch" \
    "merge_stage_stream_raw: a reordered log must be rejected on content mismatch, not silently accepted"

  # binary_filter narrows the expected subsequence: only "git"-tagged expected
  # lines should be compared against a git-only log.
  MIXED_EXPECTED="$(printf 'gh issue view 100 --repo o/r --json subIssues\ngit -C /tmp/wt log -1 --format=%%B\n')"
  GIT_ONLY_LOG="${MERGE_SELFTEST_DIR}/git-only.log"
  printf -- '-C /tmp/wt log -1 --format=%%B\n' > "${GIT_ONLY_LOG}"
  GIT_STREAM="${MERGE_SELFTEST_DIR}/git-stream.tsv"
  : > "${GIT_STREAM}"
  merge_stage_stream_raw "selftest" "git" "${MIXED_EXPECTED}" "${GIT_ONLY_LOG}" "${GIT_STREAM}"
  assert_ok "$?" "merge_stage_stream_raw: binary_filter must narrow the expected list to just that binary's subsequence"

  if merge_stage_stream_raw "selftest" "" "${GOOD_EXPECTED}" "${MERGE_SELFTEST_DIR}/does-not-exist.log" "${GOOD_STREAM}" >/dev/null 2>&1; then
    fail "merge_stage_stream_raw: an unreadable log file must be rejected, never a vacuous pass"
  fi

  # Reject side (non-vacuity): a 0-expected-lines call must be rejected
  # fail-closed, never trivially "pass" by matching an equally-empty log
  # (0 == 0) having asserted nothing.
  EMPTY_LOG="${MERGE_SELFTEST_DIR}/empty.log"
  : > "${EMPTY_LOG}"
  EMPTY_STREAM="${MERGE_SELFTEST_DIR}/empty-stream.tsv"
  : > "${EMPTY_STREAM}"
  if merge_stage_stream_raw "selftest" "" "" "${EMPTY_LOG}" "${EMPTY_STREAM}" >/dev/null 2>&1; then
    fail "merge_stage_stream_raw: a 0-expected-lines call must be rejected fail-closed, never a vacuous 0/0 pass"
  fi

  rm -rf "${MERGE_SELFTEST_DIR}"
fi

# --- 0c. project_refine_parent_view_json / project_parent_subissues_view_json
#
# Dirs are allocated under the suite's own checked WORK_DIR (never a fresh
# unchecked `mktemp -d` substitution) so allocation failure is caught
# explicitly and cleanup stays under WORK_DIR's single EXIT trap.

SELFTEST_0C_DIR_1="${WORK_DIR}/selftest-0c-1"
mkdir -p "${SELFTEST_0C_DIR_1}" || fail "mkdir -p failed for project_refine_parent_view_json self-test dir"
SELFTEST_0C_DIR_2="${WORK_DIR}/selftest-0c-2"
mkdir -p "${SELFTEST_0C_DIR_2}" || fail "mkdir -p failed for project_parent_subissues_view_json (unreadable-fixture) self-test dir"
SELFTEST_0C_DIR_3="${WORK_DIR}/selftest-0c-3"
mkdir -p "${SELFTEST_0C_DIR_3}" || fail "mkdir -p failed for project_parent_subissues_view_json (bogus-mode) self-test dir"

if ( GOLDEN_GRAPH_FIXTURE="/nonexistent/${RANDOM}${RANDOM}.json"; project_refine_parent_view_json "${SELFTEST_0C_DIR_1}" ) >/dev/null 2>&1; then
  fail "project_refine_parent_view_json: an unreadable golden fixture must be rejected, never a vacuous pass"
fi
if ( GOLDEN_GRAPH_FIXTURE="/nonexistent/${RANDOM}${RANDOM}.json"; project_parent_subissues_view_json "${SELFTEST_0C_DIR_2}" open-sibling 102 ) >/dev/null 2>&1; then
  fail "project_parent_subissues_view_json: an unreadable golden fixture must be rejected, never a vacuous pass"
fi
if project_parent_subissues_view_json "${SELFTEST_0C_DIR_3}" bogus-mode 102 >/dev/null 2>&1; then
  fail "project_parent_subissues_view_json: an unrecognized mode must be rejected fail-closed, never silently accepted"
fi

# --- 0d. drive_stage_* reject an unrecognized mode ------------------------

BADMODE_DIR="$(mktemp -d)" || { fail "mktemp -d failed for drive_stage_* bad-mode self-test"; BADMODE_DIR=""; }
if [[ -n "${BADMODE_DIR}" ]]; then
  BADMODE_STREAM="${BADMODE_DIR}/stream.tsv"
  : > "${BADMODE_STREAM}"
  if drive_stage_refine "${BADMODE_DIR}" "${BADMODE_STREAM}" bogus-mode >/dev/null 2>&1; then
    fail "drive_stage_refine: an unrecognized mode must be rejected fail-closed"
  fi
  if drive_stage_escalation "${BADMODE_DIR}" "${BADMODE_STREAM}" bogus-mode >/dev/null 2>&1; then
    fail "drive_stage_escalation: an unrecognized mode must be rejected fail-closed"
  fi
  rm -rf "${BADMODE_DIR}"
fi

# --- 0e. oracle wrapper relay (verify_refine_write_order_via_lib /
# verify_parent_close_reconciliation_via_lib / classify_gh_call_via_lib):
# each must relay its underlying library's real reject-side reason/exit
# code through the subshell boundary, proven against a deliberately-broken
# op sequence -- never the golden fixture's own good sequence, which every
# oracle trivially accepts.
# =====================================================================

reason="$(verify_refine_write_order_via_lib "working claim parent-body refined visual-check")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must precede working" \
  "verify_refine_write_order_via_lib: must relay the underlying library's real rejection reason through the subshell boundary"

reason="$(verify_parent_close_reconciliation_via_lib "close-to-hold" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-ok label-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never cascade" \
  "verify_parent_close_reconciliation_via_lib: must relay the underlying library's real rejection reason through the subshell boundary"

assert_eq "$(classify_gh_call_via_lib 'gh issue transfer 100 --repo o/r --to other/repo')" "unknown" \
  "classify_gh_call_via_lib: an unrecognized gh subcommand must relay as unknown, not vacuously as read"
assert_eq "$(classify_gh_call_via_lib 'gh issue edit 100 --repo o/r --add-label Working')" "write" \
  "classify_gh_call_via_lib: a recognized write call must relay as write"

# --- 0f. non-collision proof: each stage driver actually used its OWN
# library's fixture root, never the other's (the Risks section's mitigation)
# -- proven by a marker unique to each stub's own env-var contract, not by
# inspecting internal variable names.
# =====================================================================

COLLISION_DIR="$(mktemp -d)" || { fail "mktemp -d failed for non-collision self-test"; COLLISION_DIR=""; }
if [[ -n "${COLLISION_DIR}" ]]; then
  COLLISION_STREAM="${COLLISION_DIR}/stream.tsv"
  : > "${COLLISION_STREAM}"

  drive_stage_refine "${COLLISION_DIR}" "${COLLISION_STREAM}" confirmed >/dev/null
  assert_ok "$?" "non-collision self-test: drive_stage_refine(confirmed) must succeed"
  # refine-write-order/fixtures/gh.stub.sh's OWN --jq .number counter
  # behavior (a bare "900+n" line) is the marker: parent-close-reconcile/
  # fixtures/gh.stub.sh has no such branch and would instead fall through to
  # its own generic empty-success case for this exact invocation, so a
  # standalone "9\d+" line anywhere in the captured stdout proves
  # refine-write-order's stub -- not the other library's -- actually served
  # the two `--jq .number` child-create calls (no line in either JSON view
  # blob this replay also emits could ever coincidentally match this bare,
  # unquoted, un-bracketed shape).
  if ! grep -qE '^9[0-9]+$' "${COLLISION_DIR}/refine.log.stdout" 2>/dev/null; then
    fail "non-collision self-test: drive_stage_refine's captured stdout carries no bare 900+n counter line -- want refine-write-order/fixtures/gh.stub.sh's own --jq .number counter shape; proves the wrong stub/fixture root was used"
  fi

  drive_stage_parent_close "${COLLISION_DIR}" "${COLLISION_STREAM}" open-sibling >/dev/null
  assert_ok "$?" "non-collision self-test: drive_stage_parent_close(open-sibling) must succeed"
  # parent-close-reconcile/fixtures/gh.stub.sh's OWN GH_STUB_ISSUE_VIEW_FIXTURE
  # env var is the marker: refine-write-order/fixtures/gh.stub.sh only
  # honors GH_STUB_VIEW_FIXTURE (a DIFFERENT env var name) for the identical
  # "issue view" case, so if the wrong stub had run, this call would have
  # fallen back to its own default "{}" rather than our real projected
  # subIssues content.
  PARENT_CLOSE_STDOUT="$(cat "${COLLISION_DIR}/parent-close-open-sibling.stdout" 2>/dev/null)"
  [[ "${PARENT_CLOSE_STDOUT}" == *'"subIssues"'* ]] || fail "non-collision self-test: drive_stage_parent_close's issue-view call returned [${PARENT_CLOSE_STDOUT}], want the projected subIssues content -- proves the wrong stub/fixture root was used"

  rm -rf "${COLLISION_DIR}"
fi

# =====================================================================
# Section 1 -- fixture<->doc agreement: the golden fixture's writeOrder must
# equal extract_write_order_ops_raw's real extraction from the committed
# docs. Both refine/SKILL.md's "### Write order" section and refine/codex.md's
# parity block already document their canonical op-token example for a
# 2-child split -- exactly the golden fixture's own child count (Q1/Q3), so
# no further child-count expansion is needed here: a fixture carrying a
# different child count would need its own doc-example update to match, and
# THIS assertion is exactly what would catch that drift.
# =====================================================================

require_golden_field GOLDEN_WRITE_ORDER '.writeOrder | join("\n")' "golden fixture writeOrder"
require_extract_write_order_ops SKILL_WRITE_ORDER "${REFINE_SKILL}" "refine/SKILL.md ### Write order section"
require_extract_write_order_ops CODEX_WRITE_ORDER "${REFINE_CODEX}" "refine/codex.md Write order parity block"

if [[ -n "${GOLDEN_WRITE_ORDER:-}" && -n "${SKILL_WRITE_ORDER:-}" ]]; then
  assert_eq "${SKILL_WRITE_ORDER}" "${GOLDEN_WRITE_ORDER}" \
    "fixture<->doc agreement: the golden fixture's writeOrder must equal extract_write_order_ops_raw's real extraction from refine/SKILL.md"
fi
if [[ -n "${GOLDEN_WRITE_ORDER:-}" && -n "${CODEX_WRITE_ORDER:-}" ]]; then
  assert_eq "${CODEX_WRITE_ORDER}" "${GOLDEN_WRITE_ORDER}" \
    "fixture<->doc agreement: the golden fixture's writeOrder must equal extract_write_order_ops_raw's real extraction from refine/codex.md"
fi

# The fixture's own writeOrder must itself satisfy the partial-order oracle
# -- a live regression guard, not just a presence/equality check.
if [[ -n "${GOLDEN_WRITE_ORDER:-}" ]]; then
  reason="$(verify_refine_write_order_via_lib "${GOLDEN_WRITE_ORDER}")"; rc=$?
  assert_ok "${rc}" "the golden fixture's writeOrder must itself satisfy verify_refine_write_order: ${reason}"
fi

# =====================================================================
# Section 2 -- AC2: one ordered recorded gh/git stream spans refine ->
# escalation/resume -> parent-close, asserted on exact argv, call counts,
# and ordering -- never a grep-only doc scan.
# =====================================================================

AC2_DIR="${WORK_DIR}/ac2"
mkdir -p "${AC2_DIR}"
AC2_STREAM="${AC2_DIR}/stream.tsv"
: > "${AC2_STREAM}"

drive_scenario drive_stage_refine "${AC2_DIR}" "${AC2_STREAM}" confirmed
assert_ok "$?" "AC2: drive_stage_refine(confirmed) must replay cleanly"
drive_scenario drive_stage_escalation "${AC2_DIR}" "${AC2_STREAM}" resumed
assert_ok "$?" "AC2: drive_stage_escalation(resumed) must replay cleanly"
drive_scenario drive_stage_parent_close "${AC2_DIR}" "${AC2_STREAM}" open-sibling
assert_ok "$?" "AC2: drive_stage_parent_close(open-sibling) must replay cleanly"

AC2_LINE_COUNT="$(grep -c . "${AC2_STREAM}" 2>/dev/null || echo 0)"
assert_eq "${AC2_LINE_COUNT}" "16" \
  "AC2: the merged stream must carry exactly 12 refine + 3 escalation + 1 parent-close records"

# Ordering: the stage column must transition refine -> escalation ->
# parent-close and never revert (proves "one ordered stream", not merely
# "three streams that happen to get concatenated in the right order").
AC2_STAGE_SEQUENCE="$(cut -f1 "${AC2_STREAM}" | uniq)"
EXPECTED_STAGE_SEQUENCE="$(printf 'refine\nescalation\nparent-close')"
assert_eq "${AC2_STAGE_SEQUENCE}" "${EXPECTED_STAGE_SEQUENCE}" \
  "AC2: the merged stream's stage column must transition refine -> escalation -> parent-close exactly once each, never interleaved or reverted"

# Exact argv/classification: every refine-stage write op classifies write,
# both refine-stage manifest-revalidation reads classify read.
AC2_REFINE_LINES="$(awk -F'\t' '$1 == "refine" {print $2, $3}' "${AC2_STREAM}")"
i=0
while IFS= read -r line; do
  i=$((i + 1))
  cls="$(classify_gh_call_via_lib "${line}")"
  if [[ "${i}" -le 2 ]]; then
    [[ "${cls}" == "read" ]] || fail "AC2: refine-stage line ${i} [${line}] classified ${cls}, want read (manifest revalidation / ownership recheck)"
  else
    [[ "${cls}" == "write" ]] || fail "AC2: refine-stage line ${i} [${line}] classified ${cls}, want write"
  fi
done <<<"${AC2_REFINE_LINES}"

# =====================================================================
# Section 3 -- AC3: declined refinement and every pre-confirmation
# interruption produce zero write-classified commands in the recorded
# stream (classify_gh_call's fail-closed "unknown" counts as a write).
# =====================================================================

AC3_DIR="${WORK_DIR}/ac3"
mkdir -p "${AC3_DIR}"
AC3_STREAM="${AC3_DIR}/stream.tsv"
: > "${AC3_STREAM}"

drive_scenario drive_stage_refine "${AC3_DIR}" "${AC3_STREAM}" declined
assert_ok "$?" "AC3: drive_stage_refine(declined) must replay cleanly (zero commands)"
drive_scenario drive_stage_refine "${AC3_DIR}" "${AC3_STREAM}" interrupted-after-revalidation
assert_ok "$?" "AC3: drive_stage_refine(interrupted-after-revalidation) must replay cleanly"
drive_scenario drive_stage_escalation "${AC3_DIR}" "${AC3_STREAM}" interrupted
assert_ok "$?" "AC3: drive_stage_escalation(interrupted) must replay cleanly (zero commands)"

AC3_LINE_COUNT="$(grep -c . "${AC3_STREAM}" 2>/dev/null || echo 0)"
assert_eq "${AC3_LINE_COUNT}" "1" \
  "AC3: exactly one recorded call is expected across all three pre-confirmation interruption scenarios (the single manifest-revalidation read)"

saw_write=0
while IFS= read -r line; do
  [[ -z "${line}" ]] && continue
  binary="$(printf '%s' "${line}" | cut -f2)"
  argv="$(printf '%s' "${line}" | cut -f3)"
  cls="$(classify_gh_call_via_lib "${binary} ${argv}")"
  [[ "${cls}" == "write" || "${cls}" == "unknown" ]] && saw_write=1
done < "${AC3_STREAM}"
if [[ "${saw_write}" -eq 1 ]]; then
  fail "AC3: declined refinement and every pre-confirmation interruption must produce zero write-classified commands, but a write/unknown-classified call was recorded"
fi

# =====================================================================
# Section 4 -- AC4: parent-audit close<->hold retries reconcile commit
# message, PR body, and closing references, and an open sibling projected
# from the fixture forces hold.
# =====================================================================

GOOD_CLOSE_TO_HOLD="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-ok label-no-cascade stale-inreview-note"
verify_parent_close_reconciliation_via_lib "close-to-hold" "${GOOD_CLOSE_TO_HOLD}"
assert_ok "$?" "AC4: the canonical close-to-hold amend -> push -> body-edit -> verify-exclude -> no-cascade -> stale-note order must be accepted"

GOOD_HOLD_TO_CLOSE="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-cascade"
verify_parent_close_reconciliation_via_lib "hold-to-close" "${GOOD_HOLD_TO_CLOSE}"
assert_ok "$?" "AC4: the canonical hold-to-close amend -> push -> body-edit -> verify-include -> cascade order must be accepted"

AC4_DIR="${WORK_DIR}/ac4"
mkdir -p "${AC4_DIR}"
AC4_STREAM="${AC4_DIR}/stream.tsv"
: > "${AC4_STREAM}"

drive_scenario drive_stage_parent_close "${AC4_DIR}" "${AC4_STREAM}" open-sibling
assert_ok "$?" "AC4: drive_stage_parent_close(open-sibling) must replay cleanly"
OPEN_SIBLING_JSON="${AC4_DIR}/subissues-open-sibling.json"
OPEN_SIBLING_COUNT="$(jq '[.subIssues[] | select(.number != 102 and .state == "OPEN")] | length' "${OPEN_SIBLING_JSON}" 2>/dev/null)"
assert_eq "${OPEN_SIBLING_COUNT}" "1" \
  "AC4: the golden fixture's own post-refinement subIssues (both children still open) must project exactly one open sibling other than childId 102 -- the shape that forces hold"

drive_scenario drive_stage_parent_close "${AC4_DIR}" "${AC4_STREAM}" all-closed
assert_ok "$?" "AC4: drive_stage_parent_close(all-closed) must replay cleanly"
ALL_CLOSED_JSON="${AC4_DIR}/subissues-all-closed.json"
ALL_CLOSED_COUNT="$(jq '[.subIssues[] | select(.number != 102 and .state == "OPEN")] | length' "${ALL_CLOSED_JSON}" 2>/dev/null)"
assert_eq "${ALL_CLOSED_COUNT}" "0" \
  "AC4: the all-closed projection must record zero open siblings other than childId 102 -- allows close"

# =====================================================================
# Section 5 -- pinned SCENARIO_COUNT, a fixed non-vacuity floor: if a
# scenario silently stops being driven (e.g. a future edit drops a
# drive_scenario call), this catches it rather than a shrinking assertion
# count passing vacuously.
# =====================================================================

SCENARIO_COUNT=8
assert_eq "${SCENARIOS_DRIVEN}" "${SCENARIO_COUNT}" \
  "pinned SCENARIO_COUNT: expected exactly ${SCENARIO_COUNT} scenarios actually driven across sections 2-4"

echo "adversarial-chain.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
