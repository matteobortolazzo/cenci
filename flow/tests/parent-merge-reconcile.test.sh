#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #811: merge-time
# split-parent reconciliation in `cenci babysit`. The prose in the six
# affected flow/docs sites must describe merge-time reconciliation from the
# live native parent/sub-issue graph *alongside* the retained #856/#858
# `hold` guarantee (a recorded `parent-gap-report` marker defers babysit's
# auto-close) — babysit detects the marker, it never adjudicates or re-runs
# the Parent Close Gate audit itself (see the plan's Q&A item 10).
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/parent-close-verification.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers
# built on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob — no registration needed. No
# `read_*` helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan.
#
# Markers are exact replacement-site substrings actually written into the
# six prose files, not generic keywords (docs/shell-scripting-gotchas.md's
# precision-on-both-ends rule) — each positive marker is a sentence
# fragment that exists only once this ticket's prose is written, and the
# one negative marker is the exact retired sentence phase-9-pr.md's
# comment-failure branch used to carry.
#
# Covered files:
#   - flow/skills/babysit/SKILL.md (Safety guarantees)
#   - flow/skills/implement/phases/phase-9-pr.md (Parent Close Gate step 4,
#     the comment-failure sentence, and the Labels closing paragraph)
#   - flow/docs/comment-attribution.md (Consumers column + cross-project
#     contract paragraph)
#   - flow/README.md (Babysitting-a-PR tick list + closing line, Board
#     lifecycle Implemented row, ticket-splitting paragraph)
#   - flow/skills/implement/SKILL.md (Last child ≠ parent complete note)
#   - docs/orchestration.md (lifecycle table row + Implemented paragraph)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "parent-merge-reconcile.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "parent-merge-reconcile.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "parent-merge-reconcile.test.sh: failed to resolve repo directory." >&2; exit 2; }

BABYSIT_SKILL="${FLOW_DIR}/skills/babysit/SKILL.md"
PHASE9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
COMMENT_ATTRIBUTION="${FLOW_DIR}/docs/comment-attribution.md"
FLOW_README="${FLOW_DIR}/README.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
ORCHESTRATION_DOC="${REPO_DIR}/docs/orchestration.md"

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

# =====================================================================
# Markers (single source line each, per docs/shell-scripting-gotchas.md).
# =====================================================================

BABYSIT_SKILL_MARKER='reconciles split-parent completion at merge'
BABYSIT_SKILL_REMEDIATION_MARKER='A stale gap report holds the parent'

PHASE9_LOAD_BEARING_MARKER='This gap-report comment is load-bearing beyond visibility'
PHASE9_AUTOCLOSE_MARKER='babysit` will close the parent on the next merge tick'
PHASE9_RETIRED_VISIBILITY_MARKER='the comment is only its visibility)'
PHASE9_LABELS_MARKER="The parent's real completion is reconciled by \`cenci babysit\` at merge time, not decided here"

COMMENT_ATTRIBUTION_CONSUMER_MARKER='`watch/internal/babysit` (merge-time parent-close gate)'
COMMENT_ATTRIBUTION_CONTRACT_MARKER='the first kind read *by identity* by a non-flow consumer'

README_STEP1_MARKER='reads closed, the parent closes and relabels to `Implemented` too — unless its comment thread'
README_CLOSING_MARKER='deferring to any recorded `parent-gap-report` marker rather than auto-closing over it'
README_LIFECYCLE_ROW_MARKER='including split-parent reconciliation from the live sub-issue graph'
README_SPLIT_PARA_MARKER="parent completion itself is reconciled by \`cenci babysit\` at merge time from the live native sub-issue graph"

IMPLEMENT_SKILL_MARKER="\`cenci babysit\` reconciles split-parent completion at merge time from the live native sub-issue graph"

ORCH_ROW_MARKER='including merge-time split-parent reconciliation'
ORCH_PARA_MARKER='babysit separately reconciles split-parent completion at merge'

# --- flow/skills/babysit/SKILL.md — Safety guarantees ----------------------

assert_file_contains "${BABYSIT_SKILL}" "${BABYSIT_SKILL_MARKER}" \
  "must describe merge-time parent reconciliation from the live native graph"
assert_file_contains "${BABYSIT_SKILL}" "${BABYSIT_SKILL_REMEDIATION_MARKER}" \
  "must state the human-remediation note for a stale gap report"

# --- phase-9-pr.md — the three #811 sites -----------------------------------

assert_file_contains "${PHASE9}" "${PHASE9_LOAD_BEARING_MARKER}" \
  "must state the gap-report comment is load-bearing beyond visibility (step 4)"
assert_file_contains "${PHASE9}" "${PHASE9_AUTOCLOSE_MARKER}" \
  "must say a failed gap-report comment means babysit auto-closes on the next merge tick"
assert_file_lacks "${PHASE9}" "${PHASE9_RETIRED_VISIBILITY_MARKER}" \
  "must retire the now-inaccurate 'the comment is only its visibility' sentence"
assert_file_contains "${PHASE9}" "${PHASE9_LABELS_MARKER}" \
  "must state parent completion is reconciled by babysit at merge time, not by isLastChild"

# --- flow/docs/comment-attribution.md — Consumers column -------------------

assert_file_contains "${COMMENT_ATTRIBUTION}" "${COMMENT_ATTRIBUTION_CONSUMER_MARKER}" \
  "must name watch/internal/babysit as a parent-gap-report consumer"
assert_file_contains "${COMMENT_ATTRIBUTION}" "${COMMENT_ATTRIBUTION_CONTRACT_MARKER}" \
  "must state parent-gap-report is the first kind read by identity by a non-flow consumer"

# --- flow/README.md — four sites --------------------------------------------

assert_file_contains "${FLOW_README}" "${README_STEP1_MARKER}" \
  "must describe merge-time parent reconciliation in the tick list's step 1"
assert_file_contains "${FLOW_README}" "${README_CLOSING_MARKER}" \
  "must describe deferring to a recorded gap-report marker in the babysit section's closing line"
assert_file_contains "${FLOW_README}" "${README_LIFECYCLE_ROW_MARKER}" \
  "must describe split-parent reconciliation in the Board lifecycle Implemented row"
assert_file_contains "${FLOW_README}" "${README_SPLIT_PARA_MARKER}" \
  "must state parent completion is reconciled by babysit at merge time in the ticket-splitting paragraph"

# --- flow/skills/implement/SKILL.md — Last child ≠ parent complete ---------

assert_file_contains "${IMPLEMENT_SKILL}" "${IMPLEMENT_SKILL_MARKER}" \
  "must mention babysit's merge-time reconciliation in the parent-child edge-case note"

# --- docs/orchestration.md — lifecycle row + Implemented paragraph ---------

assert_file_contains "${ORCHESTRATION_DOC}" "${ORCH_ROW_MARKER}" \
  "must describe merge-time split-parent reconciliation in the lifecycle table row"
assert_file_contains "${ORCHESTRATION_DOC}" "${ORCH_PARA_MARKER}" \
  "must describe merge-time split-parent reconciliation in the Implemented paragraph"

echo "parent-merge-reconcile.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
