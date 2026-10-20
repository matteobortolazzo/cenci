#!/usr/bin/env bash
# Contract test for ticket #523 -- minimum-sufficient solution analysis in the
# planner, the code reviewer and the structure analyzer, plus the Codex mirrors.
#
# What it pins, file by file:
#   - agents/planner.md: a `#### Minimum-sufficient ladder` block inside
#     `### Alternatives Considered` (six rungs in order, a stop rule, a
#     ticket-tied justification line for new things, no restated routing), the
#     `### Files to Create` entry template, the conditional
#     `Sibling/shared-path audit:` line under `### Risks`, and the unchanged
#     `For UI/frontend work` paragraph the `Repo reuse:` rung points to.
#   - agents/code-reviewer.md: a `## Simplification Findings` section and one
#     `## Review Checklist` item for the sibling audit.
#   - agents/structure-analyzer.md: a step before any split or extraction that
#     weighs deletion, reuse, inlining and a native capability in that order,
#     the widened `**Suggestion**` values, and the cohesive-file bullet kept
#     word for word under `## What NOT to Flag`.
#   - templates/agents-md-codex.md and the three Codex role templates under
#     templates/codex/agent-roles/: each states its own rule.
#   - The precedence sentence appears exactly once in each of the 7 sources.
#   - Each installed <repo>/.codex/agents/{planner,critical-reviewer,
#     structure-analyzer}.toml is byte-identical to its template (cmp -s).
#
# Follows the idiom of flow/tests/code-reviewer-failure-scenario.test.sh
# (require_doc / failures= counter / exact single-source-line markers, never
# generic keywords), flow/tests/agent-shared-reference-contract.test.sh
# (fence-aware section extraction, `grep -oF | wc -l` occurrence counts,
# mktemp -d self-tests with a cleanup trap) and flow/tests/doc-routing-contract.test.sh
# (cmp -s template/installed pairs). See flow/docs/shell-scripting-gotchas.md.
#
# Every assertion lives in check_tree <flow-root> <repo-root>. The mutation
# self-test copies the ten covered files into a mktemp -d tree and checks that
# an unmodified copy reports the same failures as the real tree (0 once the
# prompts carry the rules) and that every single mutation -- one positive
# marker removed, a stray second precedence sentence, a rung swap, an `owner:`
# injection into the ladder, a drift in an installed TOML copy -- is caught.
# The mutation rounds only prove anything against a passing base tree, so they
# are reported as SKIP (never silently passed) while the base tree still fails.
#
# The markers deliberately exclude <example> blocks and shell-discipline prose,
# so ticket #693's boilerplate cleanup cannot break this test.
#
# No `read_*`-named helpers (flow/tests/read-helper-purity-contract.test.sh);
# no permission-removing chmod (flow/tests/root-safe-perms-contract.test.sh).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "minimum-sufficient-solution-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "minimum-sufficient-solution-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_DIR="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "minimum-sufficient-solution-contract.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# Safety net for the mktemp directories below: removes only a recorded,
# non-empty, non-symlink directory, so an unset/empty path never reaches rm.
TMP_DIRS=()
cleanup_tmp() {
  local d
  for d in "${TMP_DIRS[@]+"${TMP_DIRS[@]}"}"; do
    if [[ -n "${d}" && -d "${d}" && ! -L "${d}" ]]; then
      rm -rf -- "${d}"
    fi
  done
  return 0
}
trap cleanup_tmp EXIT

# --- assertion helpers --------------------------------------------------------

# require_doc <result-var> <absolute-path> -- assigns the file's content, or
# fails closed (returns 1) when it is missing/unreadable, so a missing file can
# never masquerade as empty content and satisfy a negative assertion.
require_doc() {
  local -n _doc_out="$1"
  local _doc_path="$2" _doc_content
  if [[ ! -f "${_doc_path}" ]] || ! _doc_content="$(cat "${_doc_path}")"; then
    fail "doc not found/unreadable: ${_doc_path}"
    _doc_out=""
    return 1
  fi
  _doc_out="${_doc_content}"
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

assert_not_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty forbidden pattern (test bug)"; return; }
  [[ "${content}" != *"${pattern}"* ]] || fail "${label}: forbidden text present: [${pattern}]"
}

# count_in <content> <needle> -- occurrences (not lines) of a fixed string.
count_in() {
  printf '%s' "$1" | grep -oF -- "$2" | wc -l | tr -d '[:space:]'
}

assert_count() {
  local content="$1" needle="$2" expected="$3" label="$4" actual
  [[ -n "${needle}" ]] || { fail "${label}: empty counted pattern (test bug)"; return; }
  actual="$(count_in "${content}" "${needle}")"
  [[ "${actual}" -eq "${expected}" ]] || fail "${label}: expected ${expected} occurrence(s) of [${needle}], found ${actual}"
}

# --- pure extractors (no fail() side effects; safe inside $(...)) --------------
# Values travel through ENVIRON, never `awk -v`, which would interpret backslashes.

# exact_line_numbers <content> <line> -- 1-based numbers of lines equal to <line>.
exact_line_numbers() {
  printf '%s\n' "$1" | L="$2" awk '$0 == ENVIRON["L"] { print NR }'
}

# prefix_line_numbers <content> <prefix> -- numbers of lines starting with <prefix>.
prefix_line_numbers() {
  printf '%s\n' "$1" | P="$2" awk 'index($0, ENVIRON["P"]) == 1 { print NR }'
}

# contains_line_numbers <content> <needle> -- numbers of lines containing <needle>.
contains_line_numbers() {
  printf '%s\n' "$1" | P="$2" awk 'index($0, ENVIRON["P"]) > 0 { print NR }'
}

# line_with_prefix <content> <prefix> -- the first line starting with <prefix>.
line_with_prefix() {
  printf '%s\n' "$1" | P="$2" awk 'index($0, ENVIRON["P"]) == 1 { print; exit }'
}

# section_of <content> <heading-line> -- the heading's section up to the next
# heading of the same or a shallower level. Fence-aware: a heading-shaped line
# inside a fenced block never ends the section.
section_of() {
  printf '%s\n' "$1" | H="$2" awk '
    BEGIN { h = ENVIRON["H"]; match(h, /^#+/); lvl = RLENGTH }
    /^```/ { infence = !infence }
    !infence && /^#+ / {
      match($0, /^#+/); l = RLENGTH
      if (on && l <= lvl) exit
      if (!on && $0 == h) on = 1
    }
    on { print }
  '
}

# indented_block <content> <start-line-exact> <end-line-ere> -- the lines after
# the first line equal to <start> up to (excluding) the next line matching
# <end>. Indent-aware (the planner template is a 4-space indented block, so its
# headings are `    ### ...`) and fence-aware (an end match inside a fence is
# ignored). Prints nothing when the start is absent or the end never follows.
indented_block() {
  printf '%s\n' "$1" | S="$2" E="$3" awk '
    BEGIN { s = ENVIRON["S"]; e = ENVIRON["E"] }
    /^[ ]*```/ { infence = !infence }
    !on { if ($0 == s) on = 1; next }
    on && !infence && $0 ~ e { found = 1; exit }
    on { buf = buf $0 "\n" }
    END { if (found) printf "%s", buf }
  '
}

# count_lines <text> -- number of non-empty lines.
count_lines() {
  printf '%s' "$1" | grep -c .
}

# pos_of <string> <needle> -- 0-based offset of the first <needle>, or -1.
pos_of() {
  local pre="${1%%"$2"*}"
  if [[ "${pre}" == "$1" ]]; then printf -- '-1'; else printf '%s' "${#pre}"; fi
}

# one_line <result-var> <content> <exact-line> <label> -- sets <result-var> to
# the line number of the single line equal to <exact-line>; fails (returns 1)
# unless there is exactly one such line.
one_line() {
  local -n _one_out="$1"
  local content="$2" line="$3" label="$4" nums count
  nums="$(exact_line_numbers "${content}" "${line}")"
  count="$(count_lines "${nums}")"
  if [[ "${count}" -ne 1 ]]; then
    fail "${label}: expected exactly 1 line equal to [${line}], found ${count}"
    _one_out=""
    return 1
  fi
  _one_out="${nums}"
}

# --- fixed data: the marker contract -------------------------------------------

PREC='Security, accessibility, data integrity, correctness, approved-plan decisions, and behavior-first testing outrank code-size reduction.'

PLANNER='agents/planner.md'
REVIEWER='agents/code-reviewer.md'
ANALYZER='agents/structure-analyzer.md'
CODEX_MD='templates/agents-md-codex.md'
ROLE_DIR='templates/codex/agent-roles'
TOML_PLANNER="${ROLE_DIR}/planner.toml"
TOML_REVIEWER="${ROLE_DIR}/critical-reviewer.toml"
TOML_ANALYZER="${ROLE_DIR}/structure-analyzer.toml"

# The sources whose precedence sentence is counted (installed TOML copies are
# covered through the cmp pairs).
PRECEDENCE_SOURCES=("${PLANNER}" "${REVIEWER}" "${ANALYZER}" "${CODEX_MD}" "${TOML_PLANNER}" "${TOML_REVIEWER}" "${TOML_ANALYZER}")
ROLES=(planner critical-reviewer structure-analyzer)

# Planner -- ladder block (each line keeps the 4-space template indent).
P_LADDER_H='    #### Minimum-sufficient ladder'
P_STOP=$'    Walk the rungs in order and stop at the first rung that is sufficient; write `n/a` for every rung below it.'
RUNGS=('Necessity:' 'Repo reuse:' 'Stdlib:' 'Platform-native:' 'Installed dependency:' 'New code:')
P_DEPS='New dependencies/frameworks/abstractions: none | <item> — <ticket-tied justification>'
P_NONE='Write `New dependencies/frameworks/abstractions: none` when the plan adds none; no justification is required then.'
P_DOCS='Documentation edits follow `### Documentation Routing` below; this ladder does not restate them.'
P_UI_POINTER='`For UI/frontend work`'
# Planner -- Files to Create and Risks.
P_CREATE='- `path/to/new/file` — <purpose>; justification: <ticket-tied reason>'
P_EMPTY='Leave this section empty when the plan creates no file.'
P_AUDIT='Sibling/shared-path audit: <`<path or symbol>` — <result>, one per sibling caller or shared path checked>'
P_AUDIT_REQ=$'Required when the plan fixes a defect\'s root cause: list every sibling caller or shared path checked and the result for each.'
P_AUDIT_OMIT=$'Omit this line when the plan does not fix a defect\'s root cause.'
# The For UI/frontend work paragraph, pinned verbatim (it must stay unchanged).
UI_PARAGRAPH="$(cat <<'EOF'
**For UI/frontend work**: read the applicable `AGENTS.md`'s `## UI Conventions` section (it is
bundled into the plan's `## Project Context`) and plan against it. It names the repo's component
library and its browsable catalog. Plan to reuse existing components; propose a new one only when
nothing in the library covers the need, and when you do, say which library it belongs in and why
the existing components fall short. Name the specific components the plan will reuse in
`### Implementation Order`, so the implementer is not left to rediscover them.
EOF
)"

# Code reviewer -- the new section, its tags, bullets and sentences.
R_HEADING='## Simplification Findings'
R_TAGS=('`delete`' '`stdlib`' '`native`' '`yagni`' '`shrink`')
R_B1='- Location: the file and line of the code that can be simplified.'
R_B2='- Removable complexity: the code, file, dependency, or abstraction that can be removed.'
R_B3='- Sufficient replacement: the deletion, existing code, standard-library call, or native capability that covers the need.'
R_B4='- Failure or unmet requirement: the concrete failure or unmet requirement the current form causes.'
R_MAP=$'Map a simplification finding onto the existing fields: prefix **Issue** with the tag (for example `[yagni]`) and name the removable complexity there, put the sufficient replacement in **Fix**, and put the concrete failure or unmet requirement in **Failure scenario**.'
R_SCORE=$'Score it with the existing rubric: Should Fix (75–89), or Must Fix only when it meets that tier\'s existing bar.'
R_FEWER='A preference for fewer lines or files alone is never a merge blocker.'
R_NOFAIL='A simplification finding with no concrete failure or unmet requirement scores 25 or below and is omitted from the output.'
R_PLAN='A simplification finding that contradicts an approved-plan decision (for example, a file the plan created on purpose) is not reported as Must Fix or Should Fix.'
R_NIT='Record it at most as a Nitpick that names the plan decision.'
R_LSP='LSP-detected findings keep their `## LSP Awareness` mappings.'
R_CHECK=$'- [ ] A root-cause fix carries the plan\'s `Sibling/shared-path audit:` line, and the diff handles every sibling caller or shared path it flags'

# Structure analyzer.
A_STEP4=$'4. Before suggesting any split or extraction, consider in this order: deletion, reuse of an existing module, inlining a single-use indirection, and a native capability of the language, framework or platform; suggest a split or extraction only when none of them is sufficient.'
A_STEP5='5. For test files, check organization patterns and fixture usage'
A_STEP6='6. Analyze import/dependency patterns for structural issues'
A_STEP7='7. Prioritize findings by impact: larger files and more tangled dependencies rank higher'
A_SUG_HIGH='- **Suggestion**: Delete | Reuse <path> | Inline | Native <capability> | Split into:'
A_SUG_MED='- **Suggestion**: Delete | Reuse <path> | Inline | Native <capability> | <reorganization approach>'
A_SPLIT_SUB='  - `path/to/file-a` — <responsibility A>'
A_COHESIVE='- Files that are inherently cohesive despite being large (e.g., a single large component with its template, a comprehensive test suite for a complex module)'

# Codex self-review (agents-md-codex.md, under **Code-quality review.**).
C_LEAD='- **Simplification findings**:'
C_FINDINGS_LEAD='- **Findings**:'
C_REQ='the location, the removable complexity, the sufficient replacement, and the concrete failure or unmet requirement'

# Codex role templates (single-line developer_instructions strings).
TP_LADDER='Walk the minimum-sufficient ladder in order and stop at the first sufficient rung: Necessity, Repo reuse, Stdlib, Platform-native, Installed dependency, New code.'
TP_JUST='Every new file, dependency, framework, and single-implementation abstraction needs a justification tied to the ticket.'
TC_SHAPE='A simplification finding may carry a tag (delete, stdlib, native, yagni, or shrink) and names the location, the removable complexity, the sufficient replacement, and the concrete failure or unmet requirement the current form causes.'
TS_ORDER='Before suggesting any split or extraction, consider deletion, reuse of an existing module, inlining a single-use indirection, and a native capability, in that order; suggest a split or extraction only when none of them is sufficient.'

# The marker registry: every positive marker, asserted against its whole file by
# check_tree and removed one at a time by the mutation self-test.
MK_FILE=(); MK_LABEL=(); MK_TEXT=()
reg() { MK_FILE+=("$1"); MK_LABEL+=("$2"); MK_TEXT+=("$3"); }

reg "${PLANNER}" 'ladder heading' "${P_LADDER_H}"
reg "${PLANNER}" 'ladder stop rule' "${P_STOP}"
for rung in "${RUNGS[@]}"; do
  reg "${PLANNER}" "ladder rung ${rung}" "    ${rung}"
done
reg "${PLANNER}" 'new-things justification line' "${P_DEPS}"
reg "${PLANNER}" 'none-needs-no-justification sentence' "${P_NONE}"
reg "${PLANNER}" 'ladder does not restate routing' "${P_DOCS}"
reg "${PLANNER}" 'Files to Create entry template' "${P_CREATE}"
reg "${PLANNER}" 'Files to Create empty sentence' "${P_EMPTY}"
reg "${PLANNER}" 'sibling audit line' "${P_AUDIT}"
reg "${PLANNER}" 'sibling audit required sentence' "${P_AUDIT_REQ}"
reg "${PLANNER}" 'sibling audit omit sentence' "${P_AUDIT_OMIT}"
reg "${PLANNER}" 'precedence sentence' "${PREC}"

reg "${REVIEWER}" 'section heading' "${R_HEADING}"
for tag in "${R_TAGS[@]}"; do
  reg "${REVIEWER}" "tag ${tag}" "${tag}"
done
reg "${REVIEWER}" 'location bullet' "${R_B1}"
reg "${REVIEWER}" 'removable-complexity bullet' "${R_B2}"
reg "${REVIEWER}" 'sufficient-replacement bullet' "${R_B3}"
reg "${REVIEWER}" 'failure-or-unmet-requirement bullet' "${R_B4}"
reg "${REVIEWER}" 'field mapping sentence' "${R_MAP}"
reg "${REVIEWER}" 'rubric sentence' "${R_SCORE}"
reg "${REVIEWER}" 'fewer-lines sentence' "${R_FEWER}"
reg "${REVIEWER}" 'no-failure-scenario sentence' "${R_NOFAIL}"
reg "${REVIEWER}" 'approved-plan sentence' "${R_PLAN}"
reg "${REVIEWER}" 'nitpick sentence' "${R_NIT}"
reg "${REVIEWER}" 'LSP sentence' "${R_LSP}"
reg "${REVIEWER}" 'review checklist item' "${R_CHECK}"
reg "${REVIEWER}" 'precedence sentence' "${PREC}"

reg "${ANALYZER}" 'step 4 (before any split or extraction)' "${A_STEP4}"
reg "${ANALYZER}" 'renumbered step 5' "${A_STEP5}"
reg "${ANALYZER}" 'renumbered step 6' "${A_STEP6}"
reg "${ANALYZER}" 'renumbered step 7' "${A_STEP7}"
reg "${ANALYZER}" 'HIGH suggestion line' "${A_SUG_HIGH}"
reg "${ANALYZER}" 'MEDIUM suggestion line' "${A_SUG_MED}"
reg "${ANALYZER}" 'Split into file sub-bullet' "${A_SPLIT_SUB}"
reg "${ANALYZER}" 'cohesive large-file bullet' "${A_COHESIVE}"
reg "${ANALYZER}" 'precedence sentence' "${PREC}"

reg "${CODEX_MD}" 'simplification findings bullet' "${C_LEAD}"
reg "${CODEX_MD}" 'simplification finding shape' "${C_REQ}"
reg "${CODEX_MD}" 'fewer-lines sentence' "${R_FEWER}"
reg "${CODEX_MD}" 'precedence sentence' "${PREC}"

reg "${TOML_PLANNER}" 'ladder' "${TP_LADDER}"
reg "${TOML_PLANNER}" 'ticket-tied justification' "${TP_JUST}"
reg "${TOML_PLANNER}" 'precedence sentence' "${PREC}"
reg "${TOML_REVIEWER}" 'simplification finding shape' "${TC_SHAPE}"
reg "${TOML_REVIEWER}" 'fewer-lines sentence' "${R_FEWER}"
reg "${TOML_REVIEWER}" 'precedence sentence' "${PREC}"
reg "${TOML_ANALYZER}" 'delete/reuse/inline/native before extraction' "${TS_ORDER}"
reg "${TOML_ANALYZER}" 'precedence sentence' "${PREC}"

COVERED_FLOW_FILES=("${PLANNER}" "${REVIEWER}" "${ANALYZER}" "${CODEX_MD}" "${TOML_PLANNER}" "${TOML_REVIEWER}" "${TOML_ANALYZER}")

# --- per-file structural checks ---------------------------------------------------

# check_planner <planner-content>
check_planner() {
  local c="$1" lbl="${PLANNER}"
  local alt rej lad ftm ladder rung prev nums count fc rk sec

  # Ladder sits after the Rejected block, inside Alternatives Considered, before Files to Modify.
  one_line alt "${c}" '    ### Alternatives Considered' "${lbl}"
  one_line rej "${c}" '    #### Rejected: <approach name>' "${lbl}"
  one_line lad "${c}" "${P_LADDER_H}" "${lbl}"
  one_line ftm "${c}" '    ### Files to Modify' "${lbl}"
  if [[ -n "${alt}" && -n "${rej}" && -n "${lad}" && -n "${ftm}" ]]; then
    if ! [[ "${alt}" -lt "${rej}" && "${rej}" -lt "${lad}" && "${lad}" -lt "${ftm}" ]]; then
      fail "${lbl}: the ladder must sit after '#### Rejected' and before '### Files to Modify' inside '### Alternatives Considered' (lines ${alt}/${rej}/${lad}/${ftm})"
    fi
  fi

  ladder="$(indented_block "${c}" "${P_LADDER_H}" '^    ###+ ')"
  if [[ -z "${ladder}" ]]; then
    fail "${lbl}: the '#### Minimum-sufficient ladder' block is missing or not followed by a heading"
  else
    # Each rung exactly once, strictly increasing line numbers.
    prev=0
    for rung in "${RUNGS[@]}"; do
      nums="$(prefix_line_numbers "${ladder}" "    ${rung}")"
      count="$(count_lines "${nums}")"
      if [[ "${count}" -ne 1 ]]; then
        fail "${lbl} ladder: expected exactly 1 line starting with [    ${rung}], found ${count}"
        continue
      fi
      if [[ "${nums}" -le "${prev}" ]]; then
        fail "${lbl} ladder: rung [${rung}] is out of order (line ${nums} is not after line ${prev})"
      fi
      prev="${nums}"
    done
    assert_contains "${ladder}" "${P_STOP}" "${lbl} ladder stop rule"
    assert_contains "${ladder}" "${P_DEPS}" "${lbl} ladder justification line"
    assert_contains "${ladder}" "${P_NONE}" "${lbl} ladder none sentence"
    assert_contains "${ladder}" "${P_DOCS}" "${lbl} ladder routing pointer"
    assert_not_contains "${ladder}" 'owner:' "${lbl} ladder (documentation routing must not be restated)"
    rk="$(line_with_prefix "${ladder}" '    Repo reuse:')"
    assert_contains "${rk}" "${P_UI_POINTER}" "${lbl} ladder Repo reuse rung (points to the UI-reuse paragraph)"
  fi

  # The For UI/frontend work paragraph is unchanged.
  fc="$(printf '%s\n' "${c}" | awk '/^\*\*For UI\/frontend work\*\*/ { f = 1 } f && /^$/ { exit } f')"
  if [[ -z "${fc}" ]]; then
    fail "${lbl}: the 'For UI/frontend work' paragraph is missing"
  else
    [[ "${fc}" == "${UI_PARAGRAPH}" ]] || fail "${lbl}: the 'For UI/frontend work' paragraph changed (it must stay verbatim)"
  fi

  # Files to Create entry template.
  sec="$(indented_block "${c}" '    ### Files to Create' '^    ###+ ')"
  if [[ -z "${sec}" ]]; then
    fail "${lbl}: the '### Files to Create' block is missing or not followed by a heading"
  else
    assert_contains "${sec}" "${P_CREATE}" "${lbl} Files to Create entry"
    assert_contains "${sec}" "${P_EMPTY}" "${lbl} Files to Create empty-when-none sentence"
  fi

  # Sibling/shared-path audit under Risks.
  sec="$(indented_block "${c}" '    ### Risks' '^    ###+ ')"
  if [[ -z "${sec}" ]]; then
    fail "${lbl}: the '### Risks' block is missing or not followed by a heading"
  else
    assert_contains "${sec}" "${P_AUDIT}" "${lbl} Risks sibling audit line"
    assert_contains "${sec}" "${P_AUDIT_REQ}" "${lbl} Risks sibling audit required sentence"
    assert_contains "${sec}" "${P_AUDIT_OMIT}" "${lbl} Risks sibling audit omit sentence"
  fi
  assert_count "${c}" "${P_AUDIT}" 1 "${lbl} sibling audit line"
}

# check_reviewer <code-reviewer-content>
check_reviewer() {
  local c="$1" lbl="${REVIEWER}"
  local tv sf oc sec tag chk

  one_line tv "${c}" '## Test Value' "${lbl}"
  one_line sf "${c}" "${R_HEADING}" "${lbl}"
  one_line oc "${c}" '## Output Constraints' "${lbl}"
  if [[ -n "${tv}" && -n "${sf}" && -n "${oc}" ]]; then
    if ! [[ "${tv}" -lt "${sf}" && "${sf}" -lt "${oc}" ]]; then
      fail "${lbl}: '${R_HEADING}' must sit between '## Test Value' and '## Output Constraints' (lines ${tv}/${sf}/${oc})"
    fi
  fi
  assert_count "${c}" "${R_HEADING}" 1 "${lbl} section heading"

  sec="$(section_of "${c}" "${R_HEADING}")"
  if [[ -z "${sec}" ]]; then
    fail "${lbl}: no '${R_HEADING}' section"
  else
    for tag in "${R_TAGS[@]}"; do
      assert_contains "${sec}" "${tag}" "${lbl} Simplification Findings tag"
    done
    assert_contains "${sec}" "${R_B1}" "${lbl} Simplification Findings location bullet"
    assert_contains "${sec}" "${R_B2}" "${lbl} Simplification Findings removable-complexity bullet"
    assert_contains "${sec}" "${R_B3}" "${lbl} Simplification Findings sufficient-replacement bullet"
    assert_contains "${sec}" "${R_B4}" "${lbl} Simplification Findings failure bullet"
    assert_contains "${sec}" "${R_MAP}" "${lbl} Simplification Findings field mapping"
    assert_contains "${sec}" "${R_SCORE}" "${lbl} Simplification Findings rubric"
    assert_contains "${sec}" "${R_FEWER}" "${lbl} Simplification Findings fewer-lines sentence"
    assert_contains "${sec}" "${R_NOFAIL}" "${lbl} Simplification Findings no-failure sentence"
    assert_contains "${sec}" "${R_PLAN}" "${lbl} Simplification Findings approved-plan sentence"
    assert_contains "${sec}" "${R_NIT}" "${lbl} Simplification Findings nitpick sentence"
    assert_contains "${sec}" "${R_LSP}" "${lbl} Simplification Findings LSP sentence"
    assert_contains "${sec}" "${PREC}" "${lbl} Simplification Findings precedence"
  fi

  chk="$(section_of "${c}" '## Review Checklist')"
  if [[ -z "${chk}" ]]; then
    fail "${lbl}: no '## Review Checklist' section"
  else
    assert_contains "${chk}" "${R_CHECK}" "${lbl} Review Checklist sibling audit item"
  fi
}

# check_analyzer <structure-analyzer-content>
check_analyzer() {
  local c="$1" lbl="${ANALYZER}"
  local ap s4 s5 s6 s7 pl sec line tok pd pr pi pn sugs nsug split_seen

  ap="$(section_of "${c}" '## Analysis Process')"
  if [[ -z "${ap}" ]]; then
    fail "${lbl}: no '## Analysis Process' section"
  else
    one_line s4 "${ap}" "${A_STEP4}" "${lbl} Analysis Process"
    one_line s5 "${ap}" "${A_STEP5}" "${lbl} Analysis Process"
    one_line s6 "${ap}" "${A_STEP6}" "${lbl} Analysis Process"
    one_line s7 "${ap}" "${A_STEP7}" "${lbl} Analysis Process"
    if [[ -n "${s4}" && -n "${s5}" && -n "${s6}" && -n "${s7}" ]]; then
      if ! [[ "${s4}" -lt "${s5}" && "${s5}" -lt "${s6}" && "${s6}" -lt "${s7}" ]]; then
        fail "${lbl} Analysis Process: step 4 must come before the renumbered steps 5-7 (lines ${s4}/${s5}/${s6}/${s7})"
      fi
      # Deletion, then reuse, then inlining, then a native capability.
      pd="$(pos_of "${A_STEP4}" 'deletion')"
      pr="$(pos_of "${A_STEP4}" 'reuse')"
      pi="$(pos_of "${A_STEP4}" 'inlining')"
      pn="$(pos_of "${A_STEP4}" 'native')"
      if ! [[ "${pd}" -ge 0 && "${pd}" -lt "${pr}" && "${pr}" -lt "${pi}" && "${pi}" -lt "${pn}" ]]; then
        fail "${lbl} step 4: deletion, reuse, inlining and native must appear in that order"
      fi
      pl="$(contains_line_numbers "${ap}" "${PREC}")"
      if [[ -z "${pl}" ]] || ! [[ "${pl%%$'\n'*}" -gt "${s7}" ]]; then
        fail "${lbl} Analysis Process: the precedence paragraph must follow the process list (after step 7)"
      fi
    fi
  fi

  # Every Suggestion line in the Output Format accepts the four new values; at least one keeps Split into.
  sec="$(section_of "${c}" '## Output Format')"
  if [[ -z "${sec}" ]]; then
    fail "${lbl}: no '## Output Format' section"
  else
    sugs="$(printf '%s\n' "${sec}" | grep -F -- '**Suggestion**')"
    nsug="$(count_lines "${sugs}")"
    [[ "${nsug}" -ge 2 ]] || fail "${lbl} Output Format: expected at least 2 '**Suggestion**' lines, found ${nsug}"
    split_seen=0
    while IFS= read -r line; do
      [[ -n "${line}" ]] || continue
      for tok in 'Delete' 'Reuse <path>' 'Inline' 'Native <capability>'; do
        assert_contains "${line}" "${tok}" "${lbl} Output Format Suggestion line"
      done
      if [[ "${line}" == *'Split into'* ]]; then split_seen=1; fi
    done <<<"${sugs}"
    [[ "${split_seen}" -eq 1 ]] || fail "${lbl} Output Format: no Suggestion line keeps 'Split into'"
  fi

  sec="$(section_of "${c}" '## What NOT to Flag')"
  if [[ -z "${sec}" ]]; then
    fail "${lbl}: no '## What NOT to Flag' section"
  else
    assert_contains "${sec}" "${A_COHESIVE}" "${lbl} What NOT to Flag cohesive-file bullet"
  fi
}

# check_codex_md <agents-md-codex-content>
check_codex_md() {
  local c="$1" lbl="${CODEX_MD}"
  local block fnd lead leadline tag

  block="$(indented_block "${c}" '**Code-quality review.**' '^[*][*]Silent-failure review[.][*][*]')"
  if [[ -z "${block}" ]]; then
    fail "${lbl}: the '**Code-quality review.**' block is missing or not followed by '**Silent-failure review.**'"
    return
  fi
  fnd="$(prefix_line_numbers "${block}" "${C_FINDINGS_LEAD}")"
  lead="$(prefix_line_numbers "${block}" "${C_LEAD}")"
  if [[ "$(count_lines "${lead}")" -ne 1 ]]; then
    fail "${lbl} code-quality block: expected exactly 1 line starting with [${C_LEAD}]"
    return
  fi
  if [[ "$(count_lines "${fnd}")" -ne 1 ]] || ! [[ "${lead}" -gt "${fnd}" ]]; then
    fail "${lbl} code-quality block: the simplification bullet must follow the single '${C_FINDINGS_LEAD}' bullet"
  fi
  leadline="$(line_with_prefix "${block}" "${C_LEAD}")"
  for tag in "${R_TAGS[@]}"; do
    assert_contains "${leadline}" "${tag}" "${lbl} simplification bullet tag"
  done
  assert_contains "${leadline}" "${C_REQ}" "${lbl} simplification bullet shape"
  assert_contains "${leadline}" "${R_FEWER}" "${lbl} simplification bullet fewer-lines sentence"
  assert_contains "${leadline}" "${PREC}" "${lbl} simplification bullet precedence"
}

# check_role <toml-content> <flow-relative-path> <marker>... -- every marker sits
# inside the single-line developer_instructions string.
check_role() {
  local c="$1" lbl="$2" nums line m
  shift 2
  nums="$(prefix_line_numbers "${c}" 'developer_instructions = "')"
  if [[ "$(count_lines "${nums}")" -ne 1 ]]; then
    fail "${lbl}: expected exactly 1 single-line 'developer_instructions = \"' entry"
    return
  fi
  line="$(line_with_prefix "${c}" 'developer_instructions = "')"
  for m in "$@"; do
    assert_contains "${line}" "${m}" "${lbl} developer_instructions"
  done
}

# --- the whole contract ------------------------------------------------------------

# check_tree <flow-root> <repo-root> -- every assertion; the mutation self-test
# runs it against mutated copies of the covered files.
check_tree() {
  local flow="$1" repo="$2" f i role template installed c
  local -A D=()

  for f in "${COVERED_FLOW_FILES[@]}"; do
    if require_doc c "${flow}/${f}"; then D["${f}"]="${c}"; fi
  done

  # Every positive marker is present in its file.
  for i in "${!MK_FILE[@]}"; do
    f="${MK_FILE[$i]}"
    [[ -n "${D[${f}]+x}" ]] || continue
    assert_contains "${D[${f}]}" "${MK_TEXT[$i]}" "${f}: ${MK_LABEL[$i]}"
  done

  # The precedence sentence appears exactly once per source.
  for f in "${PRECEDENCE_SOURCES[@]}"; do
    [[ -n "${D[${f}]+x}" ]] || continue
    assert_count "${D[${f}]}" "${PREC}" 1 "${f} precedence sentence"
  done

  [[ -z "${D[${PLANNER}]+x}" ]] || check_planner "${D[${PLANNER}]}"
  [[ -z "${D[${REVIEWER}]+x}" ]] || check_reviewer "${D[${REVIEWER}]}"
  [[ -z "${D[${ANALYZER}]+x}" ]] || check_analyzer "${D[${ANALYZER}]}"
  [[ -z "${D[${CODEX_MD}]+x}" ]] || check_codex_md "${D[${CODEX_MD}]}"
  [[ -z "${D[${TOML_PLANNER}]+x}" ]] || check_role "${D[${TOML_PLANNER}]}" "${TOML_PLANNER}" "${TP_LADDER}" "${TP_JUST}" "${PREC}"
  [[ -z "${D[${TOML_REVIEWER}]+x}" ]] || check_role "${D[${TOML_REVIEWER}]}" "${TOML_REVIEWER}" "${TC_SHAPE}" "${R_FEWER}" "${PREC}"
  [[ -z "${D[${TOML_ANALYZER}]+x}" ]] || check_role "${D[${TOML_ANALYZER}]}" "${TOML_ANALYZER}" "${TS_ORDER}" "${PREC}"

  # Installed copies are byte-identical to their templates.
  for role in "${ROLES[@]}"; do
    template="${flow}/${ROLE_DIR}/${role}.toml"
    installed="${repo}/.codex/agents/${role}.toml"
    if [[ ! -f "${template}" ]]; then
      fail "${ROLE_DIR}/${role}.toml: template not found"
    elif [[ ! -f "${installed}" ]]; then
      fail ".codex/agents/${role}.toml: installed copy not found"
    elif ! cmp -s "${template}" "${installed}"; then
      fail ".codex/agents/${role}.toml drifted from ${ROLE_DIR}/${role}.toml"
    fi
  done
}

# --- extractor non-vacuity self-tests ------------------------------------------------
# These run in every state (red or green): they prove the extractors behave on
# synthetic input, independent of what the real prompts currently contain.

selftest_extractors() {
  local out nums
  local fenced_doc plain_doc fenced_end_doc

  # section_of must not end at a heading-shaped line inside a fence.
  fenced_doc=$'## A\nalpha\n```text\n## B\n```\nomega\n## C\ngamma'
  out="$(section_of "${fenced_doc}" '## A')"
  assert_contains "${out}" 'omega' "self-test section_of (content after a fenced heading look-alike)"
  assert_not_contains "${out}" 'gamma' "self-test section_of (stops at the next real heading)"
  out="$(section_of "${fenced_doc}" '## Missing')"
  [[ -z "${out}" ]] || fail "self-test section_of: an absent heading must yield an empty section"

  # indented_block returns the body, nothing when the end never follows, and ignores a fenced end.
  plain_doc=$'    ### S\n    one\n    two\n    ### E\n    three'
  out="$(indented_block "${plain_doc}" '    ### S' '^    ###+ ')"
  [[ "${out}" == $'    one\n    two' ]] || fail "self-test indented_block: expected the two body lines, got [${out}]"
  out="$(indented_block $'    ### S\n    one' '    ### S' '^    ###+ ')"
  [[ -z "${out}" ]] || fail "self-test indented_block: an unterminated block must yield nothing"
  fenced_end_doc=$'    ### S\n```\n    ### E\n```\n    kept\n    ### E2'
  out="$(indented_block "${fenced_end_doc}" '    ### S' '^    ###+ ')"
  assert_contains "${out}" 'kept' "self-test indented_block (a fenced end look-alike must not end the block)"

  # Line-number helpers.
  nums="$(exact_line_numbers $'a\nb\na' 'a')"
  [[ "${nums}" == $'1\n3' ]] || fail "self-test exact_line_numbers: expected 1 and 3, got [${nums}]"
  nums="$(prefix_line_numbers $'  x\nx' 'x')"
  [[ "${nums}" == '2' ]] || fail "self-test prefix_line_numbers: expected only the unindented line, got [${nums}]"
  nums="$(exact_line_numbers 'a' 'zzz')"
  [[ -z "${nums}" ]] || fail "self-test exact_line_numbers: an absent line must yield nothing"
  [[ "$(count_in 'abc' 'zzz')" == '0' ]] || fail "self-test count_in: an absent needle must count 0"
  [[ "$(count_in $'aa aa\naa' 'aa')" == '3' ]] || fail "self-test count_in: occurrences, not lines, must be counted"
  [[ "$(pos_of 'one two three' 'two')" == '4' ]] || fail "self-test pos_of: expected offset 4"
  [[ "$(pos_of 'one two' 'zzz')" == '-1' ]] || fail "self-test pos_of: an absent needle must be -1"
}

# --- mutation self-test ------------------------------------------------------------------

# covered_rel_paths -- repo-relative paths of the ten covered files.
covered_rel_paths() {
  local f role
  for f in "${COVERED_FLOW_FILES[@]}"; do printf 'flow/%s\n' "${f}"; done
  for role in "${ROLES[@]}"; do printf '.codex/agents/%s.toml\n' "${role}"; done
}

# copy_covered <src-repo-root> <dst-root> -- (re)create the ten covered files under <dst-root>.
copy_covered() {
  local src="$1" dst="$2" rel
  while IFS= read -r rel; do
    mkdir -p "${dst}/$(dirname "${rel}")" || return 1
    cp -- "${src}/${rel}" "${dst}/${rel}" || return 1
  done < <(covered_rel_paths)
}

# remove_text <root> <repo-relative-file> <text> -- delete every occurrence of <text>.
remove_text() {
  local file="$1/$2" c
  c="$(cat "${file}")" || return 1
  printf '%s\n' "${c//"$3"/}" > "${file}"
}

# append_line <root> <repo-relative-file> <line>
append_line() {
  printf '%s\n' "$3" >> "$1/$2"
}

# swap_prefixed_lines <root> <repo-relative-file> <prefix-a> <prefix-b> -- swap the first line starting with each.
swap_prefixed_lines() {
  local file="$1/$2" a="$3" b="$4" lines=() i ia=-1 ib=-1 tmp
  mapfile -t lines < "${file}" || return 1
  for i in "${!lines[@]}"; do
    if [[ "${ia}" -lt 0 && "${lines[$i]}" == "${a}"* ]]; then ia="${i}"; fi
    if [[ "${ib}" -lt 0 && "${lines[$i]}" == "${b}"* ]]; then ib="${i}"; fi
  done
  [[ "${ia}" -ge 0 && "${ib}" -ge 0 ]] || return 1
  tmp="${lines[$ia]}"; lines[ia]="${lines[$ib]}"; lines[ib]="${tmp}"
  printf '%s\n' "${lines[@]}" > "${file}"
}

# insert_after_prefix <root> <repo-relative-file> <prefix> <line> -- insert <line> after the first line starting with <prefix>.
insert_after_prefix() {
  local file="$1/$2" prefix="$3" new="$4" lines=() i done_flag=0
  mapfile -t lines < "${file}" || return 1
  : > "${file}"
  for i in "${!lines[@]}"; do
    printf '%s\n' "${lines[$i]}" >> "${file}"
    if [[ "${done_flag}" -eq 0 && "${lines[$i]}" == "${prefix}"* ]]; then
      printf '%s\n' "${new}" >> "${file}"
      done_flag=1
    fi
  done
  [[ "${done_flag}" -eq 1 ]]
}

MUT_PRISTINE=""
MUT_WORK=""
MUT_ROUNDS=0

# mirror_role_template <root> <repo-relative-file> -- when <file> is a role template, copy the
# mutated template over its installed .codex/agents twin so the cmp pair stays byte-identical
# and only the marker/count assertions can catch the mutation. Anything else: a no-op.
mirror_role_template() {
  local root="$1" rel="$2" role
  [[ "${rel}" == "flow/${ROLE_DIR}/"*.toml ]] || return 0
  role="$(basename "${rel}" .toml)"
  cp -- "${root}/${rel}" "${root}/.codex/agents/${role}.toml" || { fail "self-test: could not mirror ${rel} to its installed copy"; return 1; }
}

# BASH_RUNTIME_ERROR matches a bash runtime/syntax failure inside check_tree's stderr, which a
# failure-count delta alone would silently accept as "caught".
BASH_RUNTIME_ERROR=': line [0-9]+:|syntax error|unbound variable|integer expression expected|command not found|bad substitution'

# run_round <label> <repo-relative-mutated-file> [expected-stderr-substring] -- the work tree was
# just mutated; it must differ from the pristine copy (non-vacuity), check_tree must report more
# than 0 failures, its stderr must carry no bash runtime error, and (when given) the expected
# detector's message.
run_round() {
  local label="$1" rel="$2" expected="${3:-}" before delta errfile
  MUT_ROUNDS=$((MUT_ROUNDS+1))
  if cmp -s "${MUT_PRISTINE}/${rel}" "${MUT_WORK}/${rel}"; then
    fail "self-test [${label}]: the mutation did not change ${rel} (vacuous round)"
    return
  fi
  errfile="${MUT_ROOT}/round-stderr.txt"
  before="${failures}"
  check_tree "${MUT_WORK}/flow" "${MUT_WORK}" 2>"${errfile}"
  delta=$((failures - before))
  failures="${before}"
  if grep -Eq -- "${BASH_RUNTIME_ERROR}" "${errfile}"; then
    fail "self-test [${label}]: check_tree hit a bash runtime error instead of a clean assertion failure"
    return
  fi
  [[ "${delta}" -gt 0 ]] || { fail "self-test [${label}]: the mutation was NOT caught (check_tree reported 0 failures)"; return; }
  if [[ -n "${expected}" ]] && ! grep -qF -- "${expected}" "${errfile}"; then
    fail "self-test [${label}]: the mutation was caught, but not by the expected detector [${expected}]"
  fi
}

# fresh_work -- reset the work tree to the pristine copy.
fresh_work() {
  copy_covered "${MUT_PRISTINE}" "${MUT_WORK}" || { fail "self-test: could not reset the work tree"; return 1; }
}

mutation_self_test() {
  local real_failures="$1" base_failures before i rel f role src

  MUT_ROOT="$(mktemp -d)" || { fail "self-test: could not create a mutation directory"; return; }
  [[ -d "${MUT_ROOT}" ]] || { fail "self-test: mktemp returned a non-directory [${MUT_ROOT}]"; return; }
  TMP_DIRS+=("${MUT_ROOT}")
  MUT_PRISTINE="${MUT_ROOT}/pristine"
  MUT_WORK="${MUT_ROOT}/work"
  mkdir -p "${MUT_PRISTINE}" "${MUT_WORK}" || { fail "self-test: could not create the copy directories"; return; }

  if ! copy_covered "${REPO_DIR}" "${MUT_PRISTINE}"; then
    fail "self-test: could not copy the covered files (a covered file is missing)"
    return
  fi

  # The unmodified copy must report exactly what the real tree reports.
  before="${failures}"
  check_tree "${MUT_PRISTINE}/flow" "${MUT_PRISTINE}" 2>/dev/null
  base_failures=$((failures - before))
  failures="${before}"
  if [[ "${base_failures}" -ne "${real_failures}" ]]; then
    fail "self-test: the unmodified copy reports ${base_failures} failure(s) but the real tree reports ${real_failures} (the copy is not faithful)"
    return
  fi
  if [[ "${base_failures}" -gt 0 ]]; then
    echo "SKIP: mutation self-test not applicable -- the unmodified copy already reports ${base_failures} failure(s), so a mutation cannot be told apart from the existing failures. The rounds run once the prompts carry the rules (0 failures on the base tree)."
    return
  fi

  # One round per positive marker removed.
  for i in "${!MK_FILE[@]}"; do
    rel="flow/${MK_FILE[$i]}"
    fresh_work || return
    if ! remove_text "${MUT_WORK}" "${rel}" "${MK_TEXT[$i]}"; then
      fail "self-test: could not remove [${MK_LABEL[$i]}] from ${rel}"
      continue
    fi
    mirror_role_template "${MUT_WORK}" "${rel}" || continue
    run_round "remove ${MK_FILE[$i]}: ${MK_LABEL[$i]}" "${rel}"
  done

  # A stray second precedence sentence in each of the 7 sources.
  for f in "${PRECEDENCE_SOURCES[@]}"; do
    rel="flow/${f}"
    fresh_work || return
    if [[ "${f}" == *.toml ]]; then
      append_line "${MUT_WORK}" "${rel}" "# ${PREC}"
    else
      append_line "${MUT_WORK}" "${rel}" "${PREC}"
    fi
    mirror_role_template "${MUT_WORK}" "${rel}" || continue
    run_round "duplicate precedence in ${f}" "${rel}"
  done

  # Rung swap: Necessity and Repo reuse trade places.
  rel="flow/${PLANNER}"
  fresh_work || return
  if swap_prefixed_lines "${MUT_WORK}" "${rel}" '    Necessity:' '    Repo reuse:'; then
    run_round 'swap the Necessity and Repo reuse rungs' "${rel}" 'is out of order'
  else
    fail "self-test: could not swap the ladder rungs"
  fi

  # owner: injection into the ladder block.
  fresh_work || return
  if insert_after_prefix "${MUT_WORK}" "${rel}" '    Necessity:' '    owner: docs/example.md'; then
    run_round 'inject owner: into the ladder block' "${rel}" 'forbidden text present: [owner:]'
  else
    fail "self-test: could not inject owner: after the Necessity rung"
  fi

  # Drift in each installed TOML copy.
  for role in "${ROLES[@]}"; do
    rel=".codex/agents/${role}.toml"
    fresh_work || return
    append_line "${MUT_WORK}" "${rel}" '# drift'
    run_round "drift in installed ${role}.toml" "${rel}"
  done

  # A drifted template (the pair differs from the other side).
  for role in "${ROLES[@]}"; do
    src="flow/${ROLE_DIR}/${role}.toml"
    fresh_work || return
    append_line "${MUT_WORK}" "${src}" '# drift'
    run_round "drift in template ${role}.toml" "${src}"
  done

  echo "mutation self-test: ${MUT_ROUNDS} round(s) run"
}

# --- run ------------------------------------------------------------------------------------

selftest_extractors

before_real="${failures}"
check_tree "${FLOW_DIR}" "${REPO_DIR}"
REAL_TREE_FAILURES=$((failures - before_real))

mutation_self_test "${REAL_TREE_FAILURES}"

echo "minimum-sufficient-solution-contract.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
