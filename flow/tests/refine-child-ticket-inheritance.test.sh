#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #798: the tickets
# /cenci:refine creates — split children (Pass 1) and the companion design
# ticket — must inherit the parent's milestone and non-lifecycle labels.
#
# #878 supersedes #798's fetch-failure handling: the parent-metadata fetch
# now runs unconditionally, before the first write of the run (it also
# guards the parent's own label write against drift, not only inheritance),
# so its original graceful-degrade justification ("stopping would abort the
# split after the parent's body was already rewritten") no longer holds —
# stopping is now free, same as the Coverage gate. The no-meta fallback
# payload forms and their three pinned assertions are deleted; a fetch
# failure after one retry now fails closed with zero writes (D1).
#
# This ports the already-tested #635/#756 follow-up inheritance pattern
# (flow/skills/implement/phases/phase-9-pr.md, flow/skills/address-review/
# SKILL.md) to refine's two creation sites, so the anchors below deliberately
# mirror flow/tests/followup-ticket-inheritance.test.sh.
#
# Follows the fixture-free, grep-based idiom of that test: `set -uo pipefail`,
# a `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers
# built on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob — no registration needed.
#
# Covered files:
#   - flow/skills/refine/SKILL.md (parent-meta fetch + both jq creation sites)
#   - flow/skills/refine/codex.md (portability parity)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-child-ticket-inheritance.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-child-ticket-inheritance.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
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
MILESTONE_LABELS_FETCH='--json milestone,labels'
LIFECYCLE_EXCLUSION_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed"'
# #848: automerge:ok, Browser, and ui:visual-check are per-ticket grants that
# must never be inherited by a split child or the companion design ticket —
# LIFECYCLE_EXCLUSION_MARKER above (a substring of this extended list) keeps
# passing; this marker is what actually pins the 10-entry extension.
LIFECYCLE_EXCLUSION_10_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"'
MILESTONE_NUMBER_MARKER='.milestone.number'
SLURPFILE_MARKER='--slurpfile meta'
CHILD_SEED_MARKER='(["Refined"] +'
DESIGN_SEED_MARKER='(["Refined","Design"] +'
PARENT_META_CLEANUP_MARKER='-parent-meta.json'
# #878: the fetch is now unconditional and runs before the first write; a
# failure after one retry fails closed with zero writes (D1) rather than
# degrading to the no-meta fallback forms.
FAIL_CLOSED_STOP_MARKER='parent cannot be read after one retry'
# #878: the presence gate must validate fetched content, not just a
# successful `cat` exit status — a present-but-empty-or-malformed file must
# be treated the same as an unreadable fetch (D1), never as a good fetch.
JSON_SHAPE_GATE_MARKER='jq -e '\''has("labels")'\'''

# --- skills/refine/SKILL.md — the inheriting creation sites -----------------

assert_file_contains "${REFINE_SKILL}" "${MILESTONE_LABELS_FETCH}" \
  "must fetch the parent ticket's milestone and labels via gh issue view"
assert_file_contains "${REFINE_SKILL}" "${LIFECYCLE_EXCLUSION_MARKER}" \
  "must exclude the 7 lifecycle labels on a single source line"
assert_file_contains "${REFINE_SKILL}" "${LIFECYCLE_EXCLUSION_10_MARKER}" \
  "must extend the exclusion array to 10 entries so automerge:ok, Browser, and ui:visual-check are never inherited by a split child or the companion design ticket (#848)"
assert_file_contains "${REFINE_SKILL}" "${MILESTONE_NUMBER_MARKER}" \
  "must source the inherited milestone as the numeric .milestone.number, not the title"
assert_file_contains "${REFINE_SKILL}" "${SLURPFILE_MARKER}" \
  "must consume the fetched parent metadata mechanically via jq --slurpfile, never by interpolating label names into a command line"
assert_file_contains "${REFINE_SKILL}" "${CHILD_SEED_MARKER}" \
  "must seed the Pass 1 child labels array with Refined plus the carried-over parent labels"
assert_file_contains "${REFINE_SKILL}" "${DESIGN_SEED_MARKER}" \
  "must seed the design-ticket labels array with Refined,Design plus the carried-over parent labels"
assert_file_contains "${REFINE_SKILL}" "${PARENT_META_CLEANUP_MARKER}" \
  "must list the parent-meta temp file in step 13's explicit rm -f cleanup path list"
assert_file_contains "${REFINE_SKILL}" "${FAIL_CLOSED_STOP_MARKER}" \
  "must fail closed with zero writes when the parent-metadata fetch fails after one retry (#878, D1) rather than graceful-degrading to a no-meta payload"
assert_file_contains "${REFINE_SKILL}" "${JSON_SHAPE_GATE_MARKER}" \
  "must validate the fetched parent-meta file's JSON shape, not just a successful cat exit status, so a present-but-empty-or-malformed file fails the presence gate (#878, D1)"

# --- skills/refine/codex.md — portability parity ---------------------------

assert_file_contains "${REFINE_CODEX}" "milestone" \
  "must name the milestone inheritance rule so the native Codex procedure matches"
assert_file_contains "${REFINE_CODEX}" "--slurpfile" \
  "must name --slurpfile in its least-privilege command-surface inventory"

echo "refine-child-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
