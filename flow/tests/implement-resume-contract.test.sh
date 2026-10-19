#!/usr/bin/env bash
# Documentation-contract coverage for ticket #1293: stage-aware resume of a
# crashed implement run. Pins the resume routing the implement skill documents
# so it cannot drift from the pipeline state machine or from its own phases:
#   - AC8: every `Stage = "<value>"` constant read from
#     watch/internal/pipeline/statemachine.go (fails closed on zero matches)
#     has a row in SKILL.md's stage -> route table, the table rows are pinned
#     verbatim, and the `Unknown pipeline stage` stop line is present.
#   - AC9: the `finalized` + `prUrl` stop and the unknown-stage stop sit in
#     Plan Verification's `#### Resume Routing` block, ahead of the `prepare`
#     paragraph and `## Ticket Ownership`; the block makes no `cenci pipeline`
#     call other than plan-check, no `gh issue edit`, no `--transition`.
#   - AC10: `stale` still reaches Phase 1's `## Existing Plan` gate before any
#     attach route.
#   - AC11: reset -> prepare on all three re-plan paths (`replan`,
#     `multiple` "Re-plan from scratch", `stale` at Phase 1's `## Existing
#     Plan`), plus the leftover-worktree statements.
#   - AC12: no `AskUserQuestion` in the resume-routing text.
#   - AC6/AC7: phase-2 attach step, baseline-gate rule and gone/leftover lines;
#     phase-3 resume brief template.
#
# Fixture-free grep/awk idiom of flow/tests/resume-abort-contract.test.sh:
# fence-aware pure extractors (no fail() side effect -- safe inside $(...))
# paired with nameref `require_*` wrappers that call fail() in the parent
# shell (flow/docs/shell-scripting-gotchas.md). Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob.
#
# Reads watch/internal/pipeline/statemachine.go, so .github/workflows/
# flow-ci.yml's `extra` flow filter lists that file: a new Stage constant must
# trigger this suite.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-resume-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-resume-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "implement-resume-contract.test.sh: failed to resolve repo directory." >&2; exit 2; }
SKILL_FILE="${FLOW_DIR}/skills/implement/SKILL.md"
PHASE1_FILE="${FLOW_DIR}/skills/implement/phases/phase-1-plan.md"
PHASE2_FILE="${FLOW_DIR}/skills/implement/phases/phase-2-worktree.md"
PHASE3_FILE="${FLOW_DIR}/skills/implement/phases/phase-3-test-red.md"
STATEMACHINE_FILE="${REPO_DIR}/watch/internal/pipeline/statemachine.go"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# load_file <result-var> <path> <label> -- reads a file or fails closed.
load_file() {
  local -n _file_result="$1"
  local _content
  if ! _content="$(cat "$2" 2>/dev/null)" || [[ -z "${_content}" ]]; then
    fail "$3: file not found/unreadable/empty: $2"
    _file_result=""
    return 1
  fi
  _file_result="${_content}"
}

load_file SKILL "${SKILL_FILE}" "SKILL.md" || true
load_file PHASE1 "${PHASE1_FILE}" "phase-1-plan.md" || true
load_file PHASE2 "${PHASE2_FILE}" "phase-2-worktree.md" || true
load_file PHASE3 "${PHASE3_FILE}" "phase-3-test-red.md" || true
load_file STATEMACHINE "${STATEMACHINE_FILE}" "statemachine.go" || true

# ---------------------------------------------------------------------------
# Pure extractors (no fail() side effect -- safe inside $(...))
# ---------------------------------------------------------------------------

# extract_block_raw <content> <start-line> <stop-prefix> -- fence-aware: from
# the exact start line up to (not including) the next real (unfenced) heading
# of any level, or the first unfenced line beginning with <stop-prefix>
# (empty = no prefix stop), or EOF.
extract_block_raw() {
  awk -v start="$2" -v stop="$3" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && $0 == start { on=1; print; next }
    on && !infence && /^#+ / { exit }
    on && !infence && stop != "" && index($0, stop) == 1 { exit }
    on { print }
  ' <<<"$1"
}

# extract_h2_raw <content> <heading-line> -- the named "## " section, bounded to
# the next real (unfenced) "## " heading (deeper headings stay inside).
extract_h2_raw() {
  awk -v want="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && $0 == want { on=1; print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_bullet_raw <content> <line-prefix> -- one top-level decision bullet
# (and its continuation lines) up to the next unfenced "- **" bullet.
extract_bullet_raw() {
  awk -v pfx="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && index($0, pfx) == 1 { on=1; print; next }
    on && !infence && (index($0, "- **") == 1 || /^#+ /) { exit }
    on { print }
  ' <<<"$1"
}

# line_of <content> <needle> [exact] -- 1-based line number of the first line
# containing the fixed-string needle (or equal to it when a third arg is
# given), or empty when absent. Empty needle never matches.
line_of() {
  local content="$1" needle="$2" exact="${3:-}"
  [[ -n "${needle}" ]] || { printf ''; return; }
  awk -v n="${needle}" -v exact="${exact}" '
    (exact != "" ? $0 == n : index($0, n) > 0) { print NR; exit }
  ' <<<"${content}"
}

# stage_values <statemachine-go-content> -- every `Stage = "<value>"` value.
stage_values() {
  grep -oE 'Stage = "[a-z_]+"' <<<"$1" | sed -E 's/^Stage = "([a-z_]+)"$/\1/'
}

# table_rows_raw <content> -- unfenced lines starting with "|".
table_rows_raw() {
  awk '
    /^```/ { infence = !infence; next }
    !infence && /^\|/ { print }
  ' <<<"$1"
}

# stages_without_row <block> <newline-separated stages> -- stages with no
# table row mentioning them as an inline-code value.
stages_without_row() {
  local rows stage
  rows="$(table_rows_raw "$1")"
  while IFS= read -r stage; do
    [[ -z "${stage}" ]] && continue
    if [[ "${rows}" != *'`'"${stage}"'`'* ]]; then
      printf '%s\n' "${stage}"
    fi
  done <<<"$2"
}

# block_violations <block> -- one line per forbidden construct found in the
# resume-routing block: a `cenci pipeline <verb>` other than plan-check, a
# `gh issue edit`, a `--transition`, or an AskUserQuestion literal.
block_violations() {
  local block="$1" verb
  while IFS= read -r verb; do
    [[ -z "${verb}" ]] && continue
    [[ "${verb}" == "plan-check" ]] || printf 'cenci pipeline %s\n' "${verb}"
  done < <(grep -oE 'cenci pipeline [a-z-]+' <<<"${block}" | sed -E 's/^cenci pipeline //')
  [[ "${block}" == *'gh issue edit'* ]] && printf 'gh issue edit\n'
  [[ "${block}" == *'--transition'* ]] && printf -- '--transition\n'
  [[ "${block}" == *'AskUserQuestion'* ]] && printf 'AskUserQuestion\n'
  return 0
}

# ---------------------------------------------------------------------------
# Nameref wrappers (call fail() in the parent shell -- must NOT run in $(...))
# ---------------------------------------------------------------------------

# require_block <result-var> <content> <start-line> <stop-prefix> <label>
require_block() {
  local -n _block_result="$1"
  local _body
  _body="$(extract_block_raw "$2" "$3" "$4")"
  if [[ -z "${_body}" ]]; then
    fail "$5: could not locate '$3' block"
    _block_result=""
    return 1
  fi
  _block_result="${_body}"
}

require_h2() {
  local -n _h2_result="$1"
  local _body
  _body="$(extract_h2_raw "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate '$3' section"
    _h2_result=""
    return 1
  fi
  _h2_result="${_body}"
}

require_bullet() {
  local -n _bullet_result="$1"
  local _body
  _body="$(extract_bullet_raw "$2" "$3")"
  if [[ -z "${_body}" ]]; then
    fail "$4: could not locate bullet starting '$3'"
    _bullet_result=""
    return 1
  fi
  _bullet_result="${_body}"
}

# require_line <result-var> <content> <needle> <label> [exact] -- 1-based line
# number, or fails loudly and assigns -1.
require_line() {
  local -n _line_result="$1"
  local _n
  _n="$(line_of "$2" "$3" "${5:-}")"
  if [[ -z "${_n}" ]]; then
    fail "$4: marker not found: [$3]"
    _line_result=-1
  else
    _line_result="${_n}"
  fi
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label} (expected to contain: ${pattern})"
}

assert_lacks() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${content}" ]] || { fail "${label}: empty content (a 'must not contain' claim about nothing is meaningless)"; return; }
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label} (must not contain: ${pattern})"
}

# assert_before <label> <first-offset-label> <first-line> <second-label> <second-line>
assert_line_order() {
  local label="$1" a_name="$2" a="$3" b_name="$4" b="$5"
  if [[ "${a}" -lt 0 || "${b}" -lt 0 ]]; then
    return
  fi
  [[ "${a}" -lt "${b}" ]] || fail "${label}: ${a_name} (line ${a}) must come before ${b_name} (line ${b})"
}

# =====================================================================
# 1. SKILL.md -- Resume Routing block in Plan Verification
# =====================================================================

BLOCK_HEADING='#### Resume Routing'
PREPARE_PARA_PREFIX='**If this run just set `hasPlanFile = true`**'
PREPARE_PARA_LABEL='SKILL.md (hasPlanFile -> prepare paragraph)'

require_block BLOCK "${SKILL}" "${BLOCK_HEADING}" "${PREPARE_PARA_PREFIX}" "SKILL.md" || true

if [[ -n "${BLOCK:-}" ]]; then
  assert_block() { assert_contains "${BLOCK}" "$1" "SKILL.md (#### Resume Routing) $2"; }

  # --- AC8: every Stage constant has a row; fails closed on zero constants ---
  STAGES="$(stage_values "${STATEMACHINE}")"
  if [[ -z "${STAGES}" ]]; then
    fail "statemachine.go: no 'Stage = \"<value>\"' constants found (fail closed -- the extractor or the file changed shape)"
  else
    MISSING_ROWS="$(stages_without_row "${BLOCK}" "${STAGES}")"
    if [[ -n "${MISSING_ROWS}" ]]; then
      fail "SKILL.md stage -> route table has no row for stage(s): $(tr '\n' ' ' <<<"${MISSING_ROWS}")"
    fi
  fi

  # --- AC6: the table rows verbatim from the ticket ---
  assert_block '| `pipeline.stage` | Route |' "table header must be pinned"
  assert_block '| `new`, `prepared`, `waiting_for_input`, `waiting_for_plan_approval` | no resume routing — today'"'"'s flow |' \
    "row for the no-routing stages must be pinned verbatim"
  assert_block '| `plan_approved`, `executed` | Phase 2 attaches the recorded worktree → Phase 3 with the resume brief |' \
    "plan_approved/executed row must be pinned verbatim"
  assert_block '| `reviewed` | Phase 2 attaches → Phase 6 |' \
    "reviewed row must be pinned verbatim"
  assert_block '| `finalized`, no `prUrl` | Phase 2 attaches → Phase 6 (this session has no review results — see Decisions) |' \
    "finalized-without-prUrl row must be pinned verbatim"
  assert_block '`### Decisions` section' \
    "the 'see Decisions' reference must name the ticket's ### Decisions section so it is not dangling"
  assert_block '| `finalized` with `prUrl` | stop in Plan Verification, right after `plan-check` |' \
    "finalized-with-prUrl row must be pinned verbatim"
  assert_block '| any other value | stop with `Unknown pipeline stage <stage> for #<id> — not resuming` |' \
    "unknown-stage row must be pinned verbatim"

  # --- AC6/AC8/AC9: resume line and stop lines ---
  assert_block 'Resuming #<id> from stage <stage> in <worktreePath> (branch <branch>, <n> commits ahead of main, <m> uncommitted files)' \
    "the exact resume line must be pinned"
  assert_block '#<id> already has PR <prUrl> — nothing to resume' \
    "the finalized+prUrl stop line must be pinned"
  assert_block 'Unknown pipeline stage <stage> for #<id> — not resuming' \
    "the unknown-stage stop line must be present"
  assert_block '`(missing)`' \
    "a missing stage must render as (missing) in the unknown-stage stop"
  assert_block 'In Review' \
    "the PR stop must note the In Review label may not have been applied"
  assert_block 'babysit' \
    "the PR stop must note the babysit arm may not have run"
  assert_block "Phase 9's post-PR steps" \
    "the PR stop must attribute the possibly-missed steps to Phase 9's post-PR steps"
  assert_block 'print any `plan-check` `warnings[]` entries' \
    "the unknown-stage stop must print plan-check warnings[] so an unreadable-state cause is shown"

  # --- AC6: attach only when worktreeRegistered ---
  assert_block '`pipeline.worktreeRegistered` is true' \
    "attach routes must apply only when worktreeRegistered is true"
  assert_block '`git -C <worktreePath> rev-list --count main..HEAD`' \
    "<n> must come from rev-list --count main..HEAD"
  assert_block '`git -C <worktreePath> status --short`' \
    "<m> must come from the status --short line count"

  # --- Q1: clean-worktree override ---
  assert_block 'Resumed worktree is clean — running Phase 3 with the standard brief' \
    "the clean-worktree override line must be pinned"

  # --- Q2: multiple-disambiguated-to-stale applicability ---
  assert_block '`multiple`' "must name the multiple branch"
  assert_block '`planCheckDecision = "stale"`' \
    "must state the multiple-disambiguated route is treated like stale"

  # --- AC10: stale gate ordering ---
  assert_block '`stale` still reaches Phase 1'"'"'s `## Existing Plan` human-confirmation gate before any attach route' \
    "the stale-gate ordering sentence must be pinned"

  # --- AC9/AC12: purity of the block ---
  VIOLATIONS="$(block_violations "${BLOCK}")"
  if [[ -n "${VIOLATIONS}" ]]; then
    fail "SKILL.md (#### Resume Routing) must contain no cenci pipeline call other than plan-check, no gh issue edit, no --transition, no AskUserQuestion; found: $(tr '\n' ';' <<<"${VIOLATIONS}")"
  fi
fi

# --- AC9: ordering -- block after the decision bullets, before the prepare
#     paragraph and before the real `## Ticket Ownership` heading ---
require_line L_PV "${SKILL}" '### Plan Verification' "SKILL.md ordering" exact
require_line L_LAST_BULLET "${SKILL}" '- **Unrecognized/empty `decision`**' "SKILL.md ordering"
require_line L_BLOCK "${SKILL}" "${BLOCK_HEADING}" "SKILL.md ordering" exact
require_line L_PREPARE "${SKILL}" "${PREPARE_PARA_PREFIX}" "${PREPARE_PARA_LABEL}"
require_line L_OWNERSHIP "${SKILL}" '## Ticket Ownership' "SKILL.md ordering (real heading)" exact
assert_line_order "SKILL.md" "Plan Verification heading" "${L_PV}" "Resume Routing block" "${L_BLOCK}"
assert_line_order "SKILL.md" "last decision bullet" "${L_LAST_BULLET}" "Resume Routing block" "${L_BLOCK}"
assert_line_order "SKILL.md" "Resume Routing block (holds the PR/unknown-stage stops)" "${L_BLOCK}" "the cenci pipeline prepare paragraph" "${L_PREPARE}"
assert_line_order "SKILL.md" "the cenci pipeline prepare paragraph" "${L_PREPARE}" "## Ticket Ownership" "${L_OWNERSHIP}"

# --- AC12: the resume bullet adds no question on attach routes / the PR stop ---
require_bullet RESUME_BULLET "${SKILL}" '- **`resume`**' "SKILL.md" || true
if [[ -n "${RESUME_BULLET:-}" ]]; then
  assert_contains "${RESUME_BULLET}" 'Resume Routing' \
    "SKILL.md (resume bullet) must point at the Resume Routing block"
  assert_contains "${RESUME_BULLET}" 'non-interactive' \
    "SKILL.md (resume bullet) must state attach routes and the PR stop are non-interactive"
  assert_lacks "${RESUME_BULLET}" 'AskUserQuestion' \
    "SKILL.md (resume bullet)"
fi

# --- AC10: the stale bullet states the Phase 1 gate runs before any attach ---
require_bullet STALE_BULLET "${SKILL}" '- **`stale`**' "SKILL.md" || true
if [[ -n "${STALE_BULLET:-}" ]]; then
  assert_contains "${STALE_BULLET}" 'before any attach' \
    "SKILL.md (stale bullet) must state Phase 1's gate runs before any attach route"
fi

# =====================================================================
# 2. AC11: reset -> prepare on all three re-plan paths
# =====================================================================

# assert_replan_path <content> <label> -- reset then prepare, gated on a
# non-empty pipeline.worktreePath, with the leftover-worktree statements.
assert_replan_path() {
  local content="$1" label="$2" r p
  [[ -n "${content}" ]] || return
  assert_contains "${content}" '`pipeline.worktreePath`' "${label} must gate the reset on pipeline.worktreePath"
  assert_contains "${content}" 'Leftover worktree <path> (branch <branch>) — Phase 2 fails with ErrWorktreeExists until a human removes it' \
    "${label} must hand the leftover worktree to the planner's user context"
  r="$(line_of "${content}" 'cenci pipeline reset <id>')"
  p="$(line_of "${content}" 'cenci pipeline prepare <id>')"
  if [[ -z "${r}" ]]; then
    fail "${label}: missing 'cenci pipeline reset <id>'"
  elif [[ -z "${p}" ]]; then
    fail "${label}: missing 'cenci pipeline prepare <id>'"
  else
    # Same-line is fine (reset ... then prepare); the in-line order is checked below.
    [[ "${r}" -le "${p}" ]] || fail "${label}: 'cenci pipeline reset <id>' must precede 'cenci pipeline prepare <id>'"
    if [[ "${r}" -eq "${p}" ]]; then
      local head="${content%%cenci pipeline prepare <id>*}"
      [[ "${head}" == *'cenci pipeline reset <id>'* ]] || fail "${label}: reset must precede prepare on the shared line"
    fi
  fi
}

require_bullet REPLAN_BULLET "${SKILL}" '- **`replan`**' "SKILL.md" || true
assert_replan_path "${REPLAN_BULLET:-}" "SKILL.md (replan bullet)"

require_bullet MULTIPLE_BULLET "${SKILL}" '- **`multiple`**' "SKILL.md" || true
assert_replan_path "${MULTIPLE_BULLET:-}" "SKILL.md (multiple bullet, Re-plan from scratch)"

require_h2 EXISTING_PLAN "${PHASE1}" '## Existing Plan' "phase-1-plan.md" || true
assert_replan_path "${EXISTING_PLAN:-}" "phase-1-plan.md (## Existing Plan, stale re-plan)"
if [[ -n "${EXISTING_PLAN:-}" ]]; then
  assert_contains "${EXISTING_PLAN}" '`### Risks`' \
    "phase-1-plan.md (## Existing Plan) must say the new plan records the leftover worktree under ### Risks"
fi

# =====================================================================
# 3. phase-2-worktree.md -- gate check, attach step, baseline, proceed
# =====================================================================

require_h2 GATE_CHECK "${PHASE2}" '## Gate Check' "phase-2-worktree.md" || true
if [[ -n "${GATE_CHECK:-}" ]]; then
  assert_contains "${GATE_CHECK}" 'On a resume' \
    "phase-2-worktree.md (## Gate Check) must say the plan --approve/execute calls are no-ops on a resume"
  assert_contains "${GATE_CHECK}" 'no-op warnings' \
    "phase-2-worktree.md (## Gate Check) must say the resume no-op warnings are shown"
fi

require_h2 CREATE_WT "${PHASE2}" '## Create Worktree' "phase-2-worktree.md" || true
if [[ -n "${CREATE_WT:-}" ]]; then
  assert_create() { assert_contains "${CREATE_WT}" "$1" "phase-2-worktree.md (## Create Worktree) $2"; }
  assert_create 'cenci pipeline worktree <id> --attach <worktreePath>' "must carry the --attach <worktreePath> resume step"
  assert_create 'git -C <worktreePath> rev-list --count main..HEAD' "must count commits ahead with the literal main"
  assert_create 'git -C <worktreePath> status --short' "must count uncommitted files with status --short"
  assert_create 'Resuming #<id> from stage <stage> in <worktreePath> (branch <branch>, <n> commits ahead of main, <m> uncommitted files)' \
    "must print the exact resume line"
  assert_create 'Recorded worktree <worktreePath> is gone — creating a fresh one' "must carry the gone-directory line"
  assert_create 'ErrWorktreeExists' "must handle the leftover directory/branch via ErrWorktreeExists"
  assert_create 'never stash' "must keep the crashed run's uncommitted changes (never stash or discard)"
  assert_lacks "${CREATE_WT}" 'run-gate.sh' "phase-2-worktree.md (## Create Worktree) -- parity marker order requires run-gate.sh to appear only in the Baseline section"
fi

BASELINE_APPLICABILITY="$(extract_block_raw "${PHASE2}" '### 1. Applicability' '')"
if [[ -z "${BASELINE_APPLICABILITY}" ]]; then
  fail "phase-2-worktree.md: could not locate '### 1. Applicability'"
else
  assert_contains "${BASELINE_APPLICABILITY}" 'attach route' \
    "phase-2-worktree.md (### 1. Applicability) must state the attach-route baseline rule"
  assert_contains "${BASELINE_APPLICABILITY}" 'git -C <worktreePath> status --short' \
    "phase-2-worktree.md (### 1. Applicability) must gate the baseline on an empty status --short"
  assert_contains "${BASELINE_APPLICABILITY}" 'Baseline gate: skipped — resumed worktree has <m> uncommitted files from the crashed run' \
    "phase-2-worktree.md (### 1. Applicability) must print the skipped-baseline line"
  assert_lacks "${BASELINE_APPLICABILITY}" 'run-gate.sh' \
    "phase-2-worktree.md (### 1. Applicability) -- must not name run-gate.sh (parity marker order)"
fi

PROCEED="$(extract_block_raw "${PHASE2}" '### 5. Proceed' '')"
if [[ -z "${PROCEED}" ]]; then
  fail "phase-2-worktree.md: could not locate '### 5. Proceed'"
else
  assert_contains "${PROCEED}" 'Phase 6' \
    "phase-2-worktree.md (### 5. Proceed) must route reviewed/finalized to Phase 6"
  assert_contains "${PROCEED}" 'skipping Phases 3' \
    "phase-2-worktree.md (### 5. Proceed) must skip Phases 3-5 on that route"
  assert_contains "${PROCEED}" 'Resumed worktree is clean — running Phase 3 with the standard brief' \
    "phase-2-worktree.md (### 5. Proceed) must carry the clean-worktree fallback to Phase 3"
fi

# =====================================================================
# 4. phase-3-test-red.md -- Resume Brief
# =====================================================================

require_h2 RESUME_BRIEF "${PHASE3}" '## Resume Brief' "phase-3-test-red.md" || true
if [[ -n "${RESUME_BRIEF:-}" ]]; then
  assert_brief() { assert_contains "${RESUME_BRIEF}" "$1" "phase-3-test-red.md (## Resume Brief) $2"; }
  assert_brief 'git status --short' "must name git status --short"
  assert_brief 'git diff --stat HEAD' "must name git diff --stat HEAD"
  assert_brief 'git log --oneline main..HEAD' "must name git log --oneline main..HEAD"
  assert_brief 'check which plan tasks are already done' "must carry the verify-before-implementing instruction"
  assert_brief 'never stash' "must forbid stashing or discarding the crashed run's changes"
  assert_brief 'Resumed worktree is clean — running Phase 3 with the standard brief' "must cover the clean-worktree line"
  assert_brief 'every lane' "must say the brief goes to every implementer lane"
  assert_brief 'compact' "must say the brief also goes to compact mode"
  assert_brief 'second resume' "must say a crash-resume is not a second resume for the Context Budget"
  assert_brief 'not a red-phase violation' "must say pre-existing passing tests are expected, not a red-phase violation"
fi

# =====================================================================
# 5. Non-vacuity self-tests: the extractors and checks must catch an
#    injected violation (a pass against the real docs proves nothing if the
#    machinery cannot fail).
# =====================================================================

SYNTH_BLOCK=$'#### Resume Routing\n\n| `pipeline.stage` | Route |\n|---|---|\n| `new` | x |\n| `executed` | y |\n\nRun `cenci pipeline plan-check <id>` then `cenci pipeline prepare <id>`.\n'
if [[ -z "$(block_violations "${SYNTH_BLOCK}")" ]]; then
  fail "self-test: block_violations did not flag an injected 'cenci pipeline prepare' call"
fi
if [[ -n "$(block_violations $'only a plan-check call: `cenci pipeline plan-check <id>`')" ]]; then
  fail "self-test: block_violations false-positived on a plan-check-only block"
fi
for injected in 'run `gh issue edit 5`' 'pass `--transition working`' 'use AskUserQuestion here'; do
  if [[ -z "$(block_violations "${injected}")" ]]; then
    fail "self-test: block_violations did not flag injected text: ${injected}"
  fi
done
SYNTH_MISSING="$(stages_without_row "${SYNTH_BLOCK}" $'new\nexecuted\nreviewed')"
if [[ "${SYNTH_MISSING}" != "reviewed" ]]; then
  fail "self-test: stages_without_row should report exactly 'reviewed' for a table lacking its row (got: ${SYNTH_MISSING})"
fi
SYNTH_FENCED=$'text\n```\n#### Resume Routing\n```\nafter'
if [[ -n "$(extract_block_raw "${SYNTH_FENCED}" '#### Resume Routing' '')" ]]; then
  fail "self-test: extract_block_raw started a block on a fenced heading"
fi
SYNTH_STOP=$'#### Resume Routing\nbody\n**If this run just set stop\nafter'
if [[ "$(extract_block_raw "${SYNTH_STOP}" '#### Resume Routing' '**If this run just set')" != $'#### Resume Routing\nbody' ]]; then
  fail "self-test: extract_block_raw did not stop at the prefix line"
fi

echo "implement-resume-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
