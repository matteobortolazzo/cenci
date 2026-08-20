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

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing file must never masquerade as empty content, which would make
# assert_not_contains trivially pass). Must NOT be invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
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

require_doc refiner "agents/refiner.md" || true
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

# --- #978 — require a recommended option and ban entailed questions ---
#
# Why this exists: the refiner's question format let every option stand
# unranked and every open-ended question land as a bare prompt, so the user
# had to supply the judgment call the refiner was better placed to make.
# Separately, nothing stopped a question whose answer already followed from
# an earlier recorded answer — re-opening a settled decision under a new
# label. This pins both requirements down: a recommended-first option (or a
# proposed answer for open-ended questions) on every question, and a new
# "entailed" forbidden-question category that auto-adopts into
# `### Decisions` with a `follows from Q<n> (round <m>)` citation, with a
# required confirm/overrule question only when the entailed decision is
# irreversible or fixes a security posture. None of these production edits
# exist yet at RED-phase time — every assertion below is expected to fail
# until Phase 4 lands agents/refiner.md.

if [[ -n "${refiner}" ]]; then
  # AC1: every question carrying options must mark exactly one option
  # recommended, list it first, and attach a grounded one-line rationale —
  # for all question kinds, not only frontend/design.
  assert_contains "${refiner}" "Every question that carries options MUST mark exactly one option as recommended, list it first, and attach a one-line rationale grounded in cited codebase evidence or a prior recorded answer." "agents/refiner.md #978 recommended-option requirement"
  assert_contains "${refiner}" "This applies to every question kind, not only frontend/design questions." "agents/refiner.md #978 applies to all question kinds"
  assert_contains "${refiner}" "- <recommended option label> (recommended: <one-line rationale>) — <implication>" "agents/refiner.md #978 recommended-option format line"

  # AC1 (retention): the frontend propose-first bullet at :98 survives as an
  # instance of the general rule, not deleted.
  assert_contains "${refiner}" "**For frontend tickets — propose design directions instead of asking open-ended questions.**" "agents/refiner.md #978 frontend propose-first bullet retained"

  # AC2: option-less (open-ended) questions must lead with the refiner's
  # proposed answer, not a bare prompt.
  assert_contains "${refiner}" "Every open-ended question with no options MUST lead with the refiner's proposed answer, never a bare prompt." "agents/refiner.md #978 open-ended proposed-answer requirement"

  # AC4: entailment joins the forbidden-question list; entailed decisions
  # auto-adopt into `### Decisions` (never `### Assumptions (auto-adopted)`)
  # with a follows-from citation, and a confirm/overrule question is
  # required only when the entailed decision is irreversible or fixes a
  # security posture, and forbidden from re-opening the full option space.
  assert_contains "${refiner}" "**Forbidden as entailed** — a question whose answer is already fixed by a previously recorded answer; asking it again only re-opens an already-settled decision." "agents/refiner.md #978 entailment forbidden category"
  assert_contains "${refiner}" "Auto-adopt an entailed decision into \`### Decisions\` with a \`follows from Q<n> (round <m>)\` citation — never into \`### Assumptions (auto-adopted)\`." "agents/refiner.md #978 entailed decision citation"
  assert_contains "${refiner}" "When an entailed decision fixes a security posture or is otherwise irreversible, ask a confirm/overrule question that states the entailed decision and its derivation — but never one that re-opens the full option space." "agents/refiner.md #978 entailed confirm/overrule requirement"
fi

# --- evidence-grounded sizing (refine evidence/sizing fix, PR 2 of 3) -------
#
# Why this exists: split-child sizing used to be a qualitative guess
# disconnected from the refiner's own Glob/Grep exploration. This pins the
# requirement that each child's ### Size cites a bounded enumeration of the
# files/components the refiner actually found, and that the parent's own
# ### Size Estimate reasoning is grounded the same way.

if [[ -n "${refiner}" ]]; then
  assert_contains "${refiner}" "grounded in a bounded enumeration of files/components affected by that child" "agents/refiner.md evidence-grounded child sizing requirement"
  assert_contains "${refiner}" "Ground the reasoning in the specific files/components found via Glob/Grep during exploration" "agents/refiner.md evidence-grounded parent Size Estimate requirement"
fi

# --- skills/refine/SKILL.md — sonnet orchestrator, delegation, one-question relay ---

require_doc skill "skills/refine/SKILL.md" || true
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
  assert_contains "${skill}" '${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md' "skills/refine/SKILL.md"
  if [[ "${bundle_count}" -lt 2 ]]; then
    fail "skills/refine/SKILL.md: bundle file referenced fewer than 2 times (need creation + cleanup): ${bundle_count}"
  fi
  # Re-invocations carry Q&A history, never a re-pasted ticket.
  assert_contains "${skill}" "do not re-paste ticket" "skills/refine/SKILL.md"
fi

# --- skills/refine/codex.md — the Claude-only divergence is documented ---

require_doc codex "skills/refine/codex.md" || true
if [[ -n "${codex}" ]]; then
  assert_contains "${codex}" "refiner agent split is Claude-only" "skills/refine/codex.md"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "refiner-agent-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "refiner-agent-contract.test.sh: all checks passed."
