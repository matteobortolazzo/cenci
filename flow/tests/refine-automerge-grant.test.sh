#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #821 (1/8 of #661): the
# refiner's inverted question policy, its three new proposal sections
# (### Assumptions (auto-adopted), ### Decisions, ### Automation), the
# refine skill's step-11 automerge:ok grant/withhold wiring (parent ticket
# only), the canonical label table's new automerge:ok/Browser/ui:visual-check
# rows, and codex.md's mirrored policy.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/refine-child-ticket-inheritance.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers built
# on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob — no registration needed. No `read_*` helpers, so this file
# is trivially compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan.
#
# Several markers below (the withhold-override formula, the automerge:ok
# grant strings) are deliberately more specific than a bare identifier —
# `isDesignTicket` and `browserRequired` already appear elsewhere in
# flow/skills/refine/SKILL.md for unrelated purposes (step 2, step 8, the
# existing step-11 label branches), so a bare-identifier assertion would pass
# vacuously even before this ticket lands. Each marker below is an exact
# substring that only exists once this ticket's step-11 addition is written
# (docs/shell-scripting-gotchas.md's precision-on-both-ends rule).
#
# Covered files:
#   - flow/agents/refiner.md (question-policy inversion + new proposal sections)
#   - flow/skills/refine/SKILL.md (step 11 automerge:ok grant/withhold)
#   - flow/skills/configure/SKILL.md (canonical label table rows)
#   - flow/skills/refine/codex.md (portability parity)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-automerge-grant.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-automerge-grant.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
CONFIGURE_SKILL="${FLOW_DIR}/skills/configure/SKILL.md"
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
ASSUMPTIONS_SECTION_MARKER='### Assumptions (auto-adopted)'
DECISIONS_SECTION_MARKER='### Decisions'
AUTOMATION_SECTION_MARKER='### Automation'
# Pinned verbatim: flow/agents/refiner.md's ## Questions section must state
# this exact inverted-policy sentence. Phase 4 (green) must write this
# sentence into refiner.md character-for-character.
INVERTED_POLICY_MARKER='Ask ONLY about product decisions, architecture decisions with a real trade-off, or contradictions/unknowns the codebase cannot resolve — everything else with an obvious recommended answer must be auto-adopted, never asked.'

AUTOMERGE_ADD_LABEL_MARKER='--add-label "automerge:ok"'
AUTOMERGE_REMOVE_LABEL_MARKER='--remove-label "automerge:ok"'
AUTOMERGE_COLOR_MARKER='--color "006B75"'
AUTOMERGE_DESCRIPTION_MARKER='Human granted hands-off merge at refinement — babysit may merge this PR without review'

# Withhold-override formula (step 11): each identifier below is asserted as
# an exact "NOT <identifier>"/hyphenated substring, not the bare identifier,
# since the bare identifiers already exist elsewhere in the file today.
# `isDesignTicket` was dropped from this formula along with the rest of
# Pencil/design (there is no longer a design-ticket classification to
# override on) — only the browser/visual-check overrides remain.
AUTOMERGE_WITHHOLD_BROWSER_MARKER='NOT browserRequired'
AUTOMERGE_WITHHOLD_VISUALCHECK_MARKER='visual-check-signals-match'

CONFIGURE_AUTOMERGE_ROW_MARKER='| `automerge:ok` | `006B75` |'
CONFIGURE_BROWSER_ROW_MARKER='| `Browser` | `BFD4F2` |'
CONFIGURE_VISUALCHECK_ROW_MARKER='| `ui:visual-check` | `FEF2C0` |'

CODEX_ASSUMPTIONS_MARKER='Assumptions (auto-adopted)'
CODEX_AUTOMERGE_GRANT_MARKER='automerge:ok'

# Coverage for "### Automation is never persisted into the ticket body",
# and (as of the refine evidence/sizing fix, PR 2 of 3) "### Size Estimate
# IS persisted, right after ### Technical Notes": a single whole-list
# POSITIVE pin, rather than a positional negative one. A positional
# negative marker (asserting a specific two-name adjacency like "`###
# Technical Notes`, `### Automation`, plus ...`" is absent) goes silently
# vacuous the moment a legitimate new section is inserted into that same
# slot — which is exactly what this PR did by inserting `### Size
# Estimate` right after `### Technical Notes`. A future regression that
# persists `### Automation` could then land on either side of `### Size
# Estimate` (`` `### Size Estimate`, `### Automation`, plus ... `` or ``
# `### Automation`, `### Size Estimate`, plus ... ``) and neither would
# trip the old two-name marker. This marker instead pins the ENTIRE
# ordered section list, from `### Updated Description` through `###
# Design Direction` when present, as one exact substring — position-
# independent: an insertion (`### Automation` or anything else) anywhere
# within that span breaks the exact-substring match, so this is both the
# positive "### Size Estimate is in the right place" pin and the
# negative "### Automation is nowhere in the list" pin at once.
FULL_BODY_SECTION_LIST_MARKER='`### Updated Description`, `### Acceptance Criteria`, `### Assumptions (auto-adopted)`, `### Decisions`, `### Technical Notes`, `### Size Estimate`, plus `### Design Direction` when present'

# --- agents/refiner.md — question-policy inversion + new sections ----------

assert_file_contains "${REFINER_AGENT}" "${ASSUMPTIONS_SECTION_MARKER}" \
  "must add the ### Assumptions (auto-adopted) proposal section"
assert_file_contains "${REFINER_AGENT}" "${DECISIONS_SECTION_MARKER}" \
  "must add the ### Decisions proposal section"
assert_file_contains "${REFINER_AGENT}" "${AUTOMATION_SECTION_MARKER}" \
  "must add the ### Automation proposal section"
assert_file_contains "${REFINER_AGENT}" "${INVERTED_POLICY_MARKER}" \
  "must state the exact inverted question-policy sentence restricting questions to product decisions, real-tradeoff architecture decisions, and unresolvable contradictions"

# --- skills/refine/SKILL.md — step 11 automerge:ok grant/withhold ----------

assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_ADD_LABEL_MARKER}" \
  "must add --add-label \"automerge:ok\" to step 11 when the effective grant holds"
assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_REMOVE_LABEL_MARKER}" \
  "must add --remove-label \"automerge:ok\" to step 11 for the re-refine withhold case"
assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_COLOR_MARKER}" \
  "must ensure the automerge:ok label with color 006B75 before the label edit"
assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_DESCRIPTION_MARKER}" \
  "must use the exact automerge:ok label description"
assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_WITHHOLD_BROWSER_MARKER}" \
  "must name browserRequired as a step-11 withhold override on the automerge:ok verdict"
assert_file_contains "${REFINE_SKILL}" "${AUTOMERGE_WITHHOLD_VISUALCHECK_MARKER}" \
  "must name the ui:visual-check signal match as a step-11 withhold override on the automerge:ok verdict"

# --- skills/configure/SKILL.md — canonical label table ---------------------

assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_AUTOMERGE_ROW_MARKER}" \
  "must add the automerge:ok row (color 006B75) to the canonical label table"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_BROWSER_ROW_MARKER}" \
  "must add the missing Browser row (color BFD4F2) to the canonical label table"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_VISUALCHECK_ROW_MARKER}" \
  "must add the missing ui:visual-check row (color FEF2C0) to the canonical label table"

# --- skills/refine/codex.md — portability parity ---------------------------

assert_file_contains "${REFINE_CODEX}" "${CODEX_ASSUMPTIONS_MARKER}" \
  "must mirror the auto-adopted-assumptions policy so the native Codex procedure matches"
assert_file_contains "${REFINE_CODEX}" "${CODEX_AUTOMERGE_GRANT_MARKER}" \
  "must mirror the automerge:ok grant wording in the apply-mode label edit"

# --- skills/refine/SKILL.md — step 10 must never persist ### Automation ----

assert_file_contains "${REFINE_SKILL}" "${FULL_BODY_SECTION_LIST_MARKER}" \
  "step-10 body-file section list must be exactly this ordered list (### Size Estimate persisted right after ### Technical Notes, ### Automation nowhere in it)"

echo "refine-automerge-grant.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
