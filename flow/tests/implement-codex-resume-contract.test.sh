#!/usr/bin/env bash
# Documentation-contract coverage for ticket #1308: the Codex implement
# procedure (skills/implement/codex.md) records the `cenci pipeline` lifecycle
# and routes a crashed `apply` on the recorded stage, mirroring the Claude Code
# resume routing pinned by implement-resume-contract.test.sh (#1293). Pins:
#   - AC1/AC2: the nine lifecycle calls, in order, inside the `**Pipeline
#     lifecycle (ticket mode).**` block, followed by the errors[] hard-stop
#     sentence; every call is also named at the step that runs it.
#   - AC3/AC4: every `Stage = "<value>"` constant read from
#     watch/internal/pipeline/statemachine.go (fails closed on zero matches)
#     has a row in the stage -> route table; header and six rows are verbatim.
#   - AC5-AC7: apply `plan-check` sits after the blockedBy gate and before the
#     plan-file persist; both stop texts sit before the persist anchor.
#   - AC8-AC12: attach, resume line, baseline skip, resume brief
#     (byte-identical to phase-3-test-red.md), second-resume sentence and the
#     review-entry skip list.
#   - AC13/AC14: re-plan reset -> prepare before persist; the `/plan`
#     read-only plan-check (no writing `cenci pipeline` verb).
#   - AC15: no AskUserQuestion in the routing text; the stale and multiple
#     prompts use "the client's available user-input mechanism".
#   - AC16: the two doc sentences (flow/docs/codex.md, flow/README.md).
#
# Fixture-free grep/awk idiom of implement-resume-contract.test.sh (the
# extractors are copied inline so that suite stays unchanged): fence-aware pure
# extractors (no fail() side effect -- safe inside $(...)) paired with nameref
# `require_*` wrappers that call fail() in the parent shell
# (flow/docs/shell-scripting-gotchas.md rules 10, 13, 29, 30). Ordering uses
# byte offsets under LC_ALL=C scoped to one block, never a whole-file first
# occurrence, and every ordering/extraction helper has a non-vacuity self-test.
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob.
#
# Reads watch/internal/pipeline/statemachine.go, so .github/workflows/
# flow-ci.yml's `extra` flow filter lists that file: a new Stage constant must
# trigger this suite.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-codex-resume-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-codex-resume-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "implement-codex-resume-contract.test.sh: failed to resolve repo directory." >&2; exit 2; }
CODEX_FILE="${FLOW_DIR}/skills/implement/codex.md"
PHASE3_FILE="${FLOW_DIR}/skills/implement/phases/phase-3-test-red.md"
STATEMACHINE_FILE="${REPO_DIR}/watch/internal/pipeline/statemachine.go"
DOCS_CODEX_FILE="${FLOW_DIR}/docs/codex.md"
README_FILE="${FLOW_DIR}/README.md"
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

load_file CODEX "${CODEX_FILE}" "implement/codex.md" || true
load_file PHASE3 "${PHASE3_FILE}" "phase-3-test-red.md" || true
load_file STATEMACHINE "${STATEMACHINE_FILE}" "statemachine.go" || true
load_file DOCS_CODEX "${DOCS_CODEX_FILE}" "docs/codex.md" || true
load_file README "${README_FILE}" "README.md" || true

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

# extract_from_prefix_raw <content> <start-prefix> <stop-prefix> -- like
# extract_block_raw, but the block starts at the first unfenced line that
# BEGINS with <start-prefix> (a multi-line paragraph's first line).
extract_from_prefix_raw() {
  awk -v start="$2" -v stop="$3" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && index($0, start) == 1 { on=1; print; next }
    on && !infence && /^#+ / { exit }
    on && !infence && stop != "" && index($0, stop) == 1 { exit }
    on { print }
  ' <<<"$1"
}

# extract_bullet_raw <content> <line-prefix> -- one single-paragraph bullet and
# its continuation lines, up to the next blank line or unfenced "- " bullet.
extract_bullet_raw() {
  awk -v pfx="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && index($0, pfx) == 1 { on=1; print; next }
    on && !infence && ($0 == "" || index($0, "- ") == 1) { exit }
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

# first_fence_raw <content> -- the lines inside the first fenced code block.
first_fence_raw() {
  awk '
    /^```/ { n++; if (n == 2) exit; next }
    n == 1 { print }
  ' <<<"$1"
}

# between_raw <content> <start> <end> -- the text from the first <start> to the
# first <end> after it (plain slice, fences ignored); empty when either is
# absent. Both anchors must be single-line fixed strings.
between_raw() {
  local content="$1" start="$2" end="$3" rest
  [[ -n "${start}" && -n "${end}" ]] || return 0
  [[ "${content}" == *"${start}"* ]] || return 0
  rest="${content#*"${start}"}"
  [[ "${rest}" == *"${end}"* ]] || return 0
  printf '%s%s' "${start}" "${rest%%"${end}"*}"
}

# offset_of <content> <needle> -- byte offset (LC_ALL=C) of the first
# occurrence of the fixed string, or -1 when absent. Empty needle never matches.
offset_of() {
  local LC_ALL=C
  local content="$1" needle="$2" prefix
  if [[ -z "${needle}" || "${content}" != *"${needle}"* ]]; then
    printf '%s' "-1"
    return
  fi
  prefix="${content%%"${needle}"*}"
  printf '%s' "${#prefix}"
}

# count_of <content> <needle> -- occurrences (not lines) of the fixed string.
count_of() {
  [[ -n "$2" ]] || { printf '0'; return; }
  grep -oF -- "$2" <<<"$1" | wc -l | tr -d '[:space:]'
}

# calls_in_order <content> <needle>... -- exit 0 only when every needle is
# present and the first-occurrence byte offsets strictly increase.
calls_in_order() {
  local content="$1" needle off prev=-1
  shift
  for needle in "$@"; do
    off="$(offset_of "${content}" "${needle}")"
    [[ "${off}" -gt "${prev}" ]] || return 1
    prev="${off}"
  done
  return 0
}

# stage_decl_pattern anchors on the constant declaration line
# (`StageNew  Stage = "new"`), so a comment or a string elsewhere never counts.
stage_decl_pattern='^[[:space:]]*Stage[A-Za-z0-9_]+[[:space:]]+Stage[[:space:]]*=[[:space:]]*"'

# stage_values <statemachine-go-content> -- the value of every `Stage*`
# constant declaration whose value is a lowercase/digit/underscore/hyphen word.
stage_values() {
  grep -E "${stage_decl_pattern}[a-z0-9_-]+\"" <<<"$1" | sed -E 's/^.*Stage[[:space:]]*=[[:space:]]*"([a-z0-9_-]+)".*$/\1/'
}

# stage_decl_count <statemachine-go-content> -- every `Stage*` constant
# declaration regardless of its value's shape (must equal the stage_values count,
# so a value the value-pattern cannot read fails loudly instead of vanishing).
stage_decl_count() {
  grep -cE "${stage_decl_pattern}" <<<"$1"
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

# write_verbs_in <text> -- one line per `cenci pipeline <verb>` token naming any
# verb other than the read-only plan-check (an allowlist: an unknown or new
# verb is flagged). Prose such as "no `cenci pipeline` call" has a backtick
# right after "pipeline" and is not a token.
write_verbs_in() {
  local verb
  while IFS= read -r verb; do
    [[ "${verb}" == "plan-check" ]] || printf 'cenci pipeline %s\n' "${verb}"
  done < <(grep -oE 'cenci pipeline [a-z-]+' <<<"$1" | sed -E 's/^cenci pipeline //')
  return 0
}

# bare_plan_flags <text> -- one line per `artifact <id> --plan` occurrence that
# is not followed by ` <plan-path>` (the CLI exits 2 on a bare --plan).
bare_plan_flags() {
  local hit
  while IFS= read -r hit; do
    [[ "${hit}" == 'artifact <id> --plan <plan-path>'* ]] || printf '%s\n' "${hit}"
  done < <(grep -oE 'artifact <id> --plan.{0,13}' <<<"$1")
  return 0
}

# strip_longer_calls <content> <call> <call>... -- <content> with every other
# call that contains <call> as a proper substring removed, so counting <call>
# is not satisfied by its longer sibling (`plan <id>` vs `plan <id> --approve`).
strip_longer_calls() {
  local content="$1" call="$2" other
  shift 2
  for other in "$@"; do
    if [[ "${other}" != "${call}" && "${other}" == *"${call}"* ]]; then
      content="${content//"${other}"/}"
    fi
  done
  printf '%s' "${content}"
}

# ---------------------------------------------------------------------------
# Nameref wrappers (call fail() in the parent shell -- must NOT run in $(...))
# ---------------------------------------------------------------------------

# require_extract <result-var> <label> <body> -- fails when the extraction is empty.
require_extract() {
  local -n _extract_result="$1"
  if [[ -z "$3" ]]; then
    fail "$2"
    _extract_result=""
    return 1
  fi
  _extract_result="$3"
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

# assert_count <content> <needle> <expected> <label> -- exact occurrence count.
assert_count() {
  local actual
  actual="$(count_of "$1" "$2")"
  [[ "${actual}" -eq "$3" ]] || fail "$4 (expected $3 occurrence(s) of [$2], found ${actual})"
}

# assert_order <label> <content> <needle>... -- strictly increasing offsets.
assert_order() {
  local label="$1" content="$2"
  shift 2
  [[ -n "${content}" ]] || { fail "${label}: empty content to order"; return; }
  calls_in_order "${content}" "$@" || fail "${label}: markers missing or out of order: $(printf '[%s] ' "$@")"
}

# =====================================================================
# 1. Block extraction
# =====================================================================

ROUTING_LEAD='**Resume routing (apply, ticket mode).**'
LIFECYCLE_LEAD='**Pipeline lifecycle (ticket mode).**'
PERSIST_PREFIX='In normal apply mode persist'
PERSIST_ANCHOR='persist `.plans/'
PLAN_SENTENCE_PREFIX='In ticket mode, `/plan` also runs'
PLAN_END_PREFIX='Then return an approved plan'
DISPATCH_LEAD='**Implementation dispatch (apply only).**'
DISPATCH_END_PREFIX='Then, before the reviews'

require_extract ROUTING "codex.md: could not locate the '${ROUTING_LEAD}' block" \
  "$(extract_block_raw "${CODEX}" "${ROUTING_LEAD}" "${LIFECYCLE_LEAD}")" || true
require_extract LIFECYCLE "codex.md: could not locate the '${LIFECYCLE_LEAD}' block" \
  "$(extract_block_raw "${CODEX}" "${LIFECYCLE_LEAD}" "${PERSIST_PREFIX}")" || true
require_extract PLAN_SENTENCE "codex.md: could not locate the /plan plan-check sentence starting '${PLAN_SENTENCE_PREFIX}'" \
  "$(extract_from_prefix_raw "${CODEX}" "${PLAN_SENTENCE_PREFIX}" "${PLAN_END_PREFIX}")" || true
require_extract DISPATCH "codex.md: could not locate the '${DISPATCH_LEAD}' block" \
  "$(extract_from_prefix_raw "${CODEX}" "${DISPATCH_LEAD}" "${DISPATCH_END_PREFIX}")" || true
require_extract PERSIST_PARA "codex.md: could not locate the paragraph starting '${PERSIST_PREFIX}'" \
  "$(extract_from_prefix_raw "${CODEX}" "${PERSIST_PREFIX}" 'Arm')" || true

# =====================================================================
# 2. AC3/AC4: stage -> route table
# =====================================================================

if [[ -n "${ROUTING:-}" ]]; then
  STAGES="$(stage_values "${STATEMACHINE}")"
  STAGE_DECLS="$(stage_decl_count "${STATEMACHINE}")"
  STAGE_VALUE_COUNT="$(grep -c . <<<"${STAGES}")"
  if [[ -z "${STAGES}" ]]; then
    fail "statemachine.go: no 'Stage* Stage = \"<value>\"' constants found (fail closed -- the extractor or the file changed shape)"
  elif [[ "${STAGE_DECLS}" -ne "${STAGE_VALUE_COUNT}" ]]; then
    fail "statemachine.go: ${STAGE_DECLS} Stage constant declaration(s) but only ${STAGE_VALUE_COUNT} readable value(s) -- the stage value pattern no longer matches every constant"
  else
    MISSING_ROWS="$(stages_without_row "${ROUTING}" "${STAGES}")"
    if [[ -n "${MISSING_ROWS}" ]]; then
      fail "codex.md stage -> route table has no row for stage(s): $(tr '\n' ' ' <<<"${MISSING_ROWS}")"
    fi
  fi

  assert_contains "${ROUTING}" '| `pipeline.stage` | Route |' "table header must be pinned"
  assert_contains "${ROUTING}" '|---|---|' "table separator must be pinned"
  assert_contains "${ROUTING}" '| `new`, `prepared`, `waiting_for_input`, `waiting_for_plan_approval` | no resume routing |' \
    "row for the no-routing stages must be pinned verbatim"
  assert_contains "${ROUTING}" '| `plan_approved`, `executed` | attach, then Implementation dispatch with the resume brief |' \
    "plan_approved/executed row must be pinned verbatim"
  assert_contains "${ROUTING}" '| `reviewed` | attach, then review entry |' \
    "reviewed row must be pinned verbatim"
  assert_contains "${ROUTING}" '| `finalized` with no `prUrl` | attach, then review entry |' \
    "finalized-without-prUrl row must be pinned verbatim"
  assert_contains "${ROUTING}" '| `finalized` with `prUrl` | stop |' \
    "finalized-with-prUrl row must be pinned verbatim"
  assert_contains "${ROUTING}" '| any other value | unknown-stage stop |' \
    "unknown-stage row must be pinned verbatim"
  ROW_COUNT="$(table_rows_raw "${ROUTING}" | wc -l | tr -d '[:space:]')"
  [[ "${ROW_COUNT}" -eq 8 ]] || fail "codex.md stage table must have exactly a header, a separator and six rows (found ${ROW_COUNT} table lines)"
fi

# =====================================================================
# 3. AC1/AC2: lifecycle calls in order, then the errors[] stop sentence
# =====================================================================

LIFECYCLE_CALLS=(
  'cenci pipeline prepare <id>'
  'cenci pipeline plan <id>'
  'cenci pipeline plan <id> --approve'
  'cenci pipeline artifact <id> --plan'
  'cenci pipeline worktree <id> --slug <slug>'
  'cenci pipeline execute <id>'
  'cenci pipeline review <id>'
  'cenci pipeline finalize <id>'
  'cenci pipeline artifact <id> --pr <pr-url> --pr-number <n>'
)
ERRORS_SENTENCE='Every one of these calls stops the run on a non-empty `errors[]` and reports the errors.'

if [[ -n "${LIFECYCLE:-}" ]]; then
  assert_order "codex.md lifecycle block" "${LIFECYCLE}" "${LIFECYCLE_CALLS[@]}" "${ERRORS_SENTENCE}"
  assert_contains "${LIFECYCLE}" 'A call that exits non-zero, or prints empty or unparseable output, stops the run the same way and reports its stderr; this covers the re-plan `cenci pipeline reset <id>` too.' \
    "the lifecycle stop must cover a non-zero exit, empty or unparseable output, and the re-plan reset"
  assert_contains "${LIFECYCLE}" 'of the `plan-check` result (the picked file after a `multiple` choice) on a saved-plan apply)' \
    "lifecycle step 4 must name the picked file after a multiple choice as the saved-plan <plan-path>"
  assert_contains "${LIFECYCLE}" '(at acceptance-review entry)' "the review call must be tied to acceptance-review entry"
  assert_contains "${LIFECYCLE}" '(before the rebase/commit step)' "the finalize call must be tied to the step before the rebase/commit"
  assert_contains "${LIFECYCLE}" '(only after a successful `gh pr create`)' "the PR artifact call must be tied to a successful gh pr create"
  assert_contains "${LIFECYCLE}" 'the monotonic `plan --approve` and `execute` calls run unchanged and return no-op `warnings[]`' \
    "a resume's plan --approve/execute no-op warnings must be described"
  assert_contains "${LIFECYCLE}" 'never treat them as failures' \
    "the resume no-op warnings must never be treated as failures"
fi

# Each call is also named at the step that runs it (wired, not just listed).
for call in "${LIFECYCLE_CALLS[@]}"; do
  [[ "$(count_of "$(strip_longer_calls "${CODEX}" "${call}" "${LIFECYCLE_CALLS[@]}")" "${call}")" -ge 2 ]] \
    || fail "codex.md must name [${call}] at its step as well as in the lifecycle block"
done

# The CLI exits 2 on a bare `--plan`: every artifact --plan call carries a path.
[[ "$(count_of "${CODEX}" 'artifact <id> --plan')" -ge 2 ]] \
  || fail "codex.md must name 'artifact <id> --plan <plan-path>' in the lifecycle block and at the persist step"
BARE_PLAN="$(bare_plan_flags "${CODEX}")"
[[ -z "${BARE_PLAN}" ]] || fail "codex.md has an 'artifact <id> --plan' call without a ' <plan-path>' argument: $(tr '\n' ';' <<<"${BARE_PLAN}")"

# =====================================================================
# 4. AC5-AC7, AC13: ordering anchors in the full file
# =====================================================================

APPLY_PC_ANCHOR='the apply-time `plan-check`'
PR_STOP_TEXT='#<id> already has PR <prUrl> — nothing to resume'
UNKNOWN_STOP_TEXT='Unknown pipeline stage <stage> for #<id> — not resuming'
REPLAN_BULLET_PREFIX='- A new-plan apply (decision `none` or `replan`)'

assert_count "${CODEX}" "${APPLY_PC_ANCHOR}" 1 "codex.md apply-time plan-check anchor must be unique"
assert_count "${CODEX}" "${PERSIST_ANCHOR}" 1 "codex.md persist anchor must be unique"
assert_count "${CODEX}" "${PR_STOP_TEXT}" 1 "codex.md PR stop text must be unique"
assert_count "${CODEX}" "${UNKNOWN_STOP_TEXT}" 1 "codex.md unknown-stage stop text must be unique"

assert_order "codex.md apply plan-check placement" "${CODEX}" '`blockedBy`' "${APPLY_PC_ANCHOR}" "${PERSIST_ANCHOR}"
assert_order "codex.md PR stop placement" "${CODEX}" "${APPLY_PC_ANCHOR}" "${PR_STOP_TEXT}" "${PERSIST_ANCHOR}"
assert_order "codex.md unknown-stage stop placement" "${CODEX}" "${APPLY_PC_ANCHOR}" "${UNKNOWN_STOP_TEXT}" "${PERSIST_ANCHOR}"
assert_order "codex.md section order" "${CODEX}" \
  'The plan also carries' "${PLAN_SENTENCE_PREFIX}" "${PLAN_END_PREFIX}" 'Stop before mutations' \
  "${ROUTING_LEAD}" "${LIFECYCLE_LEAD}" "${PERSIST_PREFIX}"
assert_order "codex.md parity anchor order" "${CODEX}" 'Stop before mutations' 'create the worktree'

require_extract REPLAN_BULLET "codex.md: could not locate the re-plan reset bullet starting '${REPLAN_BULLET_PREFIX}'" \
  "$(extract_bullet_raw "${ROUTING:-}" "${REPLAN_BULLET_PREFIX}")" || true
if [[ -n "${REPLAN_BULLET:-}" ]]; then
  assert_order "codex.md re-plan reset bullet" "${REPLAN_BULLET}" 'cenci pipeline reset <id>' 'cenci pipeline prepare <id>'
  assert_contains "${REPLAN_BULLET}" '`pipeline.worktreePath` is non-empty' "the reset must be gated on a non-empty pipeline.worktreePath"
  assert_contains "${REPLAN_BULLET}" 'then persist the new plan' "the reset and prepare must precede persisting the new plan"
  assert_contains "${REPLAN_BULLET}" 'A failure of that `reset` or `prepare` (a non-empty `errors[]`, a non-zero exit, or empty or unparseable output) stops the run before the persist step and reports it.' \
    "a failing re-plan reset or prepare must stop the run before the persist step"
  assert_order "codex.md re-plan bullet before persist" "${CODEX}" "${REPLAN_BULLET_PREFIX}" "${PERSIST_ANCHOR}"
fi

# =====================================================================
# 5. AC14: /plan runs only the read-only plan-check
# =====================================================================

if [[ -n "${PLAN_SENTENCE:-}" ]]; then
  assert_contains "${PLAN_SENTENCE}" 'the read-only `cenci pipeline plan-check <id>`' "/plan must run the read-only plan-check"
  assert_contains "${PLAN_SENTENCE}" 'ticketless mode skips it' "/plan must say ticketless mode skips the plan-check"
  assert_contains "${PLAN_SENTENCE}" 'Leftover worktree <path> (branch <branch>) — Phase 2 fails with ErrWorktreeExists until a human removes it' \
    "/plan must hand the leftover worktree to the planner's context verbatim"
  assert_contains "${PLAN_SENTENCE}" '`### Risks`' "/plan must say the new plan records the leftover worktree under ### Risks"
  assert_contains "${PLAN_SENTENCE}" 'If that `plan-check` fails, add `plan-check unavailable: <error>` to the planner'"'"'s context under `### Risks` and continue: `/plan` is read-only and never stops on it.' \
    "a failing /plan plan-check must be recorded as a risk and planning must continue"
  assert_contains "${PLAN_SENTENCE}" '`/plan` makes no `cenci pipeline` call that writes anything.' \
    "/plan must state it makes no writing cenci pipeline call"
  PLAN_WRITES="$(write_verbs_in "${PLAN_SENTENCE}")"
  [[ -z "${PLAN_WRITES}" ]] || fail "codex.md /plan text must name no writing cenci pipeline verb; found: $(tr '\n' ';' <<<"${PLAN_WRITES}")"
  assert_lacks "${PLAN_SENTENCE}" 'create the worktree' "/plan text must not carry the apply-only worktree anchor (parity marker order)"
  assert_lacks "${PLAN_SENTENCE}" 'Stop before mutations' "/plan text must not carry the stop anchor (parity marker order)"
fi

assert_contains "${CODEX}" 'Stop before mutations and tell the user to continue with `apply` in this same session (a new-plan apply); `cenci run implement apply <ticket> --agent codex` is only for resuming an already-saved plan after a crash.' \
  "the /plan handoff must continue with apply in the same session and keep the launcher form for a saved-plan crash resume"

# =====================================================================
# 6. AC5-AC9, AC15: the resume-routing block
# =====================================================================

if [[ -n "${ROUTING:-}" ]]; then
  assert_lacks "${ROUTING}" 'AskUserQuestion' "codex.md resume routing must not name AskUserQuestion"
  assert_contains "${ROUTING}" 'Ticketless (task) mode skips this block, every `cenci pipeline` call below and all resume routing.' \
    "ticketless mode must skip plan-check, every cenci pipeline call and all routing"
  assert_contains "${ROUTING}" 'run `cenci pipeline plan-check <id>` for a saved-plan apply (`apply <ticket>`, `resume <ticket>` or a `.plans/<file>` target)' \
    "a saved-plan apply must run plan-check"
  assert_contains "${ROUTING}" '`cenci pipeline plan-check <id> --replan-requested` for a new-plan apply' \
    "a new-plan apply must run plan-check --replan-requested"
  assert_contains "${ROUTING}" 'never read `.cenci/pipeline/` yourself and never make a second `plan-check` call' \
    "routing must read only the one plan-check result"
  assert_contains "${ROUTING}" $'`plan-check`\'s own `errors[]` for the `none` and `multiple` decisions is a normal branch (exit 0), not the lifecycle hard-stop below.' \
    "plan-check's own errors[] must be exempted from the lifecycle hard-stop"

  MECHANISM=$'the client\'s available user-input mechanism'

  require_extract STALE_BULLET "codex.md: could not locate the stale bullet" "$(extract_bullet_raw "${ROUTING}" '- `stale`:')" || true
  if [[ -n "${STALE_BULLET:-}" ]]; then
    assert_contains "${STALE_BULLET}" "${MECHANISM}" "the stale prompt must use the abstract user-input wording"
    assert_contains "${STALE_BULLET}" 'check the two stops below first' "stale must check both stops before asking"
    assert_contains "${STALE_BULLET}" $'Continue takes the table\'s route' "stale Continue must take the table's attach route"
    assert_contains "${STALE_BULLET}" 'a stage with no attach route takes the normal flow' \
      "stale Continue on a stage with no attach route must take the normal flow"
    assert_contains "${STALE_BULLET}" 'Re-plan ends the run and tells the user to run `/plan` again, with no `cenci pipeline reset` at that point' \
      "stale Re-plan must stop and point to /plan with no reset"
  fi

  require_extract MULTIPLE_SAVED_BULLET "codex.md: could not locate the saved-plan multiple bullet" \
    "$(extract_bullet_raw "${ROUTING}" '- `multiple` on a saved-plan apply:')" || true
  if [[ -n "${MULTIPLE_SAVED_BULLET:-}" ]]; then
    assert_contains "${MULTIPLE_SAVED_BULLET}" "${MECHANISM}" "the multiple prompt must use the abstract user-input wording"
    assert_contains "${MULTIPLE_SAVED_BULLET}" 'ask which file to use, or whether to re-plan' \
      "the saved-plan multiple prompt must ask which file to use or whether to re-plan"
    assert_contains "${MULTIPLE_SAVED_BULLET}" 'A picked file is handled exactly like `stale`' "a picked multiple file must be treated like stale"
  fi

  require_extract MULTIPLE_NEW_BULLET "codex.md: could not locate the new-plan multiple bullet" \
    "$(extract_bullet_raw "${ROUTING}" '- `multiple` on a new-plan apply:')" || true
  if [[ -n "${MULTIPLE_NEW_BULLET:-}" ]]; then
    assert_contains "${MULTIPLE_NEW_BULLET}" 'stop before any mutation' "a new-plan multiple must stop before any mutation"
    assert_contains "${MULTIPLE_NEW_BULLET}" 'name every file in `artifacts[]`' "a new-plan multiple must name every file in artifacts[]"
    assert_contains "${MULTIPLE_NEW_BULLET}" 'remove the outdated ones and re-run the apply' "a new-plan multiple must ask to remove outdated files and re-run"
    assert_contains "${MULTIPLE_NEW_BULLET}" 'The `multiple` prompt above applies only to a saved-plan apply.' "the multiple prompt must be scoped to saved-plan applies"
  fi

  require_extract AWAITING_BULLET "codex.md: could not locate the awaiting-input bullet" "$(extract_bullet_raw "${ROUTING}" '- `awaiting-input`:')" || true
  if [[ -n "${AWAITING_BULLET:-}" ]]; then
    assert_contains "${AWAITING_BULLET}" 'stop before any mutation' "awaiting-input must stop before any mutation"
    assert_contains "${AWAITING_BULLET}" 'unanswered draft' "awaiting-input must say the saved plan is an unanswered draft"
    assert_contains "${AWAITING_BULLET}" 'Claude `/cenci:implement <id>`' "awaiting-input must point to Claude /cenci:implement"
    assert_contains "${AWAITING_BULLET}" 're-running `/plan`' "awaiting-input must offer re-running /plan"
  fi

  require_extract NONE_BULLET "codex.md: could not locate the none bullet" "$(extract_bullet_raw "${ROUTING}" '- `none` on a saved-plan apply:')" || true
  if [[ -n "${NONE_BULLET:-}" ]]; then
    assert_contains "${NONE_BULLET}" 'stop and tell the user to run `/plan`' "a saved-plan none must stop and point to /plan"
    assert_contains "${NONE_BULLET}" 'never improvise a plan' "a saved-plan none must never improvise a plan"
  fi

  require_extract EXIT1_BULLET "codex.md: could not locate the exit-1 bullet" \
    "$(extract_bullet_raw "${ROUTING}" '- An empty or unrecognized decision (exit 1)')" || true
  if [[ -n "${EXIT1_BULLET:-}" ]]; then
    assert_contains "${EXIT1_BULLET}" 'show `errors[0]` and stop before any mutation' "an exit-1 decision must show errors[0] and stop before any mutation"
  fi

  assert_contains "${ROUTING}" "${PR_STOP_TEXT}" "the finalized+prUrl stop line must be pinned"
  assert_contains "${ROUTING}" 'the plan-file archive and the `cenci babysit` arm may not have run for that PR' \
    "the PR stop must note the plan-file archive and babysit arm may not have run"
  assert_contains "${ROUTING}" "${UNKNOWN_STOP_TEXT}" "the unknown-stage stop line must be pinned"
  assert_contains "${ROUTING}" 'then every `warnings[]` entry' "the unknown-stage stop must print every warnings[] entry"
  assert_contains "${ROUTING}" 'a missing or unreadable stage renders as `(missing)`' "a missing stage must render as (missing)"
  assert_contains "${ROUTING}" 'Both stops run before the persist step and touch nothing: no transition, no checkpoint, no worktree call and no label write.' \
    "both stops must touch nothing"
  assert_contains "${ROUTING}" 'Attach routes apply only when `pipeline.worktreeRegistered` is true' \
    "attach routes must apply only when worktreeRegistered is true"
  assert_contains "${ROUTING}" 'never stash, reset, check out over or discard' \
    "the crashed run's uncommitted changes must never be stashed or discarded"
fi

# =====================================================================
# 7. AC8-AC10: worktree step sites (persist / checkpoint / worktree / baseline)
# =====================================================================

if [[ -n "${PERSIST_PARA:-}" ]]; then
  assert_order "codex.md persist/worktree step order" "${PERSIST_PARA}" \
    "${PERSIST_ANCHOR}" \
    'cenci pipeline prepare <id>' 'cenci pipeline plan <id>' 'cenci pipeline plan <id> --approve' 'cenci pipeline artifact <id> --plan' \
    '`checkpoint.mjs advance`' 'cenci pipeline worktree <id> --slug <slug>' 'cenci pipeline execute <id>' 'run-gate.sh'
  assert_contains "${PERSIST_PARA}" 'cenci pipeline artifact <id> --plan <plan-path>` in that order, where `<plan-path>` is the `.plans/<id>-<slug>.md` just persisted on a new-plan apply, or the saved plan file from `artifacts[0]` of the `plan-check` result' \
    "the persist step must pass the plan path to artifact --plan for both apply kinds"
  assert_contains "${PERSIST_PARA}" 'a new-plan apply only' "the persist step must be scoped to a new-plan apply"
  assert_contains "${PERSIST_PARA}" 'stop before the checkpoint/label' "the heading hard stop must stay before the checkpoint/label mutations"
  assert_contains "${PERSIST_PARA}" '`checkpoint.mjs advance` when a checkpoint file already exists on an attach route, never `init`' \
    "an existing checkpoint on an attach route must be advanced, never re-created with init"
  assert_contains "${PERSIST_PARA}" '`checkpoint.mjs init` otherwise' "a missing checkpoint must be created with init"
  assert_contains "${PERSIST_PARA}" 'cenci pipeline worktree <id> --attach <worktreePath>' "an attach route must run worktree --attach <worktreePath>"
  assert_contains "${PERSIST_PARA}" 'which applies only when `pipeline.worktreeRegistered` is true' "--attach must apply only when worktreeRegistered is true"
  assert_contains "${PERSIST_PARA}" 'Resuming #<id> from stage <stage> in <worktreePath> (branch <branch>, <n> commits ahead of main, <m> uncommitted files)' \
    "the exact resume line must be pinned"
  assert_contains "${PERSIST_PARA}" '`git -C <worktreePath> rev-list --count main..HEAD`' "<n> must come from rev-list --count main..HEAD"
  assert_contains "${PERSIST_PARA}" 'the line count of `git -C <worktreePath> status --short`' "<m> must come from the status --short line count"
  assert_contains "${PERSIST_PARA}" 'if either count command fails (for example there is no local `main`), stop the resume with the git error and never guess a count' \
    "a failing count command must stop the resume without guessing"
  assert_contains "${PERSIST_PARA}" 'Recorded worktree <worktreePath> is gone — creating a fresh one' "the gone-directory line must be pinned"
  assert_contains "${PERSIST_PARA}" 'use no resume brief' "a gone directory must run with no resume brief"
  assert_contains "${PERSIST_PARA}" '`ErrWorktreeExists`' "a leftover directory or branch must be handled via ErrWorktreeExists"
  assert_contains "${PERSIST_PARA}" 'never remove the directory or delete the branch' "a leftover directory or branch must never be removed"
  assert_contains "${PERSIST_PARA}" 'never stashed, reset, checked out over or discarded by the root agent or any worker' \
    "the crashed run's uncommitted changes must never be stashed, reset or discarded"
  assert_contains "${PERSIST_PARA}" 'run the baseline gate only when `git -C <worktreePath> status --short` is empty' \
    "the baseline gate must run on an attach route only when status --short is empty"
  assert_contains "${PERSIST_PARA}" 'Baseline gate: skipped — resumed worktree has <m> uncommitted files from the crashed run' \
    "the skipped-baseline line must be pinned"
fi

# =====================================================================
# 8. AC11/AC12: Implementation dispatch -- resume brief, clean line, skip list
# =====================================================================

RESUME_BRIEF_BLOCK="$(first_fence_raw "$(extract_h2_raw "${PHASE3}" '## Resume Brief')")"
if [[ -z "${RESUME_BRIEF_BLOCK}" ]]; then
  fail "phase-3-test-red.md: could not extract the fenced resume brief from '## Resume Brief' (fail closed)"
else
  assert_contains "${RESUME_BRIEF_BLOCK}" 'Resume brief — an earlier run of this ticket crashed partway through implementation' \
    "the extracted phase-3 resume brief must start with its fixed first line (extractor sanity)"
  assert_contains "${RESUME_BRIEF_BLOCK}" 'changes, and do not stage or commit them.' \
    "the extracted phase-3 resume brief must end with its fixed last line (extractor sanity)"
  assert_contains "${CODEX}" "${RESUME_BRIEF_BLOCK}" \
    "codex.md must carry the phase-3 resume brief byte-for-byte"
fi

if [[ -n "${DISPATCH:-}" ]]; then
  assert_contains "${DISPATCH}" "${RESUME_BRIEF_BLOCK}" "the resume brief must sit inside the Implementation dispatch block"
  assert_contains "${DISPATCH}" 'every implementer delegation receives the resume brief: the single implementer, every parallel or sequential lane and the compact implementer' \
    "the resume brief must go to the single implementer, every lane and the compact implementer"
  assert_contains "${DISPATCH}" '`git -C <abs-worktree-path> status --short`, `git -C <abs-worktree-path> diff --stat HEAD` and `git -C <abs-worktree-path> log --oneline main..HEAD`' \
    "the brief must be filled with status --short, diff --stat HEAD and log --oneline main..HEAD"
  assert_contains "${DISPATCH}" 'Resumed worktree is clean — running implementation with the standard brief' \
    "the clean-worktree line must be pinned"
  assert_contains "${DISPATCH}" 'on every attach route (this replaces the review-entry route for `reviewed` and `finalized` without a `prUrl`)' \
    "the clean-worktree line must apply on every attach route and replace the review-entry route"
  assert_contains "${DISPATCH}" 'If any git read in this block (`status --short`, `log --oneline main..HEAD` or `diff --stat HEAD`) fails, stop with the git error: never treat missing output as a clean worktree and never paste git error text into the brief.' \
    "a failing dispatch-time git read must stop the run, never read as clean or reach the brief"
  DIRTY_DEFINITION='A resumed worktree is clean only with 0 commits ahead of main and 0 uncommitted files; otherwise (any commit ahead of main or any uncommitted file) it is dirty.'
  assert_contains "${DISPATCH}" "${DIRTY_DEFINITION}" \
    "dirty worktree must be defined: clean only with 0 commits ahead and 0 uncommitted files, so commits ahead with a clean status is dirty"
  assert_order "codex.md dirty-worktree definition precedes both rules that use it" "${DISPATCH}" \
    "${DIRTY_DEFINITION}" \
    'On the `reviewed` and `finalized`-without-`prUrl` routes with a dirty worktree' \
    'On the `plan_approved` and `executed` routes with a dirty worktree'
  assert_contains "${DISPATCH}" 'A crash-resume brief is not a second resume for the context-budget split signal' \
    "a crash-resume brief must not count as a second resume"
  assert_contains "${DISPATCH}" 'skip this Implementation dispatch, the lane verification barrier, the refactor pass, the reuse check and Visual Verification, and continue at the acceptance review' \
    "the reviewed/finalized routes must skip dispatch, barrier, refactor pass and reuse check"
  assert_contains "${DISPATCH}" 'expected on a resume, not a red-phase violation' \
    "pre-existing passing tests must be expected on a resume"
fi

# =====================================================================
# 9. AC1/AC2 step sites: review, finalize, PR artifact (+ already-exists)
# =====================================================================

REVIEW_REGION="$(between_raw "${CODEX}" 'Then, before any code review, run the acceptance review' 'Then run the configured reviews')"
if [[ -z "${REVIEW_REGION}" ]]; then
  fail "codex.md: could not locate the acceptance-review step"
else
  assert_order "codex.md acceptance-review entry" "${REVIEW_REGION}" 'cenci pipeline review <id>' 'delegate the `acceptance-reviewer`'
fi

FINALIZE_REGION="$(between_raw "${CODEX}" 'Before rewriting commits' 'Then commit, push')"
if [[ -z "${FINALIZE_REGION}" ]]; then
  fail "codex.md: could not locate the rebase/commit step"
else
  assert_order "codex.md finalize before rebase" "${FINALIZE_REGION}" 'cenci pipeline finalize <id>' 'rebase-remote-base.sh'
fi

PR_REGION="$(between_raw "${CODEX}" 'open the PR with a `## Size` section' 'archive the consumed plan file instead of deleting it')"
if [[ -z "${PR_REGION}" ]]; then
  fail "codex.md: could not locate the PR creation step"
else
  assert_order "codex.md PR artifact after creation" "${PR_REGION}" \
    'already exists' 'gh pr view <branch> --json url,number' 'treat it as a successful create' \
    'After a successful PR creation (never on a failed `gh pr create`' 'cenci pipeline artifact <id> --pr <pr-url> --pr-number <n>'
  assert_contains "${PR_REGION}" 'gh pr view <branch> --json url,number,state` and treat it as a successful create only when it succeeds with a non-empty url and number and state `OPEN`; otherwise stop and report the original `gh pr create` error.' \
    "the already-exists recovery must verify a successful gh pr view with url, number and an OPEN state, else report the original error"
  assert_contains "${PR_REGION}" '(if that call fails after the PR was created, the stop message prints the PR URL)' \
    "a failing PR artifact call must print the PR URL in its stop message"
  assert_contains "${PR_REGION}" 'a recovered "already exists" counts as a success' \
    "the already-exists recovery must count as a successful create for the artifact call"
fi

assert_order "codex.md review/finalize/PR step order" "${CODEX}" \
  'Then, before any code review, run the acceptance review' 'Before rewriting commits' \
  'After a successful PR creation (never on a failed `gh pr create`'

# =====================================================================
# 10. AC16: doc sentences
# =====================================================================

assert_contains "${DOCS_CODEX}" 'Codex implement records `cenci pipeline` stages and resumes a crashed `apply` from its recorded stage' \
  "docs/codex.md must say Codex implement records pipeline stages and resumes a crashed apply from its recorded stage"
require_extract README_RESUME "README.md: could not locate the '### Resuming a crashed run' section" \
  "$(extract_block_raw "${README}" '### Resuming a crashed run' '')" || true
if [[ -n "${README_RESUME:-}" ]]; then
  assert_contains "${README_RESUME}" '`cenci run implement apply <ticket> --agent codex` follows the same table' \
    "README.md must say the Codex apply follows the same table"
fi

# =====================================================================
# 11. Non-vacuity self-tests: the extractors and checks must catch an
#     injected violation (a pass against the real docs proves nothing if the
#     machinery cannot fail).
# =====================================================================

SYNTH_ORDER=$'**Pipeline lifecycle (ticket mode).**\n1. `cenci pipeline execute <id>`\n2. `cenci pipeline prepare <id>`\n'
if calls_in_order "${SYNTH_ORDER}" 'cenci pipeline prepare <id>' 'cenci pipeline execute <id>'; then
  fail "self-test: calls_in_order accepted an injected reorder"
fi
if calls_in_order "${SYNTH_ORDER}" 'cenci pipeline prepare <id>' 'cenci pipeline review <id>'; then
  fail "self-test: calls_in_order accepted a missing call"
fi
if ! calls_in_order "${SYNTH_ORDER}" 'cenci pipeline execute <id>' 'cenci pipeline prepare <id>'; then
  fail "self-test: calls_in_order rejected a correctly ordered pair"
fi
if [[ "$(offset_of 'abc' '')" != "-1" || "$(offset_of 'abc' 'zz')" != "-1" ]]; then
  fail "self-test: offset_of must return -1 for an empty or absent needle"
fi
if [[ "$(count_of $'aa aa\naa' 'aa')" != "3" ]]; then
  fail "self-test: count_of must count occurrences, not lines"
fi

SYNTH_TABLE=$'**Resume routing (apply, ticket mode).**\n\n| `pipeline.stage` | Route |\n|---|---|\n| `new` | x |\n| `executed` | y |\n'
SYNTH_MISSING="$(stages_without_row "${SYNTH_TABLE}" $'new\nexecuted\nreviewed')"
if [[ "${SYNTH_MISSING}" != "reviewed" ]]; then
  fail "self-test: stages_without_row should report exactly 'reviewed' for a table lacking its row (got: ${SYNTH_MISSING})"
fi

SYNTH_FENCED=$'text\n```\n**Resume routing (apply, ticket mode).**\nIn ticket mode, `/plan` also runs x\n```\nafter'
if [[ -n "$(extract_block_raw "${SYNTH_FENCED}" "${ROUTING_LEAD}" '')" ]]; then
  fail "self-test: extract_block_raw started a block on a fenced lead-in"
fi
if [[ -n "$(extract_from_prefix_raw "${SYNTH_FENCED}" "${PLAN_SENTENCE_PREFIX}" '')" ]]; then
  fail "self-test: extract_from_prefix_raw started a block on a fenced line"
fi
SYNTH_STOP=$'**Resume routing (apply, ticket mode).**\nbody\n**Pipeline lifecycle (ticket mode).**\nafter'
if [[ "$(extract_block_raw "${SYNTH_STOP}" "${ROUTING_LEAD}" "${LIFECYCLE_LEAD}")" != $'**Resume routing (apply, ticket mode).**\nbody' ]]; then
  fail "self-test: extract_block_raw did not stop at the prefix line"
fi
SYNTH_BULLETS=$'- `stale`: a\ncontinued\n- `multiple`: b\n\n- `other`: c'
if [[ "$(extract_bullet_raw "${SYNTH_BULLETS}" '- `stale`:')" != $'- `stale`: a\ncontinued' ]]; then
  fail "self-test: extract_bullet_raw did not stop at the next bullet"
fi
if [[ "$(extract_bullet_raw "${SYNTH_BULLETS}" '- `multiple`:')" != '- `multiple`: b' ]]; then
  fail "self-test: extract_bullet_raw did not stop at a blank line"
fi
SYNTH_FENCE_BODY=$'intro\n```text\nline one\nline two\n```\n```\nsecond\n```'
if [[ "$(first_fence_raw "${SYNTH_FENCE_BODY}")" != $'line one\nline two' ]]; then
  fail "self-test: first_fence_raw must return only the first fenced block"
fi
if [[ -n "$(between_raw 'a START b' 'START' 'END')" || -n "$(between_raw 'a END b START' 'START' 'END')" ]]; then
  fail "self-test: between_raw must return nothing when the end anchor is absent or precedes the start"
fi

SYNTH_PLAN=$'In ticket mode, `/plan` also runs the read-only `cenci pipeline plan-check <id>` then `cenci pipeline prepare <id>`.\n`/plan` makes no `cenci pipeline` call that writes anything.'
if [[ -z "$(write_verbs_in "${SYNTH_PLAN}")" ]]; then
  fail "self-test: write_verbs_in did not flag an injected 'cenci pipeline prepare' in the /plan sentence"
fi
if [[ -n "$(write_verbs_in $'only the read-only `cenci pipeline plan-check <id>` and a `cenci pipeline` call')" ]]; then
  fail "self-test: write_verbs_in false-positived on plan-check and the prose 'call'"
fi
if [[ -z "$(write_verbs_in $'a new verb: `cenci pipeline frobnicate <id>`')" ]]; then
  fail "self-test: write_verbs_in did not flag an unlisted verb (allowlist)"
fi

if [[ -z "$(bare_plan_flags $'run `cenci pipeline artifact <id> --plan` now')" ]]; then
  fail "self-test: bare_plan_flags did not flag a bare artifact --plan"
fi
if [[ -n "$(bare_plan_flags $'run `cenci pipeline artifact <id> --plan <plan-path>` now')" ]]; then
  fail "self-test: bare_plan_flags false-positived on artifact --plan <plan-path>"
fi

SYNTH_CALLS_TEXT=$'a `cenci pipeline plan <id> --approve` b'
if [[ "$(count_of "$(strip_longer_calls "${SYNTH_CALLS_TEXT}" 'cenci pipeline plan <id>' 'cenci pipeline plan <id>' 'cenci pipeline plan <id> --approve')" 'cenci pipeline plan <id>')" != "0" ]]; then
  fail "self-test: strip_longer_calls let 'plan <id>' be satisfied by 'plan <id> --approve'"
fi

SYNTH_GO=$'const (\n\tStageNew Stage = "new"\n\tStageWaitingForInput Stage = "waiting_for_input"\n\tStageOdd Stage = "Odd Value"\n)\n// Stage = "commented"\n'
if [[ "$(stage_values "${SYNTH_GO}" | tr '\n' ' ')" != "new waiting_for_input " ]]; then
  fail "self-test: stage_values must read declared lowercase values only and ignore a comment (got: $(stage_values "${SYNTH_GO}" | tr '\n' ' '))"
fi
if [[ "$(stage_decl_count "${SYNTH_GO}")" != "3" ]]; then
  fail "self-test: stage_decl_count must count every Stage declaration, including a value stage_values cannot read"
fi
if [[ -n "$(stage_values $'no constants here')" ]]; then
  fail "self-test: stage_values must be empty when there are no declarations"
fi

echo "implement-codex-resume-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
