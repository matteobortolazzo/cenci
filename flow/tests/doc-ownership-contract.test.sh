#!/usr/bin/env bash
# Contract test for #1102: Phase 8 owns docs; bound the implementer's doc reads.
#
# The gap this pins down: the implement pipeline never states who owns doc
# updates. `implementer.md` rule 9 and `phase-4-implement-green.md`'s `## Rules`
# both told the implementer to update `README.md`/`CLAUDE.md`/`docs/**` "in the
# same change", while `phase-8-docs.md` already has `## Update CLAUDE.md` and
# `## Update README.md` sub-steps that own exactly that -- a documented double
# write, where Phase 8's `## Maintenance Check` can then flag drift Phase 4
# just created. Separately, the implementer's doc *reads* were unbounded:
# `README.md` read "wholesale" for a multi-frontend repo, topic docs picked by
# guessing a filename, and a probe for a `.claude/rules/lessons-learned*.md`
# file that does not exist in this repo. Two more defects -- a phantom
# `shell-rules` heading citation, and no `Skill` grant despite being told to
# read Skill-gated reference docs -- caused open-ended searching.
#
# This is a prose-only pipeline-instruction change (Markdown), so -- following
# the fixture-free idiom of `flow/tests/phase5-reuse-check-contract.test.sh`
# -- the only verification available is a grep-based contract test: a
# `failures=` counter, small assert_* helpers, `require_doc`/`read_doc_raw`
# failing closed on an unreadable file, and `extract_section`/`extract_line`
# to bind an assertion to the one heading/line it is actually about, so a
# match landing elsewhere in a ~400-line file can never pass vacuously.
#
# Covered files:
#   - agents/implementer.md                          (write ban + read scoping)
#   - skills/implement/SKILL.md                       (:33 planner/implementer split)
#   - skills/implement/phases/phase-3-test-red.md     (topic-doc bullet)
#   - skills/implement/phases/phase-4-implement-green.md (topic-doc bullet, Rules rewrite)
#   - skills/implement/phases/phase-5-refactor.md     (topic-doc bullet)
#   - skills/implement/phases/phase-6-7-review.md     ($RUN_DIR/docs-to-update.txt handoff)
#   - skills/implement/phases/phase-8-docs.md         (ownership + sub-step triggers)
#   - README.md                                       (phase-8 pipeline entry)
#   - repo-wide flow/ sweep for the retired phantom heading string
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "doc-ownership-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "doc-ownership-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
SELF_PATH="${SCRIPT_DIR}/doc-ownership-contract.test.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # Pure extraction, no fail() side effect -- safe to call inside $(...).
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found (a
# missing/unreadable file must never silently masquerade as empty content,
# which would make assert_not_contains trivially "pass"). Must NOT be
# invoked via $(...).
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
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# extract_section <content> <exact-heading-line> -- prints the body under that
# heading, up to the next `## ` heading. Pure, safe inside $(...). Assertions
# scoped through this cannot pass on a match that landed somewhere else in the
# file.
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    $0 == h { inside = 1; next }
    inside && /^## / { exit }
    inside { print }
  ' <<<"${content}"
}

# extract_line <content> <literal-substring> -- prints the first line containing
# the substring, for assertions that must bind to one specific bullet/line.
extract_line() {
  local content="$1" needle="$2"
  grep -F -m1 -e "${needle}" <<<"${content}"
}

# Shared marker text, required verbatim in more than one file, so both call
# sites can never silently drift apart (flow AGENTS.md's #979 restated-rule
# rule -- when a rule is stated at multiple sites, keep them identical).
CARVEOUT_SENTENCE="a doc file explicitly named in the plan's \`### Files to Modify\` / \`### Files to Create\` stays the implementer's job and is not listed under \`Docs to update:\`"
DOCS_TO_UPDATE_LINE_FORMAT='- <repo-relative path> — <what needs to change>'
GREP_THE_CONTRACT_SENTENCE='`Grep` it for the specific contract being touched'
TOPIC_DOC_BULLET='Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.'

# =====================================================================
# agents/implementer.md -- write ban + carve-out + `Docs to update:` report
# contract, plus the three bounded-read rules and the two defect fixes
# (Skill grant, phantom shell-rules heading citation).
# =====================================================================
FILE="agents/implementer.md"
if require_doc CONTENT "${FILE}"; then
  TOOLS_LINE="$(extract_line "${CONTENT}" 'tools:')"

  # Defect fix 1: `Skill` granted so the implementer can actually reach the
  # Skill-gated reference docs (`shell-rules`, `testing`, `verify-ui`, ...) it
  # is told to read, inserted before the context7 pair so
  # allowed-tools-sweep.test.sh's CONTEXT7_ENUMERATED marker stays intact.
  assert_contains "${TOOLS_LINE}" 'Bash, Skill, mcp__context7__resolve-library-id, mcp__context7__query-docs' "${FILE} (tools: frontmatter)"
  assert_contains "${TOOLS_LINE}" 'mcp__context7__resolve-library-id, mcp__context7__query-docs' "${FILE} (tools: frontmatter, context7 preserved)"

  # Write side: old rule 9's literal text is gone -- the implementer no
  # longer edits documentation incidentally.
  assert_not_contains "${CONTENT}" 'update the relevant doc' "${FILE}"

  # Its replacement states the plan-named-file carve-out verbatim -- without
  # it, a plan whose only Files to Modify is a doc file becomes
  # unimplementable (Phase 8 defaults to skip and has no plan-reading step).
  assert_contains "${CONTENT}" "${CARVEOUT_SENTENCE}" "${FILE}"

  # The `Docs to update:` reporting contract: the heading, the exact per-doc
  # line format, and the literal `None.` fallback so Phase 6 + 7 can tell
  # "nothing to do" apart from "the section was lost to a truncated report".
  assert_contains "${CONTENT}" 'Docs to update:' "${FILE}"
  assert_contains "${CONTENT}" "${DOCS_TO_UPDATE_LINE_FORMAT}" "${FILE}"
  assert_contains "${CONTENT}" 'the literal `None.`' "${FILE}"

  # Read side, rule 2: never read README.md wholesale -- Grep it for the
  # specific contract being touched.
  assert_not_contains "${CONTENT}" '`README.md` user-visible contracts' "${FILE}"
  assert_contains "${CONTENT}" "${GREP_THE_CONTRACT_SENTENCE}" "${FILE}"

  # Read side, rule 3: no more guessing topic docs by filename.
  assert_not_contains "${CONTENT}" 'pick by topic name' "${FILE}"

  # Read side, rule 4: the legacy lessons-learned probe is deleted outright
  # (it probes for a file that does not exist in this repo).
  assert_not_contains "${CONTENT}" '.claude/rules/lessons-learned' "${FILE}"

  # Defect fix 2: cite the real shell-rules heading. "Worktree & Command
  # Patterns" does not exist anywhere in shell-rules/SKILL.md; the real
  # heading is "Worktrees and Cross-Directory Writes".
  assert_contains "${CONTENT}" 'Worktrees and Cross-Directory Writes' "${FILE}"
fi

# =====================================================================
# skills/implement/SKILL.md -- :33's "Point subagents at context, don't
# paste it" bullet is split into a planner half (unchanged: still names
# `docs/<topic>.md` and both legacy lessons-learned filenames) and an
# implementer half (points at the Delegation Context's capped list, no
# legacy probe). Both halves live in the same bullet, so a whole-line
# assert_not_contains for "lessons-learned" would be vacuous -- it must
# still be true of the planner half. Split the line on the anchor phrase
# marking where the implementer-specific instruction begins, and assert
# each half separately.
# =====================================================================
FILE="skills/implement/SKILL.md"
IMPLEMENTER_HALF_ANCHOR='When delegating to the implementer,'
if require_doc CONTENT "${FILE}"; then
  BULLET_LINE="$(extract_line "${CONTENT}" "Point subagents at context, don't paste it")"

  assert_contains "${BULLET_LINE}" "${IMPLEMENTER_HALF_ANCHOR}" "${FILE} (:33 planner/implementer split)"

  PLANNER_HALF="${BULLET_LINE%%"${IMPLEMENTER_HALF_ANCHOR}"*}"
  IMPLEMENTER_HALF="${BULLET_LINE#*"${IMPLEMENTER_HALF_ANCHOR}"}"

  # Planner half: unchanged -- still names docs/<topic>.md and the legacy
  # lessons-learned files (the planner keeps reading broadly; out of scope
  # for this ticket).
  assert_contains "${PLANNER_HALF}" 'docs/<topic>.md' "${FILE} (:33 planner half)"
  assert_contains "${PLANNER_HALF}" '.claude/rules/lessons-learned' "${FILE} (:33 planner half)"

  # Implementer half: points at the Delegation Context's capped list, no
  # legacy probe.
  assert_contains "${IMPLEMENTER_HALF}" 'at most 3' "${FILE} (:33 implementer half)"
  assert_not_contains "${IMPLEMENTER_HALF}" 'lessons-learned' "${FILE} (:33 implementer half)"
fi

# =====================================================================
# phase-3-test-red.md / phase-4-implement-green.md / phase-5-refactor.md --
# each carries the identical resolved-topic-doc-list bullet, capped at 3,
# in its own Delegation Context (phase-3/4) or Process (phase-5) section --
# scoped so a match elsewhere in the file cannot pass vacuously.
# =====================================================================
FILE="skills/implement/phases/phase-3-test-red.md"
if require_doc CONTENT "${FILE}"; then
  DELEGATION_SECTION="$(extract_section "${CONTENT}" '## Delegation Context')"
  assert_contains "${DELEGATION_SECTION}" "${TOPIC_DOC_BULLET}" "${FILE} (## Delegation Context)"
fi

FILE="skills/implement/phases/phase-4-implement-green.md"
if require_doc CONTENT "${FILE}"; then
  DELEGATION_SECTION="$(extract_section "${CONTENT}" '## Delegation Context')"
  assert_contains "${DELEGATION_SECTION}" "${TOPIC_DOC_BULLET}" "${FILE} (## Delegation Context)"

  RULES_SECTION="$(extract_section "${CONTENT}" '## Rules')"

  # The doc-update clause is dropped from ## Rules -- Phase 8 owns doc
  # writes now, not Phase 4.
  assert_not_contains "${RULES_SECTION}" 'update docs if behavior, setup, configuration, or user-visible contracts change' "${FILE} (## Rules)"

  # Instead, ## Rules instructs the implementer to emit the report Phase 6 +
  # 7 will persist to $RUN_DIR/docs-to-update.txt.
  assert_contains "${RULES_SECTION}" 'emit the `Docs to update:` section in its report' "${FILE} (## Rules)"
fi

FILE="skills/implement/phases/phase-5-refactor.md"
if require_doc CONTENT "${FILE}"; then
  PROCESS_SECTION="$(extract_section "${CONTENT}" '## Process')"
  assert_contains "${PROCESS_SECTION}" "${TOPIC_DOC_BULLET}" "${FILE} (## Process)"
fi

# =====================================================================
# phase-6-7-review.md -- `## Shared Context` persists the implementer's
# `Docs to update:` lines to $RUN_DIR/docs-to-update.txt via the client's
# `Write` tool (never a shell `printf`/`>>` interpolating the implementer's
# free-text report, which can break out of a double-quoted shell argument),
# exactly mirroring the adjacent reuse-notes.txt paragraph in cadence:
# written once on first entry (never re-appended by a fix-and-rerun cycle),
# absent file means "none". The generic once-on-first-entry clause already
# appears verbatim for reuse-notes.txt, so a bare match on that clause would
# pass vacuously without a real edit -- assert phrasing and a
# Phase-8-specific "absent means none" sentence that cannot already be true
# of the reuse-notes paragraph (which names Phase 9, not Phase 8, as its
# consumer).
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" 'create `$RUN_DIR/docs-to-update.txt` directly with the reported lines' "${FILE}"
  assert_contains "${CONTENT}" 'never re-appended by a fix-and-rerun cycle' "${FILE}"
  assert_contains "${CONTENT}" 'Phase 8 treats an absent file as "none"' "${FILE}"

  # The persistence-confirmation clause must never be a silent no-op on
  # failure: it must ban shell interpolation of the free-text report (the
  # original defect) and, on a confirmation failure, retry once then report
  # a distinct error for Phase 9 instead of silently continuing.
  assert_contains "${CONTENT}" 'never a shell `printf`' "${FILE} (never-shell-printf ban)"
  assert_contains "${CONTENT}" 'report `docs: error (docs-to-update.txt persistence failed)`' "${FILE} (persistence-failure error branch)"
fi

# =====================================================================
# phase-8-docs.md -- explicit ownership statement (with the carve-out named
# as the sole exception), three doc sub-steps each with a stated trigger
# keyed to $RUN_DIR/docs-to-update.txt (overriding the file's `Default
# action: skip`), the AGENTS.md-preferred/CLAUDE.md-legacy routing rule, and
# the Maintenance Check recompute-on-doc-write rule.
# =====================================================================
FILE="skills/implement/phases/phase-8-docs.md"
if require_doc CONTENT "${FILE}"; then
  # Ownership statement: Phase 8 is the sole owner of these three doc
  # classes for an implement run, with the plan-named-file carve-out as the
  # one exception.
  assert_contains "${CONTENT}" 'the owner of `README.md`, `AGENTS.md`/`CLAUDE.md`, and `docs/**` updates for an implement run' "${FILE}"
  assert_contains "${CONTENT}" 'plan-named-file carve-out' "${FILE}"

  # New third sub-step for docs/** -- without it the ban would orphan
  # docs/** (today only lessons-collector writes it, and only when a lesson
  # fires).
  assert_contains "${CONTENT}" '## Update Topic Docs' "${FILE}"

  # Each of the three doc sub-steps gets an explicit trigger keyed to
  # $RUN_DIR/docs-to-update.txt, overriding the file's `Default action:
  # skip` at the top -- otherwise the sub-step never runs.
  for HEADING in '## Update CLAUDE.md' '## Update README.md' '## Update Topic Docs'; do
    SUBSTEP_SECTION="$(extract_section "${CONTENT}" "${HEADING}")"
    assert_contains "${SUBSTEP_SECTION}" 'Trigger:' "${FILE} (${HEADING})"
    assert_contains "${SUBSTEP_SECTION}" '$RUN_DIR/docs-to-update.txt' "${FILE} (${HEADING})"
  done

  # ## Update CLAUDE.md specifically: an AGENTS.md entry routes here and is
  # written to AGENTS.md when that file exists, CLAUDE.md only as the
  # legacy fallback.
  CLAUDE_SECTION="$(extract_section "${CONTENT}" '## Update CLAUDE.md')"
  assert_contains "${CLAUDE_SECTION}" 'written to `AGENTS.md` when that file exists, `CLAUDE.md` only as the legacy fallback' "${FILE} (## Update CLAUDE.md)"

  # ## Update Topic Docs specifically: the operative shape-guard rule (never
  # write outside docs/**) that bounds a free-text path from the
  # implementer's report -- assert the operative "accept it only if..." rule
  # itself, not the surrounding prose commentary about why it's needed
  # (that clause is reworded independently and is not the contract).
  TOPIC_DOCS_SECTION="$(extract_section "${CONTENT}" '## Update Topic Docs')"
  assert_contains "${TOPIC_DOCS_SECTION}" 'matches `docs/*.md` or `<project-path>/docs/*.md`' "${FILE} (## Update Topic Docs)"

  # ## Maintenance Check's trigger guard recomputes the changed-file list
  # (via its already-documented git -C <worktree-path> diff --name-only
  # origin/main fallback) when any doc sub-step in this phase wrote a file
  # -- otherwise Phase 8's own doc writes land after $RUN_DIR/files.txt was
  # computed and are invisible to the checker.
  MAINTENANCE_SECTION="$(extract_section "${CONTENT}" '## Maintenance Check')"
  assert_contains "${MAINTENANCE_SECTION}" 'when any doc sub-step in this phase wrote a file' "${FILE} (## Maintenance Check)"
  assert_contains "${MAINTENANCE_SECTION}" 'git -C <worktree-path> diff --name-only origin/main' "${FILE} (## Maintenance Check)"
fi

# =====================================================================
# README.md -- the Implementation Pipeline's phase-8 entry names doc
# ownership alongside lesson capture. Scoped to the phase-8 list item
# specifically -- a whole-file match on "doc ownership" would be vacuous
# this early since the phrase could land anywhere.
# =====================================================================
FILE="README.md"
if require_doc CONTENT "${FILE}"; then
  PHASE8_LINE="$(extract_line "${CONTENT}" '8. **Capture Lessons**')"
  assert_contains "${PHASE8_LINE}" 'doc ownership' "${FILE} (phase-8 pipeline entry)"
fi

# =====================================================================
# Repo-wide sweep -- no file under flow/ may retain the phantom heading
# string "Worktree & Command Patterns" (implementer.md was its only, and
# therefore unfindable, occurrence). Count occurrences with `grep -oF | wc
# -l`, never `grep -cF` (a `-cF` count would silently undercount two
# occurrences landing on the same source line). This test's own file
# necessarily names the string above to describe it, so it must be excluded
# by exact path -- a bare comment would not be enough.
# =====================================================================
WORKTREE_PATTERNS_MARKER='Worktree & Command Patterns'

# Pure extractor -- prints a non-negative occurrence count, or the literal
# string "ERROR" if the recursive scan itself failed (distinct from a
# genuine zero count). Never calls fail() -- safe to invoke inside $(...).
sweep_worktree_patterns_count() {
  local raw rc filtered total=0 f count
  raw="$(grep -rlF -- "${WORKTREE_PATTERNS_MARKER}" "${FLOW_DIR}")"
  rc=$?
  if [[ ${rc} -eq 2 ]]; then
    echo "ERROR"
    return
  fi
  # rc 0 (matches found) and rc 1 (no matches) are both a completed scan.
  filtered="$(printf '%s\n' "${raw}" | grep -vF -- "${SELF_PATH}")"
  if [[ -z "${filtered}" ]]; then
    echo 0
    return
  fi
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    count="$(grep -oF -- "${WORKTREE_PATTERNS_MARKER}" "${f}" | wc -l | tr -d '[:space:]')"
    total=$((total + count))
  done <<<"${filtered}"
  echo "${total}"
}

SWEEP_COUNT="$(sweep_worktree_patterns_count)"
if [[ "${SWEEP_COUNT}" == "ERROR" ]]; then
  fail "repo-wide sweep: grep scan error while searching flow/ for '${WORKTREE_PATTERNS_MARKER}'"
elif [[ "${SWEEP_COUNT}" -ne 0 ]]; then
  fail "repo-wide sweep: flow/ still contains the phantom heading '${WORKTREE_PATTERNS_MARKER}' (${SWEEP_COUNT} occurrence(s) outside this test's own file)"
fi

echo "doc-ownership-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
