#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #857: split children must
# own an explicit partition of the parent's acceptance criteria. The
# refiner's ### Suggested Split format gains a per-child acceptance-criteria
# checklist plus the exactly-one-child partition rule (integration-scoped
# criteria land on a child that depends on all others); the refine skill
# gains a coverage gate that verifies the partition BEFORE any child ticket
# is created and STOPs on unassigned/duplicated criteria; Pass 1 persists
# each child's assigned criteria into the child body; codex.md mirrors the
# rule for portability parity.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/refine-automerge-grant.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains` built on `grep -qF`, markers
# kept on a single source line (per docs/shell-scripting-gotchas.md),
# auto-discovered by scripts/run-checks.sh's `*.test.sh` glob — no
# registration needed. No `read_*` helpers, so this file is trivially
# compliant with flow/tests/read-helper-purity-contract.test.sh's scan.
#
# Marker precision (docs/shell-scripting-gotchas.md's precision-on-both-ends
# rule): `### Acceptance Criteria`, `Suggested Split`, and `Pass 1` all
# already appear in these files for the pre-#857 behavior, so every marker
# below is a sentence fragment that exists only once the partition rules
# are written — never a bare pre-existing identifier.
#
# Covered files:
#   - flow/agents/refiner.md (split format + partition rule)
#   - flow/skills/refine/SKILL.md (coverage gate + Pass 1 persistence)
#   - flow/skills/refine/codex.md (portability parity)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "split-ac-partition.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "split-ac-partition.test.sh: failed to resolve flow directory." >&2; exit 2; }
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

# agents/refiner.md — split format + partition rules
REFINER_TEMPLATE_AC_MARKER='criterion assigned to this child'
REFINER_PARTITION_RULE_MARKER='must be assigned to exactly one child'
REFINER_NO_WEAKENING_MARKER='reworded only to scope them to the child, never weakened'
REFINER_INTEGRATION_RULE_MARKER='a child that depends on every other child'
REFINER_ADD_INTEGRATION_CHILD_MARKER='add a final integration child'
REFINER_ZERO_CRITERIA_CHILD_MARKER='the rule constrains criteria, not children'

# skills/refine/SKILL.md — coverage gate + Pass 1 persistence
SKILL_COVERAGE_GATE_HEADING_MARKER="#### Coverage gate — verify the split's acceptance-criteria partition"
SKILL_COVERAGE_STOP_MARKER='If any criterion is unassigned or duplicated, STOP'
SKILL_CHILD_AC_BULLET_MARKER='Its own `### Acceptance Criteria` section'
SKILL_CHILD_BODY_TEMPLATE_MARKER='<ticket-body>\n\n### Acceptance Criteria'

# skills/refine/codex.md — portability parity
CODEX_PARTITION_SLICE_MARKER="its slice of the parent's partition"
CODEX_PARTITION_RULE_MARKER='exactly one child'

# --- agents/refiner.md ------------------------------------------------------

assert_file_contains "${REFINER_AGENT}" "${REFINER_TEMPLATE_AC_MARKER}" \
  "must give each split child its own acceptance-criteria checklist in the template"
assert_file_contains "${REFINER_AGENT}" "${REFINER_PARTITION_RULE_MARKER}" \
  "must state the exactly-one-child partition rule for parent criteria"
assert_file_contains "${REFINER_AGENT}" "${REFINER_NO_WEAKENING_MARKER}" \
  "must allow scoping rewording but forbid weakening criteria"
assert_file_contains "${REFINER_AGENT}" "${REFINER_INTEGRATION_RULE_MARKER}" \
  "must route integration-scoped criteria to a child depending on all others"
assert_file_contains "${REFINER_AGENT}" "${REFINER_ADD_INTEGRATION_CHILD_MARKER}" \
  "must add a final integration child when none exists to carry integration criteria"
assert_file_contains "${REFINER_AGENT}" "${REFINER_ZERO_CRITERIA_CHILD_MARKER}" \
  "must permit a zero-criteria child (e.g. design-only) without breaking the partition"

# --- skills/refine/SKILL.md -------------------------------------------------

assert_file_contains "${REFINE_SKILL}" "${SKILL_COVERAGE_GATE_HEADING_MARKER}" \
  "must add the coverage gate before Pass 1"
assert_file_contains "${REFINE_SKILL}" "${SKILL_COVERAGE_STOP_MARKER}" \
  "must STOP before creating any child on an unassigned or duplicated criterion"
assert_file_contains "${REFINE_SKILL}" "${SKILL_CHILD_AC_BULLET_MARKER}" \
  "must list the child's own acceptance-criteria section among the child body contents"
assert_file_contains "${REFINE_SKILL}" "${SKILL_CHILD_BODY_TEMPLATE_MARKER}" \
  "must append the assigned criteria to the Pass 1 child body template"

# --- skills/refine/codex.md -------------------------------------------------

assert_file_contains "${REFINE_CODEX}" "${CODEX_PARTITION_SLICE_MARKER}" \
  "must mirror the per-child partition-slice persistence"
assert_file_contains "${REFINE_CODEX}" "${CODEX_PARTITION_RULE_MARKER}" \
  "must mirror the exactly-one-child partition rule"

echo "split-ac-partition.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
