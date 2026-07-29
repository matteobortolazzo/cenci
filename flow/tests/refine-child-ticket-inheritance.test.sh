#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #798: the tickets
# /cenci:refine creates — split children (Pass 1) and the companion design
# ticket — must inherit the parent's milestone and non-lifecycle labels,
# with a graceful degrade on fetch failure.
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
MILESTONE_NUMBER_MARKER='.milestone.number'
SLURPFILE_MARKER='--slurpfile meta'
CHILD_SEED_MARKER='(["Refined"] +'
DESIGN_SEED_MARKER='(["Refined","Design"] +'
GRACEFUL_DEGRADE_MARKER='milestone/label inheritance was skipped'
PARENT_META_CLEANUP_MARKER='-parent-meta.json'

# --- skills/refine/SKILL.md — the inheriting creation sites -----------------

assert_file_contains "${REFINE_SKILL}" "${MILESTONE_LABELS_FETCH}" \
  "must fetch the parent ticket's milestone and labels via gh issue view"
assert_file_contains "${REFINE_SKILL}" "${LIFECYCLE_EXCLUSION_MARKER}" \
  "must exclude the 7 lifecycle labels on a single source line"
assert_file_contains "${REFINE_SKILL}" "${MILESTONE_NUMBER_MARKER}" \
  "must source the inherited milestone as the numeric .milestone.number, not the title"
assert_file_contains "${REFINE_SKILL}" "${SLURPFILE_MARKER}" \
  "must consume the fetched parent metadata mechanically via jq --slurpfile, never by interpolating label names into a command line"
assert_file_contains "${REFINE_SKILL}" "${CHILD_SEED_MARKER}" \
  "must seed the Pass 1 child labels array with Refined plus the carried-over parent labels"
assert_file_contains "${REFINE_SKILL}" "${DESIGN_SEED_MARKER}" \
  "must seed the design-ticket labels array with Refined,Design plus the carried-over parent labels"
assert_file_contains "${REFINE_SKILL}" "${GRACEFUL_DEGRADE_MARKER}" \
  "must graceful-degrade and note the skip when the parent-metadata fetch fails"
assert_file_contains "${REFINE_SKILL}" "${PARENT_META_CLEANUP_MARKER}" \
  "must list the parent-meta temp file in step 13's explicit rm -f cleanup path list"

# The graceful-degrade path keeps today's hard-coded payloads as the
# documented no-meta fallback form at both creation sites, so a fetch failure
# still produces a correct (just un-inherited) child.
assert_file_contains "${REFINE_SKILL}" 'labels: ["Refined"]}' \
  "must retain the no-meta Pass 1 fallback payload (labels: [\"Refined\"] only)"
assert_file_contains "${REFINE_SKILL}" 'labels: ["Refined","Design"]}' \
  "must retain the no-meta design-ticket fallback payload (labels: [\"Refined\",\"Design\"] only)"

# --- skills/refine/codex.md — portability parity ---------------------------

assert_file_contains "${REFINE_CODEX}" "milestone" \
  "must name the milestone inheritance rule so the native Codex procedure matches"
assert_file_contains "${REFINE_CODEX}" "--slurpfile" \
  "must name --slurpfile in its least-privilege command-surface inventory"

echo "refine-child-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
