#!/usr/bin/env bash
# Contract test for ticket #1175 — drop "Use ultrathink" / "think hard"-style
# directives from the opus-pinned planner and refiner agents, and document
# the reviewer-effort experiment in the README.
#
# Why this exists: Anthropic's Opus 5.5 guidance
# (https://claude.dev/blog/getting-the-most-out-of-opus-5-5/) says to remove
# "think hard"/"think carefully"-style lines from prompts and saved
# instructions — the model always thinks and decides how much; the `effort`
# frontmatter is the knob. Both agents carry `effort: high` for that reason,
# so a trailing "Use ultrathink for complex analysis." line is dead weight
# that a future edit could easily reintroduce by copy-paste. This test pins
# the removal down and guards the pins that replace it.
#
# Follows the idiom of flow/tests/refiner-agent-contract.test.sh: a
# `failures=` counter, small assert_* helpers, fail-closed doc loading,
# self-contained, auto-discovered by the flow gate's `*.test.sh` glob. It
# greps the real committed docs directly; no fixtures.
#
# Covered files:
#   - agents/planner.md
#   - agents/refiner.md
#   - README.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "agent-thinking-directives-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "agent-thinking-directives-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, safe inside $(...).
  cat "${FLOW_DIR}/$1" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — fail closed on a missing
# file so it can never masquerade as "empty content" and make the
# forbidden-phrase assertions pass vacuously. Must NOT be invoked via $(...).
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

# assert_no_thinking_directive <content> <label> — case-insensitive scan for
# the directive phrasings the Opus 5.5 guidance says to remove. Newlines are
# collapsed to spaces first so a phrase wrapped across Markdown source lines
# cannot slip past the match (docs/shell-scripting-gotchas.md line-wrapping
# pitfall).
assert_no_thinking_directive() {
  local content="$1" label="$2"
  local flat
  flat="$(printf '%s' "${content}" | tr '\n' ' ' | tr -s ' ')"
  local phrase
  for phrase in "ultrathink" "think hard" "think carefully" "think step by step" "think step-by-step"; do
    if printf '%s' "${flat}" | grep -qiF -- "${phrase}"; then
      fail "${label}: forbidden thinking directive still present: [${phrase}]"
    fi
  done
}

# assert_last_line_nonblank <flow-relative-path> <label> — removing the
# trailing directive must not leave a dangling blank line at end of file.
assert_last_line_nonblank() {
  local relpath="$1" label="$2"
  local last
  last="$(tail -n 1 "${FLOW_DIR}/${relpath}" 2>/dev/null)" || { fail "${label}: could not read last line"; return; }
  [[ -n "${last// /}" ]] || fail "${label}: file ends with a blank line left behind by the removed directive"
}

# --- agents/planner.md and agents/refiner.md — no directive, pins intact ---

for agent in planner refiner; do
  relpath="agents/${agent}.md"
  content=""
  require_doc content "${relpath}" || true
  if [[ -n "${content}" ]]; then
    assert_no_thinking_directive "${content}" "${relpath} #1175"
    assert_last_line_nonblank "${relpath}" "${relpath} #1175"
    # Effort is the knob that replaces the directive — the pins must survive.
    assert_contains "${content}" "model: opus" "${relpath} #1175 opus pin retained"
    assert_contains "${content}" "effort: high" "${relpath} #1175 effort pin retained"
  fi
done

# --- README.md — the effort-experiment note sits in the effort paragraph ---

readme=""
require_doc readme "README.md" || true
if [[ -n "${readme}" ]]; then
  assert_contains "${readme}" "thoroughness is guaranteed regardless of the session setting." "README.md effort paragraph anchor"
  assert_contains "${readme}" "Anthropic's Opus 5.5 guidance reports low-effort Opus 5.5 reviewing at least as well as prior high-effort Opus, so lowering reviewer effort is a reasonable experiment once you trust the baseline" "README.md #1175 effort-experiment sentence"
  assert_contains "${readme}" "the pins stay so a low-effort session never silently degrades the unattended pipeline" "README.md #1175 pins-stay clause"
  # The README must not itself recommend the removed directive.
  assert_no_thinking_directive "${readme}" "README.md #1175"
fi

if (( failures > 0 )); then
  echo "agent-thinking-directives-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "agent-thinking-directives-contract.test.sh: OK"
