#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #929: refining a split
# child must never propose another split. Without a provenance signal, a
# child brought back through /cenci:refine was evaluated cold under the same
# L-triggers-split rule as any ticket, and refinement rounds inflate the
# structural sizing signals — so complex tickets were re-split 3-4 levels
# deep. The guard: the refine skill detects split-child provenance (native
# `parent` field, `Related to #<n>` body-line fallback — context-gatherer's
# existing recipe) and passes `isSplitChild` through the bundle; the refiner
# presumes a split child sized and never emits `### Suggested Split` for it
# (an L verdict instead carries a parent re-partition recommendation); the
# skill fails closed if a split proposal arrives for a split child anyway,
# and the Confirmation Gate escalates an L-sized child to the human instead
# of creating grandchildren; ticket-sizing.md records the one-level rule;
# codex.md mirrors all of it for portability parity.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/split-ac-partition.test.sh: `set -uo pipefail`, a `failures`
# counter, `assert_file_contains` built on `grep -qF`, markers kept on a
# single source line (per docs/shell-scripting-gotchas.md), auto-discovered
# by scripts/run-checks.sh's `*.test.sh` glob — no registration needed. No
# `read_*` helpers, so this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's scan.
#
# Marker precision (docs/shell-scripting-gotchas.md's precision-on-both-ends
# rule): `### Suggested Split`, `Size Estimate`, and the split trigger all
# already appear in these files for the pre-#929 behavior, so every marker
# below is a sentence fragment that exists only once the split-depth rules
# are written — never a bare pre-existing identifier.
#
# Covered files:
#   - flow/agents/refiner.md (isSplitChild input + no-resplit rule)
#   - flow/skills/refine/SKILL.md (provenance detection + bundle flag +
#     fail-closed resplit stop + gate escalation)
#   - flow/skills/refine/codex.md (portability parity)
#   - flow/docs/ticket-sizing.md (one-level split-depth rule)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "split-depth-guard.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "split-depth-guard.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
TICKET_SIZING="${FLOW_DIR}/docs/ticket-sizing.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}

assert_file_lacks() {
  # $1=file $2=needle $3=description — guarded for existence so a renamed
  # file can never vacuously satisfy a "must not contain" claim (per
  # docs/shell-scripting-gotchas.md).
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (must NOT contain: $2)"
}

# =====================================================================
# Anchors (single source line each, per docs/shell-scripting-gotchas.md:
# keep contract-test markers on one source line so re-wrapping can't
# split the grep).
# =====================================================================

# agents/refiner.md — isSplitChild input + no-resplit rule
REFINER_INPUT_FLAG_MARKER='`isSplitChild` (with its `parentNumber`)'
REFINER_NO_RESPLIT_MARKER='never emit a `### Suggested Split` section — regardless of the size estimate'
REFINER_REPARTITION_MARKER="an oversize child means the parent's partition was wrong, not that this child should fan out again"
REFINER_SPLIT_HEADER_MARKER='### Suggested Split (only if L AND (NOT `isSplitChild` OR `resplitAuthorized`)'
REFINER_OLD_SPLIT_HEADER='### Suggested Split (only if L — real risk'
# #1093 (PR 3/3) — human-authorized resplit exception
REFINER_RESPLIT_AUTH_INPUT_MARKER='present, and `true`, only on a re-invocation following the refine skill'\''s Oversize split child escalation'
REFINER_RESPLIT_AUTH_ONLY_WAY_MARKER='This is the **only** way this flag is ever set — never inferred from the bundle, never carried over from a prior run, and never something you set yourself'
REFINER_CHECKLIST_AUTH_MARKER='**If `isSplitChild` and `resplitAuthorized` is `true`**'
REFINER_CHECKLIST_AUTH_EVALUATE_MARKER='actually evaluate whether a split is warranted here, exactly as you would for a non-child ticket'
REFINER_GUARD_EXCEPTION_MARKER='unless the invocation explicitly carries `resplitAuthorized: true`, set only by the refine skill'\''s Oversize split child escalation'
REFINER_GUARD_BYPASS_MARKER='a `### Suggested Split` is expected and routes through the normal split path — no special-cased bypass'

# skills/refine/SKILL.md — provenance detection, bundle flag, fail-closed
# stop, gate escalation
SKILL_DETECT_CMD_MARKER="--json parent --jq '.parent.number // empty'"
SKILL_DETECT_FALLBACK_MARKER='first non-empty line of the fetched body matches `Related to #<number>`'
SKILL_BUNDLE_FLAG_MARKER='and `isSplitChild` with its `parentNumber` (from the **Split-child provenance detection**'
SKILL_FAIL_CLOSED_HEADING_MARKER='**Split-depth guard (fail closed).**'
SKILL_FAIL_CLOSED_STOP_MARKER='the refiner violated the no-resplit contract for split child'
SKILL_GATE_ESCALATION_HEADING_MARKER='**Oversize split child escalation'
SKILL_GATE_PROCEED_OPTION_MARKER='Proceed — keep the oversize child as-is'
SKILL_GATE_DECLINE_OPTION_MARKER="Decline — redo the parent's partition"
SKILL_NO_GRANDCHILD_MARKER='grandchild tickets are never created'
# #1093 (PR 3/3) — third escalation option + resplitAuthorized re-invocation
SKILL_GATE_RESPLIT_OPTION_MARKER='Split this child anyway (human-authorized; creates grandchildren)'
SKILL_GATE_RESPLIT_ONLY_WAY_MARKER='This is the only way `resplitAuthorized` is ever set'
SKILL_GATE_RESPLIT_REINVOKE_MARKER='re-invoke the refiner agent (`Task` tool) with the bundle path, the complete accumulated Q&A history exactly as step 6'
SKILL_GATE_RESPLIT_FLAG_MARKER='`resplitAuthorized: true`'
SKILL_GATE_RESPLIT_NORMAL_PATH_MARKER='exactly as any other split proposal; there is no special-cased bypass of any of those checks for an authorized resplit'
SKILL_GUARD_UNAUTHORIZED_MARKER='STOP immediately unless `resplitAuthorized` was set earlier in this same run'

# skills/refine/codex.md — portability parity
CODEX_GUARD_HEADING_MARKER='**Split-depth guard**'
CODEX_NO_RESPLIT_MARKER='never emit `### Suggested Split` for it'
CODEX_ESCALATION_MARKER="proceed with the oversize child as-is or decline so the parent's partition can be redone"
# #1093 (PR 3/3) — third escalation option mirror
CODEX_RESPLIT_OPTION_MARKER='or explicitly authorize splitting this child anyway (human-authorized; creates grandchildren)'
CODEX_RESPLIT_GUARD_MARKER='unless `resplitAuthorized` was set earlier in this run'

# docs/ticket-sizing.md — one-level split-depth rule
SIZING_SECTION_HEADING_MARKER='## Split children are presumed sized'
SIZING_DEPTH_ONE_MARKER='Split depth is one.'
SIZING_SIGNAL_MARKER="a signal the **parent's partition was wrong**, not a trigger for another split"
# #1093 (PR 3/3) — human-authorized grandchild exception
SIZING_AUTH_HEADING_MARKER='### Human-authorized exception'
SIZING_AUTH_NOT_RELAX_MARKER='does not relax the "split depth is one" default rule'
SIZING_AUTH_929_MARKER='the #929 incident was about automatic recursive splitting with no human in the loop'

# --- agents/refiner.md ------------------------------------------------------

assert_file_contains "${REFINER_AGENT}" "${REFINER_INPUT_FLAG_MARKER}" \
  "must document isSplitChild/parentNumber among the bundle's resolved flags"
assert_file_contains "${REFINER_AGENT}" "${REFINER_NO_RESPLIT_MARKER}" \
  "must forbid emitting a Suggested Split for a split child at any size estimate"
assert_file_contains "${REFINER_AGENT}" "${REFINER_REPARTITION_MARKER}" \
  "must route an L split child to parent re-partition instead of another split"
assert_file_contains "${REFINER_AGENT}" "${REFINER_SPLIT_HEADER_MARKER}" \
  "must gate the Suggested Split section header on NOT isSplitChild"
assert_file_lacks "${REFINER_AGENT}" "${REFINER_OLD_SPLIT_HEADER}" \
  "must not retain the pre-#929 unguarded Suggested Split header"
assert_file_contains "${REFINER_AGENT}" "${REFINER_RESPLIT_AUTH_INPUT_MARKER}" \
  "must document resplitAuthorized as an Input, present only on the escalation re-invocation"
assert_file_contains "${REFINER_AGENT}" "${REFINER_RESPLIT_AUTH_ONLY_WAY_MARKER}" \
  "must state resplitAuthorized is only ever set by the refine skill's escalation, never by the refiner itself"
assert_file_contains "${REFINER_AGENT}" "${REFINER_CHECKLIST_AUTH_MARKER}" \
  "must add the isSplitChild+resplitAuthorized Analysis Checklist branch"
assert_file_contains "${REFINER_AGENT}" "${REFINER_CHECKLIST_AUTH_EVALUATE_MARKER}" \
  "must direct the refiner to actually evaluate a split when resplitAuthorized is true"
assert_file_contains "${REFINER_AGENT}" "${REFINER_GUARD_EXCEPTION_MARKER}" \
  "must state the resplitAuthorized exception to the never-emit-Suggested-Split rule"
assert_file_contains "${REFINER_AGENT}" "${REFINER_GUARD_BYPASS_MARKER}" \
  "must state an authorized resplit routes through the normal split path with no special-cased bypass"

# --- skills/refine/SKILL.md -------------------------------------------------

assert_file_contains "${REFINE_SKILL}" "${SKILL_DETECT_CMD_MARKER}" \
  "must detect provenance from the native parent field (context-gatherer's recipe)"
assert_file_contains "${REFINE_SKILL}" "${SKILL_DETECT_FALLBACK_MARKER}" \
  "must fall back to the Related-to body-line convention for older tickets"
assert_file_contains "${REFINE_SKILL}" "${SKILL_BUNDLE_FLAG_MARKER}" \
  "must carry isSplitChild/parentNumber into the context bundle's resolved flags"
assert_file_contains "${REFINE_SKILL}" "${SKILL_FAIL_CLOSED_HEADING_MARKER}" \
  "must carry the fail-closed split-depth guard"
assert_file_contains "${REFINE_SKILL}" "${SKILL_FAIL_CLOSED_STOP_MARKER}" \
  "must STOP with zero writes when a split proposal arrives for a split child"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_ESCALATION_HEADING_MARKER}" \
  "must surface the L-on-split-child escalation at the Confirmation Gate"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_PROCEED_OPTION_MARKER}" \
  "must offer an explicit proceed-as-is option in the escalation"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_DECLINE_OPTION_MARKER}" \
  "must offer an explicit decline-for-reparation option in the escalation"
assert_file_contains "${REFINE_SKILL}" "${SKILL_NO_GRANDCHILD_MARKER}" \
  "must state that grandchild tickets are never created"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_RESPLIT_OPTION_MARKER}" \
  "must offer the third human-authorized resplit escalation option"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_RESPLIT_ONLY_WAY_MARKER}" \
  "must state the third option is the only way resplitAuthorized is ever set"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_RESPLIT_REINVOKE_MARKER}" \
  "must document re-invoking the refiner with the bundle path and full Q&A history exactly as step 6"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_RESPLIT_FLAG_MARKER}" \
  "must document resplitAuthorized: true as the new input reaching the refiner"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GATE_RESPLIT_NORMAL_PATH_MARKER}" \
  "must route an authorized resplit through the normal split path with no special-cased bypass"
assert_file_contains "${REFINE_SKILL}" "${SKILL_GUARD_UNAUTHORIZED_MARKER}" \
  "must state the split-depth guard STOPs unless resplitAuthorized was set earlier in this same run"

# --- Negative: unauthorized resplit must still fail closed -----------------
# The STOP report text (already pinned above as SKILL_FAIL_CLOSED_STOP_MARKER)
# must remain reachable specifically on the *unauthorized* branch -- proving
# the exception did not silently swallow the fail-closed default.
SKILL_GUARD_UNAUTHORIZED_BRANCH_MARKER='When the guard fires (`resplitAuthorized` not set)'
assert_file_contains "${REFINE_SKILL}" "${SKILL_GUARD_UNAUTHORIZED_BRANCH_MARKER}" \
  "must explicitly name the unauthorized branch as where the fail-closed STOP applies"

# --- skills/refine/codex.md -------------------------------------------------

assert_file_contains "${REFINE_CODEX}" "${CODEX_GUARD_HEADING_MARKER}" \
  "must mirror the split-depth guard for portability parity"
assert_file_contains "${REFINE_CODEX}" "${SKILL_DETECT_CMD_MARKER}" \
  "must mirror the native parent-field provenance detection"
assert_file_contains "${REFINE_CODEX}" "${CODEX_NO_RESPLIT_MARKER}" \
  "must mirror the no-resplit rule for split children"
assert_file_contains "${REFINE_CODEX}" "${CODEX_ESCALATION_MARKER}" \
  "must mirror the oversize-child escalation semantics"
assert_file_contains "${REFINE_CODEX}" "${CODEX_RESPLIT_OPTION_MARKER}" \
  "must mirror the third human-authorized resplit escalation option"
assert_file_contains "${REFINE_CODEX}" "${CODEX_RESPLIT_GUARD_MARKER}" \
  "must mirror the resplitAuthorized exception on the split-depth guard"

# --- docs/ticket-sizing.md --------------------------------------------------

assert_file_contains "${TICKET_SIZING}" "${SIZING_SECTION_HEADING_MARKER}" \
  "must document the split-children-are-presumed-sized rule"
assert_file_contains "${TICKET_SIZING}" "${SIZING_DEPTH_ONE_MARKER}" \
  "must state the one-level split-depth rule"
assert_file_contains "${TICKET_SIZING}" "${SIZING_SIGNAL_MARKER}" \
  "must frame an L split child as a parent-partition signal, not a split trigger"
assert_file_contains "${TICKET_SIZING}" "${SIZING_AUTH_HEADING_MARKER}" \
  "must add a Human-authorized exception subsection"
assert_file_contains "${TICKET_SIZING}" "${SIZING_AUTH_NOT_RELAX_MARKER}" \
  "must state the exception does not relax the split-depth-is-one default rule"
assert_file_contains "${TICKET_SIZING}" "${SIZING_AUTH_929_MARKER}" \
  "must tie the exception's boundary to #929 being about automatic, not human-authorized, resplitting"

# =====================================================================

if [[ "${failures}" -gt 0 ]]; then
  echo "split-depth-guard.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "split-depth-guard.test.sh: all assertions passed."
exit 0
