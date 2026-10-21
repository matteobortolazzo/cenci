#!/usr/bin/env bash
# Contract test for ticket #1341 (parent #692) -- flow/skills/configure/SKILL.md
# becomes a router: the detection, delta-resolution and questions 1-9c prose
# moves verbatim into four phases/*.md files, the legacy-migration prose moves
# into the conditionally read references/migrations.md, and the skill must
# behave exactly as before.
#
# Each case is named after the acceptance criterion or plan decision it proves.
# What this suite pins (the load-bearing wording of each moved block is pinned
# by its own retargeted suite -- configure-delta-rerun.test.sh for Delta
# Resolution, skill-convention-contract.test.sh for the MCP catalog,
# configure-fragment-markers.test.sh for Q9b, allowed-tools-sweep.test.sh for
# the retired patterns and the grant set):
#
#   * case_step_map              SKILL.md `### Step Map`: ordered table, every
#                                 phase file read on every run, the interaction
#                                 rule extended to phases/ and references/
#   * case_P17_fallbacks_colocated   manual fallbacks live in the same phase
#                                 file as the detection that consumes them
#   * case_P19_migrations_conditional  migration prose only in
#                                 references/migrations.md, read only when
#                                 `.claude/config.json` exists
#   * case_single_keep_or_change_rule  one home for the Keep/Change gate:
#                                 SKILL.md `### Question Defaults`
#   * case_moved_headings_unique     each moved heading exists exactly once, in
#                                 its new file
#   * case_cross_file_pointers       positional words that now cross files name
#                                 the file
#   * case_allowed_tools_only_in_skill_md  P16 companion: no allowed-tools line
#                                 outside SKILL.md
#   * case_readme_workflow_deps_row  README workflow-deps row lists the files
#
# Pinned constants, never derived from the doc under test. Every absence check
# is paired with an existence marker (flow/docs/shell-scripting-gotchas.md) so
# it cannot pass vacuously; extractors are pure (no fail() inside $(...)).
#
# Covered files:
#   - flow/skills/configure/SKILL.md, phases/*.md, references/migrations.md
#   - flow/README.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-router-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-router-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
CONFIGURE_DIR="${FLOW_DIR}/skills/configure"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# --- helpers (read/extract helpers are pure: safe inside $(...)) -------------

read_doc_raw() { cat "${FLOW_DIR}/$1" 2>/dev/null; }

# require_doc <result-var> <flow-relative-path> -- nameref wrapper; must NOT be
# invoked via $(...). A missing file fails loudly and assigns "".
require_doc() {
  local -n _result="$1"
  local _relpath="$2" _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

normalize_ws() {
  local content="$1"
  content="${content//$'\n'/ }"
  printf '%s' "${content}" | tr -s ' \t'
}

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

assert_contains_ws() {
  local content="$1" pattern="$2" label="$3" norm
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden> <label>
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4" norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${forbidden}]"
}

# count_occurrences <content> <needle> -- occurrences (not lines).
count_occurrences() {
  local content="$1" needle="$2"
  grep -oF -- "${needle}" <<<"${content}" | wc -l | tr -d '[:space:]'
}

# count_exact_lines <content> <line> -- lines equal to <line>.
count_exact_lines() {
  local content="$1" line="$2"
  grep -cxF -- "${line}" <<<"${content}" | tr -d '[:space:]'
}

# offset_of <content> <needle> -- byte offset of the first occurrence, or -1.
offset_of() {
  local content="$1" needle="$2" prefix
  if [[ "${content}" != *"${needle}"* ]]; then
    printf '%s' "-1"
    return
  fi
  prefix="${content%%"${needle}"*}"
  printf '%s' "${#prefix}"
}

# extract_section_body_raw <content> <heading-line> -- lines strictly between
# the heading and the next `### ` heading (or the end of the content).
extract_section_body_raw() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    index($0, heading) == 1 && length($0) == length(heading) { found=1; next }
    found && index($0, "### ") == 1 { exit }
    found { print }
  ' <<<"${content}"
}

# extract_between_raw <content> <begin-substring> <end-substring> -- lines from
# the first line containing <begin> (inclusive) up to, not including, the next
# line containing <end>.
extract_between_raw() {
  local content="$1" begin="$2" end="$3"
  awk -v b="${begin}" -v e="${end}" '
    on && index($0, e) > 0 { exit }
    index($0, b) > 0 { on=1 }
    on { print }
  ' <<<"${content}"
}

# row_for <content> <file> -- the Step Map table row naming <file> in backticks
# as its first cell.
row_for() {
  local content="$1" file="$2"
  grep -F -- "| \`${file}\` |" <<<"${content}" | head -n 1
}

# --- Pinned constants --------------------------------------------------------

DETECT_FILE='phases/detection-and-delta.md'
PROJECT_FILE='phases/project-questions.md'
MCP_FILE='phases/mcp-lsp-ui-questions.md'
RUNTIME_FILE='phases/runtime-ci-sandbox-questions.md'
MIGRATIONS_FILE='references/migrations.md'

STEP_MAP_HEADING='### Step Map'
QUESTION_DEFAULTS_HEADING='### Question Defaults'

INTERACTION_RULE_EXTENSION='This also governs the `phases/*.md` and `references/*.md` files this skill reads.'
EVERY_RUN='Every run'
MIGRATIONS_NEVER_READ='A `.cenci/config.json`-only run never reads it'
LEGACY_CONDITION='`.claude/config.json` exists'

MANUAL_FALLBACK='**Manual fallback** (only if Scripted Detection failed)'
SCRIPTED_FALLBACK_SENTENCE='the **manual fallback** procedure kept in each consuming section'
PLATFORM_ANCHOR='git remote get-url origin'
CONTAINER_ANCHOR='`test "${CENCI_SANDBOX:-}" = "1"`'
DEPENDENCY_ANCHOR='Testcontainers/Docker-SDK detection'

# Migration prose that moved out of SKILL.md (old lines 35-45).
MIGRATION_PRESERVE='On migration, preserve every unknown key from `legacyConfig`; new writes go only to `.cenci/config.json`.'
MIGRATION_MERGE='recursively merge them into one `migrationBase` with canonical values winning'
MIGRATION_FENCE_LINE='bash "${CLAUDE_PLUGIN_ROOT}/scripts/migrate-project-core.sh" <root> [--apply]'
MIGRATION_NEVER_REWRITE='Never rewrite or delete `.claude/config.json`; it is a read-only compatibility artifact.'

KEEP_OR_CHANGE_OPTION='"No — let me change them"'
KEEP_FIRST_RULE='lists its Keep option first'
KEEP_RECOMMENDED='Keep current (Recommended)'
ID_COLUMN_SENTENCE='The `ID` column is the question'"'"'s identifier in `question-keys.json`'
DEFAULTS_TABLE_HEADER='| Question | ID | `existingConfig` field | Default when field exists |'
ASK_ONE_AT_A_TIME='Ask these questions one at a time using the `AskUserQuestion` tool'

# Moved headings: "<file>|<exact heading line>". Each must occur exactly once
# across SKILL.md, phases/*.md and references/*.md, in the named file.
MOVED_HEADINGS=(
  "${DETECT_FILE}|### Scripted Detection"
  "${DETECT_FILE}|### Delta Resolution"
  "${DETECT_FILE}|### Platform Detection"
  "${DETECT_FILE}|### Container Detection"
  "${PROJECT_FILE}|#### Step 2a — Auto-detect projects"
  "${PROJECT_FILE}|#### Step 2b — Per-project details"
  "${MCP_FILE}|### Dependency Detection"
  "${MCP_FILE}|### MCP Server Catalog"
  "${MCP_FILE}|### LSP Server Catalog"
  "${MCP_FILE}|### UI Conventions"
  "${MCP_FILE}|### Playwright CLI Setup"
  "${MCP_FILE}|### LSP Detection"
  "${MCP_FILE}|#### Binary Verification"
)

# Headings that stay inline in SKILL.md (Question 10 onward and step 5e).
INLINE_MARKERS=(
  '### Board Config (lazyboards)'
  '### Fleet Dispatch Enrollment'
  '### Autonomy Settings'
  '5e. **Generate `.cenci/Dockerfile`**'
)

README_CONFIGURE_ROW_PREFIX='| `configure` | codex.md'

# --- Load the files under test ------------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
require_doc detect_phase "skills/configure/${DETECT_FILE}" || true
require_doc project_phase "skills/configure/${PROJECT_FILE}" || true
require_doc mcp_phase "skills/configure/${MCP_FILE}" || true
require_doc runtime_phase "skills/configure/${RUNTIME_FILE}" || true
require_doc migrations "skills/configure/${MIGRATIONS_FILE}" || true
require_doc readme "README.md" || true

# every_configure_md -- SKILL.md plus every phases/*.md and references/*.md on
# disk (not just the five named above), so a stray sixth file is still counted.
configure_all="${skill}"
shopt -s nullglob
configure_disk_files=("${CONFIGURE_DIR}"/phases/*.md "${CONFIGURE_DIR}"/references/*.md)
shopt -u nullglob
for _part in "${configure_disk_files[@]}"; do
  configure_all+=$'\n'"$(cat "${_part}")"
done

# file_content <relative-file> -- the content loaded above for a named file.
file_content() {
  case "$1" in
    "${DETECT_FILE}") printf '%s' "${detect_phase}" ;;
    "${PROJECT_FILE}") printf '%s' "${project_phase}" ;;
    "${MCP_FILE}") printf '%s' "${mcp_phase}" ;;
    "${RUNTIME_FILE}") printf '%s' "${runtime_phase}" ;;
    "${MIGRATIONS_FILE}") printf '%s' "${migrations}" ;;
    SKILL.md) printf '%s' "${skill}" ;;
  esac
}

step_map_body=""
question_defaults_body=""
if [[ -n "${skill}" ]]; then
  step_map_body="$(extract_section_body_raw "${skill}" "${STEP_MAP_HEADING}")"
  question_defaults_body="$(extract_section_body_raw "${skill}" "${QUESTION_DEFAULTS_HEADING}")"
fi

# ============================================================================
# Cases
# ============================================================================

case_step_map() {
  [[ -n "${skill}" ]] || { fail "step map: SKILL.md missing"; return; }
  if [[ -z "${step_map_body}" ]]; then
    fail "SKILL.md: ${STEP_MAP_HEADING} section missing or empty"
    return
  fi

  # Ordered: migrations (conditional) first, then the four phase files in the
  # original reading order.
  local prev=-1 file at
  for file in "${MIGRATIONS_FILE}" "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}"; do
    at="$(offset_of "${step_map_body}" "| \`${file}\` |")"
    if [[ "${at}" -lt 0 ]]; then
      fail "${STEP_MAP_HEADING}: no table row for \`${file}\`"
    elif [[ "${at}" -le "${prev}" ]]; then
      fail "${STEP_MAP_HEADING}: row for \`${file}\` is out of reading order"
    fi
    prev="${at}"
  done

  # Every phase file is read on every run; each row carries file, condition, why.
  local row
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}"; do
    row="$(row_for "${step_map_body}" "${file}")"
    assert_contains "${row}" "${EVERY_RUN}" "${STEP_MAP_HEADING}: \`${file}\` row states it is read on every run"
  done
  assert_contains_ws "${step_map_body}" '| File | Condition | Why |' "${STEP_MAP_HEADING}: table header names file, condition and why"

  # The interaction rule covers the phase and reference files.
  local rule_line
  rule_line="$(grep -F '> **Interaction rule**' <<<"${skill}" | head -n 1)"
  assert_contains_ws "${rule_line}" "${INTERACTION_RULE_EXTENSION}" "SKILL.md Interaction rule extends to phases/*.md and references/*.md"

  # Every phase/reference file on disk is named from SKILL.md (check.sh
  # orphan-files enforces the same; this keeps the failure local and early).
  local disk rel
  for disk in "${configure_disk_files[@]}"; do
    rel="${disk#"${CONFIGURE_DIR}/"}"
    assert_contains "${step_map_body}" "\`${rel}\`" "${STEP_MAP_HEADING} names ${rel}"
  done
  [[ "${#configure_disk_files[@]}" -ge 5 ]] || fail "step map: expected at least 5 phases/references files on disk, found ${#configure_disk_files[@]}"
}

case_P17_fallbacks_colocated() {
  local scripted platform container dependency lsp_detection
  scripted="$(extract_section_body_raw "${detect_phase}" '### Scripted Detection')"
  platform="$(extract_section_body_raw "${detect_phase}" '### Platform Detection')"
  container="$(extract_section_body_raw "${detect_phase}" '### Container Detection')"
  dependency="$(extract_section_body_raw "${mcp_phase}" '### Dependency Detection')"
  lsp_detection="$(extract_section_body_raw "${mcp_phase}" '### LSP Detection')"

  assert_contains_ws "${scripted}" "${SCRIPTED_FALLBACK_SENTENCE}" "Scripted Detection: the manual fallback is kept in each consuming section"

  assert_contains_ws "${platform}" "${MANUAL_FALLBACK}" "${DETECT_FILE} Platform Detection carries its manual fallback"
  assert_contains_ws "${platform}" "${PLATFORM_ANCHOR}" "${DETECT_FILE} Platform Detection fallback runs git remote get-url origin"
  assert_contains_ws "${container}" "${MANUAL_FALLBACK}" "${DETECT_FILE} Container Detection carries its manual fallback"
  assert_contains_ws "${container}" "${CONTAINER_ANCHOR}" "${DETECT_FILE} Container Detection fallback tests CENCI_SANDBOX"
  assert_contains_ws "${dependency}" "${MANUAL_FALLBACK}" "${MCP_FILE} Dependency Detection carries its manual fallback"
  assert_contains_ws "${dependency}" "${DEPENDENCY_ANCHOR}" "${MCP_FILE} Dependency Detection fallback scans for Testcontainers/Docker-SDK"
  assert_contains_ws "${lsp_detection}" "${MANUAL_FALLBACK}" "${MCP_FILE} LSP Detection carries its manual fallback"

  # The fallbacks are not duplicated into the router or a later phase file.
  assert_absent_paired_ws "${skill}" "${STEP_MAP_HEADING}" "${MANUAL_FALLBACK}" "SKILL.md router carries no detection fallback (it lives with its detection)"
  assert_absent_paired_ws "${project_phase}" "Auto-detect projects" "${MANUAL_FALLBACK}" "${PROJECT_FILE} carries no detection fallback"

  # The always-read rows for both detection-bearing files.
  assert_contains "$(row_for "${step_map_body}" "${DETECT_FILE}")" "${EVERY_RUN}" "step map: ${DETECT_FILE} is read on every run, so its fallbacks stay reachable"
  assert_contains "$(row_for "${step_map_body}" "${MCP_FILE}")" "${EVERY_RUN}" "step map: ${MCP_FILE} is read on every run, so its fallbacks stay reachable"
}

case_P19_migrations_conditional() {
  [[ -n "${migrations}" ]] || { fail "P19: ${MIGRATIONS_FILE} missing or empty"; return; }
  assert_contains_ws "${migrations}" "${MIGRATION_PRESERVE}" "${MIGRATIONS_FILE}: unknown-key preservation"
  assert_contains_ws "${migrations}" "${MIGRATION_MERGE}" "${MIGRATIONS_FILE}: the migrationBase merge"
  assert_contains_ws "${migrations}" "${MIGRATION_FENCE_LINE}" "${MIGRATIONS_FILE}: the migrate-project-core.sh preview/--apply fence"
  assert_contains_ws "${migrations}" "${MIGRATION_NEVER_REWRITE}" "${MIGRATIONS_FILE}: never rewrite .claude/config.json"

  # The migration prose is gone from the router (paired with the step-map row
  # that now points at it, so the absence cannot pass vacuously).
  local migrations_row
  migrations_row="$(row_for "${step_map_body}" "${MIGRATIONS_FILE}")"
  assert_contains "${migrations_row}" "${LEGACY_CONDITION}" "step map: the migrations row is conditioned on .claude/config.json existing"
  assert_contains_ws "${migrations_row}" "${MIGRATIONS_NEVER_READ}" "step map: the migrations row states a .cenci/config.json-only run never reads it"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_NEVER_READ}" "${MIGRATION_PRESERVE}" "SKILL.md no longer carries the unknown-key preservation prose"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_NEVER_READ}" "${MIGRATION_MERGE}" "SKILL.md no longer carries the migrationBase merge prose"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_NEVER_READ}" "${MIGRATION_FENCE_LINE}" "SKILL.md no longer carries the migrate-project-core.sh fence"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_NEVER_READ}" "${MIGRATION_NEVER_REWRITE}" "SKILL.md no longer carries the never-rewrite rule"

  # No phase file restates migration prose either.
  local file
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}"; do
    assert_absent_paired_ws "$(file_content "${file}")" '### ' "${MIGRATION_FENCE_LINE}" "${file} carries no migrate-project-core.sh fence"
  done

  # Every SKILL.md line that mentions the reference file also names the
  # condition that gates it, and there is at least one such line (the row).
  local mention_lines bad
  mention_lines="$(grep -F -- "${MIGRATIONS_FILE}" <<<"${skill}")"
  [[ -n "${mention_lines}" ]] || fail "SKILL.md never names ${MIGRATIONS_FILE}; the router would not read it"
  bad="$(grep -vF -- '.claude/config.json' <<<"${mention_lines}")"
  [[ -z "${bad}" ]] || fail "SKILL.md names ${MIGRATIONS_FILE} outside a .claude/config.json-conditioned line: ${bad}"
}

case_single_keep_or_change_rule() {
  [[ -n "${question_defaults_body}" ]] || { fail "SKILL.md: ${QUESTION_DEFAULTS_HEADING} section missing or empty"; return; }
  local total
  total="$(count_occurrences "$(normalize_ws "${configure_all}")" "${KEEP_OR_CHANGE_OPTION}")"
  [[ "${total}" -eq 1 ]] || fail "the options string ${KEEP_OR_CHANGE_OPTION} must occur exactly once across SKILL.md, phases/*.md and references/*.md, found ${total}"
  assert_contains_ws "${question_defaults_body}" "${KEEP_OR_CHANGE_OPTION}" "${QUESTION_DEFAULTS_HEADING} holds the multi-select Keep/Change gate options"
  assert_contains_ws "${question_defaults_body}" '**Keep-or-change gate**' "${QUESTION_DEFAULTS_HEADING} names the gate"
  assert_contains_ws "${question_defaults_body}" "${KEEP_FIRST_RULE}" "${QUESTION_DEFAULTS_HEADING} states the --all Keep-first rule"
  assert_contains_ws "${question_defaults_body}" "${KEEP_RECOMMENDED}" "${QUESTION_DEFAULTS_HEADING} names the \"Keep current (Recommended)\" option"
  assert_contains_ws "${question_defaults_body}" "${ID_COLUMN_SENTENCE}" "${QUESTION_DEFAULTS_HEADING} keeps the ID-column contract sentence"
  assert_contains_ws "${question_defaults_body}" "${DEFAULTS_TABLE_HEADER}" "${QUESTION_DEFAULTS_HEADING} keeps the ID table"
  assert_contains_ws "${question_defaults_body}" "${ASK_ONE_AT_A_TIME}" "${QUESTION_DEFAULTS_HEADING} keeps the ask-one-at-a-time line"

  # Q5 and Q6 point at the single rule instead of restating it.
  local q5 q6
  q5="$(extract_between_raw "${mcp_phase}" '5. **MCP Servers**' '### UI Conventions')"
  q6="$(extract_between_raw "${mcp_phase}" '6. **LSP Servers**' '#### Binary Verification')"
  [[ -n "${q5}" ]] || fail "${MCP_FILE}: question 5 block not found"
  [[ -n "${q6}" ]] || fail "${MCP_FILE}: question 6 block not found"
  assert_contains_ws "${q5}" "${QUESTION_DEFAULTS_HEADING}" "question 5 points at ${QUESTION_DEFAULTS_HEADING}"
  assert_contains_ws "${q6}" "${QUESTION_DEFAULTS_HEADING}" "question 6 points at ${QUESTION_DEFAULTS_HEADING}"
  assert_absent_paired_ws "${q5}" "${QUESTION_DEFAULTS_HEADING}" "${KEEP_OR_CHANGE_OPTION}" "question 5 does not restate the gate options"
  assert_absent_paired_ws "${q6}" "${QUESTION_DEFAULTS_HEADING}" "${KEEP_OR_CHANGE_OPTION}" "question 6 does not restate the gate options"
}

case_moved_headings_unique() {
  local entry file heading n in_file in_skill
  for entry in "${MOVED_HEADINGS[@]}"; do
    file="${entry%%|*}"
    heading="${entry#*|}"
    n="$(count_exact_lines "${configure_all}" "${heading}")"
    [[ "${n}" -eq 1 ]] || fail "heading [${heading}] must occur exactly once across SKILL.md, phases/*.md and references/*.md, found ${n}"
    in_file="$(count_exact_lines "$(file_content "${file}")" "${heading}")"
    [[ "${in_file}" -eq 1 ]] || fail "heading [${heading}] must be in ${file}, found ${in_file} there"
    in_skill="$(count_exact_lines "${skill}" "${heading}")"
    [[ "${in_skill}" -eq 0 ]] || fail "heading [${heading}] must no longer be in SKILL.md, found ${in_skill}"
  done

  # Paired existence markers: what stays inline really is still inline, so the
  # zero counts above cannot be the result of an emptied SKILL.md.
  local marker
  for marker in "${INLINE_MARKERS[@]}"; do
    assert_contains "${skill}" "${marker}" "SKILL.md keeps ${marker} inline"
  done
}

case_cross_file_pointers() {
  # Delta Resolution's pointer to the defaults table now names the router.
  assert_contains_ws "${detect_phase}" 'the `ID` column of the defaults table in `SKILL.md`'"'"'s `### Question Defaults`' "${DETECT_FILE}: the defaults-table pointer names SKILL.md ${QUESTION_DEFAULTS_HEADING}"
  assert_absent_paired_ws "${detect_phase}" '### Delta Resolution' 'defaults table under Container Detection' "${DETECT_FILE}: retired positional pointer to the defaults table"

  # Step 2a's Lint pointer names the file that holds the Stack-to-CI table.
  assert_contains_ws "${project_phase}" 'see the table under question 8 in `phases/runtime-ci-sandbox-questions.md`' "${PROJECT_FILE}: the Lint pointer names ${RUNTIME_FILE}"
  assert_absent_paired_ws "${project_phase}" '#### Step 2a' 'under question 8 below' "${PROJECT_FILE}: retired positional Lint pointer"

  # The dind detection fallback and its detection pointer name Q9b's file.
  assert_contains_ws "${mcp_phase}" 'question 9b in `phases/runtime-ci-sandbox-questions.md`' "${MCP_FILE}: the dind pointers name ${RUNTIME_FILE}"

  # Inline steps that consume moved tables name the file holding them.
  assert_contains_ws "${skill}" 'LSP Server Catalog` in `phases/mcp-lsp-ui-questions.md`' "SKILL.md step 1b names the file holding the LSP Server Catalog"
  assert_contains_ws "${skill}" '**Monorepo**: fragments are' "SKILL.md step 5e Monorepo sentence keeps its anchor prefix"
  local monorepo
  monorepo="$(grep -F '**Monorepo**: fragments are' <<<"${skill}" | head -n 1)"
  assert_contains_ws "${monorepo}" '`phases/runtime-ci-sandbox-questions.md`' "SKILL.md step 5e Monorepo sentence names the file holding the Stack-to-fragment mapping table"
  assert_absent_paired_ws "${skill}" '**Monorepo**: fragments are' 'mapping table above' "SKILL.md step 5e retired positional mapping-table pointer"
  assert_absent_paired_ws "${skill}" '**Monorepo**: fragments are' 'mapping table under question 9,' "SKILL.md step 5e retired positional question 9 pointer"

  # The router's early prose points at the Delta Resolution file, not "below".
  assert_contains_ws "${skill}" '`### Delta Resolution` in `phases/detection-and-delta.md`' "SKILL.md names the file holding Delta Resolution"
  assert_absent_paired_ws "${skill}" '`### Delta Resolution` in `phases/detection-and-delta.md`' 'Delta Resolution subsection below' "SKILL.md retired positional Delta Resolution pointer"
}

case_allowed_tools_only_in_skill_md() {
  local line_count disk
  line_count="$(grep -c '^allowed-tools:' <<<"${skill}" | tr -d '[:space:]')"
  [[ "${line_count}" -eq 1 ]] || fail "SKILL.md must carry exactly one allowed-tools: frontmatter line, found ${line_count}"
  [[ "${#configure_disk_files[@]}" -ge 5 ]] || fail "allowed-tools guard is vacuous: found ${#configure_disk_files[@]} phases/references files (expected at least 5)"
  for disk in "${configure_disk_files[@]}"; do
    if grep -q '^allowed-tools:' "${disk}"; then
      fail "${disk#"${FLOW_DIR}/"}: allowed-tools: belongs only in SKILL.md frontmatter"
    fi
    if [[ "$(head -n 1 "${disk}")" == "---" ]]; then
      fail "${disk#"${FLOW_DIR}/"}: phase/reference files carry no frontmatter"
    fi
  done
}

case_readme_workflow_deps_row() {
  local row file
  row="$(grep -F -- "${README_CONFIGURE_ROW_PREFIX}" <<<"${readme}" | head -n 1)"
  [[ -n "${row}" ]] || { fail "README.md: workflow-deps row for configure not found"; return; }
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}" "${MIGRATIONS_FILE}"; do
    assert_contains "${row}" "${file}" "README.md workflow-deps row for configure lists ${file}"
  done
}

# --- Run every case ----------------------------------------------------------

case_step_map
case_P17_fallbacks_colocated
case_P19_migrations_conditional
case_single_keep_or_change_rule
case_moved_headings_unique
case_cross_file_pointers
case_allowed_tools_only_in_skill_md
case_readme_workflow_deps_row

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-router-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-router-contract.test.sh: all checks passed."
