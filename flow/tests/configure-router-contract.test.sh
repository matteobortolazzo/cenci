#!/usr/bin/env bash
# Contract test for tickets #1341, #1342, #1343 and #1344 (parent #692) --
# flow/skills/configure/SKILL.md becomes a router: the detection,
# delta-resolution and questions 1-9c prose moves verbatim into four
# phases/*.md files, the legacy-migration prose moves into the conditionally
# read references/migrations.md (#1341), the lazyboards (question 10 and
# step 5f), fleet enrollment (questions 11, 12, 18-20) and autonomy
# (questions 13-17) prose moves into three more conditionally read phase files
# plus references/lazyboards-example.md (#1342), and generation steps 0 to
# 5c-bis move into phases/guidance-settings.md, steps 5d and 5e into
# phases/ci-dockerfile.md, and the base deny list into
# references/settings-template.md (#1343), and finally step 6 into
# phases/write-config.md (with the `sandbox` merge section), the config example
# and field schema into references/config-schema.md, step 7 and the final
# report into phases/commit-pr.md, and the three board migration notes into
# references/migrations.md; `### Board lifecycle labels` shrinks to a pointer at
# the plugin's docs/board-labels.md and the router gains `### Run Order`
# (#1344). The skill must behave exactly as before.
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
#   * case_moved_step_markers_unique  (#1343) each moved generation step marker
#                                 occurs once, in its new file, never in SKILL.md
#   * case_P30_step1_verbatim    (#1343) step 1 moved byte for byte
#   * case_P6_dockerfile_verbatim  (#1343) step 5e's Merge-safe regeneration
#                                 bullets moved byte for byte
#   * case_P1_router_size         (#1344) SKILL.md is at most 300 lines and keeps
#                                 no step 6 or step 7 body inline
#   * case_P2_every_file_named_with_condition  (#1344) every phases/*.md and
#                                 references/*.md file on disk has a Step Map row
#                                 whose Condition cell is non-empty
#   * case_P3_run_order           (#1344) `### Run Order`: questions 1 to 20 in
#                                 order, then generation steps 0 to 7; a first run
#                                 and --all select every question section, a delta
#                                 run asks only delta.ask; resolver runs show it
#   * case_P23_empty_delta_runs_generation  (#1344) an empty delta.ask asks nothing
#                                 and every generation step is still selected
#   * case_P7_step6_verbatim      (#1344) step 6's overlay order and validation,
#                                 the ledger program and the fallen-back paragraph
#                                 moved byte for byte into phases/write-config.md
#   * case_step6_step7_markers    (#1344) the step 6 and step 7 stubs keep the
#                                 moved bold markers
#   * case_sandbox_merge_in_write_config  (#1344) the sandbox merge section moved
#                                 into phases/write-config.md
#   * case_P8_step7_recovery_verbatim  (#1344) step 7's recovery branches moved
#                                 byte for byte into phases/commit-pr.md
#   * case_P9_board_labels_pointer  (#1344) `### Board lifecycle labels` is a
#                                 pointer; the orthogonal-marker restatements are
#                                 gone from every configure file
#   * case_P11_schema_only_in_reference  (#1344) no fenced JSON or YAML in
#                                 SKILL.md; the example and field schema occur once,
#                                 in references/config-schema.md
#   * case_P24_board_migration_notes  (#1344) the three board migration notes live
#                                 in references/migrations.md `## Board migration
#                                 notes`, read when re-configuring an existing
#                                 project
#
# Pinned constants, never derived from the doc under test. Every absence check
# is paired with an existence marker (flow/docs/shell-scripting-gotchas.md) so
# it cannot pass vacuously; extractors are pure (no fail() inside $(...)). The
# byte pins of #1344 (P7, P8, P24) were taken from the pre-move SKILL.md
# (`git show main:flow/skills/configure/SKILL.md`), never from the file under
# test. Negative (retired-pattern) scans cover SKILL.md plus every phases/*.md
# and references/*.md (configure_all) with a minimum file count.
#
# Covered files:
#   - flow/skills/configure/SKILL.md, phases/*.md, references/migrations.md,
#     references/lazyboards-example.md, references/settings-template.md,
#     phases/write-config.md, phases/commit-pr.md, references/config-schema.md
#   - flow/README.md, flow/docs/board-labels.md (the trivial-ticket sentence)
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
  set_norm "${content}"
  norm="${NORM}"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# set_norm <content> -- sets NORM to the whitespace-normalized <content>. The two
# large contents the #1344 negatives scan over and over (the router and the
# aggregate of every configure markdown file) are normalized once, after loading,
# and reused; anything else is normalized on demand. Not for use inside $(...)
# (NORM would be lost), so only the assert_* helpers call it.
NORM=""
SKILL_WS=""
CONFIGURE_ALL_WS=""
set_norm() {
  local c="$1"
  if [[ -n "${CONFIGURE_ALL_WS}" && "${c}" == "${configure_all}" ]]; then
    NORM="${CONFIGURE_ALL_WS}"
  elif [[ -n "${SKILL_WS}" && "${c}" == "${skill}" ]]; then
    NORM="${SKILL_WS}"
  else
    NORM="$(normalize_ws "${c}")"
  fi
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden> <label>
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4" norm
  set_norm "${content}"
  norm="${NORM}"
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
# #1343 (3/4): generation steps 0-5c-bis, the base deny list, and steps 5d-5e.
GUIDANCE_FILE='phases/guidance-settings.md'
SETTINGS_TEMPLATE_FILE='references/settings-template.md'
CI_FILE='phases/ci-dockerfile.md'
# #1344 (4/4): step 6 with the sandbox merge section, the config example and
# field schema, and step 7 with the final report.
WRITE_CONFIG_FILE='phases/write-config.md'
SCHEMA_FILE='references/config-schema.md'
COMMIT_PR_FILE='phases/commit-pr.md'

STEP_MAP_HEADING='### Step Map'
RUN_ORDER_HEADING='### Run Order'
QUESTION_DEFAULTS_HEADING='### Question Defaults'
BOARD_LABELS_HEADING='### Board lifecycle labels'

INTERACTION_RULE_EXTENSION='This also governs the `phases/*.md` and `references/*.md` files this skill reads.'
EVERY_RUN='Every run'
LEGACY_CONDITION='`.claude/config.json` exists'
# #1344: references/migrations.md holds two sections, each with its own read
# condition. The Step Map row names both; the file-level "never reads it"
# sentence became the section-level pin below (tightens, never loosens).
MIGRATIONS_LEGACY_HEADING='## Legacy config migration'
MIGRATIONS_BOARD_HEADING='## Board migration notes'
MIGRATIONS_ROW_PARTS=("${LEGACY_CONDITION}" '`## Legacy config migration`' '`## Board migration notes`' 're-configuring an existing project')
MIGRATIONS_ROW_NEVER_READ='never reads `## Legacy config migration`'
MIGRATIONS_LEGACY_NEVER_READ='A `.cenci/config.json`-only run never reads this section.'
MIGRATIONS_BOARD_CONDITION='Read this section only when re-configuring an existing project (`existingConfig` or `migrationBase` is present)'
FAIL_CLOSED_READ_RULE='If a file marked `Every run` cannot be read, stop and report — never reconstruct step 6 (`phases/write-config.md`) or step 7 (`phases/commit-pr.md`) from memory.'

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
  "${GUIDANCE_FILE}|### MCP Server Configuration"
)

# Markers that stay inline in SKILL.md: Auth Verification (always run) and the
# five stubs that defer to phases/guidance-settings.md (steps 0 to 5c-bis),
# phases/ci-dockerfile.md (steps 5d and 5e), phases/lazyboards.md (step 5f),
# phases/write-config.md (step 6) and phases/commit-pr.md (step 7, #1344).
# Step 5e itself moved out in #1343, so its marker is no longer inline.
STUB_5F='run step 5f in `phases/lazyboards.md` when that file was read this run'
STUB_GUIDANCE='run steps 0 to 5c-bis in `phases/guidance-settings.md`'
STUB_CI='run steps 5d and 5e in `phases/ci-dockerfile.md`; each step decides internally whether it runs'
# #1344: steps 6 and 7 are stubs too. Each keeps the moved bold marker (as the 5f
# stub does) so the existence anchor in configure-fragment-markers.test.sh holds.
STUB_6='6. **Write `.cenci/config.json`**: run step 6 in `phases/write-config.md`'
STUB_7='7. **Commit, Push, and Open PR**: run step 7 in `phases/commit-pr.md`'
STEP6_MARKER='6. **Write `.cenci/config.json`**'
STEP7_MARKER='7. **Commit, Push, and Open PR**'
INLINE_MARKERS=(
  '### Auth Verification'
  "${STUB_GUIDANCE}"
  "${STUB_CI}"
  "${STUB_5F}"
  "${STUB_6}"
  "${STUB_7}"
)

# Moved step markers (#1343): "<file>|<exact marker>". Each must occur exactly
# once across SKILL.md, phases/*.md and references/*.md, in the named file, and
# zero times in SKILL.md (where only the stubs above remain).
MOVED_STEP_MARKERS=(
  "${GUIDANCE_FILE}|0. **Generate Codex agent adapters**"
  "${GUIDANCE_FILE}|1. **Generate canonical guidance and client adapters**"
  "${GUIDANCE_FILE}|2. Create the \`docs/\` directory at the repo root (if it doesn't exist) and deploy on-demand reference docs"
  "${GUIDANCE_FILE}|3a. For each project, create \`<project-path>/AGENTS.md\` using \`templates/agents-md-project.md\`"
  "${GUIDANCE_FILE}|3c. **Ensure board lifecycle labels exist**"
  "${GUIDANCE_FILE}|4. **Create or update \`.claude/settings.json\`**"
  "${GUIDANCE_FILE}|5. Update \`.gitignore\`:"
  "${GUIDANCE_FILE}|5b. **Generate \`.claudeignore\`**"
  "${GUIDANCE_FILE}|5c. **Configure auto-compact**"
  "${GUIDANCE_FILE}|5c-bis. **Pin subagents to 200K context**"
  "${CI_FILE}|5d. **Generate CI/CD pipeline**"
  "${CI_FILE}|5e. **Generate \`.cenci/Dockerfile\`**"
)

# Step Map conditions (#1342). Each new row names the condition that gates it.
Q10_CARRIED_MISSING_FILE='If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it.'
LAZY_ROW_PARTS=('`lazyboards` is asked' '`.lazyboards.yml` exists' 'test -f' "${Q10_CARRIED_MISSING_FILE}")
EXAMPLE_ROW_PARTS=('step 5f' 'analyzer')
FLEET_ROW_PARTS=('`dispatchEnroll`' '`dispatch.planRefined`' '`automerge.enabled`' '`planning.attended`' 'host run' '`detection.inContainer`' '`### Fleet Autonomy Controls`' 'after `phases/autonomy-settings.md`')
AUTONOMY_ROW_PARTS=('`planning`' '`automerge`' '`refinement`' '`refinement.confirmBeforeApply`' '`refinement.automerge`')
OLD_STEP_MAP_EVERY_PHASE='Every phase file is read in full on every run'
OLD_STEP_MAP_INLINE_TAIL='Question 10 onward and generation steps 0 to 7 stay inline in this file.'
# #1343: generation steps 0 to 5f no longer all stay inline. The intro sentence
# that claimed so is retired, and the new one names where each step range runs.
# #1344: steps 6 and 7 moved too, so "only steps 6 and 7 stay inline" is retired
# and the intro names the file each of them runs from.
OLD_STEP_MAP_GENERATION_INLINE='Generation steps 0 to 7 stay inline in this file'
OLD_STEP_MAP_STEPS_6_7_INLINE='only steps 6 and 7 stay inline in this file'
STEP_MAP_INTRO_PARTS=('steps 0 to 5c-bis' 'steps 5d and 5e' 'step 5f' 'step 6' 'step 7' '`phases/guidance-settings.md`' '`phases/ci-dockerfile.md`' '`phases/lazyboards.md`' '`phases/write-config.md`' '`phases/commit-pr.md`')
# The three generation rows are read on every run; each names its own position.
GUIDANCE_ROW_PARTS=('Auth Verification' 'steps 0 to 5c-bis')
SETTINGS_TEMPLATE_ROW_PARTS=('step 4' 'deny')
CI_ROW_PARTS=('steps 5d and 5e' 'after `phases/guidance-settings.md`')
# #1344: the last three rows are read on every run too. Step 6 follows the Dockerfile
# step, the schema is read from step 6 (as the settings template is read from step 4),
# and step 7 follows step 6.
WRITE_CONFIG_ROW_PARTS=('step 6' 'after `phases/ci-dockerfile.md`')
SCHEMA_ROW_PARTS=('step 6' '`phases/write-config.md`')
COMMIT_PR_ROW_PARTS=('step 7' 'final report' 'after `phases/write-config.md`')
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

# Cross-file pointers (#1343): a positional "above"/"below" or a bare step number
# that now crosses files names the file ("step N in `<file>`", or "`### X` in
# `<file>`"). Each OLD_* form is the retired positional pointer.
# #1344: `### Board lifecycle labels` shrank to a pointer, so step 3c's reconciler
# pointer now names the reconciler bullet in the plugin's docs/board-labels.md.
STEP3C_BOARD_POINTER='see the reconciler bullet in the plugin'"'"'s `docs/board-labels.md`'
OLD_STEP3C_BOARD_POINTER='under Board lifecycle labels below'
OLD_STEP3C_SKILL_POINTER='`### Board lifecycle labels` in `SKILL.md`'
# Step 6's sandbox merge section (now in phases/write-config.md) still names the
# file that holds each step it cites.
STEP6_5E_POINTER='(see step 5e in `phases/ci-dockerfile.md`)'
OLD_STEP6_5E_POINTER='(see step 5e above)'
STEP6_STEP4_POINTER='Step 4 in `phases/guidance-settings.md` always writes'
OLD_STEP6_STEP4_POINTER='Step 4 above'
# #1344: pointers at step 6, step 7 and the schema that now cross files name the
# file. The moved phase/reference text and the router's own early prose both
# carry them.
AUTONOMY_STEP6_POINTER='step 6 in `phases/write-config.md`'
OLD_AUTONOMY_STEP6_POINTER='step 6 in `SKILL.md`'
RUNTIME_DIND_POINTER='see the `sandbox.dind` schema note in `phases/write-config.md`'
OLD_RUNTIME_DIND_POINTER='see the `sandbox.dind` schema note in `SKILL.md`'
RUNTIME_AZURE_POINTER='see the `sandbox.azure` schema note in `phases/write-config.md`'
OLD_RUNTIME_AZURE_POINTER='see the `sandbox.azure` schema note in `SKILL.md`'
SCHEMA_RESOLVER_POINTER='The configure flow calls this resolver itself — see `### Delta Resolution` in `phases/detection-and-delta.md`.'
DETECT_STEP1_POINTER='step 1 in `phases/guidance-settings.md`'
OLD_DETECT_STEP1_POINTER='diff-approval gate in step 1 stays'
DETECT_5E_POINTER='step 5e in `phases/ci-dockerfile.md`'
OLD_DETECT_5E_POINTER='(see step 5e and `### Board Config (lazyboards)`'
MCP_OPEN_POINTER='`phases/guidance-settings.md`'
OLD_MCP_OPEN_POINTER='because generation steps 1b and the MCP configuration use them'
RUNTIME_5C_POINTER='see step 5c in `phases/guidance-settings.md` for the exact jq/mv sequence'
OLD_RUNTIME_5C_POINTER='see step 5c for the exact jq/mv sequence'
RUNTIME_5C_BIS_POINTER='see step 5c-bis in `phases/guidance-settings.md` for the exact jq/mv sequence'
OLD_RUNTIME_5C_BIS_POINTER='see step 5c-bis for the exact jq/mv sequence'
RUNTIME_5E_POINTER='see step 5e in `phases/ci-dockerfile.md`'
OLD_RUNTIME_5E_POINTER='see step 5e).'
RUNTIME_OPEN_POINTER='steps 5d and 5e in `phases/ci-dockerfile.md`'
OLD_RUNTIME_OPEN_POINTER='steps 5d and 5e use them'
MONOREPO_ANCHOR='**Monorepo**: fragments are'

# P30 (#1343): step 1's AGENTS.md/CLAUDE.md generation moves verbatim --
# whatever #1182 has shipped when the work starts (#1182 is open, so this is the
# AGENTS.md-canonical text with CLAUDE.md import adapters). Pinned byte for byte
# from the pre-move SKILL.md (`git show main:flow/skills/configure/SKILL.md`,
# lines 133-150), never read from the doc under test. Mirrors the P5 pins in
# configure-fragment-markers.test.sh.
IFS= read -r -d '' P30_STEP1_BLOCK <<'PINNED_BLOCK'
1. **Generate canonical guidance and client adapters**:
   - `AGENTS.md` at the repository root is canonical shared guidance. Use
     `templates/agents-md-root.md` or `templates/agents-md-root-monorepo.md`.
   - For each monorepo project, generate `<project>/AGENTS.md` from
     `templates/agents-md-project.md`.
   - Generate root `CLAUDE.md` as `@AGENTS.md`, followed only by an optional block
     delimited with `<!-- cenci:claude-only:start -->` and
     `<!-- cenci:claude-only:end -->`.
   - Generate each `<project>/CLAUDE.md` as `@AGENTS.md` with the same optional block.

   Discover root `AGENTS.md`, root `CLAUDE.md`, legacy `.claude/CLAUDE.md`, and every
   configured project's AGENTS/CLAUDE pair. A retained `.claude/CLAUDE.md` adapter imports
   `@../AGENTS.md`. Before replacing any substantive existing guidance, render and show
   the complete proposed diff with `git diff --no-index`, then require approval through
   `AskUserQuestion`. Preserve existing material by merging it into the shared AGENTS
   content or the explicitly Claude-only block. Never silently delete guidance. Stable
   markers make reruns idempotent. Never delete a second substantive guidance file unless
   the user explicitly approves its content having been merged into AGENTS.md.
PINNED_BLOCK
P30_STEP1_BLOCK="${P30_STEP1_BLOCK%$'\n'}"

# P6 (#1343): step 5e's "Merge-safe regeneration" bullets (both markers, no
# markers, malformed markers; delta versus --all/full) move verbatim. Pinned
# byte for byte from the pre-move SKILL.md (lines 773-782). The first line is
# the `**Merge-safe regeneration**` lead-in; the last is the malformed-markers
# bullet whose delta-run sentence is also pinned on its own as P20 in
# configure-delta-rerun.test.sh.
IFS= read -r -d '' P6_MERGE_SAFE_BLOCK <<'PINNED_BLOCK'
   **Merge-safe regeneration**: the whole block above (from `# cenci:managed-begin` through `# cenci:managed-end` inclusive) is the managed block.
   - **File doesn't exist**: create `.cenci/` (`mkdir -p .cenci`) and write the managed block as the full file content.
   - **Effective sandbox flags, per ID**: the fragment selection uses the effective `sandbox.enabled`, `sandbox.dind` and `sandbox.azure` values, decided per question ID. An ID in `delta.carried` reads the existing config value; an ID in `delta.ask` this run (its key was absent and it was never offered, e.g. a config with `sandbox.enabled: true` that predates questions 9b/9c asks `sandbox.dind` and `sandbox.azure`) uses the answer just collected, so a just-answered Yes is consumed by this same generation. A Dockerfile is generated or refreshed whenever the effective `sandbox.enabled` is `true`, whether it was carried or just answered. The refresh itself never asks a question.
   - **File exists with both markers present**: replace only the text from `# cenci:managed-begin` through `# cenci:managed-end` (inclusive) with the freshly generated block. Preserve everything before the begin marker and everything after the end marker exactly as-is — this is where a team can hand-append their own `RUN` steps across re-runs. On a delta run with valid `# cenci:managed-begin` / `# cenci:managed-end` markers, refresh the managed block silently — no Q9/Q9b/Q9c prompt — and leave every byte outside the markers unchanged. Report the block diff with `git -C <worktree-path> diff -- .cenci/Dockerfile`. Only an ID that is in `delta.ask` (key absent and never offered) puts a Q9/Q9b/Q9c question to the user on a delta run; the markers never cause one.
   - **File exists with no markers** (e.g. a hand-authored legacy `.cenci/Dockerfile`): do not silently overwrite it. On a delta run, a `.cenci/Dockerfile` with no markers or malformed markers is left untouched and reported — no prompt. On an `--all` or `full` run, a `.cenci/Dockerfile` with no markers or malformed markers gets the Overwrite/Skip/Show prompt. It reuses the conflict-check UX already used for CI/CD generation (question 8 / step 5d) — same three options, same behavior:
     "Found existing `.cenci/Dockerfile` without cenci's managed markers. What would you like to do?"
     Options: "Overwrite — wrap it in managed markers and replace with the generated block", "Skip — keep the existing file, still record `sandbox` in config.json", "Show existing — display the current file contents"
     - If Skip: still record `sandbox` in config.json, don't write the file.
     - If Show existing: read and display the file, then re-ask Overwrite/Skip.
   - **File exists with malformed markers** (exactly one of `# cenci:managed-begin` / `# cenci:managed-end` present, markers out of order, or duplicate marker pairs): do **not** attempt a partial text replace — a malformed marker pair cannot be trusted to safely bound the managed block, and a replacement span is never guessed. On a delta run it is left untouched and reported exactly like the "no markers" case above (no prompt); on an `--all` or `full` run it takes the same Overwrite/Skip/Show prompt — same prompt text, same three options, same behavior.
PINNED_BLOCK
P6_MERGE_SAFE_BLOCK="${P6_MERGE_SAFE_BLOCK%$'\n'}"

# --- #1344 byte pins ----------------------------------------------------------
# Each block below is pinned byte for byte from the pre-move SKILL.md
# (`git show main:flow/skills/configure/SKILL.md`), never from the file under
# test, so a changed byte in a moved block fails the suite. Mirrors the P30 and
# P6 pins above.

# P7 (#1344): step 6's overlay order and validation (old line 173). The leading
# three spaces are part of the pin: the block moved with its list indentation.
IFS= read -r -d '' P7_OVERLAY_ORDER_BLOCK <<'PINNED_BLOCK'
   **Overlay order and validation.** The overlay order is `merge-sandbox-config.sh` stdout, then the question 13/14/15 overlays (`cenci:configure-q13-switch`, `cenci:configure-q14-remove`, `cenci:configure-q15-switch`), then the independent leaf overlays (`cenci:configure-confirm-before-apply`, `cenci:configure-refinement-automerge`), then the `configureOffered` ledger (`cenci:configure-offered-ledger`). Run each overlay with `jq` over the candidate config as its own Bash call (write the candidate to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-config.json` as the scratch input and remove it afterwards under the existing `rm -f` grant — that `rm -f` also runs when an overlay fails validation, so no scratch file is left behind). An overlay result is valid only when the `jq` call exits 0 and the result passes `jq -e 'type == "object"'`: an empty output passes `jq empty`, so `jq empty` alone never gates a write. Apply that check after each overlay and before every write — the same fail-closed discipline as the adjacent `sandbox` merge above: if an overlaid result fails it, do not write it, report the failure, and fall back to the content before that overlay (the previous step's output as-is). A fallback that drops the ledger overlay still writes, but tell the user the ledger could not be stamped, so the questions offered this run will be asked again.
PINNED_BLOCK
P7_OVERLAY_ORDER_BLOCK="${P7_OVERLAY_ORDER_BLOCK%$'\n'}"

# P7 (#1344): the fallen-back question 13/14/15 paragraph (old line 175), which
# removes the fallen-back ID from the offered set before the ledger overlay runs.
IFS= read -r -d '' P7_FALLEN_BACK_BLOCK <<'PINNED_BLOCK'
   **A fallen-back question 13/14/15 overlay was not applied.** When a question 13, 14 or 15 overlay fails the check above, the collected answer is not in the written config. Tell the user which question's answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; remove that question's ID (`planning` for question 13, `automerge` for question 14, `refinement` for question 15) from the offered set before the ledger overlay runs, so the ledger never records an offer whose answer was dropped; and key step 7 to the overlay actually applied — a fallen-back "Remove the block(s)" never emits the `## Review required — automerge policy removed` section and is never reported as removed, and a fallen-back scaffold never emits `## Review required — automerge policy`.
PINNED_BLOCK
P7_FALLEN_BACK_BLOCK="${P7_FALLEN_BACK_BLOCK%$'\n'}"

# P7 (#1344): the ledger stamp paragraph and its marker-bounded program (old
# lines 181-191), which consumers extract between the markers.
IFS= read -r -d '' P7_LEDGER_BLOCK <<'PINNED_BLOCK'
Always stamp `configureOffered` on every write as the sorted, de-duplicated union of the existing ledger and the question IDs offered this run; `--all` never removes ledger entries. Only questions actually put to the user this run are recorded as offered (the IDs from the `ID` column of the defaults table): a carried question, a conditional question whose detection condition failed, and question 12 are never recorded, and entries are never pruned. String entries of an existing `configureOffered` array are kept and non-strings are dropped. An empty result is still written as `[]`. Apply this fixed program last in the overlay order, with `--argjson offered '[<ids>]'` (the JSON array of offered IDs, `[]` when no question was asked — minus any ID removed above because its overlay fell back, or because question 11's enroll failed), then validate it like every other overlay (exit 0 and `jq -e 'type == "object"'`):

<!-- cenci:configure-offered-ledger:begin -->
```jq
.configureOffered = (
  ((.configureOffered // []) | if type == "array" then map(select(type == "string")) else [] end)
  + ($offered | map(select(type == "string")))
  | unique
)
```
<!-- cenci:configure-offered-ledger:end -->
PINNED_BLOCK
P7_LEDGER_BLOCK="${P7_LEDGER_BLOCK%$'\n'}"

# P8 (#1344): step 7's recovery branches, each pinned from the pre-move text
# (old lines 595, 601, 642 and 644-647).
IFS= read -r -d '' P8_NOTHING_STAGED_BLOCK <<'PINNED_BLOCK'
   If nothing is staged (a re-run that changed nothing), skip Commit/Push/PR entirely and report "No configuration changes — nothing to commit" instead of opening an empty PR. A stamp-only diff still opens a PR; "No configuration changes — nothing to commit" applies only when nothing is staged. For example, a run that offered a new question and recorded the decline in `configureOffered` changes `.cenci/config.json` and so is not empty.
PINNED_BLOCK
P8_NOTHING_STAGED_BLOCK="${P8_NOTHING_STAGED_BLOCK%$'\n'}"

IFS= read -r -d '' P8_PUSH_FAILURE_BLOCK <<'PINNED_BLOCK'
   If push fails due to sandbox/network/auth, show the exact command and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.
PINNED_BLOCK
P8_PUSH_FAILURE_BLOCK="${P8_PUSH_FAILURE_BLOCK%$'\n'}"

IFS= read -r -d '' P8_PR_RECOVERY_BLOCK <<'PINNED_BLOCK'
   If `gh pr create` fails because a PR for this branch already exists (re-entry after a prior turn already created it), recover the URL with `gh pr view chore/configure-<slug> --json url -q .url` and continue. For any other failure (auth, network, validation), show the exact error and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve it before continuing.
PINNED_BLOCK
P8_PR_RECOVERY_BLOCK="${P8_PR_RECOVERY_BLOCK%$'\n'}"

IFS= read -r -d '' P8_TEMP_REMOVAL_BLOCK <<'PINNED_BLOCK'
   Delete the PR-body temp file after a successful `gh pr create` (or a successful recovery via `gh pr view`):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md
   ```
PINNED_BLOCK
P8_TEMP_REMOVAL_BLOCK="${P8_TEMP_REMOVAL_BLOCK%$'\n'}"

# P24 (#1344): the board migration notes for a project that predates `In Review`,
# `Planned` or the reconciler-managed labels (old lines 677-682, 684-690 and
# 692-698). The first two move byte for byte. The third cites step 3c, which lives
# in another file now, so its pointer may name the file: it is pinned as the
# two segments around that pointer and the file name is pinned separately.
IFS= read -r -d '' P24_IN_REVIEW_NOTE <<'PINNED_BLOCK'
**Migration note for existing boards** (state this when re-configuring a project that predates
`In Review`): add an **`In Review`** column/label. Previously, tickets dropped straight into
`Implemented` the moment the PR was opened; now PR-open lands them in `In Review`, and
`/cenci:babysit` promotes them to `Implemented` when the PR actually merges. Existing tickets
that carry `Implemented` from before this change but whose PRs are still open can be relabeled to
`In Review` by hand; new work follows the split automatically.
PINNED_BLOCK
P24_IN_REVIEW_NOTE="${P24_IN_REVIEW_NOTE%$'\n'}"

IFS= read -r -d '' P24_PLANNED_NOTE <<'PINNED_BLOCK'
**Migration note for existing boards** (state this when re-configuring a project that predates
`Planned`): add a **`Planned`** column/label between `Refined` and `Working`. Previously, a
planning session applied `Working` at pipeline start and then stopped once the plan was saved —
so the board showed `Working` on a ticket nobody was actively working. Now a planning session
lands the ticket on `Planned` ("a plan exists on disk, ready to pick up"); the swap to
`Working` happens when the plan-file implementation run begins. New work follows this
automatically.
PINNED_BLOCK
P24_PLANNED_NOTE="${P24_PLANNED_NOTE%$'\n'}"

P24_RECONCILER_NOTE_HEAD='**Migration note for existing boards** (state this when re-configuring a project that predates the reconciler-managed labels): re-running `/cenci:configure` on an existing project creates the `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` labels retroactively via'
P24_RECONCILER_NOTE_TAIL='missing-only `gh label create` loop. No other action is needed — the reconciler already self-heals these labels itself via `EnsureLabels`, so a board that skips this step is not blocked; re-running configure just creates them proactively instead of waiting for the reconciler to hit the corresponding error state first.'
P24_RECONCILER_NOTE_FILE='`phases/guidance-settings.md`'
STUB_BOARD_NOTES='on a re-configuration run state the matching board migration note (see the migrations row of `### Step Map`)'
BOARD_NOTE_LEAD='**Migration note for existing boards**'

# P9 (#1344): `### Board lifecycle labels` is a pointer. docs/board-labels.md is
# the only lifecycle-label table; the orthogonal-marker restatements are gone from
# every configure file, and the trivial-ticket fact moved into that doc.
BOARD_LABELS_POINTER='`docs/board-labels.md`'
BOARD_LABELS_SUMMARY='Document the label set in the completion summary so the user can mirror it as columns on their board'
BOARD_LABELS_RETIRED=(
  '`Followup` is orthogonal to the linear lifecycle above'
  '`Browser`, `ui:visual-check`, and `automerge:ok` are likewise refine-applied orthogonal markers'
  '**Reconciler-managed labels**: `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` are not applied by refine or implement'
  '`Input Needed` is likewise orthogonal'
  'Lifecycle: `New → Refined → Planned → Working → In Review → Implemented`.'
  'A ticket judged trivial by implement'"'"'s Trivial-Ticket Triage still transits through `Planned`'
  'UI conventions — the component library, its browsable catalog'
)
BOARD_LABELS_TRIVIAL_FACT='same labels, no new label state'
BOARD_LABELS_BODY_MAX_LINES=8

# P11 (#1344): the complete `.cenci/config.json` examples and the field-by-field
# schema exist only in references/config-schema.md. Each anchor is a distinctive
# line of one example or one field entry, so it must occur exactly once across the
# configure markdown and be in the schema file.
SCHEMA_ANCHORS=(
  '"guidanceLocation": "AGENTS.md",'
  '"slug": "web-client",'
  '"gateOutputLines": 120'
  '"babysitInterval": "20m"'
  '"remindAfterDays": 30,'
  '`compactImplementation` — exactly `true` allows'
  '`cicd.platform` — `"github-actions"`'
  '`security.sensitivePaths` — an array of glob-pattern strings'
  '`automerge.maxChangedFiles` — required whenever an `automerge` block is present'
  '`docs.policy` — the exact, case-sensitive string `"minimal"`'
)
# The migration-removal list and the MCP/LSP inclusion rules close the schema, so
# every "migration-removal list below" pointer stays inside that one file.
SCHEMA_MIGRATION_REMOVAL_LIST='When migrating from an older config that has `ticketSystem`, `prSystem`, `ticketPrefix`, `adoOrg`, `adoProject`, `adoRepo`, `profile`, or `sandboxEnabled` fields, remove them during the merge.'
SCHEMA_MCP_INCLUSION='Only include servers in `mcpServers` that were presented as options (i.e., detected or always-available).'
SCHEMA_LSP_INCLUSION='Only include servers in `lspServers` that were detected and presented in question 6.'
SCHEMA_REMOVAL_POINTER='migration-removal list'
SCHEMA_REMOVAL_POINTER_MIN=7
SCHEMA_SANDBOX_POINTER='`phases/write-config.md`'

# The sandbox merge section moved from the schema prose into step 6's file
# (Decision: codex.md's "sandbox" field pointer goes to phases/write-config.md).
SANDBOX_MERGE_PINS=(
  'bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh" <path to existingConfig, or "-" for stdin>'
  'The script fails closed (exit 2) for several distinct reasons'
  'Every boolean flag is **required**'
  'Q9=No, 9b=Yes, 9c=No → `"sandbox": { "dind": true }` (no `enabled`/`azure` keys)'
  '> **Not the same as `.claude/settings.json`'"'"'s `sandbox.enabled`.**'
)

# P3 (#1344): `### Run Order` table. A row's first cell is "Question <token>" or
# "Step <token>", where <token> is the question's label in `### Question Defaults`
# (the text before its first "."; the whole label when it has no number). The third
# cell, "Selected", states which run modes select the row. The sequence is pinned
# here, never read back from the doc. Question 12 (the half-enrolled repair, which
# has no ID) may appear between 11 and 13 but is not required.
RUN_ORDER_QUESTION_TOKENS=('1' '2' '3' '5' '5c' 'Playwright CLI setup' '6' '7' '7b' '8' '9' '9b' '9c' '10' '11' '13' '14' '15' '16' '17' '18–20')
RUN_ORDER_STEP_TOKENS=('0 to 5c-bis' '5d and 5e' '5f' '6' '7')
# Question rows name the file that holds them; generation rows name the phase file
# they run from (steps 0 to 5c-bis reach three files; the first one is pinned).
RUN_ORDER_QUESTION_FILES=(
  "1|${PROJECT_FILE}" "2|${PROJECT_FILE}" "3|${PROJECT_FILE}"
  "5|${MCP_FILE}" "5c|${MCP_FILE}" "Playwright CLI setup|${MCP_FILE}" "6|${MCP_FILE}"
  "7|${RUNTIME_FILE}" "7b|${RUNTIME_FILE}" "8|${RUNTIME_FILE}" "9|${RUNTIME_FILE}" "9b|${RUNTIME_FILE}" "9c|${RUNTIME_FILE}"
  "10|${LAZY_FILE}" "11|${FLEET_FILE}"
  "13|${AUTONOMY_FILE}" "14|${AUTONOMY_FILE}" "15|${AUTONOMY_FILE}" "16|${AUTONOMY_FILE}" "17|${AUTONOMY_FILE}"
  "18–20|${FLEET_FILE}"
)
RUN_ORDER_STEP_FILES=(
  "0 to 5c-bis|${GUIDANCE_FILE}" "5d and 5e|${CI_FILE}" "5f|${LAZY_FILE}"
  "6|${WRITE_CONFIG_FILE}" "7|${COMMIT_PR_FILE}"
)
# A question row says when it is asked; a generation row says Every run and never
# mentions delta.ask (a delta run still selects it).
RUN_ORDER_QUESTION_PARTS=('first run' '`--all`' '`full`' '`delta.ask`')
RUN_ORDER_STEP_PART='Every run'
RUN_ORDER_FORBIDDEN_IN_STEP='delta.ask'
DR_ZERO_ASK='When `delta.ask` is empty, ask no questions and still run every generation step.'
ID_FLOOR=20
ROUTER_MAX_LINES=300

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
require_doc guidance_phase "skills/configure/${GUIDANCE_FILE}" || true
require_doc settings_template "skills/configure/${SETTINGS_TEMPLATE_FILE}" || true
require_doc ci_phase "skills/configure/${CI_FILE}" || true
require_doc write_config_phase "skills/configure/${WRITE_CONFIG_FILE}" || true
require_doc schema_ref "skills/configure/${SCHEMA_FILE}" || true
require_doc commit_pr_phase "skills/configure/${COMMIT_PR_FILE}" || true
require_doc board_labels_doc "docs/board-labels.md" || true
require_doc readme "README.md" || true

# every_configure_md -- SKILL.md plus every phases/*.md and references/*.md on
# disk (not just the five named above), so a stray sixth file is still counted.
configure_all="${skill}"
shopt -s nullglob
configure_disk_files=("${CONFIGURE_DIR}"/phases/*.md "${CONFIGURE_DIR}"/references/*.md)
shopt -u nullglob
for _part in "${configure_disk_files[@]}"; do
  if ! _part_content="$(cat "${_part}")"; then
    fail "skills/configure/**: ${_part#"${FLOW_DIR}/"}: not found/unreadable"
    continue
  fi
  configure_all+=$'\n'"${_part_content}"
done

SKILL_WS="$(normalize_ws "${skill}")"
CONFIGURE_ALL_WS="$(normalize_ws "${configure_all}")"

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
    "${GUIDANCE_FILE}") printf '%s' "${guidance_phase}" ;;
    "${SETTINGS_TEMPLATE_FILE}") printf '%s' "${settings_template}" ;;
    "${CI_FILE}") printf '%s' "${ci_phase}" ;;
    "${WRITE_CONFIG_FILE}") printf '%s' "${write_config_phase}" ;;
    "${SCHEMA_FILE}") printf '%s' "${schema_ref}" ;;
    "${COMMIT_PR_FILE}") printf '%s' "${commit_pr_phase}" ;;
    SKILL.md) printf '%s' "${skill}" ;;
  esac
}

# extract_h2_section_raw <content> <exact-heading-line> -- lines strictly between a
# `## ` heading and the next `## ` heading (or the end of the content). A heading-
# shaped line inside a fenced code block is never a boundary. Pure.
extract_h2_section_raw() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    /^```/ { infence = !infence }
    !infence && $0 == heading { found=1; next }
    found && !infence && index($0, "## ") == 1 { exit }
    found { print }
  ' <<<"${content}"
}

# table_cell <row> <n> -- cell n (1-based, first cell = 1) of a Markdown table row,
# trimmed. Cells are split on "|", so no cell of the tables read here may contain a
# pipe character. Pure.
table_cell() {
  local row="$1" n="$2"
  awk -F'|' -v n="${n}" '{ c = $(n + 1); gsub(/^[[:space:]]+|[[:space:]]+$/, "", c); print c }' <<<"${row}"
}

# condition_cell <step-map-row> -- the Condition cell (second cell). Pure.
condition_cell() {
  table_cell "$1" 2
}

# run_order_row <run-order-body> <first-cell> -- the Run Order table row whose
# first cell is exactly <first-cell> (e.g. "Question 5c" or "Step 6"). Pure.
run_order_row() {
  local body="$1" first="$2"
  grep -F -- "| ${first} |" <<<"${body}" | head -n 1
}

# defaults_label_for_id <question-defaults-body> <id> -- the first cell of the
# `### Question Defaults` row whose ID cell lists `<id>` (backtick-delimited, so
# `automerge` never matches `automerge.enabled`). Pure.
defaults_label_for_id() {
  local body="$1" id="$2"
  awk -F'|' -v id="\`${id}\`" '
    index($0, "|") == 1 && index($3, id) > 0 { c = $2; gsub(/^[[:space:]]+|[[:space:]]+$/, "", c); print c; exit }
  ' <<<"${body}"
}

# label_token <label> -- the Run Order token for a Question Defaults label: the text
# before the first "." when the label starts with a digit, else the whole label.
label_token() {
  local label="$1"
  if [[ "${label}" =~ ^[0-9] ]]; then
    printf '%s' "${label%%.*}"
  else
    printf '%s' "${label}"
  fi
}

# pinned_file_for <token> <entry>... -- the file pinned for <token> in a
# "<token>|<file>" list. Pure.
pinned_file_for() {
  local token="$1" entry
  shift
  for entry in "$@"; do
    if [[ "${entry%%|*}" == "${token}" ]]; then
      printf '%s' "${entry#*|}"
      return
    fi
  done
}

step_map_body=""
question_defaults_body=""
run_order_body=""
if [[ -n "${skill}" ]]; then
  step_map_body="$(extract_section_body_raw "${skill}" "${STEP_MAP_HEADING}")"
  question_defaults_body="$(extract_section_body_raw "${skill}" "${QUESTION_DEFAULTS_HEADING}")"
  run_order_body="$(extract_section_body_raw "${skill}" "${RUN_ORDER_HEADING}")"
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
  # example, fleet-enrollment and autonomy-settings rows (#1342), then the three
  # generation rows read after the questions: guidance-settings (steps 0 to
  # 5c-bis), settings-template (the base deny list step 4 reads) and
  # ci-dockerfile (steps 5d and 5e, after guidance-settings) (#1343), then
  # write-config (step 6), the schema it reads, and commit-pr (step 7) (#1344).
  local prev=-1 file at
  for file in "${MIGRATIONS_FILE}" "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}" "${LAZY_FILE}" "${LAZY_EXAMPLE_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}" "${GUIDANCE_FILE}" "${SETTINGS_TEMPLATE_FILE}" "${CI_FILE}" "${WRITE_CONFIG_FILE}" "${SCHEMA_FILE}" "${COMMIT_PR_FILE}"; do
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
  # #1344: the migrations row names both section conditions (the legacy migration
  # and the board migration notes) instead of one file-level condition.
  for part in "${MIGRATIONS_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${MIGRATIONS_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${MIGRATIONS_FILE}\` row names both section conditions"
  done
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_EVERY_PHASE}" "${STEP_MAP_HEADING}: retired \"every phase file is read\" intro"
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_INLINE_TAIL}" "${STEP_MAP_HEADING}: retired \"question 10 onward stays inline\" tail"

  # #1343: the three generation rows are read on every run (steps 5d and 5e each
  # decide internally whether to skip), each names its own position, and the
  # intro no longer claims generation steps 0 to 7 all stay inline.
  for file in "${GUIDANCE_FILE}" "${SETTINGS_TEMPLATE_FILE}" "${CI_FILE}" "${WRITE_CONFIG_FILE}" "${SCHEMA_FILE}" "${COMMIT_PR_FILE}"; do
    row="$(row_for "${step_map_body}" "${file}")"
    if [[ -z "${row}" ]]; then
      fail "${STEP_MAP_HEADING}: no row for generation file \`${file}\`"
    else
      assert_contains "${row}" "${EVERY_RUN}" "${STEP_MAP_HEADING}: \`${file}\` row states it is read on every run"
    fi
  done
  # Case-insensitive: a Why cell may open with "Step 6:" and a Condition cell with
  # "after ...", so the row and the part are both lower-cased before the match.
  local row_lc
  row_lc="$(row_for "${step_map_body}" "${WRITE_CONFIG_FILE}")"
  for part in "${WRITE_CONFIG_ROW_PARTS[@]}"; do
    assert_contains_ws "${row_lc,,}" "${part,,}" "${STEP_MAP_HEADING}: \`${WRITE_CONFIG_FILE}\` row names its position"
  done
  row_lc="$(row_for "${step_map_body}" "${SCHEMA_FILE}")"
  for part in "${SCHEMA_ROW_PARTS[@]}"; do
    assert_contains_ws "${row_lc,,}" "${part,,}" "${STEP_MAP_HEADING}: \`${SCHEMA_FILE}\` row names its position"
  done
  row_lc="$(row_for "${step_map_body}" "${COMMIT_PR_FILE}")"
  for part in "${COMMIT_PR_ROW_PARTS[@]}"; do
    assert_contains_ws "${row_lc,,}" "${part,,}" "${STEP_MAP_HEADING}: \`${COMMIT_PR_FILE}\` row names its position"
  done
  for part in "${GUIDANCE_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${GUIDANCE_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${GUIDANCE_FILE}\` row names its position"
  done
  for part in "${SETTINGS_TEMPLATE_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${SETTINGS_TEMPLATE_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${SETTINGS_TEMPLATE_FILE}\` row names its position"
  done
  for part in "${CI_ROW_PARTS[@]}"; do
    assert_contains_ws "$(row_for "${step_map_body}" "${CI_FILE}")" "${part}" "${STEP_MAP_HEADING}: \`${CI_FILE}\` row names its position"
  done
  local intro table_header='| File | Condition | Why |'
  intro="${step_map_body%%"${table_header}"*}"
  for part in "${STEP_MAP_INTRO_PARTS[@]}"; do
    assert_contains_ws "${intro,,}" "${part,,}" "${STEP_MAP_HEADING}: intro names where each generation step range runs"
  done
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_GENERATION_INLINE}" "${STEP_MAP_HEADING}: retired \"generation steps 0 to 7 stay inline\" intro"
  assert_absent_paired_ws "${step_map_body}" '| File | Condition | Why |' "${OLD_STEP_MAP_STEPS_6_7_INLINE}" "${STEP_MAP_HEADING}: retired \"only steps 6 and 7 stay inline\" intro (#1344)"

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
  [[ "${#configure_disk_files[@]}" -ge 15 ]] || fail "step map: expected at least 15 phases/references files on disk, found ${#configure_disk_files[@]}"
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

  # #1344: the file holds two sections, each read under its own condition. The
  # legacy-migration prose and its "never read on a .cenci/config.json-only run"
  # condition are pinned inside `## Legacy config migration` (a section-level pin,
  # tighter than the former file-level sentence); the board notes are pinned in P24.
  local legacy_section
  legacy_section="$(extract_h2_section_raw "${migrations}" "${MIGRATIONS_LEGACY_HEADING}")"
  [[ "$(count_exact_lines "${migrations}" "${MIGRATIONS_LEGACY_HEADING}")" -eq 1 ]] || fail "P19: ${MIGRATIONS_FILE} must hold exactly one [${MIGRATIONS_LEGACY_HEADING}] heading"
  [[ -n "${legacy_section}" ]] || fail "P19: ${MIGRATIONS_FILE} [${MIGRATIONS_LEGACY_HEADING}] section missing or empty"
  assert_contains_ws "${legacy_section}" "${LEGACY_CONDITION}" "${MIGRATIONS_FILE}: the legacy section is conditioned on .claude/config.json existing"
  assert_contains_ws "${legacy_section}" "${MIGRATIONS_LEGACY_NEVER_READ}" "${MIGRATIONS_FILE}: the legacy section states a .cenci/config.json-only run never reads it"
  assert_contains_ws "${legacy_section}" "${MIGRATION_PRESERVE}" "${MIGRATIONS_FILE}: unknown-key preservation"
  assert_contains_ws "${legacy_section}" "${MIGRATION_MERGE}" "${MIGRATIONS_FILE}: the migrationBase merge"
  assert_contains_ws "${legacy_section}" "${MIGRATION_FENCE_LINE}" "${MIGRATIONS_FILE}: the migrate-project-core.sh preview/--apply fence"
  assert_contains_ws "${legacy_section}" "${MIGRATION_NEVER_REWRITE}" "${MIGRATIONS_FILE}: never rewrite .claude/config.json"

  # The migration prose is gone from the router (paired with the step-map row
  # that now points at it, so the absence cannot pass vacuously).
  local migrations_row
  migrations_row="$(row_for "${step_map_body}" "${MIGRATIONS_FILE}")"
  assert_contains "${migrations_row}" "${LEGACY_CONDITION}" "step map: the migrations row is conditioned on .claude/config.json existing"
  assert_contains_ws "${migrations_row}" "${MIGRATIONS_ROW_NEVER_READ}" "step map: the migrations row states a .cenci/config.json-only run never reads the legacy section"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_ROW_NEVER_READ}" "${MIGRATION_PRESERVE}" "SKILL.md no longer carries the unknown-key preservation prose"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_ROW_NEVER_READ}" "${MIGRATION_MERGE}" "SKILL.md no longer carries the migrationBase merge prose"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_ROW_NEVER_READ}" "${MIGRATION_FENCE_LINE}" "SKILL.md no longer carries the migrate-project-core.sh fence"
  assert_absent_paired_ws "${skill}" "${MIGRATIONS_ROW_NEVER_READ}" "${MIGRATION_NEVER_REWRITE}" "SKILL.md no longer carries the never-rewrite rule"

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
  for file in "${LAZY_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}" "${GUIDANCE_FILE}" "${SETTINGS_TEMPLATE_FILE}" "${CI_FILE}" "${WRITE_CONFIG_FILE}" "${SCHEMA_FILE}" "${COMMIT_PR_FILE}"; do
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

# #1343: each moved generation step's marker line occurs exactly once across the
# configure markdown, in its new file, and no longer in SKILL.md. The zero count
# in SKILL.md is paired with the stubs that must remain there (INLINE_MARKERS in
# case_moved_headings_unique), so an emptied SKILL.md cannot satisfy it; here the
# same pairing is made explicit per marker.
case_moved_step_markers_unique() {
  local entry file marker n in_file
  for entry in "${MOVED_STEP_MARKERS[@]}"; do
    file="${entry%%|*}"
    marker="${entry#*|}"
    n="$(count_occurrences "${configure_all}" "${marker}")"
    [[ "${n}" -eq 1 ]] || fail "step marker [${marker}] must occur exactly once across SKILL.md, phases/*.md and references/*.md, found ${n}"
    in_file="$(count_occurrences "$(file_content "${file}")" "${marker}")"
    [[ "${in_file}" -eq 1 ]] || fail "step marker [${marker}] must be in ${file}, found ${in_file} there"
    assert_absent_paired_ws "${skill}" "${STUB_GUIDANCE}" "${marker}" "SKILL.md no longer carries step marker [${marker}]"
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

  # Generation steps that consume moved tables name the file holding them. Steps
  # 1b and 5e moved to their own phase files in #1343, so the pointers are read
  # there; SKILL.md keeps only the stubs.
  assert_contains_ws "${guidance_phase}" 'LSP Server Catalog` in `phases/mcp-lsp-ui-questions.md`' "${GUIDANCE_FILE} step 1b names the file holding the LSP Server Catalog"
  assert_contains_ws "${ci_phase}" "${MONOREPO_ANCHOR}" "${CI_FILE} step 5e Monorepo sentence keeps its anchor prefix"
  local monorepo
  monorepo="$(grep -F "${MONOREPO_ANCHOR}" <<<"${ci_phase}" | head -n 1)"
  assert_contains_ws "${monorepo}" '`phases/runtime-ci-sandbox-questions.md`' "${CI_FILE} step 5e Monorepo sentence names the file holding the Stack-to-fragment mapping table"
  assert_absent_paired_ws "${ci_phase}" "${MONOREPO_ANCHOR}" 'mapping table above' "${CI_FILE} step 5e retired positional mapping-table pointer"
  assert_absent_paired_ws "${ci_phase}" "${MONOREPO_ANCHOR}" 'mapping table under question 9,' "${CI_FILE} step 5e retired positional question 9 pointer"
  assert_absent_paired_ws "${skill}" "${STUB_CI}" "${MONOREPO_ANCHOR}" "SKILL.md no longer carries the step 5e Monorepo sentence"
  assert_absent_paired_ws "${skill}" "${STUB_GUIDANCE}" 'LSP Server Catalog` in `phases/mcp-lsp-ui-questions.md`' "SKILL.md no longer carries the step 1b LSP Server Catalog pointer"

  # --- #1343: pointers that crossed files when steps 0 to 5e moved --------------
  # Step 3c's reconciler pointer lives in a phase file. `### Board lifecycle
  # labels` shrank to a pointer in #1344, so step 3c now names the reconciler
  # bullet in the plugin's docs/board-labels.md instead of SKILL.md.
  assert_contains_ws "${guidance_phase}" "${STEP3C_BOARD_POINTER}" "${GUIDANCE_FILE} step 3c names the reconciler bullet in docs/board-labels.md"
  assert_absent_paired_ws "${configure_all}" "${STEP3C_BOARD_POINTER}" "${OLD_STEP3C_BOARD_POINTER}" "configure markdown retired positional Board lifecycle labels pointer"
  assert_absent_paired_ws "${configure_all}" "${STEP3C_BOARD_POINTER}" "${OLD_STEP3C_SKILL_POINTER}" "configure markdown retired pointer at SKILL.md for Board lifecycle labels (#1344)"
  # Step 6 moved to phases/write-config.md (#1344) and points at the steps that
  # left it; the sandbox merge section's pointers travel with it.
  assert_contains_ws "${write_config_phase}" "${STEP6_5E_POINTER}" "${WRITE_CONFIG_FILE} names the file holding step 5e"
  assert_absent_paired_ws "${configure_all}" "${STEP6_5E_POINTER}" "${OLD_STEP6_5E_POINTER}" "configure markdown retired positional step 5e pointer"
  assert_contains_ws "${write_config_phase}" "${STEP6_STEP4_POINTER}" "${WRITE_CONFIG_FILE} names the file holding step 4"
  assert_absent_paired_ws "${configure_all}" "${STEP6_STEP4_POINTER}" "${OLD_STEP6_STEP4_POINTER}" "configure markdown retired positional Step 4 pointer"
  # Pointers at step 6, step 7 and the schema that crossed files name the file.
  assert_contains_ws "${runtime_phase}" "${RUNTIME_DIND_POINTER}" "${RUNTIME_FILE}: the sandbox.dind schema note pointer names ${WRITE_CONFIG_FILE}"
  assert_contains_ws "${runtime_phase}" "${RUNTIME_AZURE_POINTER}" "${RUNTIME_FILE}: the sandbox.azure schema note pointer names ${WRITE_CONFIG_FILE}"
  assert_absent_paired_ws "${configure_all}" "${RUNTIME_DIND_POINTER}" "${OLD_RUNTIME_DIND_POINTER}" "configure markdown retired sandbox.dind schema note pointer at SKILL.md"
  assert_absent_paired_ws "${configure_all}" "${RUNTIME_AZURE_POINTER}" "${OLD_RUNTIME_AZURE_POINTER}" "configure markdown retired sandbox.azure schema note pointer at SKILL.md"
  assert_absent_paired_ws "${configure_all}" "${AUTONOMY_STEP6_POINTER}" "${OLD_AUTONOMY_STEP6_POINTER}" "configure markdown retired step 6 pointer at SKILL.md"
  assert_contains_ws "${autonomy_phase}" 'step 7 in `phases/commit-pr.md`' "${AUTONOMY_FILE}: the step 7 pointer names ${COMMIT_PR_FILE}"
  assert_absent_paired_ws "${configure_all}" 'step 7 in `phases/commit-pr.md`' 'step 7 in `SKILL.md`' "configure markdown retired step 7 pointer at SKILL.md"
  assert_contains_ws "${schema_ref}" "${SCHEMA_RESOLVER_POINTER}" "${SCHEMA_FILE}: the resolver pointer names ${DETECT_FILE}"
  # The router's own early prose names the file for every step 6 / step 7 mention:
  # a line that cites step 6 or step 7 by number names the file holding it (the
  # stubs, the Run Order rows and the Step Map rows do). At least three such lines
  # must exist so the scan cannot pass vacuously.
  local step_lines step_line step_hits=0
  step_lines="$(grep -E '(^|[^0-9A-Za-z])[Ss]teps? [67]($|[^0-9A-Za-z])' <<<"${skill}")"
  if [[ -n "${step_lines}" ]]; then
    while IFS= read -r step_line; do
      [[ -n "${step_line}" ]] || continue
      step_hits=$((step_hits+1))
      if [[ "${step_line}" =~ (^|[^0-9A-Za-z])[Ss]teps?\ 6($|[^0-9A-Za-z]) && "${step_line}" != *'`phases/write-config.md`'* ]]; then
        fail "SKILL.md: a line cites step 6 without naming ${WRITE_CONFIG_FILE}: ${step_line:0:160}"
      fi
      if [[ "${step_line}" =~ (^|[^0-9A-Za-z])[Ss]teps?\ 7($|[^0-9A-Za-z]) && "${step_line}" != *'`phases/commit-pr.md`'* ]]; then
        fail "SKILL.md: a line cites step 7 without naming ${COMMIT_PR_FILE}: ${step_line:0:160}"
      fi
    done <<<"${step_lines}"
  fi
  [[ "${step_hits}" -ge 3 ]] || fail "SKILL.md: expected at least 3 lines citing step 6 or step 7 (the stubs and the pointers), found ${step_hits}"
  # The phase files that cite generation steps name where those steps now run.
  assert_contains_ws "${detect_phase}" "${DETECT_STEP1_POINTER}" "${DETECT_FILE}: the diff-approval gate pointer names ${GUIDANCE_FILE}"
  assert_absent_paired_ws "${detect_phase}" "${DETECT_STEP1_POINTER}" "${OLD_DETECT_STEP1_POINTER}" "${DETECT_FILE}: retired bare step 1 pointer"
  assert_contains_ws "${detect_phase}" "${DETECT_5E_POINTER}" "${DETECT_FILE}: the Dockerfile refresh pointer names ${CI_FILE}"
  assert_absent_paired_ws "${detect_phase}" "${DETECT_5E_POINTER}" "${OLD_DETECT_5E_POINTER}" "${DETECT_FILE}: retired bare step 5e pointer"
  local mcp_head
  mcp_head="$(head -n 4 <<<"${mcp_phase}")"
  assert_contains_ws "${mcp_head}" "${MCP_OPEN_POINTER}" "${MCP_FILE}: the opening line names ${GUIDANCE_FILE} for step 1b and the MCP configuration"
  assert_absent_paired_ws "${mcp_head}" "${MCP_OPEN_POINTER}" "${OLD_MCP_OPEN_POINTER}" "${MCP_FILE}: retired unnamed generation-steps pointer"
  assert_contains_ws "${runtime_phase}" "${RUNTIME_5C_POINTER}" "${RUNTIME_FILE}: the question 7 pointer names ${GUIDANCE_FILE} for step 5c"
  assert_absent_paired_ws "${runtime_phase}" "${RUNTIME_5C_POINTER}" "${OLD_RUNTIME_5C_POINTER}" "${RUNTIME_FILE}: retired bare step 5c pointer"
  assert_contains_ws "${runtime_phase}" "${RUNTIME_5C_BIS_POINTER}" "${RUNTIME_FILE}: the question 7b pointer names ${GUIDANCE_FILE} for step 5c-bis"
  assert_absent_paired_ws "${runtime_phase}" "${RUNTIME_5C_BIS_POINTER}" "${OLD_RUNTIME_5C_BIS_POINTER}" "${RUNTIME_FILE}: retired bare step 5c-bis pointer"
  assert_contains_ws "${runtime_phase}" "${RUNTIME_5E_POINTER}" "${RUNTIME_FILE}: the sync obligation pointer names ${CI_FILE} for step 5e"
  assert_absent_paired_ws "${runtime_phase}" "${RUNTIME_5E_POINTER}" "${OLD_RUNTIME_5E_POINTER}" "${RUNTIME_FILE}: retired bare step 5e pointer"
  local runtime_head
  runtime_head="$(head -n 4 <<<"${runtime_phase}")"
  assert_contains_ws "${runtime_head}" "${RUNTIME_OPEN_POINTER}" "${RUNTIME_FILE}: the opening line names ${CI_FILE} for steps 5d and 5e"
  assert_absent_paired_ws "${runtime_head}" "${RUNTIME_OPEN_POINTER}" "${OLD_RUNTIME_OPEN_POINTER}" "${RUNTIME_FILE}: retired unnamed steps 5d and 5e pointer"

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
  # Step 7 (the final report) moved to phases/commit-pr.md, step 6 (the wiring)
  # to phases/write-config.md and the field notes to references/config-schema.md
  # (#1344): the pointers are read there, and every retired positional form is
  # scanned across SKILL.md plus every phases/*.md and references/*.md.
  assert_contains_ws "${commit_pr_phase}" "${FLEET_AUTONOMY_POINTER}" "${COMMIT_PR_FILE} step 7 names the file holding the fleet Next step block"
  assert_contains_ws "${write_config_phase}" "question 13 in \`${AUTONOMY_FILE}\` was asked" "${WRITE_CONFIG_FILE} step 6 wiring names ${AUTONOMY_FILE} for question 13"
  assert_contains_ws "${write_config_phase}" "question 14 in \`${AUTONOMY_FILE}\` was asked" "${WRITE_CONFIG_FILE} step 6 wiring names ${AUTONOMY_FILE} for question 14"
  assert_contains_ws "${write_config_phase}" "question 15 in \`${AUTONOMY_FILE}\` was asked" "${WRITE_CONFIG_FILE} step 6 wiring names ${AUTONOMY_FILE} for question 15"
  assert_contains_ws "${schema_ref}" "\`### Autonomy Settings\` question 13 in \`${AUTONOMY_FILE}\`" "${SCHEMA_FILE} planning field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${schema_ref}" "\`### Autonomy Settings\` question 14 in \`${AUTONOMY_FILE}\`" "${SCHEMA_FILE} automerge field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${schema_ref}" "\`### Autonomy Settings\` question 15 in \`${AUTONOMY_FILE}\`" "${SCHEMA_FILE} refinement field note names ${AUTONOMY_FILE}"
  assert_contains_ws "${schema_ref}" 'derivation tables in `### Board Config (lazyboards)` in `phases/lazyboards.md`' "${SCHEMA_FILE} names the file holding the lazyboards derivation tables"
  assert_contains_ws "${write_config_phase}" 'fallback as question 13/14/15 in `phases/autonomy-settings.md`' "${WRITE_CONFIG_FILE} step 6 fleet-leaf overlay names ${AUTONOMY_FILE} for the question 13/14/15 validation"
  assert_absent_paired_ws "${configure_all}" 'fallback as question 13/14/15 in `phases/autonomy-settings.md`' 'fallback as question 13/14/15 below' "configure markdown retired positional question 13/14/15 validation pointer"
  for site in "${OLD_SKILL_AUTONOMY_POINTERS[@]}"; do
    assert_absent_paired_ws "${configure_all}" "${AUTONOMY_POINTER}" "${site}" "configure markdown retired positional pointer [${site}]"
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
  assert_contains_ws "${autonomy_phase}" "${AUTONOMY_STEP6_POINTER}" "${AUTONOMY_FILE}: the intro names ${WRITE_CONFIG_FILE} for the step 6 write (#1344)"
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
  [[ "${#configure_disk_files[@]}" -ge 15 ]] || fail "allowed-tools guard is vacuous: found ${#configure_disk_files[@]} phases/references files (expected at least 15)"
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
  for file in "${DETECT_FILE}" "${PROJECT_FILE}" "${MCP_FILE}" "${RUNTIME_FILE}" "${LAZY_FILE}" "${LAZY_EXAMPLE_FILE}" "${FLEET_FILE}" "${AUTONOMY_FILE}" "${MIGRATIONS_FILE}" "${GUIDANCE_FILE}" "${SETTINGS_TEMPLATE_FILE}" "${CI_FILE}" "${WRITE_CONFIG_FILE}" "${SCHEMA_FILE}" "${COMMIT_PR_FILE}"; do
    assert_contains "${row}" "${file}" "README.md workflow-deps row for configure lists ${file}"
  done
}

# count_block_occurrences <content> <block> -- occurrences of a multi-line
# <block> in <content>, via literal pattern removal (a quoted pattern is a
# literal in bash). Pure; prints 0 for an empty <block> (guarded by the caller).
count_block_occurrences() {
  local content="$1" block="$2" stripped
  [[ -n "${block}" ]] || { printf '%s' 0; return; }
  stripped="${content//"${block}"/}"
  printf '%s' $(( (${#content} - ${#stripped}) / ${#block} ))
}

# check_verbatim_block <label> <pinned-block> <new-file> <stub> -- the pinned
# block occurs exactly once across the configure markdown, exactly once in its
# new file, and zero times in SKILL.md. The SKILL.md absence is paired with the
# stub that must remain there, so an emptied SKILL.md cannot satisfy it.
check_verbatim_block() {
  local label="$1" pinned="$2" new_file="$3" stub="$4" n_all n_file n_skill
  if [[ -z "$(file_content "${new_file}")" ]]; then
    fail "${label}: ${new_file} missing or empty, cannot verify the moved block"
    return
  fi
  n_all="$(count_block_occurrences "${configure_all}" "${pinned}")"
  [[ "${n_all}" -eq 1 ]] || fail "${label}: the pinned block must occur exactly once across SKILL.md, phases/*.md and references/*.md, found ${n_all}"
  n_file="$(count_block_occurrences "$(file_content "${new_file}")" "${pinned}")"
  [[ "${n_file}" -eq 1 ]] || fail "${label}: the pinned block must be byte-identical to the pre-move SKILL.md text in ${new_file}, found ${n_file} occurrence(s) there"
  assert_contains "${skill}" "${stub}" "${label}: SKILL.md keeps the stub that replaces the block"
  n_skill="$(count_block_occurrences "${skill}" "${pinned}")"
  [[ "${n_skill}" -eq 0 ]] || fail "${label}: the pinned block must no longer be in SKILL.md, found ${n_skill}"
}

# Non-vacuity self-test for the block counter the two byte pins rely on: it must
# count two copies as 2, one as 1, and a one-byte-different copy as 0 (so a
# single changed byte in a moved block is caught, not counted).
case_block_counter_self_test() {
  local block=$'first line\n   second line' got
  got="$(count_block_occurrences "head
${block}
mid
${block}
tail" "${block}")"
  [[ "${got}" -eq 2 ]] || fail "block counter self-test: expected 2 copies, got ${got}"
  got="$(count_block_occurrences "head
${block}
tail" "${block}")"
  [[ "${got}" -eq 1 ]] || fail "block counter self-test: expected 1 copy, got ${got}"
  got="$(count_block_occurrences "head
${block/second/secnd}
tail" "${block}")"
  [[ "${got}" -eq 0 ]] || fail "block counter self-test: a one-byte-changed copy was counted (got ${got})"
}

# P30 (#1343): step 1's AGENTS.md/CLAUDE.md generation and its substantive-
# guidance diff-approval gate move word for word into phases/guidance-settings.md.
case_P30_step1_verbatim() {
  check_verbatim_block "P30 step 1" "${P30_STEP1_BLOCK}" "${GUIDANCE_FILE}" "${STUB_GUIDANCE}"
}

# P6 (#1343): step 5e's managed-block rules (both markers, no markers, malformed
# markers; delta versus --all/full) move word for word into phases/ci-dockerfile.md.
case_P6_dockerfile_verbatim() {
  check_verbatim_block "P6 step 5e" "${P6_MERGE_SAFE_BLOCK}" "${CI_FILE}" "${STUB_CI}"
}

# =============================================================================
# #1344 (4/4): step 6, the config schema, step 7 and the board notes move out;
# the router gains `### Run Order`.
# =============================================================================

# P1 (#1344): the router stays at 300 lines or fewer and keeps no step 6 or step 7
# body inline. The absences are paired with the two stubs that must remain.
case_P1_router_size() {
  [[ -n "${skill}" ]] || { fail "P1: SKILL.md missing or empty"; return; }
  local lines marker
  lines="$(wc -l < "${CONFIGURE_DIR}/SKILL.md" | tr -d '[:space:]')"
  [[ "${lines}" -le "${ROUTER_MAX_LINES}" ]] || fail "P1: SKILL.md is ${lines} lines; the router must stay at ${ROUTER_MAX_LINES} lines or fewer"
  assert_contains "${skill}" "${STUB_6}" "P1: SKILL.md keeps the step 6 stub"
  assert_contains "${skill}" "${STUB_7}" "P1: SKILL.md keeps the step 7 stub"
  for marker in \
    '**Autonomy Settings wiring.**' \
    '**Independent refinement leaves wiring.**' \
    '**Overlay order and validation.**' \
    '**Sandbox flags are derived per question ID.**' \
    '**Always stamp `configVersion`**' \
    'Read `<worktree-path>/docs/git-workflow.md`' \
    '## Carried through (delta run)'; do
    assert_absent_paired_ws "${skill}" "${STUB_7}" "${marker}" "P1: SKILL.md carries no step 6 or step 7 body [${marker}]"
  done
}

# condition_cell and table_cell are pure extractors; this self-test proves the
# P2 check below sees an empty Condition cell (non-vacuity, gotchas rule 29).
case_P2_condition_extractor_self_test() {
  local ok_row empty_row blank_row
  ok_row='| `phases/a.md` | Every run, after the questions. | Why a. |'
  empty_row='| `phases/b.md` |  | Why b. |'
  blank_row='| `phases/c.md` | | Why c. |'
  [[ "$(condition_cell "${ok_row}")" == "Every run, after the questions." ]] || fail "P2 self-test: the extractor lost a populated Condition cell"
  [[ -z "$(condition_cell "${empty_row}")" ]] || fail "P2 self-test: the extractor invented a Condition cell for an empty one"
  [[ -z "$(condition_cell "${blank_row}")" ]] || fail "P2 self-test: the extractor invented a Condition cell for a blank one"
  [[ "$(table_cell "${ok_row}" 3)" == "Why a." ]] || fail "P2 self-test: the extractor lost the third cell"
}

# P2 (#1344): SKILL.md names every phases/*.md and references/*.md file of the
# skill together with the condition under which it is read. The file list comes
# from the disk, not from a constant, so a new file with no row (or an empty
# Condition cell) fails here.
case_P2_every_file_named_with_condition() {
  [[ -n "${step_map_body}" ]] || { fail "P2: SKILL.md ${STEP_MAP_HEADING} section missing or empty"; return; }
  [[ "${#configure_disk_files[@]}" -ge 15 ]] || fail "P2: expected at least 15 phases/references files on disk, found ${#configure_disk_files[@]}"
  local disk rel row cond
  for disk in "${configure_disk_files[@]}"; do
    rel="${disk#"${CONFIGURE_DIR}/"}"
    row="$(row_for "${step_map_body}" "${rel}")"
    if [[ -z "${row}" ]]; then
      fail "P2: \`${rel}\` is not named in a ${STEP_MAP_HEADING} table row"
      continue
    fi
    cond="$(condition_cell "${row}")"
    if [[ -z "${cond}" ]]; then
      fail "P2: the ${STEP_MAP_HEADING} row for \`${rel}\` has an empty Condition cell"
      continue
    fi
    [[ "${cond}" == *"${EVERY_RUN}"* || "${cond}" == *"when"* ]] || fail "P2: the Condition cell for \`${rel}\` states neither \"${EVERY_RUN}\" nor a \"when\" condition: ${cond:0:120}"
  done
}

# run_resolver <config-json> [--all] -- the shipped resolver's single JSON line for
# a config passed on stdin. Pure.
run_resolver() {
  local cfg="$1"
  shift
  printf '%s' "${cfg}" | bash "${CONFIGURE_DIR}/scripts/resolve-delta.sh" - "$@" 2>/dev/null
}

# check_generation_rows <label> -- every generation row of `### Run Order` is
# selected on every run and never conditioned on delta.ask. Calls fail(), so it is
# never used inside $(...).
check_generation_rows() {
  local label="$1" token row selected
  for token in "${RUN_ORDER_STEP_TOKENS[@]}"; do
    row="$(run_order_row "${run_order_body}" "Step ${token}")"
    if [[ -z "${row}" ]]; then
      fail "${label}: ${RUN_ORDER_HEADING} has no row for Step ${token}"
      continue
    fi
    selected="$(table_cell "${row}" 3)"
    assert_contains "${selected}" "${RUN_ORDER_STEP_PART}" "${label}: ${RUN_ORDER_HEADING} Step ${token} is selected on every run"
    [[ "${selected}" != *"${RUN_ORDER_FORBIDDEN_IN_STEP}"* ]] || fail "${label}: ${RUN_ORDER_HEADING} Step ${token} must not be conditioned on ${RUN_ORDER_FORBIDDEN_IN_STEP}: ${selected:0:120}"
  done
}

# P3 (#1344): the router pins the ordered step map -- question rows 1 to 20, then
# generation steps 0 to 7 -- and states which run modes select each row. A first
# run and an `--all` run select every question section; a delta run asks only the
# IDs in `delta.ask` and still selects every generation step. The resolver runs
# below prove the first-run, --all and delta claims against the shipped script.
case_P3_run_order() {
  [[ -n "${skill}" ]] || { fail "P3: SKILL.md missing or empty"; return; }
  if [[ -z "${run_order_body}" ]]; then
    fail "P3: SKILL.md ${RUN_ORDER_HEADING} section missing or empty"
    return
  fi
  local header token row selected part want_file prev=-1 at n
  header="$(grep -m1 -E '^\| ' <<<"${run_order_body}")"
  [[ "$(table_cell "${header}" 3)" == "Selected" ]] || fail "P3: ${RUN_ORDER_HEADING} table must have a third column named Selected, got header [${header}]"

  # Question rows 1 to 20: every pinned row exists exactly once, in order, names
  # its file, and says when it is asked.
  for token in "${RUN_ORDER_QUESTION_TOKENS[@]}"; do
    n="$(grep -cF -- "| Question ${token} |" <<<"${run_order_body}")"
    if [[ "${n}" -ne 1 ]]; then
      fail "P3: ${RUN_ORDER_HEADING} must hold exactly one \"Question ${token}\" row, found ${n}"
      continue
    fi
    at="$(offset_of "${run_order_body}" "| Question ${token} |")"
    [[ "${at}" -gt "${prev}" ]] || fail "P3: ${RUN_ORDER_HEADING} row \"Question ${token}\" is out of order"
    prev="${at}"
    row="$(run_order_row "${run_order_body}" "Question ${token}")"
    selected="$(table_cell "${row}" 3)"
    for part in "${RUN_ORDER_QUESTION_PARTS[@]}"; do
      assert_contains_ws "${selected}" "${part}" "P3: ${RUN_ORDER_HEADING} \"Question ${token}\" Selected cell names [${part}]"
    done
    want_file="$(pinned_file_for "${token}" "${RUN_ORDER_QUESTION_FILES[@]}")"
    [[ -n "${want_file}" ]] || { fail "P3: test bug: no pinned file for Question ${token}"; continue; }
    assert_contains "${row}" "\`${want_file}\`" "P3: ${RUN_ORDER_HEADING} \"Question ${token}\" row names \`${want_file}\`"
  done

  # Any "Question <number>" row (including an optional question 12) keeps the
  # numbers non-decreasing, from 1 up to the 18-20 row.
  local nums num last=0 first_num="" last_num=""
  nums="$(grep -oE '^\| Question [0-9]+' <<<"${run_order_body}" | grep -oE '[0-9]+$')"
  while IFS= read -r num; do
    [[ -n "${num}" ]] || continue
    [[ -n "${first_num}" ]] || first_num="${num}"
    [[ "${num}" -ge "${last}" ]] || fail "P3: ${RUN_ORDER_HEADING} question rows are not monotone: ${num} follows ${last}"
    last="${num}"
    last_num="${num}"
  done <<<"${nums}"
  [[ "${first_num}" == "1" && "${last_num}" == "18" ]] || fail "P3: ${RUN_ORDER_HEADING} question rows must run from 1 to the 18-20 row, got first=[${first_num}] last=[${last_num}]"

  # Generation steps 0 to 7 follow the last question row, in order, each naming the
  # file it runs from and selected on every run.
  for token in "${RUN_ORDER_STEP_TOKENS[@]}"; do
    n="$(grep -cF -- "| Step ${token} |" <<<"${run_order_body}")"
    if [[ "${n}" -ne 1 ]]; then
      fail "P3: ${RUN_ORDER_HEADING} must hold exactly one \"Step ${token}\" row, found ${n}"
      continue
    fi
    at="$(offset_of "${run_order_body}" "| Step ${token} |")"
    [[ "${at}" -gt "${prev}" ]] || fail "P3: ${RUN_ORDER_HEADING} row \"Step ${token}\" is out of order"
    prev="${at}"
    row="$(run_order_row "${run_order_body}" "Step ${token}")"
    want_file="$(pinned_file_for "${token}" "${RUN_ORDER_STEP_FILES[@]}")"
    [[ -n "${want_file}" ]] || { fail "P3: test bug: no pinned file for Step ${token}"; continue; }
    assert_contains "${row}" "\`${want_file}\`" "P3: ${RUN_ORDER_HEADING} \"Step ${token}\" row names \`${want_file}\`"
  done
  check_generation_rows "P3"

  # Resolver runs: a first run ({}) and an --all run ask every question-keys.json
  # ID; each ID maps through `### Question Defaults` to a Run Order question row.
  if ! command -v jq >/dev/null 2>&1; then
    fail "P3: jq is required to run the resolver"
    return
  fi
  local ids_json id_count first_run all_run delta_run last_id
  ids_json="$(jq -c '[.questions[].id]' "${CONFIGURE_DIR}/question-keys.json")" || { fail "P3: cannot read question-keys.json"; return; }
  id_count="$(jq -r 'length' <<<"${ids_json}")"
  [[ "${id_count}" -ge "${ID_FLOOR}" ]] || fail "P3: expected at least ${ID_FLOOR} question IDs in question-keys.json, found ${id_count}"
  first_run="$(run_resolver '{}')"
  jq -e --argjson ids "${ids_json}" '.ask == $ids and .carried == []' <<<"${first_run}" >/dev/null 2>&1 \
    || fail "P3: a first run ({}) must ask every question-keys.json ID and carry none, got [${first_run}]"
  all_run="$(run_resolver "$(jq -cn --argjson ids "${ids_json}" '{configureOffered: $ids, stack: {}}')" --all)"
  jq -e --argjson ids "${ids_json}" '.ask == $ids and .carried == []' <<<"${all_run}" >/dev/null 2>&1 \
    || fail "P3: an --all run must ask every question-keys.json ID even with a full ledger, got [${all_run}]"
  last_id="$(jq -r '.[-1]' <<<"${ids_json}")"
  delta_run="$(run_resolver "$(jq -cn --argjson ids "${ids_json}" '{configureOffered: $ids[:-1]}')")"
  jq -e --arg last "${last_id}" '.ask == [$last]' <<<"${delta_run}" >/dev/null 2>&1 \
    || fail "P3: a delta run must ask only the IDs in delta.ask ([${last_id}]), got [${delta_run}]"

  local id label ids_list
  ids_list="$(jq -r '.[]' <<<"${ids_json}")" || { fail "P3: cannot list the question IDs"; return; }
  while IFS= read -r id; do
    [[ -n "${id}" ]] || continue
    label="$(defaults_label_for_id "${question_defaults_body}" "${id}")"
    if [[ -z "${label}" ]]; then
      fail "P3: question ID \`${id}\` has no row in ${QUESTION_DEFAULTS_HEADING}"
      continue
    fi
    token="$(label_token "${label}")"
    row="$(run_order_row "${run_order_body}" "Question ${token}")"
    [[ -n "${row}" ]] || fail "P3: question ID \`${id}\` maps to \"${label}\" in ${QUESTION_DEFAULTS_HEADING}, but ${RUN_ORDER_HEADING} has no \"Question ${token}\" row"
  done <<<"${ids_list}"
}

# P23 (#1344): a delta run with an empty `delta.ask` asks no question and still
# reads and runs every generation phase. The empty-ask result is produced by the
# shipped resolver on a full-ledger fixture; the router selects every generation
# step on every run; the Delta Resolution sentence is pinned; and the generation
# files' Step Map rows say they are read on every run (the wiring that makes the
# sentence true, not just stated).
case_P23_empty_delta_runs_generation() {
  [[ -n "${skill}" ]] || { fail "P23: SKILL.md missing or empty"; return; }
  if ! command -v jq >/dev/null 2>&1; then
    fail "P23: jq is required to run the resolver"
    return
  fi
  local ids_json id_count full_ledger result
  ids_json="$(jq -c '[.questions[].id]' "${CONFIGURE_DIR}/question-keys.json")" || { fail "P23: cannot read question-keys.json"; return; }
  id_count="$(jq -r 'length' <<<"${ids_json}")"
  [[ "${id_count}" -ge "${ID_FLOOR}" ]] || fail "P23: expected at least ${ID_FLOOR} question IDs in question-keys.json, found ${id_count}"
  full_ledger="$(jq -cn --argjson ids "${ids_json}" '{configureOffered: $ids}')"
  result="$(run_resolver "${full_ledger}")"
  jq -e --argjson ids "${ids_json}" '.ask == [] and .carried == $ids' <<<"${result}" >/dev/null 2>&1 \
    || fail "P23: a full-ledger delta run must have an empty delta.ask and carry every ID, got [${result}]"

  assert_contains_ws "${detect_phase}" "${DR_ZERO_ASK}" "P23: ${DETECT_FILE} states an empty delta.ask asks nothing and still runs every generation step"
  if [[ -z "${run_order_body}" ]]; then
    fail "P23: SKILL.md ${RUN_ORDER_HEADING} section missing or empty, cannot verify the generation steps are still selected"
  else
    check_generation_rows "P23"
  fi
  local file row
  for file in "${GUIDANCE_FILE}" "${SETTINGS_TEMPLATE_FILE}" "${CI_FILE}" "${WRITE_CONFIG_FILE}" "${SCHEMA_FILE}" "${COMMIT_PR_FILE}"; do
    row="$(row_for "${step_map_body}" "${file}")"
    assert_contains "${row}" "${EVERY_RUN}" "P23: the ${STEP_MAP_HEADING} row for ${file} is read on every run, including a run with an empty delta.ask"
  done
}

# case_step6_step7_markers -- the moved bold markers occur once in the new file and
# once in the SKILL.md stub (never as a body), so the existence anchor in
# configure-fragment-markers.test.sh still holds.
case_step6_step7_markers() {
  local entry marker file n_file n_skill n_all
  for entry in "${STEP6_MARKER}|${WRITE_CONFIG_FILE}" "${STEP7_MARKER}|${COMMIT_PR_FILE}"; do
    marker="${entry%%|*}"
    file="${entry#*|}"
    if [[ -z "$(file_content "${file}")" ]]; then
      fail "step marker [${marker}]: ${file} missing or empty"
      continue
    fi
    n_file="$(count_occurrences "$(file_content "${file}")" "${marker}")"
    n_skill="$(count_occurrences "${skill}" "${marker}")"
    n_all="$(count_occurrences "${configure_all}" "${marker}")"
    [[ "${n_file}" -eq 1 ]] || fail "step marker [${marker}] must occur once in ${file}, found ${n_file}"
    [[ "${n_skill}" -eq 1 ]] || fail "step marker [${marker}] must occur once in SKILL.md (the stub), found ${n_skill}"
    [[ "${n_all}" -eq 2 ]] || fail "step marker [${marker}] must occur exactly twice across the configure markdown (stub and file), found ${n_all}"
  done
}

# P7 (#1344): step 6's overlay order and validation, the offered-ledger program and
# the removal of a fallen-back question 13/14/15 ID before the ledger stamp keep
# their wording in phases/write-config.md, byte for byte, in reading order.
case_P7_step6_verbatim() {
  check_verbatim_block "P7 overlay order and validation" "${P7_OVERLAY_ORDER_BLOCK}" "${WRITE_CONFIG_FILE}" "${STUB_6}"
  check_verbatim_block "P7 fallen-back overlay paragraph" "${P7_FALLEN_BACK_BLOCK}" "${WRITE_CONFIG_FILE}" "${STUB_6}"
  check_verbatim_block "P7 offered-ledger program" "${P7_LEDGER_BLOCK}" "${WRITE_CONFIG_FILE}" "${STUB_6}"
  # Reading order inside the file: the overlay order, then the fallen-back
  # paragraph, then the configVersion stamp, then the ledger stamp and program.
  local content prev=-1 at anchor
  content="$(file_content "${WRITE_CONFIG_FILE}")"
  for anchor in \
    "${STEP6_MARKER}" \
    '**Overlay order and validation.**' \
    '**A fallen-back question 13/14/15 overlay was not applied.**' \
    'Always stamp `configVersion`' \
    'Always stamp `configureOffered`' \
    '<!-- cenci:configure-offered-ledger:begin -->'; do
    at="$(offset_of "${content}" "${anchor}")"
    if [[ "${at}" -lt 0 ]]; then
      fail "P7: ${WRITE_CONFIG_FILE} is missing [${anchor}]"
    elif [[ "${at}" -le "${prev}" ]]; then
      fail "P7: ${WRITE_CONFIG_FILE} has [${anchor}] out of reading order"
    fi
    prev="${at}"
  done
}

# The `sandbox` merge section moved from the schema prose into step 6's file
# (Decision: codex.md's "sandbox" field pointer goes to phases/write-config.md);
# the schema keeps a one-line `sandbox` field entry that points there.
case_sandbox_merge_in_write_config() {
  local pin n_all
  [[ -n "${write_config_phase}" ]] || { fail "sandbox merge: ${WRITE_CONFIG_FILE} missing or empty"; return; }
  for pin in "${SANDBOX_MERGE_PINS[@]}"; do
    assert_contains_ws "${write_config_phase}" "${pin}" "sandbox merge: ${WRITE_CONFIG_FILE} holds [${pin:0:70}]"
    n_all="$(count_occurrences "${configure_all}" "${pin}")"
    [[ "${n_all}" -eq 1 ]] || fail "sandbox merge: [${pin:0:70}] must occur exactly once across the configure markdown, found ${n_all}"
    assert_absent_paired_ws "${skill}" "${STUB_6}" "${pin}" "sandbox merge: SKILL.md no longer carries [${pin:0:70}]"
  done
  local sandbox_entry
  sandbox_entry="$(grep -F -- '`sandbox`' <<<"${schema_ref}" | grep -F -- "${SCHEMA_SANDBOX_POINTER}" | head -n 1)"
  [[ -n "${sandbox_entry}" ]] || fail "sandbox merge: ${SCHEMA_FILE} must keep a \`sandbox\` field entry that points at ${SCHEMA_SANDBOX_POINTER}"
}

# P8 (#1344): step 7's recovery branches -- nothing staged, push failure, an
# existing PR recovered with `gh pr view`, any other `gh pr create` failure, and
# removal of the PR-body temp file -- keep their wording in phases/commit-pr.md,
# byte for byte, in reading order.
case_P8_step7_recovery_verbatim() {
  check_verbatim_block "P8 nothing staged" "${P8_NOTHING_STAGED_BLOCK}" "${COMMIT_PR_FILE}" "${STUB_7}"
  check_verbatim_block "P8 push failure" "${P8_PUSH_FAILURE_BLOCK}" "${COMMIT_PR_FILE}" "${STUB_7}"
  check_verbatim_block "P8 PR create failure and recovery" "${P8_PR_RECOVERY_BLOCK}" "${COMMIT_PR_FILE}" "${STUB_7}"
  check_verbatim_block "P8 PR-body temp file removal" "${P8_TEMP_REMOVAL_BLOCK}" "${COMMIT_PR_FILE}" "${STUB_7}"
  local content prev=-1 at anchor
  content="$(file_content "${COMMIT_PR_FILE}")"
  for anchor in \
    "${STEP7_MARKER}" \
    'No configuration changes — nothing to commit' \
    '"Pushed, continue" / "Abort"' \
    'gh pr view chore/configure-<slug> --json url -q .url' \
    '"Created, continue" / "Abort"' \
    'Delete the PR-body temp file'; do
    at="$(offset_of "${content}" "${anchor}")"
    if [[ "${at}" -lt 0 ]]; then
      fail "P8: ${COMMIT_PR_FILE} is missing [${anchor}]"
    elif [[ "${at}" -le "${prev}" ]]; then
      fail "P8: ${COMMIT_PR_FILE} has [${anchor}] out of reading order"
    fi
    prev="${at}"
  done
}

# P9 (#1344): docs/board-labels.md is the only lifecycle-label table. `### Board
# lifecycle labels` is a pointer at it plus the completion-summary instruction and
# the board migration pointer, and the orthogonal-marker restatements are gone from
# every configure file (paired with the pointer that must remain).
case_P9_board_labels_pointer() {
  [[ -n "${skill}" ]] || { fail "P9: SKILL.md missing or empty"; return; }
  local body body_lines retired
  body="$(extract_section_body_raw "${skill}" "${BOARD_LABELS_HEADING}")"
  if [[ -z "${body}" ]]; then
    fail "P9: SKILL.md ${BOARD_LABELS_HEADING} section missing or empty"
    return
  fi
  assert_contains_ws "${body}" "${BOARD_LABELS_POINTER}" "P9: ${BOARD_LABELS_HEADING} points at the plugin's docs/board-labels.md"
  assert_contains_ws "${body}" "${BOARD_LABELS_SUMMARY}" "P9: ${BOARD_LABELS_HEADING} keeps the completion-summary instruction"
  assert_contains_ws "${body}" "${STUB_BOARD_NOTES}" "P9: ${BOARD_LABELS_HEADING} points at the migrations row for the board migration notes"
  [[ "${body}" != *"${MIGRATIONS_FILE}"* ]] || fail "P9: ${BOARD_LABELS_HEADING} must not name ${MIGRATIONS_FILE} (P19's line rule: a line naming it names .claude/config.json too)"
  body_lines="$(grep -c . <<<"${body}")"
  [[ "${body_lines}" -le "${BOARD_LABELS_BODY_MAX_LINES}" ]] || fail "P9: ${BOARD_LABELS_HEADING} must be a pointer of at most ${BOARD_LABELS_BODY_MAX_LINES} lines, found ${body_lines}"
  for retired in "${BOARD_LABELS_RETIRED[@]}"; do
    assert_absent_paired_ws "${configure_all}" "${BOARD_LABELS_SUMMARY}" "${retired}" "P9: configure markdown no longer restates [${retired:0:70}]"
  done
  # The one fact that was not a label restatement moved into the doc's lifecycle
  # paragraph.
  assert_contains_ws "${board_labels_doc}" "${BOARD_LABELS_TRIVIAL_FACT}" "P9: docs/board-labels.md carries the trivial-ticket fact"
}

# P11 (#1344): the complete `.cenci/config.json` examples and the field-by-field
# schema exist only in references/config-schema.md. SKILL.md carries no fenced JSON
# or YAML block, and the moved text (the examples, the field entries, the migration-
# removal list and the MCP/LSP inclusion rules at the end) occurs exactly once.
case_P11_schema_only_in_reference() {
  [[ -n "${skill}" ]] || { fail "P11: SKILL.md missing or empty"; return; }
  [[ -n "${schema_ref}" ]] || { fail "P11: ${SCHEMA_FILE} missing or empty"; return; }
  local fences schema_fences anchor n_all n_schema removal_all removal_schema
  fences="$(grep -cE '^[[:space:]]*```[[:space:]]*(json|jsonc|yaml|yml)[[:space:]]*$' <<<"${skill}")"
  [[ "${fences}" -eq 0 ]] || fail "P11: SKILL.md must contain no fenced JSON or YAML block, found ${fences}"
  assert_contains "${skill}" "${STUB_6}" "P11: SKILL.md fence absence is paired with the step 6 stub that remains"
  schema_fences="$(grep -cE '^```json$' <<<"${schema_ref}")"
  [[ "${schema_fences}" -ge 5 ]] || fail "P11: ${SCHEMA_FILE} must hold the five fenced json examples that moved, found ${schema_fences}"
  for anchor in "${SCHEMA_ANCHORS[@]}"; do
    n_all="$(count_occurrences "${configure_all}" "${anchor}")"
    n_schema="$(count_occurrences "${schema_ref}" "${anchor}")"
    [[ "${n_schema}" -eq 1 ]] || fail "P11: [${anchor}] must occur once in ${SCHEMA_FILE}, found ${n_schema}"
    [[ "${n_all}" -eq 1 ]] || fail "P11: [${anchor}] must occur exactly once across the configure markdown (in ${SCHEMA_FILE}), found ${n_all}"
  done
  # The migration-removal list and the inclusion rules close the schema, so every
  # "migration-removal list below" pointer stays inside the one file.
  local docs_at pin_at pin
  docs_at="$(offset_of "${schema_ref}" '`docs.policy` — the exact, case-sensitive string')"
  for pin in "${SCHEMA_MIGRATION_REMOVAL_LIST}" "${SCHEMA_MCP_INCLUSION}" "${SCHEMA_LSP_INCLUSION}"; do
    assert_contains_ws "${schema_ref}" "${pin}" "P11: ${SCHEMA_FILE} ends with [${pin:0:60}]"
    assert_absent_paired_ws "${skill}" "${STUB_6}" "${pin}" "P11: SKILL.md no longer carries [${pin:0:60}]"
    pin_at="$(offset_of "${schema_ref}" "${pin:0:60}")"
    if [[ "${docs_at}" -ge 0 && "${pin_at}" -ge 0 && "${pin_at}" -le "${docs_at}" ]]; then
      fail "P11: [${pin:0:60}] must come after the last field entry in ${SCHEMA_FILE}"
    fi
  done
  removal_schema="$(count_occurrences "${schema_ref}" "${SCHEMA_REMOVAL_POINTER}")"
  removal_all="$(count_occurrences "${configure_all}" "${SCHEMA_REMOVAL_POINTER}")"
  [[ "${removal_schema}" -ge "${SCHEMA_REMOVAL_POINTER_MIN}" ]] || fail "P11: ${SCHEMA_FILE} must keep at least ${SCHEMA_REMOVAL_POINTER_MIN} \"${SCHEMA_REMOVAL_POINTER}\" pointers, found ${removal_schema}"
  [[ "${removal_all}" -eq "${removal_schema}" ]] || fail "P11: every \"${SCHEMA_REMOVAL_POINTER}\" pointer must stay inside ${SCHEMA_FILE}; ${removal_all} occur across the configure markdown, ${removal_schema} in the schema file"
  # Pointers that crossed files name the file; the watch link gained one level.
  n_all="$(count_occurrences "${schema_ref}" '`phases/write-config.md`')"
  [[ "${n_all}" -ge 4 ]] || fail "P11: ${SCHEMA_FILE} must name \`phases/write-config.md\` at the sandbox entry and each \"config write\" pointer (at least 4 sites), found ${n_all}"
  assert_absent_paired_ws "${configure_all}" "${SCHEMA_SANDBOX_POINTER}" 'the config write below' "P11: configure markdown retired positional \"the config write below\" pointer"
  assert_contains "${schema_ref}" '](../../../../watch/README.md#planning-pickup-and-autonomous-re-plan)' "P11: ${SCHEMA_FILE} links watch/README.md with the corrected relative path"
  assert_absent_paired_ws "${schema_ref}" '](../../../../watch/README.md#planning-pickup-and-autonomous-re-plan)' '](../../../watch/README.md' "P11: ${SCHEMA_FILE} retired the pre-move relative watch link"
}

# P24 (#1344): re-configuring a project that predates `In Review`, `Planned` or the
# reconciler-managed labels states the matching migration note, read from a
# reference file. The three notes live in references/migrations.md under `## Board
# migration notes` (read when re-configuring an existing project); SKILL.md keeps
# only the pointer at the migrations row.
case_P24_board_migration_notes() {
  [[ -n "${migrations}" ]] || { fail "P24: ${MIGRATIONS_FILE} missing or empty"; return; }
  [[ "$(count_exact_lines "${migrations}" "${MIGRATIONS_BOARD_HEADING}")" -eq 1 ]] || fail "P24: ${MIGRATIONS_FILE} must hold exactly one [${MIGRATIONS_BOARD_HEADING}] heading"
  local board_section legacy_section n
  board_section="$(extract_h2_section_raw "${migrations}" "${MIGRATIONS_BOARD_HEADING}")"
  legacy_section="$(extract_h2_section_raw "${migrations}" "${MIGRATIONS_LEGACY_HEADING}")"
  if [[ -z "${board_section}" ]]; then
    fail "P24: ${MIGRATIONS_FILE} [${MIGRATIONS_BOARD_HEADING}] section missing or empty"
    return
  fi
  assert_contains_ws "${board_section}" "${MIGRATIONS_BOARD_CONDITION}" "P24: the board section states it is read only when re-configuring an existing project"
  # The two byte-pinned notes, checked across the configure markdown, in the file,
  # and (a second time) inside the board section, so neither can sit in the legacy
  # section or in another file.
  check_verbatim_block "P24 In Review note" "${P24_IN_REVIEW_NOTE}" "${MIGRATIONS_FILE}" "${STUB_BOARD_NOTES}"
  check_verbatim_block "P24 Planned note" "${P24_PLANNED_NOTE}" "${MIGRATIONS_FILE}" "${STUB_BOARD_NOTES}"
  n="$(count_block_occurrences "${board_section}" "${P24_IN_REVIEW_NOTE}")"
  [[ "${n}" -eq 1 ]] || fail "P24: the In Review note must sit inside [${MIGRATIONS_BOARD_HEADING}], found ${n} there"
  n="$(count_block_occurrences "${board_section}" "${P24_PLANNED_NOTE}")"
  [[ "${n}" -eq 1 ]] || fail "P24: the Planned note must sit inside [${MIGRATIONS_BOARD_HEADING}], found ${n} there"
  # The reconciler note cites step 3c, which lives in another file, so it may name
  # that file: the text around the pointer is pinned and the file name pinned apart.
  assert_contains_ws "${board_section}" "${P24_RECONCILER_NOTE_HEAD}" "P24: the reconciler-managed labels note keeps its opening"
  assert_contains_ws "${board_section}" "${P24_RECONCILER_NOTE_TAIL}" "P24: the reconciler-managed labels note keeps its closing"
  assert_contains_ws "${board_section}" "${P24_RECONCILER_NOTE_FILE}" "P24: the reconciler-managed labels note names the file holding step 3c"
  assert_absent_paired_ws "${skill}" "${STUB_BOARD_NOTES}" "${P24_RECONCILER_NOTE_HEAD}" "P24: SKILL.md no longer carries the reconciler-managed labels note"
  assert_absent_paired_ws "${skill}" "${STUB_BOARD_NOTES}" "${P24_RECONCILER_NOTE_TAIL}" "P24: SKILL.md no longer carries the reconciler-managed labels note body"
  # Exactly three notes across the configure markdown, none in the router or in
  # the legacy section.
  n="$(count_occurrences "${configure_all}" "${BOARD_NOTE_LEAD}")"
  [[ "${n}" -eq 3 ]] || fail "P24: the three board migration notes must occur exactly 3 times across the configure markdown, found ${n}"
  n="$(count_occurrences "${board_section}" "${BOARD_NOTE_LEAD}")"
  [[ "${n}" -eq 3 ]] || fail "P24: all three board migration notes must sit inside [${MIGRATIONS_BOARD_HEADING}], found ${n}"
  assert_absent_paired_ws "${skill}" "${STUB_BOARD_NOTES}" "${BOARD_NOTE_LEAD}" "P24: SKILL.md carries no board migration note body"
  assert_absent_paired_ws "${legacy_section}" "${MIGRATIONS_LEGACY_NEVER_READ}" "${BOARD_NOTE_LEAD}" "P24: the legacy section carries no board migration note"
  # The router row that gates the file names the board section's condition too.
  assert_contains_ws "$(row_for "${step_map_body}" "${MIGRATIONS_FILE}")" 're-configuring an existing project' "P24: the migrations row is read when re-configuring an existing project"
}

# Steps 6 and 7 no longer live inline, so an unreadable `Every run` file must stop
# the run instead of letting the model rebuild them from memory (#1344).
case_fail_closed_read_rule() {
  assert_contains_ws "${step_map_body}" "${FAIL_CLOSED_READ_RULE}" "${STEP_MAP_HEADING}: states the fail-closed read rule for unreadable Every run files"
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
case_moved_step_markers_unique
case_cross_file_pointers
case_allowed_tools_only_in_skill_md
case_readme_workflow_deps_row
case_block_counter_self_test
case_P30_step1_verbatim
case_P6_dockerfile_verbatim
case_P1_router_size
case_P2_condition_extractor_self_test
case_P2_every_file_named_with_condition
case_P3_run_order
case_P23_empty_delta_runs_generation
case_step6_step7_markers
case_P7_step6_verbatim
case_sandbox_merge_in_write_config
case_P8_step7_recovery_verbatim
case_P9_board_labels_pointer
case_P11_schema_only_in_reference
case_P24_board_migration_notes
case_fail_closed_read_rule

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-router-contract.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-router-contract.test.sh: all checks passed."
