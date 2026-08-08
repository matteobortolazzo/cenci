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
# Also tests ticket #979 (2/2, sibling of #978): extends `agents/planner.md`
# to full parity with the refiner's already-landed (commit `dab2aa47`)
# recommendation/entailment question policy, and wires the matching relay
# into `skills/implement/phases/phase-1-plan.md`. Several markers in the
# `#979` block below are asserted against BOTH `PLANNER_AGENT` and
# `REFINER_AGENT` — this is AC4's cross-file consistency proof: the planner
# reuses the refiner's shipped sentences verbatim (agent noun swapped)
# rather than paraphrasing them, so the identical literal string must exist
# in both files once the planner is edited.
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
#   - The configure/SKILL.md assertion below pins the opening line of the
#     `planning`-scoped rationale paragraph (`### Autonomy Settings` question
#     13 offers `planning.autonomy` only when absent), not a bare identifier
#     like `planning.autonomy` itself — that already recurs several times
#     elsewhere in the same file (the schema bullet, the dispatch-side
#     consumer paragraph), so a bare-identifier marker would pass vacuously
#     without proving this specific rationale sentence was authored. Ticket
#     #965 rewrote this paragraph (it used to read "never written by a
#     configure prompt", the `security` block's own wording) to describe the
#     new absent-only question instead — this marker moved with it.
#
# Covered files:
#   - flow/agents/planner.md (## Self-Answer Policy, escalation classes,
#     ## Auto-Adopted Answers, and — #979 — the ## Clarifying Questions
#     recommendation/entailment policy)
#   - flow/skills/implement/phases/phase-1-plan.md (planning.autonomy
#     delegation line, ## Lean Approval Path, restated error handling,
#     ## Q&A from Planning template, negative regression guards, and —
#     #979 — the (Recommended)-first interactive relay and the
#     ## Unattended Escalation Path option/recommendation clause)
#   - flow/skills/implement/phases/phase-2-worktree.md (Gate Check third
#     entrance)
#   - flow/skills/configure/SKILL.md (document-only planning schema block)
#   - flow/skills/implement/SKILL.md (both checkpoint-free exceptions named)
#   - flow/agents/refiner.md — #979 only, read-only consistency reference:
#     several markers below assert the identical literal string against
#     both agents/planner.md and this file (AC4).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "lean-planning-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "lean-planning-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
PLANNER_AGENT="${FLOW_DIR}/agents/planner.md"
REFINER_AGENT="${FLOW_DIR}/agents/refiner.md"
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
CONFIGURE_PLANNING_OFFERED_WHEN_ABSENT_MARKER='The `planning` field is optional. `### Autonomy Settings` question 13 above asks for it only'
CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER='"planning": { "autonomy": "interactive" }'

assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_SCHEMA_MARKER}" \
  "must document the planning.autonomy schema line"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_DEFAULT_MARKER}" \
  "must state the \"interactive\" default"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_OFFERED_WHEN_ABSENT_MARKER}" \
  "must state planning.autonomy (question 13, ### Autonomy Settings) is offered only when absent (#965)"
assert_file_contains "${CONFIGURE_SKILL}" "${CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER}" \
  "must add planning.autonomy to the example config JSON"

# =====================================================================
# skills/implement/SKILL.md — both checkpoint-free exceptions named at the
# hard-stop rule.
# =====================================================================

HARD_STOP_BOTH_EXCEPTIONS_MARKER='the **Trivial Fast Path** and the **Lean Approval Path**'

assert_file_contains "${IMPLEMENT_SKILL}" "${HARD_STOP_BOTH_EXCEPTIONS_MARKER}" \
  "hard-stop-after-planning rule must name both checkpoint-free exceptions"

# =====================================================================
# #979 — extend the recommendation and entailment policy to the planner
# (2/2). agents/planner.md's ## Clarifying Questions gains options
# permitted with a recommended-first option plus a one-line rationale,
# propose-first for option-less questions, and an entailment ban binding
# in BOTH modes with `follows from Q<n>` disposition into
# ## Auto-Adopted Answers. skills/implement/phases/phase-1-plan.md gets
# the matching relay: (Recommended)-first interactive mapping, a
# both-modes ## Auto-Adopted Answers parse, and verbatim option/
# recommendation rendering in the lean escalation comment.
#
# AC4 cross-file consistency: markers shared with agents/refiner.md
# (already shipped, commit dab2aa47) are asserted against BOTH
# PLANNER_AGENT and REFINER_AGENT below — REFINER_AGENT's half of each
# pair passes today (unchanged); PLANNER_AGENT's half is the RED half
# until planner.md is edited.
# =====================================================================

# --- AC1 (format), shared with refiner.md --------------------------------

RECOMMENDED_FIRST_MUST_MARKER='Every question that carries options MUST mark exactly one option as recommended, list it first, and attach a one-line rationale grounded in cited codebase evidence or a prior recorded answer.'
OPTION_FORMAT_BLOCK_MARKER='- <recommended option label> (recommended: <one-line rationale>) — <implication>'
PROPOSE_FIRST_MARKER='proposed answer, never a bare prompt.'

assert_file_contains "${PLANNER_AGENT}" "${RECOMMENDED_FIRST_MUST_MARKER}" \
  "## Clarifying Questions must require exactly one recommended option, listed first, with a one-line rationale (AC1)"
assert_file_contains "${REFINER_AGENT}" "${RECOMMENDED_FIRST_MUST_MARKER}" \
  "refiner.md must still carry the recommended-first MUST sentence verbatim (AC4 consistency reference)"
assert_file_contains "${PLANNER_AGENT}" "${OPTION_FORMAT_BLOCK_MARKER}" \
  "## Clarifying Questions must use the exact recommended-option format block (AC1)"
assert_file_contains "${REFINER_AGENT}" "${OPTION_FORMAT_BLOCK_MARKER}" \
  "refiner.md must still carry the option format block verbatim (AC4 consistency reference)"
assert_file_contains "${PLANNER_AGENT}" "${PROPOSE_FIRST_MARKER}" \
  "an option-less question must lead with the planner's proposed answer, never a bare prompt (AC1)"
assert_file_contains "${REFINER_AGENT}" "${PROPOSE_FIRST_MARKER}" \
  "refiner.md must still carry the propose-first sentence verbatim (AC4 consistency reference)"

# --- AC1 (format), planner-specific ---------------------------------------

RATIONALE_SECURITY_MARKER='cite repo-relative paths or identifiers only — never file contents, configuration values, or command output'

assert_file_contains "${PLANNER_AGENT}" "${RATIONALE_SECURITY_MARKER}" \
  "recommendation rationales must be scoped to repo-relative paths/identifiers only, never file contents/config values/command output (AC1) — a lean escalation posts questions verbatim to a possibly-public ticket"

# --- AC3 (entailment), shared with refiner.md -----------------------------

ENTAILMENT_ANSWER_ALREADY_FIXED_MARKER='a question whose answer is already fixed by a previously recorded answer'
CONFIRM_OVERRULE_MARKER='ask a confirm/overrule question that states the entailed decision and its derivation — but never one that re-opens the full option space.'

assert_file_contains "${PLANNER_AGENT}" "${ENTAILMENT_ANSWER_ALREADY_FIXED_MARKER}" \
  "must forbid a question whose answer is already fixed by a previously recorded answer (AC3)"
assert_file_contains "${REFINER_AGENT}" "${ENTAILMENT_ANSWER_ALREADY_FIXED_MARKER}" \
  "refiner.md must still carry the entailment-ban sentence verbatim (AC4 consistency reference)"
assert_file_contains "${PLANNER_AGENT}" "${CONFIRM_OVERRULE_MARKER}" \
  "must require a confirm/overrule question, never one that re-opens the full option space, on a security-sensitive/irreversible entailed decision (AC3)"
assert_file_contains "${REFINER_AGENT}" "${CONFIRM_OVERRULE_MARKER}" \
  "refiner.md must still carry the confirm/overrule sentence verbatim (AC4 consistency reference)"

# --- AC3 (entailment), planner-specific ------------------------------------

BOTH_MODES_ENTAILMENT_MARKER='The entailment ban above applies in **both** interactive and lean mode.'
FOLLOWS_FROM_QN_MARKER='Auto-adopt an entailed decision into `## Auto-Adopted Answers` with a `follows from Q<n>` citation.'
NOT_SIXTH_CLASS_TIE_IN_MARKER='coincides with the existing **security-sensitive** and **destructive or irreversible** escalation classes above, not a sixth class'
ENTAILMENT_SOURCE_MARKER='entailment sources include the refined ticket'"'"'s persisted `### Decisions` and `### Assumptions (auto-adopted)`'
TRAILING_CLAUSE_CARVEOUT_MARKER='except entailment-derived entries, which `## Clarifying Questions` below permits in both modes'

assert_file_contains "${PLANNER_AGENT}" "${BOTH_MODES_ENTAILMENT_MARKER}" \
  "the entailment ban must state it applies in both interactive and lean mode (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${FOLLOWS_FROM_QN_MARKER}" \
  "an entailed decision's resolution must disposition into ## Auto-Adopted Answers with a follows from Q<n> citation (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${NOT_SIXTH_CLASS_TIE_IN_MARKER}" \
  "the confirm/overrule requirement must tie into the existing security-sensitive/destructive-or-irreversible classes, not a sixth class (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${ENTAILMENT_SOURCE_MARKER}" \
  "entailment sources must include the refined ticket's persisted ### Decisions and ### Assumptions (auto-adopted) (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${TRAILING_CLAUSE_CARVEOUT_MARKER}" \
  "planner.md:51's trailing clause must gain the interactive-mode entailment carve-out, with LEAN_GATE_MARKER's first sentence staying byte-identical (AC3)"

# --- AC2 (relay) in phase-1-plan.md ----------------------------------------

RECOMMENDED_FIRST_MAPPING_MARKER='map it onto the first `AskUserQuestion` option, appending `(Recommended)` to that option label, and carry the rationale into its description'
RECOMMENDED_FIRST_ADVISORY_MARKER='advisory only, never a re-invocation of the planner'
BOTH_MODES_AUTOADOPTED_PARSE_MARKER='Parse `## Clarifying Questions` and `## Auto-Adopted Answers` in **both** modes'
ESCALATION_STEP2_OPTION_BULLETS_MARKER='the option bullets and marked recommendation for each question are included verbatim'
RATIONALE_SCOPE_MARKER='Recommendation rationales are held to the same scope: repo-relative paths or identifiers only, never pasted file contents, configuration values, or command output.'

assert_file_contains "${PHASE1_PLAN}" "${RECOMMENDED_FIRST_MAPPING_MARKER}" \
  "the interactive relay bullet (:446) must map a marked recommendation onto the first AskUserQuestion option with the (Recommended) suffix (AC2)"
assert_file_contains "${PHASE1_PLAN}" "${RECOMMENDED_FIRST_ADVISORY_MARKER}" \
  "the (Recommended)-first mapping must be advisory only, never a re-invocation of the planner (AC2)"
assert_file_contains "${PHASE1_PLAN}" "${BOTH_MODES_AUTOADOPTED_PARSE_MARKER}" \
  "the :444 parse must generalize from lean-only to both modes (AC2, AC3)"
assert_file_contains "${PHASE1_PLAN}" "${ESCALATION_STEP2_OPTION_BULLETS_MARKER}" \
  "## Unattended Escalation Path step 2 (:189) must state that each question's option bullets and recommendation post verbatim (AC2)"
assert_file_contains "${PHASE1_PLAN}" "${RATIONALE_SCOPE_MARKER}" \
  "## Unattended Escalation Path's secrecy paragraph (:191) must append a sentence scoping recommendation rationales to repo-relative paths/identifiers (AC2)"

# --- AC2 negative: the retired byte-unchanged phrasing at :446 must be
#     gone once phase-1-plan.md is edited (currently present, so this
#     assertion correctly FAILS in the red phase) -----------------------

RETIRED_BYTE_UNCHANGED_MARKER='**interactive mode** — byte-unchanged —'

assert_file_lacks "${PHASE1_PLAN}" "${RETIRED_BYTE_UNCHANGED_MARKER}" \
  "the :446 interactive relay bullet must no longer be marked byte-unchanged once it carries the (Recommended)-first mapping (AC2)"

# --- Regression: #823's markers must remain untouched by the #979 edit ---

assert_file_contains "${PLANNER_AGENT}" "${LEAN_GATE_MARKER}" \
  "#979 regression: ## Self-Answer Policy's lean-gate sentence must stay byte-identical"
assert_file_contains "${PLANNER_AGENT}" "${AUTOADOPTED_FORMAT_MARKER}" \
  "#979 regression: the auto-adopted: <answer> — <rationale> format string must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${AUTOADOPTED_HEADING_MARKER}" \
  "#979 regression: the ## Auto-Adopted Answers heading must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_SECURITY_MARKER}" \
  "#979 regression: the security-sensitive escalation class must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_DESTRUCTIVE_MARKER}" \
  "#979 regression: the destructive-or-irreversible escalation class must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_CONTRADICTS_MARKER}" \
  "#979 regression: the contradicts-the-refined-ticket escalation class must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_AMBIGUITY_MARKER}" \
  "#979 regression: the genuine-product-ambiguity escalation class must be unchanged"
assert_file_contains "${PLANNER_AGENT}" "${ESCALATION_SCOPE_MARKER}" \
  "#979 regression: the scope-blowup escalation class must be unchanged"

# =====================================================================
# #979 review fixes (Phase 6+7: security-reviewer 3 Low, silent-failure-
# hunter 1 Warning). All four are prose-only additions to files already
# edited by the #979 block above.
# =====================================================================

# --- Fix 1 (security, Low): secrecy takes precedence over the "post
#     verbatim" requirement at ## Unattended Escalation Path step 2 -----

SECRECY_PRECEDENCE_MARKER='This secrecy rule takes precedence over the verbatim-posting requirement above: if a recommendation rationale would contain file contents, configuration values, credentials, tokens, secrets, or raw command output, drop that rationale — keeping the option label and the recommendation marker — rather than posting it verbatim.'

assert_file_contains "${PHASE1_PLAN}" "${SECRECY_PRECEDENCE_MARKER}" \
  "## Unattended Escalation Path step 2's secrecy paragraph (:191) must state that secrecy takes precedence over verbatim posting, dropping a sensitive rationale rather than posting it (Fix 1)"

# --- Fix 2 (security, Low): broaden planner.md's rationale-scoping
#     sentence to also cover an option's <implication> text and the
#     propose-first proposed answer text ----------------------------------

RATIONALE_SCOPE_BROADENED_MARKER='This same repo-relative-paths-only restriction binds an option'"'"'s `<implication>` text and the propose-first proposed answer text too — both are posted verbatim under the same escalation path and are equally capable of embedding sensitive material.'

assert_file_contains "${PLANNER_AGENT}" "${RATIONALE_SCOPE_BROADENED_MARKER}" \
  "the rationale-citation security sentence must also scope an option's <implication> text and the propose-first proposed answer text to repo-relative paths/identifiers only (Fix 2)"

# --- Fix 3 (security, Low): restate the rationale-scoping sentence at
#     the other three secrecy-paragraph restatement sites — case (ii),
#     case (iii), and ## Resume From Draft step 3's write-questions
#     sub-step — so all 4 occurrences of the secrecy paragraph carry it,
#     not just the one at :191 fixed as part of the original #979 AC2 ---

RATIONALE_SCOPE_OCCURRENCE_COUNT="$(grep -cF -- "${RATIONALE_SCOPE_MARKER}" "${PHASE1_PLAN}")"
[[ "${RATIONALE_SCOPE_OCCURRENCE_COUNT}" -eq 4 ]] || fail "phase-1-plan.md rationale-scoping sentence (Fix 3) must appear at exactly 4 secrecy-paragraph sites (:191, case (ii), case (iii), ## Resume From Draft step 3) — found ${RATIONALE_SCOPE_OCCURRENCE_COUNT}"

# --- Fix 4 (silent-failure, Warning): anti-starvation priority rule for
#     the confirm/overrule question against the 6-question cap -----------

CONFIRM_OVERRULE_PRIORITY_MARKER='When the cap would otherwise already be exhausted by other escalation-class questions, the confirm/overrule question always takes priority over a lower-priority escalation-class question — it must always be asked, even if that means displacing a less important question from this session'"'"'s question set, never silently dropped for lack of budget.'

assert_file_contains "${PLANNER_AGENT}" "${CONFIRM_OVERRULE_PRIORITY_MARKER}" \
  "the confirm/overrule question must always take priority over a lower-priority escalation-class question when the 6-question cap would otherwise be exhausted, never silently dropped (Fix 4)"

echo "lean-planning-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
