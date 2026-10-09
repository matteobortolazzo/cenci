#!/usr/bin/env bash
# Contract test for the verify-before-fix rule (ticket #1177).
#
# The gap this pins down: Phase 6 + 7 of the implement pipeline (and the
# standalone review skill's consolidation phase) dispatched fixes straight
# from reviewer findings. A subagent's finding is a claim, not evidence --
# Anthropic's Opus 5.5 guidance for subagent fan-out is "when a subagent
# reports back, check its evidence before you accept it". For the top
# tiers (Must Fix / Critical / High), where a wrong finding costs a
# delegated fix cycle plus a re-review, the orchestrating agent must read
# the cited file and line and confirm the finding holds against the actual
# code before any fix is delegated or applied. A finding that does not hold
# is not fixed; it is recorded as a one-line `Considered and discarded`
# entry so the rejection stays visible in the PR Notes instead of silently
# vanishing.
#
# Per docs/skill-authoring.md every skill change must reconcile all client
# surfaces, so the rule must be stated on the Claude procedures AND their
# Codex mirrors. This test greps the real committed docs for the exact
# marker sentences (not generic keywords -- see
# docs/shell-scripting-gotchas.md's grep-based-contract-test rule), and
# follows the fixture-free idiom of tests/phase5-reuse-check-contract.test.sh.
#
# Covered files (the only docs this test scans):
#   - skills/implement/phases/phase-6-7-review.md   (Claude implement pipeline)
#   - skills/review/SKILL.md                        (Claude standalone review)
#   - skills/implement/codex.md                     (Codex implement mirror)
#   - skills/review/codex.md                        (Codex review mirror)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "verify-before-fix-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "verify-before-fix-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # Pure extraction, no fail() side effect -- safe to call inside $(...).
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- assigns the real committed
# file's content into <result-var>, or fails closed with a distinct "not
# found" message (a missing file must never masquerade as empty content).
# Must NOT be invoked via $(...).
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

# extract_section <content> <exact-heading-line> -- prints the body under that
# heading, up to the next `## ` heading. Pure, safe inside $(...).
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    $0 == h { inside = 1; next }
    inside && /^## / { exit }
    inside { print }
  ' <<<"${content}"
}

# first_line_of <content> <literal-substring> -- prints the 1-based line
# number of the first line containing the substring, or nothing.
first_line_of() {
  local content="$1" needle="$2"
  grep -n -F -m1 -e "${needle}" <<<"${content}" | cut -d: -f1
}

# The two marker sentences every surface must carry verbatim. The first is
# the operative rule; the second is the Opus 5.5 guidance it cites. Both are
# specific replacement text, not keywords that could pre-exist elsewhere.
RULE_MARKER='confirms the finding holds against the actual code'
GUIDANCE_MARKER='check its evidence before you accept it'
# The discard path: a rejected finding must still surface in the PR Notes.
DISCARD_MARKER='Considered and discarded'

# =====================================================================
# phase-6-7-review.md -- the Claude implement pipeline.
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if require_doc CONTENT "${FILE}"; then
  # The rule lives in its own short subsection ...
  assert_contains "${CONTENT}" '## Verify Before Fixing' "${FILE}"

  # ... placed right before the first Actions section, so it is read before
  # any dispatch rule, not discovered after the fix is already delegated.
  verify_line="$(first_line_of "${CONTENT}" '## Verify Before Fixing')"
  security_line="$(first_line_of "${CONTENT}" '## Security Review Actions')"
  if [[ -z "${verify_line}" || -z "${security_line}" ]]; then
    fail "${FILE}: could not locate both '## Verify Before Fixing' and '## Security Review Actions' headings"
  elif (( verify_line >= security_line )); then
    fail "${FILE}: '## Verify Before Fixing' (line ${verify_line}) must precede '## Security Review Actions' (line ${security_line})"
  fi

  SECTION="$(extract_section "${CONTENT}" '## Verify Before Fixing')"
  [[ -n "${SECTION}" ]] || fail "${FILE}: '## Verify Before Fixing' section is empty"
  assert_contains "${SECTION}" "${RULE_MARKER}" "${FILE} (Verify Before Fixing)"
  assert_contains "${SECTION}" "${GUIDANCE_MARKER}" "${FILE} (Verify Before Fixing)"
  assert_contains "${SECTION}" "${DISCARD_MARKER}" "${FILE} (Verify Before Fixing)"
  # The rule is scoped to the top tiers; lower tiers keep their existing
  # fix-now-or-discard handling.
  assert_contains "${SECTION}" 'Must Fix' "${FILE} (Verify Before Fixing)"
  assert_contains "${SECTION}" 'Critical' "${FILE} (Verify Before Fixing)"
  assert_contains "${SECTION}" 'High' "${FILE} (Verify Before Fixing)"

  # Each Actions section that dispatches a fix points back at the rule, so
  # a reader landing on the dispatch bullet alone cannot miss it.
  for heading in '## Security Review Actions' '## Code Review Actions' '## Silent Failure Actions'; do
    ACTIONS="$(extract_section "${CONTENT}" "${heading}")"
    [[ -n "${ACTIONS}" ]] || { fail "${FILE}: '${heading}' section missing or empty"; continue; }
    assert_contains "${ACTIONS}" 'Verify Before Fixing' "${FILE} (${heading})"
  done
fi

# =====================================================================
# review/SKILL.md -- the Claude standalone review skill. Findings are
# consolidated in Phase 3; the rule must bind there, not elsewhere.
# =====================================================================
FILE="skills/review/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  SECTION="$(extract_section "${CONTENT}" '## Phase 3: Consolidate Results')"
  [[ -n "${SECTION}" ]] || fail "${FILE}: '## Phase 3: Consolidate Results' section missing or empty"
  assert_contains "${SECTION}" "${RULE_MARKER}" "${FILE} (Phase 3)"
  assert_contains "${SECTION}" "${GUIDANCE_MARKER}" "${FILE} (Phase 3)"
  assert_contains "${SECTION}" "${DISCARD_MARKER}" "${FILE} (Phase 3)"
fi

# =====================================================================
# implement/codex.md -- Codex implement mirror.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "${RULE_MARKER}" "${FILE}"
  assert_contains "${CONTENT}" "${GUIDANCE_MARKER}" "${FILE}"
  assert_contains "${CONTENT}" "${DISCARD_MARKER}" "${FILE}"
fi

# =====================================================================
# review/codex.md -- Codex review mirror.
# =====================================================================
FILE="skills/review/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "${RULE_MARKER}" "${FILE}"
  assert_contains "${CONTENT}" "${GUIDANCE_MARKER}" "${FILE}"
  assert_contains "${CONTENT}" "${DISCARD_MARKER}" "${FILE}"
fi

if (( failures > 0 )); then
  echo "verify-before-fix-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "verify-before-fix-contract.test.sh: all assertions passed."
