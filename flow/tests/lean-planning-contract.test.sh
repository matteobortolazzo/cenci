#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #823 (2/8 of #661): the
# planner's lean-gated self-answer policy and its five named escalation
# classes (agents/planner.md), the `planning.autonomy: "lean" | "interactive"`
# config key and the new `## Lean Approval Path` same-session continuation
# branch with fully restated (not referenced) checkpoint-free error handling
# (skills/implement/phases/phase-1-plan.md), the Gate Check's third entrance
# (skills/implement/phases/phase-2-worktree.md), the document-only config
# schema entry (skills/configure/references/config-schema.md), and the "two checkpoint-free
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
# Also tests ticket #1131 (lean decide-and-notify): the three stop classes vs
# the two decide-and-notify classes, the `decided:` entry form, the one
# `<!-- cenci-decision -->` ticket comment (ordering before
# `cenci pipeline plan <id>` at all three call sites), the PR-body
# `## Planner Decisions` section, and the doc sync. The #1131 block sits at
# the end of this file; it rewrote the #1199 markers that claimed a
# more-than-one-defensible-resolution question still escalates.
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
#   - The configure/references/config-schema.md assertion below pins the opening line of the
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
#   - flow/skills/configure/references/config-schema.md (document-only planning
#     schema block, #1344), plus SKILL.md and every phases/*.md and
#     references/*.md for the retired-pointer negative
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

LEAN_GATE_MARKER='Question selection is identical in interactive and lean mode'
AUTOADOPTED_FORMAT_MARKER='auto-adopted: <answer> — <rationale>'
AUTOADOPTED_HEADING_MARKER='## Auto-Adopted Answers'
TICKET_SIZING_TRIGGER_MARKER='size against the S/M/L table in `docs/ticket-sizing.md`'
TICKET_SIZING_TIER_RESTATEMENT_MARKER='Many files spanning multiple independent concerns'

ESCALATION_SECURITY_MARKER='**security-sensitive**'
ESCALATION_DESTRUCTIVE_MARKER='**destructive or irreversible**'
ESCALATION_CONTRADICTS_MARKER='**contradicts the refined ticket**'
ESCALATION_AMBIGUITY_MARKER="**genuine product ambiguity the ticket doesn't settle**"
ESCALATION_SCOPE_MARKER='**scope blowup**'

assert_file_contains "${PLANNER_AGENT}" "${LEAN_GATE_MARKER}" \
  "must apply self-answer filtering in both planning modes"
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
LEAN_HARD_STOP_CHAIN_MARKER='skip the `planComment` comment, skip step 5'"'"'s `cenci pipeline plan` call, skip step 6'"'"'s label call, skip step 7'"'"'s artifact recording, leave `hasPlanFile` unset, and do not enter Phase 2.'
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
# skills/configure/references/config-schema.md — document-only planning
# schema block (the schema moved out of SKILL.md in #1344). The retired
# positional pointer is scanned across SKILL.md plus every phases/*.md and
# references/*.md, so it cannot reappear in any configure file.
# =====================================================================

CONFIGURE_SCHEMA="${FLOW_DIR}/skills/configure/references/config-schema.md"
configure_markdown_files=("${CONFIGURE_SKILL}")
shopt -s nullglob
configure_markdown_files+=("${FLOW_DIR}"/skills/configure/phases/*.md "${FLOW_DIR}"/skills/configure/references/*.md)
shopt -u nullglob
[[ "${#configure_markdown_files[@]}" -ge 16 ]] \
  || fail "configure markdown scan is vacuous: found ${#configure_markdown_files[@]} files (expected SKILL.md plus at least 15 phases/references files)"

CONFIGURE_PLANNING_SCHEMA_MARKER='planning.autonomy'
CONFIGURE_PLANNING_DEFAULT_MARKER='default `"interactive"`'
# The pointer names the file that now holds question 13 (#1342); the old
# positional "question 13 above" pointer crosses files, so it is retired. This
# suite greps line by line, so the marker must stay on one source line.
CONFIGURE_PLANNING_OFFERED_WHEN_ABSENT_MARKER='The `planning` field is optional. `### Autonomy Settings` question 13 in `phases/autonomy-settings.md` asks for it only'
CONFIGURE_PLANNING_OLD_POSITIONAL_MARKER='`### Autonomy Settings` question 13 above'
CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER='"planning": { "autonomy": "interactive" }'

assert_file_contains "${CONFIGURE_SCHEMA}" "${CONFIGURE_PLANNING_SCHEMA_MARKER}" \
  "must document the planning.autonomy schema line"
assert_file_contains "${CONFIGURE_SCHEMA}" "${CONFIGURE_PLANNING_DEFAULT_MARKER}" \
  "must state the \"interactive\" default"
assert_file_contains "${CONFIGURE_SCHEMA}" "${CONFIGURE_PLANNING_OFFERED_WHEN_ABSENT_MARKER}" \
  "must state planning.autonomy (question 13, ### Autonomy Settings in phases/autonomy-settings.md) is offered only when absent (#965)"
for configure_file in "${configure_markdown_files[@]}"; do
  assert_file_lacks "${configure_file}" "${CONFIGURE_PLANNING_OLD_POSITIONAL_MARKER}" \
    "must not keep the positional \"question 13 above\" pointer now that question 13 lives in another file (#1342)"
done
assert_file_contains "${CONFIGURE_SCHEMA}" "${CONFIGURE_PLANNING_EXAMPLE_JSON_MARKER}" \
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
TRAILING_CLAUSE_CARVEOUT_MARKER='Interactive resolutions use `auto-adopted:`'

assert_file_contains "${PLANNER_AGENT}" "${BOTH_MODES_ENTAILMENT_MARKER}" \
  "the entailment ban must state it applies in both interactive and lean mode (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${FOLLOWS_FROM_QN_MARKER}" \
  "an entailed decision's resolution must disposition into ## Auto-Adopted Answers with a follows from Q<n> citation (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${NOT_SIXTH_CLASS_TIE_IN_MARKER}" \
  "the confirm/overrule requirement must tie into the existing security-sensitive/destructive-or-irreversible classes, not a sixth class (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${ENTAILMENT_SOURCE_MARKER}" \
  "entailment sources must include the refined ticket's persisted ### Decisions and ### Assumptions (auto-adopted) (AC3)"
assert_file_contains "${PLANNER_AGENT}" "${TRAILING_CLAUSE_CARVEOUT_MARKER}" \
  "interactive resolutions must use auto-adopted provenance"

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
  "shared self-answer policy must remain explicit"
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

# =====================================================================
# #1199 (Part 1) -- grounded resolution of the contradicts-the-refined-
# ticket and genuine-product-ambiguity classes in agents/planner.md's
# ## Self-Answer Policy (lean only), plus the matching doc sync in
# docs/autonomous-loop.md.
#
# Acceptance-criterion map (each AC has at least one explicit assertion):
#   - velka#2330 shape (doc-prescribed sequencing auto-adopts, doc cited)
#       -> GROUNDED_DOC_EXAMPLE_MARKER + GROUNDED_CITE_PATH_MARKER
#   - velka#2329 shape (package-declared fact auto-adopts, package cited)
#       -> GROUNDED_PACKAGE_EXAMPLE_* markers + GROUNDED_CITE_PACKAGE_MARKER
#   - AC unsatisfiable as written still escalates
#       -> GROUNDED_AC_UNSATISFIABLE_MARKER + GROUNDED_AC_TEXT_IMMUTABLE_MARKER
#   - more than one defensible resolution is now DECIDED and notified, not
#     escalated (#1131 rewrote the original #1199 "still escalates" claim)
#       -> GROUNDED_III_DECIDED_MARKER + the retired-text absence checks
#   - security-sensitive / destructive bullets byte-for-byte unchanged
#       -> SECURITY_BULLET_LINE / DESTRUCTIVE_BULLET_LINE whole-line pins
#         (these PASS on the unedited file by design; they guard against edits)
#   - grounded answer never suppresses a security/irreversibility ask
#       -> GROUNDED_PRECEDENCE_* markers
#   - explicit "unique mechanical resolution" vs "product judgment" boundary
#     with worked examples and at least one counter-example
#       -> GROUNDED_MECHANICAL_* / GROUNDED_JUDGMENT_* / counter-example count
#   - scope-blowup class asks only when a split is actually recommended
#       -> SCOPE_BLOWUP_ONLY_SPLIT_MARKER
#   - docs/autonomous-loop.md in sync (Input Needed off-ramp)
#       -> LOOP_DOC_* markers (whitespace-normalized: the doc is hard-wrapped)
# =====================================================================

REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "lean-planning-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
LOOP_DOC="${REPO_ROOT}/docs/autonomous-loop.md"

# load_content <file> -- pure read, no fail() side effect (safe inside $(...)).
load_content() {
  cat "$1" 2>/dev/null
}

# require_content <result-var> <file> <label> -- nameref wrapper; fails closed
# and assigns "" on a read failure. Must NOT be invoked via $(...).
require_content() {
  local -n _res="$1"
  local _c
  if ! _c="$(load_content "$2")"; then
    fail "$3: could not read file: $2"
    _res=""
    return 1
  fi
  _res="${_c}"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware: the body
# of the named "## <heading>" section through the next real (unfenced) "## "
# heading, or EOF.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_block <content> <start-needle> -- pure: from the first line containing
# the needle through the line before the next blank line, numbered-step line
# ("N. "), or bullet line ("- "). Keeps a wrapped paragraph/step/bullet together
# and scopes a marker to the one construct it belongs to.
extract_block() {
  awk -v want="$2" '
    !on && index($0, want) { on=1; print; next }
    on && (/^[[:space:]]*$/ || /^[0-9]+\. / || /^- /) { exit }
    on { print }
  ' <<<"$1"
}

# require_section / require_block <result-var> <content> <anchor> <label> --
# nameref wrappers; fail closed (never an empty-but-present masquerade).
require_section() {
  local -n _res="$1"
  local _body
  _body="$(extract_section "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _res=""
    return 1
  fi
  _res="${_body}"
}
require_block() {
  local -n _res="$1"
  local _body
  _body="$(extract_block "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate block starting at '$3'"
    _res=""
    return 1
  fi
  _res="${_body}"
}

# normalize_ws <content> -- collapse newlines and repeated whitespace so a
# markdown-wrapped sentence matches as one substring (shell-scripting-gotchas.md
# line-wrapping pitfall). Mirrors attended-autonomy-contract.test.sh.
normalize_ws() {
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <needle> <label>
assert_contains_ws() {
  local content="$1" needle="$2" label="$3"
  [[ -n "${needle}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${needle}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${needle}]"
}

# assert_count_at_least_ws <content> <needle> <min> <label> -- occurrence count
# (grep -o, never -c, per shell-scripting-gotchas.md) on normalized content.
assert_count_at_least_ws() {
  local content="$1" needle="$2" min="$3" label="$4"
  [[ -n "${needle}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm count
  norm="$(normalize_ws "${content}")"
  count="$(grep -oF -- "${needle}" <<<"${norm}" | wc -l | tr -d '[:space:]')"
  [[ "${count}" -ge "${min}" ]] || fail "${label}: expected at least ${min} occurrences, found ${count}: [${needle}]"
}

# assert_file_has_line <file> <exact-line> <label> -- whole-line match
# (grep -qxF): pins a full bullet byte-for-byte, so any edit to it fails.
assert_file_has_line() {
  [[ -n "$2" ]] || { fail "$3: empty line (test bug)"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qxF -- "$2" "$1" || fail "$(basename "$1") $3 (expected an exact whole line: $2)"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden> <label> -- a
# bare absence check passes vacuously while the replacement text is still
# unauthored, proving nothing. Requiring the replacement marker's own presence
# first makes the assertion fail loudly, for the right reason, until the edit
# lands. Mirrors attended-autonomy-contract.test.sh.
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  [[ -n "${forbidden}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- replacement marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: retired text still present (whitespace-normalized): [${forbidden}]"
}

# extract_h3_section <content> <exact-heading-line> -- pure, fence-aware: the
# body of the named "### <heading>" section through the line before the next
# real (unfenced) "## " or "### " heading, or EOF. "#### " subsections stay in.
extract_h3_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && (/^## / || /^### /) { exit }
    on { print }
  ' <<<"$1"
}

# heading_line <content> <exact-heading-line> -- pure: the 1-based line number
# of the first exact match, or empty.
heading_line() {
  awk -v want="$2" '$0 == want { print NR; exit }' <<<"$1"
}

# assert_heading_order <content> <first-heading> <second-heading> <label> --
# both exact headings must exist and the first must come before the second.
assert_heading_order() {
  local content="$1" first="$2" second="$3" label="$4"
  local first_no second_no
  first_no="$(heading_line "${content}" "${first}")"
  second_no="$(heading_line "${content}" "${second}")"
  if [[ -z "${first_no}" || -z "${second_no}" ]]; then
    fail "${label}: could not locate both headings [${first}] and [${second}]"
    return
  fi
  [[ "${first_no}" -lt "${second_no}" ]] || fail "${label}: [${first}] (line ${first_no}) must come before [${second}] (line ${second_no})"
}

# assert_mention_precedes_line <region> <mention-needle> <exact-line> <label> --
# ordering proof inside one region: the first line containing the mention
# needle must come BEFORE the first line that, trimmed, equals the exact
# command line (for example the bare `cenci pipeline plan <id>` fence line).
# Both must exist (fail closed, never an empty-but-present masquerade). Pure
# awk inside $(...); fail() is called in the parent shell only.
assert_mention_precedes_line() {
  local region="$1" mention="$2" exact="$3" label="$4"
  [[ -n "${mention}" && -n "${exact}" ]] || { fail "${label}: empty ordering pattern (test bug)"; return; }
  local verdict
  verdict="$(awk -v m="${mention}" -v c="${exact}" '
    !mline && index($0, m) { mline = NR }
    { t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t]+$/, "", t) }
    !cline && t == c { cline = NR }
    END {
      if (!mline) { print "mention-missing"; exit }
      if (!cline) { print "line-missing"; exit }
      print (mline < cline) ? "ok" : "mention-after-line"
    }
  ' <<<"${region}")"
  [[ "${verdict}" == "ok" ]] || fail "${label}: ordering check ${verdict} -- [${mention}] must appear before the exact line [${exact}]"
}

# assert_has_trimmed_line <region> <exact-line> <label> -- some line of the
# region, trimmed, equals the exact line. A trimmed-equal line cannot start
# with ">" so a marker matched this way is provably non-blockquoted.
assert_has_trimmed_line() {
  local region="$1" exact="$2" label="$3"
  [[ -n "${exact}" ]] || { fail "${label}: empty line (test bug)"; return; }
  local verdict
  verdict="$(awk -v c="${exact}" '
    { t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t]+$/, "", t) }
    t == c { found = 1 }
    END { print found ? "ok" : "missing" }
  ' <<<"${region}")"
  [[ "${verdict}" == "ok" ]] || fail "${label}: no line equals [${exact}] on its own"
}

# --- AC: security-sensitive and destructive bullets are byte-for-byte
#     unchanged (both full lines pinned; expected to PASS on the unedited
#     file and keep passing) ---------------------------------------------------

SECURITY_BULLET_LINE='- **security-sensitive** — auth, credentials/secrets, crypto, payment/billing, or anything matching the union of `skills/implement/SKILL.md`'"'"'s `### Sensitive-path backstop (deterministic)` pattern set (the built-in default sensitive-path patterns) and the project'"'"'s `security.sensitivePaths` — not `security.sensitivePaths` alone.'
DESTRUCTIVE_BULLET_LINE='- **destructive or irreversible** — data migrations, schema changes, deletions, history rewrites, or anything else without a cheap rollback.'

assert_file_has_line "${PLANNER_AGENT}" "${SECURITY_BULLET_LINE}" \
  "the security-sensitive escalation bullet must stay byte-for-byte unchanged (#1199)"
assert_file_has_line "${PLANNER_AGENT}" "${DESTRUCTIVE_BULLET_LINE}" \
  "the destructive-or-irreversible escalation bullet must stay byte-for-byte unchanged (#1199)"

# --- ## Self-Answer Policy: the grounded-resolution rule ---------------------

GROUNDED_LABEL_MARKER='**Grounded resolution**'
GROUNDED_BOTH_CLASSES_MARKER='applies to the **contradicts the refined ticket** and **genuine product ambiguity the ticket doesn'"'"'t settle** classes only'
GROUNDED_AUTOADOPT_MARKER='auto-adopts it into `## Auto-Adopted Answers`'
GROUNDED_MECHANICAL_MARKER='**unique mechanical resolution**'
GROUNDED_MECHANICAL_ONE_ANSWER_MARKER='exactly one correct answer'
GROUNDED_MECHANICAL_GROUNDING_MARKER='a project doc (`AGENTS.md`, `README.md`, `docs/<topic>.md`) or a checkable codebase/package fact'
GROUNDED_JUDGMENT_MARKER='**product judgment**'
# #1131: only (i) and (ii) still escalate; (iii) goes to decide-and-notify.
GROUNDED_STOPS_I_II_MARKER='still escalates under its class exactly as before only when it is (i) a contradiction that makes an acceptance criterion unsatisfiable as written'
GROUNDED_AC_UNSATISFIABLE_MARKER='makes an acceptance criterion unsatisfiable as written'
GROUNDED_AC_TEXT_IMMUTABLE_MARKER='auto-adoption never edits an acceptance criterion'"'"'s literal text, only the interpretation of a `### Decisions` detail may be resolved mechanically'
GROUNDED_III_DECIDED_MARKER='(iii) more than one defensible resolution is decided and notified under **Decide and notify** below, not escalated'
# Retired #1199 wording that #1131 made false (asserted absent, paired with the
# replacement marker above so the absence check is never vacuous).
GROUNDED_RETIRED_JUDGMENT_MARKER='product judgment, which still escalates under its class exactly as before, is any of'
GROUNDED_RETIRED_MULTI_RESOLUTION_MARKER='or (iii) more than one defensible resolution.'
GROUNDED_RETIRED_COUNTEREXAMPLE_D_MARKER='Counter-example (d)'
GROUNDED_ANSWER_TEXT_MARKER='The auto-adopted answer text is held to the same restriction'
GROUNDED_ANSWER_PUBLIC_IDENT_MARKER='may name a public identifier (such as a package-declared family name like `Inter Variable`) but never configuration values, file contents, or command output'
GROUNDED_ANSWER_SECRECY_WINS_MARKER='secrecy wins over recording the answer in full'
GROUNDED_CITE_PATH_MARKER='grounded in `<repo-relative path>`'
GROUNDED_CITE_PACKAGE_MARKER='grounded in package `<identifier>`'
GROUNDED_CITE_PATHS_ONLY_MARKER='repo-relative path or package identifier'
GROUNDED_CITE_NO_CONTENTS_MARKER='never file contents, configuration values, or command output'
GROUNDED_PRECEDENCE_NEVER_SUPPRESSES_MARKER='a grounded answer never suppresses a security or irreversibility ask'
GROUNDED_PRECEDENCE_WINS_MARKER='the security/destructive class wins'
GROUNDED_PACKAGE_EXAMPLE_PKG_MARKER='@fontsource-variable/inter'
GROUNDED_PACKAGE_EXAMPLE_FAMILY_MARKER='Inter Variable'
GROUNDED_DOC_EXAMPLE_MARKER='a project doc prescribes the sequencing'
COUNTEREXAMPLE_MARKER='Counter-example'

if require_content PLANNER_CONTENT "${PLANNER_AGENT}" "planner.md" \
   && require_section POLICY_SECTION "${PLANNER_CONTENT}" "## Self-Answer Policy" "planner.md"; then
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_LABEL_MARKER}" \
    "planner.md (## Self-Answer Policy) must add a Grounded resolution rule"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_BOTH_CLASSES_MARKER}" \
    "planner.md (## Self-Answer Policy) grounded rule must name both the contradicts and ambiguity classes, and only those"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_AUTOADOPT_MARKER}" \
    "planner.md (## Self-Answer Policy) grounded rule must record the answer in ## Auto-Adopted Answers"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_MECHANICAL_MARKER}" \
    "planner.md (## Self-Answer Policy) must define a unique mechanical resolution"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_MECHANICAL_ONE_ANSWER_MARKER}" \
    "planner.md (## Self-Answer Policy) unique mechanical resolution must require exactly one correct answer"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_MECHANICAL_GROUNDING_MARKER}" \
    "planner.md (## Self-Answer Policy) unique mechanical resolution must be grounded in a project doc or a checkable codebase/package fact"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_JUDGMENT_MARKER}" \
    "planner.md (## Self-Answer Policy) must define the product judgment side of the boundary"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_STOPS_I_II_MARKER}" \
    "planner.md (## Self-Answer Policy) must state product judgment still escalates only for (i) an unsatisfiable acceptance criterion and (ii) a literal-text change (#1131 rewrite of the #1199 claim)"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_AC_UNSATISFIABLE_MARKER}" \
    "planner.md (## Self-Answer Policy) must keep escalating a contradiction that makes an AC unsatisfiable as written"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_AC_TEXT_IMMUTABLE_MARKER}" \
    "planner.md (## Self-Answer Policy) must state auto-adoption can never change an acceptance criterion's literal text"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_III_DECIDED_MARKER}" \
    "planner.md (## Self-Answer Policy) must route (iii) more than one defensible resolution to Decide and notify instead of escalating (#1131 rewrite of the #1199 claim)"
  assert_absent_paired_ws "${POLICY_SECTION}" "${GROUNDED_III_DECIDED_MARKER}" "${GROUNDED_RETIRED_MULTI_RESOLUTION_MARKER}" \
    "planner.md (## Self-Answer Policy) must no longer list (iii) more than one defensible resolution as an escalation trigger"
  assert_absent_paired_ws "${POLICY_SECTION}" "${GROUNDED_III_DECIDED_MARKER}" "${GROUNDED_RETIRED_JUDGMENT_MARKER}" \
    "planner.md (## Self-Answer Policy) must no longer say all product judgment escalates (the retired is-any-of-(i)-(ii)-(iii) sentence)"
  assert_absent_paired_ws "${POLICY_SECTION}" "${GROUNDED_III_DECIDED_MARKER}" "${GROUNDED_RETIRED_COUNTEREXAMPLE_D_MARKER}" \
    "planner.md (## Self-Answer Policy) worked example (d) must no longer be a counter-example that escalates (it becomes a Decide example)"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_CITE_PATH_MARKER}" \
    "planner.md (## Self-Answer Policy) must require a grounding citation by repo-relative path"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_CITE_PACKAGE_MARKER}" \
    "planner.md (## Self-Answer Policy) must require a grounding citation by package identifier"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_CITE_PATHS_ONLY_MARKER}" \
    "planner.md (## Self-Answer Policy) must restrict citations to a repo-relative path or package identifier"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_CITE_NO_CONTENTS_MARKER}" \
    "planner.md (## Self-Answer Policy) must restate the paths/identifiers-only restriction (never file contents, config values, or command output) for grounding citations"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_ANSWER_TEXT_MARKER}" \
    "planner.md (## Self-Answer Policy) must extend the paths/identifiers-only restriction to the auto-adopted answer text itself, not just the citation"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_ANSWER_PUBLIC_IDENT_MARKER}" \
    "planner.md (## Self-Answer Policy) must allow the answer text to name public identifiers but never configuration values, file contents, or command output"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_ANSWER_SECRECY_WINS_MARKER}" \
    "planner.md (## Self-Answer Policy) must state secrecy wins over recording the answer in full (state precedence, #979)"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_PRECEDENCE_NEVER_SUPPRESSES_MARKER}" \
    "planner.md (## Self-Answer Policy) must state a grounded answer never suppresses a security or irreversibility ask"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_PRECEDENCE_WINS_MARKER}" \
    "planner.md (## Self-Answer Policy) must state the security/destructive class wins when a question fits both"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_PACKAGE_EXAMPLE_PKG_MARKER}" \
    "planner.md (## Self-Answer Policy) must include the velka#2329 worked example naming the package"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_PACKAGE_EXAMPLE_FAMILY_MARKER}" \
    "planner.md (## Self-Answer Policy) must include the velka#2329 worked example naming the font family the package declares"
  assert_contains_ws "${POLICY_SECTION}" "${GROUNDED_DOC_EXAMPLE_MARKER}" \
    "planner.md (## Self-Answer Policy) must include the velka#2330 worked example (a project doc prescribes the sequencing)"
  assert_count_at_least_ws "${POLICY_SECTION}" "${COUNTEREXAMPLE_MARKER}" 1 \
    "planner.md (## Self-Answer Policy) must keep the counter-example that still escalates (AC unsatisfiable); the more-than-one-defensible-resolution example moved to a Decide example in #1131"

  # --- scope-blowup class: asks only on an actual split recommendation -------
  SCOPE_BLOWUP_ONLY_SPLIT_MARKER='only when it actually recommends a split'
  SCOPE_BLOWUP_SPLIT_GATE_MARKER='goes through the Split Gate rather than being asked'
  if require_block SCOPE_BLOWUP_BULLET "${POLICY_SECTION}" '- **scope blowup**' "planner.md (## Self-Answer Policy)"; then
    assert_contains_ws "${SCOPE_BLOWUP_BULLET}" "${SCOPE_BLOWUP_ONLY_SPLIT_MARKER}" \
      "planner.md scope-blowup class must ask a clarifying question only when the planner actually recommends a split"
    assert_contains_ws "${SCOPE_BLOWUP_BULLET}" "${SCOPE_BLOWUP_SPLIT_GATE_MARKER}" \
      "planner.md scope-blowup class must route an L estimate with a reasoned no-split verdict (or a size change) through the Split Gate instead of asking"
    assert_contains_ws "${SCOPE_BLOWUP_BULLET}" "${TICKET_SIZING_TRIGGER_MARKER}" \
      "planner.md scope-blowup class must keep sizing against the budget in docs/ticket-sizing.md, splitting only on real budget risk (tier L)"
  fi
fi

# --- Regression: the opening sentence of the #1069 settled-posture paragraph
#     (## Clarifying Questions) must stay byte-identical. -----------------------

SETTLED_POSTURE_OPENING_MARKER='When that entailed decision is a security-sensitive or irreversible posture already fixed verbatim by the refined ticket'"'"'s own `### Decisions` or `### Assumptions (auto-adopted)`, this posture is auto-adopted instead of asked'
assert_file_contains "${PLANNER_AGENT}" "${SETTLED_POSTURE_OPENING_MARKER}" \
  "#1199 regression: the #1069 settled-posture paragraph's opening sentence must stay untouched"

# --- docs/autonomous-loop.md: Input Needed off-ramp + escalation-class
#     description in sync (whitespace-normalized: the doc is hard-wrapped) ---

LOOP_OFFRAMP_MARKER='unless attended mode is on for this machine, in which case planning asks you directly instead'
# #1131 rewrote the off-ramp bullet around three stop classes: the unique-grounded
# resolution stays auto-adopted silently, the old five-class phrasing is gone.
LOOP_DOC_GROUNDED_MARKER='a unique grounded resolution is still auto-adopted silently'
LOOP_DOC_RETIRED_GROUNDED_MARKER='contradictions and ambiguities with exactly one grounded resolution are auto-adopted'
LOOP_DOC_RETIRED_FIVE_CLASSES_MARKER='planning hit one of five escalation classes'
LOOP_DOC_SCOPE_BLOWUP_MARKER='scope blowup means a real split recommendation'

if require_content LOOP_CONTENT "${LOOP_DOC}" "docs/autonomous-loop.md"; then
  if require_block LOOP_OFFRAMP_BLOCK "${LOOP_CONTENT}" '**`Input Needed`**' "docs/autonomous-loop.md (Input Needed off-ramp)"; then
    assert_contains_ws "${LOOP_OFFRAMP_BLOCK}" "${LOOP_DOC_GROUNDED_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must say a unique grounded resolution is still auto-adopted silently, not escalated"
    assert_absent_paired_ws "${LOOP_OFFRAMP_BLOCK}" "${LOOP_DOC_GROUNDED_MARKER}" "${LOOP_DOC_RETIRED_GROUNDED_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must drop the retired #1199 grounded-resolution sentence"
    assert_absent_paired_ws "${LOOP_OFFRAMP_BLOCK}" "${LOOP_DOC_GROUNDED_MARKER}" "${LOOP_DOC_RETIRED_FIVE_CLASSES_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must no longer describe five escalation classes"
    assert_contains_ws "${LOOP_OFFRAMP_BLOCK}" "${LOOP_DOC_SCOPE_BLOWUP_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must say scope blowup means a real split recommendation"
    assert_contains_ws "${LOOP_OFFRAMP_BLOCK}" "${LOOP_OFFRAMP_MARKER}" \
      "#1199 regression: the attended-mode clause of the Input Needed off-ramp must stay unchanged"
  fi
fi

# =====================================================================
# #1131 -- lean decide-and-notify. In lean mode the planner DECIDES the
# contradiction and product-ambiguity questions it can justify from the repo
# (a `decided:` entry), instead of escalating them, and the human is notified
# on the ticket (one `<!-- cenci-decision -->` comment) and the PR (a
# `## Planner Decisions` section), with the PR as the overrule channel.
#
# Acceptance-criterion map (each AC has at least one explicit assertion
# below; plan descriptions are intent, not proof of coverage):
#   - AC1 self-approves through the Lean Approval Path, no Input Needed
#       -> ROUTE_ONLY_DECIDED_MARKER, LEAN_ENTRY_DECIDED_MARKER
#   - AC2 `decided:` form, distinct from `auto-adopted:`
#       -> DECIDED_FORM_MARKER (policy + Plan Output template + Clarifying
#          Questions), ROUTE_CARRY_MARKER, LEAN_STEP2_*, PERSIST_QA_*,
#          DECIDE_UNIQUE_MARKER
#   - AC3 one decision comment: banner, list, overrule line, own-line marker,
#     `decision` kind registered
#       -> DC_* markers, the registry rows
#   - AC4 ordering before `cenci pipeline plan <id>` and the label call, and
#     secrecy -> the three call-site regions + DC_SECRECY_*
#   - AC5 Phase 9 `## Planner Decisions` -> P9_* markers
#   - AC6 the three stop-class bullets byte-identical (incl. scope blowup)
#       -> SECURITY/DESTRUCTIVE/SCOPE bullet whole-line pins
#   - AC7 an unsatisfiable / literal-text-changing contradiction still stops
#       -> DECIDE_STOP_AC_MARKER (+ the rewritten #1199 (i)/(ii) markers)
#   - AC8 no repo-justifiable recommendation still stops, with a decide
#     example and a stop example -> DECIDE_BAR_*, the example blocks
#   - AC9 mixed runs: stop-class first, decide-class alongside, overflow
#     stays decided -> MIXED_* markers, UNATTENDED_*, RESUME_STEP7_OVERFLOW
#   - AC10 `approval: lean-decided` replaces lean / lean-resumed
#       -> LEAN_STEP2_APPROVAL_MARKER, RESUME_STEP6_*, CONSUMERS_*
#   - AC11 third plan-comment banner and AC12 the Go `lean-decided` fixture
#       -> plan-approval-provenance.test.sh (value table + banners),
#          TestCheckPlan_ApprovalKeyPresent_StillHealthy
#   - AC13 attended mode never decides -> attended-autonomy-contract.test.sh
#   - AC14 interactive mode never emits `decided:` -> INTERACTIVE_NEVER_MARKER
#          (policy intro + ## Clarifying Questions), ROUTE_INTERACTIVE_*
#   - AC15 docs in sync -> LOOP_* and SKILL_* markers
# =====================================================================

DECIDED_FORM_MARKER='decided: <answer> — <rationale> (alternative: <option label>)'

# --- AC6: the three stop-class bullets stay byte-for-byte unchanged. The
#     security and destructive pins are owned by the #1199 block above; the
#     scope-blowup bullet is pinned here. These PASS on the unedited file by
#     design -- they are edit guards, not red-phase assertions. -------------

SCOPE_BULLET_LINE='- **scope blowup** — the plan would need a split: size against the S/M/L table in `docs/ticket-sizing.md`. A plan whose `### Size Check` verdict is `exceeds` (the plan'"'"'s files or lines exceed the ticket'"'"'s size bound, or the plan needs a shape the letter forbids) always returns a real split in `### Split Recommendation` — never a `None —` verdict and never a question, since the Split Gate stops on it unconditionally. The planner asks a scope-blowup clarifying question only when it actually recommends a split for a ticket whose bound is not exceeded; an `L` estimate within its bound with a reasoned no-split verdict, or a size change without a split, is recorded in `### Size Estimate` / `### Split Recommendation` and goes through the Split Gate rather than being asked.'
assert_file_has_line "${PLANNER_AGENT}" "${SCOPE_BULLET_LINE}" \
  "the scope-blowup stop-class bullet must stay byte-for-byte unchanged (#1131)"

# --- agents/planner.md ## Self-Answer Policy: the class split ---------------

STOP_INTRO_MARKER='only when a question falls into one of these three stop classes'
NOTHING_OUTSIDE_MARKER='Nothing outside the three stop classes and the two decide-and-notify classes may be asked'
RETIRED_FIVE_INTRO_MARKER='these five escalation classes'
RETIRED_NOTHING_OUTSIDE_MARKER='Nothing outside these five classes may be asked'
RETIRED_FIVE_ABOVE_MARKER='the five escalation classes above'

DECIDE_LABEL_MARKER='**Decide and notify**'
DECIDE_LEAN_ONLY_MARKER='**Decide and notify** uses the same recommendation threshold in both modes, for the two decide classes'
DECIDE_STOP_AC_MARKER='A decide-class question still stops when (a) the contradiction makes an acceptance criterion unsatisfiable as written, or resolving it would change the literal text of an acceptance criterion'
DECIDE_STOP_BAR_MARKER='or (b) the planner cannot recommend an option it can justify from the repo'
DECIDE_BAR_DEF_MARKER='Justify from the repo means exactly one option, with a rationale citing the ticket, the codebase, or project docs by repo-relative path or identifier'
DECIDE_BAR_PREFERENCE_MARKER='A question whose only basis would be preference or facts outside the repo is not recommendable and stops'
DECIDE_UNIQUE_MARKER='a unique grounded resolution stays `auto-adopted:` and triggers no notification'
DECIDE_PRECEDENCE_MARKER='The security-sensitive and destructive or irreversible stop classes override decide-and-notify: when a question fits both, ask'
MIXED_ALONGSIDE_MARKER='In lean mode, when the run has at least one stop-class question, its decide-class questions are asked alongside it instead of being decided'
MIXED_ORDER_MARKER='confirm/overrule questions first, then stop-class questions, then decide-class questions, under the 6-question cap'
MIXED_OVERFLOW_MARKER='any decide-class question that does not fit under the cap stays decided and is recorded in `## Auto-Adopted Answers`'
DECIDE_SECRECY_SCOPE_MARKER='The decided answer, rationale, and alternative are held to the same restriction: repo-relative paths and identifiers only, never file contents, configuration values, or command output'
DECIDE_SECRECY_WINS_MARKER='secrecy wins over recording the decision in full'
DECIDE_FAIL_CLOSED_MARKER='If the classification is in doubt, or the alternative is missing, ask instead of deciding'
INTERACTIVE_NEVER_MARKER='interactive mode never emits a `decided:` entry'
DECIDE_EXAMPLE_START='- Decide example (d):'
STOP_EXAMPLE_START='- Stop example (f):'

if require_section POLICY_1131 "${PLANNER_CONTENT}" "## Self-Answer Policy" "planner.md (#1131)"; then
  assert_contains_ws "${POLICY_1131}" "${STOP_INTRO_MARKER}" \
    "planner.md (## Self-Answer Policy) must open the stop list with the three stop classes (security-sensitive, destructive or irreversible, scope blowup)"
  assert_contains_ws "${POLICY_1131}" "${NOTHING_OUTSIDE_MARKER}" \
    "planner.md (## Self-Answer Policy) must reword the nothing-outside sentence around three stop classes plus the two decide-and-notify classes"
  assert_absent_paired_ws "${POLICY_1131}" "${STOP_INTRO_MARKER}" "${RETIRED_FIVE_INTRO_MARKER}" \
    "planner.md (## Self-Answer Policy) must no longer introduce five escalation classes"
  assert_absent_paired_ws "${POLICY_1131}" "${NOTHING_OUTSIDE_MARKER}" "${RETIRED_NOTHING_OUTSIDE_MARKER}" \
    "planner.md (## Self-Answer Policy) must drop the retired nothing-outside-these-five-classes sentence"
  assert_absent_paired_ws "${PLANNER_CONTENT}" "${STOP_INTRO_MARKER}" "${RETIRED_FIVE_ABOVE_MARKER}" \
    "planner.md must not keep a stale five-escalation-classes reference anywhere (the ## Clarifying Questions settled-posture paragraph restates the class set, #979 restatement sites)"

  assert_contains_ws "${POLICY_1131}" "${DECIDE_LABEL_MARKER}" \
    "planner.md (## Self-Answer Policy) must add a Decide and notify rule"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_LEAN_ONLY_MARKER}" \
    "planner.md Decide and notify must share the recommendation threshold while retaining the two decide classes"
  assert_contains_ws "${POLICY_1131}" "${DECIDED_FORM_MARKER}" \
    "planner.md (## Self-Answer Policy) must define the decided: <answer> — <rationale> (alternative: <option label>) form (AC2)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_UNIQUE_MARKER}" \
    "planner.md Decide and notify must keep a unique grounded resolution as a silent auto-adopted: entry, distinct from decided: (AC2, ticket assumption)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_STOP_AC_MARKER}" \
    "planner.md Decide and notify must still stop a contradiction that makes an acceptance criterion unsatisfiable or changes its literal text (AC7)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_STOP_BAR_MARKER}" \
    "planner.md Decide and notify must still stop when no option can be justified from the repo (AC8, stop condition b)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_BAR_DEF_MARKER}" \
    "planner.md Decide and notify must define the recommendable bar: exactly one option, rationale cited by repo-relative path or identifier (AC8)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_BAR_PREFERENCE_MARKER}" \
    "planner.md Decide and notify must say a preference-only or outside-the-repo basis is not recommendable and stops (AC8)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_PRECEDENCE_MARKER}" \
    "planner.md Decide and notify must state the security-sensitive and destructive stop classes win when a question fits both (#979 precedence)"
  assert_contains_ws "${POLICY_1131}" "${MIXED_ALONGSIDE_MARKER}" \
    "planner.md Decide and notify must ask the decide-class questions alongside a stop-class question instead of deciding them (AC9)"
  assert_contains_ws "${POLICY_1131}" "${MIXED_ORDER_MARKER}" \
    "planner.md Decide and notify must keep the cap priority: confirm/overrule first, then stop-class, then decide-class (AC9, #979 anti-starvation)"
  assert_contains_ws "${POLICY_1131}" "${MIXED_OVERFLOW_MARKER}" \
    "planner.md Decide and notify must keep decide-class overflow decided and recorded (AC9)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_SECRECY_SCOPE_MARKER}" \
    "planner.md Decide and notify must hold the decided answer, rationale and alternative to repo-relative paths and identifiers only (AC4 secrecy)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_SECRECY_WINS_MARKER}" \
    "planner.md Decide and notify must state secrecy wins over recording the decision in full (#979 precedence)"
  assert_contains_ws "${POLICY_1131}" "${DECIDE_FAIL_CLOSED_MARKER}" \
    "planner.md Decide and notify must fail closed to asking when the classification is in doubt or the alternative is missing"
  assert_contains_ws "${POLICY_1131}" "${INTERACTIVE_NEVER_MARKER}" \
    "planner.md (## Self-Answer Policy) interactive-mode clause must say interactive mode never emits a decided: entry (AC14)"

  # Worked examples: one Decide example and one Stop example, each scoped to
  # its own bullet so a stray mention elsewhere cannot satisfy them (AC8).
  if require_block DECIDE_EXAMPLE_BLOCK "${POLICY_1131}" "${DECIDE_EXAMPLE_START}" "planner.md (## Self-Answer Policy) Decide example"; then
    assert_contains_ws "${DECIDE_EXAMPLE_BLOCK}" 'the codebase already has both patterns' \
      "planner.md Decide example (d) must keep the two-defensible-patterns situation it was turned from"
    assert_contains_ws "${DECIDE_EXAMPLE_BLOCK}" 'record it as a `decided:` entry' \
      "planner.md Decide example (d) must show the decision being recorded as a decided: entry, not asked"
    assert_contains_ws "${DECIDE_EXAMPLE_BLOCK}" '(alternative:' \
      "planner.md Decide example (d) must name the alternative not taken"
  fi
  if require_block STOP_EXAMPLE_BLOCK "${POLICY_1131}" "${STOP_EXAMPLE_START}" "planner.md (## Self-Answer Policy) Stop example"; then
    assert_contains_ws "${STOP_EXAMPLE_BLOCK}" 'whose only basis is preference or facts outside the repo' \
      "planner.md Stop example (f) must be a question with no repo-justifiable recommendation"
    assert_contains_ws "${STOP_EXAMPLE_BLOCK}" 'so it stops' \
      "planner.md Stop example (f) must conclude that the question stops"
  fi
fi

# --- planner.md ## Plan Output template and ## Clarifying Questions ---------

if require_section PLAN_OUTPUT_1131 "${PLANNER_CONTENT}" "## Plan Output" "planner.md (#1131)"; then
  assert_contains_ws "${PLAN_OUTPUT_1131}" "${DECIDED_FORM_MARKER}" \
    "planner.md (## Plan Output) ## Auto-Adopted Answers template must carry the decided: line (AC2)"
  assert_contains_ws "${PLAN_OUTPUT_1131}" "${AUTOADOPTED_FORMAT_MARKER}" \
    "planner.md (## Plan Output) template must keep the auto-adopted: line next to the new decided: line"
fi
if require_section CLARIFYING_1131 "${PLANNER_CONTENT}" "## Clarifying Questions" "planner.md (#1131)"; then
  assert_contains_ws "${CLARIFYING_1131}" "${INTERACTIVE_NEVER_MARKER}" \
    "planner.md (## Clarifying Questions) must say interactive mode never emits a decided: entry (AC14)"
fi

# --- phase-1-plan.md --------------------------------------------------------

if require_content PHASE1_CONTENT "${PHASE1_PLAN}" "phase-1-plan.md"; then

  # The decision-comment call-site contract, restated (never referenced) at
  # the three sites that can persist a plan with decisions: Lean step 4,
  # Persist the Plan's Mark-Planned prose, Resume From Draft step 7.
  CS_TRIGGER_MARKER='when the plan carries at least one `decided:` entry, post the `## Decision Comment`'
  CS_MENTION='post the `## Decision Comment`'
  CS_ORDER_MARKER='after the `planComment` comment and before the `cenci pipeline plan <id>` call and the label call'
  CS_NONBLOCKING_MARKER='A failed decision comment is non-blocking: report it and continue, with no retry loop'
  PLAN_CMD_LINE='cenci pipeline plan <id>'

  # --- new ## Decision Comment section --------------------------------------
  DC_BANNER1_MARKER='> 🤖 **cenci** — planner decisions posted by `/cenci:implement` (lean planning — decide and notify).'
  DC_BANNER2_MARKER='> To overrule a decision, comment on the pull request opened for this ticket; a reply on this ticket is not acted on automatically.'
  DC_MARKER_LINE='<!-- cenci-decision -->'
  DC_LIST_MARKER='lists every decided question with the option chosen and the alternative not taken'
  DC_ITEM_MARKER='1. <question> — chosen: <chosen option> (alternative: <alternative option>)'
  DC_ONE_MARKER='posts exactly one decision comment per run that made decisions'
  DC_MKDIR_MARKER='mkdir -p "${TMPDIR:-/tmp}/cenci"'
  DC_MKDIR_CHECK_MARKER='Confirm it succeeded before writing the staging file'
  DC_POST_MARKER='gh api repos/<owner>/<repo>/issues/<number>/comments -F body=@"${TMPDIR:-/tmp}/cenci/cenci-decision-<id>-<session-uuid>.md" --jq .id'
  DC_NUMERIC_MARKER='Verify the returned value is numeric before trusting it'
  DC_READBACK_CMD_MARKER='gh api repos/<owner>/<repo>/issues/comments/<id> --jq '"'"'{id, body}'"'"
  DC_READBACK_MARKER='confirm its `body` contains the exact marker `<!-- cenci-decision -->`'
  DC_RM_MARKER='rm -f "${TMPDIR:-/tmp}/cenci/cenci-decision-<id>-<session-uuid>.md" regardless of outcome'
  DC_NONBLOCKING_MARKER='is non-blocking: on any failure record it in the status summary and continue, with no retry loop'
  DC_NOREPOST_MARKER='A restarted session resumes through `## Existing Plan` and never re-posts the decision comment'
  DC_SECRECY_MARKER='repo-relative paths or identifiers only, never file contents, configuration values, or command output'
  DC_SECRECY_WINS_MARKER='secrecy wins over listing a decision in full'
  DC_TICKET_ONLY_MARKER='Ticket mode only'

  if require_section DC_SECTION "${PHASE1_CONTENT}" "## Decision Comment" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${DC_SECTION}" "${DC_BANNER1_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must open the body with the blockquoted cenci banner naming the planner decisions (AC3)"
    assert_contains_ws "${DC_SECTION}" "${DC_BANNER2_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must say overruling means commenting on the pull request, not replying on the ticket (AC3)"
    assert_has_trimmed_line "${DC_SECTION}" "${DC_MARKER_LINE}" \
      "phase-1-plan.md (## Decision Comment) must carry <!-- cenci-decision --> on its own non-blockquoted line (AC3)"
    assert_mention_precedes_line "${DC_SECTION}" '> 🤖 **cenci** — planner decisions posted by' "${DC_MARKER_LINE}" \
      "phase-1-plan.md (## Decision Comment) banner must come before the marker line (AC3)"
    assert_contains_ws "${DC_SECTION}" "${DC_LIST_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must list every decided question with its chosen option and alternative (AC3)"
    assert_contains_ws "${DC_SECTION}" "${DC_ITEM_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must show the numbered decision item shape"
    assert_contains_ws "${DC_SECTION}" "${DC_ONE_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must post exactly one comment per run that made decisions (AC3)"
    assert_contains_ws "${DC_SECTION}" "${DC_MKDIR_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must create the scoped temp directory"
    assert_contains_ws "${DC_SECTION}" "${DC_MKDIR_CHECK_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must confirm mkdir -p succeeded before writing the staging file"
    assert_contains_ws "${DC_SECTION}" "${DC_POST_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must post the session-uuid-scoped staging file through the REST comments API with --jq .id"
    assert_contains_ws "${DC_SECTION}" "${DC_NUMERIC_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must verify the returned comment ID is numeric"
    assert_contains_ws "${DC_SECTION}" "${DC_READBACK_CMD_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must read the new comment back by its own ID"
    assert_contains_ws "${DC_SECTION}" "${DC_READBACK_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must confirm the read-back body contains the marker"
    assert_contains_ws "${DC_SECTION}" "${DC_RM_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must always remove the staging file, whatever the outcome"
    assert_contains_ws "${DC_SECTION}" "${DC_NONBLOCKING_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must be non-blocking: record a failure in the status summary, no retry loop (AC3 error handling)"
    assert_contains_ws "${DC_SECTION}" "${DC_NOREPOST_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must state a restarted session never re-posts"
    assert_contains_ws "${DC_SECTION}" "${DC_SECRECY_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must restrict the body to repo-relative paths or identifiers (AC4 secrecy)"
    assert_contains_ws "${DC_SECTION}" "${DC_SECRECY_WINS_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must state secrecy wins over listing a decision in full (#979 precedence)"
    assert_contains_ws "${DC_SECTION}" "${DC_TICKET_ONLY_MARKER}" \
      "phase-1-plan.md (## Decision Comment) must be ticket mode only"
  fi
  assert_heading_order "${PHASE1_CONTENT}" "## Escalation Anchor" "## Decision Comment" \
    "phase-1-plan.md ## Decision Comment must be placed after ## Escalation Anchor"
  assert_heading_order "${PHASE1_CONTENT}" "## Decision Comment" "## Unattended Escalation Path" \
    "phase-1-plan.md ## Decision Comment must sit before ## Unattended Escalation Path"

  # --- ## Lean Approval Path -------------------------------------------------
  LEAN_ENTRY_DECIDED_MARKER='A `decided:` entry in `## Auto-Adopted Answers` is not an escalation and does not disqualify this path'
  LEAN_STEP1_ZERO_LINE_MARKER='Lean planning: no escalations — plan implicitly approved, implementing directly'
  LEAN_STEP1_DECISIONS_LINE_MARKER='Lean planning: no escalations, <n> planner decisions recorded — plan implicitly approved, implementing directly'
  LEAN_STEP2_APPROVAL_MARKER='when the planner returned at least one `decided:` entry, front matter carries `approval: lean-decided` instead of `approval: lean`'
  LEAN_STEP2_QA_MARKER='its `decided:` entries in the `decided: <answer> — <rationale> (alternative: <option label>)` form'
  LEAN_STEP8_MARKER='adds `decisions: <n>` and the decision-comment outcome'

  if require_section LEAN_1131 "${PHASE1_CONTENT}" "## Lean Approval Path" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${LEAN_1131}" "${LEAN_ENTRY_DECIDED_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) entry conditions must say a decided: entry is not an escalation (AC1)"
    if require_block LEAN_STEP1 "${LEAN_1131}" '1. Print one line, no confirmation prompt' "phase-1-plan.md (## Lean Approval Path) step 1"; then
      assert_contains_ws "${LEAN_STEP1}" "${LEAN_STEP1_ZERO_LINE_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 1 must keep the existing zero-decision print line"
      assert_contains_ws "${LEAN_STEP1}" "${LEAN_STEP1_DECISIONS_LINE_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 1 must add a decision-count variant of the print line (visible status line)"
    fi
    if require_block LEAN_STEP2 "${LEAN_1131}" '2. Write the plan file using the **same**' "phase-1-plan.md (## Lean Approval Path) step 2"; then
      assert_contains_ws "${LEAN_STEP2}" "${LEAN_STEP2_APPROVAL_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 2 must write approval: lean-decided instead of lean when there is at least one decision (AC10)"
      assert_contains_ws "${LEAN_STEP2}" "${LEAN_STEP2_QA_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 2 must carry the decided: entries into ## Q&A from Planning (AC2)"
    fi
    if require_block LEAN_STEP4 "${LEAN_1131}" '4. If `cenci.planComment: true`' "phase-1-plan.md (## Lean Approval Path) step 4"; then
      assert_contains_ws "${LEAN_STEP4}" "${CS_TRIGGER_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 4 must post the decision comment when the plan carries a decided: entry (AC3)"
      assert_contains_ws "${LEAN_STEP4}" "${CS_ORDER_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 4 must restate the ordering: after planComment, before cenci pipeline plan and the label call (AC4)"
      assert_contains_ws "${LEAN_STEP4}" "${CS_NONBLOCKING_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 4 must restate that a failed decision comment is non-blocking"
    fi
    assert_mention_precedes_line "${LEAN_1131}" "${CS_MENTION}" "${PLAN_CMD_LINE}" \
      "phase-1-plan.md (## Lean Approval Path) the decision comment must be posted before the cenci pipeline plan <id> call of step 5 (AC4)"
    if require_block LEAN_STEP8 "${LEAN_1131}" '8. Emit a 3-5 line status summary only' "phase-1-plan.md (## Lean Approval Path) step 8"; then
      assert_contains_ws "${LEAN_STEP8}" "${LEAN_STEP8_MARKER}" \
        "phase-1-plan.md (## Lean Approval Path) step 8 summary must state the decision count and the decision-comment outcome"
    fi
    if require_block LEAN_STEP3 "${LEAN_1131}" '3. Run assembly step 3'"'"'s three-heading check' "phase-1-plan.md (## Lean Approval Path) step 3"; then
      assert_contains_ws "${LEAN_STEP3}" 'On that hard stop, skip the decision comment, and skip the `planComment` comment' \
        "phase-1-plan.md (## Lean Approval Path) step 3 hard stop must also skip the decision comment"
    fi
  fi

  # --- ## Route Planner Output -----------------------------------------------
  ROUTE_CARRY_MARKER='carry any `decided:` entries into `## Q&A from Planning` verbatim as `decided: <answer> — <rationale> (alternative: <option label>)` pairs'
  ROUTE_MALFORMED_MARKER='A `decided:` entry that is malformed, or that names no alternative, is treated as a question and routed like any other question'
  ROUTE_INTERACTIVE_MARKER='A `decided:` entry received under `interactive` autonomy is treated as a question, never recorded as a decision'
  ROUTE_ONLY_DECIDED_MARKER='A run whose only planner output is `decided:` entries has `## Clarifying Questions` of `None` and takes `## Lean Approval Path` like any other no-escalation run'
  SPLIT_GATE_DECISION_MARKER='A `decided:` entry never relaxes this gate'

  if require_section ROUTE_1131 "${PHASE1_CONTENT}" "## Route Planner Output" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${ROUTE_1131}" "${ROUTE_CARRY_MARKER}" \
      "phase-1-plan.md (## Route Planner Output) must carry decided: entries into ## Q&A from Planning verbatim (AC2)"
    assert_contains_ws "${ROUTE_1131}" "${ROUTE_MALFORMED_MARKER}" \
      "phase-1-plan.md (## Route Planner Output) must turn a malformed or alternative-less decided: entry into a question (fail closed)"
    assert_contains_ws "${ROUTE_1131}" "${ROUTE_INTERACTIVE_MARKER}" \
      "phase-1-plan.md (## Route Planner Output) must turn a decided: entry under interactive autonomy into a question (AC14 backstop)"
    assert_contains_ws "${ROUTE_1131}" "${ROUTE_ONLY_DECIDED_MARKER}" \
      "phase-1-plan.md (## Route Planner Output) must route a decisions-only run through the Lean Approval Path with no Input Needed (AC1)"
    assert_contains_ws "${ROUTE_1131}" 'such an entry is rewritten into question form and appended to `## Clarifying Questions` before routing' \
      "phase-1-plan.md (## Route Planner Output) must rewrite a malformed decided: entry into question form and append it to Clarifying Questions before routing"
    assert_contains_ws "${ROUTE_1131}" 'it is removed from the `## Q&A from Planning` carry until a human answers it, and it counts toward the 6-question cap' \
      "phase-1-plan.md (## Route Planner Output) must drop a rewritten decided: entry from the Q&A carry and count it toward the 6-question cap"
    assert_contains_ws "${ROUTE_1131}" 'is rewritten and excluded from the carry the same way, so it can never flip `approval` to `lean-decided` or trigger a decision comment' \
      "phase-1-plan.md (## Route Planner Output) must exclude an interactive-autonomy decided: entry from the carry so it can never set lean-decided or trigger a decision comment"
    assert_contains_ws "${ROUTE_1131}" 'the planner still self-resolves or decides every non-escalation-class question' \
      "phase-1-plan.md (## Route Planner Output) ticketless lean bullet must say the planner self-resolves or decides"
    SPLIT_GATE_BODY="$(extract_h3_section "${ROUTE_1131}" "### Split Gate")"
    if [[ -z "${SPLIT_GATE_BODY}" ]]; then
      fail "phase-1-plan.md (#1131): could not locate the '### Split Gate' section"
    else
      assert_contains_ws "${SPLIT_GATE_BODY}" "${SPLIT_GATE_DECISION_MARKER}" \
        "phase-1-plan.md (### Split Gate) must say a decision never relaxes the gate"
    fi
  fi

  # --- ## Planner Delegation lean bullet ---------------------------------------
  DELEGATION_LEAN_BULLET_MARKER='**Lean**: ask only stop-class questions per `agents/planner.md`'"'"'s `## Self-Answer Policy`, decide the decide-class questions that are not stop cases and record them as `decided:` entries, and self-resolve everything else under `## Auto-Adopted Answers`'
  DELEGATION_RETIRED_MARKER='ask only escalation-class questions per'
  if require_section DELEGATION_1131 "${PHASE1_CONTENT}" "## Planner Delegation" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${DELEGATION_1131}" "${DELEGATION_LEAN_BULLET_MARKER}" \
      "phase-1-plan.md (## Planner Delegation) lean bullet must ask only stop-class questions and decide the rest of the decide classes"
    assert_absent_paired_ws "${DELEGATION_1131}" "${DELEGATION_LEAN_BULLET_MARKER}" "${DELEGATION_RETIRED_MARKER}" \
      "phase-1-plan.md (## Planner Delegation) must drop the retired escalation-class wording"
  fi

  # --- ## Unattended Escalation Path (unchanged except Q&A carry, Q1) -----------
  UNATTENDED_QA_MARKER='the draft'"'"'s `## Q&A from Planning` carries the planner'"'"'s `auto-adopted:` and `decided:` entries verbatim'
  UNATTENDED_OVERFLOW_MARKER='decide-class questions that did not fit under the 6-question cap stay decided in the draft'
  if require_section UNATTENDED_1131 "${PHASE1_CONTENT}" "## Unattended Escalation Path" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${UNATTENDED_1131}" "${UNATTENDED_QA_MARKER}" \
      "phase-1-plan.md (## Unattended Escalation Path) step 1 must carry auto-adopted: and decided: entries into the draft verbatim (AC9)"
    assert_contains_ws "${UNATTENDED_1131}" "${UNATTENDED_OVERFLOW_MARKER}" \
      "phase-1-plan.md (## Unattended Escalation Path) must keep decide-class overflow decided in the draft (AC9)"
    # Q1 (resolved): this path posts NO second comment -- the overflow
    # decisions are posted by Resume From Draft step 7 when the plan is
    # finalized. Paired with the carry marker so the absence is never vacuous.
    assert_absent_paired_ws "${UNATTENDED_1131}" "${UNATTENDED_QA_MARKER}" "${CS_MENTION}" \
      "phase-1-plan.md (## Unattended Escalation Path) must NOT post the decision comment (Q1: overflow decisions are posted at Resume From Draft step 7)"
  fi

  # --- ## Resume From Draft -------------------------------------------------------
  RESUME_STEP6_CARRY_MARKER='carries the draft'"'"'s `auto-adopted:` and `decided:` entries forward'
  RESUME_STEP6_APPROVAL_MARKER='when the draft'"'"'s `## Q&A from Planning` carries at least one `decided:` entry, front matter carries `approval: lean-decided` instead of `approval: lean-resumed`'
  RESUME_STEP7_OVERFLOW_MARKER='including decisions an earlier escalating run left decided in the draft'
  if require_section RESUME_1131 "${PHASE1_CONTENT}" "## Resume From Draft" "phase-1-plan.md (#1131)"; then
    if require_block RESUME_STEP6 "${RESUME_1131}" '6. **Persist the finalized plan.**' "phase-1-plan.md (## Resume From Draft) step 6"; then
      assert_contains_ws "${RESUME_STEP6}" "${RESUME_STEP6_CARRY_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 6 must carry the draft's auto-adopted: and decided: entries forward (AC10)"
      assert_contains_ws "${RESUME_STEP6}" "${RESUME_STEP6_APPROVAL_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 6 must write approval: lean-decided instead of lean-resumed when there is a decision (AC10)"
      assert_contains_ws "${RESUME_STEP6}" 'front matter carries `approval: lean-resumed`' \
        "phase-1-plan.md (## Resume From Draft) step 6 must keep lean-resumed as the no-decision value"
    fi
    if require_block RESUME_STEP7 "${RESUME_1131}" '7. **Optional `planComment`, then the load-bearing ordering.**' "phase-1-plan.md (## Resume From Draft) step 7"; then
      assert_contains_ws "${RESUME_STEP7}" "${CS_TRIGGER_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 7 must post the decision comment when the plan carries a decided: entry (AC3, Q1)"
      assert_contains_ws "${RESUME_STEP7}" "${CS_ORDER_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 7 must restate the ordering: after planComment, before cenci pipeline plan and the label call (AC4)"
      assert_contains_ws "${RESUME_STEP7}" "${CS_NONBLOCKING_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 7 must restate that a failed decision comment is non-blocking"
      assert_contains_ws "${RESUME_STEP7}" "${RESUME_STEP7_OVERFLOW_MARKER}" \
        "phase-1-plan.md (## Resume From Draft) step 7 must post the decisions an earlier escalating run left decided in the draft (Q1)"
    fi
    assert_mention_precedes_line "${RESUME_1131}" "${CS_MENTION}" "${PLAN_CMD_LINE}" \
      "phase-1-plan.md (## Resume From Draft) the decision comment must be posted before the cenci pipeline plan <id> call of step 7 (AC4)"
    assert_contains_ws "${RESUME_1131}" 'The draft'"'"'s `decided:` entries are passed to the planner as fixed decisions too' \
      "phase-1-plan.md (## Resume From Draft) step 5 must pass the draft's decided: entries to the planner as fixed decisions on the stale/unknown path"
    assert_contains_ws "${RESUME_1131}" 'Any `decided:` entry in the re-delegated planner'"'"'s return goes through `## Route Planner Output`'"'"'s same well-formedness check and carry' \
      "phase-1-plan.md (## Resume From Draft) step 5 must run the re-delegated planner's decided: entries through the same well-formedness check and carry"
    if require_block RESUME_STEP6_RETURN "${RESUME_1131}" '6. **Persist the finalized plan.**' "phase-1-plan.md (## Resume From Draft) step 6 (re-delegated return)"; then
      assert_contains_ws "${RESUME_STEP6_RETURN}" 'the same holds when the re-delegated planner'"'"'s return carries a well-formed `decided:` entry' \
        "phase-1-plan.md (## Resume From Draft) step 6 must count a re-delegated planner decided: entry for the lean-decided determination"
    fi
    if require_block RESUME_HALT "${RESUME_1131}" '**Restated for this path, not referenced**: a missing `## Ticket Details`, `## Implementation Plan`, or `## Architectural Context` means this resume' "phase-1-plan.md (## Resume From Draft) step 6 halt text"; then
      assert_contains_ws "${RESUME_HALT}" 'skip posting the optional `planComment` audit comment, skip the decision comment, skip the `cenci pipeline plan`/label/artifact calls' \
        "phase-1-plan.md (## Resume From Draft) step 6 halt text must also skip the decision comment"
    fi
    if require_block RESUME_STEP8 "${RESUME_1131}" '8. **Stop cleanly.** Leave `hasPlanFile` unset.' "phase-1-plan.md (## Resume From Draft) step 8"; then
      assert_contains_ws "${RESUME_STEP8}" 'the stop report also names the decision-comment outcome (posted, or the failure note)' \
        "phase-1-plan.md (## Resume From Draft) step 8 must name the decision-comment outcome"
    fi
  fi

  # --- ## Persist the Plan: the Mark-Planned prose (also the lean ## New Plan fallback) --
  PERSIST_MARK_HEADING='### Mark the ticket `Planned` (ticket mode only)'
  PERSIST_FALLBACK_MARKER='This also covers a lean plan persisted through `## New Plan`, whose front matter carries `approval: lean-decided` when it has a decided: entry'
  PERSIST_TICKETLESS_MARKER='In ticketless mode no decision comment is posted; the plan and the pull request body still record every decision'
  if require_section PERSIST_1131 "${PHASE1_CONTENT}" "## Persist the Plan" "phase-1-plan.md (#1131)"; then
    MARK_BODY="$(extract_h3_section "${PERSIST_1131}" "${PERSIST_MARK_HEADING}")"
    if [[ -z "${MARK_BODY}" ]]; then
      fail "phase-1-plan.md (#1131): could not locate the '${PERSIST_MARK_HEADING}' section"
    else
      assert_contains_ws "${MARK_BODY}" "${CS_TRIGGER_MARKER}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) must post the decision comment when the plan carries a decided: entry (AC3)"
      assert_contains_ws "${MARK_BODY}" "${CS_ORDER_MARKER}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) must restate the ordering: after planComment, before cenci pipeline plan and the label call (AC4)"
      assert_contains_ws "${MARK_BODY}" "${CS_NONBLOCKING_MARKER}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) must restate that a failed decision comment is non-blocking"
      assert_contains_ws "${MARK_BODY}" "${PERSIST_FALLBACK_MARKER}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) must cover the lean ## New Plan fallback (Q2)"
      assert_contains_ws "${MARK_BODY}" "${PERSIST_TICKETLESS_MARKER}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) must record decisions in the plan and PR body in ticketless mode with no comment (ticket assumption)"
      assert_mention_precedes_line "${MARK_BODY}" "${CS_MENTION}" "${PLAN_CMD_LINE}" \
        "phase-1-plan.md (## Persist the Plan, Mark-Planned) the decision comment must be posted before the cenci pipeline plan <id> call (AC4)"
    fi
  fi

  if [[ -n "${PERSIST_1131:-}" ]]; then
    assert_contains_ws "${PERSIST_1131}" 'do **not** post the `planComment` audit comment, do **not** post the decision comment, do **not** run the `Planned` label transition' \
      "phase-1-plan.md (## Persist the Plan) assembly hard stop must also skip the decision comment"
    assert_contains_ws "${PERSIST_1131}" 'add one line to this final message naming the decision-comment outcome (posted, or the failure note)' \
      "phase-1-plan.md (## Persist the Plan) final message must name the decision-comment outcome"
  fi

  # --- ## Resolve Planning Autonomy: Consumers non-consumer bullet ---------------
  CONSUMERS_PIN_MARKER='still finalizes to `approval: lean-resumed` even when the resuming session is attended'
  CONSUMERS_DECIDED_MARKER='finalizes to `approval: lean-decided` instead when the draft carries a `decided:` entry'
  if require_section RESOLVE_1131 "${PHASE1_CONTENT}" "## Resolve Planning Autonomy" "phase-1-plan.md (#1131)"; then
    assert_contains_ws "${RESOLVE_1131}" "${CONSUMERS_PIN_MARKER}" \
      "phase-1-plan.md (### Consumers) must keep the pinned lean-resumed non-consumer substring"
    assert_contains_ws "${RESOLVE_1131}" "${CONSUMERS_DECIDED_MARKER}" \
      "phase-1-plan.md (### Consumers) Resume non-consumer bullet must add the lean-decided case (AC10)"
  fi
fi

# --- phase-9-pr.md: the PR-body ## Planner Decisions section -----------------

P9_HEADING_MARKER='## Planner Decisions'
P9_OVERRULE_MARKER='A comment on this PR overrules a decision'
P9_LIST_MARKER='lists every `decided:` entry from the plan file'"'"'s `## Q&A from Planning`'
P9_OMIT_MARKER='Omit `## Planner Decisions` entirely when the plan file has no `decided:` entry'
P9_SECRECY_MARKER='repo-relative paths or identifiers only, never file contents, configuration values, or command output; secrecy wins over listing a decision verbatim'
P9_BOTH_MODES_MARKER='applies in ticket mode and in ticketless mode'

if require_content PHASE9_CONTENT "${FLOW_DIR}/skills/implement/phases/phase-9-pr.md" "phase-9-pr.md" \
   && require_section P9_PR_SECTION "${PHASE9_CONTENT}" "## PR" "phase-9-pr.md (#1131)"; then
  assert_contains_ws "${P9_PR_SECTION}" "${P9_HEADING_MARKER}" \
    "phase-9-pr.md (## PR) body template must add a ## Planner Decisions section (AC5)"
  assert_contains_ws "${P9_PR_SECTION}" "${P9_OVERRULE_MARKER}" \
    "phase-9-pr.md (## PR) ## Planner Decisions must state that a PR comment overrules a decision (AC5)"
  assert_contains_ws "${P9_PR_SECTION}" "${P9_LIST_MARKER}" \
    "phase-9-pr.md (## PR) must list every decided: entry from the plan file's ## Q&A from Planning (AC5)"
  assert_contains_ws "${P9_PR_SECTION}" "${P9_OMIT_MARKER}" \
    "phase-9-pr.md (## PR) must omit ## Planner Decisions entirely when there is no decided: entry (AC5)"
  assert_contains_ws "${P9_PR_SECTION}" "${P9_SECRECY_MARKER}" \
    "phase-9-pr.md (## PR) must restate the repo-relative-paths-only rule with secrecy winning (AC4 secrecy, #979)"
  assert_contains_ws "${P9_PR_SECTION}" "${P9_BOTH_MODES_MARKER}" \
    "phase-9-pr.md (## PR) ## Planner Decisions must apply in ticket mode and ticketless mode (ticket assumption)"
  assert_contains_ws "${P9_PR_SECTION}" 'when the plan file'"'"'s front matter says `approval: lean-decided` but zero well-formed `decided:` entries parse from its `## Q&A from Planning`, still omit the section but print a one-line warning in the status summary' \
    "phase-9-pr.md (## PR) must warn in the status summary when lean-decided front matter has no well-formed decided: entry"
fi

# --- codex.md: the autonomous-values count stays in step with the four values ----
if require_content CODEX_CONTENT "${FLOW_DIR}/skills/implement/codex.md" "codex.md (#1131)" \
   && require_block CODEX_APPROVAL_BLOCK "${CODEX_CONTENT}" '`## Persist the Plan` documents the key' "codex.md (#1131) approval-key sentence"; then
  assert_contains_ws "${CODEX_APPROVAL_BLOCK}" 'and the four autonomous values the' \
    "codex.md must say four autonomous values (lean, lean-resumed, lean-decided, trivial), not three"
  assert_absent_paired_ws "${CODEX_APPROVAL_BLOCK}" 'and the four autonomous values the' 'the three autonomous values' \
    "codex.md must drop the retired three-autonomous-values wording"
fi

# --- skills/implement/SKILL.md: session shapes 2/3/4 and the Cost Controls bullet ------

SKILL_SHAPE2_START='2. **Trivial Fast Path continues to Phase 2**'
SKILL_SHAPE3_START='3. **Escalation stop**'
SKILL_SHAPE4_START='4. **Split Gate stop**'
SKILL_COST_START='- `planning.autonomy: "lean"`'
SKILL_SHAPE2_MARKER='a plan whose open questions were all decided by the planner (recorded as `decided:` entries) self-approves the same way, persisted as `approval: lean-decided`, with one `<!-- cenci-decision -->` comment posted before the label call'
SKILL_SHAPE3_MARKER='only stop-class questions escalate here: decide-class questions are decided and notified instead'
SKILL_SHAPE3_OVERFLOW_MARKER='decide-class questions ride along in the same escalation under the 6-question cap, stop-class first, and any that do not fit stay decided in the draft'
SKILL_SHAPE4_MARKER='a planner decision never replaces the Split Gate or a scope-blowup stop'
SKILL_COST_MARKER='the planner self-resolves everything except its three stop classes (security-sensitive, destructive or irreversible, scope blowup), decides the contradiction and product-ambiguity questions it can justify from the repo, and records them as `decided:` entries'
SKILL_COST_RETIRED_MARKER='five named escalation classes'

if require_content SKILL_CONTENT "${IMPLEMENT_SKILL}" "implement/SKILL.md"; then
  if require_block SKILL_SHAPE2 "${SKILL_CONTENT}" "${SKILL_SHAPE2_START}" "implement/SKILL.md shape 2"; then
    assert_contains_ws "${SKILL_SHAPE2}" "${SKILL_SHAPE2_MARKER}" \
      "implement/SKILL.md session shape 2 must say a decided-only plan self-approves as lean-decided with one decision comment (AC15)"
  fi
  if require_block SKILL_SHAPE3 "${SKILL_CONTENT}" "${SKILL_SHAPE3_START}" "implement/SKILL.md shape 3"; then
    assert_contains_ws "${SKILL_SHAPE3}" "${SKILL_SHAPE3_MARKER}" \
      "implement/SKILL.md session shape 3 must say only stop-class questions escalate (AC15)"
    assert_contains_ws "${SKILL_SHAPE3}" "${SKILL_SHAPE3_OVERFLOW_MARKER}" \
      "implement/SKILL.md session shape 3 must say decide-class questions ride along under the cap and overflow stays decided (AC9, AC15)"
  fi
  if require_block SKILL_SHAPE4 "${SKILL_CONTENT}" "${SKILL_SHAPE4_START}" "implement/SKILL.md shape 4"; then
    assert_contains_ws "${SKILL_SHAPE4}" "${SKILL_SHAPE4_MARKER}" \
      "implement/SKILL.md session shape 4 must say a planner decision never replaces the Split Gate or scope blowup (AC15)"
  fi
  if require_block SKILL_COST "${SKILL_CONTENT}" "${SKILL_COST_START}" "implement/SKILL.md Cost Controls planning.autonomy bullet"; then
    assert_contains_ws "${SKILL_COST}" "${SKILL_COST_MARKER}" \
      "implement/SKILL.md Cost Controls planning.autonomy bullet must describe three stop classes plus decide-and-notify (#979 restatement site)"
    assert_absent_paired_ws "${SKILL_COST}" "${SKILL_COST_MARKER}" "${SKILL_COST_RETIRED_MARKER}" \
      "implement/SKILL.md Cost Controls planning.autonomy bullet must drop the five-named-classes wording"
  fi
fi

# --- docs/autonomous-loop.md ------------------------------------------------------------

LOOP_OFFRAMP_THREE_MARKER='planning hit one of three stop classes (security-sensitive, destructive/irreversible, scope blowup)'
LOOP_OFFRAMP_DECIDE_MARKER='contradictions and product ambiguities the planner can justify from the repo are decided and flagged rather than asked'
LOOP_OFFRAMP_DECIDE_STOPS_MARKER='they still stop when the contradiction makes an acceptance criterion unsatisfiable or changes its literal text, or when no option can be justified from the repo'
LOOP_DECISIONS_HEADING_MARKER='**Planner decisions.**'
LOOP_DECISIONS_COMMENT_MARKER='one comment on the ticket listing each decided question, the option chosen, and the alternative'
LOOP_DECISIONS_PRBODY_MARKER='the pull request body lists the same decisions under `## Planner Decisions`'
LOOP_DECISIONS_OVERRULE_MARKER='to overrule a decision, comment on the pull request'
LOOP_DECISIONS_TICKET_REPLY_MARKER='a reply left only on the ticket is not acted on'
LOOP_DECISIONS_AUTOMERGE_MARKER='there is no extra automerge hold: a `lean-decided` ticket carrying `automerge:ok` merges like any other, and babysit'"'"'s pre-merge feedback recheck still catches an overruling PR comment'
LOOP_DECISIONS_AFTER_MERGE_MARKER='after a merge, overruling means a revert or a follow-up ticket'
LOOP_APPROVAL_VALUES_MARKER='and `approval: lean-decided` (the plan carries at least one planner decision, and its plan comment names them)'
LOOP_REFUSES_STOP_CLASSES_MARKER='no stop-class escalation (security-sensitive, destructive or irreversible, scope blowup) and no contradiction or product-ambiguity question that must stop;'
LOOP_REFUSES_RETIRED_MARKER='no escalation in the five named classes'
LOOP_REFUSES_SENSITIVE_PATH_MARKER='no file in `### Files to Modify`/`### Files to Create` matching the sensitive-path set'

if [[ -n "${LOOP_CONTENT}" ]]; then
  if require_block LOOP_OFFRAMP_1131 "${LOOP_CONTENT}" '**`Input Needed`**' "docs/autonomous-loop.md (Input Needed off-ramp, #1131)"; then
    assert_contains_ws "${LOOP_OFFRAMP_1131}" "${LOOP_OFFRAMP_THREE_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must name the three stop classes (AC15)"
    assert_contains_ws "${LOOP_OFFRAMP_1131}" "${LOOP_OFFRAMP_DECIDE_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must say justifiable contradictions and product ambiguities are decided and flagged (AC15)"
    assert_contains_ws "${LOOP_OFFRAMP_1131}" "${LOOP_OFFRAMP_DECIDE_STOPS_MARKER}" \
      "docs/autonomous-loop.md Input Needed off-ramp must state the decide-class stop conditions (AC7, AC8, AC15)"
  fi
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_HEADING_MARKER}" \
    "docs/autonomous-loop.md must add a Planner decisions note (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_COMMENT_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must describe the ticket decision comment (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_PRBODY_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must describe the PR-body section (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_OVERRULE_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must name the PR comment as the overrule channel (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_TICKET_REPLY_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must say a ticket-only reply is not acted on (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_AUTOMERGE_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must say lean-decided adds no automerge hold and babysit still catches an overruling PR comment"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DECISIONS_AFTER_MERGE_MARKER}" \
    "docs/autonomous-loop.md Planner decisions note must say overruling after a merge means a revert or follow-up ticket"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_APPROVAL_VALUES_MARKER}" \
    "docs/autonomous-loop.md approval values paragraph must add lean-decided (AC15)"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_REFUSES_STOP_CLASSES_MARKER}" \
    "docs/autonomous-loop.md Planning refuses to guess must replace the five-named-classes bullet with the three stop classes (AC15)"
  assert_absent_paired_ws "${LOOP_CONTENT}" "${LOOP_REFUSES_STOP_CLASSES_MARKER}" "${LOOP_REFUSES_RETIRED_MARKER}" \
    "docs/autonomous-loop.md Planning refuses to guess must drop the retired five-named-classes bullet"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_REFUSES_SENSITIVE_PATH_MARKER}" \
    "docs/autonomous-loop.md Planning refuses to guess sensitive-path bullet must stay unchanged (#1131 edit guard)"
fi

# --- flow/docs/comment-attribution.md: the `decision` kind --------------------------------

ATTRIBUTION_DOC_PATH="${FLOW_DIR}/docs/comment-attribution.md"
ATTR_DECISION_ROW_MARKER='| `decision` | `skills/implement/phases/phase-1-plan.md` — `## Decision Comment`'
ATTR_DECISION_MARKER_MARKER='`<!-- cenci-decision -->`'
ATTR_SIX_KINDS_MARKER="flow's seven kinds:"
ATTR_FIVE_KINDS_MARKER="flow's six kinds:"

if require_content ATTR_CONTENT "${ATTRIBUTION_DOC_PATH}" "comment-attribution.md"; then
  assert_contains_ws "${ATTR_CONTENT}" "${ATTR_DECISION_ROW_MARKER}" \
    "comment-attribution.md registry must add the decision kind row pointing at phase-1-plan.md's ## Decision Comment (AC3)"
  assert_contains_ws "${ATTR_CONTENT}" "${ATTR_DECISION_MARKER_MARKER}" \
    "comment-attribution.md registry must record the <!-- cenci-decision --> marker (AC3)"
  assert_contains_ws "${ATTR_CONTENT}" "${ATTR_SIX_KINDS_MARKER}" \
    "comment-attribution.md must count six flow kinds once decision is registered"
  assert_absent_paired_ws "${ATTR_CONTENT}" "${ATTR_SIX_KINDS_MARKER}" "${ATTR_FIVE_KINDS_MARKER}" \
    "comment-attribution.md must drop the stale five-kinds count"
fi

echo "lean-planning-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
