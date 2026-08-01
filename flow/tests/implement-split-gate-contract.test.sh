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

if require_section PIPELINE_SECTION "${SKILL_CONTENT}" "## Pipeline" "SKILL.md"; then
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_INVARIANT_MARKER}" \
    "SKILL.md (## Pipeline) must state the single-deliverable invariant"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_OVERFLOW_MARKER}" \
    "SKILL.md (## Pipeline) must state the mid-run scope-overflow rule's complete-and-capture-as-Followup outcome"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_OVERFLOW_ERROR_GATE_MARKER}" \
    "SKILL.md (## Pipeline) must state the mid-run scope-overflow rule's other disjunctive outcome (stop at an error gate recommending /cenci:refine)"
  assert_section_contains "${PIPELINE_SECTION}" "${SKILL_FOURTH_SHAPE_MARKER}" \
    "SKILL.md (## Pipeline) must add a fourth named planning-session shape for the Split Gate stop"
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
