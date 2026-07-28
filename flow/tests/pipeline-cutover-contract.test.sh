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

# =====================================================================
# Ticket #560 -- plan-file handling moves into `cenci pipeline plan-check`:
# discovery (globbing `.plans/<id>-*.md`), front-matter/section/slug
# validation, and the resume/stale/replan/none/multiple freshness decision
# all move out of skill prose and into the CLI. Cuts SKILL.md's old
# "Plan file auto-detection" mode-detection block and phase-1-plan.md's old
# `planCommitSha` vs. `git rev-parse HEAD` staleness-judgment prose over to
# rendering `cenci pipeline plan-check`'s returned `decision`.
#
# Marker choice mirrors the #558/#559 blocks above: assert_contains pins the
# literal new CLI invocation text (scoped to the specific section it must
# appear in, per this file's SKILL.md ## Pipeline precedent, so a match
# elsewhere in the file cannot vacuously satisfy it); assert_not_contains
# pins the exact old prose this ticket's diff removed (verified via `git
# diff --staged` against this ticket's actual before/after while writing
# this test), never a generic marker.
# =====================================================================

# extract_plan_verification_section <skill-md-content>
# Returns SKILL.md's "### Plan Verification" subsection body only (through
# the next "## "-level heading, "## Context Gathering (Delegated)"), so the
# assertion below cannot be satisfied by unrelated `cenci pipeline` mentions
# elsewhere in the file.
extract_plan_verification_section() {
  awk '
    /^### Plan Verification$/ { on=1 }
    on && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_existing_plan_section <phase-1-plan-md-content>
# Returns phase-1-plan.md's "## Existing Plan" section body only (through
# the next "## "-level heading, "## Trivial Fast Path").
extract_existing_plan_section() {
  awk '
    /^## Existing Plan$/ { on=1 }
    on && /^## / && !/^## Existing Plan$/ { exit }
    on { print }
  ' <<<"$1"
}

# ---------------------------------------------------------------------
# skills/implement/SKILL.md -- ### Plan Verification must invoke `cenci
# pipeline plan-check`; the old "Plan file auto-detection" mode-detection
# block (glob `.plans/<id>-*.md` directly in skill prose) must be gone.
# ---------------------------------------------------------------------
FILE="skills/implement/SKILL.md"
if CONTENT="$(read_doc "${FILE}")"; then
  PLAN_VERIFICATION_SECTION="$(extract_plan_verification_section "${CONTENT}")"
  if [[ -z "${PLAN_VERIFICATION_SECTION}" ]]; then
    fail "${FILE}: could not locate '### Plan Verification' section"
  else
    assert_contains "${PLAN_VERIFICATION_SECTION}" "cenci pipeline plan-check" "${FILE} (### Plan Verification section)"
    # Ticket #636: a resume against a missing state file (stage `new` --
    # .cenci/pipeline/ is gitignored, or the plan file predates the
    # pipeline CLI) must not dead-end on `label --transition working`/bare
    # `plan`. Plan Verification's resume/stale (and multiple-disambiguated-
    # to-resume) branches gain a closing `cenci pipeline prepare <id>` call
    # before Ticket Ownership -- scoped to this section so it cannot be
    # satisfied by an unrelated `cenci pipeline prepare` mention elsewhere
    # in the file (e.g. Context Gathering's own prepare call site).
    assert_contains "${PLAN_VERIFICATION_SECTION}" "cenci pipeline prepare" "${FILE} (### Plan Verification section)"
  fi
  assert_not_contains "${CONTENT}" 'glob for `.plans/<id>-*.md`' "${FILE} (mode-detection section)"
fi

# ---------------------------------------------------------------------
# skills/implement/phases/phase-1-plan.md -- ## Existing Plan must render
# the `cenci pipeline plan-check` verdict; the old front-matter
# `planCommitSha` vs. `git rev-parse HEAD` staleness-judgment prose, and the
# old "sanctioned exception" gh re-fetch carve-out it required, must both be
# gone -- that judgment call and that re-fetch now live in the CLI.
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-1-plan.md"
if CONTENT="$(read_doc "${FILE}")"; then
  EXISTING_PLAN_SECTION="$(extract_existing_plan_section "${CONTENT}")"
  if [[ -z "${EXISTING_PLAN_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Existing Plan' section"
  else
    assert_contains "${EXISTING_PLAN_SECTION}" "cenci pipeline plan-check" "${FILE} (## Existing Plan section)"
  fi
  assert_not_contains "${CONTENT}" 'planCommitSha` from front matter to `git rev-parse HEAD`' "${FILE} (## Existing Plan section)"
  assert_not_contains "${CONTENT}" "sanctioned exception to the" "${FILE} (## Existing Plan section)"
fi

# =====================================================================
# Ticket #688 -- the two remaining #718 pipeline-tooling-friction items:
# (1) plan --approve self-adopts a stage-less-but-plan-file-present ticket
# (watch/internal/pipeline/adopt.go); (2) `cenci pipeline worktree <id>
# --attach PATH` reuse mode (watch/internal/pipeline/worktree.go). Both get a
# `flow/` skill-doc call site: phase-2-worktree.md's `## Gate Check` documents
# the adoption warning as informational (not a failure gate), and its
# `## Create Worktree` gains the `--attach <path>` reuse bullet.
# phase-9-pr.md's `## Push` step must source the pushed branch from the
# recorded pipeline artifact rather than reconstructing it from
# `feature/<ticket-id>-<description>` alone, so an attached non-standard
# branch pushes the correct ref.
#
# Marker choice mirrors the #558/#559/#560 blocks above: assert_contains pins
# literal new text scoped to the specific section it must appear in (so a
# match elsewhere in the file cannot vacuously satisfy it, per
# flow/docs/shell-scripting-gotchas.md rule 3); assert_not_contains pins the
# exact pre-#688 line this ticket's diff must remove (verified against the
# real committed phase-9-pr.md content while writing this test).
# =====================================================================

# extract_gate_check_section <phase-2-worktree-md-content>
# Returns phase-2-worktree.md's "## Gate Check" section body only (through
# the next "## "-level heading, "## Arm Goal Autopilot"), so the adoption-
# warning assertion below cannot be satisfied by unrelated `cenci pipeline`
# mentions elsewhere in the file.
extract_gate_check_section() {
  awk '
    /^## Gate Check$/ { on=1 }
    on && /^## / && !/^## Gate Check$/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_create_worktree_section <phase-2-worktree-md-content>
# Returns phase-2-worktree.md's "## Create Worktree" section body only
# (through the next "## "-level heading, "## Baseline Gate Check").
extract_create_worktree_section() {
  awk '
    /^## Create Worktree$/ { on=1 }
    on && /^## / && !/^## Create Worktree$/ { exit }
    on { print }
  ' <<<"$1"
}

# extract_push_section <phase-9-pr-md-content>
# Returns phase-9-pr.md's "## Push" section body only (through the next
# "## "-level heading, "## Screenshots (UI Work)"), so the assertions below
# cannot be satisfied by the artifact-recording call already present
# elsewhere in this same section for a DIFFERENT purpose (recording the
# branch post-push) versus reading it back to source the push itself.
extract_push_section() {
  awk '
    /^## Push$/ { on=1 }
    on && /^## / && !/^## Push$/ { exit }
    on { print }
  ' <<<"$1"
}

# ---------------------------------------------------------------------
# phase-2-worktree.md -- ## Gate Check step 3 must document that a
# pre-stage-tracking plan self-adopts via `plan --approve`, and that the
# resulting `warnings[]` entry is informational, not a failure gate.
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-2-worktree.md"
if CONTENT="$(read_doc "${FILE}")"; then
  GATE_CHECK_SECTION="$(extract_gate_check_section "${CONTENT}")"
  if [[ -z "${GATE_CHECK_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Gate Check' section"
  else
    assert_contains "${GATE_CHECK_SECTION}" "self-adopt" "${FILE} (## Gate Check section, plan-file adoption)"
    assert_contains "${GATE_CHECK_SECTION}" "informational, not a failure" "${FILE} (## Gate Check section, adoption warning is informational)"
  fi

  # ---------------------------------------------------------------------
  # phase-2-worktree.md -- ## Create Worktree must document the ticket-mode
  # `--attach <path>` reuse bullet.
  # ---------------------------------------------------------------------
  CREATE_WORKTREE_SECTION="$(extract_create_worktree_section "${CONTENT}")"
  if [[ -z "${CREATE_WORKTREE_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Create Worktree' section"
  else
    assert_contains "${CREATE_WORKTREE_SECTION}" "--attach" "${FILE} (## Create Worktree section)"
  fi
fi

# ---------------------------------------------------------------------
# phase-9-pr.md -- ## Push must no longer reconstruct the pushed branch
# from `feature/<ticket-id>-<description>` alone (wrong for an attached
# non-standard branch); it must source the branch from the recorded
# pipeline artifact (`cenci pipeline artifact <id> --get`) instead.
# ---------------------------------------------------------------------
FILE="skills/implement/phases/phase-9-pr.md"
if CONTENT="$(read_doc "${FILE}")"; then
  PUSH_SECTION="$(extract_push_section "${CONTENT}")"
  if [[ -z "${PUSH_SECTION}" ]]; then
    fail "${FILE}: could not locate '## Push' section"
  else
    assert_not_contains "${PUSH_SECTION}" "git -C <worktree-path> push -u origin feature/<ticket-id>-<description>" "${FILE} (## Push, ticket mode)"
    assert_contains "${PUSH_SECTION}" "--get" "${FILE} (## Push sources branch via artifact --get)"
  fi
fi

echo "pipeline-cutover-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
