#!/usr/bin/env bash
# Contract test for ticket #1176 — the code-reviewer agent reports
# merge-blocking findings with a failure scenario and drops Positive Notes.
#
# Why this exists: Anthropic's Opus 5.5 diff-review guidance
# (https://claude.dev/blog/getting-the-most-out-of-opus-5-5/) is to list
# only the problems you would block the merge for and, for each, give the
# file and line, why it is wrong, and how to show it fails. The agent's
# Must Fix / Should Fix entry templates had no slot for the "how to show it
# fails" part, and a `### Positive Notes` section invited praise that is
# never actionable. This test pins the new shape so a future edit cannot
# quietly drop the failure-scenario field or reintroduce a praise section.
#
# Follows the idiom of flow/tests/refiner-agent-contract.test.sh: a
# `failures=` counter, small assert_* helpers, exact replacement-sentence
# markers (never generic keywords — see docs/shell-scripting-gotchas.md),
# self-contained, auto-discovered by the flow gate's `*.test.sh` glob. It
# greps the real committed docs directly; no fixtures.
#
# Covered files:
#   - agents/code-reviewer.md
#   - templates/agents-md-codex.md                        (Codex self-review mirror)
#   - templates/codex/agent-roles/critical-reviewer.toml  (Codex role mirror)
#   - <repo-root>/.codex/agents/critical-reviewer.toml    (installed copy of the role)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "code-reviewer-failure-scenario.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "code-reviewer-failure-scenario.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <absolute-path> — pure extraction, no fail() side effect,
  # so it is safe inside a $(...) command substitution.
  cat "$1" 2>/dev/null
}

# require_doc <result-var> <absolute-path> — assigns the real committed
# file's content into <result-var>, or fails closed with a distinct "not
# found" message and assigns "" (a missing file must never masquerade as
# empty content, which would make assert_not_contains trivially pass).
require_doc() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_doc_raw "${_path}")"; then
    fail "doc not found/unreadable: ${_path}"
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

# section_body <content> <heading-line> — the lines after <heading-line> up
# to (excluding) the next "### " or "## " heading. Pure extraction (no
# fail()), safe inside $(...). Prints nothing when the heading is absent,
# which the caller treats as a failure.
section_body() {
  local content="$1" heading="$2"
  printf '%s\n' "${content}" | awk -v h="${heading}" '
    $0 == h { inside = 1; next }
    inside && (/^### / || /^## /) { exit }
    inside { print }
  '
}

# line_of <body> <fixed-string> — 1-based line number of the first line
# containing <fixed-string>, or "" when absent. Pure extraction.
line_of() {
  local body="$1" needle="$2"
  printf '%s\n' "${body}" | grep -n -F -- "${needle}" | head -1 | cut -d: -f1
}

FAILURE_SCENARIO_LINE='- **Failure scenario**: <concrete input or state → wrong output, crash, or missed requirement; how a reader would show it fails>'

# --- agents/code-reviewer.md — failure scenario per finding, no praise ---

require_doc reviewer "${FLOW_DIR}/agents/code-reviewer.md" || true
if [[ -n "${reviewer}" ]]; then
  # AC1a: both merge-blocking entry templates carry the field, placed
  # directly after Risk so a reader sees "what breaks" then "how to show it".
  for heading in "### Must Fix (confidence >= 90)" "### Should Fix (confidence 75–89)"; do
    body="$(section_body "${reviewer}" "${heading}")"
    if [[ -z "${body}" ]]; then
      fail "agents/code-reviewer.md: section [${heading}] not found"
      continue
    fi
    assert_contains "${body}" "${FAILURE_SCENARIO_LINE}" "agents/code-reviewer.md [${heading}] failure-scenario field"
    risk_line="$(line_of "${body}" '- **Risk**:')"
    fs_line="$(line_of "${body}" '- **Failure scenario**:')"
    if [[ -n "${risk_line}" && -n "${fs_line}" ]]; then
      [[ "${fs_line}" -eq $((risk_line + 1)) ]] || fail "agents/code-reviewer.md [${heading}]: Failure scenario must directly follow Risk (Risk at line ${risk_line}, Failure scenario at line ${fs_line})"
    else
      fail "agents/code-reviewer.md [${heading}]: could not locate both the Risk and the Failure scenario lines"
    fi
  done

  # Nitpicks are not merge-blocking; the field is required only on the two
  # blocking tiers, so the Nitpicks template stays as it was.
  nit_body="$(section_body "${reviewer}" "### Nitpicks (confidence 50–74)")"
  [[ -n "${nit_body}" ]] || fail "agents/code-reviewer.md: Nitpicks section missing"
  assert_not_contains "${nit_body}" "Failure scenario" "agents/code-reviewer.md Nitpicks template unchanged"

  # AC1b: Positive Notes is gone — the section heading, its template line,
  # and the output-constraint bullet.
  assert_not_contains "${reviewer}" "Positive Notes" "agents/code-reviewer.md"
  assert_not_contains "${reviewer}" "<what was done well>" "agents/code-reviewer.md"

  # Passing Checks stays: it is evidence of what was verified, not praise.
  assert_contains "${reviewer}" "### Passing Checks" "agents/code-reviewer.md"
  assert_contains "${reviewer}" "**Passing Checks**: only list checks that were actively verified" "agents/code-reviewer.md"

  # The framing sentence placed after the Output discipline blockquote.
  assert_contains "${reviewer}" "Findings are merge-blocking problems only: each names the file and line, why it is wrong, and how to show it fails" "agents/code-reviewer.md merge-blocking framing"

  # The sections around the change survive intact.
  for kept in "## Confidence Scoring" "## LSP Awareness" "## Review Checklist" "## Test Value" "## Output Constraints" "## Output Format" "### Verdict" "APPROVE | APPROVE_WITH_SUGGESTIONS | REQUEST_CHANGES"; do
    assert_contains "${reviewer}" "${kept}" "agents/code-reviewer.md retained section"
  done
fi

# --- templates/agents-md-codex.md — the Codex self-review mirror ---

require_doc codex_agents "${FLOW_DIR}/templates/agents-md-codex.md" || true
if [[ -n "${codex_agents}" ]]; then
  assert_contains "${codex_agents}" "**Findings**: report only problems you would block the merge for; for each, name the file and line, why it is wrong, and a failure scenario (concrete input or state → wrong output, crash, or missed requirement) showing how it fails. No praise or positive notes." "templates/agents-md-codex.md code-quality findings rule"
  assert_not_contains "${codex_agents}" "Positive Notes" "templates/agents-md-codex.md"
fi

# --- Codex critical-reviewer role (template + the repo's installed copy) ---

require_doc role_template "${FLOW_DIR}/templates/codex/agent-roles/critical-reviewer.toml" || true
if [[ -n "${role_template}" ]]; then
  assert_contains "${role_template}" "For each finding give the file and line, why it is wrong, and a failure scenario showing how it fails. No praise." "templates/codex/agent-roles/critical-reviewer.toml"
  # The repo's installed copy is generated from the template and must not drift.
  require_doc installed "${FLOW_DIR}/../.codex/agents/critical-reviewer.toml" || true
  if [[ -n "${installed}" ]]; then
    [[ "${installed}" == "${role_template}" ]] || fail ".codex/agents/critical-reviewer.toml drifted from templates/codex/agent-roles/critical-reviewer.toml"
  fi
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "code-reviewer-failure-scenario.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "code-reviewer-failure-scenario.test.sh: all assertions passed"
