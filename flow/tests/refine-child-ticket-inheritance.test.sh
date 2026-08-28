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
# stopping is now free, same as the Coverage gate. A fetch failure after one
# retry now fails closed with zero writes (D1) — SKILL.md always passes
# --parent-meta to ensure-issue.sh, never omitting it.
#
# #876 (2/12 of #661) separately retargets *where* the label/milestone
# construction machinery lives: refine's two creating sites (Pass 1 child
# create, companion design create) are extracted into the deterministic
# scripts/ensure-issue.sh helper, so the --slurpfile meta consumption, the
# 10-entry exclusion list, and the child/design seed-array literals now live
# in the script rather than inline in SKILL.md. skills/refine/SKILL.md keeps
# only the parent-metadata *fetch* itself (`gh issue view --json
# milestone,labels`, the source of the `--parent-meta <file>` argument
# ensure-issue.sh's `init` subcommand consumes) and its fail-closed
# presence/shape gate (#878, D1).
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
#   - flow/skills/refine/SKILL.md (parent-meta fetch + its fail-closed gate)
#   - flow/skills/refine/scripts/ensure-issue.sh (label/milestone construction)
#   - flow/skills/refine/codex.md (portability parity)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-child-ticket-inheritance.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-child-ticket-inheritance.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
REFINE_ENSURE_ISSUE_SCRIPT="${FLOW_DIR}/skills/refine/scripts/ensure-issue.sh"
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
#
# #876 retarget: refine's two *creating* sites (Pass 1 child create,
# companion design create) are extracted into the deterministic
# scripts/ensure-issue.sh helper (ticket #876, 2/12 of #661) -- so the
# label/milestone *construction* machinery (--slurpfile meta, the 10-entry
# exclusion list, the child/design seed-array literals, and the parent-meta
# temp-file naming convention) moves with it into the script's new home.
# What stays orchestration-level in skills/refine/SKILL.md is only the
# parent-metadata *fetch* itself (`gh issue view --json milestone,labels`,
# still the source of the `--parent-meta <file>` argument ensure-issue.sh's
# `init` subcommand consumes), the numeric-milestone-not-title sourcing
# note, and the fail-closed presence/shape gate (#878, D1).
# =====================================================================
MILESTONE_LABELS_FETCH='--json milestone,labels'
MILESTONE_NUMBER_MARKER='.milestone.number'
LIFECYCLE_EXCLUSION_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed"'
# #848: automerge:ok, Browser, and ui:visual-check are per-ticket grants that
# must never be inherited by a split child or the companion design ticket —
# LIFECYCLE_EXCLUSION_MARKER above (a substring of this extended list) keeps
# passing; this marker is what actually pins the 10-entry extension.
LIFECYCLE_EXCLUSION_10_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"'
SLURPFILE_MARKER='--slurpfile meta'
CHILD_SEED_MARKER='(["Refined"] +'
PARENT_META_CLEANUP_MARKER='-parent-meta.json'
# #878: the fetch is now unconditional and runs before the first write; a
# failure after one retry fails closed with zero writes (D1) rather than
# degrading to a no-meta fallback.
FAIL_CLOSED_STOP_MARKER='parent cannot be read after one retry'
# #878: the presence gate must validate fetched content, not just a
# successful `cat` exit status — a present-but-empty-or-malformed file must
# be treated the same as an unreadable fetch (D1), never as a good fetch.
JSON_SHAPE_GATE_MARKER='jq -e '\''has("labels")'\'''

# --- skills/refine/SKILL.md — orchestration-level fetch + fail-closed gate --

assert_file_contains "${REFINE_SKILL}" "${MILESTONE_LABELS_FETCH}" \
  "must fetch the parent ticket's milestone and labels via gh issue view"
assert_file_contains "${REFINE_SKILL}" "${MILESTONE_NUMBER_MARKER}" \
  "must source the inherited milestone as the numeric .milestone.number, not the title"
assert_file_contains "${REFINE_SKILL}" "${FAIL_CLOSED_STOP_MARKER}" \
  "must fail closed with zero writes when the parent-metadata fetch fails after one retry (#878, D1) rather than graceful-degrading to a no-meta payload"
assert_file_contains "${REFINE_SKILL}" "${JSON_SHAPE_GATE_MARKER}" \
  "must validate the fetched parent-meta file's JSON shape, not just a successful cat exit status, so a present-but-empty-or-malformed file fails the presence gate (#878, D1)"

# --- skills/refine/scripts/ensure-issue.sh — the label/milestone
# construction machinery's new home (#876) ----------------------------------

assert_file_contains "${REFINE_ENSURE_ISSUE_SCRIPT}" "${LIFECYCLE_EXCLUSION_MARKER}" \
  "must exclude the 7 lifecycle labels on a single source line"
assert_file_contains "${REFINE_ENSURE_ISSUE_SCRIPT}" "${LIFECYCLE_EXCLUSION_10_MARKER}" \
  "must extend the exclusion array to 10 entries so automerge:ok, Browser, and ui:visual-check are never inherited by a split child or the companion design ticket (#848, retargeted from refine/SKILL.md by #876)"
assert_file_contains "${REFINE_ENSURE_ISSUE_SCRIPT}" "${SLURPFILE_MARKER}" \
  "must consume the fetched parent metadata mechanically via jq --slurpfile, never by interpolating label names into a command line (retargeted from refine/SKILL.md by #876)"
assert_file_contains "${REFINE_ENSURE_ISSUE_SCRIPT}" "${CHILD_SEED_MARKER}" \
  "must seed a split child's labels array with Refined plus the carried-over parent labels (retargeted from refine/SKILL.md's Pass 1 by #876)"
# The DESIGN_SEED_MARKER companion-design-ticket jq example was removed from
# ensure-issue.sh's comments along with the rest of the design-stage removal
# -- refine no longer creates a companion design ticket at all, so there is no
# design-ticket seed-array example left to document. The 10-entry
# LIFECYCLE_EXCLUSION_10_MARKER above still keeps "Design","Designed" for
# legacy compat (a pre-removal parent's leftover label must never be
# inherited onto a new child) -- only the design-ticket-creation example is
# gone.
assert_file_contains "${REFINE_ENSURE_ISSUE_SCRIPT}" "${PARENT_META_CLEANUP_MARKER}" \
  "must name/own the parent-meta temp file's lifecycle (retargeted from refine/SKILL.md's step 13 rm -f cleanup list by #876 -- the script is now what consumes --parent-meta and, when SKILL.md's own fail-closed fetch already guarantees it exists, is the sole remaining consumer of that path)"

# --- skills/refine/codex.md — portability parity ---------------------------

assert_file_contains "${REFINE_CODEX}" "milestone" \
  "must name the milestone inheritance rule so the native Codex procedure matches"
assert_file_contains "${REFINE_CODEX}" "--slurpfile" \
  "must name --slurpfile in its least-privilege command-surface inventory (now delegated to scripts/ensure-issue.sh, #876)"

echo "refine-child-ticket-inheritance.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
