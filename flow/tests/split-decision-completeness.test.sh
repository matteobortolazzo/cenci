#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #872: the refine skill's
# coverage gate must verify each split child block is structurally complete
# BEFORE the existing acceptance-criteria partition check (#859/#661), so a
# child is only ever created — and labeled `Refined` — when it actually
# carries all five required subsections (`### Goal`, `### Decisions`,
# `### Assumptions (auto-adopted)`, `### Acceptance criteria`,
# `### Dependencies`), each satisfying its emptiness rule. The refiner's
# `### Suggested Split` template gains an explicit "None." sentinel rule for
# `### Decisions`, `### Assumptions (auto-adopted)`, and `### Dependencies`
# (always present, never omitted) so the gate check is deterministic;
# codex.md mirrors the extended gate for adapter parity
# (docs/adapter-contract.md).
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/split-ac-partition.test.sh (this ticket's own closest
# precedent): `set -uo pipefail`, a `failures` counter, `assert_file_contains`
# built on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob — no registration needed. No
# `read_*` helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's scan.
#
# Marker precision (docs/shell-scripting-gotchas.md's precision-on-both-ends
# rule): every marker below is a sentence fragment verified absent from the
# pre-#872 text (confirmed by hand before authoring this test) — never a
# bare pre-existing identifier like a bare "### Goal" heading, which already
# appears elsewhere in these files for unrelated reasons.
#
# Covered files:
#   - flow/skills/refine/SKILL.md (structural completeness check, ordered
#     before the AC-partition check, within the same Coverage gate)
#   - flow/agents/refiner.md ("None." sentinel rule for the split template)
#   - flow/skills/refine/codex.md (portability parity)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "split-decision-completeness.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "split-decision-completeness.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
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

# =====================================================================
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================

# skills/refine/SKILL.md — structural completeness check, ordered before
# the AC-partition check, within the same Coverage gate
SKILL_SIX_SECTIONS_MARKER='verify all six subsections are present: `### Goal`, `### Size`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance criteria`, and `### Dependencies`'
SKILL_GOAL_EMPTINESS_MARKER='`### Goal` must contain non-empty prose'
SKILL_SIZE_EMPTINESS_MARKER='`### Size` must contain a real `<S/M> — <reasoning>` value — never empty, and never the "None." sentinel'
SKILL_DEPENDENCIES_EMPTINESS_MARKER='`### Dependencies` must be non-empty'
SKILL_DEPENDENCIES_NONE_VALID_MARKER='"None." is a valid value'
SKILL_DECISIONS_ASSUMPTIONS_EMPTINESS_MARKER='`### Decisions` and `### Assumptions (auto-adopted)` must each be non-empty or exactly "None."'
SKILL_AC_EMPTINESS_MARKER='`### Acceptance criteria` may be empty only for a child the partition assigned zero criteria'
SKILL_STRUCTURE_ORDER_MARKER='the structural completeness check first, the acceptance-criteria partition check second'
SKILL_STRUCTURE_STOP_MARKER='STOP before any child creation'
SKILL_STRUCTURE_REPORT_MARKER="report the violating child's \`(K/N)\` title and the missing or empty section(s)"
SKILL_PARTITION_AFTER_STRUCTURE_MARKER='Only after the structural completeness check passes for every child, verify the proposal partitions'

# agents/refiner.md — "None." sentinel rule for the split template
REFINER_SENTINEL_HEADING_MARKER='"None." sentinel rule'
REFINER_SENTINEL_ALWAYS_PRESENT_MARKER='`### Decisions`, `### Assumptions (auto-adopted)`, and `### Dependencies` are always present in every child block, even when genuinely empty'
REFINER_SENTINEL_NEVER_OMITTED_MARKER='write exactly "None." rather than omitting the subsection or leaving it blank'
REFINER_SIZE_SENTINEL_MARKER='joins this "always present, never omitted" list too, but it is never itself the "None." sentinel'
REFINER_SIZE_NEVER_L_MARKER='so `### Size` on a child is always S or M, never L'

# skills/refine/codex.md — portability parity
CODEX_STRUCTURE_MARKER='first verify each child block is structurally complete'
CODEX_STRUCTURE_SIX_SECTIONS_MARKER='all six subsections present (`### Goal`, `### Size`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance criteria`, `### Dependencies`)'
CODEX_STRUCTURE_ORDER_MARKER='before the acceptance-criteria partition check'
CODEX_STRUCTURE_SIZE_EMPTINESS_MARKER='`### Size` a real `<S/M> — <reasoning>` value, never empty and never the "None." sentinel'
CODEX_PASS1_SIZE_PERSIST_MARKER='own `### Size`, `### Decisions` and `### Assumptions (auto-adopted)` persisted from its'

# --- skills/refine/SKILL.md -------------------------------------------------

assert_file_contains "${REFINE_SKILL}" "${SKILL_SIX_SECTIONS_MARKER}" \
  "must require all six child subsections in the structural completeness check"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GOAL_EMPTINESS_MARKER}" \
  "must require non-empty prose for a child's Goal section"
assert_file_contains "${REFINE_SKILL}" "${SKILL_SIZE_EMPTINESS_MARKER}" \
  "must require a real, non-None. value for a child's Size section"
assert_file_contains "${REFINE_SKILL}" "${SKILL_DEPENDENCIES_EMPTINESS_MARKER}" \
  "must require a non-empty Dependencies section"
assert_file_contains "${REFINE_SKILL}" "${SKILL_DEPENDENCIES_NONE_VALID_MARKER}" \
  "must allow a literal None. as a valid Dependencies value"
assert_file_contains "${REFINE_SKILL}" "${SKILL_DECISIONS_ASSUMPTIONS_EMPTINESS_MARKER}" \
  "must require Decisions and Assumptions to be non-empty or exactly None."
assert_file_contains "${REFINE_SKILL}" "${SKILL_AC_EMPTINESS_MARKER}" \
  "must permit an empty Acceptance criteria section only for a zero-criteria child"
assert_file_contains "${REFINE_SKILL}" "${SKILL_STRUCTURE_ORDER_MARKER}" \
  "must state the structural check runs before the partition check within the same gate"
assert_file_contains "${REFINE_SKILL}" "${SKILL_STRUCTURE_STOP_MARKER}" \
  "must STOP before any child creation on a structural violation"
assert_file_contains "${REFINE_SKILL}" "${SKILL_STRUCTURE_REPORT_MARKER}" \
  "must report the violating child's title and missing/empty sections"
assert_file_contains "${REFINE_SKILL}" "${SKILL_PARTITION_AFTER_STRUCTURE_MARKER}" \
  "must gate the partition check on the structural check having already passed"

# --- agents/refiner.md ------------------------------------------------------

assert_file_contains "${REFINER_AGENT}" "${REFINER_SENTINEL_HEADING_MARKER}" \
  "must state the None. sentinel rule for the Suggested Split template"
assert_file_contains "${REFINER_AGENT}" "${REFINER_SENTINEL_ALWAYS_PRESENT_MARKER}" \
  "must require Decisions, Assumptions, and Dependencies always present even when empty"
assert_file_contains "${REFINER_AGENT}" "${REFINER_SENTINEL_NEVER_OMITTED_MARKER}" \
  "must forbid omitting or blanking those sections in favor of a None. sentinel"
assert_file_contains "${REFINER_AGENT}" "${REFINER_SIZE_SENTINEL_MARKER}" \
  "must state ### Size joins the always-present list but is never itself the None. sentinel"
assert_file_contains "${REFINER_AGENT}" "${REFINER_SIZE_NEVER_L_MARKER}" \
  "must state a split child's ### Size is always S or M, never L"

# --- skills/refine/codex.md -------------------------------------------------

assert_file_contains "${REFINE_CODEX}" "${CODEX_STRUCTURE_MARKER}" \
  "must mirror the structural completeness check"
assert_file_contains "${REFINE_CODEX}" "${CODEX_STRUCTURE_SIX_SECTIONS_MARKER}" \
  "must mirror the six required child subsections"
assert_file_contains "${REFINE_CODEX}" "${CODEX_STRUCTURE_ORDER_MARKER}" \
  "must mirror the structural check running before the partition check"
assert_file_contains "${REFINE_CODEX}" "${CODEX_STRUCTURE_SIZE_EMPTINESS_MARKER}" \
  "must mirror the Size emptiness rule"
assert_file_contains "${REFINE_CODEX}" "${CODEX_PASS1_SIZE_PERSIST_MARKER}" \
  "must mirror ### Size flowing through into each child's persisted body alongside Decisions and Assumptions"

echo "split-decision-completeness.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
