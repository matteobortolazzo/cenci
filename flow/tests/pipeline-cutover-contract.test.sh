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

echo "pipeline-cutover-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
