#!/usr/bin/env bash
# babysit-host-supervisor-contract.test.sh — contract test for ticket #977:
# "Make Phase 9 and the babysit skill tell the truth about where the
# supervisor runs (3/3)". The flow-side text still claims implement Phase 9's
# `cenci babysit` launch always ends with the PR "open and being watched" —
# an unconditional claim the mechanism shipped by #1094/#1095 no longer
# supports: `cenci babysit` (watch/internal/babysit/arm.go) reports one of
# three outcomes (armed / not armed / arm status unknown), and inside a
# `cenci sandbox` container the supervisor itself always ends up running on
# the host, never in the sandbox. Neither the babysit skill nor
# docs/autonomous-loop.md says so today.
#
# This test asserts, by grepping the real committed doc/skill content (never
# by re-implementing the procedure or the CLI), that:
#   1. phase-9-pr.md's `## Babysit` section distinguishes "not armed" and
#      "arm status unknown" from "armed", instructs relaying the CLI's reason
#      verbatim (never re-deriving/re-wording it), names the host re-arm
#      command, and states the phase stays green on this outcome.
#   2. phase-9-pr.md no longer frames the "open and being watched" report as
#      the phase's unconditional, singular terminal state.
#   3. implement/SKILL.md and implement/codex.md each carry the same
#      not-armed distinction (the multi-site restatement rule, flow/AGENTS.md
#      #979).
#   4. babysit/SKILL.md documents that the supervisor always runs on the
#      host, the host state-dir path (with its `$XDG_STATE_HOME`/fallback
#      pair), and the `<pr>.json`/`.log` verification file pattern — without
#      widening `allowed-tools` (pinned by allowed-tools-sweep.test.sh
#      elsewhere; this test independently guards the exact grant string).
#   5. docs/autonomous-loop.md's `## What still stops the machine` section
#      names the forwarded/sandboxed arming path and the not-armed outcome.
#   6. Anti-drift cross-check: every outcome literal the docs above are
#      expected to quote verbatim actually appears in
#      watch/internal/babysit/arm.go, the client's own source of truth for
#      those strings (Q&A 2's "cannot drift" rule) — an unreadable arm.go
#      fails closed rather than skipping the check.
#
# Follows the fixture-free idiom of
# autonomous-loop-protectedpaths-trailing-slash.test.sh (REPO_ROOT resolved
# from FLOW_DIR/.. to reach repo-root docs/ and watch/, a `failures=` counter,
# require_file failing closed) and doc-ownership-contract.test.sh
# (extract_section binding an assertion to its heading so a match landing
# elsewhere in a long file can never pass vacuously).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "babysit-host-supervisor-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "babysit-host-supervisor-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "babysit-host-supervisor-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# read_file_raw <absolute-path> — pure extraction, no fail() side effect
# here: it is deliberately safe to call inside a $(...) command substitution
# (flow/docs/shell-scripting-gotchas.md rule on fail()-in-command-substitution).
read_file_raw() {
  cat "$1" 2>/dev/null
}

# require_file <result-var> <absolute-path> — nameref wrapper that assigns
# the real committed file's content into <result-var>, or fails closed with a
# distinct "not found" message and assigns "" if not found. Must NOT be
# invoked via $(...).
require_file() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_file_raw "${_path}")"; then
    fail "${_path}: file not found/unreadable"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# assert_contains_any <content> <label> <pattern...> — pass if any <pattern>
# is a substring of <content>.
assert_contains_any() {
  local content="$1" label="$2"
  shift 2
  local pattern
  for pattern in "$@"; do
    [[ "${content}" == *"${pattern}"* ]] && return 0
  done
  fail "${label}: expected one of [$*] not found"
  return 1
}

# extract_section <content> <exact-heading-line> — prints the body under that
# heading, up to the next `## ` heading. Pure, safe inside $(...). Assertions
# scoped through this cannot pass on a match that landed somewhere else in
# the file.
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    $0 == h { inside = 1; next }
    inside && /^## / { exit }
    inside { print }
  ' <<<"${content}"
}

# extract_line <content> <literal-substring> — prints the first line
# containing the substring, for assertions that must bind to one specific
# line/paragraph rather than the whole file.
extract_line() {
  local content="$1" needle="$2"
  grep -F -m1 -e "${needle}" <<<"${content}"
}

# =====================================================================
# 1. phase-9-pr.md `## Babysit` — outcome classification: not-armed and
#    arm-status-unknown are named, the reason is relayed verbatim (never
#    re-derived/re-worded), the host re-arm command is printed, and the
#    phase is stated to stay green on this outcome.
# =====================================================================
FILE="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
if require_file CONTENT "${FILE}"; then
  BABYSIT_SECTION="$(extract_section "${CONTENT}" '## Babysit')"
  if [[ -z "${BABYSIT_SECTION}" ]]; then
    fail "phase-9-pr.md: ## Babysit section not found"
  else
    assert_contains "${BABYSIT_SECTION}" "not armed" \
      "phase-9-pr.md (## Babysit names the not-armed outcome)"
    assert_contains "${BABYSIT_SECTION}" "arm status unknown" \
      "phase-9-pr.md (## Babysit names the arm-status-unknown outcome)"
    assert_contains "${BABYSIT_SECTION}" "verbatim" \
      "phase-9-pr.md (## Babysit instructs relaying the CLI's reason verbatim)"
    assert_contains_any "${BABYSIT_SECTION}" \
      "phase-9-pr.md (## Babysit forbids re-deriving/re-wording the reason)" \
      "re-derive" "re-word" "reword" "rederive"
    # Bind the re-arm command to its "host tmux pane" context (grep -A, like
    # autonomous-loop-protectedpaths-trailing-slash.test.sh's BULLET_BLOCK) —
    # a bare substring check on "cenci babysit <pr-number> --agent claude"
    # would pass vacuously today: that exact string is already a prefix of
    # the unrelated initial-launch command a few lines above
    # ("cenci babysit <pr-number> --agent claude --interval <interval>").
    REARM_BLOCK="$(printf '%s\n' "${BABYSIT_SECTION}" | grep -A 3 -F 'host tmux pane')"
    if [[ -z "${REARM_BLOCK}" ]]; then
      fail "phase-9-pr.md (## Babysit does not name a host tmux pane for the re-arm)"
    else
      assert_contains "${REARM_BLOCK}" "cenci babysit <pr-number> --agent claude" \
        "phase-9-pr.md (## Babysit prints the host re-arm command near the host tmux pane mention)"
    fi
    assert_contains_any "${BABYSIT_SECTION}" \
      "phase-9-pr.md (## Babysit states the phase stays green on this outcome)" \
      "stays green" "stay green" "keeps the phase green" "keeping the phase green" \
      "remains green" "phase remains green"

    # 2. The old unconditional framing sentence ("Finally, report the
    # terminal state as the PR being open and watched, not as done/merged")
    # must be gone — the terminal report must become an outcome-classified
    # two-form report (watched / not-watched), never a single unconditional
    # claim. Assert the specific replacement text at the edit site, per
    # docs/shell-scripting-gotchas.md rule 3 — never a bare keyword like
    # "watched", which the armed-outcome form legitimately keeps.
    assert_not_contains "${BABYSIT_SECTION}" \
      "Finally, report the terminal state as the PR being open and watched, not as done/merged:" \
      "phase-9-pr.md (## Babysit no longer frames the watched report as the phase's one unconditional terminal state)"
  fi
fi

# =====================================================================
# 3. implement/SKILL.md and implement/codex.md — the same not-armed
# distinction must be restated at both sites (flow/AGENTS.md #979's
# multi-site restatement rule). Whole-file scope, matching
# implement-babysit-launch.test.sh's own precedent for this same file pair;
# safe from a vacuous pass since neither file mentions any of these phrases
# today (verified while writing this test).
# =====================================================================
FILE="${FLOW_DIR}/skills/implement/SKILL.md"
if require_file CONTENT "${FILE}"; then
  assert_contains_any "${CONTENT}" \
    "implement/SKILL.md (:333 restatement carries the not-armed distinction)" \
    "not armed" "arm status unknown" "not being watched" "PR open, not being watched"
fi

FILE="${FLOW_DIR}/skills/implement/codex.md"
if require_file CONTENT "${FILE}"; then
  assert_contains_any "${CONTENT}" \
    "implement/codex.md (babysit hand-off carries the not-armed distinction)" \
    "not armed" "arm status unknown" "not being watched" "PR open, not being watched"
fi

# =====================================================================
# 4. babysit/SKILL.md — "the supervisor always runs on the host", the host
# state-dir path (with its fallback), and the <pr>.json/.log verification
# file pattern. `allowed-tools` must stay byte-identical (captured from the
# current committed file, not hardcoded, per this suite's own instructions —
# allowed-tools-sweep.test.sh independently pins the grant set, this is a
# second, narrower guard scoped to this ticket's own no-new-grant constraint).
# =====================================================================
FILE="${FLOW_DIR}/skills/babysit/SKILL.md"
if require_file CONTENT "${FILE}"; then
  CURRENT_ALLOWED_TOOLS_LINE="$(extract_line "${CONTENT}" 'allowed-tools:')"
  EXPECTED_ALLOWED_TOOLS_LINE='allowed-tools: Read, Bash(cenci babysit:*), Bash(sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh":*)'
  if [[ -z "${CURRENT_ALLOWED_TOOLS_LINE}" ]]; then
    fail "babysit/SKILL.md: allowed-tools frontmatter line not found"
  elif [[ "${CURRENT_ALLOWED_TOOLS_LINE}" != "${EXPECTED_ALLOWED_TOOLS_LINE}" ]]; then
    fail "babysit/SKILL.md: allowed-tools frontmatter must stay unchanged for this ticket — got [${CURRENT_ALLOWED_TOOLS_LINE}], expected [${EXPECTED_ALLOWED_TOOLS_LINE}]"
  fi

  assert_contains_any "${CONTENT}" \
    "babysit/SKILL.md (states the supervisor always runs on the host)" \
    "always runs on the host" "runs on the host" "supervisor always runs on the host"
  assert_contains_any "${CONTENT}" \
    "babysit/SKILL.md (host state-dir path)" \
    "\$XDG_STATE_HOME/cenci/babysit" "XDG_STATE_HOME/cenci/babysit"
  assert_contains "${CONTENT}" "~/.local/state/cenci/babysit" \
    "babysit/SKILL.md (host state-dir fallback path)"
  assert_contains "${CONTENT}" "pr>.json" \
    "babysit/SKILL.md (state-file verification pattern, .json)"
  assert_contains "${CONTENT}" "pr>.log" \
    "babysit/SKILL.md (state-file verification pattern, .log)"
fi

# =====================================================================
# 5. docs/autonomous-loop.md `## What still stops the machine` — names the
# forwarded/sandboxed arming path and the not-armed outcome. Scoped to this
# heading: the bare substring "forward" already appears elsewhere in this
# same section today (an unrelated "delegation's forwarded provenance"
# sentence), so the assertion below must not use a bare "forward" keyword.
# =====================================================================
FILE="${REPO_ROOT}/docs/autonomous-loop.md"
if require_file CONTENT "${FILE}"; then
  STOPS_SECTION="$(extract_section "${CONTENT}" '## What still stops the machine')"
  if [[ -z "${STOPS_SECTION}" ]]; then
    fail "docs/autonomous-loop.md: ## What still stops the machine section not found"
  else
    assert_contains_any "${STOPS_SECTION}" \
      "docs/autonomous-loop.md (## What still stops the machine names forwarded/sandboxed arming)" \
      "forwarded to the host daemon" "arming is forwarded" "forward the arm request" \
      "sandboxed arming is forwarded"
    assert_contains "${STOPS_SECTION}" "not armed" \
      "docs/autonomous-loop.md (## What still stops the machine names the not-armed outcome)"
  fi
fi

# =====================================================================
# 6. Anti-drift cross-check — every outcome literal the docs above are
# expected to quote verbatim must actually exist in
# watch/internal/babysit/arm.go, the client's own source of truth. An
# unreadable arm.go fails closed (require_file already does this).
# =====================================================================
FILE="${REPO_ROOT}/watch/internal/babysit/arm.go"
if require_file ARM_CONTENT "${FILE}"; then
  assert_contains "${ARM_CONTENT}" "not armed" \
    "watch/internal/babysit/arm.go (source of truth for the 'not armed' outcome string)"
  assert_contains "${ARM_CONTENT}" "arm status unknown" \
    "watch/internal/babysit/arm.go (source of truth for the 'arm status unknown' outcome string)"
  assert_contains "${ARM_CONTENT}" "the supervisor now runs on the host" \
    "watch/internal/babysit/arm.go (source of truth for the armed-outcome message)"
fi

echo "babysit-host-supervisor-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
