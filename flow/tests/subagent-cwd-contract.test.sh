#!/usr/bin/env bash
# Contract test for ticket #526 — stop assuming a subagent's Bash CWD persists
# across tool calls. Bash CWD does not reliably persist across calls in this
# environment (see flow/AGENTS.md's Critical Rules and the shell-rules skill's
# "Command Shape" section, both of which state the correct rule). Several
# docs still instruct a subagent to `cd <worktree-path>` once as its first
# Bash call and rely on that CWD persisting for every later call — that is
# the stale, incorrect contract this test pins down and forbids.
#
# Follows the fixture-driven idiom of flow/tests/maintain.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. Unlike maintain.test.sh this test does
# not build synthetic fixture repos — it greps the real committed docs
# directly (relative to FLOW_DIR), since the contract under test lives in
# those docs' prose, not in generated output.
#
# Covered files (the only docs this test scans):
#   - agents/implementer.md
#   - skills/implement/phases/phase-3-test-red.md
#   - skills/implement/phases/phase-4-implement-green.md
#   - skills/implement/phases/phase-5-refactor.md
#   - skills/implement/phases/phase-6-7-review.md
#   - skills/implement/phases/phase-9-pr.md
#   - skills/configure/SKILL.md
#
# Deliberately NOT scanned: flow/AGENTS.md and flow/skills/shell-rules/SKILL.md.
# Both already state the correct rule ("CWD does not reliably persist across
# calls... use absolute paths or git -C") — that is the fix target, not a bug,
# so this test must never flag their persistence language.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "subagent-cwd-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "subagent-cwd-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> -- pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing/unreadable file must never silently masquerade as empty content,
# which would make assert_not_contains trivially "pass"). Must NOT be
# invoked via $(...).
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

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden stale text still present: [${pattern}]"
}

# assert_contains <content> <required-substring> <label>
# Asserts the SPECIFIC replacement sentence actually written at this file's
# fixed edit site is present -- not a generic marker (e.g. bare "git -C")
# that could also match unrelated pre-existing text elsewhere in the file.
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected corrected text not found: [${pattern}]"
}

# Specific corrected-text markers -- the exact (or near-exact) replacement
# sentence Phase 4 actually wrote at each file's fixed edit site. A generic
# whole-file marker set (e.g. bare "git -C") can match unrelated pre-existing
# text anywhere in the file and pass vacuously even against unfixed prose;
# asserting the literal replacement sentence gives real regression signal.
# Ticket #693 single-sourced the worktree-targeting guidance in the implementer's
# own `## Working Directory` section (its system prompt, always loaded), so the
# phase Delegation Context bullets now POINT at that section instead of
# restating it. The marker is the exact reference sentence fragment; the
# previous full restatement must be gone.
PHASE_3_4_5_MARKER='follow the implementer'"'"'s `## Working Directory` section on every command'
PHASE_3_4_5_OLD_RESTATEMENT='target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations,'
PHASE_3_4_5_OLD_NO_CD_RESTATEMENT='do **not** prefix every command with `cd <path> &&`'
HAND_RESCUE_RULE='Never hand-rescue a blocked or stranded edit'

# =====================================================================
# agents/implementer.md -- Working Directory section instructs a single
# standalone `cd` at session start and claims CWD persists for later calls.
# =====================================================================
FILE="agents/implementer.md"
if require_doc CONTENT "${FILE}"; then
  assert_not_contains "${CONTENT}" 'CWD persists between Bash calls' "${FILE}"
  assert_contains "${CONTENT}" 'Bash CWD does not reliably persist across calls. Use `git -C <worktree-path> ...` for git commands, absolute paths for file operations' "${FILE}"
  # The section the phases now point at must exist, with its own hand-rescue rule.
  assert_contains "${CONTENT}" '## Working Directory' "${FILE}"
  assert_contains "${CONTENT}" "**${HAND_RESCUE_RULE}.**" "${FILE}"
  assert_contains "${CONTENT}" 'target it explicitly on every command' "${FILE}"
fi

# =====================================================================
# phase-3-test-red.md, phase-4-implement-green.md, phase-5-refactor.md --
# all three Delegation Context sections tell the main agent to instruct the
# implementer to enter the worktree with a standalone `cd` as the first Bash
# call and claim CWD persists for later calls.
# =====================================================================
for FILE in \
  "skills/implement/phases/phase-3-test-red.md" \
  "skills/implement/phases/phase-4-implement-green.md" \
  "skills/implement/phases/phase-5-refactor.md"
do
  if require_doc CONTENT "${FILE}"; then
    assert_not_contains "${CONTENT}" 'CWD persists for later calls' "${FILE}"
    assert_not_contains "${CONTENT}" 'standalone `cd <worktree-path>` as the first Bash call' "${FILE}"
    assert_contains "${CONTENT}" "${PHASE_3_4_5_MARKER}" "${FILE}"
    assert_not_contains "${CONTENT}" "${PHASE_3_4_5_OLD_RESTATEMENT}" "${FILE}"
    assert_not_contains "${CONTENT}" "${PHASE_3_4_5_OLD_NO_CD_RESTATEMENT}" "${FILE}"
  fi
done

# Phase 4 keeps the never-hand-rescue rule as a precise reference to the
# implementer section that holds it.
FILE="skills/implement/phases/phase-4-implement-green.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "${HAND_RESCUE_RULE}" "${FILE}"
  assert_contains "${CONTENT}" 'after two failed attempts at the same unexplained defect' "${FILE}"
fi

# Each phase keeps its own constraints next to the shortened worktree bullet.
FILE="skills/implement/phases/phase-3-test-red.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" 'other workers may be editing it, never revert their changes, never run git state mutations, and write only its owned files' "${FILE}"
  assert_contains "${CONTENT}" 'up to 3 attempts within its lane' "${FILE}"
  assert_contains "${CONTENT}" 'A worker summary opening with `Context: low`' "${FILE}"
fi
FILE="skills/implement/phases/phase-5-refactor.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" 'focused cleanup of touched code only' "${FILE}"
  assert_contains "${CONTENT}" 'Behavior must not change.' "${FILE}"
fi

# =====================================================================
# phase-6-7-review.md -- Shared Context step tells the main agent to run a
# standalone `cd <worktree-path>` before the git diff commands so they
# resolve against the worktree.
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if require_doc CONTENT "${FILE}"; then
  assert_not_contains "${CONTENT}" 'run a standalone `cd <worktree-path>` before these commands' "${FILE}"
  assert_contains "${CONTENT}" 'Target the worktree explicitly with `git -C <worktree-path>` on each `git diff` call so it resolves against the worktree and stays auto-approved; redirect to an absolute temp-file path' "${FILE}"
fi

# =====================================================================
# phase-9-pr.md -- tells the main agent to run a standalone `cd
# <worktree-path>` before the rebase/commit/push commands so they resolve
# against the worktree.
# =====================================================================
FILE="skills/implement/phases/phase-9-pr.md"
if require_doc CONTENT "${FILE}"; then
  assert_not_contains "${CONTENT}" 'run a standalone `cd <worktree-path>` before the rebase/commit/push commands' "${FILE}"
  assert_contains "${CONTENT}" 'Target the worktree explicitly with `git -C <worktree-path>` on every rebase/commit/push command below so they resolve against the worktree and stay auto-approved' "${FILE}"
fi

# =====================================================================
# skills/configure/SKILL.md -- the commit step (git-workflow.md read +
# commit/branch/PR conventions) tells the agent to run a standalone `cd
# <worktree-path>` first so the commands below resolve against the worktree.
# =====================================================================
FILE="skills/configure/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  assert_not_contains "${CONTENT}" 'Run a standalone `cd <worktree-path>` first so the commands below resolve against the worktree' "${FILE}"
  assert_contains "${CONTENT}" 'Target the worktree explicitly with `git -C <worktree-path>` on every command below so they resolve against the worktree and stay auto-approved' "${FILE}"
fi

echo "subagent-cwd-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
