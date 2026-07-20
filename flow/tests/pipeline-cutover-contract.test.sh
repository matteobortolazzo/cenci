#!/usr/bin/env bash
# Contract test for ticket #558 — cut the implement skill's stage-sequencing
# prose over to invoking the new `cenci pipeline <stage> <id>` CLI instead of
# re-deriving "what stage am I in / what's next" from prompt logic. See
# .plans/558-pipeline-engine-core.md's "Flow Cutover (deeper rewrite —
# concrete diffs)" subsection for the per-file diff list this test pins.
#
# Follows the fixture-driven idiom of flow/tests/goal-autopilot-gate-contract.test.sh
# and flow/tests/subagent-cwd-contract.test.sh: a `failures=` counter, small
# assert_* helpers, self-contained, auto-discovered by the flow gate's
# `*.test.sh` glob. This test greps the real committed docs directly
# (relative to FLOW_DIR) since the contract under test lives in those docs'
# prose, not in generated output.
#
# Covered files (the only docs this test scans):
#   - skills/implement/SKILL.md               (## Pipeline section)
#   - skills/implement/phases/phase-1-plan.md
#   - skills/implement/phases/phase-2-worktree.md (## Gate Check)
#   - skills/implement/phases/phase-6-7-review.md
#   - skills/implement/phases/phase-8-docs.md and phase-9-pr.md (either)
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the
# specific replacement text at the edit site, never a generic marker that may
# already exist elsewhere in the file — a generic marker passes vacuously
# against unfixed prose and hides regressions): `cenci pipeline` does not
# appear anywhere in flow/skills/implement/ today (verified with a whole-tree
# grep while writing this test), so a `cenci pipeline <stage>` substring is
# not a pre-existing, vacuously-matchable marker. The exact `<id>`/`<ticket-id>`
# placeholder spelling the eventual prose uses is not pinned here — both
# spellings already coexist in this skill (e.g. `.plans/<id>-*.md` vs.
# `<ticket-id>-<description>`) — only the literal CLI invocation text is
# pinned, per each file's diff description in the plan's Flow Cutover
# subsection. SKILL.md's assertion is additionally scoped to the `## Pipeline`
# section body (not the whole file) so it cannot be satisfied by unrelated
# text elsewhere in the file.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "pipeline-cutover-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "pipeline-cutover-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc() {
  # read_doc <flow-relative-path> -- prints the real committed file's content,
  # or fails closed with a distinct "not found" message if it cannot be read
  # (a missing/unreadable file must never silently masquerade as empty
  # content, which would make assert_not_contains trivially "pass").
  local path="${FLOW_DIR}/$1"
  local content
  if ! content="$(cat "${path}" 2>/dev/null)"; then
    fail "$1: doc not found/unreadable: ${path}"
    printf ''
    return 1
  fi
  printf '%s' "${content}"
}

# assert_contains <content> <required-substring> <label>
# Asserts the SPECIFIC cenci-pipeline invocation text this file's edit site
# must introduce is present -- not a generic marker that could also match
# unrelated pre-existing text elsewhere in the file (see file header).
assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected cenci pipeline invocation not found: [${pattern}]"
}

# assert_not_contains <content> <forbidden-substring> <label>
# Asserts the SPECIFIC raw gh-CLI prose this file's edit site must remove is
# absent -- per docs/shell-scripting-gotchas.md rule 3, the forbidden
# substring must be the exact removed text (e.g. `gh label create "Working"`),
# never a generic marker like bare `gh issue edit`, since that invocation
# legitimately appears elsewhere in the codebase for unrelated purposes
# (comments, followups) and must not be a false-positive match.
assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden pre-cutover text still present: [${pattern}]"
}

# extract_pipeline_section <skill-md-content>
# Returns only the `## Pipeline` section body (that header through EOF, since
# it is SKILL.md's last level-2 section) so the assertion below cannot be
# satisfied by unrelated content elsewhere in SKILL.md.
extract_pipeline_section() {
  awk '
    /^## Pipeline$/ { on=1 }
    on { print }
  ' <<<"$1"
}

# =====================================================================
# skills/implement/SKILL.md -- the "## Pipeline" section's stage-sequencing
# narration ("execute in order", "between major phases, give a one-line
# status update", the 9-phase table's "what's next" framing) must be replaced
# with `cenci pipeline <stage> <id>` invocations rendering the returned
# state/next_actions/warnings/errors, per the plan's Flow Cutover subsection.
# =====================================================================
FILE="skills/implement/SKILL.md"
if CONTENT="$(read_doc "${FILE}")"; then
  PIPELINE_SECTION="$(extract_pipeline_section "${CONTENT}")"
  if [[ -z "${PIPELINE_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Pipeline' section"
  else
    assert_contains "${PIPELINE_SECTION}" "cenci pipeline" "${FILE} (## Pipeline section)"
  fi
fi

# =====================================================================
# skills/implement/phases/phase-1-plan.md -- replace "Proceed to Phase 2…",
# "continue directly into Phase 2", and the hard-stop-after-planning
# narration with `cenci pipeline plan <id>` at planning start (→
# waiting_for_plan_approval), rendering its next_actions.
# =====================================================================
FILE="skills/implement/phases/phase-1-plan.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline plan" "${FILE}"
fi

# =====================================================================
# skills/implement/phases/phase-2-worktree.md -- at the Gate Check (human
# launched the plan-file run = approval), invoke `cenci pipeline plan <id>
# --approve` then `cenci pipeline execute <id>`, replacing the "hand off to
# Phase 3 / proceed to Phase 3" baseline-gate transition prose with rendered
# next_actions.
# =====================================================================
FILE="skills/implement/phases/phase-2-worktree.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline plan" "${FILE} (plan --approve)"
  assert_contains "${CONTENT}" "--approve" "${FILE} (plan --approve)"
  assert_contains "${CONTENT}" "cenci pipeline execute" "${FILE} (execute)"
fi

# =====================================================================
# skills/implement/phases/phase-6-7-review.md -- invoke `cenci pipeline
# review <id>` at entry; replace the lite-docs "proceed to the next phase"
# line with rendered next_actions.
# =====================================================================
FILE="skills/implement/phases/phase-6-7-review.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline review" "${FILE}"
fi

# =====================================================================
# skills/implement/phases/phase-8-docs.md / phase-9-pr.md -- invoke `cenci
# pipeline finalize <id>` at Phase 8 start; render its next_actions.
# =====================================================================
FILE_8="skills/implement/phases/phase-8-docs.md"
FILE_9="skills/implement/phases/phase-9-pr.md"
CONTENT_8="$(read_doc "${FILE_8}")" || CONTENT_8=""
CONTENT_9="$(read_doc "${FILE_9}")" || CONTENT_9=""
if [[ "${CONTENT_8}" != *"cenci pipeline finalize"* && "${CONTENT_9}" != *"cenci pipeline finalize"* ]]; then
  fail "${FILE_8} or ${FILE_9}: expected cenci pipeline invocation not found: [cenci pipeline finalize]"
fi

# =====================================================================
# Ticket #559 -- deterministic pipeline mechanics: label lifecycle,
# worktree creation, artifact tracking. Cuts the implement skill's
# hand-rolled `gh label create` / `gh issue edit --add-label` / raw
# `git worktree add` prose over to the `cenci pipeline label`, `cenci
# pipeline worktree`, and `cenci pipeline artifact` CLI subcommands. See
# .plans/559-*.md's Flow Cutover subsection for the per-file diff list.
#
# Marker choice mirrors the #558 blocks above: `assert_contains` pins the
# exact new CLI invocation text; `assert_not_contains` pins the exact raw
# gh-CLI prose that must be gone, never a generic marker (e.g. bare `gh
# issue edit` legitimately appears elsewhere for unrelated purposes such as
# comments/followups, and must not be a false-positive match).
# =====================================================================

# ---------------------------------------------------------------------
# skills/implement/SKILL.md -- ## Ticket Ownership / ## Label "Working":
# the pipeline start Working-label application must call `cenci pipeline
# label <id> --transition working` instead of hand-rolling `gh label
# create "Working" ...` + `gh issue edit --add-label "Working"`. Pin the
# literal `gh label create "Working"` string as gone (CLI now owns
# Working-label creation), and pin `cenci pipeline label` as present.
# ---------------------------------------------------------------------
FILE="skills/implement/SKILL.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline label" "${FILE} (Label \"Working\" section)"
  assert_not_contains "${CONTENT}" 'gh label create "Working"' "${FILE} (Label \"Working\" section)"
fi

# ---------------------------------------------------------------------
# skills/implement/phases/phase-1-plan.md -- ## Persist the Plan's "Mark
# the ticket Planned" step (and the Trivial Fast Path step that mirrors
# it) must call `cenci pipeline label <id> --transition planned` instead
# of hand-rolling `gh label create "Planned" ...` + `gh issue edit
# --add-label "Planned" [--remove-label "Working"]`. The saved plan path
# must also be recorded via `cenci pipeline artifact <id> --plan`.
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-1-plan.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline label" "${FILE} (Mark the ticket Planned)"
  assert_contains "${CONTENT}" "--transition planned" "${FILE} (Mark the ticket Planned)"
  assert_not_contains "${CONTENT}" 'gh label create "Planned"' "${FILE} (Mark the ticket Planned)"
  assert_contains "${CONTENT}" "cenci pipeline artifact" "${FILE} (record plan path)"
fi

# ---------------------------------------------------------------------
# skills/implement/phases/phase-2-worktree.md -- ## Create Worktree must
# invoke `cenci pipeline worktree` instead of the raw `git worktree add
# .worktrees/<ticket-id>-<description> -b feature/<ticket-id>-<description>`
# ticket-mode invocation. Pin only that exact ticket-mode string as gone
# (not bare `git worktree add`, which could still appear elsewhere in doc
# prose, e.g. a `cenci:worktrees` reference or comment).
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-2-worktree.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline worktree" "${FILE} (Create Worktree)"
  assert_not_contains "${CONTENT}" "git worktree add .worktrees/<ticket-id>-<description>" "${FILE} (Create Worktree)"
fi

# ---------------------------------------------------------------------
# skills/implement/phases/phase-9-pr.md -- ## Push / ## PR / ## Labels:
# after PR creation, the Working -> In Review swap must call `cenci
# pipeline label <id> --transition in-review` instead of hand-rolling `gh
# label create "In Review" ...` + `gh issue edit --add-label "In Review"
# --remove-label "Working"`. Branch/PR/PR-number must also be recorded via
# `cenci pipeline artifact <id> --branch/--pr/--pr-number`.
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-9-pr.md"
if CONTENT="$(read_doc "${FILE}")"; then
  assert_contains "${CONTENT}" "cenci pipeline label" "${FILE} (Labels)"
  assert_contains "${CONTENT}" "--transition in-review" "${FILE} (Labels)"
  assert_not_contains "${CONTENT}" 'gh label create "In Review"' "${FILE} (Labels)"
  assert_contains "${CONTENT}" "cenci pipeline artifact" "${FILE} (record branch/PR)"
fi

echo "pipeline-cutover-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
