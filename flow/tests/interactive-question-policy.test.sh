#!/usr/bin/env bash
# Skills are the implementation: pin cross-client question filtering and keep
# interactive decisions separate from unattended approval/comment provenance.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
failures=0
assert_contains() {
  if ! grep -qF -- "$2" "${FLOW_DIR}/$1"; then
    printf 'FAIL: %s: %s\n' "$1" "$3" >&2
    failures=$((failures + 1))
  fi
}
assert_lacks() {
  if grep -qF -- "$2" "${FLOW_DIR}/$1"; then
    printf 'FAIL: %s: %s\n' "$1" "$3" >&2
    failures=$((failures + 1))
  fi
}

# Same question threshold, including zero questions, without changing routing.
assert_contains agents/planner.md 'Question selection is identical in interactive and lean mode' 'both planning modes must filter questions alike'
assert_contains agents/planner.md 'a cap is a ceiling, never a target' 'must not fill a question quota'
assert_contains agents/planner.md 'Interactive resolutions use `auto-adopted:`' 'interactive choices must retain human provenance'
assert_contains agents/planner.md 'including the rationale and the main alternative for a judgment call' 'judgment must remain reviewable'
assert_lacks agents/planner.md 'no self-answering' 'interactive self-answering must not be forbidden'
assert_lacks agents/planner.md 'contradiction and product-ambiguity questions are asked, never decided' 'grounded choices must not be re-asked'
assert_contains skills/implement/phases/phase-1-plan.md 'interactive mode carries both entailed and repo-grounded resolutions' 'relay must persist the expanded interactive answers'
assert_contains skills/implement/phases/phase-1-plan.md '**Both modes**: apply the shared question selection' 'delegation must activate the shared policy'
assert_contains skills/implement/phases/phase-1-plan.md '**interactive mode** presents all questions using the planner' 'interactive questions must still use the tool'
assert_contains skills/implement/phases/phase-1-plan.md 'every ticket-mode lean-mode escalation instead goes to `## Unattended Escalation Path`' 'unattended questions must still use GitHub'
assert_contains skills/implement/phases/phase-1-plan.md 'front matter records `approval: human`' 'interactive planning must retain plan review'
assert_contains skills/implement/codex.md "Apply \`agents/planner.md\`'s shared question selection" 'Codex must apply the same filter'

# Refiner agent plus native inline Codex procedure must agree on all cases.
for file in agents/refiner.md skills/refine/codex.md; do
  assert_contains "$file" 'Question selection is identical in interactive and lean mode' 'both refinement modes must filter questions alike'
  assert_contains "$file" 'a cap is a ceiling, never a target' 'must not fill a question quota'
  assert_contains "$file" 'a ticket with only conventional decisions reaches the Confirmation Gate with zero questions asked' 'conventional tickets must skip Q&A'
  assert_contains "$file" 'Interactive judgment calls go into `### Decisions` with the answer, rationale, and main alternative' 'interactive recommendations must be recorded'
  assert_contains "$file" 'Security and irreversibility take precedence over a recommendation in both modes' 'safety questions must survive filtering'
  assert_contains "$file" 'Follow-up questions require new material ambiguity introduced by an answer' 'must not turn every answer into another round'
  assert_contains "$file" 'or a still-unanswered material question deferred by the per-round cap' 'more than four genuine questions must remain answerable'
  assert_lacks "$file" 'Reference existing code/patterns when relevant: "I see we use toast notifications elsewhere' 'must not suggest asking an obvious default'
done
assert_contains skills/refine/SKILL.md 'Apply the shared question selection in `agents/refiner.md` in both modes' 'Claude relay must activate shared filtering'
assert_contains skills/refine/SKILL.md 'single `AskUserQuestion` call' 'refinement must retain tool transport'
assert_contains skills/refine/codex.md "client's available user-input mechanism" 'Codex must retain native tool transport'
assert_contains skills/refine/SKILL.md '## Confirmation Gate' 'refinement must retain human confirmation'

printf 'interactive-question-policy.test.sh: failures=%s\n' "$failures"
[[ "$failures" -eq 0 ]]
