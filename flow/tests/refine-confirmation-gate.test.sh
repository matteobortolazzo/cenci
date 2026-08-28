#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #848: refine's human
# authorization boundary moves from "after the writes" (the Q&A loop) to a
# new pre-write Confirm/Decline gate that presents the complete proposal plus
# a per-ticket manifest (labels + automerge:ok grant/withhold for the parent
# and every proposed child) before any proposal-related GitHub mutation.
# Each child gets an independently computed verdict and can earn
# Browser/ui:visual-check on its own merit; automerge:ok, Browser, and
# ui:visual-check are removed from the inheritable label set at all four
# ticket-creation sites in the repo.
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/refine-automerge-grant.test.sh and
# flow/tests/refine-child-ticket-inheritance.test.sh: `set -uo pipefail`, a
# `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers built
# on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob — no registration needed. No `read_*` helpers, so this file
# is trivially compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan.
#
# Marker-precision note (docs/shell-scripting-gotchas.md's precision-on-
# both-ends rule): several plausible bare identifiers already appear in the
# unmodified files for unrelated reasons — "Confirm"/"Decline" as English
# words, "withhold"/"grant" from the existing step-11 formula, "Working" and
# "automerge:ok" from the existing label machinery. Every marker below is
# instead an exact multi-word substring (a quoted question, an exact option
# label, a formula fragment naming a per-ticket/per-child variable that does
# not exist today) that can only exist once this ticket's gate section is
# written — verified against a full read of every covered file before this
# suite was authored.
#
# AC 6 ("a last-child PR closing both parent and child can only automerge
# when both have explicit grants") is already satisfied in watch by
# evaluateAutomerge's require-every-closing-issue-labeled rule, pinned by
# watch/internal/babysit/automerge_test.go:190
# (TestEvaluateAutomergeRequiresEveryClosingIssueLabeled) — no watch test
# change here; this suite covers only the flow half (independent explicit
# grants per ticket). The followup-inheritance block below extends the same
# never-inherited invariant to the untriaged capture-queue described in
# flow/docs/followup-triage.md.
#
# Covered files:
#   - flow/agents/refiner.md (per-ticket ### Automation registry, per-child
#     decision-complete split blocks, parent/last-child grant implication)
#   - flow/skills/refine/SKILL.md (the ## Confirmation Gate section, the
#     10-entry lifecycle exclusion set including the legacy-compat
#     "Design","Designed" markers, step 13's declined branch)
#   - flow/skills/refine/codex.md (portability parity)
#   - flow/skills/implement/phases/phase-9-pr.md (followup exclusion, the
#     same 10-entry legacy-compat set -- see the note at EXCLUSION_10_MARKER
#     below)
#   - flow/skills/address-review/SKILL.md (followup exclusion, same
#     10-entry legacy-compat set)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-confirmation-gate.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-confirmation-gate.test.sh: failed to resolve flow directory." >&2; exit 2; }
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
REFINE_SKILL="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
PHASE_9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
ADDRESS_REVIEW_SKILL="${FLOW_DIR}/skills/address-review/SKILL.md"
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

# --- Block 1: Confirmed grant ---------------------------------------------
GATE_SECTION_MARKER='## Confirmation Gate'
GATE_QUESTION_MARKER='Apply this refinement as shown?'
GATE_CONFIRM_OPTION_MARKER='Confirm — apply as shown'
# #878 supersedes #848: "No proposal-related GitHub write occurs before" was the
# weasel-word qualifier that let the pre-gate ownership claim and `Working` label
# slip through #848's own gate — #878 moves both of those writes to after the
# gate too, so the marker drops "proposal-related" entirely. The old marker is
# now asserted ABSENT (below) so the weakened qualifier cannot creep back in.
GATE_PRECEDES_WRITES_MARKER='No GitHub write of any kind occurs before'
GATE_PRECEDES_WRITES_OLD_MARKER='No proposal-related GitHub write occurs before'
GATE_AUTHORIZES_VERDICTS_MARKER='authorizes every verdict listed'
GATE_PARENT_AUTHORIZES_LAST_CHILD_MARKER='authorizes the last child'

# --- Block 2: Confirmed withhold -------------------------------------------
GATE_FAIL_CLOSED_MARKER='Fail-closed default preserved'
GATE_CONSUME_COMPUTED_MARKER='consume the gate-computed'
GATE_LABEL_SET_GRANT_MARKER='[+ `automerge:ok` when its effective grant holds]'

# --- Block 3: Rejected proposal ---------------------------------------------
GATE_DECLINE_OPTION_MARKER='Decline — make no changes'
GATE_DECLINED_CLEANUP_BRANCH_MARKER='declined-cleanup branch'
# #878 supersedes #848: a decline now performs ZERO GitHub writes (the ownership
# claim and `Working` no longer happen pre-gate at all, so there is nothing left
# to "remain"). GATE_WORKING_REMAINS_MARKER flips from a positive to a negative
# assertion on both SKILL.md and codex.md below, replaced by a positive
# assertion on the new zero-write decline text.
GATE_WORKING_REMAINS_MARKER='`Working` and the assignee claim remain'
GATE_DECLINE_ZERO_WRITES_MARKER='no cleanup mutation'
GATE_REREFINE_ADJUST_MARKER='is how to adjust'
STEP13_DECLINED_INTENTIONAL_MARKER='intentionally absent'

# --- Block 4: Mixed child verdicts ------------------------------------------
AUTOMATION_PARENT_VERDICT_MARKER='**automerge (parent)**'
AUTOMATION_CHILD_VERDICT_MARKER='**automerge (K/N)'
SEED_ARRAY_APPEND_ORDER_MARKER='in that fixed order for each label the child earned at the gate'
CHILD_PLANNABLE_STANDALONE_MARKER='plannable without undocumented parent context'

# --- Block 5: Safety overrides per child ------------------------------------
CHILD_OWN_BLOCK_TEXT_MARKER="that child's own block text"
CHILD_BROWSER_QUESTION_MARKER='Does child (K/N)'
CHILD_BROWSER_QUESTION_BATCH_MARKER='Batch up to 4 children per `AskUserQuestion` call, one question per child, in child order'
# The design-only-child skip was folded into the general "no frontend/
# browser signal" skip when the design stage was removed -- there is no
# separate design classification for a child to be "design-only" any more.
CHILD_NO_SIGNAL_SKIP_MARKER='A child with no frontend/browser signal is not asked at all'
PARENT_ANSWER_NOT_PROPAGATED_MARKER='is never propagated to any child'

# --- Block 6: Stale parent labels -------------------------------------------
# EXCLUSION_10_MARKER: every ticket-creation site -- refine/SKILL.md's own
# ensure-issue.sh-based child-creation path AND phase-9-pr.md/address-review/
# SKILL.md's legacy jq-based followup-creation sites -- intentionally still
# excludes "Design","Designed" as a defensive legacy-compat measure: a
# parent ticket refined before the design-stage removal may still carry one
# of those labels, and it must never be inherited onto a new child or
# followup ticket. See the note at refine/SKILL.md's own call site.
EXCLUSION_10_MARKER='"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"'
REREFINE_DEFERRAL_TEXT_MARKER='deferred to a later ticket'
GATE_LABELS_APPLIED_EXPLICITLY_MARKER='applied explicitly from the gate'

# --- Block 7: Followup inheritance ------------------------------------------
# EXCLUSION_10_MARKER (above) is reused here too: it is a literal substring
# of the 11-entry followup-site array (10 entries + "Followup" appended),
# the same technique flow/tests/followup-ticket-inheritance.test.sh already
# uses for its 7-entry LIFECYCLE_EXCLUSION_MARKER against the 8-entry
# (7 + "Followup") followup arrays.

# --- Codex parity (Test Strategy: "Same suite's parity block") -------------
CODEX_GATE_MARKER='Confirmation Gate'
CODEX_DECLINE_MARKER='no ticket, label, or sub-issue mutation'
CODEX_EXCLUSION_TRIO_MARKER='`automerge:ok`, `Browser`, `ui:visual-check`'

# =====================================================================
# Block 1: Confirmed grant — the gate's single confirmation exists,
# precedes every write, and its confirm branch is what unlocks
# ## Update Ticket.
# =====================================================================

assert_file_contains "${REFINE_SKILL}" "${GATE_SECTION_MARKER}" \
  "must add the unnumbered ## Confirmation Gate section between Process step 9 and ## Update Ticket"
assert_file_contains "${REFINE_SKILL}" "${GATE_QUESTION_MARKER}" \
  "must ask the single confirmation question verbatim"
assert_file_contains "${REFINE_SKILL}" "${GATE_CONFIRM_OPTION_MARKER}" \
  "must offer the exact Confirm option label"
assert_file_contains "${REFINE_SKILL}" "${GATE_PRECEDES_WRITES_MARKER}" \
  "must state that no GitHub write of any kind occurs before the gate's confirmation, replacing the old post-write CRITICAL note (#878 supersedes #848)"
assert_file_lacks "${REFINE_SKILL}" "${GATE_PRECEDES_WRITES_OLD_MARKER}" \
  "must not retain the weakened 'proposal-related' qualifier #878 dropped, since it previously let the pre-gate ownership claim and Working label slip through"
assert_file_contains "${REFINER_AGENT}" "${GATE_AUTHORIZES_VERDICTS_MARKER}" \
  "must note that the human's single confirmation authorizes every verdict listed"
assert_file_contains "${REFINER_AGENT}" "${GATE_PARENT_AUTHORIZES_LAST_CHILD_MARKER}" \
  "must note that granting the parent on a split authorizes the last child's PR to merge the epic"

# =====================================================================
# Block 2: Confirmed withhold — the fail-closed default and the withhold
# path's grant/withhold label wiring, consumed (not recomputed) downstream.
# =====================================================================

assert_file_contains "${REFINE_SKILL}" "${GATE_FAIL_CLOSED_MARKER}" \
  "must restate the fail-closed withhold default inside the gate's verdict computation"
assert_file_contains "${REFINE_SKILL}" "${GATE_CONSUME_COMPUTED_MARKER}" \
  "steps 11-12 must consume the gate-computed verdict rather than recomputing it"
assert_file_contains "${REFINE_SKILL}" "${GATE_LABEL_SET_GRANT_MARKER}" \
  "must compute each child's label set with automerge:ok included only when its effective grant holds"

# =====================================================================
# Block 3: Rejected proposal — decline makes no ticket, label, or
# sub-issue mutation, plus step 13's declined-cleanup branch.
# =====================================================================

assert_file_contains "${REFINE_SKILL}" "${GATE_DECLINE_OPTION_MARKER}" \
  "must offer the exact Decline option label"
assert_file_contains "${REFINE_SKILL}" "${GATE_DECLINED_CLEANUP_BRANCH_MARKER}" \
  "must jump straight to a declined-cleanup branch of step 13 on decline"
assert_file_lacks "${REFINE_SKILL}" "${GATE_WORKING_REMAINS_MARKER}" \
  "must NOT report that Working and the assignee claim remain after a decline (#878 supersedes #848: both are now post-confirm writes, so a decline never applies them in the first place)"
assert_file_lacks "${REFINE_CODEX}" "${GATE_WORKING_REMAINS_MARKER}" \
  "codex.md must NOT report that Working and the assignee claim remain after a decline (#878 supersedes #848)"
assert_file_contains "${REFINE_SKILL}" "${GATE_DECLINE_ZERO_WRITES_MARKER}" \
  "must report the new zero-write, no-cleanup-mutation decline contract (#878)"
assert_file_contains "${REFINE_CODEX}" "${GATE_DECLINE_ZERO_WRITES_MARKER}" \
  "codex.md must mirror the new zero-write, no-cleanup-mutation decline contract (#878)"
assert_file_contains "${REFINE_SKILL}" "${GATE_REREFINE_ADJUST_MARKER}" \
  "must tell the user re-running /cenci:refine <id> is how to adjust after a decline"
assert_file_contains "${REFINE_SKILL}" "${STEP13_DECLINED_INTENTIONAL_MARKER}" \
  "step 13 must distinguish a declined run (marker intentionally absent) from an earlier-step failure"

# =====================================================================
# Block 4: Mixed child verdicts — the per-ticket ### Automation registry
# in refiner.md and the per-child seed-array append rule in SKILL.md.
# =====================================================================

assert_file_contains "${REFINER_AGENT}" "${AUTOMATION_PARENT_VERDICT_MARKER}" \
  "### Automation must register a distinct parent verdict line"
assert_file_contains "${REFINER_AGENT}" "${AUTOMATION_CHILD_VERDICT_MARKER}" \
  "### Automation must register one per-child verdict line per proposed child"
assert_file_contains "${REFINE_SKILL}" "${SEED_ARRAY_APPEND_ORDER_MARKER}" \
  "Pass 1's child seed array must append each earned label in a fixed, documented order"
assert_file_contains "${REFINER_AGENT}" "${CHILD_PLANNABLE_STANDALONE_MARKER}" \
  "each child block must be plannable without undocumented parent context (AC 5)"

# =====================================================================
# Block 5: Safety overrides per child — per-child frontend-classification,
# per-child browser question (batched up to 4 per AskUserQuestion call,
# mirroring step 6's batched-round rule), the no-signal skip, and the
# explicit non-propagation of the parent's step-8 answer.
# =====================================================================

assert_file_contains "${REFINE_SKILL}" "${CHILD_OWN_BLOCK_TEXT_MARKER}" \
  "must apply frontend-classification to each child's own block text, never an inlined keyword list"
assert_file_contains "${REFINE_SKILL}" "${CHILD_BROWSER_QUESTION_MARKER}" \
  "must ask the browser question once per flagged child, scoped to that child"
assert_file_contains "${REFINE_SKILL}" "${CHILD_BROWSER_QUESTION_BATCH_MARKER}" \
  "must batch up to 4 children per AskUserQuestion call, one question per child, in child order, mirroring step 6's batched-round rule"
assert_file_contains "${REFINE_SKILL}" "${CHILD_NO_SIGNAL_SKIP_MARKER}" \
  "must skip the per-child browser question entirely for a child with no frontend/browser signal"
assert_file_contains "${REFINE_SKILL}" "${PARENT_ANSWER_NOT_PROPAGATED_MARKER}" \
  "must state the parent's step-8 browser answer is never propagated to any child"

# =====================================================================
# Block 6: Stale parent labels — the 10-entry exclusion set at refine's own
# creation site, plus the removed "deferred to a later ticket" text.
# =====================================================================

assert_file_contains "${REFINE_SKILL}" "${EXCLUSION_10_MARKER}" \
  "must extend the lifecycle exclusion array to 10 entries (adding automerge:ok, Browser, ui:visual-check) at the refine creation site"
assert_file_lacks "${REFINE_SKILL}" "${REREFINE_DEFERRAL_TEXT_MARKER}" \
  "must remove the stale 're-refine exception ... deferred to a later ticket' text now that the gap is closed"
assert_file_contains "${REFINE_SKILL}" "${GATE_LABELS_APPLIED_EXPLICITLY_MARKER}" \
  "the rewritten limitation paragraph must state each child's verdict and labels are applied explicitly from the gate"

# =====================================================================
# Block 7: Followup inheritance — the extended exclusion set asserted
# across all three ticket-creation sites in one place (refine child,
# phase-9-pr.md, address-review/SKILL.md), pinning the repo-wide invariant
# that a grant is never inherited anywhere one ticket creates another (see
# watch/internal/babysit/automerge_test.go:190 for AC 6's already-covered
# half, and flow/docs/followup-triage.md for the untriaged-capture-queue
# rationale). All three use the same legacy-compat 10-entry set
# (EXCLUSION_10_MARKER) that still names "Design","Designed".
# =====================================================================

for FILE in "${REFINE_SKILL}" "${PHASE_9}" "${ADDRESS_REVIEW_SKILL}"; do
  assert_file_contains "${FILE}" "${EXCLUSION_10_MARKER}" \
    "must exclude automerge:ok, Browser, and ui:visual-check from inheritance so no ticket-creation site ever leaks a refinement-granted marker"
done

# =====================================================================
# Codex parity — codex.md mirrors the gate, the decline contract, and the
# extended exclusion set via the client's available user-input mechanism
# (never AskUserQuestion, per flow/AGENTS.md Critical Rules).
# =====================================================================

assert_file_contains "${REFINE_CODEX}" "${CODEX_GATE_MARKER}" \
  "codex.md must name the Confirmation Gate so the native procedure matches"
assert_file_contains "${REFINE_CODEX}" "${CODEX_DECLINE_MARKER}" \
  "codex.md must mirror the decline ⇒ no mutation contract"
assert_file_contains "${REFINE_CODEX}" "${CODEX_EXCLUSION_TRIO_MARKER}" \
  "codex.md must mirror the extended exclusion set naming automerge:ok, Browser, and ui:visual-check"

echo "refine-confirmation-gate.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
