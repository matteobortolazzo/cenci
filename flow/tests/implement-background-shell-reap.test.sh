#!/usr/bin/env bash
# implement-background-shell-reap.test.sh — contract test for ticket #983: an
# implement run must not end with background shells it started still running.
#
# Why this is a contract and not just hygiene: Claude Code reports still-running
# background shells on the session's `Stop` event (`background_tasks`), and
# `cenci watch` deliberately maps such a Stop to `running` instead of `done`
# (watch/notify_cmd.go's non-terminal-status scan -> daemon/event.go's #698/#699
# hold, kept alive against the title sweep by #706). The hold clears on the
# session's next event, and an abandoned shell never produces one — so a single
# leaked shell pins the pipeline's own window at "still working" indefinitely.
#
# Follows the fixture-driven idiom of implement-babysit-launch.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. It greps the real committed docs directly
# (relative to FLOW_DIR) since the contract lives in those docs' prose.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the specific
# replacement text at the edit site, never a generic marker that may already
# exist elsewhere): "background shell" appears nowhere in flow/ today (verified
# with a whole-tree grep while writing this test), so none of the markers below
# is a pre-existing, vacuously-matchable string. The Codex push-policy anchor
# line is asserted *present and intact* (a parity-regression guard) rather than
# removed, mirroring implement-babysit-launch.test.sh.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-background-shell-reap.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-background-shell-reap.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side effect
  # here: it is deliberately safe to call inside a $(...) command substitution.
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper that assigns
# the real committed file's content into <result-var>, or fails closed with a
# distinct "not found" message and assigns "" if not found (a missing or
# unreadable file must never silently masquerade as empty content, which would
# make an assert_contains trivially fail with a confusing reason). Must NOT be
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

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected text not found: [${pattern}]"
}

# assert_contains_ws <content> <phrase> <label>
# Whitespace-insensitive presence check, mirroring parity's
# `_contains_ws_insensitive` (contract-lib.sh): shell-rules/SKILL.md and
# codex.md are Markdown-wrapped, so a raw single-space substring match would
# spuriously fail on a phrase that happens to straddle a line break. Normalize
# runs of whitespace to a single space on both sides before comparing.
assert_contains_ws() {
  local content="$1" phrase="$2" label="$3" nc np
  nc="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')"
  np="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')"
  [[ "${nc}" == *"${np}"* ]] || fail "${label}: expected text not found: [${phrase}]"
}

# first_line_of <flow-relative-path> <fixed-string>
# Line number of the first occurrence, or empty when absent. Line numbers (not
# byte offsets) are enough for the one ordering assertion below: all three
# markers are on distinct lines of the same file.
first_line_of() {
  local _relpath="$1" _needle="$2"
  read_doc_raw "${_relpath}" | grep -n -m 1 -F -- "${_needle}" | cut -d: -f1
}

# =====================================================================
# skills/shell-rules/SKILL.md — the standing, repo-wide rule. shell-rules is the
# shared reference every implement-pipeline agent already consults for command
# patterns (agents/implementer.md, agents/planner.md, phase-3/phase-5), so the
# rule has to live here to reach subagent-started shells too, not only the main
# agent's.
# =====================================================================
FILE="skills/shell-rules/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "## Background Commands" "${FILE} (section exists)"
  assert_contains_ws "${CONTENT}" "Stop every background shell you start." "${FILE} (ownership rule)"
  assert_contains_ws "${CONTENT}" "holds such a session at \`running\` rather than \`done\`" "${FILE} (watch hold rationale)"
  assert_contains_ws "${CONTENT}" "A slow-but-finite command" "${FILE} (foreground-by-default rule)"
fi

# =====================================================================
# skills/implement/phases/phase-9-pr.md (Claude adapter) — the enforcement point.
# The reap must sit inside `## Cleanup` and ahead of `## Babysit`: Cleanup is
# where the session settles its own completion, and the babysit launch that
# follows is explicitly exempt (it detaches, so its launching shell exits).
# =====================================================================
FILE="skills/implement/phases/phase-9-pr.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "Reap this run's background shells." "${FILE} (reap step)"
  assert_contains "${CONTENT}" "stop every background shell this run started that is still running" "${FILE} (reap step)"
  assert_contains "${CONTENT}" "holds such a session at \`running\` instead of \`done\`" "${FILE} (watch hold rationale)"
  assert_contains "${CONTENT}" "detaches its own supervisor" "${FILE} (babysit exemption stated)"

  REAP_LINE="$(first_line_of "${FILE}" "Reap this run's background shells.")"
  CLEANUP_LINE="$(first_line_of "${FILE}" "## Cleanup")"
  BABYSIT_LINE="$(first_line_of "${FILE}" "## Babysit")"
  if [[ -z "${REAP_LINE}" || -z "${CLEANUP_LINE}" || -z "${BABYSIT_LINE}" ]]; then
    fail "${FILE} (reap placement): could not locate all three markers (reap=[${REAP_LINE}] cleanup=[${CLEANUP_LINE}] babysit=[${BABYSIT_LINE}])"
  else
    [[ "${REAP_LINE}" -gt "${CLEANUP_LINE}" ]] || fail "${FILE} (reap placement): reap step at line ${REAP_LINE} must come after '## Cleanup' at line ${CLEANUP_LINE}"
    [[ "${REAP_LINE}" -lt "${BABYSIT_LINE}" ]] || fail "${FILE} (reap placement): reap step at line ${REAP_LINE} must come before '## Babysit' at line ${BABYSIT_LINE}"
  fi
fi

# =====================================================================
# skills/implement/codex.md (Codex adapter — parity) — the same reap, stated
# before the babysit hand-off, AND the exact push-policy anchor line still
# present and intact (guard against regressing adapter-contract.md's
# push-policy anchor while inserting the reap sentence).
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains_ws "${CONTENT}" "stop every background shell this run started that is still running" "${FILE} (reap step)"
  assert_contains_ws "${CONTENT}" "Never force-push or bypass security/design/approval gates." "${FILE} (push-policy anchor intact)"

  REAP_LINE="$(first_line_of "${FILE}" "stop every background shell this run")"
  BABYSIT_LINE="$(first_line_of "${FILE}" "cenci babysit <pr> --agent codex")"
  if [[ -z "${REAP_LINE}" || -z "${BABYSIT_LINE}" ]]; then
    fail "${FILE} (reap placement): could not locate both markers (reap=[${REAP_LINE}] babysit=[${BABYSIT_LINE}])"
  else
    [[ "${REAP_LINE}" -lt "${BABYSIT_LINE}" ]] || fail "${FILE} (reap placement): reap sentence at line ${REAP_LINE} must come before the babysit launch at line ${BABYSIT_LINE}"
  fi
fi

echo "implement-background-shell-reap.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
