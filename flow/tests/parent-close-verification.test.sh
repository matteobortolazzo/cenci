#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #856: phase 9's Parent
# Close Gate — the last-child parent-closing trailer (`Fixes #<parentId>`)
# must be gated on a completed audit of the parent's `### Acceptance
# Criteria` against delivered evidence, fail-closed on gaps, with the
# downgrade path (Related-to trailer, no --parent cascade, gap comment on
# the parent) fully specified, and the old unconditional last-child close
# rule retired from phase-9-pr.md, flow/README.md, and implement/SKILL.md.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/refine-automerge-grant.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers
# built on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob — no registration needed. No
# `read_*` helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan.
#
# Markers are exact replacement-site substrings, not generic keywords
# (docs/shell-scripting-gotchas.md's precision-on-both-ends rule):
# `isLastChild`, `Fixes #<parentId>`, and `--parent <parentId>` all already
# appear in phase-9-pr.md for the retired unconditional behavior, so each
# positive marker below is a sentence fragment that exists only once the
# gate is written, and each negative marker is the exact retired sentence.
#
# Covered files:
#   - flow/skills/implement/phases/phase-9-pr.md (the gate + branch wiring)
#   - flow/skills/implement/SKILL.md (parent-child edge-case note)
#   - flow/README.md (ticket-splitting paragraph)
#   - docs/orchestration.md (repo root — lifecycle prose)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "parent-close-verification.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "parent-close-verification.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "parent-close-verification.test.sh: failed to resolve repo directory." >&2; exit 2; }
PHASE9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
FLOW_README="${FLOW_DIR}/README.md"
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
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================
GATE_HEADING_MARKER='## Parent Close Gate (last child only)'
GATE_TOPOLOGY_MARKER='a graph-topology signal (zero open siblings), never proof the parent is done'
GATE_FETCH_FAILCLOSED_MARKER='an unreadable parent can never be proven complete (fail-closed)'
GATE_UNCERTAIN_MARKER='uncertainty is a `hold`, never a guess into the met column'
GATE_NO_AC_SECTION_MARKER='closed without an acceptance-criteria audit'
GATE_COMMENT_CMD_MARKER='gh issue comment <parentId> --repo <owner>/<repo> --body-file'

COMMIT_CLOSE_VERDICT_MARKER='written only on a `close` verdict, never from `isLastChild` alone'
COMMIT_HOLD_TRAILER_MARKER='Last child with a `hold` verdict: `Fixes #<childId>` plus `Related to #<parentId>`'
PR_BODY_HOLD_MARKER='and for a last child whose Parent Close Gate verdict is `hold`'
LABELS_CLOSE_GATED_MARKER='If `isLastChild` and the Parent Close Gate verdict is `close`, pass `--parent <parentId>`'
LABELS_HOLD_OMIT_MARKER='On a `hold` verdict, omit `--parent`'

CLEANUP_PARENT_BODY_MARKER='-parent-body.md'
CLEANUP_PARENT_GAPS_MARKER='-parent-gaps.md'

# Retired rules — the exact pre-#856 sentences, asserted absent.
RETIRED_TRAILER_MARKER='Last child: include both `Fixes #<childId>` and `Fixes #<parentId>`.'
RETIRED_README_MARKER='it auto-closes the parent alongside the child.'

SKILL_EDGE_CASE_MARKER='Last child ≠ parent complete'
README_GATE_MARKER='Parent Close Gate audits the parent'
ORCHESTRATION_GATE_MARKER='Parent Close Gate'

# --- phase-9-pr.md — the gate itself ---------------------------------------

assert_file_contains "${PHASE9}" "${GATE_HEADING_MARKER}" \
  "must add the Parent Close Gate section for last-child runs"
assert_file_contains "${PHASE9}" "${GATE_TOPOLOGY_MARKER}" \
  "must state that isLastChild is a topology signal, not proof of parent completion"
assert_file_contains "${PHASE9}" "${GATE_FETCH_FAILCLOSED_MARKER}" \
  "must fail closed (hold) when the parent body cannot be fetched"
assert_file_contains "${PHASE9}" "${GATE_UNCERTAIN_MARKER}" \
  "must count uncertain evidence as unmet, never guessed as met"
assert_file_contains "${PHASE9}" "${GATE_NO_AC_SECTION_MARKER}" \
  "must keep the close for a parent with no acceptance-criteria section and note the audit absence"
assert_file_contains "${PHASE9}" "${GATE_COMMENT_CMD_MARKER}" \
  "must post the gap report as a comment on the parent via a body file"

# --- phase-9-pr.md — verdict wiring into Commit / PR / Labels --------------

assert_file_contains "${PHASE9}" "${COMMIT_CLOSE_VERDICT_MARKER}" \
  "must condition the parent-closing trailer on a close verdict"
assert_file_contains "${PHASE9}" "${COMMIT_HOLD_TRAILER_MARKER}" \
  "must downgrade the hold-verdict trailer to Related to"
assert_file_contains "${PHASE9}" "${PR_BODY_HOLD_MARKER}" \
  "must use Related to in the PR body on a hold verdict"
assert_file_contains "${PHASE9}" "${LABELS_CLOSE_GATED_MARKER}" \
  "must gate the --parent label cascade on a close verdict"
assert_file_contains "${PHASE9}" "${LABELS_HOLD_OMIT_MARKER}" \
  "must omit the --parent cascade on a hold verdict"
assert_file_contains "${PHASE9}" "${CLEANUP_PARENT_BODY_MARKER}" \
  "must clean up the fetched parent-body temp file"
assert_file_contains "${PHASE9}" "${CLEANUP_PARENT_GAPS_MARKER}" \
  "must clean up the gap-report temp file"
assert_file_lacks "${PHASE9}" "${RETIRED_TRAILER_MARKER}" \
  "must retire the unconditional last-child double-Fixes trailer rule"

# --- SKILL.md / README / orchestration prose -------------------------------

assert_file_contains "${IMPLEMENT_SKILL}" "${SKILL_EDGE_CASE_MARKER}" \
  "must record the last-child-is-not-parent-completion edge case"
assert_file_contains "${FLOW_README}" "${README_GATE_MARKER}" \
  "must describe the audited close in the ticket-splitting paragraph"
assert_file_lacks "${FLOW_README}" "${RETIRED_README_MARKER}" \
  "must retire the unconditional auto-close sentence"
assert_file_contains "${ORCHESTRATION_DOC}" "${ORCHESTRATION_GATE_MARKER}" \
  "must reference the gate in the lifecycle prose"

echo "parent-close-verification.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
