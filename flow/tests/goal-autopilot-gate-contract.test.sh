#!/usr/bin/env bash
# Contract test for ticket #557 — stop probing Goal Autopilot availability via
# `claude --version` semver parsing. In containerized environments `SlashCommand
# /goal` can work fine even when no `claude` binary is on PATH, so shelling out
# to `claude --version` and gating on `>= 2.1.139` produces a false negative:
# the pipeline silently skips a completion guarantee it could have had. The
# fix replaces that local-binary probe with a direct `/goal` arming attempt via
# the `SlashCommand` tool, relying on the already-documented error fallback
# (missing tool / unknown command / error => treat as unavailable) instead of
# a separate live version check.
#
# Follows the fixture-driven idiom of flow/tests/subagent-cwd-contract.test.sh:
# a `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. This test greps the real committed docs
# directly (relative to FLOW_DIR) since the contract under test lives in those
# docs' prose, not in generated output.
#
# Covered files (the only docs this test scans):
#   - skills/implement/SKILL.md        ("Availability gate" section, step 2,
#                                        and the Cost Controls
#                                        `cenci.goalAutopilot` entry)
#   - skills/implement/phases/phase-2-worktree.md ("Arm Goal Autopilot" step 2)
#
# See docs/shell-scripting-gotchas.md rule 3: assert the specific replacement
# text at the edit site, never a generic marker that could already exist
# elsewhere in the file — generic markers pass vacuously against unfixed prose.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "goal-autopilot-gate-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "goal-autopilot-gate-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> -- prints the real committed file's content,
  # or fails closed with a distinct "not found" message if it cannot be read
  # (a missing/unreadable file must never silently masquerade as empty
  # content, which would make assert_not_contains trivially "pass").
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
}

# assert_not_contains <content> <forbidden-substring> <label>
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden stale text still present: [${pattern}]"
}

# assert_contains <content> <required-substring> <label>
# Asserts the SPECIFIC replacement sentence expected at this file's fixed edit
# site is present -- not a generic marker (e.g. bare "SlashCommand") that
# could also match unrelated pre-existing text elsewhere in the file.
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected corrected text not found: [${pattern}]"
}

# =====================================================================
# Forbidden mechanic markers -- the exact live-check phrasing this ticket
# removes. Present verbatim in today's unfixed prose at each site (that is
# what makes this test genuinely red before the fix lands).
# =====================================================================
FORBIDDEN_VERSION_PROBE_SKILL='Run `claude --version` and parse the leading semver. If it is ≥ `2.1.139`, arm the goal (below).'
FORBIDDEN_VERSION_PROBE_PHASE2='Otherwise run `claude --version` and version-gate on ≥ 2.1.139.'
FORBIDDEN_COST_CONTROLS='arms a `/goal` completion condition at Phase 2 start when Claude Code ≥ 2.1.139'

# =====================================================================
# Required corrected-text markers -- the direct-attempt-and-fallback
# mechanic and one-line unavailable notice the fix must introduce. Absent
# from today's unfixed prose (also making this test red before the fix).
# =====================================================================
ATTEMPT_MARKER='attempt to arm `/goal` directly via the `SlashCommand` tool, treating a missing tool, unknown command, or error as Goal Autopilot being unavailable'
NOTICE_MARKER='Goal autopilot unavailable (/goal not supported in this session) — running without a completion guarantee.'

# =====================================================================
# skills/implement/SKILL.md -- the "Version + availability gate" section
# (step 2) shells out to `claude --version` and semver-gates on 2.1.139;
# the Cost Controls `cenci.goalAutopilot` entry repeats the same stale
# version-dependent phrasing.
# =====================================================================
FILE="skills/implement/SKILL.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_not_contains "${CONTENT}" "${FORBIDDEN_VERSION_PROBE_SKILL}" "${FILE}"
  assert_not_contains "${CONTENT}" "${FORBIDDEN_COST_CONTROLS}" "${FILE}"
  assert_contains "${CONTENT}" "${ATTEMPT_MARKER}" "${FILE}"
  assert_contains "${CONTENT}" "${NOTICE_MARKER}" "${FILE}"
fi

# =====================================================================
# skills/implement/phases/phase-2-worktree.md -- the "Arm Goal Autopilot"
# section's step 2 shells out to `claude --version` and version-gates on
# 2.1.139 before ever attempting to invoke `/goal`.
# =====================================================================
FILE="skills/implement/phases/phase-2-worktree.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_not_contains "${CONTENT}" "${FORBIDDEN_VERSION_PROBE_PHASE2}" "${FILE}"
  assert_contains "${CONTENT}" "${ATTEMPT_MARKER}" "${FILE}"
fi

echo "goal-autopilot-gate-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
