#!/usr/bin/env bash
# implement-babysit-launch.test.sh — contract test for ticket #616: at the end
# of implement Phase 9 (after the PR exists and the Working -> In Review swap),
# both client adapters must auto-launch the persistent `cenci babysit`
# supervisor for the new PR, non-blocking, treating "supervisor already running"
# as expected success (Goal-Autopilot re-entry idempotency). The watch interval
# is resolved from `.cenci/config.json`'s optional `babysitInterval` via the new
# shared resolver, falling back to babysit's built-in `15m` default.
#
# Follows the fixture-driven idiom of pipeline-cutover-contract.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. Greps the real committed docs directly
# (relative to FLOW_DIR) since the contract lives in those docs' prose.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the specific
# replacement text at the edit site, never a generic marker that may already
# exist elsewhere): `cenci babysit` does not appear anywhere in
# flow/skills/implement/ today (verified with a whole-tree grep while writing
# this test), so it is not a pre-existing, vacuously-matchable marker. The Codex
# push-policy anchor line is asserted *present and intact* (a parity-regression
# guard) rather than removed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-babysit-launch.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-babysit-launch.test.sh: failed to resolve flow directory." >&2; exit 2; }
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
# missing/unreadable file must never silently masquerade as empty content,
# which would make an assert_contains trivially fail with a confusing
# reason). Must NOT be invoked via $(...).
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
# `_contains_ws_insensitive` (contract-lib.sh): the push-policy anchor sentence
# is Markdown-wrapped across two lines in codex.md, so a raw single-space
# substring match would spuriously fail. Normalize runs of whitespace to a
# single space on both sides before comparing.
assert_contains_ws() {
  local content="$1" phrase="$2" label="$3" nc np
  nc="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')"
  np="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')"
  [[ "${nc}" == *"${np}"* ]] || fail "${label}: expected text not found: [${phrase}]"
}

# =====================================================================
# The shared resolver script must exist and be non-empty (the interval source
# of truth both adapters and the standalone babysit skill reference).
# =====================================================================
RESOLVER="${FLOW_DIR}/hooks/scripts/resolve-babysit-interval.sh"
[[ -s "${RESOLVER}" ]] || fail "hooks/scripts/resolve-babysit-interval.sh: missing or empty resolver script"

# =====================================================================
# skills/implement/phases/phase-9-pr.md (Claude adapter) — the final Phase-9
# action, after Cleanup, must launch babysit for the new PR with --agent claude,
# resolve the interval via the shared resolver, and treat "already running" as
# expected success.
# =====================================================================
FILE="skills/implement/phases/phase-9-pr.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "cenci babysit" "${FILE} (babysit launch)"
  assert_contains "${CONTENT}" "--agent claude" "${FILE} (babysit launch)"
  assert_contains "${CONTENT}" "resolve-babysit-interval.sh" "${FILE} (interval resolution)"
  assert_contains "${CONTENT}" "supervisor already running for PR #" "${FILE} (idempotent re-entry treated as success)"
fi

# =====================================================================
# skills/implement/codex.md (Codex adapter — parity) — the same post-PR babysit
# hand-off with --agent codex, AND the exact push-policy anchor line still
# present and intact (guard against regressing adapter-contract.md's push-policy
# anchor while inserting the babysit line).
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "cenci babysit" "${FILE} (babysit launch)"
  assert_contains "${CONTENT}" "--agent codex" "${FILE} (babysit launch)"
  assert_contains_ws "${CONTENT}" "Never force-push or bypass security/design/approval gates." "${FILE} (push-policy anchor intact)"
fi

# =====================================================================
# skills/configure/SKILL.md — the authoritative config emitter must document the
# optional babysitInterval field so a project can set the watch cadence.
# =====================================================================
FILE="skills/configure/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "babysitInterval" "${FILE} (config field documented)"
fi

echo "implement-babysit-launch.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
