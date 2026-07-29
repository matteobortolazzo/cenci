#!/usr/bin/env bash
# Contract test for ticket #772 — fix contradiction/drift defects across
# flow/skills/ plus the full `/tmp/claude/` -> `${TMPDIR:-/tmp}/cenci/`
# temp-root migration.
#
# Why this exists: several skills accumulated small, independently-landed
# edits that now contradict each other or a shared convention — a followup
# `gh issue list` search missing the same `--limit 200` pagination guard its
# sibling call sites already carry, leftover bare `#<n>`-substring fallback
# wording from before the native-URL-match rewrite, a stale `tick.sh` script
# name, a partially-migrated `/tmp/claude/` temp root, bare (non-`/cenci:`)
# slash-command mentions, a worktree-creation invocation that dropped both
# `-C <repo-root>` and an explicit default-branch base, a `.`/`..`-rejection
# clause restated in one file but not its sibling, misnumbered Design phase
# headings, and an inconsistent "degraded" framing between address-review and
# implement's Phase 9. This suite pins each fixed shape down so a future edit
# can't silently reintroduce the drift.
#
# Follows the idiom of flow/tests/refine-skill-contract.test.sh and
# flow/tests/design-sandbox-guard.test.sh: a `failures=` counter, small
# assert_* helpers, exact substring/line-number markers (never generic
# keywords — see docs/shell-scripting-gotchas.md), self-contained, auto-
# discovered by the flow gate's `*.test.sh` glob. It greps the real committed
# docs directly; no fixtures.
#
# RED-phase note: this suite is written before the ticket's fixes land, so
# most cases below are expected to fail against the current tree — that is
# the point of this suite at this stage. Case 9's "address-review still says
# graceful degrade" sub-assertion is the one expected exception: it pins
# existing, unchanged behavior.
#
# Covered files:
#   - flow/.mcp.json
#   - flow/skills/address-review/SKILL.md
#   - flow/ (repo-wide, tick.sh + bare slash-commands)
#   - flow/skills/ (repo-wide, /tmp/claude/ literal)
#   - flow/skills/implement/phases/phase-2-worktree.md
#   - flow/skills/maintain/modes/backlog.md
#   - flow/skills/design/SKILL.md
#   - flow/skills/implement/phases/phase-9-pr.md
#   - flow/templates/settings.json
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "skill-convention-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "skill-convention-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — prints the real committed file's
  # content on stdout, or nothing (non-zero exit) if missing/unreadable. Pure
  # extraction, no fail() side effect here: it is deliberately safe to call
  # inside a $(...) command substitution.
  local path="${FLOW_DIR}/$1"
  cat "${path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper around
# read_doc_raw that assigns the file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing file must never masquerade as empty content, which would make
# assert_not_contains trivially pass). Must NOT be invoked via $(...): a
# fail() call made from inside a command-substitution subshell only
# increments that subshell's copy of `failures`, silently losing the failure
# count in the parent shell (docs/shell-scripting-gotchas.md's "never call
# fail() from within $(...)" rule).
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "772 ${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
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

# =====================================================================
# 1. flow/.mcp.json — context7 MCP server config, disabled by default
# =====================================================================

mcp_path="${FLOW_DIR}/.mcp.json"
if [[ ! -f "${mcp_path}" ]]; then
  fail "772 flow/.mcp.json: file not found: ${mcp_path}"
else
  if ! jq -e '.mcpServers.context7.command == "npx"' "${mcp_path}" >/dev/null 2>&1; then
    fail "772 flow/.mcp.json: .mcpServers.context7.command != \"npx\""
  fi
  if ! jq -e '.mcpServers.context7.args == ["-y","@upstash/context7-mcp"]' "${mcp_path}" >/dev/null 2>&1; then
    fail "772 flow/.mcp.json: .mcpServers.context7.args != [\"-y\",\"@upstash/context7-mcp\"]"
  fi
  if ! jq -e '.mcpServers.context7.env | has("CONTEXT7_API_KEY")' "${mcp_path}" >/dev/null 2>&1; then
    fail "772 flow/.mcp.json: .mcpServers.context7.env is missing a CONTEXT7_API_KEY key"
  fi
  if ! jq -e '.mcpServers.context7.disabled == true' "${mcp_path}" >/dev/null 2>&1; then
    fail "772 flow/.mcp.json: .mcpServers.context7.disabled != true"
  fi
fi

# =====================================================================
# 2. flow/skills/address-review/SKILL.md — Locate step pagination +
#    back-link wording + dropped bare #<n> fallback
# =====================================================================

require_doc address_review "skills/address-review/SKILL.md" || true
if [[ -n "${address_review}" ]]; then
  assert_contains "${address_review}" "--limit 200" \
    "772 flow/skills/address-review/SKILL.md Locate step --limit 200 pagination guard"
  assert_contains "${address_review}" "Related to #<original-ticket>" \
    "772 flow/skills/address-review/SKILL.md Related to #<original-ticket> back-link"
  assert_not_contains "${address_review}" "the PR's \`#<number>\`" \
    "772 flow/skills/address-review/SKILL.md stale bare #<pr-number> fallback wording"
  # Both dedup keys must be whole-line matches: plain `grep -qF` is a
  # within-line substring match, so `.../pull/7` would still match a body
  # containing `.../pull/70` — only `grep -qxF` delivers the collision-safety
  # the predicate claims.
  assert_contains "${address_review}" "matched as an exact whole line" \
    "772 flow/skills/address-review/SKILL.md whole-line dedup match wording"
  assert_contains "${address_review}" "grep -qxF" \
    "772 flow/skills/address-review/SKILL.md grep -qxF whole-line dedup primitive"
  assert_not_contains "${address_review}" "trailing newline included" \
    "772 flow/skills/address-review/SKILL.md unachievable trailing-newline grep recipe"
fi

# =====================================================================
# 3. Zero tick.sh occurrences anywhere under flow/ (repo-wide). Excludes
#    this suite's own file, which documents the ticket's intent by name in
#    its header comment — that self-mention is not a production occurrence
#    and must not make this case permanently red after the real fix lands.
# =====================================================================

SELF_NAME="$(basename "${BASH_SOURCE[0]}")"
tick_matches="$(grep -rIn 'tick\.sh' "${FLOW_DIR}" --exclude="${SELF_NAME}" 2>/dev/null || true)"
if [[ -n "${tick_matches}" ]]; then
  fail "772 flow/: tick.sh occurrences found (expected zero):
${tick_matches}"
fi

# =====================================================================
# 4. Zero literal /tmp/claude/ occurrences under flow/skills/ (permission
#    strings like //tmp/claude*/** do not match this literal and are fine)
# =====================================================================

tmp_claude_matches="$(grep -rIn '/tmp/claude/' "${FLOW_DIR}/skills" 2>/dev/null || true)"
if [[ -n "${tmp_claude_matches}" ]]; then
  fail "772 flow/skills/: literal /tmp/claude/ occurrences found (expected zero):
${tmp_claude_matches}"
fi

# =====================================================================
# 5. Zero bare /implement, /refine, /design, /review slash-commands under
#    flow/skills/ — every occurrence must be the canonical /cenci:<name>
#    form. Scoped to avoid matching path segments (e.g. .../design-comment-
#    <n>.md) by requiring the slash not be preceded by a path/word char and
#    not be followed by one.
# =====================================================================

bare_slash_matches="$(grep -rnoE '(^|[^a-zA-Z0-9_/-])/(implement|refine|design|review)([^a-zA-Z0-9_/-]|$)' "${FLOW_DIR}/skills" 2>/dev/null || true)"
if [[ -n "${bare_slash_matches}" ]]; then
  fail "772 flow/skills/: bare (non-/cenci:) slash-command mentions found (expected zero):
${bare_slash_matches}"
fi

# =====================================================================
# 6. flow/skills/implement/phases/phase-2-worktree.md — ticketless worktree
#    creation carries -C <repo-root> and an explicit, resolved default-branch
#    base (never a hardcoded `main`, which fails on master/trunk repos)
# =====================================================================

require_doc phase2 "skills/implement/phases/phase-2-worktree.md" || true
if [[ -n "${phase2}" ]]; then
  assert_contains "${phase2}" \
    'git -C <repo-root> worktree add .worktrees/<auto-slug> -b feature/<auto-slug> <default-branch>' \
    "772 flow/skills/implement/phases/phase-2-worktree.md ticketless worktree-add invocation"
  assert_contains "${phase2}" \
    'symbolic-ref --short refs/remotes/origin/HEAD' \
    "772 flow/skills/implement/phases/phase-2-worktree.md default-branch resolution command"
  assert_not_contains "${phase2}" \
    '-b feature/<auto-slug> main' \
    "772 flow/skills/implement/phases/phase-2-worktree.md hardcoded main base branch"
fi

# =====================================================================
# 7. flow/skills/maintain/modes/backlog.md — restates the same rejecting
#    `.`, `..` clause SKILL.md already carries for its own run-token
# =====================================================================

require_doc backlog "skills/maintain/modes/backlog.md" || true
if [[ -n "${backlog}" ]]; then
  assert_contains "${backlog}" 'rejecting `.`, `..`' \
    "772 flow/skills/maintain/modes/backlog.md rejecting \`.\`, \`..\` clause"
fi

# =====================================================================
# 8. flow/skills/design/SKILL.md — Design heading renumbering + Label
#    "Working" (at start) placement, verified via line-number comparisons
# =====================================================================

design_path="${FLOW_DIR}/skills/design/SKILL.md"
if [[ -r "${design_path}" ]]; then
  line_gen="$(grep -nF '## Phase 5 — Generate DESIGN.md' "${design_path}" | head -1 | cut -d: -f1)"
  line_report="$(grep -nF '## Phase 6 — Report Summary' "${design_path}" | head -1 | cut -d: -f1)"
  line_commit="$(grep -nF '## Phase 7 — Commit Design' "${design_path}" | head -1 | cut -d: -f1)"

  if [[ -z "${line_gen}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '## Phase 5 — Generate DESIGN.md' heading found"
  fi
  if [[ -z "${line_report}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '## Phase 6 — Report Summary' heading found"
  fi
  if [[ -z "${line_commit}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '## Phase 7 — Commit Design' heading found"
  fi
  if [[ -n "${line_gen}" && -n "${line_report}" && -n "${line_commit}" ]]; then
    if (( line_gen >= line_report )); then
      fail "772 flow/skills/design/SKILL.md: '## Phase 5 — Generate DESIGN.md' (line ${line_gen}) does not precede '## Phase 6 — Report Summary' (line ${line_report})"
    fi
    if (( line_report >= line_commit )); then
      fail "772 flow/skills/design/SKILL.md: '## Phase 6 — Report Summary' (line ${line_report}) does not precede '## Phase 7 — Commit Design' (line ${line_commit})"
    fi
  fi

  line_phase2="$(grep -nF '## Phase 2 —' "${design_path}" | head -1 | cut -d: -f1)"
  line_phase25="$(grep -nF '## Phase 2.5 —' "${design_path}" | head -1 | cut -d: -f1)"
  line_label="$(grep -nF '### Label "Working" (at start)' "${design_path}" | head -1 | cut -d: -f1)"

  if [[ -z "${line_phase2}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '## Phase 2 —' heading found"
  fi
  if [[ -z "${line_phase25}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '## Phase 2.5 —' heading found"
  fi
  if [[ -z "${line_label}" ]]; then
    fail "772 flow/skills/design/SKILL.md: no '### Label \"Working\" (at start)' heading found"
  fi
  if [[ -n "${line_phase2}" && -n "${line_phase25}" && -n "${line_label}" ]]; then
    if (( line_label <= line_phase2 || line_label >= line_phase25 )); then
      fail "772 flow/skills/design/SKILL.md: '### Label \"Working\" (at start)' (line ${line_label}) is not between '## Phase 2 —' (line ${line_phase2}) and '## Phase 2.5 —' (line ${line_phase25})"
    fi
  fi
else
  fail "772 skills/design/SKILL.md: doc not found/unreadable: ${design_path}"
fi

# =====================================================================
# 9. flow/skills/implement/phases/phase-9-pr.md — ticketless-only framing
#    replaces "a degraded fetch"; address-review keeps its own graceful-
#    degrade wording unchanged (this sub-assertion is expected to PASS today)
# =====================================================================

require_doc phase9 "skills/implement/phases/phase-9-pr.md" || true
if [[ -n "${phase9}" ]]; then
  assert_contains "${phase9}" "(ticketless mode only" \
    "772 flow/skills/implement/phases/phase-9-pr.md ticketless-mode-only framing"
  assert_not_contains "${phase9}" "degraded fetch" \
    "772 flow/skills/implement/phases/phase-9-pr.md stale 'degraded fetch' wording"
  # phase-9 is the canonical source of the whole-line dedup rule that
  # address-review mirrors (case 2) — pin the same grep -qxF semantics here
  # so the two files cannot drift apart on the collision-safety primitive.
  assert_contains "${phase9}" "grep -qxF 'Related to #<original-ticket>'" \
    "772 flow/skills/implement/phases/phase-9-pr.md grep -qxF whole-line back-link match"
  assert_not_contains "${phase9}" "trailing newline included" \
    "772 flow/skills/implement/phases/phase-9-pr.md unachievable trailing-newline grep recipe"
fi

if [[ -n "${address_review}" ]]; then
  assert_contains "${address_review}" "graceful degrade" \
    "772 flow/skills/address-review/SKILL.md unchanged graceful-degrade wording"
fi

# =====================================================================
# 10. flow/templates/settings.json — permission strings for both the old
#     /tmp/claude and the new /tmp/cenci temp roots
# =====================================================================

settings_path="${FLOW_DIR}/templates/settings.json"
if [[ ! -f "${settings_path}" ]]; then
  fail "772 flow/templates/settings.json: file not found: ${settings_path}"
else
  for entry in 'Read(//tmp/claude*/**)' 'Edit(//tmp/claude*/**)' 'Read(//tmp/cenci*/**)' 'Edit(//tmp/cenci*/**)'; do
    if ! jq -e --arg e "${entry}" '.permissions.allow | index($e) != null' "${settings_path}" >/dev/null 2>&1; then
      fail "772 flow/templates/settings.json: permissions.allow missing entry: ${entry}"
    fi
  done
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "skill-convention-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "skill-convention-contract.test.sh: all checks passed."
