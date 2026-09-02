#!/usr/bin/env bash
# gate-output-contract.test.sh — doc/schema contract test for ticket #1101:
# run-gate.sh's new `cenci.gateOutputLines` config field and its additive
# `GATE_LOG=` envelope line must be registered/documented at every site that
# owns a piece of the contract:
#   - maintain/scripts/check.sh's CONFIG_SCHEMA (the config-examples doc-drift
#     scanner's field allowlist) -- an undocumented-but-used field would make
#     check_config_examples reject a legitimate ```json example using it.
#   - configure/SKILL.md's `cenci` JSON example and schema prose -- the
#     authoritative config emitter.
#   - implement/SKILL.md's `### Cost Controls` bullet list.
#   - shell-rules/SKILL.md's new `## Command Output Discipline` section (the
#     prose half of the ticket: bounding the implementer's own repeated
#     build/test output, not run-gate.sh's).
#   - agents/implementer.md's pointer to that section (established
#     single-site-with-pointer pattern, #979 -- see
#     implement-background-shell-reap.test.sh's shell-rules assertion).
#   - the three run-gate.sh consumer-facing docs: docs/health-gates.md,
#     skills/implement/phases/phase-2-worktree.md, skills/implement/codex.md.
#
# Follows the fixture-driven idiom of automerge-config-schema.test.sh and
# implement-background-shell-reap.test.sh: a `failures=` counter, small
# assert_* helpers, self-contained, auto-discovered by the flow gate's
# `*.test.sh` glob. Greps the real committed files directly (relative to
# FLOW_DIR/repo root) rather than re-deriving the schema. Mechanical
# run-gate.sh behavior (truncation, envelope, fail-open) is covered instead
# by flow/hooks/scripts/run-gate.test.sh cases 15-23 -- this file is doc/
# schema contract only.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "gate-output-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "gate-output-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "gate-output-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# read_doc_raw <absolute-path> — pure extraction, no fail() side effect here:
# it is deliberately safe to call inside a $(...) command substitution
# (flow/docs/shell-scripting-gotchas.md rule on fail()-in-command-substitution).
read_doc_raw() {
  local _path="$1"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <absolute-path> <label> — nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing/unreadable file must never silently masquerade as empty content,
# which would make an assert_contains trivially fail with a confusing
# reason). Must NOT be invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _path="$2" _label="$3"
  local _content
  if ! _content="$(read_doc_raw "${_path}")"; then
    fail "${_label}: doc not found/unreadable: ${_path}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: assert_contains called with an empty pattern (would vacuously pass)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected text not found: [${pattern}]"
}

# assert_contains_ws <content> <phrase> <label>
# Whitespace-insensitive presence check, mirroring parity's
# `_contains_ws_insensitive` (contract-lib.sh) and
# implement-background-shell-reap.test.sh: these docs are Markdown-wrapped,
# so a raw single-space substring match would spuriously fail on a phrase
# that happens to straddle a line break. Normalize runs of whitespace to a
# single space on both sides before comparing.
assert_contains_ws() {
  local content="$1" phrase="$2" label="$3" nc np
  [[ -n "${phrase}" ]] || { fail "${label}: assert_contains_ws called with an empty phrase (would vacuously pass)"; return; }
  nc="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')"
  np="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')"
  [[ "${nc}" == *"${np}"* ]] || fail "${label}: expected text not found: [${phrase}]"
}

# =====================================================================
# skills/maintain/scripts/check.sh -- CONFIG_SCHEMA must register the new
# field, or check_config_examples rejects a legitimate ```json example that
# uses it.
# =====================================================================
FILE="skills/maintain/scripts/check.sh"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "'cenci.gateOutputLines|number'" "${FILE} (CONFIG_SCHEMA entry)"
fi

# =====================================================================
# skills/configure/SKILL.md -- the authoritative config emitter must show
# gateOutputLines in the `cenci` JSON example AND document it in the `cenci`
# schema prose (both directions matter, same as automerge-config-schema.test.sh).
# =====================================================================
FILE="skills/configure/SKILL.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" '"gateOutputLines": 120' "${FILE} (cenci JSON example shows gateOutputLines: 120)"
  assert_contains "${CONTENT}" "\`gateOutputLines\` —" "${FILE} (cenci schema prose documents gateOutputLines)"
fi

# =====================================================================
# skills/implement/SKILL.md -- `### Cost Controls` must list
# cenci.gateOutputLines alongside the other cenci.* settings, noting it
# bounds gate output for all three run-gate.sh consumers.
# =====================================================================
FILE="skills/implement/SKILL.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "### Cost Controls" "${FILE} (section exists)"
  assert_contains "${CONTENT}" "cenci.gateOutputLines" "${FILE} (Cost Controls documents cenci.gateOutputLines)"
fi

# =====================================================================
# skills/shell-rules/SKILL.md -- the new `## Command Output Discipline`
# section (client-neutral, portable to OpenCode), immediately after
# `## Background Commands`. Assert the heading plus 3 distinctive phrases
# quoted from the plan's Implementation Order step 6, since none of these
# exact phrases exists anywhere in flow/ today (verified while writing this
# test) -- so none is a pre-existing, vacuously-matchable string.
# =====================================================================
FILE="skills/shell-rules/SKILL.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "## Command Output Discipline" "${FILE} (section exists)"
  # NOTE: the bare base dir "${TMPDIR:-/tmp}/cenci/" is deliberately NOT used
  # as a marker here -- it already appears in "## Body Files and Heredocs"
  # (the issue/PR body-file convention), so it would vacuously pass today
  # (shell-scripting-gotchas.md rule on generic markers). "<name>-<scope>.log"
  # is the distinctive log-naming suffix from the plan's Implementation Order
  # step 6 and does not exist anywhere in flow/ today (verified while writing
  # this test; guard-main-worktree.sh:20 has the same base dir but a
  # different, `.md`, suffix).
  assert_contains_ws "${CONTENT}" '<name>-<scope>.log' "${FILE} (redirect target log-naming convention documented)"
  assert_contains_ws "${CONTENT}" "never inline a whole suite run into the transcript" "${FILE} (never-inline-whole-run rule)"
  assert_contains_ws "${CONTENT}" "does not violate \`## Command Shape\`'s one-command-per-call rule" "${FILE} (single-redirect precedence statement)"
fi

# =====================================================================
# agents/implementer.md -- a one-line pointer to shell-rules' new section,
# beside the existing Output/Shell discipline blockquotes, NOT a restatement
# of the rule (drift risk, #979 -- single-site precedent is
# implement-background-shell-reap.test.sh's shell-rules-only assertion).
# =====================================================================
FILE="agents/implementer.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "Command Output Discipline" "${FILE} (pointer to shell-rules' Command Output Discipline)"
fi

# =====================================================================
# docs/health-gates.md -- must document the truncated-tail + GATE_LOG
# contract: last-match parsing, reading the log via Grep-then-Read rather
# than cat, the cenci.gateOutputLines default (120), and red-only retention.
# =====================================================================
FILE="docs/health-gates.md"
if require_doc CONTENT "${REPO_ROOT}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "GATE_LOG" "${FILE} (documents the GATE_LOG= envelope line)"
  assert_contains "${CONTENT}" "cenci.gateOutputLines" "${FILE} (documents the cenci.gateOutputLines field)"
  assert_contains "${CONTENT}" "120" "${FILE} (documents the default of 120)"
fi

# =====================================================================
# skills/implement/phases/phase-2-worktree.md (Claude adapter) -- `### 4.
# Interpret` and `### 6. Stop on failure` must extend with the GATE_LOG
# contract, without disturbing the two pinned marker lines
# (`_CLAUDE_GATE_MARKER`/`_CLAUDE_STATUS_MARKER` in
# flow/tests/parity/contract-lib.sh) that flow/tests/parity depends on.
# =====================================================================
FILE="skills/implement/phases/phase-2-worktree.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "GATE_LOG" "${FILE} (documents the GATE_LOG= envelope line)"
  # Regression guard: the pinned parity markers must still be present and
  # intact -- this doc-contract test does not replace flow/tests/parity, but
  # a doc edit that silently regressed these two exact substrings would be
  # caught here too, earlier and with a clearer message.
  assert_contains "${CONTENT}" '`GATE_STATUS=green` or `GATE_STATUS=unset` → this target passes.' "${FILE} (pinned GATE_STATUS marker intact)"
  assert_contains "${CONTENT}" "hooks/scripts/run-gate.sh" "${FILE} (pinned run-gate.sh marker intact)"
fi

# =====================================================================
# skills/implement/codex.md (Codex adapter -- parity) -- the same GATE_LOG
# contract, stated without disturbing the six exact substrings
# implement-codex-gate.test.sh pins in this file.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FLOW_DIR}/${FILE}" "${FILE}"; then
  assert_contains "${CONTENT}" "GATE_LOG" "${FILE} (documents the GATE_LOG= envelope line)"
  assert_contains "${CONTENT}" '`GATE_STATUS=green` or `GATE_STATUS=unset` → proceed' "${FILE} (pinned GATE_STATUS marker intact)"
fi

echo "gate-output-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
