#!/usr/bin/env bash
# Contract test for ticket #575 — extract the opus refiner agent from the
# refine skill, with the skill demoted to a sonnet orchestrator that relays
# questions planner-style and performs all writes.
#
# Why this exists: a skill-level `model:` pin only lasts for the invoking
# turn, so `/cenci:refine`'s old `model: opus` frontmatter silently reverted
# to the session model on every follow-up turn of the Q&A loop. The durable
# mechanism is an agent-level pin — `agents/refiner.md` — which holds for the
# agent's entire run. This test pins that architecture down so a future edit
# can't quietly re-inline the analysis or drop the opus pin.
#
# Follows the idiom of flow/tests/subagent-cwd-contract.test.sh: a
# `failures=` counter, small assert_* helpers, exact replacement-sentence
# markers (never generic keywords — see docs/shell-scripting-gotchas.md),
# self-contained, auto-discovered by the flow gate's `*.test.sh` glob. It
# greps the real committed docs directly; no fixtures.
#
# Covered files:
#   - agents/refiner.md
#   - skills/refine/SKILL.md
#   - skills/refine/codex.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refiner-agent-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refiner-agent-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> — prints the real committed file's content,
  # or fails closed with a distinct "not found" message (a missing file must
  # never masquerade as empty content, which would make assert_not_contains
  # trivially pass).
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
}

# assert_contains <content> <required-substring> <label>
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden stale text still present: [${pattern}]"
}

# --- agents/refiner.md — the durable opus pin and the relay output protocol ---

refiner="$(read_doc "agents/refiner.md")" || true
if [[ -n "${refiner}" ]]; then
  assert_contains "${refiner}" "model: opus" "agents/refiner.md"
  assert_contains "${refiner}" "effort: high" "agents/refiner.md"
  assert_contains "${refiner}" "permissionMode: plan" "agents/refiner.md"
  # The planner-style relay protocol: numbered questions, an explicit
  # done-sentinel the orchestrator can detect unambiguously, and the final
  # proposal section the skill persists verbatim.
  assert_contains "${refiner}" "Do NOT ask questions directly" "agents/refiner.md"
  assert_contains "${refiner}" "## Questions" "agents/refiner.md"
  assert_contains "${refiner}" "None." "agents/refiner.md"
  assert_contains "${refiner}" "## Refined Ticket Proposal" "agents/refiner.md"
  # Adaptivity across rounds is bounded per round, not unbounded batching.
  assert_contains "${refiner}" "at most 4 questions per round" "agents/refiner.md"
fi

# --- skills/refine/SKILL.md — sonnet orchestrator, delegation, one-question relay ---

skill="$(read_doc "skills/refine/SKILL.md")" || true
if [[ -n "${skill}" ]]; then
  # The skill orchestrates on sonnet; the stale turn-scoped opus pin is gone.
  # Scope the model assertions to the frontmatter block (between the leading
  # `---` fences) — the body legitimately *explains* the refiner agent's
  # `model: opus` pin, so a whole-file forbid would flag correct prose.
  skill_frontmatter="$(printf '%s' "${skill}" | awk 'NR==1 && $0=="---" {inFM=1; next} inFM && $0=="---" {exit} inFM {print}')"
  assert_contains "${skill_frontmatter}" "model: sonnet" "skills/refine/SKILL.md frontmatter"
  assert_not_contains "${skill_frontmatter}" "model: opus" "skills/refine/SKILL.md frontmatter"
  # Delegation to the refiner agent must be explicit, via the Task tool.
  assert_contains "${skill}" "Task" "skills/refine/SKILL.md"
  assert_contains "${skill}" "refiner" "skills/refine/SKILL.md"
  # The one-question interaction contract — the exact sentence, not a keyword
  # (step 6's old "ONE question at a time" wording was ignored in practice;
  # this is the enforceable replacement the orchestrator must follow).
  assert_contains "${skill}" "Ask exactly ONE question per \`AskUserQuestion\` call" "skills/refine/SKILL.md"
  assert_contains "${skill}" "Never combine multiple refiner questions into a single \`AskUserQuestion\` call" "skills/refine/SKILL.md"
  # Context flows through the token-scoped verbatim bundle, and the bundle is
  # cleaned up with the run's other temp files.
  bundle_count="$(printf '%s' "${skill}" | grep -c -- "-bundle.md")"
  assert_contains "${skill}" "/tmp/claude/issue-<number>-<token>-bundle.md" "skills/refine/SKILL.md"
  if [[ "${bundle_count}" -lt 2 ]]; then
    fail "skills/refine/SKILL.md: bundle file referenced fewer than 2 times (need creation + cleanup): ${bundle_count}"
  fi
  # Re-invocations carry Q&A history, never a re-pasted ticket.
  assert_contains "${skill}" "do not re-paste ticket" "skills/refine/SKILL.md"
fi

# --- skills/refine/codex.md — the Claude-only divergence is documented ---

codex="$(read_doc "skills/refine/codex.md")" || true
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "refiner agent split is Claude-only" "skills/refine/codex.md"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "refiner-agent-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "refiner-agent-contract.test.sh: all checks passed."
