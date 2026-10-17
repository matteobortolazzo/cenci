#!/usr/bin/env bash
# Contract test for the Velka ticket-size and split-coverage rules in cenci
# (docs/ticket-sizing.md mirrors Velka's docs/dev/workflow/branches-and-tickets.md
# "Ticket sizes"/"Splitting a ticket" and docs/agent-workflow.md "Lane size and
# context budget"). Pins the documented behavior at every enforcement point so
# a later edit cannot quietly drop a bound, a stop, or a refusal.
#
# Follows the fixture-free, grep-based idiom of flow/tests/parent-close-
# verification.test.sh: `set -uo pipefail`, a `failures` counter,
# assert_file_contains/assert_file_lacks on `grep -qF`, markers kept on a
# single source line (docs/shell-scripting-gotchas.md). The executable halves
# of these rules have their own behavioral suites: coverage-map.test.sh,
# acceptance-verdict.test.sh, pr-size.test.sh, close-parent.test.sh.
#
# Coverage (the ticket's "Tests and docs" list):
#   1 size letter and bound written at refinement
#   2 vague acceptance criteria rejected
#   3 planner stop on over-size
#   4 PR body size line and mismatch flag
#   5 acceptance reviewer blocking on a missing item, passing on full coverage
#   6 split refusing when an item has no owner
#   7 coverage map written on the parent
#   8 parent closure refusing with a missing item and creating the child
#   9 closure succeeding with full coverage
#  10 user docs for refine, plan, review, split and close; Codex companions;
#     the Opus-tier acceptance-reviewer role; the automerge gate at the S bound
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "ticket-size-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "ticket-size-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "ticket-size-contract.test.sh: failed to resolve repo directory." >&2; exit 2; }
failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_file_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_file_lacks() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}

SIZING="${FLOW_DIR}/docs/ticket-sizing.md"
REFINER="${FLOW_DIR}/agents/refiner.md"
REFINE="${FLOW_DIR}/skills/refine/SKILL.md"
REFINE_CODEX="${FLOW_DIR}/skills/refine/codex.md"
PLANNER="${FLOW_DIR}/agents/planner.md"
PHASE1="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
PHASE3="${FLOW_DIR}/skills/implement/phases/phase-3-test-red.md"
PHASE4="${FLOW_DIR}/skills/implement/phases/phase-4-implement-green.md"
PHASE67="${FLOW_DIR}/skills/implement/phases/phase-6-7-review.md"
PHASE9="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
IMPLEMENT="${FLOW_DIR}/skills/implement/SKILL.md"
IMPLEMENT_CODEX="${FLOW_DIR}/skills/implement/codex.md"
IMPLEMENTER="${FLOW_DIR}/agents/implementer.md"
ACCEPTANCE="${FLOW_DIR}/agents/acceptance-reviewer.md"
REVIEW="${FLOW_DIR}/skills/review/SKILL.md"
REVIEW_CODEX="${FLOW_DIR}/skills/review/codex.md"
CLOSE="${FLOW_DIR}/skills/close/SKILL.md"
CLOSE_CODEX="${FLOW_DIR}/skills/close/codex.md"
README="${FLOW_DIR}/README.md"
ATTRIBUTION="${FLOW_DIR}/docs/comment-attribution.md"
ROLE_TEMPLATE="${FLOW_DIR}/templates/codex/agent-roles/acceptance-reviewer.toml"
PLANNER_TEMPLATE="${FLOW_DIR}/templates/codex/agent-roles/planner.toml"
INSTALLED_ROLE="${REPO_DIR}/.codex/agents/acceptance-reviewer.toml"
CONFIG="${REPO_DIR}/.cenci/config.json"

S_BOUND='Bound: <= 10 changed files, <= 400 changed lines, one concern in one project, no schema change, no automerge protected path.'
M_BOUND='Bound: <= 30 changed files, <= 2,000 changed lines, one feature slice, at most one migration.'
L_BOUND='Bound: <= 60 changed files, <= 5,000 changed lines, several coupled slices; reason they cannot ship as a stacked series of M PRs: <reason>.'

# --- 1. size letter and bound written at refinement ----------------------------
for f in "${SIZING}" "${REFINER}"; do
  assert_file_contains "$f" '### Size Estimate: S' "carries the S heading form"
  assert_file_contains "$f" '### Size Estimate: M' "carries the M heading form"
  assert_file_contains "$f" '### Size Estimate: L' "carries the L heading form"
  assert_file_contains "$f" "${S_BOUND}" "carries the S bound line verbatim"
  assert_file_contains "$f" "${M_BOUND}" "carries the M bound line verbatim"
  assert_file_contains "$f" "${L_BOUND}" "carries the L bound line verbatim"
done
assert_file_contains "${SIZING}" 'insertions plus deletions over the whole PR, generated files and lockfiles' "defines changed lines as insertions plus deletions incl. generated files and lockfiles"
assert_file_contains "${SIZING}" 'split, never written as one ticket' "anything beyond L is split"
assert_file_contains "${SIZING}" 'model capability is never a reason to size up' "model capability never sizes up"
assert_file_contains "${REFINER}" '### Size Estimate: <S|M|L>' "refiner proposal writes the letter in the heading"
assert_file_contains "${REFINER}" 'Rationale: <one or two sentences>' "refiner writes a one-or-two-sentence rationale"
assert_file_contains "${REFINER}" '**M is the default**' "refiner: M is the default"
assert_file_contains "${REFINE}" '#### Size Estimate check (every proposal)' "refine skill checks the size estimate before any write"
assert_file_contains "${REFINE}" 'matches that letter'"'"'s bound line **verbatim**' "refine skill requires the bound line verbatim"
assert_file_contains "${REFINE}" 'an `L` whose bound still reads `<reason>`' "refine skill rejects an L without a recorded reason"
assert_file_contains "${REFINE}" '`### Size Estimate: <letter>` (with its `Bound:` and `Rationale:` lines)' "refine skill persists the size section into the body"
assert_file_contains "${REFINE_CODEX}" '`### Size Estimate: <S|M|L>` with the letter'"'"'s bound line copied verbatim' "codex refine mirrors the size format"

# --- 2. vague acceptance criteria rejected --------------------------------------
assert_file_contains "${REFINE}" '#### Acceptance-criteria concreteness check (every proposal)' "refine skill has the concreteness check"
assert_file_contains "${REFINE}" '`works correctly`, `works as expected`, `works properly`, `handles edge cases`, `handles errors gracefully`' "refine skill names the vague shapes"
assert_file_contains "${REFINE}" 'vague: names no observable behavior, state or output' "refine skill reports the rejection reason"
assert_file_contains "${REFINE}" '`### Edge Cases` must be present, with either one concrete `- [ ]` item per edge case or exactly `None.`' "refine skill requires individually listed edge cases"
assert_file_contains "${REFINER}" 'Reject and rewrite any criterion of the "works correctly", "works as expected", "handles edge cases", "handles errors gracefully", "is robust/performant/secure" shape' "refiner rejects vague criteria"
assert_file_contains "${REFINER}" '### Edge Cases' "refiner proposal carries an Edge Cases section"
assert_file_contains "${REFINER}" 'Edge cases are listed individually, never compressed into a heading' "refiner lists edge cases individually"
assert_file_contains "${REFINE_CODEX}" 'stops the run before any write (the acceptance-criteria concreteness check)' "codex refine mirrors the concreteness stop"

# --- 3. planner stop on over-size -----------------------------------------------
assert_file_contains "${PLANNER}" '### Size Check' "planner has a Size Check section"
assert_file_contains "${PLANNER}" 'Verdict: within | exceeds | unknown' "planner Size Check verdict vocabulary"
assert_file_contains "${PLANNER}" '**Growth during planning is a split trigger, not a reason to continue**' "planner: growth is a split trigger"
assert_file_contains "${PLANNER}" 'Compare the plan against the ticket'"'"'s bound **right after Files to Modify/Create' "planner compares right after Files to Modify"
assert_file_contains "${PLANNER}" '### Delegation Preflight' "planner emits a Delegation Preflight"
assert_file_contains "${PLANNER}" '**A lane is at most one M**' "planner: each lane at most one M"
assert_file_contains "${PLANNER}" '**An L ticket is always several lanes** with exclusive file ownership, never one lane' "planner: L is several lanes"
assert_file_contains "${PLANNER}" 'This is the **confirmed size** recorded in the plan' "planner records the confirmed size"
assert_file_contains "${PHASE1}" '**Over-bound plan — the third trigger, a hard stop in every mode.**' "phase 1 Split Gate fires on an over-bound plan"
assert_file_contains "${PHASE1}" 'offers **no Proceed option and no reasoned-proceed carve-out, in any mode**' "phase 1: over-bound has no Proceed"
assert_file_contains "${PHASE1}" 'The run stops before any implementation mutation exactly as the Stop branch below' "phase 1: stops before any implementation mutation"
assert_file_contains "${PHASE1}" '**The confirmed size lives in the plan.**' "phase 1 persists the confirmed size"
assert_file_contains "${PHASE3}" '## Delegation Preflight Re-check' "phase 3 re-checks the preflight before the first mutation"
assert_file_contains "${PHASE3}" '## Context Budget' "phase 3 defines the context budget brief"
assert_file_contains "${PHASE3}" 'about half of your model'"'"'s advertised context window' "phase 3 brief: half the window"
assert_file_contains "${PHASE3}" 'Automatic compaction is disabled (`autoCompactDisabled` in `.cenci/config.json`)' "phase 3 brief: autocompact disabled"
assert_file_contains "${PHASE3}" 'is **never** retried with a longer lane' "phase 3: low context is never retried with a longer lane"
assert_file_contains "${PHASE3}" 'reports it to the root agent as a split signal' "phase 3: reported to the root agent as a split signal"
assert_file_contains "${PHASE4}" '**Context-budget signal.**' "phase 4 handles the context-budget signal"
assert_file_contains "${IMPLEMENTER}" '> **Context budget**: Your working budget is about half of your model'"'"'s advertised context window' "implementer is briefed with the budget"
assert_file_contains "${IMPLEMENTER}" '`Context: low — done: <what>; remaining: <what>`' "implementer reports low context in a fixed shape"
assert_file_contains "${IMPLEMENT}" "4. **Split Gate stop** — when the planner's \`### Size Check\` verdict is \`exceeds\`" "implement skill names the over-bound stop"
assert_file_contains "${IMPLEMENT_CODEX}" 'A `### Size Check` verdict of `exceeds` (growth' "codex implement mirrors the over-bound stop"
assert_file_contains "${IMPLEMENT_CODEX}" 'working budget is about half of its model'"'"'s advertised' "codex implement mirrors the context budget brief"

# --- 4. PR body size line and mismatch flag ------------------------------------
PR_SIZE="${FLOW_DIR}/skills/implement/scripts/pr-size.sh"
[[ -f "${PR_SIZE}" ]] || fail "scripts/pr-size.sh must exist"
assert_file_contains "${PHASE9}" '## Size' "phase 9 has a Size step"
assert_file_contains "${PHASE9}" 'bash "${CLAUDE_PLUGIN_ROOT}/skills/implement/scripts/pr-size.sh" "<worktree-path>" origin/main --plan' "phase 9 invokes pr-size.sh against the plan"
assert_file_contains "${PHASE9}" 'Size: <letter> (<files> files, <lines> lines) vs ticket <letter>' "phase 9 PR body carries the size line"
assert_file_contains "${PHASE9}" '**A PR one size above its ticket is flagged, not fixed here.**' "phase 9 flags a PR one size above its ticket"
assert_file_contains "${PHASE9}" 'Such a PR **must not be merged on green checks alone**' "phase 9: not merged on green checks alone"
assert_file_contains "${PHASE9}" 'the final session summary says so to the founder' "phase 9 flags the mismatch to the founder"
assert_file_contains "${PHASE9}" '- [ ] Size: one above ticket — founder decision required before merge' "phase 9 checklist line for the mismatch"
assert_file_contains "${PR_SIZE}" 'founder decision required; do not merge on green checks alone' "pr-size.sh emits the founder flag"
assert_file_contains "${IMPLEMENT_CODEX}" 'A PR one size above its ticket is' "codex implement mirrors the mismatch flag"

# --- 5. acceptance reviewer: blocks on a missing item, passes on full coverage ----
[[ -f "${ACCEPTANCE}" ]] || fail "agents/acceptance-reviewer.md must exist"
assert_file_contains "${ACCEPTANCE}" 'model: opus' "acceptance-reviewer is on the Opus tier"
assert_file_contains "${ACCEPTANCE}" 'effort: high' "acceptance-reviewer pins high effort"
assert_file_contains "${ACCEPTANCE}" 'permissionMode: plan' "acceptance-reviewer is read-only"
assert_file_contains "${ACCEPTANCE}" '**Refuse to proceed on fewer**' "acceptance-reviewer refuses a diff-only review"
assert_file_contains "${ACCEPTANCE}" '**Read the ticket before the code.**' "acceptance-reviewer reads the ticket before the code"
assert_file_contains "${ACCEPTANCE}" 'Never emit a code-quality comment' "acceptance-reviewer makes no code-quality comments"
assert_file_contains "${ACCEPTANCE}" '`unrequested` — built, but no item asked for it' "acceptance-reviewer reports unrequested work"
assert_file_contains "${ACCEPTANCE}" '**`en-US` and `it-IT`**' "acceptance-reviewer requires both locales for UI tickets"
assert_file_contains "${ACCEPTANCE}" 'A UI ticket reviewed from the diff alone, or with a locale or source missing, is a' "acceptance-reviewer: diff-only UI review is a failure"
assert_file_contains "${PHASE67}" '## Acceptance Review' "phase 6+7 runs the acceptance review"
assert_file_contains "${PHASE67}" 'Runs after Shared Context and **before** Review Path Classification, on every path' "phase 6+7 acceptance review runs before code review on every path"
assert_file_contains "${PHASE67}" 'bash "${CLAUDE_PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" "$RUN_DIR/acceptance.md"' "phase 6+7 scores the table mechanically"
assert_file_contains "${PHASE67}" '**Fail → back to implementation, before any code review.**' "phase 6+7: a fail returns to implementation before code review"
assert_file_contains "${PHASE67}" '**Pass → continue** to Review Path Classification. Only a `verdict=pass` ever reaches the code reviewers.' "phase 6+7: only a pass reaches the code reviewers"
assert_file_contains "${PHASE9}" '## Acceptance' "phase 9 PR body carries the acceptance table"
assert_file_contains "${PHASE9}" 'bash "${CLAUDE_PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" "$RUN_DIR/acceptance.md" --ui' "phase 9 re-runs the UI verdict after the gist upload"
assert_file_contains "${REVIEW}" '## Phase 1b: Acceptance Review (PR mode only)' "review skill grades acceptance before code review"
assert_file_contains "${REVIEW}" '**Fail → stop before code review.**' "review skill stops before the trio on a fail"
assert_file_contains "${REVIEW}" 'NEEDS_WORK (acceptance)' "review skill has the acceptance verdict"
assert_file_contains "${REVIEW_CODEX}" 'grade acceptance before code' "codex review mirrors acceptance-first"

# --- 6. split refusing when an item has no owner; 7. coverage map written -------
COVMAP="${FLOW_DIR}/skills/refine/scripts/coverage-map.sh"
[[ -f "${COVMAP}" ]] || fail "scripts/coverage-map.sh must exist"
assert_file_contains "${REFINER}" '#### Parent items' "refiner enumerates the parent items"
assert_file_contains "${REFINER}" 'Enumerate **every** acceptance criterion, edge case, verification step, documentation item and boundary of the parent' "refiner enumerates every item kind"
assert_file_contains "${REFINER}" 'If an item fits no child, the split is wrong' "refiner: an unowned item fails the split"
assert_file_contains "${REFINER}" '**A split child is at most M unless it records why it is L.**' "refiner: child at most M unless records why L"
assert_file_contains "${REFINER}" 'Series: (K/N) — previous: Ticket K-1 "<title>" | next: Ticket K+1 "<title>"' "refiner: series position and sibling names"
assert_file_contains "${REFINE}" '**Coverage-map check (mechanical).**' "refine skill runs the mechanical coverage check"
assert_file_contains "${REFINE}" '**Exit 3 — an item has no owner: the split fails.**' "refine skill fails the split on an unowned item"
assert_file_contains "${REFINE}" 'never drop the item and never proceed with a partial map' "refine skill never drops an item"
assert_file_contains "${REFINE}" '**(b) Post-creation coverage self-check (every split).**' "refine skill self-checks after creation"
assert_file_contains "${REFINE}" '**(c) Coverage map and Execution Order.**' "refine skill writes the coverage map on the parent"
assert_file_contains "${REFINE}" 'never a rewrite of the parent'"'"'s own `### Acceptance Criteria` or `### Edge Cases`, which stay exactly as step 10 wrote them' "refine skill leaves the parent's criteria unchanged"
assert_file_contains "${REFINE}" 'Coverage map: `<items>` parent items, `<owned>` owned, `<N>` owner children — post-creation self-check passed.' "refine final message reports item and owner counts"
assert_file_contains "${REFINE}" 'Its own `### Size Estimate: <letter>` section — the letter, `Bound:` line and `Rationale:` from the child block' "each child gets its own Size Estimate"
assert_file_contains "${REFINE}" 'Its `Series:` line, copied from the proposal'"'"'s child block' "each child carries its series line"
assert_file_contains "${REFINE_CODEX}" 'exit 3 (an item with no owner) or 4' "codex refine mirrors the coverage-map refusal"
assert_file_contains "${REFINE_CODEX}" 'yields the rendered `### Coverage map`, which Pass 2 appends to the parent body' "codex refine mirrors the coverage map write"

# --- 8. parent closure refusing with a missing item and creating the child; 9. closing on full coverage
CLOSE_SH="${FLOW_DIR}/skills/close/scripts/close-parent.sh"
[[ -f "${CLOSE}" ]] || fail "skills/close/SKILL.md must exist"
[[ -f "${CLOSE_SH}" ]] || fail "scripts/close-parent.sh must exist"
assert_file_contains "${CLOSE}" 'name: close' "close skill frontmatter"
assert_file_contains "${CLOSE}" 'user-invocable: true' "close skill is user-invocable"
assert_file_contains "${CLOSE}" '- **Any child not `CLOSED`**: **refuse**.' "close refuses while a child is open"
assert_file_contains "${CLOSE}" 'There is no override option.' "close has no override for open children"
assert_file_contains "${CLOSE}" '**merged child PRs, their tests and docs are the only admissible evidence**' "close compares against merged PRs"
assert_file_contains "${CLOSE}" 'never grade an item from a child issue body' "close never grades from child bodies"
assert_file_contains "${CLOSE}" 'the **parent'"'"'s milestone**' "close: gap children get the parent's milestone"
assert_file_contains "${CLOSE}" 'The parent stays open.' "close: gaps leave the parent open"
assert_file_contains "${CLOSE}" 'Zero partial/missing items: the parent is closed as completed' "close: zero gaps closes"
assert_file_contains "${CLOSE}" 'There is no "close anyway" option: a partial or missing item never closes the parent.' "close: no close-anyway"
assert_file_contains "${CLOSE}" '<!-- cenci-parent-close-review -->' "close posts the review with its marker"
assert_file_contains "${CLOSE_SH}" 'exit 4' "close-parent.sh exits 4 on gaps"
assert_file_contains "${CLOSE_SH}" 'gh issue close "${PARENT}" --repo "${REPO}" --reason completed' "close-parent.sh closes as completed"
assert_file_contains "${CLOSE_CODEX}" 'name every open child and stop, with no override' "codex close mirrors the refusal"
assert_file_contains "${CLOSE_CODEX}" 'creates one new child under the same parent per' "codex close mirrors gap-child creation"
assert_file_contains "${ATTRIBUTION}" '| `parent-close-review` |' "comment-attribution registers the parent-close-review kind"

# --- 10. user docs, Codex role, automerge gate at the S bound ------------------------
assert_file_contains "${README}" '| `/cenci:close <parent-ticket-id>` |' "README lists /cenci:close"
assert_file_contains "${README}" '## Ticket sizes' "README documents the size table"
assert_file_contains "${README}" '| **M** (default) |' "README: M is the default"
assert_file_contains "${README}" 'Splitting is a **coverage exercise**' "README documents the coverage split"
assert_file_contains "${README}" '6. **Acceptance Review**' "README documents the acceptance review phase"
assert_file_contains "${README}" '| **acceptance-reviewer** |' "README architecture table lists the acceptance reviewer"
assert_file_contains "${REPO_DIR}/docs/orchestration.md" '`/cenci:close <parent>`' "orchestration doc documents the close step"
[[ -f "${ROLE_TEMPLATE}" ]] || fail "Codex acceptance-reviewer role template must exist"
if [[ -f "${ROLE_TEMPLATE}" && -f "${PLANNER_TEMPLATE}" ]]; then
  role_model="$(grep -m1 '^model = ' "${ROLE_TEMPLATE}")"; planner_model="$(grep -m1 '^model = ' "${PLANNER_TEMPLATE}")"
  [[ "${role_model}" == "${planner_model}" ]] || fail "acceptance-reviewer Codex role must pin the same model as the planner role (got ${role_model} vs ${planner_model})"
  assert_file_contains "${ROLE_TEMPLATE}" 'model_reasoning_effort = "high"' "Codex acceptance-reviewer role pins high effort"
fi
if [[ -f "${INSTALLED_ROLE}" && -f "${ROLE_TEMPLATE}" ]]; then
  cmp -s "${INSTALLED_ROLE}" "${ROLE_TEMPLATE}" || fail ".codex/agents/acceptance-reviewer.toml drifted from its template"
else
  fail "the repo's installed .codex/agents/acceptance-reviewer.toml must exist"
fi
if command -v jq >/dev/null 2>&1 && [[ -f "${CONFIG}" ]]; then
  while IFS=$'\t' read -r slug files lines; do
    [[ "${files}" =~ ^[0-9]+$ && "${lines}" =~ ^[0-9]+$ && "${files}" -le 10 && "${lines}" -le 400 ]] || fail ".cenci/config.json project '${slug}' automerge gate must stay within the S bound (<= 10 files / <= 400 lines), got ${files}/${lines}"
  done < <(jq -r '.projects[] | select(.automerge != null) | [.slug, (.automerge.maxChangedFiles|tostring), (.automerge.maxDiffLines|tostring)] | @tsv' "${CONFIG}")
fi

echo "ticket-size-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
