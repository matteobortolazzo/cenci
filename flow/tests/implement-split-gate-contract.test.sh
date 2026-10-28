#!/usr/bin/env bash
# Tests documentation-contract coverage for ticket #871: the deterministic
# **Split Gate** added to `flow/skills/implement/phases/phase-1-plan.md`'s
# `## Route Planner Output` (fires on a non-empty, non-"None" planner
# `### Split Recommendation` **or** a `### Size Estimate` of `L`; routes to
# an interactive/ticketless `AskUserQuestion` stop/proceed choice or, in
# lean ticket mode, to the existing `## Unattended Escalation Path`), the
# `## Lean Approval Path` third deterministic disqualifier, the
# `## Pipeline: Plan Stage` second named exception, the Resume-mode note's
# Split-Gate exemption, and the **single-deliverable invariant** (one plan
# file, one branch, one PR per run) restated at each write point --
# `skills/implement/SKILL.md`'s `## Pipeline`, `phase-1-plan.md`'s
# `## Persist the Plan`, `phase-9-pr.md`'s `## PR`, and condensed in
# `codex.md`.
#
# Fixture-free, grep-based idiom of flow/tests/lean-planning-contract.test.sh
# and flow/tests/unattended-escalation-contract.test.sh: `set -uo pipefail`,
# a `failures` counter, `assert_file_contains`/`assert_file_lacks` helpers
# built on `grep -qF`, markers kept on a single source line (per
# docs/shell-scripting-gotchas.md), auto-discovered by scripts/run-checks.sh's
# `*.test.sh` glob -- no registration needed. No `read_*`-named helpers, so
# this file is trivially compliant with
# flow/tests/read-helper-purity-contract.test.sh's repo-wide scan (helpers
# below are named `load_*`/`extract_*`/`require_*`/`first_*` instead).
#
# Section extraction is fence-aware (a toggled `` ``` `` flag), mirroring
# flow/tests/plan-persist-sections-contract.test.sh's `extract_persist_section`
# -- both `## Persist the Plan` (the YAML front-matter template) and `## PR`
# (the PR-body markdown template) contain literal `## <heading>`-shaped lines
# inside fenced code blocks that must never be mistaken for a real section
# boundary (docs/shell-scripting-gotchas.md's fenced-code-block rule).
#
# Marker precision (docs/shell-scripting-gotchas.md rule 3): several markers
# below intentionally reuse a phrase that already exists once elsewhere in the
# target file, then assert an *increased* occurrence count (>= 2) rather than
# bare presence -- a bare-presence assertion on `can only disqualify, never
# promote` (already used once by the Open Questions disqualifier) or on `the
# client's available user-input mechanism` (already used once in codex.md's
# maintenance-finding routing sentence) would vacuously pass today, before any
# Split Gate text exists at all.
#
# Covered files:
#   - flow/skills/implement/phases/phase-1-plan.md (## Pipeline: Plan Stage's
#     second exception; ## Route Planner Output's new ### Split Gate --
#     trigger wording, both AskUserQuestion option labels + ordering, Stop
#     branch, lean-ticket routing; the Resume-mode note's exemption sentence;
#     ## Lean Approval Path's third disqualifier; ## Persist the Plan's
#     restated invariant)
#   - flow/skills/implement/SKILL.md (## Pipeline's single-deliverable
#     invariant, mid-run scope-overflow rule, fourth named session shape)
#   - flow/skills/implement/phases/phase-9-pr.md (## PR's restated invariant)
#   - flow/skills/implement/codex.md (condensed gate + invariant, portable
#     "client's available user-input mechanism" wording, never AskUserQuestion)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-split-gate-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-split-gate-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
PHASE1_PLAN="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
IMPLEMENT_SKILL="${FLOW_DIR}/skills/implement/SKILL.md"
PHASE9_PR="${FLOW_DIR}/skills/implement/phases/phase-9-pr.md"
CODEX_MD="${FLOW_DIR}/skills/implement/codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

assert_file_contains() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" || fail "$(basename "$1") $3 (expected to contain: $2)"
}
assert_file_lacks() {
  # $1=file $2=needle $3=description
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  grep -qF -- "$2" "$1" && fail "$(basename "$1") $3 (expected to NOT contain: $2)"
  return 0
}
assert_file_occurs_at_most_once() {
  # $1=file $2=needle $3=description -- protects a marker string owned
  # elsewhere (plan-persist-sections-contract.test.sh's assert_occurs_once,
  # pinned to exactly one file-wide occurrence in ## Persist the Plan) from
  # being duplicated by this ticket's own Stop-branch prose.
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$3: file not found: $1"; return; }
  local count
  count="$(grep -cF -- "$2" "$1")"
  [[ "${count}" -le 1 ]] || fail "$(basename "$1") $3 (expected at most one occurrence, found ${count}: $2)"
}
assert_file_occurs_at_least() {
  # $1=file $2=needle $3=min-count $4=description -- the complementary check
  # to assert_file_occurs_at_most_once: proves a *new* occurrence was added
  # beyond a pre-existing one, for markers this ticket's text legitimately
  # reuses (see the file header's marker-precision note above).
  [[ -n "$2" ]] || { fail "$4: empty needle"; return; }
  [[ -f "$1" ]] || { fail "$4: file not found: $1"; return; }
  local count
  count="$(grep -cF -- "$2" "$1")"
  [[ "${count}" -ge "$3" ]] || fail "$(basename "$1") $4 (expected at least $3 occurrences, found ${count}: $2)"
}

# load_content <file> -- pure read, no fail() side effect: safe to call
# inside $(...). Returns empty (and a non-zero exit, per the unchecked-cat
# gotcha) on a missing/unreadable file.
load_content() {
  cat "$1" 2>/dev/null
}

# require_content <result-var> <file> <label> -- nameref wrapper; fails
# closed with a distinct message and assigns "" on a read failure. Must NOT
# be invoked via $(...).
require_content() {
  local -n _res="$1"
  local _c
  if ! _c="$(load_content "$2")"; then
    fail "$3: could not read file: $2"
    _res=""
    return 1
  fi
  _res="${_c}"
}

# extract_section <content> <exact-heading-line> -- pure, fence-aware
# extractor: returns the body of the named "## <heading>" section through
# the next real (unfenced) "## "-level heading, or EOF. A line that merely
# *looks like* a "## " heading while inside a fenced (```) code block (e.g.
# ## Persist the Plan's YAML front-matter template, or ## PR's PR-body
# markdown template) is never treated as a section boundary. Safe inside
# $(...): no fail() side effect.
extract_section() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# require_section <result-var> <content> <heading-line> <label> -- nameref
# wrapper: assigns the extracted section body, or fails closed and assigns
# "" on a missing section (never masquerading as an empty-but-present one).
# Must NOT be invoked via $(...).
require_section() {
  local -n _res="$1"
  local _body
  _body="$(extract_section "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _res=""
    return 1
  fi
  _res="${_body}"
}

assert_section_contains() {
  # $1=section-body $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" == *"$2"* ]] || fail "$3 (expected section to contain: $2)"
}
assert_section_lacks() {
  # $1=section-body $2=needle $3=label
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3 (expected section to NOT contain: $2)"
}

# first_match_line_in_file <file> <needle> -- pure: prints the 1-based line
# number of the needle's first literal match in the file, or nothing if
# absent. Safe inside $(...): no fail() side effect.
first_match_line_in_file() {
  grep -nF -m1 -- "$2" "$1" 2>/dev/null | cut -d: -f1
}

# assert_marker_precedes <file> <needle-before> <needle-after> <label> --
# ordering assertion via a local grep -n first-match offset helper (per the
# plan's Test Strategy: Recommended-first is an ordering property, not a
# presence property). Calls fail() directly in the parent shell -- must NOT
# be invoked via $(...).
assert_marker_precedes() {
  local file="$1" before="$2" after="$3" label="$4"
  local line_before line_after
  line_before="$(first_match_line_in_file "${file}" "${before}")"
  line_after="$(first_match_line_in_file "${file}" "${after}")"
  if [[ -z "${line_before}" || -z "${line_after}" ]]; then
    fail "${label}: could not locate both markers to compare ordering (before='${before}' line='${line_before:-<missing>}'; after='${after}' line='${line_after:-<missing>}')"
    return
  fi
  if [[ "${line_before}" -ge "${line_after}" ]]; then
    fail "${label}: expected '${before}' (line ${line_before}) to precede '${after}' (line ${line_after})"
  fi
}

# =====================================================================
# flow/skills/implement/phases/phase-1-plan.md
# =====================================================================

require_content PHASE1_CONTENT "${PHASE1_PLAN}" "phase-1-plan.md"

# --- ## Pipeline: Plan Stage -- Split Gate Stop branch named as the second
#     exception to "every branch calls cenci pipeline plan <id>" -----------

if require_section PLAN_STAGE_SECTION "${PHASE1_CONTENT}" "## Pipeline: Plan Stage" "phase-1-plan.md"; then
  assert_section_contains "${PLAN_STAGE_SECTION}" \
    "a second named exception: the Split Gate Stop branch records nothing at all" \
    "phase-1-plan.md (## Pipeline: Plan Stage) must name the Split Gate Stop branch as the second exception to the every-branch-calls-plan rule"
fi

# --- ## Route Planner Output -- new ### Split Gate subsection -------------

TRIGGER_MARKER='fires when the planner output contains a non-empty, non-"None" `### Split Recommendation` **or** a `### Size Estimate` of `L`'
STOP_OPTION_LABEL='**"Refine now — /cenci:refine `<id>` in this session (Recommended)"**'
PROCEED_OPTION_LABEL='**"Proceed as a single PR anyway"**'
STOP_PERSIST_NOTHING_MARKER='persist nothing, no `cenci pipeline` call of any kind'
STOP_RETAIN_MARKER='leaves `Working` and the assignee claim in place'
LEAN_ROUTE_MARKER='route to `## Unattended Escalation Path` with the synthesized split question'
PIN_AWAIT_INPUT_CMD='cenci pipeline await-input <id>'

# --- #1093 (PR 3/3) -- split-child-aware Stop option, parent-comment
#     feedback write, and the new cenci-oversize-child marker -------------
CHILD_STOP_OPTION_LABEL='**"Refine now — parent #`<parentId>` via /cenci:refine `<parentId>` in this session (Recommended)"**'
NEVER_CHILD_REFINE_MARKER='Never tell the user to run `/cenci:refine <id>` against the child here'
FEEDBACK_HEADING_MARKER='#### Feedback to the parent (split-child Stop branch and lean-ticket-mode escalation branch, new write)'
FEEDBACK_TWO_CALL_SITES_MARKER='**Two call sites**: the interactive/ticketless split-child Stop branch above, and the lean-ticket-mode branch below'
FEEDBACK_ONE_NEW_WRITE_MARKER='this is the one new write this ticket adds anywhere in the Split Gate, and it never runs for a non-child ticket'
OVERSIZE_CHILD_MARKER='<!-- cenci-oversize-child -->'
OVERSIZE_CHILD_BANNER_MARKER='oversize split-child evidence posted by `/cenci:implement` (planning — Split Gate)'
FEEDBACK_VERIFY_MARKER='Verify by the created comment'\''s own identity — never by scanning the parent'\''s thread.'
FEEDBACK_NONBLOCKING_MARKER='this write is best-effort feedback, not authorization-gating, so a failure here must never prevent the child'
FEEDBACK_IDEMPOTENCY_HEADING_MARKER='Idempotency: no dedup, by design.'
FEEDBACK_IDEMPOTENCY_MARKER='posts a new comment on the parent each time'
LEAN_CHILD_QUESTION_MARKER='This is a split child of #`<parentId>`; the plan still sizes L / still recommends a split — re-partition parent #`<parentId>` via `/cenci:refine <parentId>`, or proceed as a single PR anyway?'
LEAN_UNCONDITIONAL_WRITE_MARKER='run the **Feedback to the parent** write above unconditionally, immediately before this branch routes to `## Unattended Escalation Path`'

# code-review fixes #2/#3/#6 (opus, PR #1093):
#   #2 -- secrecy-rule precedence (#826) restated on this new posting site
#   #3 -- identity-based verification (never a bare thread-wide marker grep)
#   #6 -- defined comment body for every Split Gate trigger case
SECRECY_HEADING_MARKER='**Secrecy rule (restated from `## Escalation Anchor`, #826) — takes precedence over the verbatim-posting requirement above.**'
SECRECY_NEVER_QUOTE_MARKER='never quote file contents, environment or configuration values, credentials, tokens, secrets, or raw command output in it'
SECRECY_DROP_MARKER='drop the offending content from what gets posted — keep the section heading and the rest of the safe text'
IDENTITY_POST_CMD_MARKER='gh api repos/<owner>/<repo>/issues/<parentId>/comments -F body=@"${TMPDIR:-/tmp}/cenci/cenci-oversize-child-<id>-<session-uuid>.md" --jq .id'
IDENTITY_READBACK_CMD_MARKER='gh api repos/<owner>/<repo>/issues/comments/<id> --jq '\''{id, body}'\'''
IDENTITY_NEVER_SCAN_MARKER='so a bare marker grep across the *whole* thread would report success on any pre-existing match even when this specific post just failed'
UNDEFINED_CONTENT_HEADING_MARKER='**Every trigger case gets a defined body — never an empty or placeholder-laden post.**'
UNDEFINED_CONTENT_SUBSTITUTE_MARKER='substitute a one-line statement of the L size estimate for that section'\''s content'
UNDEFINED_CONTENT_AMBIGUOUS_MARKER='state that plainly in the `### Size Estimate` section instead of leaving it empty'

if require_section ROUTE_SECTION "${PHASE1_CONTENT}" "## Route Planner Output" "phase-1-plan.md"; then
  assert_section_contains "${ROUTE_SECTION}" "### Split Gate" \
    "phase-1-plan.md (## Route Planner Output) must add a new ### Split Gate subsection"
  assert_section_contains "${ROUTE_SECTION}" "${TRIGGER_MARKER}" \
    "phase-1-plan.md (### Split Gate) trigger wording must name both conditions (non-empty/non-\"None\" Split Recommendation OR Size Estimate of L)"
  assert_section_contains "${ROUTE_SECTION}" "${STOP_OPTION_LABEL}" \
    "phase-1-plan.md (### Split Gate) must offer refinement in the current session as the recommended option"
  assert_section_contains "${ROUTE_SECTION}" "${PROCEED_OPTION_LABEL}" \
    "phase-1-plan.md (### Split Gate) must offer the Proceed as a single PR anyway option"
  assert_section_contains "${ROUTE_SECTION}" "${STOP_PERSIST_NOTHING_MARKER}" \
    "phase-1-plan.md (### Split Gate) Stop branch must persist nothing and make no cenci pipeline call of any kind"
  assert_section_contains "${ROUTE_SECTION}" "${STOP_RETAIN_MARKER}" \
    "phase-1-plan.md (### Split Gate) Stop branch must leave Working and the assignee claim in place"
  assert_section_contains "${ROUTE_SECTION}" "${LEAN_ROUTE_MARKER}" \
    "phase-1-plan.md (### Split Gate) lean-ticket branch must route to ## Unattended Escalation Path with the synthesized split question"
  assert_section_lacks "${ROUTE_SECTION}" "${PIN_AWAIT_INPUT_CMD}" \
    "phase-1-plan.md (## Route Planner Output) must NOT inline the await-input mechanics (they belong to ## Unattended Escalation Path only)"

  RESUME_EXEMPTION_MARKER='the Split Gate does not apply on the resume-mode re-plan return'
  assert_section_contains "${ROUTE_SECTION}" "${RESUME_EXEMPTION_MARKER}" \
    "phase-1-plan.md (## Route Planner Output, Resume-mode note) must state the Split Gate does not apply on the resume-mode re-plan return"

  # --- #1093 (PR 3/3): split-child-aware Stop option (parent-directed,
  #     never child-directed), the new parent-comment feedback write, its
  #     marker, and the lean-branch parent-naming wording. ----------------
  assert_section_contains "${ROUTE_SECTION}" "${CHILD_STOP_OPTION_LABEL}" \
    "phase-1-plan.md (### Split Gate) split-child Stop option must redirect to /cenci:refine <parentId>, never the child"
  assert_section_contains "${ROUTE_SECTION}" "${NEVER_CHILD_REFINE_MARKER}" \
    "phase-1-plan.md (### Split Gate) split-child Stop branch must explicitly forbid pointing the user at /cenci:refine <id> for the child"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_HEADING_MARKER}" \
    "phase-1-plan.md (### Split Gate) must add the Feedback to the parent subsection, naming both its call sites"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_TWO_CALL_SITES_MARKER}" \
    "phase-1-plan.md (### Split Gate) must explicitly enumerate the two call sites for the parent-comment write"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_ONE_NEW_WRITE_MARKER}" \
    "phase-1-plan.md (### Split Gate) must state the parent-comment write is the one new write and never runs for a non-child ticket"
  assert_section_contains "${ROUTE_SECTION}" "${OVERSIZE_CHILD_MARKER}" \
    "phase-1-plan.md (### Split Gate) must embed the <!-- cenci-oversize-child --> marker"
  assert_section_contains "${ROUTE_SECTION}" "${OVERSIZE_CHILD_BANNER_MARKER}" \
    "phase-1-plan.md (### Split Gate) must carry the oversize split-child evidence attribution banner"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_VERIFY_MARKER}" \
    "phase-1-plan.md (### Split Gate) must verify the parent comment post by the created comment's own identity, not a thread-wide scan"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_NONBLOCKING_MARKER}" \
    "phase-1-plan.md (### Split Gate) must state the parent-comment write is best-effort/non-blocking"

  # code-review fix #2 (opus, PR #1093): the #826 secrecy rule must be
  # restated verbatim on this new posting site, with explicit precedence
  # over the verbatim-posting requirement.
  assert_section_contains "${ROUTE_SECTION}" "${SECRECY_HEADING_MARKER}" \
    "phase-1-plan.md (### Split Gate) must restate the #826 secrecy rule with explicit precedence over verbatim-posting"
  assert_section_contains "${ROUTE_SECTION}" "${SECRECY_NEVER_QUOTE_MARKER}" \
    "phase-1-plan.md (### Split Gate) secrecy rule must forbid quoting file contents, config values, credentials, tokens, secrets, or command output"
  assert_section_contains "${ROUTE_SECTION}" "${SECRECY_DROP_MARKER}" \
    "phase-1-plan.md (### Split Gate) secrecy rule must direct dropping offending content rather than posting it verbatim"

  # code-review fix #3 (opus, PR #1093): verification must be identity-based
  # (the specific created comment), never a bare marker grep over the whole
  # parent thread -- which would false-positive whenever the parent already
  # carries an earlier oversize-child comment (expected, per Idempotency).
  assert_section_contains "${ROUTE_SECTION}" "${IDENTITY_POST_CMD_MARKER}" \
    "phase-1-plan.md (### Split Gate) must post via the REST comments API returning the new comment's own numeric ID"
  assert_section_contains "${ROUTE_SECTION}" "${IDENTITY_READBACK_CMD_MARKER}" \
    "phase-1-plan.md (### Split Gate) must read back the specific created comment by its own ID, mirroring ## Escalation Anchor's pattern"
  assert_section_contains "${ROUTE_SECTION}" "${IDENTITY_NEVER_SCAN_MARKER}" \
    "phase-1-plan.md (### Split Gate) must explain why a thread-wide marker scan would false-positive on a genuinely failed post"
  assert_section_lacks "${ROUTE_SECTION}" \
    "gh issue view <parentId> --repo <owner>/<repo> --json comments --jq '.comments[].body'" \
    "phase-1-plan.md (### Split Gate) must NOT verify via a bare thread-wide comments scan (superseded by identity-based verification)"

  # code-review fix #6 (opus, PR #1093): every Split Gate trigger case
  # (non-empty Split Recommendation, L-alone, and the missing/malformed
  # Size Estimate ambiguous-fires-too case) must get a defined comment body.
  assert_section_contains "${ROUTE_SECTION}" "${UNDEFINED_CONTENT_HEADING_MARKER}" \
    "phase-1-plan.md (### Split Gate) must state every trigger case gets a defined comment body"
  assert_section_contains "${ROUTE_SECTION}" "${UNDEFINED_CONTENT_SUBSTITUTE_MARKER}" \
    "phase-1-plan.md (### Split Gate) must substitute a one-line L-size statement when no Split Recommendation text was returned"
  assert_section_contains "${ROUTE_SECTION}" "${UNDEFINED_CONTENT_AMBIGUOUS_MARKER}" \
    "phase-1-plan.md (### Split Gate) must state the missing/malformed Size Estimate case plainly instead of posting an empty section"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_IDEMPOTENCY_HEADING_MARKER}" \
    "phase-1-plan.md (### Split Gate) must explicitly document idempotency behavior for the parent-comment write"
  assert_section_contains "${ROUTE_SECTION}" "${FEEDBACK_IDEMPOTENCY_MARKER}" \
    "phase-1-plan.md (### Split Gate) must state a retried Stop reaches this write again and posts a new comment each time, by design"
  assert_section_contains "${ROUTE_SECTION}" "${LEAN_CHILD_QUESTION_MARKER}" \
    "phase-1-plan.md (### Split Gate) lean-ticket branch must name the parent in the synthesized split question for a split child"
  assert_section_contains "${ROUTE_SECTION}" "${LEAN_UNCONDITIONAL_WRITE_MARKER}" \
    "phase-1-plan.md (### Split Gate) lean-ticket branch must run the parent-comment write unconditionally before routing to ## Unattended Escalation Path"

  # Negative: the non-child Stop branch must remain write-free -- extract
  # just that branch's prose (from its own bold header to the split-child
  # Stop branch's bold header) and confirm no gh write call appears in it.
  NONCHILD_STOP_SNIPPET="$(sed -n '/\*\*Stop branch, non-child\*\*/,/\*\*Stop branch, split child\*\*/p' "${PHASE1_PLAN}")"
  if [[ -z "${NONCHILD_STOP_SNIPPET}" ]]; then
    fail "phase-1-plan.md: could not locate the non-child Stop branch snippet for the write-free negative check"
  elif [[ "${NONCHILD_STOP_SNIPPET}" == *"gh issue comment"* ]]; then
    fail "phase-1-plan.md (### Split Gate) non-child Stop branch must remain write-free (found 'gh issue comment')"
  fi

  # Both no-questions bullets must carry the "only after the ### Split Gate
  # below passes" pointer text that wires the gate into the routing order --
  # otherwise the AC's core ordering requirement has zero regression coverage.
  NO_QUESTIONS_BULLET1_SPLIT_GATE_POINTER='ask for approval — only after the `### Split Gate` below passes'
  NO_QUESTIONS_BULLET2_SPLIT_GATE_POINTER='and again only after the `### Split Gate` below passes'
  assert_section_contains "${ROUTE_SECTION}" "${NO_QUESTIONS_BULLET1_SPLIT_GATE_POINTER}" \
    "phase-1-plan.md (## Route Planner Output) the interactive/lean-approved no-questions bullet must point to the ### Split Gate below passing before persisting"
  assert_section_contains "${ROUTE_SECTION}" "${NO_QUESTIONS_BULLET2_SPLIT_GATE_POINTER}" \
    "phase-1-plan.md (## Route Planner Output) the Lean Approval Path no-questions bullet must point to the ### Split Gate below passing before routing"
fi

# Recommended-first ordering: Stop's label first-match offset must precede
# Proceed's, file-wide (both live inside ### Split Gate, but the ordering
# check itself is deliberately not section-scoped -- a file-wide first-match
# is exactly what a human/AskUserQuestion caller would encounter reading
# top-to-bottom).
assert_marker_precedes "${PHASE1_PLAN}" "${STOP_OPTION_LABEL}" "${PROCEED_OPTION_LABEL}" \
  "phase-1-plan.md Split Gate option ordering (Stop/Recommended must precede Proceed)"

# --- ## Lean Approval Path -- third deterministic disqualifier ------------

SPLIT_GATE_DISQUALIFIER_LABEL='**Split Gate.**'
CAN_ONLY_DISQUALIFY_MARKER='can only disqualify, never promote'

if require_section LEAN_APPROVAL_SECTION "${PHASE1_CONTENT}" "## Lean Approval Path" "phase-1-plan.md"; then
  assert_section_contains "${LEAN_APPROVAL_SECTION}" "${SPLIT_GATE_DISQUALIFIER_LABEL}" \
    "phase-1-plan.md (## Lean Approval Path) must add a third deterministic disqualifier bullet labeled **Split Gate.**"
fi
# File-wide differential: "can only disqualify, never promote" already
# occurs once today (the pre-existing Open Questions disqualifier) -- the
# new Split Gate disqualifier bullet must carry the same clause, so the
# file-wide count must become at least 2. A bare presence check would
# vacuously pass against today's unmodified file.
assert_file_occurs_at_least "${PHASE1_PLAN}" "${CAN_ONLY_DISQUALIFY_MARKER}" 2 \
  "must carry the can-only-disqualify-never-promote clause on a second (Split Gate) disqualifier, beyond the pre-existing Open Questions one"

# --- ## Persist the Plan -- restated single-deliverable invariant ---------

PERSIST_INVARIANT_MARKER='exactly one plan file per run; never a second `.plans/<id>-*.md` for the same ticket'

if require_section PERSIST_SECTION "${PHASE1_CONTENT}" "## Persist the Plan" "phase-1-plan.md"; then
  assert_section_contains "${PERSIST_SECTION}" "${PERSIST_INVARIANT_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) must restate the single-deliverable invariant at its write point"
fi

# =====================================================================
# flow/skills/implement/SKILL.md -- ## Pipeline: single-deliverable
# invariant, mid-run scope-overflow rule, fourth named session shape.
# =====================================================================

require_content SKILL_CONTENT "${IMPLEMENT_SKILL}" "SKILL.md"

SKILL_INVARIANT_MARKER='one run persists at most one plan file and produces exactly one worktree branch and one PR'
SKILL_OVERFLOW_MARKER='capture overflow as Followup items'
SKILL_OVERFLOW_ERROR_GATE_MARKER='stop at an error gate recommending `/cenci:refine`'
SKILL_FOURTH_SHAPE_MARKER='4. **Split Gate stop'
# code-review fix #4 (opus, PR #1093): shape (3)'s write enumeration must
# also name the parent-evidence write -- it is not Stop-branch-only.
SKILL_SHAPE3_ADDENDUM_MARKER='**Split-child addendum**: when the Split-Gate-synthesized question is for a split child, this path also runs the same best-effort, non-blocking parent-evidence write shape (4) below describes'
SKILL_SHAPE4_TWO_SITES_MARKER='that subsection has two call sites, this Stop branch and shape (3)'\''s lean-ticket-mode escalation branch'

if require_section PIPELINE_SECTION "${SKILL_CONTENT}" "## Pipeline" "SKILL.md"; then
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_INVARIANT_MARKER}" \
    "SKILL.md (## Pipeline) must state the single-deliverable invariant"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_OVERFLOW_MARKER}" \
    "SKILL.md (## Pipeline) must state the mid-run scope-overflow rule's complete-and-capture-as-Followup outcome"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_OVERFLOW_ERROR_GATE_MARKER}" \
    "SKILL.md (## Pipeline) must state the mid-run scope-overflow rule's other disjunctive outcome (stop at an error gate recommending /cenci:refine)"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_FOURTH_SHAPE_MARKER}" \
    "SKILL.md (## Pipeline) must add a fourth named planning-session shape for the Split Gate stop"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_SHAPE3_ADDENDUM_MARKER}" \
    "SKILL.md (## Pipeline) shape (3) must name the parent-evidence write for a split-child escalation, not just shape (4)"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_SHAPE4_TWO_SITES_MARKER}" \
    "SKILL.md (## Pipeline) shape (4) must state the Feedback subsection has two call sites, not just this Stop branch"
fi

# =====================================================================
# flow/skills/implement/phases/phase-9-pr.md -- ## PR: restated invariant.
# =====================================================================

require_content PHASE9_CONTENT "${PHASE9_PR}" "phase-9-pr.md"

PR_INVARIANT_MARKER='exactly one `gh pr create` per run'
PR_NEVER_STACKED_MARKER='never a second/stacked PR for overflow'

if require_section PR_SECTION "${PHASE9_CONTENT}" "## PR" "phase-9-pr.md"; then
  assert_section_contains "${PR_SECTION}" "${PR_INVARIANT_MARKER}" \
    "phase-9-pr.md (## PR) must restate the single-deliverable invariant (exactly one gh pr create per run)"
  assert_section_contains "${PR_SECTION}" "${PR_NEVER_STACKED_MARKER}" \
    "phase-9-pr.md (## PR) must state never a second/stacked PR for overflow"
fi

# =====================================================================
# flow/skills/implement/codex.md -- condensed gate + invariant, portable
# wording, adapter parity.
# =====================================================================

CODEX_SPLIT_GATE_TERM='Split Gate'
CODEX_CLIENT_MECHANISM_MARKER="the client's available user-input mechanism"
CODEX_INVARIANT_MARKER='never persist a second plan file or open a second or stacked PR for one ticket'

assert_file_contains "${CODEX_MD}" "${CODEX_SPLIT_GATE_TERM}" \
  "must name the Split Gate in condensed form"
assert_file_contains "${CODEX_MD}" "${CODEX_INVARIANT_MARKER}" \
  "must carry the condensed single-deliverable invariant sentence"
# File-wide differential: the portable "the client's available user-input
# mechanism" phrase already occurs once today (the unrelated maintenance-
# finding routing sentence) -- the Split Gate's stop/proceed choice must
# route through the same portable mechanism (never AskUserQuestion, per
# flow/AGENTS.md), so the file-wide count must become at least 2. A bare
# presence check would vacuously pass against today's unmodified file.
assert_file_occurs_at_least "${CODEX_MD}" "${CODEX_CLIENT_MECHANISM_MARKER}" 2 \
  "must route the Split Gate's stop/proceed choice through a second use of the portable client user-input-mechanism phrasing, never AskUserQuestion"
assert_file_lacks "${CODEX_MD}" "AskUserQuestion" \
  "must never use AskUserQuestion in the cross-tool-portable codex.md (flow/AGENTS.md critical rule)"

# code-review fix #5 (opus, PR #1093): codex.md branches on isChild/parentId
# but never derived them anywhere -- it must state the derivation (mirroring
# refine/codex.md's own recipe), not just gate on an unwired value (#824).
CODEX_PROVENANCE_DERIVATION_MARKER="Split-child provenance for this gate is derived the same way \`skills/refine/codex.md\`'s"
CODEX_PROVENANCE_CMD_MARKER="--json parent --jq '.parent.number // empty'"
assert_file_contains "${CODEX_MD}" "${CODEX_PROVENANCE_DERIVATION_MARKER}" \
  "must state where isChild/parentId are derived from on this surface, mirroring refine/codex.md's provenance recipe"
assert_file_contains "${CODEX_MD}" "${CODEX_PROVENANCE_CMD_MARKER}" \
  "must name the native parent-field command used to derive isChild/parentId"

# code-review fix #4 (opus, PR #1093): codex.md's wording must name both
# call sites for the parent-evidence write, not just the Stop outcome.
CODEX_TWO_SITES_MARKER='Two call sites — this interactive/ticketless Stop outcome, and the equivalent lean-mode'
assert_file_contains "${CODEX_MD}" "${CODEX_TWO_SITES_MARKER}" \
  "must name both call sites (Stop outcome and lean-mode escalation) for the parent-evidence write"

# =====================================================================
# flow/docs/comment-attribution.md -- oversize-child registry row must name
# both call sites (code-review fix #4, opus, PR #1093), mirroring the
# planner-escalation row's "and its four call sites" pattern.
# =====================================================================

COMMENT_ATTRIBUTION_DOC="${FLOW_DIR}/docs/comment-attribution.md"
OVERSIZE_CHILD_REGISTRY_ROW_MARKER='the Split Gate'\''s split-child Stop branch and its lean-ticket-mode escalation branch (two call sites)'
assert_file_contains "${COMMENT_ATTRIBUTION_DOC}" "${OVERSIZE_CHILD_REGISTRY_ROW_MARKER}" \
  "comment-attribution.md's oversize-child registry row must name both call sites, not just the Stop branch"

# =====================================================================
# Negative: must not reuse plan-persist-sections-contract.test.sh's four
# assert_occurs_once markers (pinned to exactly one file-wide occurrence
# each, in ## Persist the Plan) -- the Stop branch's wording above is
# deliberately distinct from these, exactly as ## Unattended Escalation Path
# and ## Lean Approval Path already dodge them.
# =====================================================================

assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** post the `planComment` audit comment' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-post-planComment marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** run the `Planned` label transition' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-run-label-transition marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** record the plan artifact' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-record-artifact marker"
assert_file_occurs_at_most_once "${PHASE1_PLAN}" 'do **not** set `hasPlanFile = true`' \
  "must not duplicate plan-persist-sections-contract.test.sh's do-not-set-hasPlanFile marker"

# =====================================================================
# #1199 (Part 2) -- reasoned no-split verdicts. In lean ticket mode a
# `### Size Estimate` of `L` plus a well-formed `None — <rationale>`
# `### Split Recommendation` passes the Split Gate and the Lean Approval Path
# Split Gate disqualifier (format only, fail-closed), recorded as an
# auto-adopted Q&A entry; every other mode and every other trigger is
# unchanged. The planner's output contract is tightened to match.
#
# Acceptance-criterion map (each AC has at least one explicit assertion):
#   - planner emits real split or `None — <one-line rationale>` whenever L
#       -> PLANNER_* markers (## Plan Output)
#   - L + well-formed verdict: no escalation, continues through the Lean
#     Approval Path, Q&A records the auto-adopted proceed decision
#       -> CARVEOUT_* markers + LEAN_STEP2_* + LEAN_DISQUALIFIER_* markers
#   - real split recommendation escalates exactly as today (incl. the
#     split-child parent-feedback write)
#       -> CARVEOUT_REAL_SPLIT_MARKER + the pre-existing #1093 markers above
#   - missing/empty/bare/malformed verdict still escalates; missing/malformed
#     Size Estimate still fires
#       -> VERDICT_FAILCLOSED_* markers + SIZE_ESTIMATE_* markers
#   - no child-specific exemption
#       -> CARVEOUT_NO_CHILD_EXEMPTION_MARKER
#   - interactive / ticketless / attended / resume unchanged
#       -> UNCHANGED_MODES_MARKER + the interactive-branch negative snippet
#   - shapes 2, 3 and 4 in SKILL.md and docs/autonomous-loop.md in sync
#       -> SHAPE*_ and LOOP_DOC_* markers
#   - the old `TRIGGER_MARKER` / `LEAN_ROUTE_MARKER` /
#     `LEAN_UNCONDITIONAL_WRITE_MARKER` / `RESUME_EXEMPTION_MARKER` and
#     `CAN_ONLY_DISQUALIFY_MARKER` (>= 2) assertions above stay in force
#     unchanged as the regression guard for the additive edit.
# =====================================================================

PLANNER_AGENT="${FLOW_DIR}/agents/planner.md"
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "implement-split-gate-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
LOOP_DOC="${REPO_ROOT}/docs/autonomous-loop.md"

# normalize_ws <content> -- collapse newlines and repeated whitespace so a
# markdown-wrapped sentence matches as one substring (shell-scripting-gotchas.md
# line-wrapping pitfall). Mirrors attended-autonomy-contract.test.sh.
normalize_ws() {
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

# assert_contains_ws / assert_lacks_ws <content> <needle> <label>
assert_contains_ws() {
  local content="$1" needle="$2" label="$3"
  [[ -n "${needle}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${needle}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${needle}]"
}
assert_lacks_ws() {
  local content="$1" needle="$2" label="$3"
  [[ -n "${needle}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" != *"${needle}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${needle}]"
}

# extract_subsection <content> <exact-heading-line> -- pure, fence-aware: the
# body of a "### <heading>" subsection through the next real "## " or "### "
# heading ("#### " sub-subsections stay inside), or EOF.
extract_subsection() {
  awk -v want="$2" '
    $0 == want && !on { on=1; print; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && (/^## / || /^### /) { exit }
    on { print }
  ' <<<"$1"
}

# extract_block <content> <start-needle> -- pure: from the first line containing
# the needle through the line before the next blank line, numbered-step line
# ("N. "), or bullet line ("- "). Keeps a wrapped paragraph/step/bullet together
# and scopes a marker to the one construct it belongs to.
extract_block() {
  awk -v want="$2" '
    !on && index($0, want) { on=1; print; next }
    on && (/^[[:space:]]*$/ || /^[0-9]+\. / || /^- /) { exit }
    on { print }
  ' <<<"$1"
}

require_subsection() {
  local -n _res="$1"
  local _body
  _body="$(extract_subsection "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' subsection"
    _res=""
    return 1
  fi
  _res="${_body}"
}
require_block() {
  local -n _res="$1"
  local _body
  _body="$(extract_block "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate block starting at '$3'"
    _res=""
    return 1
  fi
  _res="${_body}"
}

# --- planner.md ## Plan Output: the Split Recommendation output contract ------

PLANNER_REQUIRED_WHEN_L_MARKER='whenever the `### Size Estimate` is `L`'
PLANNER_VERDICT_FORM_MARKER='either a real split or `None — <one-line rationale>`'
PLANNER_SM_MARKER='omit it or write `None`'
PLANNER_PATHS_ONLY_MARKER='repo-relative paths or identifiers only'
PLANNER_OLD_HEADING='### Split Recommendation (only if L — real risk'
PLANNER_WHOLE_SECTION_MARKER='`None — <one-line rationale>` must be the whole section: exactly one non-blank line'
PLANNER_FURTHER_CONTENT_MARKER='any further non-blank content after that line (for example `Ticket N:` lines or bullets) makes it no verdict and the gate fails closed'

if require_content PLANNER_CONTENT "${PLANNER_AGENT}" "planner.md" \
   && require_section PLAN_OUTPUT_SECTION "${PLANNER_CONTENT}" "## Plan Output" "planner.md"; then
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_REQUIRED_WHEN_L_MARKER}" \
    "planner.md (## Plan Output) must require ### Split Recommendation whenever the size estimate is L"
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_VERDICT_FORM_MARKER}" \
    "planner.md (## Plan Output) must require the L Split Recommendation to be a real split or None — <one-line rationale>"
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_SM_MARKER}" \
    "planner.md (## Plan Output) must say an S/M estimate omits the Split Recommendation or writes None"
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_PATHS_ONLY_MARKER}" \
    "planner.md (## Plan Output) must restrict the None-verdict rationale to repo-relative paths or identifiers only (it reaches a possibly-public ticket)"
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_WHOLE_SECTION_MARKER}" \
    "planner.md (## Plan Output) must require the None — <one-line rationale> verdict to be the whole section: exactly one non-blank line"
  assert_contains_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_FURTHER_CONTENT_MARKER}" \
    "planner.md (## Plan Output) must state any further non-blank content after the verdict line makes it no verdict and the gate fails closed"
  assert_lacks_ws "${PLAN_OUTPUT_SECTION}" "${PLANNER_OLD_HEADING}" \
    "planner.md (## Plan Output) must no longer carry the old non-deterministic '(only if L — ...)' heading text"
  # The heading itself must now be deterministic for the gate: a plain
  # `### Split Recommendation` line (the template block is indented).
  if ! grep -qE '^[[:space:]]*### Split Recommendation[[:space:]]*$' "${PLANNER_AGENT}"; then
    fail "planner.md (## Plan Output) must carry a plain '### Split Recommendation' heading line (deterministic for the Split Gate)"
  fi
fi

# --- phase-1-plan.md ### Split Gate: reasoned no-split verdict + carve-out ----

SPLIT_GATE_LABEL_MARKER='**Reasoned no-split verdict.**'
VERDICT_FORMAT_MARKER='first non-blank line is `None`, then an em dash or a spaced hyphen, then non-empty rationale text'
VERDICT_FAILCLOSED_MISSING_MARKER='a missing heading'
VERDICT_FAILCLOSED_EMPTY_MARKER='an empty section'
VERDICT_FAILCLOSED_BARE_MARKER='a bare `None`/`None.`'
VERDICT_FAILCLOSED_FREETEXT_MARKER='free text such as'
VERDICT_FORMAT_ONLY_MARKER='format only, never the rationale'"'"'s quality'
VERDICT_NONE_EXACT_MARKER='the section counts as "None" only if its entire trimmed text is exactly `None` or `None.`'
VERDICT_NONE_SINGLE_LINE_MARKER='or it is a single-line well-formed reasoned no-split verdict'
VERDICT_NONE_REAL_TEXT_MARKER='is real text and fires the gate as before'
VERDICT_NONE_PREFIX_EXAMPLES_MARKER='`Nonetheless…`, `None of these fit… split into…`, or `None.` followed by split bullets'
VERDICT_NONE_PREFIX_PHRASE='text starts with `None`'
VERDICT_ONE_LINE_MARKER='is exactly one non-blank line'
VERDICT_FURTHER_CONTENT_MARKER='any further non-blank content after that line (for example `Ticket N:` lines or bullets) is no verdict and fails closed'
UNCHANGED_MODES_MARKER='unchanged in interactive mode, ticketless mode, attended sessions, and on the resume-mode re-plan return'
SIZE_ESTIMATE_FIRES_MARKER='a missing or malformed estimate is never read as "doesn'"'"'t trigger."'
SIZE_ESTIMATE_CARVEOUT_MARKER='never applies to a missing or malformed `### Size Estimate`'

CARVEOUT_LABEL_MARKER='**Reasoned-proceed carve-out'
CARVEOUT_LEAN_ONLY_MARKER='lean ticket mode'
CARVEOUT_CONDITION_MARKER='`### Size Estimate` of `L` plus a reasoned no-split verdict'
CARVEOUT_NO_ESCALATION_MARKER='no escalation'
CARVEOUT_NO_QUESTION_MARKER='no synthesized split question'
CARVEOUT_NO_FEEDBACK_MARKER='no Feedback-to-the-parent write'
CARVEOUT_NO_PIPELINE_MARKER='the gate itself makes no `cenci pipeline` call (the Lean Approval Path'"'"'s own calls still run)'
SECRECY_RATIONALE_MARKER='must be repo-relative paths or identifiers only'
SECRECY_PRECEDENCE_MARKER='secrecy takes precedence over verbatim recording'
CARVEOUT_CONTINUES_MARKER='continues to `## Lean Approval Path`'
CARVEOUT_QANDA_QUESTION_MARKER='Q: Split the oversized plan?'
PROCEED_ENTRY_MARKER='auto-adopted: Proceed as a single PR — <planner rationale>'
CARVEOUT_VERBATIM_MARKER='stays verbatim in the plan'
CARVEOUT_NO_CHILD_EXEMPTION_MARKER='no child-specific exemption'
CARVEOUT_REAL_SPLIT_MARKER='a real split recommendation escalates exactly as below'
CARVEOUT_OTHER_TRIGGERS_MARKER='every other trigger escalates exactly as below'

if require_subsection SPLIT_GATE_SUBSECTION "${PHASE1_CONTENT}" "### Split Gate" "phase-1-plan.md"; then
  assert_contains_ws "${SPLIT_GATE_SUBSECTION}" "${SPLIT_GATE_LABEL_MARKER}" \
    "phase-1-plan.md (### Split Gate) must define the reasoned no-split verdict once, in the gate"
  # Scope every verdict marker to the one **Reasoned no-split verdict.**
  # paragraph (not the whole gate): 'a missing heading' etc. recur elsewhere.
  if require_block VERDICT_BLOCK "${SPLIT_GATE_SUBSECTION}" "${SPLIT_GATE_LABEL_MARKER}" "phase-1-plan.md (### Split Gate)"; then
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FORMAT_MARKER}" \
      "phase-1-plan.md (### Split Gate) must define the verdict format: None, then an em dash or spaced hyphen, then non-empty rationale text"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_ONE_LINE_MARKER}" \
      "phase-1-plan.md (### Split Gate) the verdict must be exactly one non-blank line"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FURTHER_CONTENT_MARKER}" \
      "phase-1-plan.md (### Split Gate) any further non-blank content after the verdict line must fail closed"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FAILCLOSED_MISSING_MARKER}" \
      "phase-1-plan.md (### Split Gate) must treat a missing Split Recommendation heading as no verdict (fail closed)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FAILCLOSED_EMPTY_MARKER}" \
      "phase-1-plan.md (### Split Gate) must treat an empty Split Recommendation section as no verdict (fail closed)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FAILCLOSED_BARE_MARKER}" \
      "phase-1-plan.md (### Split Gate) must treat a bare None/None. as no verdict (fail closed)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FAILCLOSED_FREETEXT_MARKER}" \
      "phase-1-plan.md (### Split Gate) must treat free text (e.g. 'No split recommended...') as no verdict (fail closed)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_FORMAT_ONLY_MARKER}" \
      "phase-1-plan.md (### Split Gate) must state the gate checks the format only, never the rationale's quality"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_NONE_EXACT_MARKER}" \
      "phase-1-plan.md (### Split Gate) must define \"None\" for the split-text condition as exactly None or None. (no prefix match)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_NONE_SINGLE_LINE_MARKER}" \
      "phase-1-plan.md (### Split Gate) must also count a single-line well-formed reasoned no-split verdict as \"None\""
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_NONE_PREFIX_EXAMPLES_MARKER}" \
      "phase-1-plan.md (### Split Gate) must name the None-prefixed counter-examples (Nonetheless, None of these fit, None. followed by bullets)"
    assert_contains_ws "${VERDICT_BLOCK}" "${VERDICT_NONE_REAL_TEXT_MARKER}" \
      "phase-1-plan.md (### Split Gate) must state None-prefixed text that is not exactly None/None./a verdict is real text and fires the gate"
    assert_contains_ws "${VERDICT_BLOCK}" "${UNCHANGED_MODES_MARKER}" \
      "phase-1-plan.md (### Split Gate) must state Split Gate behavior is unchanged in interactive, ticketless, attended and resume-mode runs"
  fi
  assert_file_lacks "${PHASE1_PLAN}" "${VERDICT_NONE_PREFIX_PHRASE}" \
    "must not carry the retired fail-open prefix rule (a Split Recommendation whose text starts with None counts as None)"
  assert_contains_ws "${SPLIT_GATE_SUBSECTION}" "${SIZE_ESTIMATE_FIRES_MARKER}" \
    "#1199 regression: a missing or malformed Size Estimate must still fire the gate"
  assert_contains_ws "${SPLIT_GATE_SUBSECTION}" "${SIZE_ESTIMATE_CARVEOUT_MARKER}" \
    "phase-1-plan.md (### Split Gate) the reasoned-proceed carve-out must never apply to a missing or malformed Size Estimate"

  if require_block CARVEOUT_BLOCK "${SPLIT_GATE_SUBSECTION}" "${CARVEOUT_LABEL_MARKER}" "phase-1-plan.md (### Split Gate)"; then
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_LEAN_ONLY_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must be scoped to lean ticket mode"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_CONDITION_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must require a Size Estimate of L plus a reasoned no-split verdict"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_NO_ESCALATION_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must not escalate"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_NO_QUESTION_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must not synthesize a split question"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_NO_FEEDBACK_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must not run the Feedback-to-the-parent write"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_NO_PIPELINE_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must say the gate itself makes no cenci pipeline call while the Lean Approval Path's own calls still run"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${VERDICT_FURTHER_CONTENT_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must restate that further non-blank content after the verdict line is no verdict (fails closed)"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${SECRECY_RATIONALE_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must restate that the recorded planner rationale is repo-relative paths or identifiers only (reaches a possibly-public ticket)"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${SECRECY_PRECEDENCE_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must state secrecy takes precedence over verbatim recording"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_CONTINUES_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must continue to ## Lean Approval Path"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_QANDA_QUESTION_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must record the Split the oversized plan? question in ## Q&A from Planning"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${PROCEED_ENTRY_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must record the auto-adopted proceed decision with the planner's rationale"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_VERBATIM_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must keep the Split Recommendation verbatim in the plan"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_NO_CHILD_EXEMPTION_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must state there is no child-specific exemption"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_REAL_SPLIT_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must state a real split recommendation still escalates exactly as before"
    assert_contains_ws "${CARVEOUT_BLOCK}" "${CARVEOUT_OTHER_TRIGGERS_MARKER}" \
      "phase-1-plan.md (### Split Gate) reasoned-proceed carve-out must state every other trigger escalates exactly as before"
  fi

  # Negative: the whole interactive/ticketless Stop/Proceed region -- from its
  # bold header through the Feedback-to-the-parent subsection, i.e. everything
  # before the lean-only carve-out paragraph -- must carry no reasoned-proceed
  # carve-out and never mention the reasoned verdict. The carve-out paragraph
  # itself sits between that region and **Lean ticket mode**, so the ordering
  # assertion below proves the region really spans up to the lean branch.
  INTERACTIVE_REGION="$(awk '
    index($0, "**Interactive mode, and ticketless mode**") { on=1 }
    on && index($0, "**Reasoned-proceed carve-out") { exit }
    on { print }
  ' <<<"${SPLIT_GATE_SUBSECTION}")"
  if [[ -z "${INTERACTIVE_REGION}" ]]; then
    fail "phase-1-plan.md: could not locate the interactive/ticketless Stop region for the no-carve-out negative check"
  elif [[ "${INTERACTIVE_REGION,,}" == *"reasoned"* ]]; then
    fail "phase-1-plan.md (### Split Gate) interactive/ticketless Stop region must not mention a reasoned no-split carve-out (found 'reasoned')"
  fi
  INTERACTIVE_LINE="$(grep -nF -m1 -- '**Interactive mode, and ticketless mode**' <<<"${SPLIT_GATE_SUBSECTION}" | cut -d: -f1)"
  CARVEOUT_LINE="$(grep -nF -m1 -- "${CARVEOUT_LABEL_MARKER}" <<<"${SPLIT_GATE_SUBSECTION}" | cut -d: -f1)"
  LEAN_LINE="$(grep -nF -m1 -- '**Lean ticket mode** (' <<<"${SPLIT_GATE_SUBSECTION}" | cut -d: -f1)"
  if [[ -z "${INTERACTIVE_LINE}" || -z "${CARVEOUT_LINE}" || -z "${LEAN_LINE}" ]]; then
    fail "phase-1-plan.md (### Split Gate): could not locate the interactive / carve-out / lean branch headers to prove the negative-check region spans up to the lean branch"
  elif ! [[ "${INTERACTIVE_LINE}" -lt "${CARVEOUT_LINE}" && "${CARVEOUT_LINE}" -lt "${LEAN_LINE}" ]]; then
    fail "phase-1-plan.md (### Split Gate): expected order interactive (${INTERACTIVE_LINE}) < carve-out (${CARVEOUT_LINE}) < lean ticket mode (${LEAN_LINE})"
  fi

  # Parent-feedback staging (#1199 review fix C): a None-led well-formed verdict
  # or any malformed/free-text Split Recommendation is "no split text" and
  # gets the one-line L statement -- never posted verbatim to the parent as
  # split evidence. Only genuinely real split text is staged verbatim.
  STAGING_NO_SPLIT_TEXT_MARKER='holds no genuine split text'
  STAGING_NONE_LED_MARKER='a well-formed `None — <rationale>` verdict'
  STAGING_MALFORMED_MARKER='any malformed or free-text section such as "No split recommended…"'
  STAGING_NEVER_VERBATIM_MARKER='never post that text verbatim to the parent as split evidence'
  STAGING_ONLY_REAL_MARKER='only genuinely real split text'
  if require_block STAGING_BLOCK "${ROUTE_SECTION}" "${UNDEFINED_CONTENT_HEADING_MARKER}" "phase-1-plan.md (### Split Gate parent-feedback staging)"; then
    assert_contains_ws "${STAGING_BLOCK}" "${STAGING_NO_SPLIT_TEXT_MARKER}" \
      "phase-1-plan.md (### Split Gate parent-feedback staging) must define the no-split-text substitution condition"
    assert_contains_ws "${STAGING_BLOCK}" "${STAGING_NONE_LED_MARKER}" \
      "phase-1-plan.md (### Split Gate parent-feedback staging) must treat a well-formed None — <rationale> verdict as no split text"
    assert_contains_ws "${STAGING_BLOCK}" "${STAGING_MALFORMED_MARKER}" \
      "phase-1-plan.md (### Split Gate parent-feedback staging) must treat a malformed or free-text Split Recommendation as no split text"
    assert_contains_ws "${STAGING_BLOCK}" "${STAGING_NEVER_VERBATIM_MARKER}" \
      "phase-1-plan.md (### Split Gate parent-feedback staging) must never post no-split text verbatim to the parent as split evidence"
    assert_contains_ws "${STAGING_BLOCK}" "${STAGING_ONLY_REAL_MARKER}" \
      "phase-1-plan.md (### Split Gate parent-feedback staging) must stage only genuinely real split text verbatim"
  fi
  LEAN_BRANCH_QUESTION_MARKER='the planner'"'"'s `### Split Recommendation` when it is genuinely real split text, or otherwise a one-line statement of the `L` size estimate'
  LEAN_BRANCH_NEVER_VERBATIM_MARKER='a malformed, free-text, or `None`-led no-split section is never posted verbatim as the question'
  if require_block LEAN_BRANCH_BLOCK "${SPLIT_GATE_SUBSECTION}" "**Lean ticket mode** (" "phase-1-plan.md (### Split Gate lean ticket mode)"; then
    assert_contains_ws "${LEAN_BRANCH_BLOCK}" "${LEAN_BRANCH_QUESTION_MARKER}" \
      "phase-1-plan.md (### Split Gate lean ticket mode) the synthesized question must use the planner's text only when it is genuinely real split text, else the one-line L statement"
    assert_contains_ws "${LEAN_BRANCH_BLOCK}" "${LEAN_BRANCH_NEVER_VERBATIM_MARKER}" \
      "phase-1-plan.md (### Split Gate lean ticket mode) a malformed, free-text, or None-led no-split section must never be posted verbatim as the synthesized question"
  fi
fi

# --- phase-1-plan.md ## Lean Approval Path: disqualifier carve-out, step 2,
#     step 8 -------------------------------------------------------------------

LEAN_DISQUALIFIER_START='- **Split Gate.**'
LEAN_DISQUALIFIER_VERDICT_MARKER='reasoned no-split verdict'
LEAN_DISQUALIFIER_MISSING_MARKER='a missing heading'
LEAN_DISQUALIFIER_EMPTY_MARKER='an empty section'
LEAN_DISQUALIFIER_BARE_MARKER='a bare `None`/`None.`'
LEAN_DISQUALIFIER_FREETEXT_MARKER='free text such as'
LEAN_DISQUALIFIER_REAL_SPLIT_MARKER='a real split recommendation'
LEAN_DISQUALIFIER_SIZE_MARKER='missing or malformed `### Size Estimate`'
LEAN_STEP2_START='2. Write the plan file'
LEAN_STEP2_QUESTION_MARKER='Q: Split the oversized plan?'
LEAN_STEP2_ROUTE_MARKER='reasoned-proceed route'
LEAN_STEP8_START='8. Emit a 3-5 line status summary'
LEAN_STEP8_ROUTE_MARKER='reasoned-proceed route'
LEAN_STEP8_ESTIMATE_MARKER='names the `L` estimate'

if require_section LEAN_APPROVAL_1199 "${PHASE1_CONTENT}" "## Lean Approval Path" "phase-1-plan.md"; then
  if require_block LEAN_DISQUALIFIER_BLOCK "${LEAN_APPROVAL_1199}" "${LEAN_DISQUALIFIER_START}" "phase-1-plan.md (## Lean Approval Path)"; then
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_VERDICT_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must carve out an L estimate with a reasoned no-split verdict"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_MISSING_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate: a missing heading is no verdict (restate, don't reference)"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_EMPTY_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate: an empty section is no verdict"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_BARE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate: a bare None/None. is no verdict"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_FREETEXT_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate: free text is no verdict"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${VERDICT_NONE_EXACT_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate (not reference) the exact-None/None. definition of \"None\", with no prefix match"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${VERDICT_NONE_SINGLE_LINE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate that a single-line well-formed verdict also counts as \"None\""
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${VERDICT_NONE_PREFIX_EXAMPLES_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate the None-prefixed counter-examples that still disqualify"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${VERDICT_ONE_LINE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate that the verdict is exactly one non-blank line"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${VERDICT_FURTHER_CONTENT_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must restate that further non-blank content after the verdict line is no verdict (fails closed)"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_REAL_SPLIT_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must keep disqualifying on a real split recommendation"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${LEAN_DISQUALIFIER_SIZE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must keep disqualifying on a missing or malformed Size Estimate"
    assert_contains_ws "${LEAN_DISQUALIFIER_BLOCK}" "${CAN_ONLY_DISQUALIFY_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path) Split Gate disqualifier must keep the can-only-disqualify-never-promote clause"
  fi
  if require_block LEAN_STEP2_BLOCK "${LEAN_APPROVAL_1199}" "${LEAN_STEP2_START}" "phase-1-plan.md (## Lean Approval Path step 2)"; then
    assert_contains_ws "${LEAN_STEP2_BLOCK}" "${LEAN_STEP2_QUESTION_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 2) must record the Split the oversized plan? question for the reasoned-proceed route"
    assert_contains_ws "${LEAN_STEP2_BLOCK}" "${PROCEED_ENTRY_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 2) must record the auto-adopted proceed entry with the planner's rationale in ## Q&A from Planning"
    assert_contains_ws "${LEAN_STEP2_BLOCK}" "${LEAN_STEP2_ROUTE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 2) must name the reasoned-proceed route"
    assert_contains_ws "${LEAN_STEP2_BLOCK}" "${SECRECY_RATIONALE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 2) must restate that the recorded planner rationale is repo-relative paths or identifiers only"
    assert_contains_ws "${LEAN_STEP2_BLOCK}" "${SECRECY_PRECEDENCE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 2) must state secrecy takes precedence over verbatim recording"
  fi
  if require_block LEAN_STEP8_BLOCK "${LEAN_APPROVAL_1199}" "${LEAN_STEP8_START}" "phase-1-plan.md (## Lean Approval Path step 8)"; then
    assert_contains_ws "${LEAN_STEP8_BLOCK}" "${LEAN_STEP8_ROUTE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 8) must name the reasoned-proceed route in the status summary"
    assert_contains_ws "${LEAN_STEP8_BLOCK}" "${LEAN_STEP8_ESTIMATE_MARKER}" \
      "phase-1-plan.md (## Lean Approval Path step 8) status summary must name the L estimate on the reasoned-proceed route"
  fi
fi

# --- phase-1-plan.md ## Persist the Plan: the ## Q&A from Planning template
#     documents the Phase-1 reasoned-proceed entry form --------------------------

if require_section PERSIST_1199 "${PHASE1_CONTENT}" "## Persist the Plan" "phase-1-plan.md"; then
  assert_contains_ws "${PERSIST_1199}" "${PROCEED_ENTRY_MARKER}" \
    "phase-1-plan.md (## Persist the Plan) ## Q&A from Planning template must document the Phase-1 reasoned-proceed entry form"
fi

# --- SKILL.md ## Pipeline: session shapes 2, 3 and 4 in sync -------------------

SHAPE2_START='2. **Trivial Fast Path continues to Phase 2**'
SHAPE2_ROUTE_MARKER='reasoned-proceed route'
SHAPE3_START='3. **Escalation stop**'
SHAPE3_QUALIFIER_MARKER='without a well-formed reasoned no-split verdict'
SHAPE4_START='4. **Split Gate stop'
SHAPE4_NONE_MARKER='a non-empty, non-`None` `### Split Recommendation`'
SHAPE4_NO_CARVEOUT_MARKER='the reasoned-proceed carve-out never applies in interactive or ticketless mode'
NONE_NOT_PREFIX_MARKER='`None` here means exactly `None`/`None.` or a single-line well-formed `None — <rationale>`, never a prefix match'

if require_section PIPELINE_1199 "${SKILL_CONTENT}" "## Pipeline" "SKILL.md"; then
  if require_block SHAPE2_BLOCK "${PIPELINE_1199}" "${SHAPE2_START}" "SKILL.md (## Pipeline shape 2)"; then
    assert_contains_ws "${SHAPE2_BLOCK}" "${SHAPE2_ROUTE_MARKER}" \
      "SKILL.md (## Pipeline) shape (2) must also continue to Phase 2 on the lean reasoned-proceed route"
  fi
  if require_block SHAPE3_BLOCK "${PIPELINE_1199}" "${SHAPE3_START}" "SKILL.md (## Pipeline shape 3)"; then
    assert_contains_ws "${SHAPE3_BLOCK}" "${SHAPE3_QUALIFIER_MARKER}" \
      "SKILL.md (## Pipeline) shape (3) must qualify the Split-Gate-synthesized question as a real split or an L/missing/malformed estimate without a well-formed reasoned no-split verdict"
  fi
  if require_block SHAPE4_BLOCK "${PIPELINE_1199}" "${SHAPE4_START}" "SKILL.md (## Pipeline shape 4)"; then
    assert_contains_ws "${SHAPE4_BLOCK}" "${SHAPE4_NONE_MARKER}" \
      "SKILL.md (## Pipeline) shape (4) must say a non-empty, non-None Split Recommendation triggers the stop"
    assert_contains_ws "${SHAPE4_BLOCK}" "${SHAPE4_NO_CARVEOUT_MARKER}" \
      "SKILL.md (## Pipeline) shape (4) must say the reasoned-proceed carve-out never applies in interactive or ticketless mode"
    assert_contains_ws "${SHAPE4_BLOCK}" "${NONE_NOT_PREFIX_MARKER}" \
      "SKILL.md (## Pipeline) shape (4) must define None as exact/single-line-verdict, never claim a prefix rule"
  fi
fi

# --- docs/autonomous-loop.md "Planning refuses to guess" condition list ---------
#     (whitespace-normalized: the doc is hard-wrapped)

LOOP_DOC_NEW_SIZE_BULLET='size estimate is not `L` unless the planner'"'"'s `### Split Recommendation` is a well-formed `None — <rationale>`, and there'"'"'s no real split recommendation'
LOOP_DOC_OLD_SIZE_BULLET='size estimate is not `L` and there'"'"'s no split recommendation;'
LOOP_DOC_SENSITIVE_BULLET='no file in `### Files to Modify`/`### Files to Create` matching the sensitive-path'

if require_content LOOP_CONTENT "${LOOP_DOC}" "docs/autonomous-loop.md"; then
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DOC_NEW_SIZE_BULLET}" \
    "docs/autonomous-loop.md 'Planning refuses to guess' list must allow an L estimate only with a well-formed None — <rationale> verdict and no real split recommendation"
  assert_lacks_ws "${LOOP_CONTENT}" "${LOOP_DOC_OLD_SIZE_BULLET}" \
    "docs/autonomous-loop.md must no longer carry the old unconditional 'size estimate is not L' bullet"
  assert_contains_ws "${LOOP_CONTENT}" "${LOOP_DOC_SENSITIVE_BULLET}" \
    "#1199 regression: the sensitive-path bullet of the 'Planning refuses to guess' list must stay unchanged"
fi

# --- codex.md: trigger wording aligned (no behavior change, still portable) ----

CODEX_NONE_MARKER='a non-empty, non-`None` `### Split Recommendation`'
if require_content CODEX_CONTENT "${CODEX_MD}" "codex.md"; then
  assert_contains_ws "${CODEX_CONTENT}" "${CODEX_NONE_MARKER}" \
    "codex.md Split Gate trigger must align to a non-empty, non-None Split Recommendation (no behavior change: Codex still asks on any L)"
  assert_contains_ws "${CODEX_CONTENT}" "${NONE_NOT_PREFIX_MARKER}" \
    "codex.md must define None as exact/single-line-verdict, never claim a prefix rule"
  assert_lacks_ws "${CODEX_CONTENT}" "${VERDICT_NONE_PREFIX_PHRASE}" \
    "codex.md must not carry the retired fail-open prefix rule"
fi
# AskUserQuestion stays absent from codex.md: asserted above (assert_file_lacks).

echo "implement-split-gate-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
