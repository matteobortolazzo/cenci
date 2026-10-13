#!/usr/bin/env bash
# Contract test for ticket #925 -- every plan-persisting branch of
# `flow/skills/implement/phases/phase-1-plan.md` must issue its pipeline calls
# in the order
#
#   optional `planComment` -> `cenci pipeline plan <id>`
#     -> `cenci pipeline label <id> --transition planned [--trivial]`
#     -> `cenci pipeline artifact <id> --plan .plans/<filename>`
#
# because the `planned` label gate (watch/internal/pipeline/labels.go) requires
# the persisted stage to be at or past `waiting_for_plan_approval`, which only
# the `plan` call advances to. Called in the inverted order the label call
# hard-fails with `ErrWrongStageForLabel` and the ticket is stranded at
# `Refined` with a finished plan on disk. `## Resume From Draft` step 7 is the
# reference model (already correct); `## Persist the Plan`, `## Trivial Fast
# Path` and `## Lean Approval Path` had the inversion.
#
# Pinned here, per region (extracted fence-aware):
#   - `## Persist the Plan` (scoped to `### Mark the ticket Planned`),
#     `## Trivial Fast Path`, `## Lean Approval Path`, `## Resume From Draft`:
#     strictly increasing line numbers plan < label < artifact (Persist also
#     gh-issue-comment < plan; Trivial/Lean also artifact < [status summary <]
#     continuation). A missing marker FAILS the suite.
#   - Trivial/Lean: the continuation is the last numbered step and no longer
#     bundles the `plan` invocation; the Trivial step-3 hard-stop list and the
#     step-4 planComment sentence name the renumbered steps; the "plan makes no
#     gh call" freshness clause is stated.
#   - Step-scoped phrase pins (PINS array; text extracted per numbered step):
#     step 5 of Trivial/Lean carries the verify-then-stop, **STOP**, and the
#     #926 "tracked, not yet shipped / label restored only by reconciler or by
#     hand" wording; Trivial step 7's artifact call is non-blocking; the
#     hasPlanFile wording in Trivial step 8 / Lean step 9; Persist's
#     skip-on-`plan`-error clause; Persist's step references to Trivial step 8 /
#     Lean step 9; `## New Plan` step 2's provenance phrase. Only those phrases
#     are pinned -- the prose around them is not.
#   - `## Pipeline: Plan Stage` states the invariant instead of "at the end of
#     that branch"; `## Existing Plan` states the re-plan re-entry.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3: the substring
# `cenci pipeline plan <id>` recurs in `## Persist the Plan` well above the
# label call (the `approval` paragraph's `cenci pipeline plan <id> --approve`),
# so a substring marker would pass without testing anything there. The `plan`
# and `label` markers are therefore EXACT whole-line matches of their fenced
# code lines (leading whitespace allowed): a call buried in prose never counts.
#
# Non-vacuity, per rule 29: the checker is a pure function (no fail() inside a
# command substitution, rule 30), so two in-memory self-tests exercise it:
# (a) the plan fenced line is stripped from the real doc and every region must
# report the marker missing; (b) a synthetic doc with the four headings in
# inverted order and fenced `## ` decoys must report an ordering violation per
# region (and a correctly ordered twin must report none); (c) each PINS phrase is
# removed in memory and must be reported missing; (d) each step-5 phrase is
# removed from step 5 alone and must still be reported (scoped to the plan step).
#
# Idiom mirrors flow/tests/plan-persist-sections-contract.test.sh and
# flow/tests/dispatch-resume-contract.test.sh. Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` find -- no registration needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "plan-label-ordering-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "plan-label-ordering-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> -- pure extraction, no fail() side
  # effect: safe inside a $(...) command substitution.
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- nameref wrapper assigning the
# real committed file's content, or failing closed (a missing file must never
# masquerade as empty content). Must NOT be invoked via $(...).
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

# assert_count_at_least <content> <substring> <min> <label> -- occurrence
# count via grep -oF | wc -l (never grep -c, which counts lines; rule 3(4)).
assert_count_at_least() {
  local content="$1" pattern="$2" min="$3" label="$4" count
  [[ -n "${pattern}" ]] || { fail "${label}: empty pattern (test bug)"; return; }
  count="$(grep -oF -- "${pattern}" <<<"${content}" | wc -l | tr -d '[:space:]')"
  [[ "${count}" -ge "${min}" ]] || fail "${label}: expected at least ${min} occurrence(s), found ${count}: [${pattern}]"
}

# extract_h2_section <content> <heading-line> -- the section from the exact
# (unfenced) heading line through the next real "## " heading or EOF.
# Fence-aware: a "## " line inside a column-0 ``` block neither starts nor ends
# a section. Pure, no fail() side effect.
extract_h2_section() {
  awk -v h="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    $0 == h && !on && !infence { on=1; print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_h3_subsection <section-content> <heading-prefix> -- from the first
# unfenced line starting with the prefix through the next real "## " / "### "
# heading or the end of the given content. Fence-aware, pure.
extract_h3_subsection() {
  awk -v p="$2" '
    /^```/ { infence = !infence; if (on) print; next }
    !on && !infence && index($0, p) == 1 { on=1; print; next }
    on && !infence && (/^## / || /^### /) { exit }
    on { print }
  ' <<<"$1"
}

# first_line_of <content> <marker> -- 1-based line of the first substring
# occurrence, or empty. Pure.
first_line_of() {
  local content="$1" marker="$2"
  grep -n -m1 -F -- "${marker}" <<<"${content}" | cut -d: -f1
}

# ere_escape <literal> -- backslash-escape ERE metacharacters. Pure.
ere_escape() {
  printf '%s' "$1" | sed 's/[][\.*^$(){}?+|]/\\&/g'
}

# first_exact_line_of <content> <marker> -- 1-based line of the first line that
# is EXACTLY the marker (leading whitespace allowed), or empty. Pure.
first_exact_line_of() {
  local content="$1" marker="$2" escaped
  escaped="$(ere_escape "${marker}")"
  grep -n -m1 -x -E -- "[[:space:]]*${escaped}" <<<"${content}" | cut -d: -f1
}

# drop_exact_lines <content> <marker> -- content minus every exact-marker line.
drop_exact_lines() {
  local content="$1" marker="$2" escaped
  escaped="$(ere_escape "${marker}")"
  grep -v -x -E -- "[[:space:]]*${escaped}" <<<"${content}"
}

# order_problems <region> <kind:marker>... -- pure checker. Each spec is
# `exact:<marker>` (whole-line match) or `sub:<marker>` (substring). Prints one
# problem per line: "marker not found: [...]" for an absent marker, "ordering
# violated: ..." when a marker's first line is not strictly after the previous
# found marker's. Prints nothing when every marker is present and in order.
# No fail() call: safe inside $(...) and reusable by the self-tests.
order_problems() {
  local region="$1"
  shift
  local spec kind marker line prev=0 prev_marker="" missing=0
  for spec in "$@"; do
    kind="${spec%%:*}"
    marker="${spec#*:}"
    if [[ -z "${marker}" ]]; then
      echo "empty marker (test bug)"
      continue
    fi
    case "${kind}" in
      exact) line="$(first_exact_line_of "${region}" "${marker}")" ;;
      sub) line="$(first_line_of "${region}" "${marker}")" ;;
      *) echo "unknown marker kind [${kind}] (test bug)"; continue ;;
    esac
    if [[ -z "${line}" ]]; then
      echo "marker not found: [${marker}]"
      missing=1
      continue
    fi
    if [[ "${missing}" -eq 0 && "${line}" -le "${prev}" ]]; then
      echo "ordering violated: [${marker}] at line ${line} is not after [${prev_marker}] at line ${prev}"
    fi
    prev="${line}"
    prev_marker="${marker}"
  done
}

# --- Markers -------------------------------------------------------------------

M_PLAN='cenci pipeline plan <id>'
M_LABEL='cenci pipeline label <id> --transition planned'
M_LABEL_TRIVIAL='cenci pipeline label <id> --transition planned --trivial'
M_ARTIFACT='cenci pipeline artifact <id> --plan'
M_GH_COMMENT='gh issue comment <number> --repo'
M_CONTINUE='Set `hasPlanFile = true` and continue into Phase 2'
M_LEAN_SUMMARY='Emit a 3-5 line status summary'
M_FORMER_PLAN_STEP='Invoke `cenci pipeline plan <id>` now'
M_VERIFY='Verify this call succeeded before continuing'
M_NO_GH='makes no `gh` call'
M_926='#926 tracks the reconciler backstop'
M_STEP4_ORDER='**before** the `cenci pipeline plan` call in step 5 and the label call in step 6'
M_STALE_STEP4='**before** the label call in the next step'
M_STALE_END='at the *end* of that branch'
M_NEVER_START='never at planning start'
M_INV_ORDER='`cenci pipeline plan <id>` must precede any `cenci pipeline label <id> --transition planned` in the same branch'
M_INV_GATE='because the label gate requires `waiting_for_plan_approval`'
M_INV_ERR='`ErrWrongStageForLabel`'
M_REPLAN_REENTRY='re-enters `## Persist the Plan` with the fixed order'
M_REPLAN_NOOP='past-minimum success'

# region_of <content> <persist|trivial|lean|resume> -- pure region extraction.
region_of() {
  case "$2" in
    persist) extract_h3_subsection "$(extract_h2_section "$1" '## Persist the Plan')" '### Mark the ticket' ;;
    trivial) extract_h2_section "$1" '## Trivial Fast Path' ;;
    lean) extract_h2_section "$1" '## Lean Approval Path' ;;
    resume) extract_h2_section "$1" '## Resume From Draft' ;;
    *) return 1 ;;
  esac
}

# region_problems <persist|trivial|lean|resume> <region> -- the per-region
# marker sequence, pure.
region_problems() {
  case "$1" in
    persist) order_problems "$2" "sub:${M_GH_COMMENT}" "exact:${M_PLAN}" "exact:${M_LABEL}" "sub:${M_ARTIFACT}" ;;
    trivial) order_problems "$2" "exact:${M_PLAN}" "exact:${M_LABEL_TRIVIAL}" "sub:${M_ARTIFACT}" "sub:${M_CONTINUE}" ;;
    lean) order_problems "$2" "exact:${M_PLAN}" "exact:${M_LABEL_TRIVIAL}" "sub:${M_ARTIFACT}" "sub:${M_LEAN_SUMMARY}" "sub:${M_CONTINUE}" ;;
    resume) order_problems "$2" "exact:${M_PLAN}" "exact:${M_LABEL}" "sub:${M_ARTIFACT}" ;;
    *) echo "unknown region [$1] (test bug)" ;;
  esac
}

REGIONS=(persist trivial lean resume)

# step_text <region> <n> -- the text of column-0 numbered step <n>: from its
# "<n>. " line up to (not including) the "<n+1>. " line, or the end of the given
# region when <n> is the last step. Fenced code in these regions is indented, so
# it never fakes a column-0 step line. Pure, no fail() side effect.
step_text() {
  awk -v n="$2" -v nx="$(($2 + 1))" '
    index($0, n ". ") == 1 && !on { on=1; print; next }
    on && index($0, nx ". ") == 1 { exit }
    on { print }
  ' <<<"$1"
}

# Step-scoped pins: "scope|step|phrase". scope is persist|trivial|lean|newplan;
# step 0 pins the phrase anywhere in the scope's region, N pins it inside
# numbered step N of that region only.
PINS=(
  "trivial|5|${M_VERIFY}"
  "trivial|5|**STOP**"
  "trivial|5|${M_926}"
  "trivial|5|tracked, not yet shipped"
  "trivial|5|never applies the \`Planned\` label"
  "lean|5|${M_VERIFY}"
  "lean|5|**STOP**"
  "lean|5|${M_926}"
  "lean|5|tracked, not yet shipped"
  "lean|5|never applies the \`Planned\` label"
  "trivial|7|**not** a hard stop"
  "trivial|7|the \`Planned\` label is already applied"
  "trivial|8|only after step 5's \`plan\` call and step 6's \`--trivial\` label call succeeded"
  "trivial|8|artifact call is non-blocking"
  "lean|9|only after step 5's \`plan\` call and step 6's \`--trivial\` label call succeeded"
  "lean|9|artifact call is non-blocking"
  "persist|0|skip the label swap and the artifact call below"
  "persist|0|Trivial Fast Path's step 8"
  "persist|0|Lean Approval Path's step 9"
  "newplan|2|the \`cenci pipeline plan <id>\` call that \`## Persist the Plan\`'s \`Mark the ticket Planned\` step makes before its label call"
)

# pin_scope_text <content> <scope> <step> -- the text a pin is checked against.
pin_scope_text() {
  local region
  if [[ "$2" == "newplan" ]]; then
    region="$(extract_h2_section "$1" '## New Plan')"
  else
    region="$(region_of "$1" "$2")"
  fi
  if [[ "$3" -eq 0 ]]; then
    printf '%s\n' "${region}"
  else
    step_text "${region}" "$3"
  fi
}

# pin_problems <content> -- pure checker: one "pin missing: <scope>/<step>:
# [<phrase>]" line per pin whose phrase is absent from its scoped text.
pin_problems() {
  local spec scope rest step phrase text
  for spec in "${PINS[@]}"; do
    scope="${spec%%|*}"
    rest="${spec#*|}"
    step="${rest%%|*}"
    phrase="${rest#*|}"
    text="$(pin_scope_text "$1" "${scope}" "${step}")"
    [[ "${text}" == *"${phrase}"* ]] || echo "pin missing: ${scope}/${step}: [${phrase}]"
  done
}

# last_numbered_step <region> -- the last column-0 "N. " line. Pure.
last_numbered_step() {
  grep -E '^[0-9]+\. ' <<<"$1" | tail -n 1
}

# --- Real doc ------------------------------------------------------------------

FILE1="skills/implement/phases/phase-1-plan.md"
if require_doc CONTENT1 "${FILE1}"; then
  declare -A REGION_TEXT
  for name in "${REGIONS[@]}"; do
    REGION_TEXT["${name}"]="$(region_of "${CONTENT1}" "${name}")"
    if [[ -z "${REGION_TEXT[${name}]}" ]]; then
      fail "${FILE1}: could not locate the ${name} region (extractor returned empty)"
    fi
  done

  # --- Per-region ordering ------------------------------------------------------
  for name in "${REGIONS[@]}"; do
    [[ -n "${REGION_TEXT[${name}]}" ]] || continue
    problems="$(region_problems "${name}" "${REGION_TEXT[${name}]}")"
    [[ -z "${problems}" ]] || fail "${FILE1} (${name} ordering): ${problems//$'\n'/; }"
  done

  # --- Over-capture guards (each region must stop at its own boundary) ---------
  assert_not_contains "${REGION_TEXT[trivial]}" 'Entry conditions (all must hold' "${FILE1} (trivial over-capture guard)"
  assert_not_contains "${REGION_TEXT[lean]}" 'openssl rand -hex 16' "${FILE1} (lean over-capture guard)"
  assert_not_contains "${REGION_TEXT[resume]}" 'Mandatory stops:' "${FILE1} (resume over-capture guard)"
  assert_not_contains "${REGION_TEXT[persist]}" '`approval` records' "${FILE1} (persist over-capture guard)"

  # --- Persist, Trivial, Lean: plan makes no gh call (label stays the last ticket edit) -
  for name in persist trivial lean; do
    assert_contains "${REGION_TEXT[${name}]}" "${M_NO_GH}" "${FILE1} (${name}: freshness clause, plan makes no gh call)"
  done

  # --- Trivial, Lean: continuation is the last numbered step, no bundled plan call -
  for name in trivial lean; do
    LAST_STEP="$(last_numbered_step "${REGION_TEXT[${name}]}")"
    assert_contains "${LAST_STEP}" "${M_CONTINUE}" "${FILE1} (${name}: continuation is the last numbered step)"
    assert_not_contains "${LAST_STEP}" "${M_FORMER_PLAN_STEP}" "${FILE1} (${name}: continuation no longer bundles the plan invocation)"

    # The moved plan call carries its own restated verify-then-stop (alongside
    # the label call's), naming #926 as the backstop for the residual state.
    assert_count_at_least "${REGION_TEXT[${name}]}" "${M_VERIFY}" 2 "${FILE1} (${name}: verify-then-stop restated at the plan call and the label call)"
    assert_contains "${REGION_TEXT[${name}]}" "${M_926}" "${FILE1} (${name}: verify-then-stop names the #926 backstop)"

    # step 4's planComment ordering sentence resolves to the renumbered steps
    assert_contains "${REGION_TEXT[${name}]}" "${M_STEP4_ORDER}" "${FILE1} (${name}: step 4 planComment ordering sentence)"
    assert_not_contains "${REGION_TEXT[${name}]}" "${M_STALE_STEP4}" "${FILE1} (${name}: stale step 4 ordering sentence)"
  done

  # --- Trivial step 3 hard-stop list resolves to the renumbered steps -----------
  assert_contains "${REGION_TEXT[trivial]}" "not invoking step 5's \`cenci pipeline plan\` call" "${FILE1} (trivial: step 3 hard-stop names step 5's plan call)"
  assert_contains "${REGION_TEXT[trivial]}" "not running step 6's \`--trivial\` label call" "${FILE1} (trivial: step 3 hard-stop names step 6's label call)"
  assert_contains "${REGION_TEXT[trivial]}" 'not recording the artifact in step 7' "${FILE1} (trivial: step 3 hard-stop names step 7's artifact)"
  assert_not_contains "${REGION_TEXT[trivial]}" "step 5's \`--trivial\` label call" "${FILE1} (trivial: stale step 3 label reference)"

  # --- ## Pipeline: Plan Stage ---------------------------------------------------
  PLAN_STAGE="$(extract_h2_section "${CONTENT1}" '## Pipeline: Plan Stage')"
  if [[ -z "${PLAN_STAGE}" ]]; then
    fail "${FILE1}: could not locate '## Pipeline: Plan Stage' (extractor returned empty)"
  else
    assert_not_contains "${PLAN_STAGE}" "${M_STALE_END}" "${FILE1} (plan stage: no longer claims plan runs at the end of each branch)"
    assert_contains "${PLAN_STAGE}" "${M_INV_ORDER}" "${FILE1} (plan stage: states the plan-before-label invariant)"
    assert_contains "${PLAN_STAGE}" "${M_INV_GATE}" "${FILE1} (plan stage: names the label gate's stage requirement)"
    assert_contains "${PLAN_STAGE}" "${M_INV_ERR}" "${FILE1} (plan stage: names the failure of the inverted order)"
    assert_contains "${PLAN_STAGE}" "${M_NEVER_START}" "${FILE1} (plan stage: still forbids plan at planning start)"
  fi

  # --- ## Existing Plan: the re-plan branch re-enters Persist with the fixed order -
  EXISTING_PLAN="$(extract_h2_section "${CONTENT1}" '## Existing Plan')"
  if [[ -z "${EXISTING_PLAN}" ]]; then
    fail "${FILE1}: could not locate '## Existing Plan' (extractor returned empty)"
  else
    assert_contains "${EXISTING_PLAN}" "${M_REPLAN_REENTRY}" "${FILE1} (existing plan: re-plan re-entry states the fixed order)"
    assert_contains "${EXISTING_PLAN}" "${M_REPLAN_NOOP}" "${FILE1} (existing plan: past-stage label call is a past-minimum success)"
  fi

  # --- Step-scoped pins: verify-then-stop on the plan step, artifact non-blocking,
  # --- hasPlanFile wording, skip-on-plan-error clause, step references, provenance -
  for name in trivial lean; do
    assert_not_contains "${REGION_TEXT[${name}]}" 'every call above succeeded' "${FILE1} (${name}: stale hasPlanFile 'every call above' wording)"
  done
  pins_found="$(pin_problems "${CONTENT1}")"
  [[ -z "${pins_found}" ]] || fail "${FILE1} (step-scoped pins): ${pins_found//$'\n'/; }"

  # --- Self-test (c): every pin is real -- removing its phrase must report it ----
  for spec in "${PINS[@]}"; do
    rest="${spec#*|}"
    pin_label="${spec%%|*}/${rest%%|*}"
    pin_phrase="${rest#*|}"
    if [[ "${CONTENT1}" != *"${pin_phrase}"* ]]; then
      fail "${FILE1} (self-test c, ${pin_label}): phrase to remove is absent from the real doc (pin would be vacuous): [${pin_phrase}]"
      continue
    fi
    mutated="${CONTENT1//"${pin_phrase}"/}"
    mutated_problems="$(pin_problems "${mutated}")"
    [[ "${mutated_problems}" == *"pin missing: ${pin_label}: [${pin_phrase}]"* ]] || \
      fail "${FILE1} (self-test c, ${pin_label}): removing the phrase was not reported -- the pin is vacuous: [${pin_phrase}]"
  done

  # --- Self-test (d): step-5 extraction is scoped to step 5 --------------------
  # Strip each step-5 phrase from step 5 ALONE (step 6 keeps its own copy): the
  # pin must still report it, proving it is tied to the plan step and not to
  # whole-region occurrence counts.
  for name in trivial lean; do
    step5="$(step_text "${REGION_TEXT[${name}]}" 5)"
    for pin_phrase in "${M_VERIFY}" '**STOP**' "${M_926}"; do
      if [[ "${step5}" != *"${pin_phrase}"* ]]; then
        fail "${FILE1} (self-test d, ${name}): step 5 extraction lacks [${pin_phrase}]"
        continue
      fi
      step5_mutated="${step5//"${pin_phrase}"/}"
      mutated="${CONTENT1/"${step5}"/"${step5_mutated}"}"
      if [[ "${mutated}" == "${CONTENT1}" ]]; then
        fail "${FILE1} (self-test d, ${name}): step 5 text was not found in the doc for mutation"
        continue
      fi
      mutated_problems="$(pin_problems "${mutated}")"
      [[ "${mutated_problems}" == *"pin missing: ${name}/5: [${pin_phrase}]"* ]] || \
        fail "${FILE1} (self-test d, ${name}): stripping [${pin_phrase}] from step 5 alone was not reported"
    done
  done

  # --- Self-test (a): stripping the plan fenced line must surface in every region -
  STRIPPED="$(drop_exact_lines "${CONTENT1}" "${M_PLAN}")"
  if [[ "${STRIPPED}" == "${CONTENT1}" ]]; then
    fail "${FILE1} (self-test a): no exact '${M_PLAN}' line exists anywhere to strip (marker would be vacuous)"
  fi
  for name in "${REGIONS[@]}"; do
    stripped_problems="$(region_problems "${name}" "$(region_of "${STRIPPED}" "${name}")")"
    [[ "${stripped_problems}" == *"marker not found: [${M_PLAN}]"* ]] || \
      fail "${FILE1} (self-test a, ${name}): removing the plan fenced line did not report a missing plan marker -- the checker is vacuous here"
  done
else
  fail "${FILE1}: could not read file, skipping its assertions"
fi

# --- Self-test (b): inverted synthetic doc ---------------------------------------

# syn_block <command> -- an indented fenced bash block holding one command.
syn_block() { printf '%s\n' '   ```bash' "   $1" '   ```'; }

# synthetic_doc <correct|inverted> -- the four region headings in reverse of
# the real doc's order, each holding the pipeline calls in the given order,
# with column-0 fenced blocks containing "## " / "### " decoy headings (one of
# them a decoy "## Persist the Plan") that a fence-unaware extractor would
# split on.
synthetic_doc() {
  local mode="$1"
  printf '%s\n' '# Synthetic phase doc' '' '```text' '## Persist the Plan' '```' ''
  printf '%s\n' '## Persist the Plan' '' 'Intro with `cenci pipeline plan <id> --approve` in prose.' '' '### Mark the ticket `Planned`' ''
  syn_block 'gh issue comment <number> --repo o/r --body-file x'
  printf '%s\n' '```text' '## Implementation Plan' '### Decoy' '```' ''
  syn_calls_standard "${mode}"
  printf '%s\n' '' '## Resume From Draft' ''
  syn_calls_standard "${mode}"
  printf '%s\n' '```text' '## Decoy inside resume' '```' ''
  printf '%s\n' '## Lean Approval Path' ''
  syn_calls_trivial "${mode}"
  printf '%s\n' '8. Emit a 3-5 line status summary only.' '9. Set `hasPlanFile = true` and continue into Phase 2.' ''
  printf '%s\n' '## Trivial Fast Path' ''
  syn_calls_trivial "${mode}"
  printf '%s\n' '8. Set `hasPlanFile = true` and continue into Phase 2.' ''
}
syn_calls_standard() {
  if [[ "$1" == "correct" ]]; then
    syn_block 'cenci pipeline plan <id>'
    syn_block 'cenci pipeline label <id> --transition planned'
    syn_block 'cenci pipeline artifact <id> --plan .plans/<filename>'
  else
    syn_block 'cenci pipeline label <id> --transition planned'
    syn_block 'cenci pipeline artifact <id> --plan .plans/<filename>'
    syn_block 'cenci pipeline plan <id>'
  fi
}
syn_calls_trivial() {
  if [[ "$1" == "correct" ]]; then
    syn_block 'cenci pipeline plan <id>'
    syn_block 'cenci pipeline label <id> --transition planned --trivial'
    syn_block 'cenci pipeline artifact <id> --plan .plans/<filename>'
  else
    syn_block 'cenci pipeline label <id> --transition planned --trivial'
    syn_block 'cenci pipeline artifact <id> --plan .plans/<filename>'
    syn_block 'cenci pipeline plan <id>'
  fi
}

SYN_CORRECT="$(synthetic_doc correct)"
SYN_INVERTED="$(synthetic_doc inverted)"
for name in "${REGIONS[@]}"; do
  syn_region="$(region_of "${SYN_INVERTED}" "${name}")"
  syn_problems="$(region_problems "${name}" "${syn_region}")"
  [[ "${syn_problems}" == *"ordering violated"* ]] || \
    fail "self-test b (${name}): the inverted synthetic doc was not reported as an ordering violation (got: [${syn_problems}])"
  [[ "${syn_problems}" != *"marker not found"* ]] || \
    fail "self-test b (${name}): the inverted synthetic doc lost a marker -- the extractor is not fence-aware (got: [${syn_problems}])"

  syn_region="$(region_of "${SYN_CORRECT}" "${name}")"
  syn_problems="$(region_problems "${name}" "${syn_region}")"
  [[ -z "${syn_problems}" ]] || \
    fail "self-test b (${name}): the correctly ordered synthetic doc was flagged (got: [${syn_problems}])"
done

echo "plan-label-ordering-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
