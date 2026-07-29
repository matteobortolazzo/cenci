#!/usr/bin/env bash
# Contract test for ticket #749 (Q3 scope expansion) — fix attachments'
# ATTACH_DIR assignment wrapper and its downstream non-persistence hazard.
#
# Why this exists: `ATTACH_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cenci-attachments-XXXXXX")"`
# is an assignment wrapper -- `shell-rules` forbids command substitution
# where the agent can run the command and read the result, and an assignment
# wrapper never matches a `Bash(mktemp -d:*)` prefix grant (so it would keep
# prompting even from design, which now grants that exact invocation shape).
# Worse, `${ATTACH_DIR}` is referenced in later, separate Bash calls where
# Bash-tool shell state does not persist -- so it already expands to empty
# and every `${ATTACH_DIR}/<file>` path silently collapses to a
# root-relative path, precisely the hazard the skill's own prose warns
# about. The fix: a standalone `mktemp -d` call, a standalone `test -d`
# verification, and carrying the printed path forward as literal text
# (never a shell variable) at every downstream reference site.
#
# Follows the idiom of flow/tests/design-sandbox-guard.test.sh /
# flow/tests/refine-skill-contract.test.sh: a `failures=` counter, small
# assert_* helpers, exact substring markers (never generic keywords -- see
# docs/shell-scripting-gotchas.md), self-contained, auto-discovered by the
# flow gate's `*.test.sh` glob. It greps the real committed doc directly; no
# fixtures.
#
# Covered files:
#   - flow/skills/attachments/SKILL.md
#   - flow/skills/design/SKILL.md (cross-file pin only)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "attachments-temp-dir-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "attachments-temp-dir-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
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

require_doc skill "skills/attachments/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  # Standalone mktemp -d invocation present -- no assignment wrapper, no
  # $(...) command substitution.
  assert_contains "${skill}" 'mktemp -d "${TMPDIR:-/tmp}/cenci-attachments-XXXXXX"' "749 skills/attachments/SKILL.md standalone mktemp -d invocation"
  assert_not_contains "${skill}" "ATTACH_DIR=" "749 skills/attachments/SKILL.md no ATTACH_DIR= assignment wrapper"
  assert_not_contains "${skill}" '$(mktemp' "749 skills/attachments/SKILL.md no \$(mktemp command substitution"
  assert_not_contains "${skill}" '${ATTACH_DIR}' "749 skills/attachments/SKILL.md no \${ATTACH_DIR} shell-variable reference"

  # A standalone test -d verification of the created directory, before any
  # download proceeds.
  assert_contains "${skill}" "test -d" "749 skills/attachments/SKILL.md test -d verification"

  # The literal-carry-forward instruction: the printed path is carried
  # forward as literal text, not held in a shell variable, because Bash-tool
  # state does not persist between calls.
  assert_contains "${skill}" "Carry the printed path forward as the literal text" "749 skills/attachments/SKILL.md literal-carry-forward instruction"
  assert_contains "${skill}" "does not persist between calls" "749 skills/attachments/SKILL.md non-persistence rationale"

  # All four downstream reference sites now use the <attach-dir> literal
  # placeholder instead of ${ATTACH_DIR}.
  assert_contains "${skill}" "<attach-dir>" "749 skills/attachments/SKILL.md <attach-dir> literal placeholder"
fi

# --- Cross-file pin: design's granted Bash(mktemp -d:*) prefix and
# attachments' own mktemp -d invocation shape must never drift apart -- a
# caller granting exactly `Bash(mktemp -d:*)` depends on attachments' command
# actually beginning with `mktemp -d `.
require_doc design_skill "skills/design/SKILL.md" || true
if [[ -n "${skill}" && -n "${design_skill}" ]]; then
  assert_contains "${design_skill}" "Bash(mktemp -d:*)" "749 cross-file pin: skills/design/SKILL.md grants Bash(mktemp -d:*)"
  assert_contains "${skill}" "mktemp -d " "749 cross-file pin: skills/attachments/SKILL.md invocation begins mktemp -d "
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "attachments-temp-dir-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "attachments-temp-dir-contract.test.sh: all checks passed."
