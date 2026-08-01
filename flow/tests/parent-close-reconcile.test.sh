#!/usr/bin/env bash
# Behavioral suite for ticket #879, "implement: bind parent-close verdict to
# commit and PR trailers across retries (4/12)". Phase 9's Parent Close Gate
# re-runs its acceptance-criteria audit on every re-entry and may flip its
# verdict, but today's phase-9-pr.md reuses an already-created commit and
# existing PR without reconciling their closing trailers/body against the
# fresh verdict -- a prior `close` verdict can leave `Fixes #<parent>` in
# the commit after a later audit returns `hold`, closing the parent against
# the current fail-closed verdict; the reverse transition can leave a
# now-approved parent unclosed. This suite pins the doc-contract text the
# green phase must add and proves the AC-derived decision oracle those
# additions must satisfy.
#
# Read before writing this suite (per flow/AGENTS.md's Critical Rules and
# this ticket's delegation brief):
#   - flow/tests/parent-close-verification.test.sh (#856) -- the
#     fixture-free, grep-based doc-contract idiom for phase-9-pr.md's
#     existing Parent Close Gate: `set -uo pipefail`, a `failures` counter,
#     assert_file_contains/assert_file_lacks on `grep -qF`, markers kept on
#     a single source line.
#   - flow/tests/refine-write-order.test.sh + its refine-write-order/
#     lib.sh and fixtures/ (#878, sibling 3/12 of #661) -- the
#     behavioral-fixture harness idiom this suite's library follows: pure
#     `*_raw` extractors, `require_*` nameref wrappers, a `drive_*` replay
#     that copies a recording stub into `mktemp -d` and chmod +x's the
#     copy, and a `verify_*` AC-derived partial-order oracle.
#
# phase-9-pr.md is prose, not an executable script, so this suite asserts
# against it two ways: (1) literal doc-contract markers/fenced command
# lines the plan's Files to Modify section specifies almost verbatim --
# guaranteed absent from today's phase-9-pr.md, a genuine red; and (2) a
# pure, doc-independent decision oracle (verify_parent_close_reconciliation)
# validated by its own self-tests against synthetic op-token sequences --
# the green phase's prose must produce behavior this oracle accepts, but
# the oracle's own correctness is provable today without phase-9-pr.md
# existing at all (Test Strategy's "Unit -- catches ordering and zero-write
# invariants a replay alone cannot distinguish from a coincidentally
# correct call log" classification).
#
# Library: flow/tests/parent-close-reconcile/lib.sh (extractors + oracles).
# Fixtures: flow/tests/parent-close-reconcile/fixtures/ (recording `gh` and
# `git` stubs, never committed executable, plus canned JSON responses for
# PR state/body/closing-refs and parent sub-issue snapshots).
#
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob -- no
# registration needed.
#
# Scenario matrix, 1:1 with the ticket's acceptance criteria and the plan's
# Test Strategy table (each row below maps to a Section 2 oracle self-test,
# a Section 3 doc-marker assertion, a Section 4 fixture replay, or a
# combination):
#
#   | # | Scenario                                            | Where asserted |
#   |---|------------------------------------------------------|----------------|
#   | 1 | close -> hold: amend, force-lease push, body edit,   | Section 2 (close-to-hold oracle), Section 3 |
#   |   | closing refs verified excluded, no --parent, stale   |                |
#   |   | In Review note                                       |                |
#   | 2 | hold -> close: amend, body edit, closing refs        | Section 2 (hold-to-close oracle), Section 3 |
#   |   | verified included, --parent cascade fires            |                |
#   | 3 | Unchanged verdict, both directions: zero amend, zero | Section 2 (unchanged-close/-hold oracle) |
#   |   | gh pr edit, zero gh pr create, no empty commit        |                |
#   | 4 | Open native sub-issue forces hold despite             | Section 3 (marker), Section 4a (fixture replay) |
#   |   | isLastChild: true                                     |                |
#   | 5 | Fail-closed matrix + hold/close asymmetry             | Section 2 (hold-unverifiable-removal, |
#   |   |                                                        | close-unverifiable-addition-{verified,unverified}-commit) |
#   | 6 | Failure-after-X re-entry converges, no duplicate      | Section 2 (duplicate-op self-tests) |
#   |   | PR/empty commit                                       |                |
#   | 7 | Merged PR: fail closed, ZERO writes                   | Section 2 (merged oracle), Section 3, Section 4b |
#   | 8 | Non-HEAD stale trailer: stop, no amend                | Section 2 (nonhead oracle), Section 1 (classify_commit_range_trailer) |
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "parent-close-reconcile.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "parent-close-reconcile.test.sh: failed to resolve flow directory." >&2; exit 2; }
LIB_DIR="${SCRIPT_DIR}/parent-close-reconcile"
FIXTURES_DIR="${LIB_DIR}/fixtures"
PHASE9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# shellcheck source=./parent-close-reconcile/lib.sh
if ! source "${LIB_DIR}/lib.sh"; then
  echo "parent-close-reconcile.test.sh: failed to source ${LIB_DIR}/lib.sh" >&2
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
assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
cleanup_replay_log() {
  # $1=log-prefix -- removes a replay_through_stub log set (.gh.log,
  # .git.log, .stdout, .stderr) used at every replay call site below.
  local prefix="$1"
  rm -f "${prefix}.gh.log" "${prefix}.git.log" "${prefix}.stdout" "${prefix}.stderr"
}

# =====================================================================
# Section 1 -- classify_commit_range_trailer self-tests. Pure decision
# logic, no doc/stub dependency: proves the Commit-reconciliation decision
# table (plan's `## Commit` bullet) directly, independent of phase-9-pr.md.
# =====================================================================

RECORDS_AGREE_CLOSE="$(printf 'SHA:sha0\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nFixes #900\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_AGREE_CLOSE}" 900 close)" "agree" \
  "classify_commit_range_trailer: HEAD already carries the correct Fixes trailer on a close verdict -> agree (no-op)"

RECORDS_AGREE_HOLD="$(printf 'SHA:sha0\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nRelated to #900\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_AGREE_HOLD}" 900 hold)" "agree" \
  "classify_commit_range_trailer: HEAD already carries the correct Related to trailer on a hold verdict -> agree (no-op)"

RECORDS_DISAGREE_HEAD="$(printf 'SHA:sha0\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nRelated to #900\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_DISAGREE_HEAD}" 900 close)" "disagree-head" \
  "classify_commit_range_trailer: HEAD carries Related to but verdict is close -> disagree-head (amend case)"

RECORDS_ABSENT="$(printf 'SHA:sha0\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_ABSENT}" 900 close)" "absent" \
  "classify_commit_range_trailer: no parent reference anywhere in range -> absent"

assert_eq "$(classify_commit_range_trailer "${RECORDS_ABSENT}" 900 hold)" "absent" \
  "classify_commit_range_trailer: no parent reference anywhere in range, hold verdict -> absent (phase-9-pr.md's fifth classification bullet: amend HEAD adding 'Related to #<parentId>')"

RECORDS_NONHEAD="$(printf 'SHA:shaHEAD\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nBODY-END\nSHA:shaOLD\nBODY-BEGIN\nchore: bump\n\nFixes #900\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_NONHEAD}" 900 close)" "nonhead:shaOLD" \
  "classify_commit_range_trailer: reference on a non-HEAD (older) commit -> nonhead:<sha>, amend cannot reach it"

RECORDS_MULTIPLE="$(printf 'SHA:shaHEAD\nBODY-BEGIN\nfeat(x): things\n\nFixes #900\nBODY-END\nSHA:shaOLD\nBODY-BEGIN\nchore: bump\n\nRelated to #900\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_MULTIPLE}" 900 close)" "multiple:shaHEAD,shaOLD" \
  "classify_commit_range_trailer: a reference on more than one commit -> multiple:<sha1>,<sha2>, both SHAs reported"

# Reject side (non-vacuity, whole-line precision, docs/shell-scripting-gotchas.md):
# a numeric-prefix collision (#9001 vs #900) must never be treated as a match.
RECORDS_PRECISION="$(printf 'SHA:sha0\nBODY-BEGIN\nfeat(x): things\n\nFixes #9001\nBODY-END\n')"
assert_eq "$(classify_commit_range_trailer "${RECORDS_PRECISION}" 900 close)" "absent" \
  "classify_commit_range_trailer: whole-line precision -- 'Fixes #9001' must never match parentId 900 as a substring"

assert_eq "$(classify_commit_range_trailer "" 900 close)" "absent" \
  "classify_commit_range_trailer: empty records input -> absent, not an error"

reason="$(classify_commit_range_trailer "${RECORDS_ABSENT}" 900 bogus)"; rc=$?
assert_nonzero "${rc}" "classify_commit_range_trailer: an invalid verdict argument must be rejected, not silently treated as close or hold"
[[ "${reason}" == *"invalid verdict"* ]] || fail "classify_commit_range_trailer: invalid-verdict reason should name the problem, got [${reason}]"

# =====================================================================
# Section 2 -- verify_parent_close_reconciliation self-tests. Every
# negative case below is a deliberately broken/reordered COPY of the good
# sequence above it -- never a real doc extraction (docs/adapter-contract.md's
# "Ordering, not just presence" non-vacuity rule; mirrors
# flow/tests/refine-write-order.test.sh's established idiom).
# =====================================================================

# --- 2a. Scenario 7: Merged PR -- zero writes ---------------------------

verify_parent_close_reconciliation "merged" "merged-guard stop-merged"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(merged): the canonical [merged-guard stop-merged] shape must be accepted"

reason="$(verify_parent_close_reconciliation "merged" "merged-guard commit-amend stop-merged")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "zero further writes" \
  "verify_parent_close_reconciliation(merged): any write op after the MERGED read must be rejected"

reason="$(verify_parent_close_reconciliation "merged" "commit-scan merged-guard stop-merged")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "zero further writes" \
  "verify_parent_close_reconciliation(merged): the merged guard must run before any other op, per the plan's 'before the Commit step' ordering assumption"

# --- 2a2. "No PR yet is not a failure" pass-through (Merged-PR Guard) ---

verify_parent_close_reconciliation "no-pr-yet" "merged-guard no-pr-pass-through"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(no-pr-yet): the canonical [merged-guard no-pr-pass-through] shape must be accepted -- proceeds to the Maintenance Gate exactly as if the guard hadn't fired"

reason="$(verify_parent_close_reconciliation "no-pr-yet" "merged-guard stop-fail-closed")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "no-pr-pass-through" \
  "verify_parent_close_reconciliation(no-pr-yet): a 'no PR yet' read must never stop-fail-closed -- it is the ordinary pass-through case, not an ambiguous/merged state"

reason="$(verify_parent_close_reconciliation "no-pr-yet" "merged-guard no-pr-pass-through stop-merged")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "exactly [merged-guard no-pr-pass-through]" \
  "verify_parent_close_reconciliation(no-pr-yet): a 'no PR yet' pass-through must never also record stop-merged"

# --- 2b. Scenario 8: non-HEAD stale trailer -----------------------------

verify_parent_close_reconciliation "nonhead" "commit-scan stop-nonhead"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(nonhead): the canonical [commit-scan stop-nonhead] shape must be accepted"

reason="$(verify_parent_close_reconciliation "nonhead" "commit-scan commit-amend stop-nonhead")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never be amended" \
  "verify_parent_close_reconciliation(nonhead): --amend cannot reach a non-HEAD commit, so an amend op here must be rejected"

reason="$(verify_parent_close_reconciliation "nonhead" "commit-scan")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "stop-nonhead" \
  "verify_parent_close_reconciliation(nonhead): a scan with no stop-nonhead recorded must be rejected"

# --- 2c. Scenario 5: fail-closed matrix + hold/close asymmetry ----------

GOOD_HOLD_UNVERIFIABLE_REMOVAL="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-failed stop-fail-closed"
verify_parent_close_reconciliation "hold-unverifiable-removal" "${GOOD_HOLD_UNVERIFIABLE_REMOVAL}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(hold-unverifiable-removal): an unverifiable removal on a hold verdict must stop fail-closed"

reason="$(verify_parent_close_reconciliation "hold-unverifiable-removal" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-failed stop-fail-closed label-no-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never reach the Labels step" \
  "verify_parent_close_reconciliation(hold-unverifiable-removal): a fail-closed stop must never proceed to Labels, even the 'safe' no-cascade branch"

GOOD_CLOSE_UNREADABLE="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-failed notes-unreadable"
verify_parent_close_reconciliation "close-unverifiable-addition-verified-commit" "${GOOD_CLOSE_UNREADABLE}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(close-unverifiable-addition-verified-commit): a verified commit trailer with an unreadable-after-retry closing reference must proceed and emit notes-unreadable"

reason="$(verify_parent_close_reconciliation "close-unverifiable-addition-verified-commit" "commit-scan commit-amend push push-verify body-read body-edit body-verify closing-verify-include-failed notes-unreadable")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "commit-message channel must be verified" \
  "verify_parent_close_reconciliation(close-unverifiable-addition-verified-commit): notes-unreadable without a prior commit-verify must be rejected -- the degrade-honestly path requires the commit channel to be provably reconciled first"

reason="$(verify_parent_close_reconciliation "close-unverifiable-addition-verified-commit" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-failed stop-fail-closed")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must proceed AND emit" \
  "verify_parent_close_reconciliation(close-unverifiable-addition-verified-commit): a verified commit channel must never hard-stop merely because the closing reference itself is unconfirmed"

reason="$(verify_parent_close_reconciliation "close-unverifiable-addition-verified-commit" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-failed notes-absent")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "notes-absent is the confirmed-absent wording" \
  "verify_parent_close_reconciliation(close-unverifiable-addition-verified-commit): an unreadable-after-retry outcome must never be reported via the confirmed-absent Notes wording"

GOOD_CLOSE_ABSENT="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-failed notes-absent"
verify_parent_close_reconciliation "close-confirmed-absent-verified-commit" "${GOOD_CLOSE_ABSENT}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(close-confirmed-absent-verified-commit): a verified commit trailer with a confirmed-absent closing reference must proceed and emit notes-absent"

reason="$(verify_parent_close_reconciliation "close-confirmed-absent-verified-commit" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-failed notes-unreadable")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "notes-unreadable is the unreadable-after-retry wording" \
  "verify_parent_close_reconciliation(close-confirmed-absent-verified-commit): a confirmed-absent outcome must never be reported via the unreadable-after-retry Notes wording"

GOOD_CLOSE_UNVERIFIED="commit-scan stop-fail-closed"
verify_parent_close_reconciliation "close-unverifiable-addition-unverified-commit" "${GOOD_CLOSE_UNVERIFIED}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(close-unverifiable-addition-unverified-commit): an unverifiable commit channel must hard-stop even on a close verdict"

reason="$(verify_parent_close_reconciliation "close-unverifiable-addition-unverified-commit" "commit-scan notes-unreadable")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must hard-stop" \
  "verify_parent_close_reconciliation(close-unverifiable-addition-unverified-commit): must never claim either honest-degrade Notes line when the commit channel itself was never verified"

# --- 2c2. Push step: an unreadable remote-tip verification (distinct from
# a disagreement) must stop fail closed before any further write ---------

GOOD_PUSH_VERIFY_UNREADABLE="commit-scan commit-amend commit-verify push push-verify-failed stop-fail-closed"
verify_parent_close_reconciliation "push-verify-unreadable" "${GOOD_PUSH_VERIFY_UNREADABLE}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(push-verify-unreadable): a failed fetch or unreadable FETCH_HEAD log must stop fail closed, the same as a disagreement"

reason="$(verify_parent_close_reconciliation "push-verify-unreadable" "commit-scan commit-amend commit-verify push push-verify-failed body-read body-edit stop-fail-closed")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "no further writes" \
  "verify_parent_close_reconciliation(push-verify-unreadable): an unreadable remote-tip verification must never proceed to any further write (body edit, closing-ref checks, labels) before stopping"

reason="$(verify_parent_close_reconciliation "push-verify-unreadable" "commit-scan commit-amend commit-verify push push-verify-failed")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "never treat an unverifiable remote read as an implicit pass" \
  "verify_parent_close_reconciliation(push-verify-unreadable): an unreadable remote-tip read with no recorded stop-fail-closed must be rejected -- never treat it as an implicit pass"

# --- 2d. Scenario 1: close -> hold ---------------------------------------

GOOD_CLOSE_TO_HOLD="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-ok label-no-cascade stale-inreview-note"
verify_parent_close_reconciliation "close-to-hold" "${GOOD_CLOSE_TO_HOLD}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(close-to-hold): the canonical amend -> push -> body-edit -> verify-exclude -> no-cascade -> stale-note order must be accepted"

reason="$(verify_parent_close_reconciliation "close-to-hold" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-exclude-ok label-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never cascade" \
  "verify_parent_close_reconciliation(close-to-hold): a close-to-hold transition must never pass --parent"

reason="$(verify_parent_close_reconciliation "close-to-hold" "commit-scan commit-amend commit-verify push push-verify closing-verify-exclude-ok body-read body-edit body-verify label-no-cascade stale-inreview-note")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must precede" \
  "verify_parent_close_reconciliation(close-to-hold): closing-ref verification before the body edit it depends on must be rejected"

reason="$(verify_parent_close_reconciliation "close-to-hold" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-no-cascade stale-inreview-note")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must verify EXCLUSION" \
  "verify_parent_close_reconciliation(close-to-hold): must verify the parent's removal from closingIssuesReferences, not its inclusion"

# --- 2e. Scenario 2: hold -> close ---------------------------------------

GOOD_HOLD_TO_CLOSE="commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-cascade"
verify_parent_close_reconciliation "hold-to-close" "${GOOD_HOLD_TO_CLOSE}"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(hold-to-close): the canonical amend -> push -> body-edit -> verify-include -> cascade order must be accepted"

reason="$(verify_parent_close_reconciliation "hold-to-close" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-no-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must cascade" \
  "verify_parent_close_reconciliation(hold-to-close): a hold-to-close transition must cascade --parent, never omit it"

reason="$(verify_parent_close_reconciliation "hold-to-close" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-cascade stale-inreview-note")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must not emit the stale In Review note" \
  "verify_parent_close_reconciliation(hold-to-close): nothing was cascaded on the prior hold, so there is nothing stale to report"

# --- 2f. Scenario 3: unchanged verdict, both directions ------------------

verify_parent_close_reconciliation "unchanged-close" "commit-scan commit-noop"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(unchanged-close): a re-entry that finds the trailer already agreeing must be accepted as a zero-write no-op"

verify_parent_close_reconciliation "unchanged-hold" "commit-scan commit-noop"; rc=$?
assert_ok "${rc}" "verify_parent_close_reconciliation(unchanged-hold): a re-entry that finds the trailer already agreeing must be accepted as a zero-write no-op"

reason="$(verify_parent_close_reconciliation "unchanged-close" "commit-scan commit-amend commit-noop")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never amend" \
  "verify_parent_close_reconciliation(unchanged-close): an unchanged verdict must never amend the commit"

reason="$(verify_parent_close_reconciliation "unchanged-hold" "commit-scan commit-amend body-edit")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must never amend" \
  "verify_parent_close_reconciliation(unchanged-hold): an unchanged verdict must never amend or edit the PR body"

reason="$(verify_parent_close_reconciliation "unchanged-close" "commit-scan")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "must record commit-noop" \
  "verify_parent_close_reconciliation(unchanged-close): a scan with no recorded outcome must be rejected -- the idempotent no-op is itself an assertable fact, not an absence of assertions"

# --- 2g. Scenario 6: failure-after-X re-entry converges, no duplicates --
# A naive re-entry that re-applies a mutating step unconditionally instead
# of first checking whether it already happened would double-amend,
# double-push, or double-cascade -- verify_parent_close_reconciliation's
# generic duplicate-op detection (Section 2's shared token-uniqueness pass)
# is exactly the invariant that catches this.

reason="$(verify_parent_close_reconciliation "close-to-hold" "commit-scan commit-amend commit-verify commit-amend push push-verify body-read body-edit body-verify closing-verify-exclude-ok label-no-cascade stale-inreview-note")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "duplicate op: commit-amend" \
  "verify_parent_close_reconciliation: a failure-after-commit re-entry must never double-amend (scenario 6)"

reason="$(verify_parent_close_reconciliation "hold-to-close" "commit-scan commit-amend commit-verify push push push-verify body-read body-edit body-verify closing-verify-include-ok label-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "duplicate op: push" \
  "verify_parent_close_reconciliation: a failure-after-push re-entry must never double-push (scenario 6)"

reason="$(verify_parent_close_reconciliation "hold-to-close" "commit-scan commit-amend commit-verify push push-verify body-read body-edit body-verify closing-verify-include-ok label-cascade label-cascade")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "duplicate op: label-cascade" \
  "verify_parent_close_reconciliation: a failure-after-parent-cascade re-entry must never double-cascade the parent label (scenario 6)"

# --- 2h. Reject side (non-vacuity): unknown scenario ---------------------

reason="$(verify_parent_close_reconciliation "bogus-scenario" "")"; rc=$?
assert_reason_contains "${rc}" "${reason}" "unknown scenario" \
  "verify_parent_close_reconciliation: an unrecognized scenario key must be rejected fail-closed, never silently accepted"

# =====================================================================
# Section 3 -- doc-contract markers/fenced commands against the real
# committed phase-9-pr.md. Every one of these is expected absent from
# today's file (verified before writing this suite) -- a genuine red until
# #879's green phase lands the corresponding prose, per the plan's Files to
# Modify section.
# =====================================================================

ATOMICITY_RECONCILE_MARKER='reconcile every externally effective parent-closing reference'
assert_file_contains "${PHASE9}" "${ATOMICITY_RECONCILE_MARKER}" \
  "the Atomicity rule must require every re-entry to reconcile externally effective parent-closing references, not just restart the build/test/lint pipeline"

SUBISSUE_RECHECK_STALE_MARKER='stale `isLastChild: true` plan front matter is never sufficient'
assert_file_contains "${PHASE9}" "${SUBISSUE_RECHECK_STALE_MARKER}" \
  "the gate must state that stale isLastChild front matter alone can never grant close (scenario 4)"

AUDIT_RECORD_PATH_MARKER='cenci-<ticket-id-or-slug>-parent-audit.md'
assert_file_contains "${PHASE9}" "${AUDIT_RECORD_PATH_MARKER}" \
  "the gate must persist a ticket-scoped audit record at cenci-<ticket-id-or-slug>-parent-audit.md"

AUDIT_FAILCLOSED_MARKER='A failed write or read-back forces `hold`'
assert_file_contains "${PHASE9}" "${AUDIT_FAILCLOSED_MARKER}" \
  "an unreadable/unwritable audit record must fail closed to hold (scenario 5)"

MERGED_GUARD_HEADING_MARKER='## Merged-PR Guard (last child only)'
assert_file_contains "${PHASE9}" "${MERGED_GUARD_HEADING_MARKER}" \
  "a new Merged-PR Guard section must exist, placed after the gate and before the Maintenance Gate (scenario 7)"

MERGED_GUARD_NO_PROTECT_MARKER='do not claim the parent is protected'
assert_file_contains "${PHASE9}" "${MERGED_GUARD_NO_PROTECT_MARKER}" \
  "the merged-PR guard (and the hold-direction closing-ref check) must never claim the parent is protected on a stop it cannot verify"

NO_PR_YET_MARKER='No PR yet is not a failure.'
assert_file_contains "${PHASE9}" "${NO_PR_YET_MARKER}" \
  "the Merged-PR Guard must distinguish an ordinary first-time-entry 'no PR yet' read (pass through) from a genuinely unreadable/merged state (fail closed)"

PUSH_VERIFY_FAILCLOSED_MARKER='never treat an unverifiable remote read as an implicit pass'
assert_file_contains "${PHASE9}" "${PUSH_VERIFY_FAILCLOSED_MARKER}" \
  "the Push step must state that a failed fetch or unreadable FETCH_HEAD log stops fail closed, the same as a disagreement -- never an implicit pass"

COMMIT_NONHEAD_MARKER="\`--amend\` cannot reach it"
assert_file_contains "${PHASE9}" "${COMMIT_NONHEAD_MARKER}" \
  "the Commit step must document that a non-HEAD stale trailer cannot be reached by --amend and must fail closed (scenario 8)"

INDEPENDENT_CHANNELS_MARKER='independently effective** closure channels'
assert_file_contains "${PHASE9}" "${INDEPENDENT_CHANNELS_MARKER}" \
  "the PR step must state that commit-message text and PR-body/closing-refs are independently effective closure channels, never inferred from each other"

CLOSING_REF_CLOSE_DEGRADE_MARKER='closing reference unverified (GitHub did not report it as a closing issue; the commit trailer is present) — verify before merge'
assert_file_contains "${PHASE9}" "${CLOSING_REF_CLOSE_DEGRADE_MARKER}" \
  "the close-verdict degrade-honestly Notes line must appear verbatim when closingIssuesReferences still lacks the parent after retry but the commit trailer is verified (scenario 5 asymmetry)"

CLOSING_REF_CLOSE_UNREADABLE_MARKER='closing reference could not be verified (GitHub'"'"'s closing-reference record could not be read; the commit trailer is present) — verify before merge'
assert_file_contains "${PHASE9}" "${CLOSING_REF_CLOSE_UNREADABLE_MARKER}" \
  "the close-verdict degrade-honestly Notes line must appear verbatim, with distinct wording, when the retried closingIssuesReferences read itself remained unreadable (as opposed to a successful read confirming absence)"

STALE_INREVIEW_MARKER='no CLI transition that removes it'
assert_file_contains "${PHASE9}" "${STALE_INREVIEW_MARKER}" \
  "the Labels step must note that a close -> hold transition cannot un-cascade a stale In Review label on the parent (scenario 1)"

CLEANUP_AUDIT_CLEANUP_MARKER='parent-audit.md'
assert_file_contains "${PHASE9}" "${CLEANUP_AUDIT_CLEANUP_MARKER}" \
  "the Cleanup step's rm -f list must remove the parent-audit temp file"

# --- Fenced command-line markers (fence-aware extraction via
# extract_fenced_line_raw / require_extract_fenced_line -- never invoked
# through $(...), per read-helper-purity-contract.test.sh's contract). ---

require_extract_fenced_line MERGED_GUARD_CMD "${PHASE9}" \
  'gh pr view "<branch>" --repo <owner>/<repo> --json state,mergedAt,url' \
  "Merged-PR Guard's state/mergedAt/url read command (quoted branch, per security review item 5; url field, per review item 2)"

require_extract_fenced_line COMMIT_SCAN_CMD "${PHASE9}" \
  "git -C <worktree-path> log origin/main..HEAD --format=%H%x00%B" \
  "Commit step's branch-range parent-trailer scan command"

require_extract_fenced_line COMMIT_AMEND_CMD "${PHASE9}" \
  'git -C <worktree-path> commit --amend -F ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-amend-msg.txt' \
  "Commit step's re-entry amend command -- must use -F <file>, never -m with an interpolated message (security review item 4)"

require_extract_fenced_line PUSH_VERIFY_FETCH_CMD "${PHASE9}" \
  'git -C <worktree-path> fetch origin "<branch>"' \
  "Push step's post-push remote-tip verification fetch command (quoted branch, per security review item 5)"

require_extract_fenced_line PUSH_VERIFY_LOG_CMD "${PHASE9}" \
  "git -C <worktree-path> log -1 FETCH_HEAD --format=%B" \
  "Push step's post-push remote-tip verification read command"

require_extract_fenced_line BODY_EDIT_CMD "${PHASE9}" \
  'gh pr edit "<branch>" --repo <owner>/<repo> --body-file' \
  "PR step's targeted body-edit command (quoted branch, per security review item 5)"

require_extract_fenced_line CLOSING_REF_CMD "${PHASE9}" \
  'gh pr view "<branch>" --repo <owner>/<repo> --json closingIssuesReferences --jq '"'"'[.closingIssuesReferences[].number]'"'"'' \
  "PR step's closing-issue-reference verification command (quoted branch, per security review item 5)"

NO_AMEND_M_MARKER='Never `git -C <worktree-path> commit --amend -m` with the corrected message interpolated inline'
assert_file_contains "${PHASE9}" "${NO_AMEND_M_MARKER}" \
  "the Commit step must explicitly forbid the unsafe -m form, never just show the safe -F form in isolation (security review item 4)"

# =====================================================================
# Section 4 -- fixture-driven replay proofs: real committed JSON fixtures
# driven through the recording gh/git stubs (never pattern-matched doc
# text), proving the fixtures themselves are non-vacuous and that the
# plumbing this suite's harness depends on actually works.
# =====================================================================

# --- 4a. Scenario 4: open native sub-issue recheck -----------------------

SUBISSUE_LOG="$(mktemp)" || { fail "mktemp failed for subissue replay log"; SUBISSUE_LOG=""; }
if [[ -n "${SUBISSUE_LOG}" ]]; then
  GH_STUB_ISSUE_VIEW_FIXTURE="${FIXTURES_DIR}/parent-subissues-open-sibling.json" \
    replay_through_stub "gh issue view <parentId> --repo <owner>/<repo> --json subIssues" "${SUBISSUE_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the sub-issue recheck against parent-subissues-open-sibling.json must succeed against the gh stub"
  OPEN_SIBLING_COUNT="$(jq '[.subIssues[] | select(.number != 800 and .state == "OPEN")] | length' "${SUBISSUE_LOG}.stdout" 2>/dev/null)"
  assert_eq "${OPEN_SIBLING_COUNT}" "1" \
    "fixtures/parent-subissues-open-sibling.json must record exactly one open sibling other than childId 800 -- the shape that forces hold even when isLastChild is true"
  cleanup_replay_log "${SUBISSUE_LOG}"
fi

ALLCLOSED_LOG="$(mktemp)" || { fail "mktemp failed for all-closed subissue replay log"; ALLCLOSED_LOG=""; }
if [[ -n "${ALLCLOSED_LOG}" ]]; then
  GH_STUB_ISSUE_VIEW_FIXTURE="${FIXTURES_DIR}/parent-subissues-all-closed.json" \
    replay_through_stub "gh issue view <parentId> --repo <owner>/<repo> --json subIssues" "${ALLCLOSED_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the sub-issue recheck against parent-subissues-all-closed.json must succeed against the gh stub"
  OPEN_SIBLING_COUNT="$(jq '[.subIssues[] | select(.number != 800 and .state == "OPEN")] | length' "${ALLCLOSED_LOG}.stdout" 2>/dev/null)"
  assert_eq "${OPEN_SIBLING_COUNT}" "0" \
    "fixtures/parent-subissues-all-closed.json must record zero open siblings other than childId 800 (the run's own still-open child is correctly excluded)"
  cleanup_replay_log "${ALLCLOSED_LOG}"
fi

# --- 4b. Scenario 7: merged-PR guard read ---------------------------------

MERGED_LOG="$(mktemp)" || { fail "mktemp failed for merged-PR replay log"; MERGED_LOG=""; }
if [[ -n "${MERGED_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/pr-merged.json" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json state,mergedAt" "${MERGED_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the merged-PR guard read against pr-merged.json must succeed against the gh stub"
  PR_STATE="$(jq -r '.state' "${MERGED_LOG}.stdout" 2>/dev/null)"
  assert_eq "${PR_STATE}" "MERGED" \
    "fixtures/pr-merged.json must report state MERGED -- the shape that must stop the run before any commit/push/body write"
  cleanup_replay_log "${MERGED_LOG}"
fi

# --- 4b2. Merged-PR Guard: "No PR yet is not a failure" pass-through -----

NOPRYET_LOG="$(mktemp)" || { fail "mktemp failed for no-pr-yet replay log"; NOPRYET_LOG=""; }
if [[ -n "${NOPRYET_LOG}" ]]; then
  GH_STUB_NO_PR_YET=1 \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json state,mergedAt,url" "${NOPRYET_LOG}"
  rc=$?
  assert_nonzero "${rc}" "replaying the Merged-PR Guard read with GH_STUB_NO_PR_YET=1 must fail the gh invocation itself -- the phase's pass-through is a doc-level interpretation of this specific failure text, not a stub-level success"
  grep -qiF "no pull requests found" "${NOPRYET_LOG}.stderr" || \
    fail "GH_STUB_NO_PR_YET=1 must emit literal 'no pull requests found' text on stderr for a pr view call -- the exact case-insensitive signal phase-9-pr.md's Merged-PR Guard matches to treat this as pass-through, not an ambiguous/merged stop"
  cleanup_replay_log "${NOPRYET_LOG}"
fi

# --- 4c. Scenario 1/2: closing-reference verification, both directions ---

WITH_PARENT_LOG="$(mktemp)" || { fail "mktemp failed for closing-refs-with-parent replay log"; WITH_PARENT_LOG=""; }
if [[ -n "${WITH_PARENT_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/closing-refs-with-parent.json" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json closingIssuesReferences" "${WITH_PARENT_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the closing-refs check against closing-refs-with-parent.json must succeed"
  HAS_PARENT="$(jq '[.closingIssuesReferences[].number] | index(900) != null' "${WITH_PARENT_LOG}.stdout" 2>/dev/null)"
  assert_eq "${HAS_PARENT}" "true" \
    "fixtures/closing-refs-with-parent.json must include parentId 900 -- the shape verify_parent_close_reconciliation's hold-to-close scenario requires"
  cleanup_replay_log "${WITH_PARENT_LOG}"
fi

WITHOUT_PARENT_LOG="$(mktemp)" || { fail "mktemp failed for closing-refs-without-parent replay log"; WITHOUT_PARENT_LOG=""; }
if [[ -n "${WITHOUT_PARENT_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/closing-refs-without-parent.json" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json closingIssuesReferences" "${WITHOUT_PARENT_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the closing-refs check against closing-refs-without-parent.json must succeed"
  HAS_PARENT="$(jq '[.closingIssuesReferences[].number] | index(900) != null' "${WITHOUT_PARENT_LOG}.stdout" 2>/dev/null)"
  assert_eq "${HAS_PARENT}" "false" \
    "fixtures/closing-refs-without-parent.json must exclude parentId 900 -- the shape verify_parent_close_reconciliation's close-to-hold scenario requires"
  cleanup_replay_log "${WITHOUT_PARENT_LOG}"
fi

# --- 4d. Fixture sanity: PR body fixtures actually record different
# parent trailers, or the close-to-hold/hold-to-close scenarios above
# would be vacuous. --------------------------------------------------------

FIXES_LOG="$(mktemp)" || { fail "mktemp failed for pr-open-fixes-parent replay log"; FIXES_LOG=""; }
if [[ -n "${FIXES_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/pr-open-fixes-parent.json" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json body" "${FIXES_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the PR body read against pr-open-fixes-parent.json must succeed"
  BODY_TEXT="$(jq -r '.body' "${FIXES_LOG}.stdout" 2>/dev/null)"
  [[ "${BODY_TEXT}" == *"Fixes #900"* ]] || fail "fixtures/pr-open-fixes-parent.json body must contain the literal line 'Fixes #900'"
  [[ "${BODY_TEXT}" != *"Related to #900"* ]] || fail "fixtures/pr-open-fixes-parent.json body must not also contain 'Related to #900' -- fixture would be ambiguous"
  cleanup_replay_log "${FIXES_LOG}"
fi

RELATED_LOG="$(mktemp)" || { fail "mktemp failed for pr-open-related-parent replay log"; RELATED_LOG=""; }
if [[ -n "${RELATED_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/pr-open-related-parent.json" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json body" "${RELATED_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the PR body read against pr-open-related-parent.json must succeed"
  BODY_TEXT="$(jq -r '.body' "${RELATED_LOG}.stdout" 2>/dev/null)"
  [[ "${BODY_TEXT}" == *"Related to #900"* ]] || fail "fixtures/pr-open-related-parent.json body must contain the literal line 'Related to #900'"
  [[ "${BODY_TEXT}" != *"Fixes #900"* ]] || fail "fixtures/pr-open-related-parent.json body must not also contain 'Fixes #900' -- fixture would be ambiguous"
  cleanup_replay_log "${RELATED_LOG}"
fi

# --- 4e. Failure injection: an unreadable closing-refs read must be
# reported as a failure, never silently swallowed. -------------------------

FAIL_LOG="$(mktemp)" || { fail "mktemp failed for closing-refs failure-injection replay log"; FAIL_LOG=""; }
if [[ -n "${FAIL_LOG}" ]]; then
  GH_STUB_PR_VIEW_FIXTURE="${FIXTURES_DIR}/closing-refs-with-parent.json" \
    GH_STUB_FAIL_PATTERN="closingIssuesReferences" \
    replay_through_stub "gh pr view <branch> --repo <owner>/<repo> --json closingIssuesReferences" "${FAIL_LOG}"
  rc=$?
  assert_nonzero "${rc}" "replay_through_stub: an injected GH_STUB_FAIL_PATTERN match on the closing-refs read must fail the replay"
  attempt_count="$(grep -c . "${FAIL_LOG}.gh.log" 2>/dev/null || echo 0)"
  assert_eq "${attempt_count}" "1" \
    "replay_through_stub: exactly one gh invocation must be logged for the single failed attempt"
  cleanup_replay_log "${FAIL_LOG}"
fi

# --- 4f. Scenario 8: non-HEAD stale trailer, driven through the git stub
# end-to-end (stub -> stdout capture -> classify_commit_range_trailer),
# rather than only the synthetic Section 1 unit test. ----------------------

NONHEAD_LOG="$(mktemp)" || { fail "mktemp failed for non-HEAD git-log replay log"; NONHEAD_LOG=""; }
if [[ -n "${NONHEAD_LOG}" ]]; then
  RANGE_TEXT="$(printf 'SHA:shaHEAD\nBODY-BEGIN\nfeat(x): things\n\nFixes #800\nBODY-END\nSHA:shaOLD\nBODY-BEGIN\nchore: bump\n\nFixes #900\nBODY-END\n')"
  GIT_STUB_LOG_RANGE_OUTPUT="${RANGE_TEXT}" \
    replay_through_stub "git -C <worktree-path> log origin/main..HEAD --format=%H%x00%B" "${NONHEAD_LOG}"
  rc=$?
  assert_ok "${rc}" "replaying the branch-range scan against a synthetic non-HEAD-reference range must succeed against the git stub"
  REPLAYED_RANGE="$(cat "${NONHEAD_LOG}.stdout" 2>/dev/null)"
  CLASSIFICATION="$(classify_commit_range_trailer "${REPLAYED_RANGE}" 900 close)"
  assert_eq "${CLASSIFICATION}" "nonhead:shaOLD" \
    "end-to-end: the git stub's captured stdout, fed back through classify_commit_range_trailer, must classify as nonhead:shaOLD -- the fail-closed stop case (scenario 8)"
  cleanup_replay_log "${NONHEAD_LOG}"
fi

# =====================================================================
# Section 5 -- extract_fenced_line_raw non-vacuity self-test (mirrors
# flow/tests/refine-write-order.test.sh's established idiom): proven
# against a deliberately-injected synthetic line in a COPY of phase-9-pr.md
# -- built fresh under mktemp -d, never touching the real committed file --
# not the real doc.
# =====================================================================

SELFTEST_DIR="$(mktemp -d)" || { fail "mktemp -d failed for extract_fenced_line_raw non-vacuity self-test"; SELFTEST_DIR=""; }
if [[ -n "${SELFTEST_DIR}" ]]; then
  INJECTED_COPY="${SELFTEST_DIR}/phase-9-pr-injected.md"
  {
    cat "${PHASE9}"
    printf '\n```bash\ngh pr view <branch> --repo <owner>/<repo> --json state,mergedAt\n```\n'
  } > "${INJECTED_COPY}"

  if injected_out="$(extract_fenced_line_raw "${INJECTED_COPY}" "gh pr view <branch> --repo <owner>/<repo> --json state,mergedAt")"; then
    [[ "${injected_out}" == "gh pr view <branch> --repo <owner>/<repo> --json state,mergedAt" ]] || \
      fail "extract_fenced_line_raw non-vacuity self-test: extracted line did not match the injected text verbatim: [${injected_out}]"
  else
    fail "extract_fenced_line_raw non-vacuity self-test: a synthetic fenced command line injected into a COPY of phase-9-pr.md was NOT captured -- the extractor is vacuous"
  fi

  if extract_fenced_line_raw "${INJECTED_COPY}" "this line does not exist anywhere in the doc" >/dev/null; then
    fail "extract_fenced_line_raw non-vacuity self-test: a substring that is genuinely absent must not be reported as found"
  fi

  rm -rf "${SELFTEST_DIR}"
fi

# Reject side: an unreadable doc path must fail closed (return 1, no
# output), never a vacuous empty-match pass.
if extract_fenced_line_raw "/nonexistent/path/${RANDOM}${RANDOM}.md" "anything" >/dev/null 2>&1; then
  fail "extract_fenced_line_raw: an unreadable/nonexistent file must return failure, not a vacuous success"
fi

echo "parent-close-reconcile.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
