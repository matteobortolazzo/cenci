#!/usr/bin/env bash
# Contract test for #1303: lesson-system-aware capture in `lessons-collector`
# and Phase 8's `## Capture Lessons`.
#
# These assertions check instruction text only; they do not prove end-to-end agent judgment.
#
# The gap this pins down: the collector routed every lesson by topic (existing
# `docs/<topic>.md`, `## Critical Rules`, or a new topic doc) as if no
# repository lesson system existed, and in `minimal` mode could still grow
# Critical Rules or create topic files. #1302 added the `lessons:` line to
# `skills/implement/scripts/doc-conventions.sh discover` (covered by
# flow/tests/doc-conventions.test.sh) and the plan's `### Documentation
# Routing` `Lessons:` / `Validations:` lines. #1303 makes the collector branch
# on them: `structured` -> one new record in the record directory, `fallback`
# -> one flat bullet in an existing owner doc, `convention: none` in default
# mode -> the legacy topic-doc / Critical Rules paths, `minimal` with no lesson
# system and no fitting owner -> the lesson is dropped and listed in the PR's
# `## Notes` (Phase 8 hands the dropped lines to Phase 9 through
# `$RUN_DIR/lessons-status.txt`).
#
# Prose-only pipeline change, so -- following flow/tests/doc-routing-contract.test.sh
# -- the verification is a grep contract: a `failures` counter, literal
# matching, section-scoped assertions through a FENCE-AWARE extract_section
# (gotchas rule 29), every shared rule defined ONCE as a single-source-line
# marker and asserted at every restatement site (flow AGENTS.md #979, gotchas
# rule 13), occurrences counted with `grep -oF | wc -l` (rule 10(4)), and every
# procedure sentence paired with a check that the mechanism it describes is
# wired (rule 10(5)): the discover fixture below runs the real
# `doc-conventions.sh`, and Phase 8 / Phase 9 are checked for the step that
# writes and the step that reads `lessons-status.txt`.
#
# Acceptance criteria covered: P11 (five named cases), P19 (minimal drop listed
# in the PR Notes, no file created), P22 (lesson validation failure reaches the
# Fix now / Stop / Push anyway question, never a pass), P25 (README
# `Lessons fallback` paragraph), P29 (this header).
#
# Non-vacuity self-tests (gotchas rules 10, 29, 30): an unscoped creation line
# injected into a copy of the collector must be caught, a copy with the dedup
# marker removed (or moved after the writes) must be caught, and the marker
# counter must report zero for an absent marker.
#
# No `read_*`-named helpers (flow/tests/read-helper-purity-contract.test.sh);
# no permission-removing chmod (flow/tests/root-safe-perms-contract.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "lessons-routing-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "lessons-routing-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# load_doc_raw <path-under-FLOW_DIR> -- pure extraction, no fail() side effect.
load_doc_raw() {
  cat "${FLOW_DIR}/$1" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> -- fails closed on a missing or
# unreadable file; a missing file must never masquerade as empty content.
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(load_doc_raw "${_relpath}")"; then
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

# assert_nonempty <content> <label> -- a scoped extraction that came back empty
# means the heading moved or vanished; every assertion on it would be vacuous.
assert_nonempty() {
  [[ -n "$1" ]] || fail "$2: section extraction is empty (heading missing or renamed)"
}

# extract_section <content> <exact-heading-line> -- the body under a `## ` or
# `### ` heading up to the next heading of the same or a higher level.
# Fence-aware: a `## ` look-alike inside a ``` fence neither starts nor ends a
# section. Pure, safe inside $(...).
extract_section() {
  local content="$1" heading="$2"
  awk -v h="${heading}" '
    BEGIN { level = (h ~ /^### /) ? 3 : 2 }
    /^```/ { infence = !infence; if (on) print; next }
    !infence && !on && $0 == h { on = 1; next }
    on && !infence && match($0, /^#+ /) && (RLENGTH - 1) <= level { exit }
    on { print }
  ' <<<"${content}"
}

# extract_fence_bodies <content> -- only the lines inside ``` fences (any
# language), without the fence lines themselves.
extract_fence_bodies() {
  awk '
    /^```/ { infence = !infence; next }
    infence { print }
  ' <<<"$1"
}

# extract_line <content> <literal-substring> -- first line containing it.
extract_line() {
  local content="$1" needle="$2"
  grep -F -m1 -e "${needle}" <<<"${content}"
}

# extract_paragraph <content> <literal-anchor> [<literal-stop>] -- the first line
# containing the anchor and every following line up to (not including) the next
# blank line. Scopes an assertion to one paragraph so a string that also occurs
# elsewhere in the document cannot satisfy it vacuously (gotchas rule 10(3)).
# Fails closed (prints nothing) when the paragraph is not properly terminated:
# running to end of input, or -- when a stop anchor is given -- a terminating
# blank line that is not followed by a line containing the stop anchor. A stop
# anchor also ends the paragraph directly, so a lost blank-line terminator
# cannot widen the scope into the next paragraph. A stop anchor that is passed but
# empty is rejected (prints nothing, returns 1) rather than silently falling back
# to the no-stop mode. Both anchors reach awk through ENVIRON, not -v, so a
# backslash in an anchor is never interpreted as an escape. Pure.
extract_paragraph() {
  local content="$1" anchor="$2" stop="${3:-}"
  if [[ $# -ge 3 && -z "${stop}" ]]; then
    return 1
  fi
  PARA_ANCHOR="${anchor}" PARA_STOP="${stop}" awk '
    BEGIN { a = ENVIRON["PARA_ANCHOR"]; s = ENVIRON["PARA_STOP"] }
    !on { if (index($0, a)) { on = 1; buf = $0 "\n" } ; next }
    !closed {
      if (s != "" && index($0, s)) { done = 1; exit }
      if ($0 == "") { if (s == "") { done = 1; exit } closed = 1; next }
      buf = buf $0 "\n"; next
    }
    { if (index($0, s)) { done = 1 } exit }
    END { if (done) printf "%s", buf }
  ' <<<"${content}"
}

# line_of <content> <literal-substring> -- line number of the first line that
# contains the substring, or empty. Pure.
line_of() {
  local content="$1" needle="$2"
  grep -nF -m1 -e "${needle}" <<<"${content}" | cut -d: -f1
}

# marker_count <content> <marker> -- occurrences (not lines) of a literal
# marker. Pure; prints a number, or ERROR when grep itself failed.
marker_count() {
  local content="$1" marker="$2" out rc
  out="$(grep -oF -e "${marker}" <<<"${content}")"
  rc=$?
  if [[ ${rc} -gt 1 ]]; then
    echo "ERROR"
    return
  fi
  if [[ -z "${out}" ]]; then
    echo 0
    return
  fi
  printf '%s\n' "${out}" | wc -l | tr -d '[:space:]'
}

# assert_marker_once <content> <marker> <label> -- the marker is present
# exactly once (a restated rule appears at the site once, never twice and
# never missing).
assert_marker_once() {
  local content="$1" marker="$2" label="$3" count
  [[ -n "${marker}" ]] || { fail "${label}: empty marker (test bug)"; return; }
  count="$(marker_count "${content}" "${marker}")"
  if [[ "${count}" == "ERROR" ]]; then
    fail "${label}: grep error while counting marker [${marker}]"
  elif [[ "${count}" -ne 1 ]]; then
    fail "${label}: expected exactly 1 occurrence of [${marker}], found ${count}"
  fi
}

# unscoped_creation_lines <content> -- the `<n>:<line>` rows of <content> that
# name a topic-doc creation or a Critical Rules append but not BOTH
# `convention: none` and `default` (creation is limited to `convention: none` in
# default mode, so a line naming only one of them is unscoped). Pure, no
# fail(); prints ERROR when grep itself failed.
CREATION_NEEDLES=('new topic doc' 'topic doc' 'Append under `## Critical Rules`' 'append it under `## Critical Rules`' 'appended under' 'create a new' 'New docs/<topic>.md')
CREATION_REGEXES=('append.*Critical Rules')
unscoped_creation_lines() {
  local content="$1" needle hits rc all=""
  for needle in "${CREATION_NEEDLES[@]}"; do
    hits="$(grep -niF -e "${needle}" <<<"${content}")"
    rc=$?
    if [[ ${rc} -gt 1 ]]; then
      echo "ERROR"
      return
    fi
    all+="${hits}"$'\n'
  done
  for needle in "${CREATION_REGEXES[@]}"; do
    hits="$(grep -niE -e "${needle}" <<<"${content}")"
    rc=$?
    if [[ ${rc} -gt 1 ]]; then
      echo "ERROR"
      return
    fi
    all+="${hits}"$'\n'
  done
  # `default` must be the backticked token (a prose "default" such as "by default" is not the mode).
  printf '%s' "${all}" | awk 'NF && !(index($0, "convention: none") && index($0, "`default`"))' | sort -u
  return 0
}

# dedup_order_problem <collector-content> -- prints why the dedup step is not
# strictly before every write step, or nothing when the order holds. Pure.
dedup_order_problem() {
  local content="$1" dedup_at first_write="" candidate at
  dedup_at="$(line_of "${content}" "${M_DEDUP}")"
  if [[ -z "${dedup_at}" ]]; then
    echo "the dedup marker is absent"
    return
  fi
  for candidate in "${M_STRUCTURED}" "${M_FALLBACK}" "${M_MINIMAL_DROP}"; do
    at="$(line_of "${content}" "${candidate}")"
    if [[ -z "${at}" ]]; then
      echo "a write-step marker is absent: [${candidate}]"
      return
    fi
    if [[ -z "${first_write}" || "${at}" -lt "${first_write}" ]]; then
      first_write="${at}"
    fi
  done
  if [[ "${dedup_at}" -ge "${first_write}" ]]; then
    echo "dedup (line ${dedup_at}) is not before the first write step (line ${first_write})"
  fi
}

# --------------------------------------------------------------------------
# Shared marker sentences. Each is ONE source line wherever it appears, and it
# is restated byte-identically at every site listed per case below (flow
# AGENTS.md #979). The existing single-occurrence markers of
# doc-routing-contract.test.sh are referred to, never restated.
# --------------------------------------------------------------------------
M_CANDIDATE='Rework or a reviewer flag is a lesson candidate, not an automatic admission.'
M_DEDUP='Deduplicate each candidate against existing owner docs and lesson records before any write; a duplicate produces no write and a `Dropped (already owned)` summary line.'
M_STRUCTURED="With \`Lessons: structured admission=<path> records=<dir>\`, an admitted lesson is one new record in \`<dir>\` following the admission doc's format, never a flat bullet."
M_FALLBACK='With `Lessons: fallback`, an admitted lesson is one flat bullet in the existing owner doc that owns its subject.'
M_MINIMAL_DROP="In \`minimal\` mode with no lesson system and no fitting existing owner, an admitted lesson is dropped and listed as one line in the PR's \`## Notes\`; no new \`docs/<topic>.md\` and no \`## Critical Rules\` bullet is written."
M_ONE_ENTRY='An admitted lesson produces exactly one entry: one record, one flat bullet or one dropped line, never several for the same subject.'
M_POLICY_LINE='Documentation policy: <minimal|default>'
M_LESSON_VALIDATION="Execute only the plan's \`Validations:\` text verbatim; compare the collector's \`### Lesson validation\` report to it per command (the backticked command text, not the whole line) by exact string equality, so a command that ran is never also reported as skipped, and use the report only to derive \`validation: <label> — skipped (not named in the plan)\` lines, never to execute anything."
M_LABEL_LIMIT="The \`<label>\` of a \`validation: <label> — skipped (not named in the plan)\` line is limited to the command name or the admission doc's repo-relative path, with environment-variable assignments, tokens and URL credentials removed; this takes precedence over verbatim copying (#979)."
M_STATUS_ALWAYS='The lessons status file is written on every entry into this lesson-capture step, including when the collector was not launched and when no lesson trigger fired: an empty file means no lesson was dropped, a file that is absent while the run directory is known means the status is unknown, and a file that exists but cannot be read means the status is unknown with a reason, never none.'
M_STATUS_CARRY='When the collector is not relaunched in this entry, read the existing lessons status file first and carry its lines forward; write an empty file only when no earlier file exists or when the collector ran in this entry and listed none, and report an earlier file that cannot be read in the status update instead of replacing it with an empty file.'
M_RUNDIR_UNKNOWN='When the run directory is unknown, skip the lessons status file write and name each dropped lesson in the status update; Phase 9 then renders its own `RUN_DIR lost` entry.'
M_STATUS_READBACK='After writing the lessons status file, read it back and confirm its content equals what was just written, an empty file reading back as empty and never as absent or unreadable; retry the write once, and when it still does not match report that in the status update instead of leaving the PR step to read a stale or absent file as none.'
M_CAPTURE_UNAVAILABLE='When the collector returned `Blocked: …`, errored, or returned neither `## Lessons Collected` nor `No lessons captured`, write `Lesson capture unavailable (<reason>)` as the only line of the lessons status file (or name it in the status update when the run directory is unknown), with a reason of repo-relative paths and identifiers only (#979); never write an empty file for it.'
M_WRITE_LIMIT="Each dropped-lesson summary keeps to repo-relative paths and identifiers only, never file content, secrets, environment values, URL credentials or command output, because Phase 9 copies it into a PR body that can reach a public repository; this takes precedence over copying the collector's line verbatim (#979)."
M_RENDER_CUT="A dropped-lesson line that carries anything beyond a repo-relative path or an identifier (file content, a command's output, a secret, an environment value, a URL credential) is cut down to its identifier-level part before it is rendered: this secrecy rule takes precedence over copying the line verbatim (#979), because a PR body can reach a possibly-public repository."
M_UNAVAILABLE_RENDER='Render a `Lesson capture unavailable (<reason>)` line as one unchecked `- [ ]` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none" and never as a dropped lesson, with the reason cut down to repo-relative paths and identifiers only (#979).'
M_ABSENT_RENDER='When the run directory is unknown, or the lessons status file is absent or cannot be read, render one unchecked `- [ ] Lesson status unavailable (<reason>)` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none", with the reason limited to `RUN_DIR lost`, `file absent` or `file unreadable` and never a path or a raw tool error (#979); the lesson-capture step always writes the file, so an absent file means the status is unknown, not that no lesson was dropped.'
M_UNRECOGNISED_LINE='Any non-empty lessons status file that contains a line matching neither `Lesson dropped (minimal policy):` nor `Lesson capture unavailable (` is treated as unreadable: render `- [ ] Lesson status unavailable (file unreadable)` and copy none of the unrecognised text into the PR body (#979).'
M_EMPTY_STATUS_READ="An empty lessons status file counts as empty (none), not unreadable: when the client's read tool returns no content or only a system reminder for an empty file, that counts as empty, and the client's shell (\`test -e\` / \`test -s\`, or \`cat\` and its exit status) confirms it, rather than a reminder-only read being treated as unreadable."
M_LESSON_FAIL='A lesson validation that fails after a record was created is recorded as a `fail` `validation:` line in the maintenance status file and enters the Fix now / Stop / Push anyway question, never as a pass.'
M_COLLECTOR_VALIDATION="Report the admission doc's validation command under \`### Lesson validation\` without running it; you have no shell, and the main agent runs only a command the plan names."
M_NOTES_RENDER='Render each `Lesson dropped (minimal policy):` line as one entry under `## Notes` → `### Considered and discarded`, using repo-relative paths and identifiers only, never file content or command output.'
M_PLANNER_VALIDATION='with a structured `Lessons:` line, list the validation command its admission doc names, if any'
M_ONLY_MINIMAL_REACHES="Only \`### Dropped (minimal policy)\` entries reach the lessons status file and the PR's \`## Notes\`; every other dropped entry stays in the collector's summary."
M_SKIPPED_WIRING='Write each `validation: <label> — skipped (not named in the plan)` line for a collector-reported command into the maintenance status file with the other `validation:` lines, never as `pass`; Phase 9 renders it as an unchecked `Docs check skipped` line.'
M_SKIPPED_REDERIVE="Re-derive that skipped line on every entry from the collector's \`### Lesson validation\` report; when the collector was not relaunched or reported none, read the earlier status file before the first-line rewrite and carry its \`skipped (not named in the plan)\` lines forward."
DROPPED_LINE='Lesson dropped (minimal policy):'
STATUS_FILE='lessons-status.txt'

COLLECTOR_REL="agents/lessons-collector.md"
PHASE8_REL="skills/implement/phases/phase-8-docs.md"
PHASE9_REL="skills/implement/phases/phase-9-pr.md"
CODEX_REL="skills/implement/codex.md"
PLANNER_REL="agents/planner.md"
README_REL="README.md"

require_doc COLLECTOR "${COLLECTOR_REL}"
require_doc PHASE8 "${PHASE8_REL}"
require_doc PHASE9 "${PHASE9_REL}"
require_doc CODEX "${CODEX_REL}"
require_doc PLANNER "${PLANNER_REL}"
require_doc README "${README_REL}"

LESSONS_SECTION="$(extract_section "${PHASE8}" '## Capture Lessons')"
MAINTENANCE_SECTION="$(extract_section "${PHASE8}" '## Maintenance Check')"
PROCESS_SECTION="$(extract_section "${COLLECTOR}" '## Process')"
OUTPUT_SECTION="$(extract_section "${COLLECTOR}" '## Output Summary')"
OUTPUT_FENCES="$(extract_fence_bodies "${OUTPUT_SECTION}")"
assert_nonempty "${LESSONS_SECTION}" "${PHASE8_REL} (## Capture Lessons)"
assert_nonempty "${MAINTENANCE_SECTION}" "${PHASE8_REL} (## Maintenance Check)"
assert_nonempty "${PROCESS_SECTION}" "${COLLECTOR_REL} (## Process)"
assert_nonempty "${OUTPUT_FENCES}" "${COLLECTOR_REL} (## Output Summary fenced template)"

# =====================================================================
# P11 case 1: ordinary TDD red/green yields `No lessons captured`.
# =====================================================================
case_tdd_red_green_no_lessons() {
  local stop_line stop_at dedup_at
  assert_contains "${PROCESS_SECTION}" 'not normal TDD red→green' "${COLLECTOR_REL} (case 1: red/green is excluded from candidates)"
  stop_line="$(extract_line "${PROCESS_SECTION}" 'stop and return')"
  assert_contains "${stop_line}" '"No lessons captured"' "${COLLECTOR_REL} (case 1: no candidate stops the run with the empty result)"
  assert_contains "${OUTPUT_FENCES}" 'No lessons captured' "${COLLECTOR_REL} (case 1: the output template carries the empty result)"
  # The empty-result stop comes before dedup and every write step.
  stop_at="$(line_of "${PROCESS_SECTION}" 'stop and return')"
  dedup_at="$(line_of "${PROCESS_SECTION}" "${M_DEDUP}")"
  if [[ -z "${stop_at}" || -z "${dedup_at}" ]]; then
    fail "${COLLECTOR_REL} (case 1): could not locate both the empty-result stop and the dedup step (stop=${stop_at:-none} dedup=${dedup_at:-none})"
  elif [[ "${stop_at}" -ge "${dedup_at}" ]]; then
    fail "${COLLECTOR_REL} (case 1): the empty-result stop must precede the dedup step (stop=${stop_at} dedup=${dedup_at})"
  fi
  # Phase 8 never launches the collector for smooth sessions or red/green progression.
  assert_contains "${LESSONS_SECTION}" 'Do not run for smooth sessions, normal TDD red/green progression, or obvious findings already covered by existing rules.' "${PHASE8_REL} (case 1: Capture Lessons skip sentence)"
}

# =====================================================================
# P11 case 2: an admitted non-obvious rework produces exactly one entry.
# =====================================================================
case_admitted_rework_one_entry() {
  assert_marker_once "${COLLECTOR}" "${M_CANDIDATE}" "${COLLECTOR_REL} (case 2: candidate, not automatic admission)"
  assert_marker_once "${LESSONS_SECTION}" "${M_CANDIDATE}" "${PHASE8_REL} (## Capture Lessons, case 2: candidate)"
  assert_marker_once "${CODEX}" "${M_CANDIDATE}" "${CODEX_REL} (case 2: candidate)"
  assert_marker_once "${COLLECTOR}" "${M_ONE_ENTRY}" "${COLLECTOR_REL} (case 2: exactly one entry)"
  assert_marker_once "${LESSONS_SECTION}" "${M_ONE_ENTRY}" "${PHASE8_REL} (## Capture Lessons, case 2: exactly one entry)"
  assert_marker_once "${CODEX}" "${M_ONE_ENTRY}" "${CODEX_REL} (case 2: exactly one entry)"
}

# =====================================================================
# P11 case 3: knowledge already owned produces no write and a
# `Dropped (already owned)` summary line.
# =====================================================================
case_already_owned_dropped() {
  local problem
  assert_marker_once "${COLLECTOR}" "${M_DEDUP}" "${COLLECTOR_REL} (case 3: dedup)"
  assert_marker_once "${LESSONS_SECTION}" "${M_DEDUP}" "${PHASE8_REL} (## Capture Lessons, case 3: dedup)"
  assert_marker_once "${CODEX}" "${M_DEDUP}" "${CODEX_REL} (case 3: dedup)"
  assert_contains "${OUTPUT_FENCES}" '### Dropped (already owned)' "${COLLECTOR_REL} (case 3: the output template names the already-owned heading)"
  problem="$(dedup_order_problem "${PROCESS_SECTION}")"
  if [[ -n "${problem}" ]]; then
    fail "${COLLECTOR_REL} (case 3: dedup before every write step): ${problem}"
  fi
}

# =====================================================================
# P11 case 4: with a structured lesson system discovered, the entry is a new
# record in its record directory following its admission doc's format. Wired
# to the real mechanism: `doc-conventions.sh discover` prints the `lessons:`
# line the marker's placeholders name.
# =====================================================================
case_structured_record_in_record_dir() {
  local fixture structured_out fallback_out lessons_count expected
  assert_marker_once "${COLLECTOR}" "${M_STRUCTURED}" "${COLLECTOR_REL} (case 4: structured record)"
  assert_marker_once "${LESSONS_SECTION}" "${M_STRUCTURED}" "${PHASE8_REL} (## Capture Lessons, case 4: structured record)"
  assert_marker_once "${CODEX}" "${M_STRUCTURED}" "${CODEX_REL} (case 4: structured record)"
  assert_marker_once "${COLLECTOR}" "${M_FALLBACK}" "${COLLECTOR_REL} (case 4: fallback bullet)"
  assert_marker_once "${LESSONS_SECTION}" "${M_FALLBACK}" "${PHASE8_REL} (## Capture Lessons, case 4: fallback bullet)"
  assert_marker_once "${CODEX}" "${M_FALLBACK}" "${CODEX_REL} (case 4: fallback bullet)"
  # The collector takes the record directory from the forwarded Lessons: line.
  assert_contains "${COLLECTOR}" 'records=<dir>' "${COLLECTOR_REL} (case 4: record directory input)"
  assert_contains "${LESSONS_SECTION}" 'Lessons:' "${PHASE8_REL} (## Capture Lessons, case 4: the Lessons: line is forwarded)"
  assert_marker_once "${COLLECTOR}" "${M_POLICY_LINE}" "${COLLECTOR_REL} (case 4: forwarded policy input)"
  assert_marker_once "${LESSONS_SECTION}" "${M_POLICY_LINE}" "${PHASE8_REL} (## Capture Lessons, case 4: policy is forwarded)"
  assert_marker_once "${CODEX}" "${M_POLICY_LINE}" "${CODEX_REL} (case 4: policy is forwarded)"
  assert_not_contains "${COLLECTOR}" 'config.json' "${COLLECTOR_REL} (case 4: the collector has no shell and does not resolve the policy itself)"

  if ! fixture="$(mktemp -d "${TMPDIR:-/tmp}/lessons-routing-contract.fixture.XXXXXX")"; then
    fail "case 4: mktemp failed for the discover fixture"
    return
  fi
  mkdir -p "${fixture}/structured/docs/lessons" "${fixture}/fallback/docs" || { fail "case 4: could not build the discover fixtures"; rm -rf -- "${fixture}"; return; }
  printf '# Lessons\n\nAdmission rules.\n' > "${fixture}/structured/docs/lessons/README.md" || fail "case 4: could not write the admission doc fixture"
  printf '# First record\n' > "${fixture}/structured/docs/lessons/2026-01-first-record.md" || fail "case 4: could not write the record fixture"
  printf '# Guide\n' > "${fixture}/fallback/docs/guide.md" || fail "case 4: could not write the guide fixture"

  expected='lessons: structured admission=docs/lessons/README.md records=docs/lessons/'
  if structured_out="$(bash "${FLOW_DIR}/skills/implement/scripts/doc-conventions.sh" discover "${fixture}/structured" 2>/dev/null)"; then
    lessons_count="$(grep -c '^lessons: ' <<<"${structured_out}")"
    if [[ "${lessons_count}" != "1" ]]; then
      fail "case 4: discover must print exactly one lessons: line for the structured fixture (found ${lessons_count})"
    fi
    if ! grep -qxF -e "${expected}" <<<"${structured_out}"; then
      fail "case 4: discover did not print [${expected}] for the structured fixture"
    fi
    # The marker's placeholders name the same two fields discover prints.
    assert_contains "${M_STRUCTURED}" 'structured admission=<path> records=<dir>' "case 4 (marker names the discover fields)"
  else
    fail "case 4: doc-conventions.sh discover failed on the structured fixture"
  fi
  if fallback_out="$(bash "${FLOW_DIR}/skills/implement/scripts/doc-conventions.sh" discover "${fixture}/fallback" 2>/dev/null)"; then
    if ! grep -qxF -e 'lessons: fallback' <<<"${fallback_out}"; then
      fail "case 4: discover did not print [lessons: fallback] for a fixture without a lesson tree"
    fi
  else
    fail "case 4: doc-conventions.sh discover failed on the fallback fixture"
  fi
  rm -rf -- "${fixture}"
}

# Apply the collector's documented search shape to fixture paths rather than
# assuming a prose mention of per-project owners makes them discoverable. This
# checks instruction/search-shape coverage, not autonomous admission judgment.
case_fallback_owner_outside_plan() {
  assert_not_contains "${LESSONS_SECTION}" 'telling the collector to write only to the owners and lesson system it lists' 'Phase 8 must allow independently found fitting owners'
  if ! python3 - "${FLOW_DIR}" <<'PY'
import glob
from pathlib import Path
import re
import subprocess
import sys
import tempfile

flow = Path(sys.argv[1])
collector = (flow / 'agents/lessons-collector.md').read_text()
destination = collector.split('### Step 7a: Find the lesson destination\n', 1)[1].split('\n### Step 7b:', 1)[0]
patterns = re.findall(r'`<project-root>/([^`]*\*[^`]*)`', destination)
assert patterns, 'collector destination step has no executable Glob search shape'

with tempfile.TemporaryDirectory(prefix='lessons-owner-fixture.') as tmp:
    root = Path(tmp)
    # A code-only plan provides no owner path. Each fitting doc must still be
    # reachable through the actual search shape and real discovery output.
    (root / 'plan.md').write_text('### Files to Modify\n- src/retry.go\n\n### Documentation Routing\nDiscovery: available\nOwners: none\nLessons: fallback\n')
    owners = ('docs/retry.md', 'docs/runtime/retry.md',
              'watch/docs/retry.md', 'packages/api/docs/runtime/retry.md')
    lesson_paths = ('docs/lessons/README.md', 'docs/lessons/retry.md',
                    'watch/docs/lessons/README.md', 'watch/docs/lessons/retry.md')
    for rel in owners:
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('# Retry\n')
    found = {str(Path(p).relative_to(root))
             for pattern in patterns
             for p in glob.glob(str(root / pattern), recursive=True)}
    missing = set(owners) - found
    assert not missing, f'Owners: none search misses fitting existing owners: {sorted(missing)}'
    command = ['bash', str(flow / 'skills/implement/scripts/doc-conventions.sh'), 'discover', str(root)]
    discovered = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    discovered_owners = {line.removeprefix('owner: ') for line in discovered.splitlines()
                         if line.startswith('owner: ')}
    assert 'lessons: fallback' in discovered.splitlines(), 'fixture must exercise no-system fallback'
    assert set(owners) <= discovered_owners, 'fixture owners must be real discover destinations'
    for rel in lesson_paths:
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('# Lesson\n')
    discovered = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    discovered_owners = {line.removeprefix('owner: ') for line in discovered.splitlines()
                         if line.startswith('owner: ')}
    assert not (set(lesson_paths) & discovered_owners), 'lesson trees must never become fallback owners'
PY
  then
    fail 'fallback owner fixture: the documented search must reach fitting owners outside a code-only plan'
  fi
}

# =====================================================================
# P11 case 5 and P19: with no lesson system in `minimal` mode, no new
# `docs/<topic>.md` and no `## Critical Rules` bullet is written, and the
# dropped lesson is listed as one line in the PR's `## Notes`.
# =====================================================================
case_minimal_no_system_no_write() {
  local offenders scoped_count status_line notes_line followup_section codex_lesson_lines
  assert_marker_once "${COLLECTOR}" "${M_MINIMAL_DROP}" "${COLLECTOR_REL} (case 5: minimal drop)"
  assert_marker_once "${LESSONS_SECTION}" "${M_MINIMAL_DROP}" "${PHASE8_REL} (## Capture Lessons, case 5: minimal drop)"
  assert_marker_once "${CODEX}" "${M_MINIMAL_DROP}" "${CODEX_REL} (case 5: minimal drop)"

  # Every collector line about creating a topic doc or appending under
  # `## Critical Rules` is scoped to `convention: none`.
  offenders="$(unscoped_creation_lines "${COLLECTOR}")"
  if [[ "${offenders}" == "ERROR" ]]; then
    fail "${COLLECTOR_REL} (case 5): grep error while scanning for unscoped creation lines"
  elif [[ -n "${offenders}" ]]; then
    fail "${COLLECTOR_REL} (case 5): a creation/append line does not name both \`convention: none\` and \`default\`: ${offenders}"
  fi
  # The same scan over Phase 8's Capture Lessons section and codex.md's lesson
  # lines: creation is `convention: none` in `default` mode only, at every site.
  offenders="$(unscoped_creation_lines "${LESSONS_SECTION}")"
  if [[ "${offenders}" == "ERROR" ]]; then
    fail "${PHASE8_REL} (case 5): grep error while scanning for unscoped creation lines"
  elif [[ -n "${offenders}" ]]; then
    fail "${PHASE8_REL} (case 5): a creation/append line does not name both \`convention: none\` and \`default\`: ${offenders}"
  fi
  codex_lesson_lines="$(grep -iF -e 'lesson' <<<"${CODEX}")"
  # An empty pre-filter (the keyword moved or vanished) would scan nothing and pass.
  assert_nonempty "${codex_lesson_lines}" "${CODEX_REL} (case 5: the lesson-line pre-filter)"
  offenders="$(unscoped_creation_lines "${codex_lesson_lines}")"
  if [[ "${offenders}" == "ERROR" ]]; then
    fail "${CODEX_REL} (case 5): grep error while scanning for unscoped creation lines"
  elif [[ -n "${offenders}" ]]; then
    fail "${CODEX_REL} (case 5): a lesson creation/append line does not name both \`convention: none\` and \`default\`: ${offenders}"
  fi
  # Positive control: the scoped creation paths still exist, so the scan is not
  # passing because they were deleted.
  scoped_count="$(marker_count "${COLLECTOR}" 'convention: none')"
  if [[ ! "${scoped_count}" =~ ^[0-9]+$ ]] || [[ "${scoped_count}" -eq 0 ]]; then
    fail "${COLLECTOR_REL} (case 5): expected the scoped fallback paths to remain under \`convention: none\` (count: ${scoped_count})"
  fi

  # P19 wiring: the collector reports a dropped line; Phase 8 turns it into a
  # run-scoped status line; Phase 9 reads the file and renders it in Notes.
  assert_contains "${OUTPUT_FENCES}" '### Dropped (minimal policy)' "${COLLECTOR_REL} (P19: the output template names the minimal-drop heading)"
  assert_contains "${LESSONS_SECTION}" '### Dropped (minimal policy)' "${PHASE8_REL} (P19: Capture Lessons reads the collector's minimal-drop heading)"
  status_line="$(extract_line "${LESSONS_SECTION}" "\$RUN_DIR/${STATUS_FILE}")"
  assert_contains "${status_line}" 'Write' "${PHASE8_REL} (P19: Capture Lessons writes the status file with the Write tool)"
  assert_contains "${status_line}" "${DROPPED_LINE}" "${PHASE8_REL} (P19: the written line shape)"
  assert_contains "${status_line}" 'overwrit' "${PHASE8_REL} (P19: a re-entry overwrites the status file)"
  assert_contains "${status_line}" 'on every entry into this sub-step' "${PHASE8_REL} (A: the write is not gated by the lesson trigger)"
  # An absent file is unknown, never none: no site may keep the old reading.
  assert_not_contains "${LESSONS_SECTION}" 'absent file means none' "${PHASE8_REL} (P19: an absent status file no longer means none)"
  assert_not_contains "${CODEX}" 'absent file means none' "${CODEX_REL} (P19: an absent status file no longer means none)"
  assert_not_contains "${PHASE9}" 'absent or empty file means no lesson' "${PHASE9_REL} (P19: an absent status file no longer means none)"
  assert_marker_once "${PHASE9}" "${M_NOTES_RENDER}" "${PHASE9_REL} (P19: Notes rendering)"
  assert_marker_once "${CODEX}" "${M_NOTES_RENDER}" "${CODEX_REL} (P19: Notes rendering)"
  notes_line="$(extract_line "${PHASE9}" "${M_NOTES_RENDER}")"
  assert_contains "${notes_line}" "${STATUS_FILE}" "${PHASE9_REL} (P19: Phase 9 names the file it reads)"
  # The lost/unreadable/unavailable renderings are asserted by the paragraph-scoped
  # F3 rows below: the old whole-document check for the lost-run-directory wording
  # passed vacuously because that string occurs elsewhere in Phase 9.
  # The dropped line never becomes a Followup ticket: it sits in the
  # `### Considered and discarded` subsection that the Followup source excludes.
  followup_section="$(extract_section "${PHASE9}" '## Followup Ticket')"
  assert_nonempty "${followup_section}" "${PHASE9_REL} (## Followup Ticket)"
  assert_contains "${followup_section}" 'excluding its `### Considered and discarded` subsection' "${PHASE9_REL} (P19: Followup creation excludes the subsection)"
  # Secrecy (#979): the lesson summary carries paths and identifiers only.
  assert_contains "${COLLECTOR}" 'repo-relative paths and identifiers only' "${COLLECTOR_REL} (P19: dropped-lesson text is secrecy-scoped)"
}

# =====================================================================
# P22: a structured lesson record whose repository validation fails surfaces
# as `fail` in the maintenance status file and in the Fix now / Stop / Push
# anyway question, never as a pass.
# =====================================================================
check_p22_lesson_validation() {
  local planner_validations repo_checks
  assert_marker_once "${COLLECTOR}" "${M_COLLECTOR_VALIDATION}" "${COLLECTOR_REL} (P22: the collector reports, never runs, the validation command)"
  assert_contains "${OUTPUT_FENCES}" '### Lesson validation' "${COLLECTOR_REL} (P22: the output template names the validation heading)"
  assert_marker_once "${LESSONS_SECTION}" "${M_LESSON_VALIDATION}" "${PHASE8_REL} (## Capture Lessons, P22: hand-off to the repository docs checks)"
  assert_marker_once "${CODEX}" "${M_LESSON_VALIDATION}" "${CODEX_REL} (P22: hand-off to the repository docs checks)"
  # The retired ambiguous wording must be gone: the collector's report is never a command source.
  assert_not_contains "${LESSONS_SECTION}" "command the collector reports only when" "${PHASE8_REL} (P22: the ambiguous run-the-reported-command wording is retired)"
  assert_not_contains "${CODEX}" "command the collector reports only when" "${CODEX_REL} (P22: the ambiguous run-the-reported-command wording is retired)"
  assert_marker_once "${MAINTENANCE_SECTION}" "${M_LESSON_FAIL}" "${PHASE8_REL} (## Maintenance Check, P22: fail is recorded and asked about)"
  assert_marker_once "${CODEX}" "${M_LESSON_FAIL}" "${CODEX_REL} (P22: fail is recorded and asked about)"
  # Mechanism wired: the question, the status file and the no-false-pass rule
  # the marker names are real steps of the same section.
  assert_contains "${MAINTENANCE_SECTION}" '**The `fail` question**' "${PHASE8_REL} (P22: the fail question exists)"
  assert_contains "${MAINTENANCE_SECTION}" 'offer exactly these options in this order: **Fix now**, **Stop**, **Push anyway**' "${PHASE8_REL} (P22: the three options)"
  assert_contains "${MAINTENANCE_SECTION}" '$RUN_DIR/maintain-status.txt' "${PHASE8_REL} (P22: the status file)"
  assert_contains "${MAINTENANCE_SECTION}" 'Never record `pass` for a check that did not run' "${PHASE8_REL} (P22: no false pass)"
  repo_checks="$(extract_line "${MAINTENANCE_SECTION}" '**Repository docs checks**: ')"
  assert_contains "${repo_checks}" '`Validations:`' "${PHASE8_REL} (P22: repository docs checks read the plan's Validations: line)"
  assert_contains "${MAINTENANCE_SECTION}" 'A non-zero exit records `fail` and enters the Fix now / Stop / Push anyway question above' "${PHASE8_REL} (P22: a non-zero exit of a repository docs check enters the question)"
  # The planner is the only source of the command.
  planner_validations="$(extract_line "${PLANNER}" '    Validations: ')"
  assert_contains "${planner_validations}" "${M_PLANNER_VALIDATION}" "${PLANNER_REL} (P22: the plan names the admission doc's validation command)"
}

# =====================================================================
# P25: the README `### Documentation policy` subsection gains a
# `Lessons fallback` paragraph.
# =====================================================================
check_p25_readme() {
  local policy_section fallback_para
  policy_section="$(extract_section "${README}" '### Documentation policy')"
  assert_nonempty "${policy_section}" "${README_REL} (### Documentation policy)"
  fallback_para="$(extract_line "${policy_section}" '**Lessons fallback.**')"
  assert_contains "${fallback_para}" 'flat `docs/<topic>.md` bullet' "${README_REL} (P25: names the flat bullet fallback)"
  assert_contains "${fallback_para}" "own lesson system takes precedence" "${README_REL} (P25: a repository lesson system takes precedence)"
}

# =====================================================================
# Acceptance round 1 (#1303): scope, owner, record, drop and skipped-validation
# rules, plus the wiring that carries a skipped lesson validation into the
# maintenance status file Phase 9 renders.
#
# Each requirement is one row: kind (contains|once), site (collector|phase8|
# codex|phase9), scope (doc | section:<heading> | line:<anchor> |
# para:<anchor>), the literal
# needle, and a unique label. `req_problems` is pure so the self-tests below
# can run it over doctored copies and prove every row would fail without its
# sentence (gotchas rules 10(1), 10(5), 29).
# =====================================================================
REQS=()
PARA_SEP=$'\x1f'
add_req() { REQS+=("$1"$'\t'"$2"$'\t'"$3"$'\t'"$4"$'\t'"$5"); }

# Row 15: the Maintain-suggestion size hint is for flat-bullet writes only.
add_req contains collector 'line:**Size check' 'flat-bullet writes only' 'R15-size-hint-scope-heading'
add_req contains collector 'line:**Size check' 'After writing flat bullets, count the rule bullets in each section you touched.' 'R15-size-hint-counts-bullets'
add_req contains collector 'line:**Size check' 'A structured record is never counted or curated here.' 'R15-structured-record-never-counted'
# Row 20: what a fitting existing owner is.
add_req contains collector 'line:A **fitting existing owner**' 'is an existing Markdown file under a `docs/` directory (root or per-project)' 'R20-owner-is-docs-markdown'
add_req contains collector 'line:A **fitting existing owner**' 'excluding the lesson trees (the admission doc and the record directory)' 'R20-owner-excludes-lesson-trees'
add_req contains collector 'line:A **fitting existing owner**' "Find it from the routing's \`Owners:\` line or with your own \`Glob\` over \`<project-root>/**/docs/**/*.md\`" 'R20-owner-found-from-owners-or-glob'
OWNER_SEARCH="When discovery is available, \`Owners:\` names the plan's doc edits, not an exhaustive destination allowlist: the collector may find and use a fitting existing owner under root or per-project \`docs/\` even when \`Owners: none\`."
OWNER_GUARD='When discovery is unavailable, its restriction to plan-named or router-named owners takes precedence over independent owner search.'
for site in collector phase8 codex; do
  add_req contains "${site}" 'line:When discovery is available, `Owners:`' "${OWNER_SEARCH}" "R20-${site}-independent-owner"
  add_req contains "${site}" 'line:When discovery is unavailable, its restriction' "${OWNER_GUARD}" "R20-${site}-unavailable-precedence"
done
add_req contains collector 'section:## Inputs' 'root and project `AGENTS.md` route to' 'R20-unavailable-reads-project-router'
add_req contains collector 'section:### Step 7a: Find the lesson destination' 'Do not descend into `.git/`, `.worktrees/`, `.plans/`, `node_modules/`, `vendor/`, ignored paths or generated build trees, and never follow symlinks outside `<project-root>`.' 'R20-owner-search-repository-boundary'
# Row 21: a structured write is exactly one new record; nothing existing is edited.
add_req contains collector 'line:Read the admission doc at' 'write exactly one new record file under' 'R21-collector-exactly-one-record'
add_req contains collector 'line:Read the admission doc at' 'Never edit the admission doc or an existing record.' 'R21-collector-never-edit'
add_req contains collector 'line:Read the admission doc at' 'drop it as `Dropped (not admitted by <path>)`' 'R21-collector-drop-line'
add_req contains collector 'section:## Output Summary' '### Dropped (not admitted by docs/lessons/README.md)' 'R21-collector-template-drop-heading'
add_req contains phase8 'line:Only when the routing reports `convention: none` in `default` mode' "to a lesson system's admission doc, or to an existing record" 'R21-phase8-never-write-existing'
add_req contains codex 'line:Lesson capture: forward' "it never writes to a lesson system's admission doc or an existing record" 'R21-codex-never-write-existing'
# Row 22: only minimal-policy drops reach the status file and the PR Notes.
add_req once collector 'doc' "${M_ONLY_MINIMAL_REACHES}" 'R22-collector-summary-only'
add_req once phase8 'section:## Capture Lessons' "${M_ONLY_MINIMAL_REACHES}" 'R22-phase8-summary-only'
add_req once codex 'doc' "${M_ONLY_MINIMAL_REACHES}" 'R22-codex-summary-only'
add_req contains phase8 "line:\$RUN_DIR/${STATUS_FILE}" 'one `Lesson dropped (minimal policy): <one-line summary>` line per entry the collector listed under `### Dropped (minimal policy)`' 'R22-phase8-copies-only-minimal-entries'
# Row 17: a skipped lesson validation is carried into the maintenance status file.
add_req once phase8 'section:## Maintenance Check' "${M_SKIPPED_WIRING}" 'R17-phase8-skipped-line-written'
add_req once phase8 'section:## Maintenance Check' "${M_SKIPPED_REDERIVE}" 'R17-phase8-skipped-line-survives-reentry'
add_req once codex 'doc' "${M_SKIPPED_WIRING}" 'R17-codex-skipped-line-written'
add_req once codex 'doc' "${M_SKIPPED_REDERIVE}" 'R17-codex-skipped-line-survives-reentry'
add_req contains phase8 'line:**Restart-safety**' 'skipped (not named in the plan)` lines carried forward above' 'R17-phase8-restart-safety-carve-out'
add_req contains phase9 'line:`skipped (<reason>)` → ' '- [ ] Docs check skipped: <label> (<reason>)' 'R17-phase9-renders-skipped-line'
add_req contains phase9 'line:Every `unavailable` status and every' 'renders as an unchecked `- [ ]` line' 'R17-phase9-skipped-unchecked'
# Finding 9: the `validation:` lines Phase 9 renders have two sources, not one.
add_req contains phase9 "line:The same file's \`validation:\` lines" "every other \`validation:\` line is a repository docs check, either one the plan named or a collector-reported lesson validation recorded as \`skipped (not named in the plan)\`" 'R17-phase9-validation-line-sources'
# Finding 3: the dropped-lesson / Considered-and-discarded paragraph of Phase 9
# renders every unavailable state. Scoped to that paragraph: the wording
# `RUN_DIR lost` also occurs in other Phase 9 paragraphs, so a whole-document check
# passed vacuously.
PARA9="para:Read \`\$RUN_DIR/lessons-status.txt\` (written by Phase 8${PARA_SEP}Any reported or deferred maintenance findings"
add_req contains phase9 "${PARA9}" 'Lesson status unavailable' 'F3-phase9-status-unavailable'
add_req contains phase9 "${PARA9}" 'cannot be read' 'F3-phase9-unreadable-file'
add_req contains phase9 "${PARA9}" 'Lesson capture unavailable' 'F3-phase9-capture-unavailable'
add_req contains phase9 "${PARA9}" 'RUN_DIR lost' 'F3-phase9-run-dir-lost-reason'
add_req once phase9 "${PARA9}" "${M_UNAVAILABLE_RENDER}" 'F1-phase9-capture-unavailable-render'
add_req once codex 'doc' "${M_UNAVAILABLE_RENDER}" 'F1-codex-capture-unavailable-render'
add_req once phase9 "${PARA9}" "${M_ABSENT_RENDER}" 'F2-phase9-absent-is-unknown'
add_req once codex 'doc' "${M_ABSENT_RENDER}" 'F2-codex-absent-is-unknown'
# Finding 1 / 2: Phase 8 always writes the file, reads it back, and records a failed capture.
add_req once phase8 'section:## Capture Lessons' "${M_STATUS_ALWAYS}" 'F2-phase8-status-always-written'
add_req once codex 'doc' "${M_STATUS_ALWAYS}" 'F2-codex-status-always-written'
add_req once phase8 'section:## Capture Lessons' "${M_STATUS_READBACK}" 'F2-phase8-status-readback'
add_req once codex 'doc' "${M_STATUS_READBACK}" 'F2-codex-status-readback'
add_req once phase8 'section:## Capture Lessons' "${M_CAPTURE_UNAVAILABLE}" 'F1-phase8-capture-unavailable-line'
add_req once codex 'doc' "${M_CAPTURE_UNAVAILABLE}" 'F1-codex-capture-unavailable-line'
# Finding 7: the skipped-validation label is cut down before it reaches a PR body.
add_req once phase8 'section:## Capture Lessons' "${M_LABEL_LIMIT}" 'F7-phase8-label-limit'
add_req once phase9 'doc' "${M_LABEL_LIMIT}" 'F7-phase9-label-limit'
add_req once codex 'doc' "${M_LABEL_LIMIT}" 'F7-codex-label-limit'
# Finding 8: the write-time limit (Phase 8) and the render-time cut-down (Phase 9) are both in codex.md.
add_req once phase8 'section:## Capture Lessons' "${M_WRITE_LIMIT}" 'F8-phase8-write-limit'
add_req once codex 'doc' "${M_WRITE_LIMIT}" 'F8-codex-write-limit'
add_req once phase9 "${PARA9}" "${M_RENDER_CUT}" 'F8-phase9-render-cut'
add_req once codex 'doc' "${M_RENDER_CUT}" 'F8-codex-render-cut'
# Round 2 A: the status-file write is exempt from the skip-by-default rule, so a
# smooth session (no lesson trigger) still writes the file and Phase 9 never renders
# a false `Lesson status unavailable`. Phase 8 names the exemption where the skip
# default (line 7) and the every-mode list (the `minimal` correctness paragraph) live.
add_req contains phase8 'line:Default action: skip.' 'except the lessons status file write in `## Capture Lessons`, which runs on every entry.' 'A-phase8-skip-default-carve-out'
add_req contains phase8 'line:Correctness work still runs in `minimal` mode.' '`## Maintenance Check`, `## Docs Status` and the lessons status file write in `## Capture Lessons` run in every mode.' 'A-phase8-every-mode-names-status-write'
add_req contains codex 'line:The lessons status file write, like the maintenance check' 'runs in every mode and is never skipped by a sub-step trigger or the `minimal` policy.' 'A-codex-every-mode-names-status-write'
# Round 2 B: a re-entry that does not relaunch the collector carries the earlier lines forward.
add_req once phase8 'section:## Capture Lessons' "${M_STATUS_CARRY}" 'B-phase8-status-carry-forward'
add_req once codex 'doc' "${M_STATUS_CARRY}" 'B-codex-status-carry-forward'
# Round 2 F: an unknown run directory skips the write; codex.md names the Write tool and the overwrite.
add_req once phase8 'section:## Capture Lessons' "${M_RUNDIR_UNKNOWN}" 'F-phase8-rundir-unknown'
add_req once codex 'doc' "${M_RUNDIR_UNKNOWN}" 'F-codex-rundir-unknown'
add_req contains codex "line:Write the run's lessons status file" "with the \`Write\` tool, overwriting any earlier copy with this entry's result when the collector ran in this entry" 'F-codex-write-tool-overwrite'
# Round 3: a status file with a line neither recognised shape is unreadable (never
# copied into a PR body), and an empty file (including a reminder-only read) is none.
add_req once phase9 "${PARA9}" "${M_UNRECOGNISED_LINE}" 'G-phase9-unrecognised-line-unreadable'
add_req once codex 'doc' "${M_UNRECOGNISED_LINE}" 'G-codex-unrecognised-line-unreadable'
add_req once phase8 'section:## Capture Lessons' "${M_EMPTY_STATUS_READ}" 'H-phase8-empty-file-is-none'
add_req once phase9 "${PARA9}" "${M_EMPTY_STATUS_READ}" 'H-phase9-empty-file-is-none'
add_req once codex 'doc' "${M_EMPTY_STATUS_READ}" 'H-codex-empty-file-is-none'

# scoped_text <content> <scope-spec> -- pure.
scoped_text() {
  local content="$1" spec="$2"
  case "${spec}" in
    doc) printf '%s' "${content}" ;;
    section:*) extract_section "${content}" "${spec#section:}" ;;
    line:*) extract_line "${content}" "${spec#line:}" ;;
    para:*)
      # `para:<anchor>` + US (0x1f) + `<stop anchor>`; a paragraph row without a
      # stop anchor is reported by req_problems (a lost blank line would widen it).
      local _body="${spec#para:}"
      if [[ "${_body}" == *"${PARA_SEP}"* ]]; then
        extract_paragraph "${content}" "${_body%%"${PARA_SEP}"*}" "${_body#*"${PARA_SEP}"}"
      else
        extract_paragraph "${content}" "${_body}"
      fi
      ;;
    *) printf '' ;;
  esac
}

# req_problems <collector> <phase8> <codex> <phase9> -- one `<label>: ...` line
# per unmet row; empty when every row holds. Pure.
req_problems() {
  local collector_doc="$1" phase8_doc="$2" codex_doc="$3" phase9_doc="$4"
  local row kind site scope needle label doc scoped count stop_count
  for row in "${REQS[@]}"; do
    IFS=$'\t' read -r kind site scope needle label <<<"${row}"
    if [[ -z "${needle}" ]]; then
      echo "${label}: empty needle (test bug)"
      continue
    fi
    case "${site}" in
      collector) doc="${collector_doc}" ;;
      phase8) doc="${phase8_doc}" ;;
      codex) doc="${codex_doc}" ;;
      phase9) doc="${phase9_doc}" ;;
      *) echo "${label}: unknown site [${site}] (test bug)"; continue ;;
    esac
    if [[ "${scope}" == para:* && "${scope}" != *"${PARA_SEP}"* ]]; then
      echo "${label}: paragraph scope [${scope}] has no stop anchor (test bug)"
      continue
    fi
    if [[ "${scope}" == para:* && -z "${scope#*"${PARA_SEP}"}" ]]; then
      echo "${label}: paragraph scope [${scope}] has an empty stop anchor (test bug)"
      continue
    fi
    scoped="$(scoped_text "${doc}" "${scope}")"
    if [[ -z "${scoped}" ]]; then
      echo "${label}: scope [${scope}] is empty in ${site}"
      continue
    fi
    if [[ "${scope}" == para:* ]]; then
      stop_count="$(marker_count "${doc}" "${scope#*"${PARA_SEP}"}")"
      if [[ "${stop_count}" != "1" ]]; then
        echo "${label}: stop anchor must occur exactly once in ${site}, found ${stop_count}: [${scope#*"${PARA_SEP}"}]"
        continue
      fi
    fi
    case "${kind}" in
      contains)
        [[ "${scoped}" == *"${needle}"* ]] || echo "${label}: missing in ${site}: [${needle}]"
        ;;
      once)
        count="$(marker_count "${scoped}" "${needle}")"
        [[ "${count}" == "1" ]] || echo "${label}: expected exactly 1 occurrence in ${site}, found ${count}: [${needle}]"
        ;;
      *) echo "${label}: unknown kind [${kind}] (test bug)" ;;
    esac
  done
}

# wiring_order_problem <maintenance-section> -- the skipped-line sentence sits
# inside the Repository docs checks paragraph (after its opening line, before
# Restart-safety), so it is part of the step that writes the status file. Pure.
wiring_order_problem() {
  local section="$1" repo_at wire_at restart_at
  repo_at="$(line_of "${section}" '**Repository docs checks**: ')"
  wire_at="$(line_of "${section}" "${M_SKIPPED_WIRING}")"
  restart_at="$(line_of "${section}" '**Restart-safety**')"
  if [[ -z "${repo_at}" || -z "${wire_at}" || -z "${restart_at}" ]]; then
    echo "could not locate the Repository docs checks line (${repo_at:-none}), the skipped-line sentence (${wire_at:-none}) and Restart-safety (${restart_at:-none})"
    return
  fi
  if [[ "${wire_at}" -le "${repo_at}" || "${wire_at}" -ge "${restart_at}" ]]; then
    echo "the skipped-line sentence (line ${wire_at}) is outside the Repository docs checks paragraph (${repo_at}..${restart_at})"
  fi
}

check_round1_rows() {
  local problems line order
  problems="$(req_problems "${COLLECTOR}" "${PHASE8}" "${CODEX}" "${PHASE9}")"
  if [[ -n "${problems}" ]]; then
    while IFS= read -r line; do
      fail "round 1: ${line}"
    done <<<"${problems}"
  fi
  order="$(wiring_order_problem "${MAINTENANCE_SECTION}")"
  if [[ -n "${order}" ]]; then
    fail "${PHASE8_REL} (R17: ${order})"
  fi
  # Mechanism wired: the sentence names the file the Repository docs checks
  # paragraph already appends to, and Phase 9 reads that same file.
  assert_contains "${MAINTENANCE_SECTION}" 'Append each result as its `validation:` line' "${PHASE8_REL} (R17: the Repository docs checks paragraph appends validation lines)"
  assert_contains "${PHASE9}" 'maintain-status.txt' "${PHASE9_REL} (R17: Phase 9 reads the maintenance status file)"
  assert_not_contains "${PHASE9}" 'is a repository docs check the plan named' "${PHASE9_REL} (R17: the retired plan-named-only claim is gone)"
  # Mechanism wired (rule 10(5)): the Phase 8 step that writes the status file
  # and the Phase 9 step that reads it name the same file, and Phase 8 reads the
  # write back before Phase 9 can see an absent file.
  assert_contains "${LESSONS_SECTION}" "\$RUN_DIR/${STATUS_FILE}" "${PHASE8_REL} (F2: Phase 8 names the status file it writes)"
  assert_contains "${PHASE9}" "\$RUN_DIR/${STATUS_FILE}" "${PHASE9_REL} (F2: Phase 9 names the status file it reads)"
}

case_tdd_red_green_no_lessons
case_admitted_rework_one_entry
case_already_owned_dropped
case_structured_record_in_record_dir
case_fallback_owner_outside_plan
case_minimal_no_system_no_write
check_p22_lesson_validation
check_p25_readme
check_round1_rows

# =====================================================================
# Non-vacuity self-tests.
# =====================================================================
SELFTEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/lessons-routing-contract.XXXXXX")" || { echo "lessons-routing-contract.test.sh: mktemp failed." >&2; exit 2; }
[[ -n "${SELFTEST_DIR}" && -d "${SELFTEST_DIR}" ]] || { echo "lessons-routing-contract.test.sh: mktemp produced no directory." >&2; exit 2; }
trap 'rm -rf -- "${SELFTEST_DIR}"' EXIT

# (1) An unscoped creation line injected into a copy of the collector is
# caught; the unmodified collector reports nothing.
CLEAN_HITS="$(unscoped_creation_lines "${COLLECTOR}")"
if [[ -n "${CLEAN_HITS}" ]]; then
  fail "self-test: the unmodified collector reports unscoped creation lines: ${CLEAN_HITS}"
fi
INJECTED_COLLECTOR="${COLLECTOR}"$'\n''When findings cluster, create a new topic doc for them.'$'\n''Append under `## Critical Rules` when the finding is project-wide.'
INJECTED_HITS="$(unscoped_creation_lines "${INJECTED_COLLECTOR}")"
if [[ -z "${INJECTED_HITS}" || "${INJECTED_HITS}" == "ERROR" ]]; then
  fail "self-test: the scoping scan failed to catch injected unscoped creation lines (vacuous)"
fi
SCOPED_INJECTION="${COLLECTOR}"$'\n''Only under `convention: none` in `default` mode, create a new topic doc.'
SCOPED_HITS="$(unscoped_creation_lines "${SCOPED_INJECTION}")"
if [[ -n "${SCOPED_HITS}" ]]; then
  fail "self-test: a line naming convention: none and default must not be reported: ${SCOPED_HITS}"
fi
# A creation line naming `convention: none` but not `default` is unscoped: it
# would allow the creation in minimal mode. Naming `default` alone is unscoped too.
NO_DEFAULT_INJECTION="${COLLECTOR}"$'\n''Only under `convention: none`, create a new topic doc.'
NO_DEFAULT_HITS="$(unscoped_creation_lines "${NO_DEFAULT_INJECTION}")"
if [[ -z "${NO_DEFAULT_HITS}" || "${NO_DEFAULT_HITS}" == "ERROR" ]]; then
  fail "self-test: a creation line naming convention: none but no default was not caught (vacuous)"
fi
NO_NONE_INJECTION="${COLLECTOR}"$'\n''In `default` mode, create a new topic doc.'
NO_NONE_HITS="$(unscoped_creation_lines "${NO_NONE_INJECTION}")"
if [[ -z "${NO_NONE_HITS}" || "${NO_NONE_HITS}" == "ERROR" ]]; then
  fail "self-test: a creation line naming default but not convention: none was not caught (vacuous)"
fi

# (1b) Reworded creation lines are caught by the extra stems: a standalone
# "appended under `## Critical Rules`" line without scope, a bare "topic doc" line,
# an "append ... Critical Rules" line, and a scoped line whose `default` is plain
# prose rather than the backticked mode token.
REWORD_APPENDED="${COLLECTOR}"$'\n''A project-wide finding is appended under `## Critical Rules`.'
REWORD_HITS="$(unscoped_creation_lines "${REWORD_APPENDED}")"
if [[ -z "${REWORD_HITS}" || "${REWORD_HITS}" == "ERROR" ]]; then
  fail "self-test: a reworded 'appended under' line without scope was not caught (vacuous)"
fi
REWORD_TOPIC="${COLLECTOR}"$'\n''Findings that cluster go to a topic doc.'
REWORD_HITS="$(unscoped_creation_lines "${REWORD_TOPIC}")"
if [[ -z "${REWORD_HITS}" || "${REWORD_HITS}" == "ERROR" ]]; then
  fail "self-test: a bare 'topic doc' line without scope was not caught (vacuous)"
fi
REWORD_APPEND_RULES="${COLLECTOR}"$'\n''Then append the finding to the Critical Rules of the project.'
REWORD_HITS="$(unscoped_creation_lines "${REWORD_APPEND_RULES}")"
if [[ -z "${REWORD_HITS}" || "${REWORD_HITS}" == "ERROR" ]]; then
  fail "self-test: an 'append ... Critical Rules' line without scope was not caught (vacuous)"
fi
PROSE_DEFAULT="${COLLECTOR}"$'\n''Under `convention: none` and by default, create a new topic doc.'
REWORD_HITS="$(unscoped_creation_lines "${PROSE_DEFAULT}")"
if [[ -z "${REWORD_HITS}" || "${REWORD_HITS}" == "ERROR" ]]; then
  fail "self-test: a line naming a prose 'default' instead of the backticked mode token was not caught (vacuous)"
fi

# (2) The dedup-order check holds on the real collector and catches a removed
# dedup marker and a dedup step moved after the writes.
if [[ -n "$(dedup_order_problem "${PROCESS_SECTION}")" ]]; then
  fail "self-test baseline: the collector's dedup order already fails: $(dedup_order_problem "${PROCESS_SECTION}")"
fi
WITHOUT_DEDUP="${PROCESS_SECTION//"${M_DEDUP}"/}"
if [[ -z "$(dedup_order_problem "${WITHOUT_DEDUP}")" ]]; then
  fail "self-test: removing the dedup marker was not caught (vacuous)"
fi
DEDUP_LAST="${WITHOUT_DEDUP}"$'\n'"${M_DEDUP}"
if [[ -z "$(dedup_order_problem "${DEDUP_LAST}")" ]]; then
  fail "self-test: a dedup step placed after the write steps was not caught (vacuous)"
fi

# (3) The marker counter reports zero for an absent marker, one for a present
# one and two for two occurrences on one line (grep -oF, not -cF).
if [[ "$(marker_count 'nothing relevant here' "${M_DEDUP}")" != "0" ]]; then
  fail "self-test: marker_count must report 0 for an absent marker"
fi
if [[ "$(marker_count "prefix ${M_DEDUP} suffix" "${M_DEDUP}")" != "1" ]]; then
  fail "self-test: marker_count must report 1 for a present marker"
fi
if [[ "$(marker_count "${M_DEDUP} ${M_DEDUP}" "${M_DEDUP}")" != "2" ]]; then
  fail "self-test: marker_count must count two occurrences on one line as 2"
fi

# (3b) The paragraph extractor stops at the blank line and ignores an earlier
# occurrence of the same words outside the paragraph.
PARA_DOC=$'outside RUN_DIR lost\n\nAnchor line one\nsecond line\n\nafter RUN_DIR lost\n'
PARA_BODY="$(extract_paragraph "${PARA_DOC}" 'Anchor line')"
assert_contains "${PARA_BODY}" 'second line' "self-test: extract_paragraph keeps the paragraph's later lines"
assert_not_contains "${PARA_BODY}" 'RUN_DIR lost' "self-test: extract_paragraph excludes text outside the paragraph"

# (3c) The paragraph extractor fails closed. With a stop anchor, a lost
# blank-line terminator still ends the paragraph at the stop line; a paragraph
# that runs to the end of input, or whose terminating blank line is not followed
# by the stop anchor, extracts nothing instead of widening into the next block.
STOP_DOC=$'Anchor line\nsecond line\n\nNext paragraph\nwith needle\n\n'
STOP_BODY="$(extract_paragraph "${STOP_DOC}" 'Anchor line' 'Next paragraph')"
assert_contains "${STOP_BODY}" 'second line' "self-test: extract_paragraph with a stop anchor keeps the paragraph"
assert_not_contains "${STOP_BODY}" 'needle' "self-test: extract_paragraph with a stop anchor excludes the next paragraph"
STOP_LOST_BLANK=$'Anchor line\nsecond line\nNext paragraph\nwith needle\n\n'
STOP_BODY="$(extract_paragraph "${STOP_LOST_BLANK}" 'Anchor line' 'Next paragraph')"
assert_contains "${STOP_BODY}" 'second line' "self-test: extract_paragraph still ends at the stop line when the blank line is lost"
assert_not_contains "${STOP_BODY}" 'needle' "self-test: a lost blank line must not widen the paragraph into the next one"
STOP_LOST_BOTH=$'Anchor line\nsecond line\nanother paragraph\nwith needle\n\n'
if [[ -n "$(extract_paragraph "${STOP_LOST_BOTH}" 'Anchor line' 'Next paragraph')" ]]; then
  fail "self-test: a paragraph whose blank line and stop anchor are both lost must extract nothing (vacuous)"
fi
if [[ -n "$(extract_paragraph $'Anchor line\nsecond line' 'Anchor line')" ]]; then
  fail "self-test: a paragraph that runs to the end of input must extract nothing"
fi

# (3c-2) The extractor rejects an empty stop anchor instead of silently falling
# back to the no-stop mode, reads backslashes in its anchors literally, and the
# `para:` row check rejects an empty stop anchor and a stop anchor that is not
# unique in its target document.
EMPTY_STOP_DOC=$'Anchor line\nsecond line\n\nNext paragraph\nwith needle\n\n'
EMPTY_STOP_BODY="$(extract_paragraph "${EMPTY_STOP_DOC}" 'Anchor line' '')"
if [[ -n "${EMPTY_STOP_BODY}" ]]; then
  fail "self-test: extract_paragraph with an empty stop anchor must extract nothing, not fall back to no-stop mode (vacuous)"
fi
BACKSLASH_DOC=$'Path C:\\new line\nsecond line\n\nNext \\paragraph\nwith needle\n\n'
BACKSLASH_BODY="$(extract_paragraph "${BACKSLASH_DOC}" 'C:\new line' 'Next \paragraph')"
assert_contains "${BACKSLASH_BODY}" 'second line' "self-test: extract_paragraph reads a backslash in its anchors literally"
assert_not_contains "${BACKSLASH_BODY}" 'needle' "self-test: extract_paragraph with backslash anchors still stops at the stop line"
SAVED_REQS=("${REQS[@]}")
REQS=("contains"$'\t'"phase9"$'\t'"para:Anchor line${PARA_SEP}"$'\t'"second line"$'\t'"ZZ-empty-stop")
EMPTY_STOP_PROBLEMS="$(req_problems "" "" "" "${EMPTY_STOP_DOC}")"
if [[ "${EMPTY_STOP_PROBLEMS}" != *"ZZ-empty-stop: "*"empty stop anchor"* ]]; then
  fail "self-test: a para row with an empty stop anchor was not rejected (vacuous): ${EMPTY_STOP_PROBLEMS}"
fi
REQS=("contains"$'\t'"phase9"$'\t'"para:Anchor line${PARA_SEP}Next paragraph"$'\t'"second line"$'\t'"ZZ-once-stop")
ONCE_STOP_PROBLEMS="$(req_problems "" "" "" "${EMPTY_STOP_DOC}")"
if [[ -n "${ONCE_STOP_PROBLEMS}" ]]; then
  fail "self-test baseline: a para row whose stop anchor occurs once must pass: ${ONCE_STOP_PROBLEMS}"
fi
DUP_STOP_DOC="${EMPTY_STOP_DOC}"$'Next paragraph again\n'
DUP_STOP_PROBLEMS="$(req_problems "" "" "" "${DUP_STOP_DOC}")"
if [[ "${DUP_STOP_PROBLEMS}" != *"ZZ-once-stop: stop anchor must occur exactly once"* ]]; then
  fail "self-test: a para row whose stop anchor occurs twice was not rejected (vacuous): ${DUP_STOP_PROBLEMS}"
fi
REQS=("${SAVED_REQS[@]}")

# (3d) Paragraph-scoped rows are really scoped (finding E). For every `para:` row
# the needle is injected OUTSIDE the paragraph (a new trailing paragraph) and
# deleted INSIDE it: the row must be reported under its own label, so a whole-
# document pass cannot satisfy it. Two further variants lose the paragraph's
# blank-line terminator: with the stop line intact the scope still ends there, and
# with the stop anchor gone too the row fails closed. Each variant is reported
# under that row's own label.
PARA_ROWS_SEEN=0
for ROW_INDEX in "${!REQS[@]}"; do
  IFS=$'\t' read -r PS_KIND PS_SITE PS_SCOPE PS_NEEDLE PS_LABEL <<<"${REQS[ROW_INDEX]}"
  [[ "${PS_SCOPE}" == para:* ]] || continue
  PARA_ROWS_SEEN=$((PARA_ROWS_SEEN+1))
  case "${PS_SITE}" in
    collector) PS_DOC="${COLLECTOR}" ;;
    phase8) PS_DOC="${PHASE8}" ;;
    codex) PS_DOC="${CODEX}" ;;
    phase9) PS_DOC="${PHASE9}" ;;
    *) fail "self-test: para row ${PS_LABEL} has unknown site ${PS_SITE}"; continue ;;
  esac
  PS_STOP="${PS_SCOPE#*"${PARA_SEP}"}"
  PS_PARA="$(scoped_text "${PS_DOC}" "${PS_SCOPE}")"
  if [[ -z "${PS_PARA}" ]]; then
    fail "self-test: para row ${PS_LABEL} extracts an empty paragraph from the real doc"
    continue
  fi
  PS_INNER_CLEARED="${PS_PARA//"${PS_NEEDLE}"/}"
  PS_OUTSIDE=$'\n\n'"${PS_NEEDLE}"$'\n'
  # (i) needle outside the paragraph, deleted inside.
  PS_DOC_I="${PS_DOC/"${PS_PARA}"/"${PS_INNER_CLEARED}"}${PS_OUTSIDE}"
  # (ii-a) blank terminator lost, stop line intact and carrying the needle after it.
  PS_DOC_IIA="${PS_DOC/"${PS_PARA}"$'\n\n'/"${PS_INNER_CLEARED}"$'\n'}"
  PS_DOC_IIA="${PS_DOC_IIA/"${PS_STOP}"/"${PS_STOP} ${PS_NEEDLE}"}"
  # (ii-b) blank terminator lost AND the stop anchor gone: the merged next block carries the needle.
  PS_DOC_IIB="${PS_DOC/"${PS_PARA}"$'\n\n'/"${PS_INNER_CLEARED}"$'\n'}"
  PS_DOC_IIB="${PS_DOC_IIB/"${PS_STOP}"/"${PS_NEEDLE}"}"
  for PS_VARIANT in I IIA IIB; do
    case "${PS_VARIANT}" in
      I) PS_VDOC="${PS_DOC_I}" ;;
      IIA) PS_VDOC="${PS_DOC_IIA}" ;;
      IIB) PS_VDOC="${PS_DOC_IIB}" ;;
    esac
    if [[ "${PS_VDOC}" == "${PS_DOC}" ]]; then
      fail "self-test: para row ${PS_LABEL} variant ${PS_VARIANT} did not alter the document (test bug)"
      continue
    fi
    PS_ARGS=("${COLLECTOR}" "${PHASE8}" "${CODEX}" "${PHASE9}")
    case "${PS_SITE}" in
      collector) PS_ARGS[0]="${PS_VDOC}" ;;
      phase8) PS_ARGS[1]="${PS_VDOC}" ;;
      codex) PS_ARGS[2]="${PS_VDOC}" ;;
      phase9) PS_ARGS[3]="${PS_VDOC}" ;;
    esac
    PS_PROBLEMS="$(req_problems "${PS_ARGS[@]}")"
    if [[ "${PS_PROBLEMS}" != *"${PS_LABEL}: "* ]]; then
      fail "self-test: para row ${PS_LABEL} variant ${PS_VARIANT} (needle outside / blank line lost) passed vacuously"
    fi
  done
done
if [[ "${PARA_ROWS_SEEN}" -lt 5 ]]; then
  fail "self-test: expected the paragraph-scoped rows to remain (found ${PARA_ROWS_SEEN})"
fi

# (4) The fence-aware extractors do not start or end a section on a fenced
# heading look-alike, and the fence-body extractor drops the fence lines.
FENCE_DOC=$'## Alpha\nbefore\n```\n## Not A Heading\n```\nafter\n## Beta\nbeta body\n'
ALPHA_BODY="$(extract_section "${FENCE_DOC}" '## Alpha')"
assert_contains "${ALPHA_BODY}" 'after' "self-test: extract_section survives a fenced heading look-alike"
assert_not_contains "${ALPHA_BODY}" 'beta body' "self-test: extract_section stops at the next real heading"
FENCE_BODIES="$(extract_fence_bodies $'text\n```\ninside\n```\noutside\n')"
assert_contains "${FENCE_BODIES}" 'inside' "self-test: extract_fence_bodies keeps fenced lines"
assert_not_contains "${FENCE_BODIES}" 'outside' "self-test: extract_fence_bodies drops unfenced lines"

# (5) Round-1 rows: the real docs satisfy every row (baseline), and removing a
# row's needle from its own site is reported under that row's label -- not
# merely "some problem" -- so no row is vacuous. A `once` row is also caught
# when its sentence is duplicated.
ROW_LABELS_SEEN=0
for ROW_INDEX in "${!REQS[@]}"; do
  IFS=$'\t' read -r ST_KIND ST_SITE ST_SCOPE ST_NEEDLE ST_LABEL <<<"${REQS[ROW_INDEX]}"
  ROW_LABELS_SEEN=$((ROW_LABELS_SEEN+1))
  ST_COLLECTOR="${COLLECTOR}"; ST_PHASE8="${PHASE8}"; ST_CODEX="${CODEX}"; ST_PHASE9="${PHASE9}"
  case "${ST_SITE}" in
    collector) ST_COLLECTOR="${COLLECTOR//"${ST_NEEDLE}"/}" ;;
    phase8) ST_PHASE8="${PHASE8//"${ST_NEEDLE}"/}" ;;
    codex) ST_CODEX="${CODEX//"${ST_NEEDLE}"/}" ;;
    phase9) ST_PHASE9="${PHASE9//"${ST_NEEDLE}"/}" ;;
  esac
  ST_PROBLEMS="$(req_problems "${ST_COLLECTOR}" "${ST_PHASE8}" "${ST_CODEX}" "${ST_PHASE9}")"
  if [[ "${ST_PROBLEMS}" != *"${ST_LABEL}: "* ]]; then
    fail "self-test: removing the sentence of row ${ST_LABEL} was not reported under that row (vacuous)"
  fi
  if [[ "${ST_KIND}" == "once" ]]; then
    ST_DOUBLE="${ST_NEEDLE}"$'\n'"${ST_NEEDLE}"
    ST_COLLECTOR="${COLLECTOR}"; ST_PHASE8="${PHASE8}"; ST_CODEX="${CODEX}"; ST_PHASE9="${PHASE9}"
    case "${ST_SITE}" in
      collector) ST_COLLECTOR="${COLLECTOR//"${ST_NEEDLE}"/"${ST_DOUBLE}"}" ;;
      phase8) ST_PHASE8="${PHASE8//"${ST_NEEDLE}"/"${ST_DOUBLE}"}" ;;
      codex) ST_CODEX="${CODEX//"${ST_NEEDLE}"/"${ST_DOUBLE}"}" ;;
      phase9) ST_PHASE9="${PHASE9//"${ST_NEEDLE}"/"${ST_DOUBLE}"}" ;;
    esac
    ST_PROBLEMS="$(req_problems "${ST_COLLECTOR}" "${ST_PHASE8}" "${ST_CODEX}" "${ST_PHASE9}")"
    if [[ "${ST_PROBLEMS}" != *"${ST_LABEL}: "* ]]; then
      fail "self-test: a duplicated sentence of row ${ST_LABEL} was not reported under that row (vacuous)"
    fi
  fi
done
if [[ "${ROW_LABELS_SEEN}" -lt 45 ]]; then
  fail "self-test: expected the round-1 table to keep its rows (found ${ROW_LABELS_SEEN})"
fi
if [[ -z "$(req_problems "" "" "" "")" ]]; then
  fail "self-test: empty docs satisfied the round-1 rows (vacuous)"
fi

# (6) The skipped-line sentence must sit inside the Repository docs checks
# paragraph: removing it, or moving it after Restart-safety, is caught.
if [[ -n "$(wiring_order_problem "${MAINTENANCE_SECTION}")" ]]; then
  fail "self-test baseline: the skipped-line sentence is outside the Repository docs checks paragraph: $(wiring_order_problem "${MAINTENANCE_SECTION}")"
fi
if [[ -z "$(wiring_order_problem "${MAINTENANCE_SECTION//"${M_SKIPPED_WIRING}"/}")" ]]; then
  fail "self-test: removing the skipped-line sentence was not caught by the order check (vacuous)"
fi
if [[ -z "$(wiring_order_problem "${MAINTENANCE_SECTION//"${M_SKIPPED_WIRING}"/}"$'\n'"${M_SKIPPED_WIRING}")" ]]; then
  fail "self-test: a skipped-line sentence moved after Restart-safety was not caught (vacuous)"
fi

echo "lessons-routing-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
