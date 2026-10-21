#!/usr/bin/env bash
# Contract test for tickets #1341 and #1342 (parent #692) --
# flow/skills/configure/SKILL.md becomes a router: the detection,
# delta-resolution and questions 1-9c prose moves verbatim into four
# phases/*.md files, the legacy-migration prose moves into the conditionally
# read references/migrations.md (#1341), and the lazyboards (question 10 and
# step 5f), fleet enrollment (questions 11, 12, 18-20) and autonomy
# (questions 13-17) prose moves into three more conditionally read phase files
# plus references/lazyboards-example.md (#1342). The skill must behave exactly
# as before.
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
#                                 SKILL.md `### Question Defaults`; the
#                                 autonomy sites (P10) point at it and keep
#                                 their option labels
#   * case_phase_read_conditions  the new phase files open with a read-condition
#                                 line and drop "runs on every invocation"
#   * case_P21_lazyboards_condition  phases/lazyboards.md read only when
#                                 question 10 is asked or .lazyboards.yml exists
#   * case_P22_fleet_guard       the fleet container guard and failed-probe rule
#                                 moved verbatim and stay reachable on host runs
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
#   - flow/skills/configure/SKILL.md, phases/*.md, references/migrations.md,
#     references/lazyboards-example.md
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
LAZY_FILE='phases/lazyboards.md'
LAZY_EXAMPLE_FILE='references/lazyboards-example.md'
FLEET_FILE='phases/fleet-enrollment.md'
AUTONOMY_FILE='phases/autonomy-settings.md'
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
# P10 (#1342): the autonomy sites point at the single rule instead of restating
# it. The three per-question "Under `--all`, question N is re-offered ..."
# sentences collapse into one, and each of the Q13/Q14/Q15 `--all` bullets
# carries the same pointer while keeping its exact option labels.
KEEP_RULE_POINTER='per the keep-or-change rule in `SKILL.md`'"'"'s `### Question Defaults`'
ALL_REOFFER_COLLAPSED='Under `--all`, questions 13, 14 and 15 are each re-offered even when `planning`, `automerge` or `refinement` is present, with the Keep option listed first and pre-selected '"${KEEP_RULE_POINTER}"'.'
OLD_Q13_ALL_REOFFER='Under `--all`, question 13 is re-offered even when `planning` is present, with the committed value pre-selected.'
OLD_Q14_ALL_REOFFER='Under `--all`, question 14 is re-offered even when `automerge` is present, with "Keep current block(s) (Recommended)" pre-selected.'
OLD_Q15_ALL_REOFFER='Under `--all`, question 15 is re-offered even when `refinement` is present, with "Keep current (Recommended)" pre-selected.'
Q13_MARKER='13. **Plan approval autonomy**'
Q14_MARKER='14. **Automerge policy block**'
Q15_MARKER='15. **Refinement autonomy**'
Q16_MARKER='16. **Refinement confirmation**'
Q13_LABELS=('"Keep current (Recommended)"' '"Switch to `interactive`"' '"Switch to `lean`"')
Q14_LABELS=('"Keep current block(s) (Recommended)"' '"Remove the block(s)"')
Q15_LABELS=('"Keep current (Recommended)"' '"Switch to `interactive`"' '"Switch to `lean`"')

# Cross-file pointers (#1342): a positional "above"/"below" that now crosses
# files names the file. The phrase form is "`### <heading>` in `<file>`".
AUTONOMY_POINTER='`### Autonomy Settings` in `phases/autonomy-settings.md`'
LAZY_POINTER='`### Board Config (lazyboards)` in `phases/lazyboards.md`'
FLEET_POINTER='`### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`'
FLEET_AUTONOMY_POINTER='`### Fleet Autonomy Controls` in `phases/fleet-enrollment.md`'
OLD_SKILL_AUTONOMY_POINTERS=(
  '(see `### Autonomy Settings`)'
  '`cenci:configure-q14-remove` program (`### Autonomy Settings`)'
  'question 13 (`### Autonomy Settings`) writes'
  'question 15 (`### Autonomy Settings`) writes'
  '(`### Autonomy Settings`) and that block was actually written'
  'If question 14 (`### Autonomy Settings`) scaffolded'
  '`### Autonomy Settings`'"'"'s own "Next step" block'
  '`### Autonomy Settings` question 13 above'
  '`### Autonomy Settings` question 14 above'
  '`### Autonomy Settings` question 15 above'
  'question 13 above was asked'
  'question 14 above was asked'
  'question 15 above was asked'
  'see *Board Config*'
  'see *Fleet Dispatch Enrollment*'
  'see *Autonomy Settings*'
  'derivation tables above'
)
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
  "${LAZY_FILE}|### Board Config (lazyboards)"
  "${LAZY_FILE}|### Step 5f — Generate .lazyboards.yml"
  "${FLEET_FILE}|### Fleet Dispatch Enrollment"
  "${FLEET_FILE}|### Fleet Autonomy Controls"
  "${AUTONOMY_FILE}|### Autonomy Settings"
)

# Markers that stay inline in SKILL.md: Auth Verification (always run), step 5e,
# and the step 5f stub that defers to phases/lazyboards.md.
STUB_5F='run step 5f in `phases/lazyboards.md` when that file was read this run'
INLINE_MARKERS=(
  '### Auth Verification'
  '5e. **Generate `.cenci/Dockerfile`**'
  "${STUB_5F}"
)

# Step Map conditions (#1342). Each new row names the condition that gates it.
Q10_CARRIED_MISSING_FILE='If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it.'
LAZY_ROW_PARTS=('`lazyboards` is asked' '`.lazyboards.yml` exists' 'test -f' "${Q10_CARRIED_MISSING_FILE}")
EXAMPLE_ROW_PARTS=('step 5f' 'analyzer')
FLEET_ROW_PARTS=('`dispatchEnroll`' '`dispatch.planRefined`' '`automerge.enabled`' '`planning.attended`' 'host run' '`detection.inContainer`' '`### Fleet Autonomy Controls`' 'after `phases/autonomy-settings.md`')
AUTONOMY_ROW_PARTS=('`planning`' '`automerge`' '`refinement`' '`refinement.confirmBeforeApply`' '`refinement.automerge`')
OLD_STEP_MAP_EVERY_PHASE='Every phase file is read in full on every run'
OLD_STEP_MAP_INLINE_TAIL='Question 10 onward and generation steps 0 to 7 stay inline in this file.'
STALE_EVERY_INVOCATION='on every configure invocation'

# Fleet guard (P22), pinned from the pre-move text.
FLEET_GUARD_HEAD='**Container guard (runs first).** Use `detection.inContainer`'
FLEET_GUARD_EFFECT='When it is `true`, this section asks nothing, runs no `cenci dispatch` command, and writes nothing anywhere'
FLEET_PROBE_FAILS='If the command exits non-zero, or its stdout is not parseable JSON, log one informational line and fall through'
FLEET_Q11_GATE='`enrolled: false` → ask question 11 below when `dispatchEnroll` is in `delta.ask`'
FLEET_NOT_RECORDED='the container guard, the path-validation skip, a failed status probe, and the other two branches record nothing'
FLEET_LEDGER_DETECT='question 11 in a container or when the status probe fails) is not recorded'

# Board Config (P21), pinned from the pre-move text.
LAZY_NO_FILE_ASKS='**No `.lazyboards.yml` at the repo root** → ask **question 10**'
LAZY_EXISTS_SKIPS='**`.lazyboards.yml` already exists** → **skip question 10**'
LAZY_ANALYZER_LABEL='**Existing config: suggest or skip**'
LAZY_ANALYZER_REPORTS_ONLY='On a delta run with an existing `.lazyboards.yml`, the suggest-or-skip analyzer reports new board actions in the end-of-run summary and never prompts; `--all` restores the Apply/Overwrite/Keep/Show prompt.'
LAZY_TEMPLATE_ONLY_IN_EXAMPLE='Board-level agent launchers. Emitted in BOTH tables'
LAZY_GENERATE_RULE='Write `.lazyboards.yml` at the repo root with the confirmed key mapping'
LAZY_BOARD_Q10='10. **Board config** (no existing `.lazyboards.yml`)'

README_CONFIGURE_ROW_PREFIX='| `configure` | codex.md'

# --- Load the files under test ------------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
require_doc detect_phase "skills/configure/${DETECT_FILE}" || true
require_doc project_phase "skills/configure/${PROJECT_FILE}" || true
require_doc mcp_phase "skills/configure/${MCP_FILE}" || true
require_doc runtime_phase "skills/configure/${RUNTIME_FILE}" || true
require_doc lazy_phase "skills/configure/${LAZY_FILE}" || true
require_doc lazy_example "skills/configure/${LAZY_EXAMPLE_FILE}" || true
require_doc fleet_phase "skills/configure/${FLEET_FILE}" || true
require_doc autonomy_phase "skills/configure/${AUTONOMY_FILE}" || true
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
    "${LAZY_FILE}") printf '%s' "${lazy_phase}" ;;
    "${LAZY_EXAMPLE_FILE}") printf '%s' "${lazy_example}" ;;
    "${FLEET_FILE}") printf '%s' "${fleet_phase}" ;;
    "${AUTONOMY_FILE}") printf '%s' "${autonomy_phase}" ;;
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

  # Ordered: migrations (conditional) first, then the four always-read phase
  # files in the original reading order, then the conditional lazyboards,
  # example, fleet-enrollment and autonomy-settings rows (#1342).
  local prev=-1 file at
  for file in "${MIGRATIONS_FILE}" "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}" "${LAZY_FILE}" "${LAZY_EXAMPLE_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}"; do
    at="$(offset_of "${step_map_body}" "| \`${file}\` |")"
    if [[ "${at}" -lt 0 ]]; then
      fail "${STEP_MAP_HEADING}: no table row for \`${file}\`"
    elif [[ "${at}" -le "${prev}" ]]; then
      fail "${STEP_MAP_HEADING}: row for \`${file}\` is out of reading order"
    fi
    prev="${at}"
  done

  # The first four phase files are read on every run; each row carries file,
  # condition, why.
  local row
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}"; do
    row="$(row_for "${step_map_body}" "${file}")"
    assert_contains "${row}" "${EVERY_RUN}" "${STEP_MAP_HEADING}: \`${file}\` row states it is read on every run"
  done
  assert_contains_ws "${step_map_body}" '| File | Condition | Why |' "${STEP_MAP_HEADING}: table header names file, condition and why"

  # The new rows are conditional: none may claim to be read on every run, and
  # each names its own condition. The router intro no longer says every phase
  # file is read, nor that question 10 onward stays inline.
  for file in "${LAZY_FILE}" "${LAZY_EXAMPLE_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}" "${MIGRATIONS_FILE}"; do
    row="$(row_for "${step_map_body}" "${file}")"
    if [[ -z "${row}" ]]; then
      fail "${STEP_MAP_HEADING}: no row for conditional \`${file}\`"
    elif [[ "${row}" == *"${EVERY_RUN}"* ]]; then
      fail "${STEP_MAP_HEADING}: conditional \`${file}\` row must not say \"${EVERY_RUN}\""
    fi
  done
  local part
  for part in "${LAZY_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${LAZY_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${LAZY_FILE}\` row names its condition"
  done
  for part in "${EXAMPLE_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${LAZY_EXAMPLE_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${LAZY_EXAMPLE_FILE}\` row names its condition"
  done
  for part in "${FLEET_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${FLEET_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${FLEET_FILE}\` row names its condition"
  done
  for part in "${AUTONOMY_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${AUTONOMY_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${AUTONOMY_FILE}\` row names its condition"
  done
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_EVERY_PHASE}" "${STEP_MAP_HEADING}: retired \"every phase file is read\" intro"
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_INLINE_TAIL}" "${STEP_MAP_HEADING}: retired \"question 10 onward stays inline\" tail"

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
  [[ "${#configure_disk_files[@]}" -ge 9 ]] || fail "step map: expected at least 9 phases/references files on disk, found ${#configure_disk_files[@]}"
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
  assert_contains_ws "${question_defaults_body}" "${KEEP_FIRST_RULE}, marked \"(Recommended)\" and pre-selected" "${QUESTION_DEFAULTS_HEADING} states the Keep option is pre-selected"
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

  # P10 (#1342): "lists its Keep option first" is stated once, in the router's
  # single home, and nowhere else across the configure markdown.
  local keep_first_total
  keep_first_total="$(count_occurrences "$(normalize_ws "${configure_all}")" "${KEEP_FIRST_RULE}")"
  [[ "${keep_first_total}" -eq 1 ]] || fail "the phrase [${KEEP_FIRST_RULE}] must occur exactly once across SKILL.md, phases/*.md and references/*.md, found ${keep_first_total}"

  # The autonomy sites reference the rule instead of restating it: the three
  # per-question "Under `--all`, question N is re-offered ..." sentences are
  # one sentence that points at `### Question Defaults`, and each of the
  # Q13/Q14/Q15 `--all` bullets carries the same pointer while keeping its
  # exact option labels.
  if [[ -z "${autonomy_phase}" ]]; then
    fail "${AUTONOMY_FILE}: missing or empty, cannot verify the autonomy sites point at ${QUESTION_DEFAULTS_HEADING}"
    return
  fi
  local autonomy_body before_q13 q13 q14 q15 label
  autonomy_body="$(extract_section_body_raw "${autonomy_phase}" '### Autonomy Settings')"
  before_q13="${autonomy_body%%"${Q13_MARKER}"*}"
  assert_contains_ws "${before_q13}" "${ALL_REOFFER_COLLAPSED}" "${AUTONOMY_FILE}: one collapsed --all re-offer sentence that points at ${QUESTION_DEFAULTS_HEADING}"
  assert_absent_paired_ws "${autonomy_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q13_ALL_REOFFER}" "${AUTONOMY_FILE}: retired per-question Q13 re-offer sentence"
  assert_absent_paired_ws "${autonomy_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q14_ALL_REOFFER}" "${AUTONOMY_FILE}: retired per-question Q14 re-offer sentence"
  assert_absent_paired_ws "${autonomy_body}" "${ALL_REOFFER_COLLAPSED}" "${OLD_Q15_ALL_REOFFER}" "${AUTONOMY_FILE}: retired per-question Q15 re-offer sentence"

  q13="$(extract_between_raw "${autonomy_body}" "${Q13_MARKER}" "${Q14_MARKER}")"
  q14="$(extract_between_raw "${autonomy_body}" "${Q14_MARKER}" "${Q15_MARKER}")"
  q15="$(extract_between_raw "${autonomy_body}" "${Q15_MARKER}" "${Q16_MARKER}")"
  [[ -n "${q13}" && -n "${q14}" && -n "${q15}" ]] || fail "${AUTONOMY_FILE}: question 13/14/15 blocks not found"
  assert_contains_ws "${q13}" "${KEEP_RULE_POINTER}" "question 13 --all bullet points at ${QUESTION_DEFAULTS_HEADING}"
  assert_contains_ws "${q14}" "${KEEP_RULE_POINTER}" "question 14 --all bullet points at ${QUESTION_DEFAULTS_HEADING}"
  assert_contains_ws "${q15}" "${KEEP_RULE_POINTER}" "question 15 --all bullet points at ${QUESTION_DEFAULTS_HEADING}"
  for label in "${Q13_LABELS[@]}"; do
    assert_contains_ws "${q13}" "${label}" "question 13 keeps its option label ${label}"
  done
  for label in "${Q14_LABELS[@]}"; do
    assert_contains_ws "${q14}" "${label}" "question 14 keeps its option label ${label}"
  done
  for label in "${Q15_LABELS[@]}"; do
    assert_contains_ws "${q15}" "${label}" "question 15 keeps its option label ${label}"
  done
  assert_absent_paired_ws "${q13}" "${KEEP_RULE_POINTER}" "${KEEP_FIRST_RULE}" "question 13 does not restate the Keep-first rule"
  assert_absent_paired_ws "${q14}" "${KEEP_RULE_POINTER}" "${KEEP_FIRST_RULE}" "question 14 does not restate the Keep-first rule"
  assert_absent_paired_ws "${q15}" "${KEEP_RULE_POINTER}" "${KEEP_FIRST_RULE}" "question 15 does not restate the Keep-first rule"
}

# Each new phase file opens with a read-condition line that names the Step Map
# (the same shape detection-and-delta.md and the child 1 phases use), and none
# keeps the stale "runs on every configure invocation" claim: these files are
# now read only under a condition.
case_phase_read_conditions() {
  local file content head
  for file in "${LAZY_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}"; do
    content="$(file_content "${file}")"
    if [[ -z "${content}" ]]; then
      fail "${file}: missing or empty"
      continue
    fi
    head="$(head -n 6 <<<"${content}")"
    assert_contains_ws "${head}" 'Read this file' "${file} opens with a read-condition line"
    assert_contains_ws "${head}" '`### Step Map`' "${file} read-condition line names the Step Map"
    assert_absent_paired_ws "${content}" 'Read this file' "${STALE_EVERY_INVOCATION}" "${file} retired \"runs on every configure invocation\" claim"
  done
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

  # --- #1342: pointers to the lazyboards, fleet and autonomy sections ---------
  local defaults_ws site count
  defaults_ws="$(normalize_ws "${question_defaults_body}")"
  assert_contains_ws "${question_defaults_body}" "see ${LAZY_POINTER}" "${QUESTION_DEFAULTS_HEADING} question 10 row names ${LAZY_FILE}"
  assert_contains_ws "${question_defaults_body}" "see ${FLEET_POINTER}" "${QUESTION_DEFAULTS_HEADING} question 11 row names ${FLEET_FILE}"
  count="$(count_occurrences "${defaults_ws}" "see ${AUTONOMY_POINTER}")"
  [[ "${count}" -ge 4 ]] || fail "${QUESTION_DEFAULTS_HEADING}: expected the Keep-first sentence and the question 13/14/15 rows to name ${AUTONOMY_FILE} (4 sites), found ${count}"
  assert_contains_ws "${skill}" "${FLEET_AUTONOMY_POINTER}" "SKILL.md step 7 names the file holding the fleet Next step block"
  assert_contains_ws "${skill}" "question 13 in \`${AUTONOMY_FILE}\` was asked" "SKILL.md step 6 wiring names ${AUTONOMY_FILE} for question 13"
  assert_contains_ws "${skill}" "question 14 in \`${AUTONOMY_FILE}\` was asked" "SKILL.md step 6 wiring names ${AUTONOMY_FILE} for question 14"
  assert_contains_ws "${skill}" "question 15 in \`${AUTONOMY_FILE}\` was asked" "SKILL.md step 6 wiring names ${AUTONOMY_FILE} for question 15"
  assert_contains_ws "${skill}" "\`### Autonomy Settings\` question 13 in \`${AUTONOMY_FILE}\`" "SKILL.md planning field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${skill}" "\`### Autonomy Settings\` question 14 in \`${AUTONOMY_FILE}\`" "SKILL.md automerge field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${skill}" "\`### Autonomy Settings\` question 15 in \`${AUTONOMY_FILE}\`" "SKILL.md refinement field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${skill}" 'derivation tables in `### Board Config (lazyboards)` in `phases/lazyboards.md`' "SKILL.md names the file holding the lazyboards derivation tables"
  assert_contains_ws "${skill}" 'fallback as question 13/14/15 in `phases/autonomy-settings.md`' "SKILL.md step 6 fleet-leaf overlay names ${AUTONOMY_FILE} for the question 13/14/15 validation"
  assert_absent_paired_ws "${skill}" 'fallback as question 13/14/15 in `phases/autonomy-settings.md`' 'fallback as question 13/14/15 below' "SKILL.md retired positional question 13/14/15 validation pointer"
  for site in "${OLD_SKILL_AUTONOMY_POINTERS[@]}"; do
    assert_absent_paired_ws "${skill}" "${AUTONOMY_POINTER}" "${site}" "SKILL.md retired positional pointer [${site}]"
  done

  # Detection and Delta Resolution name the files holding the sections they cite.
  assert_contains_ws "${detect_phase}" "(see ${AUTONOMY_POINTER})" "${DETECT_FILE}: the fallback pointer names ${AUTONOMY_FILE}"
  assert_contains_ws "${detect_phase}" "(see ${FLEET_POINTER})" "${DETECT_FILE}: the fleet pointers name ${FLEET_FILE}"
  assert_contains_ws "${detect_phase}" "${LAZY_POINTER}" "${DETECT_FILE}: the analyzer pointer names ${LAZY_FILE}"
  assert_absent_paired_ws "${detect_phase}" '### Delta Resolution' '(see `### Autonomy Settings`)' "${DETECT_FILE}: retired positional Autonomy Settings pointer"
  assert_absent_paired_ws "${detect_phase}" '### Delta Resolution' '(see `### Fleet Dispatch Enrollment`)' "${DETECT_FILE}: retired positional Fleet Dispatch Enrollment pointer"
  assert_absent_paired_ws "${detect_phase}" '### Delta Resolution' '`### Board Config (lazyboards)`)' "${DETECT_FILE}: retired positional Board Config pointer"

  # The moved sections point at what they left behind by file name.
  assert_contains_ws "${autonomy_phase}" 'unlike `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`' "${AUTONOMY_FILE}: the intro names ${FLEET_FILE}"
  assert_contains_ws "${autonomy_phase}" 'step 6 in `SKILL.md`' "${AUTONOMY_FILE}: the intro names SKILL.md for the step 6 write"
  assert_contains_ws "${autonomy_phase}" '(see `### Delta Resolution` in `phases/detection-and-delta.md`)' "${AUTONOMY_FILE}: the resolver-failure rule names ${DETECT_FILE}"
  assert_absent_paired_ws "${autonomy_phase}" '### Autonomy Settings' '(see `### Delta Resolution`)' "${AUTONOMY_FILE}: retired positional Delta Resolution pointer"
  assert_absent_paired_ws "${autonomy_phase}" '### Autonomy Settings' 'step 6 below' "${AUTONOMY_FILE}: retired positional step 6 pointer"
  assert_contains_ws "${fleet_phase}" '`### Create Worktree` in `SKILL.md`' "${FLEET_FILE}: the main-checkout rule names SKILL.md for Create Worktree"
}

# P21 (#1342): the lazyboards phase is read only when question 10 is asked or
# `.lazyboards.yml` exists; with the file present question 10 is not asked and
# the suggest-or-skip analyzer runs (reporting only on a delta run). The
# carried-but-missing report is the router's, because that is the one state in
# which the phase file is not read.
case_P21_lazyboards_condition() {
  [[ -n "${lazy_phase}" ]] || { fail "P21: ${LAZY_FILE} missing or empty"; return; }
  local row board example_row
  row="$(row_for "${step_map_body}" "${LAZY_FILE}")"
  [[ -n "${row}" ]] || { fail "P21: no ${STEP_MAP_HEADING} row for ${LAZY_FILE}"; return; }
  assert_contains_ws "${row}" "${Q10_CARRIED_MISSING_FILE}" "P21: the router row reports a carried question 10 whose .lazyboards.yml is missing"
  # The existence check is the router's own call, and a read of the phase file
  # is what the check gates; both are wired in the Step Map, not just stated.
  assert_contains_ws "${step_map_body}" '**own** Bash call' "P21: ${STEP_MAP_HEADING} runs the .lazyboards.yml existence check as its own Bash call"

  board="$(extract_section_body_raw "${lazy_phase}" '### Board Config (lazyboards)')"
  [[ -n "${board}" ]] || { fail "P21: ${LAZY_FILE} Board Config section body empty"; return; }
  assert_contains_ws "${board}" "${LAZY_NO_FILE_ASKS}" "P21: question 10 is asked only when no .lazyboards.yml exists"
  assert_contains_ws "${board}" "${LAZY_EXISTS_SKIPS}" "P21: an existing .lazyboards.yml skips question 10"
  assert_contains_ws "${board}" "${LAZY_ANALYZER_LABEL}" "P21: the existing-file branch runs the suggest-or-skip analyzer"
  assert_contains_ws "${board}" "${LAZY_ANALYZER_REPORTS_ONLY}" "P21: on a delta run the analyzer only reports"
  assert_contains_ws "${board}" "${LAZY_BOARD_Q10}" "P21: ${LAZY_FILE} holds question 10"

  # The analyzer and the generation rules travel with the question.
  assert_contains_ws "${lazy_phase}" '5f. **Generate `.lazyboards.yml`**' "P21: ${LAZY_FILE} holds step 5f"
  assert_contains_ws "$(extract_between_raw "${lazy_phase}" '5f. **Generate' '__never_matches__')" "${LAZY_ANALYZER_LABEL}" "P21: the analyzer sub-step lives in step 5f"

  # The router keeps none of it (paired with the stub that points at the file).
  assert_absent_paired_ws "${skill}" "${STUB_5F}" "${LAZY_BOARD_Q10}" "P21: SKILL.md no longer carries question 10"
  assert_absent_paired_ws "${skill}" "${STUB_5F}" "${LAZY_ANALYZER_LABEL}" "P21: SKILL.md no longer carries the analyzer sub-step"
  assert_absent_paired_ws "${skill}" "${STUB_5F}" "${LAZY_GENERATE_RULE}" "P21: SKILL.md no longer carries the 5f generation rules"
  assert_absent_paired_ws "${skill}" "${STUB_5F}" "${LAZY_TEMPLATE_ONLY_IN_EXAMPLE}" "P21: SKILL.md no longer carries the generated-file example"

  # The full worked example lives only in the reference file, fenced at column 0.
  example_row="$(row_for "${step_map_body}" "${LAZY_EXAMPLE_FILE}")"
  [[ -n "${example_row}" ]] || fail "P21: no ${STEP_MAP_HEADING} row for ${LAZY_EXAMPLE_FILE}"
  [[ -n "${lazy_example}" ]] || { fail "P21: ${LAZY_EXAMPLE_FILE} missing or empty"; return; }
  assert_contains_ws "${lazy_example}" "${LAZY_TEMPLATE_ONLY_IN_EXAMPLE}" "P21: ${LAZY_EXAMPLE_FILE} holds the generated-file example"
  [[ "$(count_exact_lines "${lazy_example}" '```yaml')" -eq 1 ]] || fail "P21: ${LAZY_EXAMPLE_FILE} must hold exactly one column-0 \`\`\`yaml fence"
  assert_absent_paired_ws "${lazy_phase}" '5f. **Generate' "${LAZY_TEMPLATE_ONLY_IN_EXAMPLE}" "P21: ${LAZY_FILE} does not duplicate the full example"
  assert_contains_ws "${lazy_phase}" "\`${LAZY_EXAMPLE_FILE}\`" "P21: ${LAZY_FILE} points at ${LAZY_EXAMPLE_FILE}"
}

# P22 (#1342): in a container, or when `cenci dispatch status --json` fails,
# question 11 is not asked and `dispatchEnroll` is not recorded. The guard moved
# verbatim into the fleet phase, so the router must still read that file on
# every host run (the half-enrolled question 12 repair has no ID) and must not
# keep a copy of the guard.
case_P22_fleet_guard() {
  [[ -n "${fleet_phase}" ]] || { fail "P22: ${FLEET_FILE} missing or empty"; return; }
  local row dispatch
  row="$(row_for "${step_map_body}" "${FLEET_FILE}")"
  [[ -n "${row}" ]] || { fail "P22: no ${STEP_MAP_HEADING} row for ${FLEET_FILE}"; return; }
  assert_contains_ws "${row}" 'host run' "P22: the router reads ${FLEET_FILE} on every host run so the question 12 repair stays reachable"

  dispatch="$(extract_section_body_raw "${fleet_phase}" '### Fleet Dispatch Enrollment')"
  [[ -n "${dispatch}" ]] || { fail "P22: ${FLEET_FILE} Fleet Dispatch Enrollment section body empty"; return; }
  assert_contains_ws "${dispatch}" "${FLEET_GUARD_HEAD}" "P22: the container guard runs first"
  assert_contains_ws "${dispatch}" "${FLEET_GUARD_EFFECT}" "P22: in a container the section asks nothing, runs nothing and writes nothing"
  assert_contains_ws "${dispatch}" "${FLEET_PROBE_FAILS}" "P22: a failed status probe never blocks the run and asks nothing further"
  assert_contains_ws "${dispatch}" "${FLEET_Q11_GATE}" "P22: question 11 is asked only on the enrolled:false branch"
  assert_contains_ws "${dispatch}" "${FLEET_NOT_RECORDED}" "P22: the container guard and a failed probe record nothing"
  assert_contains_ws "${detect_phase}" "${FLEET_LEDGER_DETECT}" "P22: ${DETECT_FILE} keeps the ledger rule for a container or failed probe"

  # The guard is not duplicated in the router (paired with the fleet row).
  assert_absent_paired_ws "${skill}" "\`${FLEET_FILE}\`" "${FLEET_GUARD_HEAD}" "P22: SKILL.md does not copy the container guard"
  assert_absent_paired_ws "${skill}" "\`${FLEET_FILE}\`" 'cenci dispatch status --json --dir' "P22: SKILL.md does not copy the status probe"

  # Fleet Autonomy Controls is asked after autonomy-settings and keeps its own
  # container guard and apply timing.
  local controls
  controls="$(extract_section_body_raw "${fleet_phase}" '### Fleet Autonomy Controls')"
  [[ -n "${controls}" ]] || { fail "P22: ${FLEET_FILE} Fleet Autonomy Controls section body empty"; return; }
  assert_contains_ws "${controls}" 'after `phases/autonomy-settings.md`' "P22: Fleet Autonomy Controls says it is asked after ${AUTONOMY_FILE}"
  assert_contains_ws "${controls}" '**Container guard and apply timing.**' "P22: Fleet Autonomy Controls keeps its container guard and apply timing"
  assert_contains_ws "${controls}" 'never claim deferred fleet choices were applied' "P22: a sandbox run never claims a deferred fleet choice was applied"
  assert_contains_ws "${controls}" '**Next step.**' "P22: the fleet Next step block moved with Fleet Autonomy Controls"
}

case_allowed_tools_only_in_skill_md() {
  local line_count disk
  line_count="$(grep -c '^allowed-tools:' <<<"${skill}" | tr -d '[:space:]')"
  [[ "${line_count}" -eq 1 ]] || fail "SKILL.md must carry exactly one allowed-tools: frontmatter line, found ${line_count}"
  [[ "${#configure_disk_files[@]}" -ge 9 ]] || fail "allowed-tools guard is vacuous: found ${#configure_disk_files[@]} phases/references files (expected at least 9)"
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
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}" "${LAZY_FILE}" "${LAZY_EXAMPLE_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}" "${MIGRATIONS_FILE}"; do
    assert_contains "${row}" "${file}" "README.md workflow-deps row for configure lists ${file}"
  done
}

# --- Run every case ----------------------------------------------------------

case_step_map
case_P17_fallbacks_colocated
case_P19_migrations_conditional
case_single_keep_or_change_rule
case_phase_read_conditions
case_P21_lazyboards_condition
case_P22_fleet_guard
case_moved_headings_unique
case_cross_file_pointers
case_allowed_tools_only_in_skill_md
case_readme_workflow_deps_row

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-router-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-router-contract.test.sh: all checks passed."
