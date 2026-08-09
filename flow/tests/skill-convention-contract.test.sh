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
# Case 1 was re-pointed by #796: context7 MCP moves from plugin scope to
# project scope, pinned to a specific version, and flow/.mcp.json is removed.
#
# #813 extends case 1 into the full Context7 migration contract (both
# permission-pair tool names, the legacy-cleanup enabled-add/dedup and
# declined-removal steps, unrelated-entry preservation, and the MCP Server
# Configuration section's merge/never-overwrite, args-only version-refresh,
# and declined-server entry-removal sentences) and adds two new cases for
# the repo-root .mcp.json and .claude/settings.json files. Every Context7
# anchor is an exact (or whitespace-normalized) procedure-text anchor, never
# a fixture: flow/skills/configure/SKILL.md is a Markdown procedure with no
# executable migration code to drive, so a fixture harness would have to
# re-implement the procedure in the test, proving the test's
# re-implementation rather than the shipped procedure (ticket #813 Q&A).
#
# Covered files:
#   - flow/.mcp.json (asserted absent)
#   - flow/skills/configure/SKILL.md
#   - .mcp.json (repo root; #813)
#   - .claude/settings.json (repo root; #813)
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

# assert_contains_ws <content> <required-phrase> <label>
# Whitespace-insensitive required-phrase match: collapses whitespace runs
# (space/tab/newline) to a single space in both <content> and
# <required-phrase> before a literal substring test, so a Markdown line wrap
# between words (e.g. the Context7 migration procedure's wrapped steps)
# never breaks a match that would otherwise hold, while the full required
# word sequence stays mandatory (a dropped word still fails the match).
# Modeled on _contains_ws_insensitive in flow/tests/parity/contract-lib.sh,
# including its empty-pattern guard (an empty <required-phrase> would
# otherwise vacuously match any content via the `*""* ` glob below).
assert_contains_ws() {
  local content="$1" phrase="$2" label="$3" normalized_content normalized_phrase
  if [[ -z "${phrase}" ]]; then
    fail "${label}: empty required pattern (test bug)"
    return
  fi
  normalized_content="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')" || { fail "${label}: failed to normalize content whitespace"; return; }
  normalized_phrase="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')" || { fail "${label}: failed to normalize phrase whitespace"; return; }
  [[ "${normalized_content}" == *"${normalized_phrase}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${phrase}]"
}

# REPO_ROOT — resolved once from FLOW_DIR/.. with a checked cd (root
# AGENTS.md: never unchecked command substitution for a security-critical
# path). Needed by the new repo-root .mcp.json / .claude/settings.json
# cases below (#813) — flow/templates/settings-permissions.test.sh already
# resolves REPO_ROOT the same way (two levels up from its own script dir).
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "skill-convention-contract.test.sh: failed to resolve repo root." >&2; exit 2; }

# =====================================================================
# 1. flow/.mcp.json (removed) + flow/skills/configure/SKILL.md — context7
#    MCP server moves from plugin scope to project scope, pinned to a
#    specific version (#796)
# =====================================================================

mcp_path="${FLOW_DIR}/.mcp.json"
if [[ ! -e "${mcp_path}" ]]; then
  true # expected: removed by #796 (context7 moves to project scope)
else
  fail "796 flow/.mcp.json: file still exists (expected removed): ${mcp_path}"
fi

require_doc configure_skill "skills/configure/SKILL.md" || true
if [[ -n "${configure_skill}" ]]; then
  assert_contains "${configure_skill}" \
    '| *(always available)* | context7 | `npx` | `["-y", "@upstash/context7-mcp@3.2.5"]` | `CONTEXT7_API_KEY` | project |' \
    "796 skills/configure/SKILL.md: pinned project-scoped context7 catalog row missing"
  assert_not_contains "${configure_skill}" \
    '@upstash/context7-mcp"]' \
    "796 skills/configure/SKILL.md: stale unpinned context7 args marker still present"
  # Vacuity gating (#813, root AGENTS.md): each negative below is preceded
  # by the positive anchor that proves its host section still exists, so a
  # section deletion/rename cannot make the negative pass vacuously.
  assert_contains "${configure_skill}" \
    "- **editor**:" \
    "796 skills/configure/SKILL.md: new editor-scope legend bullet missing"
  assert_not_contains "${configure_skill}" \
    "- **plugin**: Already defined in cenci's" \
    "796 skills/configure/SKILL.md: stale plugin-scope legend bullet still present"
  assert_contains "${configure_skill}" \
    "- **project**: Add to the project's root \`.mcp.json\`." \
    "813 skills/configure/SKILL.md: project-scoped .mcp.json legend bullet missing"
  assert_not_contains "${configure_skill}" \
    '${CLAUDE_PLUGIN_ROOT}/.mcp.json' \
    "796 skills/configure/SKILL.md: stale plugin-scoped .mcp.json reference still present"
  assert_contains "${configure_skill}" \
    "- **Context7**: \`mcp__context7__resolve-library-id\`, \`mcp__context7__query-docs\`" \
    "813 skills/configure/SKILL.md: Known tools Context7 permission-pair entry missing both mcp__context7__resolve-library-id and mcp__context7__query-docs"
  assert_not_contains "${configure_skill}" \
    "mcp__plugin_cenci_context7__resolve-library-id" \
    "796 skills/configure/SKILL.md: stale plugin-scoped context7 tool permission still present"
  assert_contains "${configure_skill}" \
    "mcp__plugin_cenci_context7__*" \
    "796 skills/configure/SKILL.md: legacy plugin-scoped context7 permission cleanup wildcard missing"

  # --- #813: full Context7 migration contract ------------------------------
  # Legacy cleanup (Context7 scope migration) section.
  assert_contains "${configure_skill}" \
    "**Legacy cleanup (Context7 scope migration)**" \
    "813 skills/configure/SKILL.md: Legacy cleanup (Context7 scope migration) heading missing"
  assert_contains_ws "${configure_skill}" \
    "2. If Context7 is enabled this run, ensure the project-scoped \`mcp__context7__resolve-library-id\` and \`mcp__context7__query-docs\` pair is present in \`permissions.allow\` — add whichever is missing, and deduplicate rather than appending a duplicate entry." \
    "813 skills/configure/SKILL.md: Context7 enabled add/deduplicate step missing"
  assert_contains "${configure_skill}" \
    "3. If Context7 is declined this run, remove the pair without adding a replacement." \
    "813 skills/configure/SKILL.md: Context7 declined removal step missing"
  assert_contains_ws "${configure_skill}" \
    "4. **Verify**: after healing, confirm \`permissions.allow\` contains no remaining entry with the \`mcp__plugin_cenci_context7__*\` prefix, and that every non-Context7 entry present before this step is still present unchanged. If either check fails, the heal step above was incomplete or over-broad — fix before continuing." \
    "813 skills/configure/SKILL.md: Context7 cleanup verify + unrelated-entry preservation step missing"

  # MCP Server Configuration section.
  assert_contains "${configure_skill}" \
    "### MCP Server Configuration" \
    "813 skills/configure/SKILL.md: MCP Server Configuration heading missing"
  assert_contains "${configure_skill}" \
    "- If the file already exists, merge into the existing \`mcpServers\` object — never overwrite existing entries, with two field-scoped exceptions:" \
    "813 skills/configure/SKILL.md: .mcp.json merge/never-overwrite sentence missing"
  assert_contains "${configure_skill}" \
    "  - **Version-pin refresh**: for a catalog server whose catalog \`Args\` are version-pinned (e.g. Context7), if an existing entry's \`args\` differ from the catalog value, overwrite **\`args\` only** — every other key (including \`env\` and any user-added keys) is preserved. This is the single explicit, documented exception to \"never overwrite existing entries.\"" \
    "813 skills/configure/SKILL.md: args-only version-pin-refresh sentence missing"
  assert_contains "${configure_skill}" \
    "  - **Declined servers**: for each catalog server recorded \`false\` in \`mcpServers\` (question 5's answer), delete that entry from an existing \`.mcp.json\` if present. If \`mcpServers\` becomes empty as a result, leave the file in place with an empty \`mcpServers\` object — never delete the file. (The \"never create an empty \`.mcp.json\`\" rule above applies to *creating* a new file only, not to editing an existing one down to empty.)" \
    "813 skills/configure/SKILL.md: declined-server entry-removal sentence missing"
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

# =====================================================================
# 11. #951 — cenci attribution banners + `<!-- cenci-<kind> -->` markers on
#     every flow-posted comment. Covers the five non-escalation call sites
#     (plan-comment, design-summary, parent-gap-report, followup-tracked,
#     plus the two PR-comment banner-only sites), each marker's exactly-one-
#     file uniqueness, and the bidirectional registry sync against the new
#     flow/docs/comment-attribution.md doc. The escalation banner itself and
#     its own two contract suites are out of scope here (AC #5) — this case
#     covers only the five non-escalation sites plus the two PR-only sites.
# =====================================================================

# Pinned literals (byte-exact — see 951-pinned-literals.md's Non-escalation
# banners + markers / PR comments tables). Each banner line is kept on a
# single markdown source line, per docs/shell-scripting-gotchas.md's
# line-wrapping rule.
PIN_951_PLAN_COMMENT_BANNER='> 🤖 **cenci** — implementation plan posted by `/cenci:implement` (planning).'
PIN_951_PLAN_COMMENT_MARKER='<!-- cenci-plan-comment -->'
PIN_951_DESIGN_SUMMARY_BANNER='> 🤖 **cenci** — design summary posted by `/cenci:design` (design handoff).'
PIN_951_DESIGN_SUMMARY_MARKER='<!-- cenci-design-summary -->'
PIN_951_PARENT_GAP_BANNER='> 🤖 **cenci** — parent acceptance-criteria gap report posted by `/cenci:implement` (Phase 9).'
PIN_951_PARENT_GAP_MARKER='<!-- cenci-parent-gap-report -->'
PIN_951_FOLLOWUP_TRACKED_BANNER='> 🤖 **cenci** — followup tracking note posted by `/cenci:implement` (Phase 9).'
PIN_951_FOLLOWUP_TRACKED_MARKER='<!-- cenci-followup-tracked -->'
PIN_951_REVIEW_BANNER='> 🤖 **cenci** — review report posted by `/cenci:review` (Phase 4).'
PIN_951_ADDRESS_REVIEW_BANNER='> 🤖 **cenci** — review reply posted by `/cenci:address-review` (posting replies).'

# --- 11a. Per-file banner + marker literal presence -----------------------

require_doc phase1_plan_951 "skills/implement/phases/phase-1-plan.md" || true
if [[ -n "${phase1_plan_951}" ]]; then
  assert_contains "${phase1_plan_951}" "${PIN_951_PLAN_COMMENT_BANNER}" \
    "951 flow/skills/implement/phases/phase-1-plan.md ## Persist the Plan planComment step: attribution banner missing"
  assert_contains "${phase1_plan_951}" "${PIN_951_PLAN_COMMENT_MARKER}" \
    "951 flow/skills/implement/phases/phase-1-plan.md ## Persist the Plan planComment step: cenci-plan-comment marker missing"
fi

require_doc design_skill_951 "skills/design/SKILL.md" || true
if [[ -n "${design_skill_951}" ]]; then
  assert_contains "${design_skill_951}" "${PIN_951_DESIGN_SUMMARY_BANNER}" \
    "951 flow/skills/design/SKILL.md Step 7B: attribution banner missing"
  assert_contains "${design_skill_951}" "${PIN_951_DESIGN_SUMMARY_MARKER}" \
    "951 flow/skills/design/SKILL.md Step 7B: cenci-design-summary marker missing"
fi

# phase-9-pr.md was already loaded into ${phase9} by case 9 above; reuse it
# rather than re-reading the file.
if [[ -n "${phase9}" ]]; then
  assert_contains "${phase9}" "${PIN_951_PARENT_GAP_BANNER}" \
    "951 flow/skills/implement/phases/phase-9-pr.md parent gap report: attribution banner missing"
  assert_contains "${phase9}" "${PIN_951_PARENT_GAP_MARKER}" \
    "951 flow/skills/implement/phases/phase-9-pr.md parent gap report: cenci-parent-gap-report marker missing"
  assert_contains "${phase9}" "${PIN_951_FOLLOWUP_TRACKED_BANNER}" \
    "951 flow/skills/implement/phases/phase-9-pr.md followups-tracked comment: attribution banner missing"
  assert_contains "${phase9}" "${PIN_951_FOLLOWUP_TRACKED_MARKER}" \
    "951 flow/skills/implement/phases/phase-9-pr.md followups-tracked comment: cenci-followup-tracked marker missing"
fi

require_doc review_skill_951 "skills/review/SKILL.md" || true
if [[ -n "${review_skill_951}" ]]; then
  assert_contains "${review_skill_951}" "${PIN_951_REVIEW_BANNER}" \
    "951 flow/skills/review/SKILL.md Phase 4 report file: attribution banner missing"
fi

# address-review/SKILL.md was already loaded into ${address_review} by case 2
# above; reuse it rather than re-reading the file.
if [[ -n "${address_review}" ]]; then
  assert_contains "${address_review}" "${PIN_951_ADDRESS_REVIEW_BANNER}" \
    "951 flow/skills/address-review/SKILL.md general PR comment: attribution banner missing"
fi

# --- 11b. Each of the four new `<!-- cenci-<kind> -->` markers occurs in
#     exactly one file under flow/skills/. Scoped to the four non-escalation
#     marker-bearing sites (plan-comment, design-summary, parent-gap-report,
#     followup-tracked) — planner-escalation is deliberately excluded here:
#     it legitimately appears in two files today (phase-1-plan.md, the
#     producer, and implement/SKILL.md, which restates the same cross-lane
#     detection rule verbatim per escalation-anchor-contract.test.sh's own
#     parity requirement), so a same-shaped "exactly one file" check on it
#     would never pass and is not what this ticket changes. -----------------

assert_marker_in_exactly_one_file() {
  # $1=marker-substring (fixed string) $2=label
  local marker="$1" label="$2" count
  [[ -n "${marker}" ]] || { fail "${label}: empty marker (test bug)"; return; }
  count="$(grep -rlF -- "${marker}" "${FLOW_DIR}/skills" 2>/dev/null | wc -l | tr -d ' ')"
  [[ "${count}" -eq 1 ]] || fail "${label}: expected marker '${marker}' in exactly one file under flow/skills/, found in ${count} file(s)"
}

assert_marker_in_exactly_one_file "cenci-plan-comment" \
  "951 cenci-plan-comment marker uniqueness"
assert_marker_in_exactly_one_file "cenci-design-summary" \
  "951 cenci-design-summary marker uniqueness"
assert_marker_in_exactly_one_file "cenci-parent-gap-report" \
  "951 cenci-parent-gap-report marker uniqueness"
assert_marker_in_exactly_one_file "cenci-followup-tracked" \
  "951 cenci-followup-tracked marker uniqueness"

# --- 11c. flow/docs/comment-attribution.md must exist and carry the
#     registry table, plus a bidirectional sync between the `cenci-<kind>`
#     markers actually found under flow/skills/ and the kinds registered in
#     that table. watch's four pre-existing kinds (dispatch-attempt,
#     dispatch-failed, plan-invalid, reconcile-stuck) are recorded in the doc
#     for reference but never appear under flow/skills/, so they are
#     excluded from both sides of this comparison — the pinned-literals
#     doc's own split (a separate "watch's existing kinds" list, not a row
#     in the shared table) is the precedent this exclusion follows. -------

COMMENT_ATTRIBUTION_PATH="${FLOW_DIR}/docs/comment-attribution.md"
if [[ ! -f "${COMMENT_ATTRIBUTION_PATH}" ]]; then
  fail "951 flow/docs/comment-attribution.md: file not found (expected new registry doc)"
else
  require_doc comment_attribution_951 "docs/comment-attribution.md" || true
  if [[ -n "${comment_attribution_951}" ]]; then
    assert_contains "${comment_attribution_951}" "Kind" \
      "951 flow/docs/comment-attribution.md: registry table Kind column header missing"

    # Markers actually found under flow/skills/, anchored to the HTML-comment
    # marker syntax (never a bare "cenci-" prefixed word, which would also
    # match unrelated temp-file-name literals like cenci-review-<n>-comment.md).
    FOUND_KINDS_951="$(grep -rohE '<!-- cenci-[a-z-]+' "${FLOW_DIR}/skills" 2>/dev/null | sed 's/^<!-- //' | sort -u)"
    # Kinds mentioned anywhere in the registry doc, same anchored extraction,
    # minus watch's four pre-existing kinds (never present under flow/skills/).
    REGISTRY_KINDS_951="$(grep -rohE '<!-- cenci-[a-z-]+' "${COMMENT_ATTRIBUTION_PATH}" 2>/dev/null | sed 's/^<!-- //' | sort -u | grep -vxF -e 'cenci-dispatch-attempt' -e 'cenci-dispatch-failed' -e 'cenci-plan-invalid' -e 'cenci-reconcile-stuck' || true)"

    SYNC_DIFF_951="$(diff <(echo "${FOUND_KINDS_951}") <(echo "${REGISTRY_KINDS_951}") || true)"
    if [[ -n "${SYNC_DIFF_951}" ]]; then
      fail "951 flow/docs/comment-attribution.md: registry table out of sync with markers found under flow/skills/ (diff below, < found only / > registry only):
${SYNC_DIFF_951}"
    fi
  fi
fi

# =====================================================================
# 12. Root .mcp.json — context7 pinned with exact args and no `disabled`
#     key (#813 Context7 migration contract). Each negative below is gated
#     behind the readability/object-existence check that precedes it, so a
#     missing file or a renamed/removed context7 entry cannot pass
#     vacuously.
# =====================================================================

MCP_JSON_PATH="${REPO_ROOT}/.mcp.json"
echo "case: root .mcp.json is readable"
if [[ ! -r "${MCP_JSON_PATH}" ]]; then
  fail "813 .mcp.json: file not readable: ${MCP_JSON_PATH}"
else
  CTX7_OBJECT_OK=0
  if ctx7_type="$(jq -r '.mcpServers.context7 | type' "${MCP_JSON_PATH}" 2>&1)"; then
    if [[ "${ctx7_type}" == "object" ]]; then
      CTX7_OBJECT_OK=1
    else
      fail "813 .mcp.json: .mcpServers.context7 is missing or not a JSON object (type: ${ctx7_type})"
    fi
  else
    fail "813 .mcp.json: jq error evaluating .mcpServers.context7's type: ${ctx7_type}"
  fi

  if [[ "${CTX7_OBJECT_OK}" -eq 1 ]]; then
    if ctx7_args="$(jq -c '.mcpServers.context7.args' "${MCP_JSON_PATH}" 2>&1)"; then
      [[ "${ctx7_args}" == '["-y","@upstash/context7-mcp@3.2.5"]' ]] || fail "813 .mcp.json: .mcpServers.context7.args does not equal [\"-y\", \"@upstash/context7-mcp@3.2.5\"] (found: ${ctx7_args})"
    else
      fail "813 .mcp.json: jq error evaluating .mcpServers.context7.args: ${ctx7_args}"
    fi

    if ctx7_has_disabled="$(jq -r '.mcpServers.context7 | has("disabled")' "${MCP_JSON_PATH}" 2>&1)"; then
      [[ "${ctx7_has_disabled}" == "false" ]] || fail "813 .mcp.json: .mcpServers.context7 carries an inert 'disabled' key"
    else
      fail "813 .mcp.json: jq error evaluating .mcpServers.context7's disabled key: ${ctx7_has_disabled}"
    fi
  else
    fail "813 .mcp.json: skipped args/disabled-key checks — .mcpServers.context7 object-existence check failed (see above)"
  fi
fi

# =====================================================================
# 13. Root .claude/settings.json — project-scoped Context7 permissions
#     present, zero plugin-scoped entries (#813 Context7 migration
#     contract). The plugin-scoped-entry negative is gated behind the
#     .permissions.allow array-existence check that precedes it.
# =====================================================================

CLAUDE_SETTINGS_PATH="${REPO_ROOT}/.claude/settings.json"
echo "case: root .claude/settings.json is readable"
if [[ ! -r "${CLAUDE_SETTINGS_PATH}" ]]; then
  fail "813 .claude/settings.json: file not readable: ${CLAUDE_SETTINGS_PATH}"
else
  ALLOW_ARRAY_OK=0
  if allow_len="$(jq -r '(.permissions.allow // "null") | if type == "array" then (length | tostring) else "not-array" end' "${CLAUDE_SETTINGS_PATH}" 2>&1)"; then
    if [[ "${allow_len}" =~ ^[0-9]+$ ]] && [[ "${allow_len}" -gt 0 ]]; then
      ALLOW_ARRAY_OK=1
    else
      fail "813 .claude/settings.json: .permissions.allow is not a non-empty array (found: ${allow_len})"
    fi
  else
    fail "813 .claude/settings.json: jq error evaluating .permissions.allow's length: ${allow_len}"
  fi

  if [[ "${ALLOW_ARRAY_OK}" -eq 1 ]]; then
    if allow_has_both="$(jq -r '.permissions.allow | (index("mcp__context7__resolve-library-id") != null) and (index("mcp__context7__query-docs") != null)' "${CLAUDE_SETTINGS_PATH}" 2>&1)"; then
      [[ "${allow_has_both}" == "true" ]] || fail "813 .claude/settings.json: .permissions.allow is missing one or both project-scoped Context7 permissions (mcp__context7__resolve-library-id, mcp__context7__query-docs)"
    else
      fail "813 .claude/settings.json: jq error evaluating project-scoped Context7 permission presence: ${allow_has_both}"
    fi

    if plugin_ctx7_count="$(jq -r '[.permissions.allow[] | select(startswith("mcp__plugin_cenci_context7__"))] | length' "${CLAUDE_SETTINGS_PATH}" 2>&1)"; then
      [[ "${plugin_ctx7_count}" == "0" ]] || fail "813 .claude/settings.json: .permissions.allow carries ${plugin_ctx7_count} plugin-scoped mcp__plugin_cenci_context7__* entry/entries (expected zero)"
    else
      fail "813 .claude/settings.json: jq error evaluating plugin-scoped Context7 entry count: ${plugin_ctx7_count}"
    fi
  else
    fail "813 .claude/settings.json: skipped project-scoped/plugin-scoped Context7 permission checks — .permissions.allow array-existence check failed (see above)"
  fi
fi

if [[ "${failures}" -gt 0 ]]; then
  echo "skill-convention-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "skill-convention-contract.test.sh: all checks passed."
