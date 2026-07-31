#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #823 (2/8 of #661): the
# planner's lean-gated self-answer policy and its five named escalation
# classes (agents/planner.md), the `planning.autonomy: "lean" | "interactive"`
# config key and the new `## Lean Approval Path` same-session continuation
# branch with fully restated (not referenced) checkpoint-free error handling
# (skills/implement/phases/phase-1-plan.md), the Gate Check's third entrance
# (skills/implement/phases/phase-2-worktree.md), the document-only config
# schema entry (skills/configure/SKILL.md), and the "two checkpoint-free
# exceptions" wording at the hard-stop rule
# (skills/implement/SKILL.md).
#
# Follows the fixture-free, grep-based idiom of
# flow/tests/refine-automerge-grant.test.sh: `set -uo pipefail`, a `failures`
# counter, `assert_file_contains`/`assert_file_lacks` helpers built on
# `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob — no registration needed. No `read_*` helpers, so this file
# is trivially compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan.
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3 — assert the
# specific replacement text at the edit site, never a generic marker that may
# already match unrelated prose): several markers below deliberately reuse
# distinctive plan-provided phrasing rather than a bare identifier, because
# the bare identifier would already exist (or trivially recur) elsewhere:
#   - `cenci pipeline label <id> --transition planned --trivial` already
#     appears once, verbatim, in the existing `## Trivial Fast Path` section —
#     so the Lean Approval Path assertion below pins the sentence that
#     explains *why* this path reuses that flag, not the bare call.
#   - `Verify this call succeeded before continuing` and the generic
#     `do **not** ...` hard-stop phrasing are already owned (and pinned to
#     exactly one file-wide occurrence each) by
#     flow/tests/plan-persist-sections-contract.test.sh's `assert_occurs_once`
#     markers on this same file — the Lean Approval Path's restated error
#     handling below deliberately uses distinct wording (mirroring the
#     Trivial Fast Path's own established dodge, see that section's `not
#     setting `hasPlanFile = true`` vs the Persist-the-Plan section's bolded
#     `do **not** set `hasPlanFile = true``), and this file's negative
#     assertions confirm the count of each of those four owned markers stays
#     at exactly one.
#   - `never written by a configure prompt` already exists once, verbatim, in
#     the `security` block precedent this ticket follows — the
#     configure/SKILL.md assertion below pins the whole `planning`-scoped
#     sentence, not the bare phrase.
#
# Covered files:
#   - flow/agents/planner.md (## Self-Answer Policy, escalation classes,
#     ## Auto-Adopted Answers)
#   - flow/skills/implement/phases/phase-1-plan.md (planning.autonomy
#     delegation line, ## Lean Approval Path, restated error handling,
#     ## Q&A from Planning template, negative regression guards)
#   - flow/skills/implement/phases/phase-2-worktree.md (Gate Check third
#     entrance)
#   - flow/skills/configure/SKILL.md (document-only planning schema block)
#   - flow/skills/implement/SKILL.md (both checkpoint-free exceptions named)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "lean-planning-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "lean-planning-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
PLANNER_AGENT="${FLOW_DIR}/agents/planner.md"
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
PHASE2_WORKTREE="${FLOW_DIR}/skills/implement/phases/phase-2-worktree.md"
CONFIGURE_SKILL="${FLOW_DIR}/skills/configure/SKILL.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
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
assert_file_occurs_at_most_once() {
  # $1=file $2=needle $3=description — protects a marker string owned
  # elsewhere (plan-persist-sections-contract.test.sh's assert_occurs_once)
  # from being duplicated by this ticket's own prose.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  local count
  count="$(grep -cF -- "$2" "$1")"
  [[ "${count}" -le 1 ]] || fail "$(basename "$1") $3 (expected at most one occurrence, found ${count}: $2)"
}

# =====================================================================
# agents/planner.md — ## Self-Answer Policy, five escalation classes,
# ## Auto-Adopted Answers output section.
# =====================================================================

LEAN_GATE_MARKER='This section applies **only** when the delegation states `Planning autonomy: lean`.'
AUTOADOPTED_FORMAT_MARKER='auto-adopted: <answer> — <rationale>'
AUTOADOPTED_HEADING_MARKER='## Auto-Adopted Answers'
TICKET_SIZING_TRIGGER_MARKER='size against the budget in `docs/ticket-sizing.md`; split only on real budget risk (tier L)'
TICKET_SIZING_TIER_RESTATEMENT_MARKER='Many files spanning multiple independent concerns'

ESCALATION_SECURITY_MARKER='**security-sensitive**'
ESCALATION_DESTRUCTIVE_MARKER='**destructive or irreversible**'
ESCALATION_CONTRADICTS_MARKER='**contradicts the refined ticket**'
ESCALATION_AMBIGUITY_MARKER="**genuine product ambiguity the ticket doesn't settle**"
ESCALATION_SCOPE_MARKER='**scope blowup**'

assert_file_contains "${PLANNER_AGENT}" "${LEAN_GATE_MARKER}" \
  "must open ## Self-Answer Policy with the exact lean-gate sentence, pinned verbatim"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_SECURITY_MARKER}" \
  "must name the security-sensitive escalation class"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_DESTRUCTIVE_MARKER}" \
  "must name the destructive-or-irreversible escalation class"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_CONTRADICTS_MARKER}" \
  "must name the contradicts-the-refined-ticket escalation class"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_AMBIGUITY_MARKER}" \
  "must name the genuine-product-ambiguity escalation class"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_SCOPE_MARKER}" \
  "must name the scope-blowup escalation class"
assert_file_contains "${PLANNER_AGENT}" "${TICKET_SIZING_TRIGGER_MARKER}" \
  "scope-blowup class must reference docs/ticket-sizing.md with the short trigger line, not restate its tiers"
assert_file_lacks "${PLANNER_AGENT}" "${TICKET_SIZING_TIER_RESTATEMENT_MARKER}" \
  "must NOT restate docs/ticket-sizing.md's Budget-risk tier signal text inline"
assert_file_contains "${PLANNER_AGENT}" "${AUTOADOPTED_FORMAT_MARKER}" \
  "must use the exact auto-adopted: <answer> — <rationale> format string in the Plan Output template"
assert_file_contains "${PLANNER_AGENT}" "${AUTOADOPTED_HEADING_MARKER}" \
  "must add the ## Auto-Adopted Answers section to the Plan Output template"

# =====================================================================
# skills/implement/phases/phase-1-plan.md — delegation autonomy line,
# ## Lean Approval Path, restated error handling, Q&A template, negative
# regression guards.
# =====================================================================

PLANNING_AUTONOMY_KEY_MARKER='planning.autonomy'
DELEGATION_LEAN_MARKER='Planning autonomy: lean'
DELEGATION_INTERACTIVE_MARKER='Planning autonomy: interactive'
FAILSAFE_MARKER='anything other than the exact string `"lean"`, including a missing key or a missing `planning` block, is `interactive`'
LEAN_APPROVAL_HEADING_MARKER='## Lean Approval Path'
TRIVIAL_FLAG_REUSE_MARKER='The flag is named for its first caller, but its implemented behavior is exactly what this path needs'
FOUR_HEADING_CHECK_MARKER="Run assembly step 3's four-heading check exactly as written"
HALT_BEFORE_AUTONOMOUS_MARKER='the lean run must halt *before* it becomes autonomous'
LEAN_HARD_STOP_CHAIN_MARKER='skip the `planComment` comment, skip step 5'"'"'s label call, skip step 6'"'"'s artifact recording, leave `hasPlanFile` unset, and do not enter Phase 2.'
STOP_LABEL_VERIFICATION_MARKER='surface them and **STOP**: leave `hasPlanFile` unset, and do not proceed into Phase 2 on an unconfirmed board state.'
LABEL_VERIFICATION_MARKER='lean mode deliberately removes exactly that checkpoint'
ESCALATIONS_NEVER_APPROVED_MARKER='the plan is never implicitly approved even after the human answers it'
TRIVIAL_PRECEDENCE_MARKER='the Trivial Fast Path did not apply (it takes precedence — a trivial ticket never reaches this section)'
QANDA_AUTOADOPTED_TEMPLATE_MARKER='planner self-resolved entries as `auto-adopted: <answer> — <rationale>`'
SENSITIVE_PATH_BACKSTOP_MARKER='**Deterministic sensitive-path backstop.** Run `SKILL.md`'"'"'s `### Sensitive-path backstop (deterministic)` pattern set'
OPEN_QUESTIONS_DISQUALIFIER_MARKER='**Unresolved Open Questions.** A non-empty, non-"None" `### Open Questions` in the planner'"'"'s output disqualifies the Lean Approval Path'
# #849: the shared temp-file escalation marker was retired in favor of the
# persisted `awaiting-input` draft itself as the durable lean-approval
# blocker -- see the repo-wide negative assertion and the durable-draft
# entry-condition marker below.
DURABLE_DRAFT_ENTRY_CONDITION_MARKER='a deterministic on-disk backstop confirms no `.plans/<id>-*.md` file'"'"'s front matter carries `status: awaiting-input`'

assert_file_contains "${PHASE1_PLAN}" "${PLANNING_AUTONOMY_KEY_MARKER}" \
  "must reference the planning.autonomy config key"
assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_LEAN_MARKER}" \
  "must add the literal Planning autonomy: lean delegation line"
assert_file_contains "${PHASE1_PLAN}" "${DELEGATION_INTERACTIVE_MARKER}" \
  "must add the literal Planning autonomy: interactive delegation line"
assert_file_contains "${PHASE1_PLAN}" "${FAILSAFE_MARKER}" \
  "must state the only-exact-\"lean\"-enables-lean-mode fail-safe resolution rule"
assert_file_contains "${PHASE1_PLAN}" "${LEAN_APPROVAL_HEADING_MARKER}" \
  "must add the ## Lean Approval Path section"
assert_file_contains "${PHASE1_PLAN}" "${TRIVIAL_FLAG_REUSE_MARKER}" \
  "Lean Approval Path must explain its reuse of the --transition planned --trivial call"
assert_file_contains "${PHASE1_PLAN}" "${FOUR_HEADING_CHECK_MARKER}" \
  "Lean Approval Path must restate running assembly step 3's four-heading check"
assert_file_contains "${PHASE1_PLAN}" "${HALT_BEFORE_AUTONOMOUS_MARKER}" \
  "Lean Approval Path must state the lean run halts before it becomes autonomous on a missing heading"
assert_file_contains "${PHASE1_PLAN}" "${LEAN_HARD_STOP_CHAIN_MARKER}" \
  "Lean Approval Path must restate its own checkpoint-free hard-stop chain (skip planComment/label/artifact, leave hasPlanFile unset, no Phase 2 entry)"
assert_file_contains "${PHASE1_PLAN}" "${STOP_LABEL_VERIFICATION_MARKER}" \
  "Lean Approval Path must restate the label-call verification STOP consequence"
assert_file_contains "${PHASE1_PLAN}" "${LABEL_VERIFICATION_MARKER}" \
  "Lean Approval Path must explain why the label-call verification is load-bearing for lean mode specifically"
assert_file_contains "${PHASE1_PLAN}" "${ESCALATIONS_NEVER_APPROVED_MARKER}" \
  "must state that any escalation makes the plan never implicitly approved, even after the human answers it"
assert_file_contains "${PHASE1_PLAN}" "${TRIVIAL_PRECEDENCE_MARKER}" \
  "Lean Approval Path entry conditions must state Trivial Fast Path precedence"
assert_file_contains "${PHASE1_PLAN}" "${QANDA_AUTOADOPTED_TEMPLATE_MARKER}" \
  "## Q&A from Planning front-matter template must document the auto-adopted: <answer> — <rationale> form"
assert_file_contains "${PHASE1_PLAN}" "${SENSITIVE_PATH_BACKSTOP_MARKER}" \
  "Lean Approval Path entry conditions must run the deterministic sensitive-path backstop over Files to Modify/Create before qualifying"
assert_file_contains "${PHASE1_PLAN}" "${OPEN_QUESTIONS_DISQUALIFIER_MARKER}" \
  "Lean Approval Path entry conditions must disqualify on a non-empty, non-None ### Open Questions"
assert_file_contains "${PHASE1_PLAN}" "${DURABLE_DRAFT_ENTRY_CONDITION_MARKER}" \
  "Lean Approval Path entry conditions must require the durable-draft on-disk backstop (status: awaiting-input), replacing the retired marker-file check (#849)"

# --- Negative: the retired shared temp-file escalation marker must appear
#     nowhere in flow/ (#849) -- excludes this test file itself, which
#     necessarily spells the retired literal out once, in a Bash single-quoted
#     string, to define what the scan searches for. ------------------------

RETIRED_MARKER_LITERAL='cenci-escalated-'
RETIRED_MARKER_HITS="$(grep -rlF --exclude="$(basename "${BASH_SOURCE[0]}")" -- "${RETIRED_MARKER_LITERAL}" "${FLOW_DIR}" 2>/dev/null || true)"
if [[ -n "${RETIRED_MARKER_HITS}" ]]; then
  fail "the retired ${RETIRED_MARKER_LITERAL} marker literal must appear nowhere in flow/ (#849) -- found in: $(tr '\n' ' ' <<<"${RETIRED_MARKER_HITS}")"
fi

# --- Negative: must not reuse plan-persist-sections-contract.test.sh's four
#     assert_occurs_once markers (would break its file-wide occurs-once
#     assertions) ---------------------------------------------------------

assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** set `hasPlanFile = true`' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-set-hasPlanFile marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** post the `planComment` audit comment' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-post-planComment marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** run the `Planned` label transition' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-run-label-transition marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** record the plan artifact' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-record-artifact marker"

# --- Negative: the parity anchor consumed by
#     flow/tests/parity/contract-lib.sh's planning-immutability property must
#     survive byte-identical -------------------------------------------------

assert_file_contains "${PHASE1_PLAN}" 'Never begin Phase 2 in a session that created a new plan' \
  "must keep the planning-immutability parity anchor byte-identical"

# =====================================================================
# skills/implement/phases/phase-2-worktree.md — Gate Check third entrance.
# =====================================================================

GATE_CHECK_LEAN_ENTRANCE_MARKER='the Lean Approval Path in Phase 1 wrote a plan file this session'

assert_file_contains "${PHASE2_WORKTREE}" "${GATE_CHECK_LEAN_ENTRANCE_MARKER}" \
  "Gate Check must name the Lean Approval Path as a third entrance alongside (a)/(b)"

# =====================================================================
# skills/configure/SKILL.md — document-only planning schema block.
# =====================================================================

CONFIGURE_PLANNING_SCHEMA_MARKER='planning.autonomy'
CONFIGURE_PLANNING_DEFAULT_MARKER='default `"interactive"`'
CONFIGURE_PLANNING_NEVER_PROMPTED_MARKER='`planning` field is optional and is **never written by a configure prompt**'
CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER='"planning": { "autonomy": "interactive" }'

assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_SCHEMA_MARKER}" \
  "must document the planning.autonomy schema line"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_DEFAULT_MARKER}" \
  "must state the \"interactive\" default"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_NEVER_PROMPTED_MARKER}" \
  "must state planning is never written by a configure prompt, following the security precedent"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER}" \
  "must add planning.autonomy to the example config JSON"

# =====================================================================
# skills/implement/SKILL.md — both checkpoint-free exceptions named at the
# hard-stop rule.
# =====================================================================

HARD_STOP_BOTH_EXCEPTIONS_MARKER='the **Trivial Fast Path** and the **Lean Approval Path**'

assert_file_contains "${IMPLEMENT_SKILL}" "${HARD_STOP_BOTH_EXCEPTIONS_MARKER}" \
  "hard-stop-after-planning rule must name both checkpoint-free exceptions"

echo "lean-planning-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
