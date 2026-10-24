#!/usr/bin/env bash
# Needles are literal Markdown containing backticks and <placeholders>, so SC2016 is expected.
# shellcheck disable=SC2016
#
# Scoped verification contract (#1103): red/green work and every fix delegation run the
# lane's scoped Verification command (or an explicit `full: <command> — <reason>`
# fallback); ONE authoritative Completed-Change Verification (CCV) runs at the end of
# Phase 5 in every mode, absorbing the Lane Verification Barrier and the old separate
# Phase 5 full run; a later fix delegation that changes a file makes that evidence stale
# and CCV reruns once before Phase 8. A scoped pass is never reported as a full-suite pass.
#
# Agent procedures implement these behaviors; this test pins the procedure text. Reading
# guide for the wording the procedures must use:
#
#   * Every pinned phrase below sits on ONE source line (shell-scripting-gotchas.md:
#     a wrapped phrase false-negatives, and backtick pairs split by wrapping leak).
#   * Sections are extracted by EXACT heading line, fence-aware (a heading-shaped line
#     inside a code fence neither starts nor ends a section). Codex companion text has
#     no headings, so it is sliced between line-start markers, as the sibling
#     implementation-concurrency-contract.test.sh does.
#   * A pin proves a sentence exists, not that a step executes it: the "wiring" group
#     pairs the sentence pins with checks that the mechanism is connected (Verification
#     cell -> `Tests:` source -> phase 3/4 delegation; `full-checks-` label in timing.md;
#     every phase that hands off to CCV names where CCV is defined).
#   * Checks are pure: each *_violations function prints one line per violated
#     expectation and never touches the failure counter, so the non-vacuity self-test
#     can run the very same function against injected copies and compare outputs.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || exit 2
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || exit 2
failures=0
fail() {
  printf 'FAIL: %s\n' "$1"
  failures=$((failures+1))
}

PHASE3="${FLOW_DIR}/skills/implement/phases/phase-3-test-red.md"
PHASE4="${FLOW_DIR}/skills/implement/phases/phase-4-implement-green.md"
PHASE5="${FLOW_DIR}/skills/implement/phases/phase-5-refactor.md"
PHASE67="${FLOW_DIR}/skills/implement/phases/phase-6-7-review.md"
PLANNER="${FLOW_DIR}/agents/planner.md"
IMPLEMENTER="${FLOW_DIR}/agents/implementer.md"
CODEX="${FLOW_DIR}/skills/implement/codex.md"
TIMING="${FLOW_DIR}/skills/implement/timing.md"
TOML_TEMPLATE="${FLOW_DIR}/templates/codex/agent-roles/implementer.toml"
TOML_INSTALLED="${REPO_DIR}/.codex/agents/implementer.toml"
README="${FLOW_DIR}/README.md"
HEALTH_GATES="${REPO_DIR}/docs/health-gates.md"

# ---------------------------------------------------------------------------
# Literal contract strings (acceptance criteria of #1103, verbatim).
# ---------------------------------------------------------------------------
F_SCOPED='Verification (scoped): <command> — <pass|fail> — not covered: <scope outside the command>'
F_FALLBACK='Verification (full fallback): <command> — <pass|fail> — reason: <reason>'
F_FULL='Verification (full): <command> — <pass|fail>'
NEVER_FULL='A scoped pass is never reported as full suite, all tests or CI green'
CCV='Completed-Change Verification'
# The pre-#1103 resume sentence is pinned by implement-codex-resume-contract.test.sh and
# stays word for word; the new CCV sentence is added after it.
CODEX_RESUME_PINNED='skip this Implementation dispatch, the lane verification barrier, the refactor pass, the reuse check and Visual Verification, and continue at the acceptance review'

# ---------------------------------------------------------------------------
# Pure helpers. None of them touches `failures`.
# ---------------------------------------------------------------------------

# count_of TEXT NEEDLE -> number of OCCURRENCES (grep -oF | wc -l), never matching lines.
# Only grep exit codes 0 (matches) and 1 (none) are answers; any other exit code prints a
# non-numeric marker, which every caller reports as a violation instead of reading as 0.
count_of() {
  local out rc
  out="$(printf '%s\n' "$1" | grep -oF -- "$2")"
  rc=$?
  if [[ "$rc" -eq 1 ]]; then
    printf '0\n'
  elif [[ "$rc" -eq 0 ]]; then
    printf '%s\n' "$out" | wc -l | tr -d '[:space:]'
  else
    printf 'grep-exit-%s\n' "$rc"
  fi
}

# is_count VALUE: VALUE is a non-negative integer (a real count, not an error marker).
is_count() {
  [[ "$1" =~ ^[0-9]+$ ]]
}

# need LABEL TEXT NEEDLE: violation when NEEDLE is absent. Empty needle or empty text is a
# violation (a glob test with an empty needle, or against an empty extraction, would pass).
need() {
  if [[ -z "$3" ]]; then
    printf '%s: empty needle (vacuous)\n' "$1"
    return 0
  fi
  if [[ -z "$2" ]]; then
    printf '%s: section or file missing/empty, cannot find: %s\n' "$1" "$3"
    return 0
  fi
  [[ "$2" == *"$3"* ]] || printf '%s: missing: %s\n' "$1" "$3"
  return 0
}

# need_all LABEL TEXT NEEDLE...
need_all() {
  local label="$1" text="$2" n
  shift 2
  for n in "$@"; do
    need "$label" "$text" "$n"
  done
  return 0
}

# forbid LABEL TEXT NEEDLE: violation when NEEDLE is present; an empty extraction is also a
# violation, because "absent from nothing" proves nothing.
forbid() {
  local n
  if [[ -z "$3" ]]; then
    printf '%s: empty needle (vacuous)\n' "$1"
    return 0
  fi
  if [[ -z "$2" ]]; then
    printf '%s: section or file missing/empty, absence is meaningless: %s\n' "$1" "$3"
    return 0
  fi
  n="$(count_of "$2" "$3")"
  if ! is_count "$n"; then
    printf '%s: could not count occurrences of %s (%s)\n' "$1" "$3" "$n"
    return 0
  fi
  [[ "$n" -eq 0 ]] || printf '%s: must not contain: %s (%s occurrence(s))\n' "$1" "$3" "$n"
  return 0
}

# forbid_re LABEL TEXT ERE: like forbid, for a pattern that must not match any line.
forbid_re() {
  local rc
  if [[ -z "$2" ]]; then
    printf '%s: section or file missing/empty, absence is meaningless: %s\n' "$1" "$3"
    return 0
  fi
  printf '%s\n' "$2" | grep -qE -- "$3"
  rc=$?
  if [[ "$rc" -eq 0 ]]; then
    printf '%s: must not match: %s\n' "$1" "$3"
  elif [[ "$rc" -ne 1 ]]; then
    printf '%s: bad pattern: %s\n' "$1" "$3"
  fi
  return 0
}

# need_count_ge LABEL TEXT NEEDLE MIN
need_count_ge() {
  local n
  if [[ -z "$3" || -z "$2" ]]; then
    printf '%s: empty needle or text (vacuous): %s\n' "$1" "$3"
    return 0
  fi
  n="$(count_of "$2" "$3")"
  if ! is_count "$n"; then
    printf '%s: could not count occurrences of %s (%s)\n' "$1" "$3" "$n"
    return 0
  fi
  [[ "$n" -ge "$4" ]] || printf '%s: expected >= %s occurrence(s) of %s, found %s\n' "$1" "$4" "$3" "$n"
  return 0
}

# section FILE HEADING: the lines from the exact heading line (leading whitespace ignored,
# so the planner's indented plan template works) up to, excluding, the next heading of the
# same or a shallower level. Fence-aware: headings inside ``` / ~~~ fences are ignored on
# both ends. Prints nothing when the file or heading is absent.
section() {
  [[ -f "$1" && -n "$2" ]] || return 0
  awk -v h="$2" '
    BEGIN { lvl = 0; while (substr(h, lvl + 1, 1) == "#") lvl++ }
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(```|~~~)/) { infence = !infence; if (on) print $0; next }
      if (!infence && match(line, /^#+ /)) {
        cur = RLENGTH - 1
        if (on && cur <= lvl) exit
        if (!on && line == h) { on = 1; print $0; next }
      }
      if (on) print $0
    }
  ' "$1"
}

# preamble FILE: everything before the first real (non-fenced) `## ` heading.
preamble() {
  [[ -f "$1" ]] || return 0
  awk '
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(```|~~~)/) { infence = !infence; print $0; next }
      if (!infence && line ~ /^## /) exit
      print $0
    }
  ' "$1"
}

# section_own FILE HEADING: like section, but only the heading's OWN body -- up to the next
# heading of ANY level, so a nested subsection is excluded. Fence-aware.
section_own() {
  [[ -f "$1" && -n "$2" ]] || return 0
  awk -v h="$2" '
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(```|~~~)/) { infence = !infence; if (on) print $0; next }
      if (!infence && match(line, /^#+ /)) {
        if (on) exit
        if (line == h) { on = 1; print $0; next }
      }
      if (on) print $0
    }
  ' "$1"
}

# between FILE START END: Codex companion text has no headings; slice from the first line
# beginning with START to, excluding, the next line beginning with END (leading whitespace
# ignored). Used only where the file has no headings to anchor on. When START is found but
# END never is, the slice would silently run to the end of the file, so the sentinel line
# below is appended and slice_complete reports it as a violation.
SLICE_SENTINEL='__END_MARKER_MISSING__'
between() {
  [[ -f "$1" && -n "$2" && -n "$3" ]] || return 0
  awk -v start="$2" -v end="$3" -v sentinel="$SLICE_SENTINEL" '
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (!active && index(line, start) == 1) active = 1
      else if (active && index(line, end) == 1) { ended = 1; exit }
      if (active) print $0
    }
    END { if (active && !ended) print sentinel }
  ' "$1"
}

# slice_complete LABEL TEXT: violation when a `between` slice never saw its end marker.
slice_complete() {
  if [[ "$2" == *"$SLICE_SENTINEL"* ]]; then
    printf '%s: slice end marker never found (%s)\n' "$1" "$SLICE_SENTINEL"
  fi
  return 0
}

# heading_count FILE HEADING: exact heading lines outside code fences.
heading_count() {
  [[ -f "$1" ]] || { printf '0\n'; return 0; }
  awk -v h="$2" '
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(```|~~~)/) { infence = !infence; next }
      if (!infence && line == h) n++
    }
    END { print n + 0 }
  ' "$1"
}

# heading_line FILE HEADING: line number of the first exact heading outside fences (0 if none).
heading_line() {
  [[ -f "$1" ]] || { printf '0\n'; return 0; }
  awk -v h="$2" '
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(```|~~~)/) { infence = !infence; next }
      if (!infence && line == h) { print NR; found = 1; exit }
    }
    END { if (!found) print 0 }
  ' "$1"
}

# line_with FILE NEEDLE: the first line containing NEEDLE (prefix-anchored lookups for
# table rows and config bullets); empty when absent.
line_with() {
  [[ -f "$1" && -n "$2" ]] || return 0
  grep -m1 -F -- "$2" "$1" || true
}

# heading_once LABEL FILE HEADING: the heading exists exactly once (outside fences).
heading_once() {
  local n
  n="$(heading_count "$2" "$3")"
  if ! is_count "$n"; then
    printf '%s: could not count "%s" headings (%s)\n' "$1" "$3" "$n"
    return 0
  fi
  [[ "$n" -eq 1 ]] || printf '%s: expected exactly one "%s" heading, found %s\n' "$1" "$3" "$n"
  return 0
}

# heading_absent LABEL FILE HEADING
heading_absent() {
  local n
  n="$(heading_count "$2" "$3")"
  if ! is_count "$n"; then
    printf '%s: could not count "%s" headings (%s)\n' "$1" "$3" "$n"
    return 0
  fi
  [[ "$n" -eq 0 ]] || printf '%s: must not have a "%s" heading, found %s\n' "$1" "$3" "$n"
  return 0
}

# heading_before LABEL FILE FIRST SECOND: both exist and FIRST precedes SECOND.
heading_before() {
  local a b
  a="$(heading_line "$2" "$3")"
  b="$(heading_line "$2" "$4")"
  if [[ "$a" -eq 0 || "$b" -eq 0 || "$a" -ge "$b" ]]; then
    printf '%s: "%s" (line %s) must come before "%s" (line %s)\n' "$1" "$3" "$a" "$4" "$b"
  fi
  return 0
}

# ---------------------------------------------------------------------------
# Pinned phrases for the two files the non-vacuity self-test rebuilds. The fixtures the
# self-test generates are written FROM these arrays, so the checker is proven against a
# conforming document and against the same document with one violation injected. The
# phrases are the exact wording the procedure files must use.
# ---------------------------------------------------------------------------

# phase-4: the text before the first `## ` heading (lane path, UI evidence kept).
P4_PREAMBLE=(
  'skip the standard implementation delegation'
  'parallel or sequential lanes, including dependency waves'
  'then run `## Visual Verification` for a UI ticket'
  'continue to Phase 5'
  "$CCV"
)
# phase-4 `## Delegation Context`: ordinary Phase 4 receives the resolved Verification
# command (the lane's `Tests:` value), not the project build/test commands.
P4_CTX=(
  'resolved Verification command'
  'Verification (scoped)'
  'Verification (full fallback)'
)
# phase-4 `## Verification`: scoped command (+ scoped lint when the cell names one), the
# literal report formats, the retained hard gate and context-budget stop.
P4_VERIFY=(
  'resolved Verification command'
  'scoped lint command'
  'An absent `lintCommand` skips'
  "$F_SCOPED"
  "$F_FALLBACK"
  "$NEVER_FULL"
  'Retry up to 3 times'
  'Lint failures are held to the same hard gate'
  'Context: low'
  "$CCV"
  'as the result line'
  'a `Context: low` first line still follow'
  'means the verification did not run'
)
P4_VERIFY_FORBIDDEN=(
  'Run the full build'
  'Run the full test suite'
  'and nothing else'
)

# Stale-evidence trigger shared by phase-5, phase-6-7 and codex.md: a fix-delegation report
# is trusted only through a literal `Changed files:` line (review-fix round 1, finding B).
STALE_PINS=(
  'carry a literal `Changed files:` line'
  'Changed files: none'
  'any edit the root agent applies itself, counts as stale'
)

# phase-5 `## Process`: cleanup runs the scoped command; the full check is CCV below,
# after the Reuse Check. (phase5-reuse-check-contract.test.sh pins the same sentence.)
P5_PROCESS=(
  'scoped Verification command'
  'runs once all cleanup is done — including the `## Reuse Check` below'
  'Behavior must not change.'
  'the refactor worker receives them only in ordinary mode'
  'in lane mode they go only to the barrier worker'
)
P5_REUSE=(
  "$CCV"
)
P5_CCV=(
  'the full build, the full test suite and lint (when `lintCommand` is set)'
  'once, after cleanup and `## Reuse Check`'
  'in both ordinary and lane modes'
  'last step of the refactor delegation'
  'An absent `lintCommand` skips'
  '3 attempts'
  'exact errors'
  'the exact errors, the attempts, and the best hypothesis'
  'Never proceed to the acceptance review while red.'
  'docs/model-escalation.md'
  'domain-specialist'
  'two failed attempts'
  'Context Budget'
  'Context: low'
  "$F_FULL"
  'full-checks-<slug>'
  'never persisted'
  'whose report lists a changed file'
  'counts as stale'
  'Phase 8 documentation edits'
  'post-rebase full run'
  'step 6 is this Completed-Change Verification'
  'still runs exactly once'
  'A report without a `Verification (full): <command> — pass` line'
  'means the check did not run'
)
P5_LANE=(
  'fresh implementer'
  'after the refactor delegation'
  'may touch any file'
  '`Scope:` summary'
  'cross-lane'
  '3 attempts'
  'domain-specialist'
)
P5_ERR=(
  "$CCV"
  'revert that step only'
)
P5_FORBIDDEN_ANYWHERE=(
  'Run the full test suite once all cleanup is done'
)

# ---------------------------------------------------------------------------
# Violation groups (pure; print violations).
# ---------------------------------------------------------------------------

phase4_violations() {
  local f="$1" pre ctx ver n
  [[ -f "$f" ]] || { printf 'phase-4: missing file %s\n' "$f"; return 0; }
  pre="$(preamble "$f")"
  ctx="$(section "$f" '## Delegation Context')"
  ver="$(section "$f" '## Verification')"
  need_all 'phase-4 preamble' "$pre" "${P4_PREAMBLE[@]}"
  forbid 'phase-4 preamble' "$pre" 'run `## Lane Verification Barrier`'
  need_all 'phase-4 ## Delegation Context' "$ctx" "${P4_CTX[@]}"
  forbid 'phase-4 ## Delegation Context' "$ctx" 'buildCommand'
  need_all 'phase-4 ## Verification' "$ver" "${P4_VERIFY[@]}"
  for n in "${P4_VERIFY_FORBIDDEN[@]}"; do
    forbid 'phase-4 ## Verification' "$ver" "$n"
  done
  # The barrier procedure moved to phase-5; phase-4 keeps only a pointer.
  heading_absent 'phase-4' "$f" '## Lane Verification Barrier'
  # UI evidence is still reached on every path (acceptance criterion: all modes).
  heading_once 'phase-4' "$f" '## Visual Verification'
  heading_once 'phase-4' "$f" '### Persist Screenshots For The PR'
  return 0
}

phase5_violations() {
  local f="$1" process reuse ccv ccv_full lane err whole n
  [[ -f "$f" ]] || { printf 'phase-5: missing file %s\n' "$f"; return 0; }
  whole="$(cat "$f")" || { printf 'phase-5: cannot read %s\n' "$f"; return 0; }
  process="$(section "$f" '## Process')"
  reuse="$(section "$f" '## Reuse Check')"
  # ccv is the parent body only (up to the `### Lane mode` heading); a pin that belongs to
  # the nested barrier lives in P5_LANE, so the subsection cannot satisfy a parent pin.
  ccv="$(section_own "$f" '## Completed-Change Verification')"
  ccv_full="$(section "$f" '## Completed-Change Verification')"
  lane="$(section "$f" '### Lane mode: Lane Verification Barrier')"
  err="$(section "$f" '## Error Recovery')"
  heading_once 'phase-5' "$f" '## Completed-Change Verification'
  heading_once 'phase-5' "$f" '### Lane mode: Lane Verification Barrier'
  # CCV runs after cleanup AND the Reuse Check, so it must be defined after that section.
  heading_before 'phase-5' "$f" '## Reuse Check' '## Completed-Change Verification'
  need_all 'phase-5 ## Process' "$process" "${P5_PROCESS[@]}"
  need_all 'phase-5 ## Reuse Check' "$reuse" "${P5_REUSE[@]}"
  forbid 'phase-5 ## Reuse Check' "$reuse" 'same full-suite-and-lint run'
  need_all 'phase-5 ## Completed-Change Verification' "$ccv" "${P5_CCV[@]}" "${STALE_PINS[@]}"
  # The lane-mode barrier is a subsection OF the single CCV, not a second definition.
  need 'phase-5 ## Completed-Change Verification' "$ccv_full" '### Lane mode: Lane Verification Barrier'
  need_all 'phase-5 ### Lane mode' "$lane" "${P5_LANE[@]}"
  need_all 'phase-5 ## Error Recovery' "$err" "${P5_ERR[@]}"
  for n in "${P5_FORBIDDEN_ANYWHERE[@]}"; do
    forbid 'phase-5' "$whole" "$n"
  done
  return 0
}

phase3_violations() {
  local f="$1" whole recheck budget compact lanes dctx resume elig fanout
  [[ -f "$f" ]] || { printf 'phase-3: missing file %s\n' "$f"; return 0; }
  whole="$(cat "$f")" || { printf 'phase-3: cannot read %s\n' "$f"; return 0; }
  recheck="$(section "$f" '## Delegation Preflight Re-check')"
  budget="$(section "$f" '## Context Budget')"
  compact="$(section "$f" '## Compact Implementation')"
  lanes="$(section "$f" '## Parallel Lanes')"
  dctx="$(section "$f" '## Delegation Context')"
  resume="$(section "$f" '## Resume Brief')"
  # Re-check: an empty cell, or a `full:` fallback with no reason, returns to planning; a
  # legacy plain command is a scoped command.
  need_all 'phase-3 ## Delegation Preflight Re-check' "$recheck" \
    'Verification cell is empty' \
    '`full:` fallback with no reason' \
    '/cenci:implement <id> replan' \
    'A legacy plain command is treated as a scoped command'
  need 'phase-3 ## Context Budget' "$budget" "$CCV"
  # Compact: step 6 IS its CCV; no other full group before acceptance review; UI evidence kept.
  need_all 'phase-3 ## Compact Implementation' "$compact" \
    'Run full build, tests and configured lint' \
    'This step is its Completed-Change Verification' \
    'no other full verification group runs between this step and the acceptance review' \
    'Visual Verification' \
    'phases/phase-5-refactor.md'
  # Parallel/sequential lanes: lane-scoped only; the full suite is CCV in Phase 5.
  need_all 'phase-3 ## Parallel Lanes' "$lanes" \
    'Never run the full build or full test suite' \
    'The full suite runs exactly once' \
    'UI work always takes the sequential path' \
    'Owned files and Verification columns supply Files and Tests' \
    'On resume, re-run completed lanes' \
    'then always run Completed-Change Verification' \
    'evidence is never persisted'
  # A `full:` fallback lane runs the full suite, so it forces sequential execution (eligibility
  # re-check) and is the one exception to the fan-out lanes' never-run-the-full-suite rule.
  elig="$(between "$f" '**Eligibility re-check' '**Sequential lanes.**')"
  fanout="$(between "$f" '**Fan-out.**' '**Error gate')"
  slice_complete 'phase-3 eligibility re-check' "$elig"
  slice_complete 'phase-3 fan-out' "$fanout"
  need 'phase-3 eligibility re-check' "$elig" 'Any lane whose Verification cell is a `full:` fallback forces sequential execution'
  need_all 'phase-3 fan-out step 5' "$fanout" \
    'Never run the full build or full test suite' \
    'except a lane whose Verification cell is a `full:` fallback (sequential only)'
  need_count_ge 'phase-3 ## Parallel Lanes' "$lanes" "$CCV" 4
  forbid_re 'phase-3 ## Parallel Lanes' "$lanes" 'Phase 4.*Lane Verification Barrier'
  need 'phase-3 ## Delegation Context' "$dctx" 'resolved Verification command'
  need 'phase-3 ## Resume Brief' "$resume" "$CCV"
  need 'phase-3 ## Resume Brief' "$resume" 'never persisted'
  # The mention below is pinned by implementation-concurrency-contract.test.sh; the
  # barrier name stays, but no site may still point at Phase 4 for it.
  need 'phase-3' "$whole" 'Lane Verification Barrier'
  forbid_re 'phase-3' "$whole" 'Phase 4.*Lane Verification Barrier'
  return 0
}

phase67_violations() {
  local f="$1" acc sec code rerun silent
  [[ -f "$f" ]] || { printf 'phase-6-7: missing file %s\n' "$f"; return 0; }
  acc="$(section "$f" '## Acceptance Review')"
  sec="$(section "$f" '## Security Review Actions')"
  code="$(section "$f" '## Code Review Actions')"
  rerun="$(section "$f" '## Completed-Change Verification Re-run')"
  silent="$(section "$f" '## Silent Failure Actions')"
  # Fix rounds run the fix's scoped Verification command and mark CCV evidence stale.
  need_all 'phase-6-7 ## Acceptance Review' "$acc" \
    'scoped Verification command' \
    'marks the Completed-Change Verification evidence stale'
  forbid_re 'phase-6-7 ## Acceptance Review' "$acc" 'Phase 4.s verification'
  need_all 'phase-6-7 ## Security Review Actions' "$sec" \
    'scoped Verification command' \
    'marks the Completed-Change Verification evidence stale'
  forbid 'phase-6-7 ## Security Review Actions' "$sec" 'rerun tests'
  need_all 'phase-6-7 ## Code Review Actions' "$code" \
    'scoped Verification command' \
    'marks the Completed-Change Verification evidence stale'
  forbid 'phase-6-7 ## Code Review Actions' "$code" 'rerun tests'
  need_all 'phase-6-7 ## Silent Failure Actions' "$silent" \
    'scoped Verification command' \
    'marks the Completed-Change Verification evidence stale'
  heading_once 'phase-6-7' "$f" '## Completed-Change Verification Re-run'
  heading_before 'phase-6-7' "$f" '## Silent Failure Actions' '## Completed-Change Verification Re-run'
  need_all 'phase-6-7 ## Completed-Change Verification Re-run' "$rerun" \
    'phases/phase-5-refactor.md' \
    'once, after the last fix round' \
    'never once per round' \
    'Do not enter Phase 8 until it passes' \
    'A round whose report lists no changed file leaves the evidence valid' \
    'A report that is ambiguous or missing counts as stale' \
    'fresh delegation' \
    're-capture' \
    're-review' \
    'Phase 9' \
    "${STALE_PINS[@]}"
  return 0
}

planner_violations() {
  local f="$1" pre whole
  [[ -f "$f" ]] || { printf 'planner: missing file %s\n' "$f"; return 0; }
  whole="$(cat "$f")" || { printf 'planner: cannot read %s\n' "$f"; return 0; }
  pre="$(section "$f" '### Delegation Preflight')"
  need_all 'planner ### Delegation Preflight' "$pre" \
    'full: <command> — <reason a narrower safe command is unavailable>' \
    'full: <command> — <reason' \
    '; lint: <scoped lint command>' \
    'including the single lane of ordinary work' \
    'the only source of `Tests:`' \
    'Owned files maps to `Files:`, Verification to `Tests:`'
  need 'planner' "$whole" 'Completed-Change Verification (Lane Verification Barrier)'
  forbid 'planner' "$whole" 'at the final Lane Verification Barrier'
  return 0
}

implementer_violations() {
  local f="$1" ver err whole
  [[ -f "$f" ]] || { printf 'implementer.md: missing file %s\n' "$f"; return 0; }
  whole="$(cat "$f")" || { printf 'implementer.md: cannot read %s\n' "$f"; return 0; }
  ver="$(section "$f" '## Verification')"
  err="$(section "$f" '## Error Recovery')"
  forbid 'implementer.md' "$whole" 'after each significant change'
  need_all 'implementer.md ## Verification' "$ver" \
    'scoped Verification command' \
    "$CCV" \
    'compact delegation' \
    '`full:` fallback' \
    'An absent `lintCommand` skips' \
    "$F_SCOPED" \
    "$F_FALLBACK" \
    "$F_FULL" \
    "$NEVER_FULL" \
    'a literal `Changed files:` line' \
    'Changed files: none'
  need 'implementer.md ## Error Recovery' "$err" 'scoped Verification command'
  forbid 'implementer.md ## Error Recovery' "$err" 'Re-run build, tests, and lint'
  return 0
}

codex_violations() {
  local f="$1" whole dispatch reuse fixes
  [[ -f "$f" ]] || { printf 'codex.md: missing file %s\n' "$f"; return 0; }
  whole="$(cat "$f")" || { printf 'codex.md: cannot read %s\n' "$f"; return 0; }
  dispatch="$(between "$f" '**Implementation dispatch (apply only).**' 'Then, before the reviews')"
  reuse="$(between "$f" 'Then, before the reviews' 'Then, before any code review')"
  fixes="$(between "$f" 'Then, before any code review' 'Documentation policy:')"
  slice_complete 'codex.md dispatch' "$dispatch"
  slice_complete 'codex.md reuse check' "$reuse"
  slice_complete 'codex.md acceptance/review fixes' "$fixes"
  need_all 'codex.md dispatch' "$dispatch" \
    'In ordinary mode the refactor delegation ends with Phase 5'"'"'s `## Completed-Change Verification`' \
    'A delegation report without a `Verification (full): <command> — pass` line means Completed-Change Verification did not run' \
    'scoped Verification command' \
    'Verification cell is empty' \
    '`full:` fallback with no reason' \
    '`replan`' \
    'Its full build, test and lint run is its Completed-Change Verification' \
    '`## Completed-Change Verification`' \
    'runs at Completed-Change Verification, after the refactor pass' \
    'Lane Verification Barrier' \
    'then always runs Completed-Change Verification' \
    'evidence is never persisted' \
    'only when a fix delegation in that session changes the worktree' \
    "$CODEX_RESUME_PINNED"
  forbid_re 'codex.md dispatch' "$dispatch" 'Phase 4.*Lane Verification Barrier'
  need 'codex.md reuse check' "$reuse" 'same Completed-Change Verification run'
  forbid 'codex.md reuse check' "$reuse" 'same full-suite-and-lint run'
  need_all 'codex.md acceptance/review fixes' "$fixes" \
    'scoped Verification command' \
    'marks the Completed-Change Verification evidence stale' \
    'once, after the last fix round' \
    'before any documentation edit' \
    "${STALE_PINS[@]}"
  need_all 'codex.md report formats' "$whole" "$F_SCOPED" "$F_FALLBACK" "$F_FULL" "$NEVER_FULL"
  return 0
}

toml_violations() {
  local label="$1" f="$2" whole
  [[ -f "$f" ]] || { printf '%s: missing file %s\n' "$label" "$f"; return 0; }
  whole="$(cat "$f")" || { printf '%s: cannot read %s\n' "$label" "$f"; return 0; }
  # The TOML basic string forbids unescaped double quotes: these phrases carry none.
  need_all "$label" "$whole" \
    'scoped Verification command' \
    "$CCV" \
    '`full:` fallback' \
    "$F_SCOPED" \
    "$F_FALLBACK" \
    "$F_FULL" \
    "$NEVER_FULL"
  return 0
}

toml_identical_violations() {
  [[ -f "$1" && -f "$2" ]] || { printf 'implementer.toml copies: missing %s or %s\n' "$1" "$2"; return 0; }
  cmp -s -- "$1" "$2" || printf 'implementer.toml: %s and %s must be byte-identical\n' "$1" "$2"
  return 0
}

docs_violations() {
  local hg readme_compact readme_conc readme_max timing_row a b
  hg="$(section "$HEALTH_GATES" '## Authoring guidance')"
  need_all 'health-gates.md ## Authoring guidance' "$hg" \
    'reporting verification' \
    '#994' \
    'did not cover' \
    'full-suite green'
  # The bullet follows the false-green list, under its own lead-in.
  a="$(grep -nF -m1 -- 'Pre-execution file validation' "$HEALTH_GATES" | cut -d: -f1)"
  b="$(grep -nF -m1 -- '#994' "$HEALTH_GATES" | cut -d: -f1)"
  if [[ -z "$a" || -z "$b" || "$b" -le "$a" ]]; then
    printf 'health-gates.md: the #994 bullet (line %s) must come after the false-green list (line %s)\n' "${b:-none}" "${a:-none}"
  fi

  readme_compact="$(line_with "$README" '- `compactImplementation: true`')"
  readme_conc="$(line_with "$README" '- `implementerConcurrency: ')"
  readme_max="$(line_with "$README" '- `implementerMaxConcurrency: ')"
  need 'README compactImplementation bullet' "$readme_compact" "$CCV"
  need 'README implementerConcurrency bullet' "$readme_conc" "$CCV"
  need_all 'README implementerMaxConcurrency bullet' "$readme_max" \
    "$CCV" \
    'run scoped tests' \
    'refactor, then one Completed-Change Verification'
  forbid 'README implementerMaxConcurrency bullet' "$readme_max" 'one combined build/test/lint barrier follows all waves'

  # timing.md: CCV reuses the full-checks label; the four bash blocks stay untouched
  # (timing.test.mjs parses them), so only the table row and the prose sentence change.
  timing_row="$(line_with "$TIMING" '| Baseline gate')"
  need_all 'timing.md verification row' "$timing_row" "$CCV" 'full-checks-'
  forbid 'timing.md verification row' "$timing_row" 'lane verification barrier'
  need 'timing.md' "$(cat "$TIMING")" 'Do not count Completed-Change Verification twice'
  forbid 'timing.md' "$(cat "$TIMING")" 'Do not count the barrier twice'
  return 0
}

# Mechanism wiring: a sentence pin proves the sentence exists, not that a step reads the
# input it names. Each chain below checks that the carrier of a value is wired at both ends.
wiring_violations() {
  local p3 p5 p67 pl tm
  p3="$(cat "$PHASE3")" || { printf 'wiring: cannot read %s\n' "$PHASE3"; return 0; }
  p5="$(cat "$PHASE5")" || { printf 'wiring: cannot read %s\n' "$PHASE5"; return 0; }
  p67="$(cat "$PHASE67")" || { printf 'wiring: cannot read %s\n' "$PHASE67"; return 0; }
  pl="$(cat "$PLANNER")" || { printf 'wiring: cannot read %s\n' "$PLANNER"; return 0; }
  tm="$(cat "$TIMING")" || { printf 'wiring: cannot read %s\n' "$TIMING"; return 0; }
  # Chain 1: planner Verification cell -> `Tests:` (planner) -> phase-3 re-check and lane
  # fields -> phase-3/phase-4 Delegation Context pass the RESOLVED command to the worker.
  need 'wiring verification-cell->Tests: (planner)' "$pl" 'Verification to `Tests:`'
  need 'wiring verification-cell->Tests: (phase-3 lanes)' "$p3" 'Owned files and Verification columns supply Files and Tests'
  need 'wiring verification-cell->worker (phase-3 delegation context)' "$(section "$PHASE3" '## Delegation Context')" 'resolved Verification command'
  need 'wiring verification-cell->worker (phase-4 delegation context)' "$(section "$PHASE4" '## Delegation Context')" 'resolved Verification command'
  need 'wiring verification-cell->worker (phase-4 verification step)' "$(section "$PHASE4" '## Verification')" 'resolved Verification command'
  # Chain 2: the timing label CCV records under, at the definition and at the helper doc.
  need 'wiring full-checks label (phase-5 CCV)' "$(section "$PHASE5" '## Completed-Change Verification')" 'full-checks-<slug>'
  need 'wiring full-checks label (timing.md)' "$tm" 'full-checks-'
  # Chain 3: every hand-off to CCV names where CCV is defined, because SKILL.md reads only
  # the file for the phase being started.
  need 'wiring hand-off phase-3 compact -> phase-5' "$(section "$PHASE3" '## Compact Implementation')" 'phases/phase-5-refactor.md'
  need 'wiring hand-off phase-4 preamble -> phase-5' "$(preamble "$PHASE4")" "$CCV"
  need 'wiring hand-off phase-4 verification -> phase-5' "$(section "$PHASE4" '## Verification')" "$CCV"
  need 'wiring hand-off phase-6-7 re-run -> phase-5' "$(section "$PHASE67" '## Completed-Change Verification Re-run')" 'phases/phase-5-refactor.md'
  # Chain 4: the stale rule has a definition (phase-5) and its consumers (phase-6-7 fix
  # rounds) -- the consumers' pins above name the marker; here the definition names its
  # trigger as a delegation whose report lists a changed file.
  need 'wiring stale-evidence definition (phase-5)' "$p5" 'whose report lists a changed file'
  need 'wiring stale-evidence consumer (phase-6-7)' "$p67" 'marks the Completed-Change Verification evidence stale'
  return 0
}

# ---------------------------------------------------------------------------
# Non-vacuity self-test: a conforming document passes, and each injected violation is
# caught with the expected message. Fixtures are generated from the pinned-phrase arrays
# (so this proves the checkers against a document that satisfies them even while the real
# files are still red), plus differential runs on mktemp -d copies of the REAL files.
# ---------------------------------------------------------------------------

write_lines() { printf '%s\n' "$@"; }

write_phase4_fixture() {
  {
    write_lines '# Phase 4: Implement (Green)' "${P4_PREAMBLE[@]}"
    write_lines '## Delegation Context' "${P4_CTX[@]}"
    write_lines '## Rules'
    write_lines '## Verification' "${P4_VERIFY[@]}"
    write_lines '## Visual Verification'
    write_lines '### Persist Screenshots For The PR'
  } > "$1"
}

write_phase5_fixture() {
  {
    write_lines '# Phase 5: Refactor'
    write_lines '## Process' "${P5_PROCESS[@]}"
    write_lines '## Reuse Check' "${P5_REUSE[@]}"
    write_lines '## Completed-Change Verification' "${P5_CCV[@]}" "${STALE_PINS[@]}"
    write_lines '### Lane mode: Lane Verification Barrier' "${P5_LANE[@]}"
    write_lines '## Error Recovery' "${P5_ERR[@]}"
  } > "$1"
}

# expect_caught LABEL OUTPUT SUBSTRING: the injected violation must be reported.
expect_caught() {
  if [[ "$2" != *"$3"* ]]; then
    fail "non-vacuity: $1: injected violation not caught (expected a report mentioning: $3)"
  fi
}

# expect_clean LABEL OUTPUT: the conforming (or benign-fence) document reports nothing.
expect_clean() {
  if [[ -n "$2" ]]; then
    fail "non-vacuity: $1: expected no violations, got: $2"
  fi
}

self_test() {
  local tmp out clean tampered
  tmp="$(mktemp -d)" || { fail 'non-vacuity: mktemp -d failed'; return 0; }
  if [[ -z "$tmp" || ! -d "$tmp" ]]; then
    fail 'non-vacuity: mktemp -d returned no directory'
    return 0
  fi

  # --- phase-4 on a conforming fixture and with injected violations -----------------
  write_phase4_fixture "$tmp/p4-ok.md"
  expect_clean 'phase-4 conforming fixture' "$(phase4_violations "$tmp/p4-ok.md")"

  awk '{ print } /^## Verification$/ { print "1. Run the full build." }' "$tmp/p4-ok.md" > "$tmp/p4-fullbuild.md"
  out="$(phase4_violations "$tmp/p4-fullbuild.md")"
  expect_caught 'phase-4 full build step injected' "$out" 'must not contain: Run the full build'

  awk '{ print } /^## Verification$/ { print "2. Run the full test suite." }' "$tmp/p4-ok.md" > "$tmp/p4-fullsuite.md"
  out="$(phase4_violations "$tmp/p4-fullsuite.md")"
  expect_caught 'phase-4 full suite step injected' "$out" 'must not contain: Run the full test suite'

  { cat "$tmp/p4-ok.md"; printf '## Lane Verification Barrier\n'; } > "$tmp/p4-barrier.md"
  out="$(phase4_violations "$tmp/p4-barrier.md")"
  expect_caught 'phase-4 barrier heading reintroduced' "$out" 'must not have a "## Lane Verification Barrier" heading'

  grep -vF -- 'resolved Verification command' "$tmp/p4-ok.md" > "$tmp/p4-nocmd.md"
  out="$(phase4_violations "$tmp/p4-nocmd.md")"
  expect_caught 'phase-4 resolved command removed' "$out" 'missing: resolved Verification command'

  # A heading-shaped line inside a fence is not a boundary: the Verification section
  # must still be read in full, and no violation may appear.
  awk '{ print } /^## Verification$/ { print "```"; print "## Fake heading"; print "```" }' "$tmp/p4-ok.md" > "$tmp/p4-fence.md"
  expect_clean 'phase-4 fenced fake heading' "$(phase4_violations "$tmp/p4-fence.md")"

  # --- phase-5 ---------------------------------------------------------------------
  write_phase5_fixture "$tmp/p5-ok.md"
  expect_clean 'phase-5 conforming fixture' "$(phase5_violations "$tmp/p5-ok.md")"

  { cat "$tmp/p5-ok.md"; printf '## Completed-Change Verification\n'; } > "$tmp/p5-dup.md"
  out="$(phase5_violations "$tmp/p5-dup.md")"
  expect_caught 'phase-5 duplicate CCV heading' "$out" 'expected exactly one "## Completed-Change Verification" heading, found 2'

  # A fenced example of the heading is not a second definition.
  { cat "$tmp/p5-ok.md"; printf '```\n## Completed-Change Verification\n```\n'; } > "$tmp/p5-fenced-dup.md"
  expect_clean 'phase-5 fenced example heading' "$(phase5_violations "$tmp/p5-fenced-dup.md")"

  # A fenced fake heading inside the CCV section must not truncate it.
  awk '{ print } /^## Completed-Change Verification$/ { print "```"; print "## Fake heading"; print "```" }' "$tmp/p5-ok.md" > "$tmp/p5-fence.md"
  expect_clean 'phase-5 fenced fake heading inside CCV' "$(phase5_violations "$tmp/p5-fence.md")"

  grep -vF -- 'may touch any file' "$tmp/p5-ok.md" > "$tmp/p5-nolane.md"
  out="$(phase5_violations "$tmp/p5-nolane.md")"
  expect_caught 'phase-5 lane cross-file authority removed' "$out" 'missing: may touch any file'

  # The hard-gate stop report (best hypothesis) and the red-blocks-acceptance sentence are
  # each pinned: removing either line from the CCV section must be reported.
  grep -vF -- 'the exact errors, the attempts, and the best hypothesis' "$tmp/p5-ok.md" > "$tmp/p5-nohyp.md"
  out="$(phase5_violations "$tmp/p5-nohyp.md")"
  expect_caught 'phase-5 best hypothesis clause removed' "$out" 'missing: the exact errors, the attempts, and the best hypothesis'

  grep -vF -- 'Never proceed to the acceptance review while red.' "$tmp/p5-ok.md" > "$tmp/p5-nored.md"
  out="$(phase5_violations "$tmp/p5-nored.md")"
  expect_caught 'phase-5 never-proceed-while-red sentence removed' "$out" 'missing: Never proceed to the acceptance review while red.'

  { cat "$tmp/p5-ok.md"; printf 'Run the full test suite once all cleanup is done.\n'; } > "$tmp/p5-oldrun.md"
  out="$(phase5_violations "$tmp/p5-oldrun.md")"
  expect_caught 'phase-5 separate full run reintroduced' "$out" 'must not contain: Run the full test suite once all cleanup is done'

  grep -vxF -- '## Reuse Check' "$tmp/p5-ok.md" > "$tmp/p5-order.md"
  printf '## Reuse Check\n' >> "$tmp/p5-order.md"
  out="$(phase5_violations "$tmp/p5-order.md")"
  expect_caught 'phase-5 CCV defined before the Reuse Check' "$out" '"## Reuse Check"'

  # A parent-body pin must not be satisfiable from the nested Lane-mode subsection.
  awk '{ print } /^### Lane mode: Lane Verification Barrier$/ { print "Never proceed to the acceptance review while red." }' "$tmp/p5-nored.md" > "$tmp/p5-nested.md"
  out="$(phase5_violations "$tmp/p5-nested.md")"
  expect_caught 'phase-5 parent pin satisfied only by the nested subsection' "$out" 'missing: Never proceed to the acceptance review while red.'

  grep -vF -- 'Changed files: none' "$tmp/p5-ok.md" > "$tmp/p5-nochanged.md"
  out="$(phase5_violations "$tmp/p5-nochanged.md")"
  expect_caught 'phase-5 Changed files: requirement removed' "$out" 'missing: Changed files: none'

  # --- helpers that have no other injected case ---------------------------------------
  out="$(forbid_re 'forbid_re' $'fine line\nPhase 4 runs the Lane Verification Barrier' 'Phase 4.*Lane Verification Barrier')"
  expect_caught 'forbid_re matching line' "$out" 'must not match: Phase 4.*Lane Verification Barrier'
  expect_clean 'forbid_re no matching line' "$(forbid_re 'forbid_re' $'fine line\nanother line' 'Phase 4.*Lane Verification Barrier')"
  out="$(forbid_re 'forbid_re' 'text' '(' 2>/dev/null)"
  expect_caught 'forbid_re invalid pattern' "$out" 'bad pattern: ('

  out="$(need_count_ge 'need_count_ge' $'one hit\nno other' 'hit' 2)"
  expect_caught 'need_count_ge below the minimum' "$out" 'expected >= 2 occurrence(s) of hit, found 1'
  expect_clean 'need_count_ge at the minimum' "$(need_count_ge 'need_count_ge' $'one hit\nsecond hit' 'hit' 2)"

  # A grep exit code other than 0/1 is a violation, never a silent zero count.
  out="$(grep() { return 2; }; forbid 'forbid' 'some text' 'needle')"
  expect_caught 'forbid with a grep scan error' "$out" 'could not count occurrences of needle'
  out="$(grep() { return 2; }; need_count_ge 'need_count_ge' 'some text' 'needle' 1)"
  expect_caught 'need_count_ge with a grep scan error' "$out" 'could not count occurrences of needle'
  out="$(awk() { printf 'oops\n'; }; heading_once 'heading_once' "$tmp/p5-ok.md" '## Process')"
  expect_caught 'heading_once with a non-numeric count' "$out" 'could not count "## Process" headings'
  out="$(awk() { printf 'oops\n'; }; heading_absent 'heading_absent' "$tmp/p5-ok.md" '## Process')"
  expect_caught 'heading_absent with a non-numeric count' "$out" 'could not count "## Process" headings'

  # A `between` slice whose end marker never appears must fail, not run to end of file.
  printf 'Alpha begins\nbody line\nno terminator here\n' > "$tmp/between-noend.txt"
  out="$(slice_complete 'between' "$(between "$tmp/between-noend.txt" 'Alpha' 'Omega')")"
  expect_caught 'between with a missing end marker' "$out" 'slice end marker never found'
  printf 'Alpha begins\nbody line\nOmega ends\n' > "$tmp/between-end.txt"
  expect_clean 'between with its end marker present' "$(slice_complete 'between' "$(between "$tmp/between-end.txt" 'Alpha' 'Omega')")"

  # The byte-identical TOML check reports a difference and a missing copy.
  printf 'developer_instructions = "a"\n' > "$tmp/role-a.toml"
  printf 'developer_instructions = "a"\n' > "$tmp/role-same.toml"
  printf 'developer_instructions = "b"\n' > "$tmp/role-b.toml"
  expect_clean 'identical toml copies' "$(toml_identical_violations "$tmp/role-a.toml" "$tmp/role-same.toml")"
  out="$(toml_identical_violations "$tmp/role-a.toml" "$tmp/role-b.toml")"
  expect_caught 'differing toml copies' "$out" 'must be byte-identical'
  out="$(toml_identical_violations "$tmp/role-a.toml" "$tmp/role-absent.toml")"
  expect_caught 'missing toml copy' "$out" 'missing'

  # --- differential runs on copies of the REAL files (hold whether red or green) -----
  if [[ -f "$CODEX" ]]; then
    grep -vF -- 'Then, before the reviews' "$CODEX" > "$tmp/real-codex-noend.md"
    out="$(codex_violations "$tmp/real-codex-noend.md")"
    expect_caught 'real codex copy with the dispatch end marker removed' "$out" 'slice end marker never found'
  else
    fail "non-vacuity: missing $CODEX"
  fi
  if [[ -f "$PHASE4" ]]; then
    cp -- "$PHASE4" "$tmp/real-p4.md"
    clean="$(phase4_violations "$tmp/real-p4.md")"
    awk '{ print } /^## Verification$/ { print "1. Run the full build." }' "$tmp/real-p4.md" > "$tmp/real-p4-bad.md"
    tampered="$(phase4_violations "$tmp/real-p4-bad.md")"
    if [[ "$clean" == "$tampered" ]]; then
      fail 'non-vacuity: real phase-4 copy: injected full-build step changed nothing'
    fi
    expect_caught 'real phase-4 copy full build injected' "$tampered" 'must not contain: Run the full build'
  else
    fail "non-vacuity: missing $PHASE4"
  fi
  if [[ -f "$PHASE5" ]]; then
    cp -- "$PHASE5" "$tmp/real-p5.md"
    clean="$(phase5_violations "$tmp/real-p5.md")"
    { cat "$tmp/real-p5.md"; printf '\n## Completed-Change Verification\n\n## Completed-Change Verification\n'; } > "$tmp/real-p5-bad.md"
    tampered="$(phase5_violations "$tmp/real-p5-bad.md")"
    if [[ "$clean" == "$tampered" ]]; then
      fail 'non-vacuity: real phase-5 copy: injected duplicate CCV headings changed nothing'
    fi
    expect_caught 'real phase-5 copy duplicate CCV injected' "$tampered" 'expected exactly one "## Completed-Change Verification" heading'
  else
    fail "non-vacuity: missing $PHASE5"
  fi

  rm -rf -- "$tmp"
  return 0
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------

# run_group NAME COMMAND...: report every printed violation through fail() in this shell.
run_group() {
  local name="$1" out line
  shift
  out="$("$@")" || { fail "$name: check itself failed"; return 0; }
  if [[ -n "$out" ]]; then
    while IFS= read -r line; do
      fail "$name: $line"
    done <<< "$out"
  fi
  return 0
}

self_test
run_group 'ordinary / retry green (phase-4)' phase4_violations "$PHASE4"
run_group 'CCV, lane mode, compact carve-out (phase-5)' phase5_violations "$PHASE5"
run_group 'compact, sequential/UI lanes, parallel lanes, resume (phase-3)' phase3_violations "$PHASE3"
run_group 'retry/fix and CCV re-run (phase-6-7)' phase67_violations "$PHASE67"
run_group 'planner preflight' planner_violations "$PLANNER"
run_group 'implementer role (Claude)' implementer_violations "$IMPLEMENTER"
run_group 'Codex companion' codex_violations "$CODEX"
run_group 'Codex role template' toml_violations 'implementer.toml template' "$TOML_TEMPLATE"
run_group 'Codex role installed copy' toml_violations '.codex/agents/implementer.toml' "$TOML_INSTALLED"
run_group 'Codex role copies identical' toml_identical_violations "$TOML_TEMPLATE" "$TOML_INSTALLED"
run_group 'docs (health-gates, README, timing)' docs_violations
run_group 'mechanism wiring' wiring_violations

printf 'scoped verification contracts: failures=%s\n' "$failures"
[[ "$failures" -eq 0 ]]
