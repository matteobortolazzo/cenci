#!/usr/bin/env bash
# Contract test for ticket #928 — enroll the current repo in fleet dispatch
# from `/cenci:configure`, with its required per-repo tmux session.
#
# Why this exists: `/cenci:configure` never touched fleet dispatch
# configuration (`~/.config/cenci/config.json`'s `dispatch.repos[]`), so a
# user who turns on fleet dispatch reached a broken or silently-wrong state
# with no guidance. After #927, an enrolled repo with no `session` is a
# guaranteed-dead configuration (the daemon reports
# `dispatch_session_unconfigured` on every pass), which makes the gap sharp
# enough to close here. This suite pins the new `### Fleet Dispatch
# Enrollment` section's load-bearing prose in `flow/skills/configure/
# SKILL.md` (questions 11/12, the container guard, the three-way branch on
# `status --json`'s `enrolled`/`session` state, the explicit `--dir
# '<main-checkout>'` on both CLI verbs, the single combined enroll call, the
# missing-`session`-key version-skew rung, the no-guessing rule, the
# delegation-only rule, and the absence of `loopEnabled`/`daemonInterval`
# questions), the two narrow new `allowed-tools` grants, the `codex.md`
# parity paragraph, and the `docs/orchestration.md` pointer naming
# `/cenci:configure` as an enrollment entry point.
#
# #928 is flow-only and stays inert until #933 (`enroll --session` / `status
# --json`'s `session` key) ships — this suite pins the flow-side prose
# against today's CLI regardless, since the missing-`session`-key rung is
# precisely the version-skew guard for a #933-less binary.
#
# Follows the idiom of flow/tests/design-sandbox-guard.test.sh exactly: a
# `failures=` counter, small assert_* helpers, `read_doc_raw`/`require_doc`
# for in-project docs, `read_repo_doc_raw`/`require_repo_doc` for the
# cross-project `docs/orchestration.md`, exact substring markers (never
# generic keywords — see docs/shell-scripting-gotchas.md), self-contained,
# auto-discovered by the flow gate's `*.test.sh` glob. It greps the real
# committed docs directly; no fixtures.
#
# CI path-filter caveat (same accepted coupling as design-sandbox-guard.test.sh):
# this suite lives under `flow/tests/**` and is only run by `flow-ci.yml`'s
# `flow-test` job, which is path-filtered on `flow == 'true'`. One assertion
# below reaches outside `flow/**` (`docs/orchestration.md`). A future PR that
# edits only `docs/orchestration.md` will not trigger `flow-ci` and so will
# not run this suite in CI — only the local flow gate (or the next
# flow-touching PR) would catch a regression there. This is a documented,
# accepted coupling risk, not a bug in this test.
#
# Covered files:
#   - flow/skills/configure/SKILL.md
#   - flow/skills/configure/codex.md
#   - docs/orchestration.md (cross-project)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-dispatch-enrollment.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-dispatch-enrollment.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "configure-dispatch-enrollment.test.sh: failed to resolve repo root." >&2; exit 2; }
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

read_repo_doc_raw() {
  # read_repo_doc_raw <repo-relative-path> — same pure-extraction contract as
  # read_doc_raw, but resolved from REPO_ROOT for the cross-project
  # docs/orchestration.md, which lives outside flow/**. No fail() side effect
  # here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  local _path="${REPO_ROOT}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_repo_doc <result-var> <repo-relative-path> — nameref wrapper
# around read_repo_doc_raw. Must NOT be invoked via $(...).
require_repo_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_repo_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${REPO_ROOT}/${_relpath}"
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
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

normalize_ws() {
  # normalize_ws <content> — collapses newlines and repeated whitespace to a
  # single space so a markdown-wrapped sentence can be matched as one
  # substring, per docs/shell-scripting-gotchas.md's line-wrapping pitfall.
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws <content> <required-substring> <label> — whitespace-
# normalized variant of assert_contains, for sentences that may be
# line-wrapped in the source markdown.
assert_contains_ws() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired <content> <existence-marker> <forbidden-substring> <label>
#
# Per the plan's Assumption 7: `loopEnabled`/`daemonInterval` already appear
# zero times in unmodified SKILL.md/codex.md today, so a bare
# assert_not_contains would pass vacuously even before the new section is
# authored — it would prove nothing about this ticket. Requiring the
# section's own existence marker to be present first means this only proves
# the intended thing (the section exists AND deliberately omits those
# questions), and fails loudly — for the right reason — while the section is
# still unauthored (this red phase).
assert_absent_paired() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  if [[ "${content}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing: [${marker}]"
    return
  fi
  [[ "${content}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present: [${forbidden}]"
}

# --- Exact authored substrings Phase 4 (green) must add verbatim -----------
#
# These are pinned here, not derived, so the red phase fails for the right
# reason and the green phase has an unambiguous authoring target.

SECTION_HEADING='### Fleet Dispatch Enrollment'
Q11_MARKER='11. **Enroll in fleet dispatch**'
Q12_MARKER='12. **Fleet dispatch session name**'

# Container guard (runs first, host/environment only).
CONTAINER_GUARD_EFFECT='asks nothing, runs no `cenci dispatch` command, and writes nothing anywhere'
HOST_FIX_COMMAND='cenci dispatch enroll --session <name>` from the repo root on the host'

# Container guard's exit wording must be unambiguous about stopping (not
# "fall through", which elsewhere in this section means "continue the run").
CONTAINER_GUARD_EXIT='Skip the remainder of this section (main-checkout resolution, status probe, questions 11/12) and continue directly to the next section (`### Auth Verification`)'

# Main-checkout resolution.
GIT_COMMON_DIR_RESOLUTION='git rev-parse --path-format=absolute --git-common-dir'

# Status probe, explicit --dir on the main checkout.
STATUS_INVOCATION="cenci dispatch status --json --dir '<main-checkout>'"

# Three-way branch states (verbatim from the ticket's own Acceptance Criteria
# wording, since that is the language the section is modelled on).
STATE_NOT_ENROLLED='enrolled: false'
STATE_ENROLLED_NO_SESSION='enrolled: true, session: ""'
STATE_ENROLLED_WITH_SESSION='enrolled: true, session: "<set>"'

# Single combined write — never two separate writes.
ENROLL_INVOCATION="cenci dispatch enroll --dir '<main-checkout>' --session '<name>'"

# Missing-session-key version-skew rung.
MISSING_SESSION_KEY='no `session` key'
UPDATE_CENCI_ADVISORY='advise the user to update cenci before enrolling'

# No-guessing rule.
NO_GUESS_AMBIENT='$TMUX_PANE'
NO_GUESS_RULE='never guesses the session name'

# Delegation-only rule — reused verbatim in both SKILL.md and codex.md.
DELEGATION_ONLY_RULE='never reads, modifies, or writes ~/.config/cenci/config.json itself'

# Frontmatter grants.
GRANT_STATUS='Bash(cenci dispatch status:*)'
GRANT_ENROLL='Bash(cenci dispatch enroll:*)'
BLANKET_CENCI='Bash(cenci:*)'
BLANKET_DISPATCH='Bash(cenci dispatch:*)'

# codex.md parity paragraph.
CODEX_CONTAINER_GUARD='In-container, the section asks nothing, runs nothing, and writes nothing'
CODEX_PLAN_MODE_NOTE='the fleet dispatch enroll is a mutation and therefore runs only in the apply step'
CODEX_NEVER_DURING_PLAN='never during `/plan`'

# docs/orchestration.md pointer.
ORCHESTRATION_POINTER='`/cenci:configure` is also a fleet dispatch enrollment entry point'

# --- skills/configure/SKILL.md ----------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
if [[ -n "${skill}" ]]; then
  skill_path="${FLOW_DIR}/skills/configure/SKILL.md"

  # Section heading and question numbering (paired: a missing heading means
  # the section-scoped absence checks below fail for the right reason too).
  assert_contains "${skill}" "${SECTION_HEADING}" "SKILL.md Fleet Dispatch Enrollment heading"
  assert_contains "${skill}" "${Q11_MARKER}" "SKILL.md question 11 (enroll)"
  assert_contains "${skill}" "${Q12_MARKER}" "SKILL.md question 12 (session name)"

  # Container guard: no ask, no run, no write; falls through.
  assert_contains "${skill}" "${CONTAINER_GUARD_EFFECT}" "SKILL.md container guard ask/run/write nothing"
  assert_contains "${skill}" "${HOST_FIX_COMMAND}" "SKILL.md container guard host-side fix command"
  assert_contains_ws "${skill}" "${CONTAINER_GUARD_EXIT}" "SKILL.md container guard exit wording is unambiguous"

  # The advisory's host-side fix line must never fabricate a --dir value —
  # checked on the specific line the fix command appears on, not the whole
  # file (the file legitimately uses --dir elsewhere in this same section).
  host_fix_line="$(grep -n 'cenci dispatch enroll --session <name>' "${skill_path}" 2>/dev/null | head -1)"
  if [[ -z "${host_fix_line}" ]]; then
    fail "SKILL.md container guard host-side fix command: line not found"
  elif [[ "${host_fix_line}" == *"--dir"* ]]; then
    fail "SKILL.md container guard host-side fix command: must not include --dir: [${host_fix_line}]"
  fi

  # Main-checkout resolution, as its own Bash call.
  assert_contains "${skill}" "${GIT_COMMON_DIR_RESOLUTION}" "SKILL.md main-checkout git rev-parse resolution"

  # Status probe with explicit --dir on the main checkout.
  assert_contains "${skill}" "${STATUS_INVOCATION}" "SKILL.md status probe explicit --dir '<main-checkout>'"

  # Three-way branch, all three states.
  assert_contains "${skill}" "${STATE_NOT_ENROLLED}" "SKILL.md three-way branch: enrolled:false"
  assert_contains "${skill}" "${STATE_ENROLLED_NO_SESSION}" "SKILL.md three-way branch: enrolled:true session:\"\""
  assert_contains "${skill}" "${STATE_ENROLLED_WITH_SESSION}" "SKILL.md three-way branch: enrolled:true session:<set>"

  # Single combined enroll call — both values in one CLI call.
  assert_contains "${skill}" "${ENROLL_INVOCATION}" "SKILL.md single combined enroll call"

  # Missing-session-key version-skew rung.
  assert_contains "${skill}" "${MISSING_SESSION_KEY}" "SKILL.md missing session key rung"
  assert_contains "${skill}" "${UPDATE_CENCI_ADVISORY}" "SKILL.md update-cenci advisory"

  # No-guessing rule.
  assert_contains "${skill}" "${NO_GUESS_AMBIENT}" "SKILL.md no-guessing rule names \$TMUX_PANE"
  assert_contains "${skill}" "${NO_GUESS_RULE}" "SKILL.md no-guessing rule statement"

  # Delegation-only rule.
  assert_contains "${skill}" "${DELEGATION_ONLY_RULE}" "SKILL.md delegation-only rule"

  # Absence of loopEnabled/daemonInterval questions, paired with the section
  # existence marker per Assumption 7 (see assert_absent_paired above).
  assert_absent_paired "${skill}" "${SECTION_HEADING}" "loopEnabled" "SKILL.md no loopEnabled question"
  assert_absent_paired "${skill}" "${SECTION_HEADING}" "daemonInterval" "SKILL.md no daemonInterval question"

  # No bare `--dir .` / bare-default enroll call anywhere.
  assert_not_contains "${skill}" "--dir ." "SKILL.md no bare --dir . default"

  # Frontmatter grants: the two new narrow per-verb grants present, the two
  # blanket forms absent.
  allowed_line="$(grep -m1 '^allowed-tools:' "${skill_path}")"
  assert_contains "${allowed_line}" "${GRANT_STATUS}" "SKILL.md allowed-tools cenci dispatch status grant"
  assert_contains "${allowed_line}" "${GRANT_ENROLL}" "SKILL.md allowed-tools cenci dispatch enroll grant"
  assert_not_contains "${allowed_line}" "${BLANKET_CENCI}" "SKILL.md allowed-tools blanket Bash(cenci:*) grant"
  assert_not_contains "${allowed_line}" "${BLANKET_DISPATCH}" "SKILL.md allowed-tools blanket Bash(cenci dispatch:*) grant"
fi

# --- skills/configure/codex.md ----------------------------------------------

require_doc codex "skills/configure/codex.md" || true
if [[ -n "${codex}" ]]; then
  # Three-way branch mirrored (same enrolled/session states named).
  assert_contains "${codex}" "${STATE_ENROLLED_NO_SESSION}" "codex.md three-way branch mirror"

  # Container guard mirrored.
  assert_contains "${codex}" "${CODEX_CONTAINER_GUARD}" "codex.md container guard mirror"

  # Delegation-only rule mirrored (same exact rule text as SKILL.md).
  assert_contains "${codex}" "${DELEGATION_ONLY_RULE}" "codex.md delegation-only rule mirror"

  # Plan-mode note: enroll is a mutation, runs only in the apply step, never
  # during /plan.
  assert_contains "${codex}" "${CODEX_PLAN_MODE_NOTE}" "codex.md plan-mode note (apply step only)"
  assert_contains "${codex}" "${CODEX_NEVER_DURING_PLAN}" "codex.md plan-mode note (never during /plan)"
fi

# --- Cross-project: docs/orchestration.md ------------------------------------

require_repo_doc orchestration "docs/orchestration.md" || true
if [[ -n "${orchestration}" ]]; then
  assert_contains_ws "${orchestration}" "${ORCHESTRATION_POINTER}" "docs/orchestration.md /cenci:configure enrollment entry point pointer"
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-dispatch-enrollment.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-dispatch-enrollment.test.sh: all checks passed."
