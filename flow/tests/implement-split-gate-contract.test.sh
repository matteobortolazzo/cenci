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
STOP_OPTION_LABEL='**"Stop — split via /cenci:refine (Recommended)"**'
PROCEED_OPTION_LABEL='**"Proceed as a single PR anyway"**'
STOP_PERSIST_NOTHING_MARKER='persist nothing, no `cenci pipeline` call of any kind'
STOP_RETAIN_MARKER='leaves `Working` and the assignee claim in place'
LEAN_ROUTE_MARKER='route to `## Unattended Escalation Path` with the synthesized split question'
PIN_AWAIT_INPUT_CMD='cenci pipeline await-input <id>'

# --- #1093 (PR 3/3) -- split-child-aware Stop option, parent-comment
#     feedback write, and the new cenci-oversize-child marker -------------
CHILD_STOP_OPTION_LABEL='**"Stop — re-partition parent #`<parentId>` via /cenci:refine `<parentId>` (Recommended)"**'
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
    "phase-1-plan.md (### Split Gate) must offer the Stop — split via /cenci:refine (Recommended) option"
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

echo "implement-split-gate-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
