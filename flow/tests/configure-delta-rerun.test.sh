#!/usr/bin/env bash
# Contract + behavior test for ticket #1246 (parent #1137) -- wire #1245's
# `resolve-delta.sh` into `/cenci:configure` so a re-run is a DELTA by default:
# questions already answered or already offered are skipped (their generation
# steps still run), every write stamps a merged `configureOffered` ledger, a
# marker-managed `.cenci/Dockerfile` is refreshed without asking, and a
# leading `--all` token replays every question (including keep-or-change
# offers for Q13 planning autonomy and Q14 automerge policy).
#
# Two kinds of case live here, each named after the acceptance criterion it
# proves (`case_AC<n>_...`); every AC the ticket claims for this suite has at
# least one explicit case below (AC1, AC2, AC3, AC4, AC5, AC7, AC8, AC9,
# AC14, AC15, AC16, AC17, AC18, AC19):
#
#   * BEHAVIOR cases extract marker-bounded jq programs out of
#     flow/skills/configure/SKILL.md and RUN them with jq against fixture
#     configs, then chain them with the REAL
#     flow/skills/configure/scripts/resolve-delta.sh (the shipped resolver).
#     They assert what the programs/resolver actually produce, never prose.
#   * CONTRACT cases pin load-bearing prose (pinned constants, never derived
#     from the doc under test) in SKILL.md, codex.md, the docs and the README,
#     with `assert_absent_paired` checks for every retired unconditional
#     claim so an absence check can never pass vacuously.
#
# --- Marker contract the SKILL.md implementation MUST write -----------------
# Each block is a fenced ```jq block (fence lines are stripped by
# extract_marked_block_raw) between a begin/end marker pair that starts at
# column 0. The program reads the CURRENT CONFIG on stdin and prints the new
# config; the test runs it as `jq -c [args] '<program>'`.
#
#   <!-- cenci:configure-offered-ledger:begin --> ... :end -->
#       Stamps the ledger. Takes `--argjson offered '[<ids>]'` (the question
#       IDs actually put to the user this run). Result:
#       .configureOffered = sorted, de-duplicated union of (the string
#       entries of any existing .configureOffered array -- non-strings are
#       dropped) and $offered. An empty result is still written as [].
#       Located anywhere in SKILL.md after "Always stamp `configVersion`"
#       (step 6).
#   <!-- cenci:configure-q13-switch:begin --> ... :end -->
#       Inside `### Autonomy Settings`. Takes `--arg autonomy <interactive|lean>`.
#       Sets .planning.autonomy to $autonomy, preserving every sibling key.
#   <!-- cenci:configure-q14-remove:begin --> ... :end -->
#       Inside `### Autonomy Settings`. No arguments. Deletes the top-level
#       `automerge` key and the `automerge` key of every projects[] entry;
#       everything else is unchanged.
#
# Idioms follow flow/tests/configure-autonomy-questions.test.sh and
# flow/docs/shell-scripting-gotchas.md: pinned constants, assert_contains_ws
# for prose that may be line-wrapped, assert_absent_paired for retired claims,
# pure extractors (no fail() inside $(...)), a `failures=` counter.
#
# Maintain's command-flags check flags backtick spans that START with "cenci "
# and carry a flag the Go CLI lacks, so `--all` is only ever written as
# `/cenci:configure --all` (slash-command form) or in prose -- never as a
# `cenci run configure ... --all` span. case_AC17_no_cenci_cli_span_carries_all
# enforces that over every file this ticket touches.
#
# Covered files:
#   - flow/skills/configure/SKILL.md, codex.md, scripts/resolve-delta.sh,
#     question-keys.json
#   - docs/autonomous-loop.md (cross-project), flow/README.md,
#     flow/docs/skill-authoring.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "configure-delta-rerun.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "configure-delta-rerun.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "configure-delta-rerun.test.sh: failed to resolve repo root." >&2; exit 2; }
RESOLVER="${FLOW_DIR}/skills/configure/scripts/resolve-delta.sh"
QUESTION_TABLE="${FLOW_DIR}/skills/configure/question-keys.json"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

if ! command -v jq >/dev/null 2>&1; then
  echo "configure-delta-rerun.test.sh: jq is required." >&2
  exit 2
fi

# --- helpers (all read/extract helpers are pure: safe inside $(...)) --------

read_doc_raw() { cat "${FLOW_DIR}/$1" 2>/dev/null; }
read_repo_doc_raw() { cat "${REPO_ROOT}/$1" 2>/dev/null; }

# require_doc / require_repo_doc <result-var> <path> -- nameref wrappers; must
# NOT be invoked via $(...). A missing file fails loudly and assigns "".
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
require_repo_doc() {
  local -n _result="$1"
  local _relpath="$2" _content
  if ! _content="$(read_repo_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${REPO_ROOT}/${_relpath}"
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
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  local norm
  norm="$(normalize_ws "${content}")"
  [[ "${norm}" == *"${pattern}"* ]] || fail "${label}: required text missing (whitespace-normalized): [${pattern}]"
}

# assert_absent_paired_ws <content> <existence-marker> <forbidden> <label> --
# the marker's existence is required first, so the absence can never pass
# vacuously before the new text exists.
assert_absent_paired_ws() {
  local content="$1" marker="$2" forbidden="$3" label="$4"
  local norm
  norm="$(normalize_ws "${content}")"
  if [[ "${norm}" != *"${marker}"* ]]; then
    fail "${label}: cannot verify absence -- existence marker missing (whitespace-normalized): [${marker}]"
    return
  fi
  [[ "${norm}" != *"${forbidden}"* ]] || fail "${label}: forbidden text present (whitespace-normalized): [${forbidden}]"
}

# count_occurrences <content> <needle> -- occurrences (not lines), per
# shell-scripting-gotchas rule 10(4).
count_occurrences() {
  local content="$1" needle="$2"
  grep -oF -- "${needle}" <<<"${content}" | wc -l | tr -d '[:space:]'
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
# the heading and the next `### ` heading.
extract_section_body_raw() {
  local content="$1" heading="$2"
  awk -v heading="${heading}" '
    index($0, heading) == 1 { found=1; next }
    found && index($0, "### ") == 1 { exit }
    found { print }
  ' <<<"${content}"
}

# extract_between_raw <content> <begin-substring> <end-substring> -- lines
# from the first line containing <begin> (inclusive) up to, not including, the
# next line containing <end>.
extract_between_raw() {
  local content="$1" begin="$2" end="$3"
  awk -v b="${begin}" -v e="${end}" '
    on && index($0, e) > 0 { exit }
    index($0, b) > 0 { on=1 }
    on { print }
  ' <<<"${content}"
}

# extract_marked_block_raw <content> <begin-marker> <end-marker> -- lines
# strictly between the marker lines (column 0), fence-delimiter lines removed.
extract_marked_block_raw() {
  local content="$1" begin_marker="$2" end_marker="$3"
  awk -v b="${begin_marker}" -v e="${end_marker}" '
    index($0, e) == 1 { on=0 }
    on && index($0, "```") != 1 { print }
    index($0, b) == 1 { on=1 }
  ' <<<"${content}"
}

# frontmatter_line_raw <content> <key> -- the first line starting with "<key>:".
frontmatter_line_raw() {
  local content="$1" key="$2"
  awk -v k="${key}:" 'index($0, k) == 1 { print; exit }' <<<"${content}"
}

# jq_apply <program> <config-json> [jq args...] -- run a program over a
# config; stdout is compact JSON. Pure (no fail()).
jq_apply() {
  local program="$1" cfg="$2"
  shift 2
  printf '%s' "${cfg}" | jq -c "$@" "${program}"
}

# json_eq <a> <b> -- exit 0 iff the two JSON documents are equal.
json_eq() {
  jq -ne --argjson a "$1" --argjson b "$2" '$a == $b' >/dev/null 2>&1
}

# assert_json_eq <label> <actual-json> <expected-json>
assert_json_eq() {
  local label="$1" actual="$2" expected="$3"
  if [[ -z "${actual}" ]]; then
    fail "${label}: no output to compare (program missing or jq error); expected ${expected}"
    return
  fi
  json_eq "${actual}" "${expected}" || fail "${label}: expected ${expected}, got ${actual}"
}

# run_resolver <config-file> [--all] -- stdout is the resolver's JSON line.
run_resolver() { bash "${RESOLVER}" "$@" 2>/dev/null; }

# passes_object_gate <text> / passes_shape_check <text> -- exit 0 iff the text
# passes the validation expression the docs under test pin (OBJECT_GATE_EXPR,
# SHAPE_CHECK_EXPR). Pure.
passes_object_gate() { printf '%s' "$1" | jq -e "${OBJECT_GATE_EXPR}" >/dev/null 2>&1; }
passes_shape_check() { printf '%s' "$1" | jq -e "${SHAPE_CHECK_EXPR}" >/dev/null 2>&1; }

# --- temp workspace (verified, cleaned up) ----------------------------------

WORK="$(mktemp -d)" || { echo "configure-delta-rerun.test.sh: mktemp failed." >&2; exit 2; }
[[ -n "${WORK}" && -d "${WORK}" ]] || { echo "configure-delta-rerun.test.sh: mktemp produced no directory." >&2; exit 2; }
trap 'rm -rf "${WORK}"' EXIT

write_cfg() { # write_cfg <name> <json> -> prints the file path
  local path="${WORK}/$1.json"
  printf '%s\n' "$2" > "${path}" || return 1
  printf '%s' "${path}"
}

# --- Pinned constants the docs under test must carry verbatim ----------------
# Pinned, never derived from the doc under test, so a missing or reworded
# sentence fails here rather than silently re-defining the contract.

# Marker names (see the header).
LEDGER_BEGIN='<!-- cenci:configure-offered-ledger:begin -->'
LEDGER_END='<!-- cenci:configure-offered-ledger:end -->'
Q13_SWITCH_BEGIN='<!-- cenci:configure-q13-switch:begin -->'
Q13_SWITCH_END='<!-- cenci:configure-q13-switch:end -->'
Q14_REMOVE_BEGIN='<!-- cenci:configure-q14-remove:begin -->'
Q14_REMOVE_END='<!-- cenci:configure-q14-remove:end -->'

# Frontmatter.
ARGUMENT_HINT_LINE='argument-hint: [--all] [additional context]'
RESOLVE_DELTA_GRANT='Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh":*)'

# `### Parse $ARGUMENTS`.
PARSE_HEADING='### Parse `$ARGUMENTS`'
ARGS_ALL_RULE='`--all` is a reserved leading token of `$ARGUMENTS`; everything after it is user context.'
ARGS_SLUG_RULE='Strip the `--all` token before deriving the slug'

# Re-run message (Existing Config Detection).
RERUN_MODE_LEAD='tell the user before starting questions, according to the run mode'
RERUN_DELTA_MESSAGE='Found existing configuration. Only new or never-offered questions will be asked; answers you already gave are carried through. Run `/cenci:configure --all` to re-open every question.'
RERUN_ALL_MESSAGE='Found existing configuration. Each question will show your current setting as the default — select it to keep it unchanged.'

# `### Delta Resolution` (new subsection between Scripted Detection and
# Platform Detection).
DELTA_HEADING='### Delta Resolution'
DR_MODE_RULE='The run mode is `delta` only when `existingConfig` (or `migrationBase`) exists, `--all` is absent, and the resolver succeeds; otherwise the run mode is `full` and every question is asked with `--all` semantics, except questions 13 and 14 (see Fallback).'
DR_LEGACY_FULL_BY_DEFINITION='all three are `full` runs by the first and second conditions, not by a resolver failure.'
OLD_DR_MODE_RULE_UNQUALIFIED='every question is asked with `--all` semantics. That is today'"'"'s behavior'
DR_OWN_CALL='Run the resolver as its **own** Bash call'
DR_CALL_CMD='bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh" <config-path>'
DR_MIGRATION_INPUT='`migrationBase` is written to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-delta-input.json` first and removed afterwards'
DR_ASK_RULE='Ask only the question IDs in `delta.ask` and carry every ID in `delta.carried`.'
DR_ZERO_ASK='When `delta.ask` is empty, ask no questions and still run every generation step.'
DR_CARRIED_DRIVES_GENERATION='A carried answer skips only the prompt — its value still drives every generation step.'
DR_FALLBACK='If the resolver exits non-zero, prints invalid JSON, or its JSON fails the shape check above, treat the run as `full` and tell the user why.'
OLD_DR_FALLBACK='If the resolver exits non-zero or prints invalid JSON, treat the run as `full` and tell the user why.'
SHAPE_CHECK_EXPR='(.ask | type) == "array" and (.carried | type) == "array"'
DR_SHAPE_CHECK='before using it, require that `delta.ask` and `delta.carried` are both JSON arrays'
DR_SHAPE_CHECK_EXPR="for example \`jq -e '${SHAPE_CHECK_EXPR}'\` over the stdout line, exit 0"
DR_FALLBACK_NO_REOFFER='A `full` run caused by a resolver failure does **not** re-offer a present `planning` or `automerge` value — only an explicit `--all` does'
DR_LEGACY_ONLY_FULL='A legacy-only run has no canonical config to resolve against, so it is a `full` run and never calls the resolver.'
DR_LEGACY_ONLY_NEVER_CALLS='a legacy-only migration run (only `.claude/config.json` exists, so there is no `.cenci/config.json`, no `existingConfig` and no `migrationBase` to resolve against) and a `--all` run never call the resolver'
OLD_DR_LEGACY_INPUT='(or `legacyConfig` when `.cenci/config.json` does not exist)'
DR_SCRATCH_RM_ON_FAILURE='whether it succeeded, failed or failed the shape check below'
DR_OFFERED_APPLIED_ONLY='An ID is recorded only for an answer that was applied or explicitly declined'
DR_OFFERED_OVERLAY_FALLBACK='when the question 13/14 overlay falls back in step 6, that question'"'"'s ID is removed from the offered set before the ledger is stamped'
DR_OFFERED_Q11='question 11 is recorded only after a successful `cenci dispatch enroll` or an explicit "No"'
DR_USER_CONTEXT='Free-text user context never re-opens an answered question on a delta run; if it names one, say that `/cenci:configure --all` re-opens it.'
DR_STACK_REOFFERS='The only re-offers a detected stack change causes are question 1 pre-filled with the detected stack, Steps 2a/2b for new monorepo projects, and questions 5/6 for servers missing from the existing maps.'
DR_CI_CARRIED='When question 8 is carried, `ci.yml` is neither regenerated nor prompted for.'
DR_UI_CARRIED='A carried `uiConventions` keeps the existing `## UI Conventions` section verbatim.'
DR_OFFERED_ONLY_ASKED='Only questions actually put to the user this run are recorded as offered'
OLD_SLUG_EXAMPLE='(e.g. "refresh MCP servers" → `mcp-refresh`)'

# Board Config / Q10.
BOARD_HEADING='### Board Config (lazyboards)'
Q10_DECLINE_RECORDED='A declined question 10 is recorded in `configureOffered` and is not re-asked on a delta run.'
Q10_CARRIED_MISSING_FILE='If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it.'
Q10_ANALYZER_REPORTS_ONLY='On a delta run with an existing `.lazyboards.yml`, the suggest-or-skip analyzer reports new board actions in the end-of-run summary and never prompts; `--all` restores the Apply/Overwrite/Keep/Show prompt.'
OLD_Q10_REASK_CLAIM='without asking again this session'

# Fleet Dispatch / Q11 / Q12.
FLEET_HEADING='### Fleet Dispatch Enrollment'
Q11_DECLINE_RECORDED='A decline of question 11 is recorded in `configureOffered`; a delta run does not re-ask it, and `--all` does.'
Q12_REPAIR_STILL_ASKED='The `enrolled: true, session: ""` repair (question 12) is still asked on a delta run'
OLD_Q11_NOT_RECORDED='A decline is not recorded anywhere'
OLD_NEVER_REPROMPTS='Re-running `/cenci:configure` on an already-configured repo never re-prompts'

# Autonomy Settings (`--all` re-offers; the delta-scoped absent-only rule is
# pinned in configure-autonomy-questions.test.sh as ABSENT_ONLY_RULE).
AUTONOMY_HEADING='### Autonomy Settings'
Q13_Q14_DELTA_RULE='On a delta run, questions 13 and 14 are asked only when the key is absent and the ID is not in `configureOffered`; nothing is written on a skip.'
Q13_ALL_REOFFER='Under `--all`, question 13 is re-offered even when `planning` is present, with the committed value pre-selected.'
Q13_ALL_OPTION_KEEP='"Keep current (Recommended)"'
Q13_ALL_OPTION_SWITCH_INTERACTIVE='"Switch to `interactive`"'
Q13_ALL_OPTION_SWITCH_LEAN='"Switch to `lean`"'
Q13_ALL_KEEP_WRITES_NOTHING='Keeping the current value writes nothing.'
Q13_ALL_SWITCH_EXPLICIT='Switching to `interactive` writes `planning.autonomy: "interactive"` explicitly, preserving sibling keys.'
Q14_ALL_REOFFER='Under `--all`, question 14 is re-offered even when `automerge` is present, with "Keep current block(s) (Recommended)" pre-selected.'
Q14_ALL_OPTION_KEEP='"Keep current block(s) (Recommended)"'
Q14_ALL_OPTION_REMOVE='"Remove the block(s)"'
Q14_ALL_KEEP_BYTE_IDENTICAL='Keeping leaves every `automerge` block byte-identical — never removed and never narrowed.'
Q14_ALL_REMOVE_SCOPE='"Remove the block(s)" deletes the top-level `automerge` key and the `automerge` key of every `projects[]` entry.'
OLD_UNCONDITIONAL_ABSENT_ONLY_RULE='When `planning` or `automerge` is already present, report the existing value verbatim, never re-prompt, never narrow, and never remove it.'

# 5e Dockerfile (scoped to the "Merge-safe regeneration" block).
DOCKERFILE_BLOCK_BEGIN='**Merge-safe regeneration**'
DOCKERFILE_BLOCK_END='**Monorepo**: fragments are'
DOCKERFILE_DELTA_VALID='On a delta run with valid `# cenci:managed-begin` / `# cenci:managed-end` markers, refresh the managed block silently — no Q9/Q9b/Q9c prompt — and leave every byte outside the markers unchanged.'
DOCKERFILE_DELTA_REPORT='Report the block diff with `git -C <worktree-path> diff -- .cenci/Dockerfile`.'
DOCKERFILE_DELTA_UNTOUCHED='On a delta run, a `.cenci/Dockerfile` with no markers or malformed markers is left untouched and reported — no prompt.'
DOCKERFILE_ALL_PROMPT='On an `--all` or `full` run, a `.cenci/Dockerfile` with no markers or malformed markers gets the Overwrite/Skip/Show prompt.'
DOCKERFILE_OVERWRITE_OPTION='Overwrite — wrap it in managed markers and replace with the generated block'
OLD_DOCKERFILE_NO_MARKERS_CLAIM='(e.g. a hand-authored legacy `.cenci/Dockerfile`): do not silently overwrite it. Reuse the exact Overwrite/Skip/Show conflict-check UX'
OLD_DOCKERFILE_MALFORMED_CLAIM='Route this through the exact same Overwrite/Skip/Show conflict-check UX as the "no markers" case above'

# Step 6 / step 7 / restatement sites.
# Step regions: step 6 runs from its numbered heading up to step 7's; step 7
# (PR body + final report) runs up to the next `###` section. Pins that belong
# to a step are asserted against that region only, so they cannot pass from an
# unrelated section that happens to repeat the sentence.
STEP6_BEGIN='6. **Write `.cenci/config.json`**'
STEP7_BEGIN='7. **Commit, Push, and Open PR**'
STEP7_END='### Board lifecycle labels'
STEP6_ALWAYS_STAMP='Always stamp `configVersion`'
STEP6_LEDGER_UNION='Always stamp `configureOffered` on every write as the sorted, de-duplicated union of the existing ledger and the question IDs offered this run; `--all` never removes ledger entries.'
STEP6_EMPTY_LEDGER='An empty result is still written as `[]`.'
STEP6_OVERLAY_ORDER='The overlay order is `merge-sandbox-config.sh` stdout, then the question 13/14 overlays (`cenci:configure-q13-switch`, `cenci:configure-q14-remove`), then the `configureOffered` ledger (`cenci:configure-offered-ledger`).'
OBJECT_GATE_EXPR='type == "object"'
STEP6_OBJECT_GATE="An overlay result is valid only when the \`jq\` call exits 0 and the result passes \`jq -e '${OBJECT_GATE_EXPR}'\`: an empty output passes \`jq empty\`, so \`jq empty\` alone never gates a write."
STEP6_GATE_TIMING='Apply that check after each overlay and before every write'
OLD_STEP6_JQ_EMPTY='Validate with `jq empty` after each overlay and before every write'
STEP6_SCRATCH_RM='that `rm -f` also runs when an overlay fails validation'
STEP6_FALLBACK_NOT_APPLIED='When a question 13 or 14 overlay fails the check above, the collected answer is not in the written config. Tell the user which question'"'"'s answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; remove that question'"'"'s ID (`planning` for question 13, `automerge` for question 14) from the offered set before the ledger overlay runs'
STEP6_FALLBACK_KEYS_STEP7='a fallen-back "Remove the block(s)" never emits the `## Review required — automerge policy removed` section and is never reported as removed'
STEP6_CARRIED_FLAGS='For an ID in `delta.carried`, the Q9/Q9b/Q9c flag is derived from the existing `sandbox.*` value, never passed as a bare `false`'
STEP6_ASKED_FLAGS='For an ID in `delta.ask` (a key that was absent and never offered, e.g. `sandbox.dind` and `sandbox.azure` on a config that predates questions 9b/9c), and for every question on an `--all` or `full` run, the flag is the answer collected this run.'
STEP6_EFFECTIVE_ENABLED='Generate or refresh the `.cenci/Dockerfile` (step 5e) whenever the effective `sandbox.enabled` is `true`, carried or just answered; a just-answered Yes for `--dind` or `--azure` selects its fragment in that same generation.'
OLD_STEP6_CARRIED_FLAGS='Carried Q9/Q9b/Q9c flags are derived from the existing `sandbox.*` values, never passed as a bare `false`.'
STEP6_LEDGER_FELL_BACK_IDS='minus any ID removed above because its overlay fell back, or because question 11'"'"'s enroll failed'
DOCKERFILE_EFFECTIVE_PER_ID='An ID in `delta.carried` reads the existing config value; an ID in `delta.ask` this run'
DOCKERFILE_EFFECTIVE_CONSUMED='so a just-answered Yes is consumed by this same generation'
DOCKERFILE_EFFECTIVE_ENABLED='A Dockerfile is generated or refreshed whenever the effective `sandbox.enabled` is `true`, whether it was carried or just answered.'
DOCKERFILE_MARKERS_NEVER_ASK='Only an ID that is in `delta.ask` (key absent and never offered) puts a Q9/Q9b/Q9c question to the user on a delta run; the markers never cause one.'
OLD_DOCKERFILE_ALL_CARRIED='on a delta run the Q9/Q9b/Q9c answers are carried, so the fragment selection'
OLD_5E_HEADER_CARRIED='only if the carried `sandbox.enabled` is `true`'
STEP7_REMOVED_APPLIED='and the removal overlay passed step 6'"'"'s validation and was written'
STEP7_FINAL_REPORT_FELL_BACK='when a question 13/14 overlay fell back, say its answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; when the ledger overlay fell back, say the ledger was not stamped so this run'"'"'s questions will be asked again; when question 11'"'"'s enroll failed, report the failure and that question 11 will be asked again only when `dispatchEnroll` is not already in the existing ledger, else to re-run `/cenci:configure --all`.'
OLD_STEP6_FALLBACK_UNCONDITIONAL='was not applied and that it will be asked again on the next delta run; remove'
OLD_STEP7_FALLBACK_UNCONDITIONAL='say its answer was not applied and will be asked again on the next delta run;'
OLD_STEP7_Q11_UNCONDITIONAL='report the failure and that question 11 will be asked again.'
OLD_DR_OFFERED_UNCONDITIONAL='(so the next delta run asks it again)'
DR_OFFERED_CONDITIONAL_REASK='the next delta run asks it again only when its key is absent and its ID is not already in the existing ledger; otherwise `/cenci:configure --all` applies it'
Q11_ENROLL_REASK_CONDITIONAL='asks question 11 again only when `dispatchEnroll` is not already in the existing ledger'
Q11_ENROLL_FAILURE='`dispatchEnroll` is recorded only after a successful `cenci dispatch enroll` or an explicit "No — skip"'
Q13_Q14_FULL_NO_REOFFER='A `full` run caused by a resolver failure (see `### Delta Resolution`) is not an `--all` run: for a present `planning` or `automerge` key it behaves like a delta run — report the existing value verbatim and do NOT re-offer it'
STEP7_CARRIED_HEADING='## Carried through (delta run)'
STEP7_CARRIED_SUMMARY='lists the carried question IDs and config key names only — never config values — and says `/cenci:configure --all` would re-open them.'
STEP7_STAMP_ONLY='A stamp-only diff still opens a PR; "No configuration changes — nothing to commit" applies only when nothing is staged.'
STEP7_NOTHING_TO_COMMIT='report "No configuration changes — nothing to commit"'
STEP7_REMOVED_HEADING='## Review required — automerge policy removed'
STEP7_REMOVED_CONDITIONAL='emitted only when question 14 removed the block(s) this run'
STEP7_REPORT_SUMMARY='The final report repeats the carried-through summary, the Dockerfile report, and the lazyboards report.'
RESTATE_MARKER='on a delta run; `--all` re-offers it'
RESTATE_MIN_COUNT=3
OLD_PLANNING_RESTATE_A='writes the `planning` key only when you explicitly choose `lean`, or when it was already present in `existingConfig`'
OLD_PLANNING_RESTATE_B='when the key is already present, this skill reports it verbatim and never re-prompts, narrows, or removes it'
OLD_AUTOMERGE_RESTATE='when a block is already present, this skill reports it verbatim and never re-prompts, narrows, or removes it'
OLD_RESOLVER_NOT_WIRED='The configure flow itself does not call the resolver yet; that wiring is tracked in #1246.'
NEW_RESOLVER_WIRED='The configure flow calls this resolver itself — see `### Delta Resolution`.'

# codex.md parity paragraph.
CODEX_DELTA_MODE='Delta mode asks only the question IDs the resolver lists in `ask` and carries the rest'
CODEX_ALL_TOKEN='A leading `--all` in the user'"'"'s request replays every question and ignores the ledger'
CODEX_Q13_Q14_ALL='under `--all`, question 13 re-offers "Keep current (Recommended)" or a switch, and question 14 re-offers "Keep current block(s) (Recommended)" or "Remove the block(s)"'
CODEX_LEDGER='stamps `configureOffered` with the same sorted, de-duplicated union as the Claude procedure'
CODEX_SHARED_PROGRAMS='uses the same marker-bounded jq programs as `SKILL.md`'
CODEX_DOCKERFILE='refreshes a valid marker-managed `.cenci/Dockerfile` without asking, and leaves a `.cenci/Dockerfile` with no markers or malformed markers untouched and reported'
CODEX_Q12='The Q12 dispatch session repair is still asked on a delta run'
CODEX_FALLBACK='If the resolver fails, it falls back to a full replay and says why'
CODEX_PLAN_MODE='the resolver runs read-only during `/plan`; the ledger, overlay and Dockerfile writes happen only in the apply step'
CODEX_AUTONOMY_ABSENT_ONLY_DELTA='on a delta run asks about `planning` and `automerge` only when absent and not already offered'
OLD_CODEX_AUTONOMY_UNCONDITIONAL='asks about `planning` and `automerge` only when absent — an already-present value'
CODEX_AUTONOMY_MIRROR='Autonomy Settings mirrors the Claude procedure'"'"'s `### Autonomy Settings` section'
CODEX_SANDBOX_CARRIED='the flag for a carried ID is derived from the existing `sandbox.*` value (`true` when it is `true`, `false` only for an absent key), never a bare `false`'
CODEX_SANDBOX_ASKED='an ID listed in `ask` (for example `sandbox.dind` and `sandbox.azure` on a config that predates them) uses the answer collected this run, so a just-answered Yes is written and consumed'
CODEX_SANDBOX_EFFECTIVE='Generate or refresh the managed `.cenci/Dockerfile` whenever the effective `sandbox.enabled` is `true`, carried or just answered.'
CODEX_OBJECT_GATE='the `jq` call must exit 0 and the result must pass `jq -e '"'"'type == "object"'"'"'` (an empty output passes `jq empty`, so that alone is not a gate)'
OLD_CODEX_JQ_EMPTY='validating each overlay with `jq empty` before the write'
CODEX_SHAPE_CHECK="A resolver result whose \`ask\` or \`carried\` is not a JSON array fails the same check (\`jq -e '${SHAPE_CHECK_EXPR}'\`) and falls back the same way."
CODEX_FALLBACK_NO_REOFFER='A fallback full replay is not an `--all` run: it does not re-offer a present `planning` or `automerge` value'
CODEX_SCRATCH_RM='is removed afterwards even when the resolver or a later overlay fails'
CODEX_LEGACY_ONLY='A legacy-only run (only `.claude/config.json`, no `.cenci/config.json`) is a full run and never calls the resolver.'
CODEX_OVERLAY_FELL_BACK='When a question 13/14 overlay falls back, its answer was not applied: the apply step tells the user (that the question will be asked again on the next delta run only when its key is absent and its ID is not already in the existing ledger, otherwise to re-run `/cenci:configure --all` to apply it), removes that question'"'"'s ID from the offered set before stamping the ledger, and reports only the overlay actually applied'
OLD_CODEX_OVERLAY_UNCONDITIONAL='the apply step tells the user, removes that question'"'"'s ID'
CODEX_Q11_ENROLL='`dispatchEnroll` is recorded only after a successful `cenci dispatch enroll` or an explicit "No"; an enroll failure is reported and not recorded.'
CODEX_CHECKPOINT='travel in the plan/apply checkpoint; the apply step reads them from there and never re-derives them from the resolver'
CODEX_REPORT_CARRIED_SUMMARY='the carried-through summary (question IDs and config key names, never values in the PR body, and saying `--all` would re-open them)'
CODEX_REPORT_LAZYBOARDS_MISSING='the notice when question 10 is carried but `.lazyboards.yml` is missing (it is not regenerated; `--all` re-offers it)'
CODEX_REPORT_LEDGER_NOT_STAMPED='a ledger-not-stamped notice when the ledger overlay fell back, so this run'"'"'s questions will be asked again'
CODEX_REPORT_DOCKERFILE='the Dockerfile report (the block diff for a refreshed block, or "left untouched" for no or malformed markers)'

# docs/autonomous-loop.md.
LOOP_SECTION3_HEADING='### 3. Declare what a merge is allowed to touch'
LOOP_AUTOMERGE_DELTA_CLAIM='`/cenci:configure` offers to scaffold this block on a delta run only when `automerge` is absent and has not been offered before — an existing block is reported verbatim and never re-prompted, narrowed, or removed.'
LOOP_AUTOMERGE_ALL_CLAIM='`/cenci:configure --all` re-offers it with "Keep current block(s) (Recommended)" pre-selected; choosing "Remove the block(s)" deletes the top-level block and every `projects[]` block.'
LOOP_PLANNING_CLAIM='`/cenci:configure` offers `planning.autonomy` on a delta run when the key is absent and has not been offered before, defaulting to `interactive`; `/cenci:configure --all` re-offers it with the committed value pre-selected.'
OLD_LOOP_AUTOMERGE_UNCONDITIONAL='`/cenci:configure` offers to scaffold this block only when `automerge` is absent — an existing block is reported verbatim and never re-prompted, narrowed, or removed.'
OLD_LOOP_PLANNING_UNCONDITIONAL='`/cenci:configure` offers `planning.autonomy` when the key is absent, defaulting to `interactive`.'

# flow/README.md and flow/docs/skill-authoring.md.
README_DELTA_NOTE='a re-run of `/cenci:configure` asks only new or never-offered questions; `/cenci:configure --all` replays every question.'
README_CONTEXT7_MIGRATION='Re-run `/cenci:configure --all` and select Context7'
README_PIN_200K='run `/cenci:configure --all` and answer **Yes** to "Pin subagents to 200K"'
OLD_README_CONTEXT7_MIGRATION='Re-run `/cenci:configure` and select Context7'
OLD_README_PIN_200K='run `/cenci:configure` and answer **Yes** to "Pin subagents to 200K"'
SKILL_AUTHORING_UNATTENDED='On an unattended or delta run with no one to answer a prompt, leave the file untouched and report it — still never guess a replacement span.'
OLD_SKILL_AUTHORING_ALWAYS_PROMPT='route them through the same manual Overwrite/Skip/Show conflict prompt used for "no markers," never guess a replacement span.'

# --- Load the docs under test -----------------------------------------------

require_doc skill "skills/configure/SKILL.md" || true
require_doc codex "skills/configure/codex.md" || true
require_repo_doc loop_doc "docs/autonomous-loop.md" || true
require_doc readme "README.md" || true
require_doc skill_authoring "docs/skill-authoring.md" || true

parse_body=""
delta_body=""
board_body=""
fleet_body=""
autonomy_body=""
dockerfile_block=""
step6_region=""
step7_region=""
LEDGER_PROG=""
Q13_PROG=""
Q14_PROG=""
if [[ -n "${skill}" ]]; then
  parse_body="$(extract_section_body_raw "${skill}" "${PARSE_HEADING}")"
  delta_body="$(extract_section_body_raw "${skill}" "${DELTA_HEADING}")"
  board_body="$(extract_section_body_raw "${skill}" "${BOARD_HEADING}")"
  fleet_body="$(extract_section_body_raw "${skill}" "${FLEET_HEADING}")"
  autonomy_body="$(extract_section_body_raw "${skill}" "${AUTONOMY_HEADING}")"
  dockerfile_block="$(extract_between_raw "${skill}" "${DOCKERFILE_BLOCK_BEGIN}" "${DOCKERFILE_BLOCK_END}")"
  step6_region="$(extract_between_raw "${skill}" "${STEP6_BEGIN}" "${STEP7_BEGIN}")"
  step7_region="$(extract_between_raw "${skill}" "${STEP7_BEGIN}" "${STEP7_END}")"
  LEDGER_PROG="$(extract_marked_block_raw "${skill}" "${LEDGER_BEGIN}" "${LEDGER_END}")"
  Q13_PROG="$(extract_marked_block_raw "${autonomy_body}" "${Q13_SWITCH_BEGIN}" "${Q13_SWITCH_END}")"
  Q14_PROG="$(extract_marked_block_raw "${autonomy_body}" "${Q14_REMOVE_BEGIN}" "${Q14_REMOVE_END}")"
fi

# need_prog <program-text> <label> -- fails (in the parent shell) when an
# extracted program is missing, so every behavior case fails for the right
# reason: the marker block has not been authored.
need_prog() {
  local program="$1" label="$2"
  if [[ -z "${program}" ]]; then
    fail "${label}: marker-bounded jq program not found in SKILL.md (see the marker contract in this file's header)"
    return 1
  fi
  if ! printf '%s' "${EMPTY_OBJ}" | jq -c --argjson offered '[]' --arg autonomy interactive "${program}" >/dev/null 2>&1; then
    fail "${label}: extracted jq program does not compile/run on ${EMPTY_OBJ}"
    return 1
  fi
  return 0
}

# require_region <region-text> <label> -- fails (in the parent shell) when a
# scoped section extraction came back empty, so a missing anchor can never
# turn into a vacuous scoped pass.
require_region() {
  if [[ -z "$1" ]]; then
    fail "$2: section extraction is empty (anchor missing from SKILL.md)"
    return 1
  fi
  return 0
}

# --- Fixtures ---------------------------------------------------------------

EMPTY_OBJ='{}'

# Every applicable question answered or ledger-recorded: a delta run asks
# nothing. Keyless rows (uiConventions, dispatchEnroll) are ledger-tracked.
# Sentinel values prove the resolver/summary never leak config values.
BRANCH_SENTINEL='sentinel-branch-pattern-do-not-leak'
COMPLETE_CFG='{
  "configVersion": "1.0.0",
  "stack": {"backend": "dotnet10"},
  "isMonorepo": false,
  "branchPattern": "'"${BRANCH_SENTINEL}"'",
  "mcpServers": {"context7": true},
  "playwrightCli": false,
  "lspServers": {"typescript": true},
  "autoCompactDisabled": true,
  "pinSubagents200K": false,
  "cicd": {"enabled": true, "platform": "github-actions"},
  "sandbox": {"enabled": true, "dind": false, "azure": false},
  "lazyboards": {"enabled": false},
  "planning": {"autonomy": "interactive", "note": "keep-me"},
  "automerge": {"maxChangedFiles": 5},
  "configureOffered": ["dispatchEnroll", "uiConventions"],
  "cenci": {"gateOutputLines": 80},
  "security": {"sensitivePaths": ["*vault*"]}
}'

# Fail hard: every case below compares against ALL_IDS, so an unreadable or
# empty question table must stop the suite rather than turn into a
# placeholder that both sides of a comparison could share.
ALL_IDS="$(jq -c '[.questions[].id]' "${QUESTION_TABLE}" 2>/dev/null)" \
  || { echo "configure-delta-rerun.test.sh: cannot read question ids from ${QUESTION_TABLE}." >&2; exit 2; }
if ! jq -e 'type == "array" and length > 0 and all(.[]; type == "string")' <<<"${ALL_IDS}" >/dev/null 2>&1; then
  echo "configure-delta-rerun.test.sh: ALL_IDS is not a non-empty array of strings: [${ALL_IDS}]" >&2
  exit 2
fi

# ============================================================================
# BEHAVIOR cases
# ============================================================================

case_AC1_zero_questions_and_serialized_stamp_noop() {
  local cfg_file out expected stamped
  cfg_file="$(write_cfg ac1-complete "${COMPLETE_CFG}")" || { fail "AC1: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  assert_json_eq "AC1 delta resolver on a complete config asks nothing" "$(jq -c '.ask' <<<"${out:-null}" 2>/dev/null)" '[]'
  assert_json_eq "AC1 delta resolver carries no missing keys" "$(jq -c '.missingKeys' <<<"${out:-null}" 2>/dev/null)" '[]'

  need_prog "${LEDGER_PROG}" "AC1 ledger program" || return
  stamped="$(jq_apply "${LEDGER_PROG}" "${COMPLETE_CFG}" --argjson offered '[]')"
  expected="$(jq -c . <<<"${COMPLETE_CFG}")"
  # Serialized (compact) text, not parsed equality: key order and every value
  # must survive the stamp byte for byte.
  [[ -n "${stamped}" && "${stamped}" == "${expected}" ]] \
    || fail "AC1: a no-question run on a sorted ledger must leave the compact-serialized config unchanged; expected ${expected}, got ${stamped}"
}

case_AC2_single_missing_key_asks_exactly_that_question() {
  local missing_cfg cfg_file out stamped after_file out2
  missing_cfg="$(jq -c 'del(.pinSubagents200K)' <<<"${COMPLETE_CFG}")"
  cfg_file="$(write_cfg ac2-missing "${missing_cfg}")" || { fail "AC2: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  assert_json_eq "AC2 exactly the missing key's question is asked" "$(jq -c '.ask' <<<"${out:-null}" 2>/dev/null)" '["pinSubagents200K"]'
  assert_json_eq "AC2 the missing key is reported" "$(jq -c '.missingKeys' <<<"${out:-null}" 2>/dev/null)" '["pinSubagents200K"]'

  need_prog "${LEDGER_PROG}" "AC2 ledger program" || return
  # The user declined: no key is written, only the ledger records the offer.
  stamped="$(jq_apply "${LEDGER_PROG}" "${missing_cfg}" --argjson offered '["pinSubagents200K"]')"
  assert_json_eq "AC2 every other key is preserved verbatim after the stamp" \
    "$(jq -c 'del(.configureOffered)' <<<"${stamped:-null}" 2>/dev/null)" \
    "$(jq -c 'del(.configureOffered)' <<<"${missing_cfg}")"
  assert_json_eq "AC2 the declined ID is recorded in the ledger" \
    "$(jq -c '.configureOffered | index("pinSubagents200K") != null' <<<"${stamped:-null}" 2>/dev/null)" 'true'
  after_file="$(write_cfg ac2-after "${stamped:-${EMPTY_OBJ}}")" || { fail "AC2: stamped fixture write failed"; return; }
  out2="$(run_resolver "${after_file}")"
  assert_json_eq "AC2 a declined question stops re-asking on the next delta run" "$(jq -c '.ask' <<<"${out2:-null}" 2>/dev/null)" '[]'
}

case_AC2_decline_chain_from_first_run() {
  # Critical journey: a first run offers everything; after the ledger stamp a
  # second delta run asks nothing even though every answer was declined
  # (declined answers write no key).
  local cfg_file out first_ask stamped after_file out2
  cfg_file="$(write_cfg chain-first "{}")" || { fail "chain: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  first_ask="$(jq -c '.ask' <<<"${out:-null}" 2>/dev/null)"
  assert_json_eq "chain: a first run (empty config) asks every question" "${first_ask}" "${ALL_IDS}"
  [[ -n "${first_ask}" ]] || { fail "chain: the first-run resolver produced no ask list; cannot stamp the ledger"; return; }
  need_prog "${LEDGER_PROG}" "chain ledger program" || return
  stamped="$(jq_apply "${LEDGER_PROG}" '{}' --argjson offered "${first_ask}")"
  after_file="$(write_cfg chain-after "${stamped:-${EMPTY_OBJ}}")" || { fail "chain: stamped fixture write failed"; return; }
  out2="$(run_resolver "${after_file}")"
  assert_json_eq "chain: the second run asks nothing after the ledger stamp" "$(jq -c '.ask' <<<"${out2:-null}" 2>/dev/null)" '[]'
  assert_json_eq "chain: the second run carries every question" "$(jq -c '.carried' <<<"${out2:-null}" 2>/dev/null)" "${ALL_IDS}"
}

case_AC4_all_replay_asks_everything_and_stamps_the_full_list() {
  # The "--all never shrinks the ledger" claim is proven by
  # case_AC14_ledger_is_sorted_deduplicated_union (subset-offered case).
  local cfg_file out stamped
  cfg_file="$(write_cfg ac4-complete "${COMPLETE_CFG}")" || { fail "AC4: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}" --all)"
  assert_json_eq "AC4 --all asks every question on a complete, ledger-recorded config" "$(jq -c '.ask' <<<"${out:-null}" 2>/dev/null)" "${ALL_IDS}"
  assert_json_eq "AC4 --all carries nothing" "$(jq -c '.carried' <<<"${out:-null}" 2>/dev/null)" '[]'

  need_prog "${LEDGER_PROG}" "AC4 ledger program" || return
  stamped="$(jq_apply "${LEDGER_PROG}" "${COMPLETE_CFG}" --argjson offered "${ALL_IDS}")"
  assert_json_eq "AC4 --all stamps the full ID list (sorted, de-duplicated)" \
    "$(jq -c '.configureOffered' <<<"${stamped:-null}" 2>/dev/null)" \
    "$(jq -c '[.questions[].id] | unique' "${QUESTION_TABLE}")"
}

case_AC5_resolver_output_carries_ids_and_keys_never_values() {
  local cfg_file out
  cfg_file="$(write_cfg ac5-complete "${COMPLETE_CFG}")" || { fail "AC5: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  if [[ -z "${out}" ]]; then
    fail "AC5: resolver produced no output on a complete config"
    return
  fi
  assert_json_eq "AC5 a complete config carries every question" "$(jq -c '.carried' <<<"${out}" 2>/dev/null)" "${ALL_IDS}"
  [[ "${out}" != *"${BRANCH_SENTINEL}"* ]] || fail "AC5: resolver output leaks a config value (${BRANCH_SENTINEL}); the carried summary must hold IDs and key names only"
}

case_AC7_planning_delta_carried_and_q13_switch() {
  local planning_cfg cfg_file out delta_out all_out switched switched_back
  planning_cfg='{"planning":{"autonomy":"lean","x":1},"stack":{"backend":"go"}}'
  cfg_file="$(write_cfg ac7-planning "${planning_cfg}")" || { fail "AC7: fixture write failed"; return; }
  delta_out="$(run_resolver "${cfg_file}")"
  assert_json_eq "AC7 a delta run carries planning (nothing asked, key left verbatim)" \
    "$(jq -c '(.carried | index("planning") != null) and (.ask | index("planning") == null)' <<<"${delta_out:-null}" 2>/dev/null)" 'true'
  all_out="$(run_resolver "${cfg_file}" --all)"
  assert_json_eq "AC7 --all re-asks planning on a config that already has it" \
    "$(jq -c '.ask | index("planning") != null' <<<"${all_out:-null}" 2>/dev/null)" 'true'

  need_prog "${Q13_PROG}" "AC7 Q13 switch program" || return
  switched="$(jq_apply "${Q13_PROG}" "${planning_cfg}" --arg autonomy interactive)"
  assert_json_eq "AC7 switching lean -> interactive writes interactive explicitly and keeps siblings" \
    "$(jq -c '.planning' <<<"${switched:-null}" 2>/dev/null)" '{"autonomy":"interactive","x":1}'
  assert_json_eq "AC7 the Q13 switch leaves every non-planning key unchanged" \
    "$(jq -c 'del(.planning)' <<<"${switched:-null}" 2>/dev/null)" '{"stack":{"backend":"go"}}'
  switched_back="$(jq_apply "${Q13_PROG}" '{"planning":{"autonomy":"interactive","x":1}}' --arg autonomy lean)"
  assert_json_eq "AC7 switching interactive -> lean writes lean and keeps siblings" \
    "$(jq -c '.planning' <<<"${switched_back:-null}" 2>/dev/null)" '{"autonomy":"lean","x":1}'
  local no_autonomy_switched
  no_autonomy_switched="$(jq_apply "${Q13_PROG}" '{"planning":{"note":"no-autonomy"}}' --arg autonomy lean)"
  assert_json_eq "AC7 a planning block without an autonomy value switches to lean and keeps siblings" \
    "$(jq -c '.planning' <<<"${no_autonomy_switched:-null}" 2>/dev/null)" '{"note":"no-autonomy","autonomy":"lean"}'

  # A delta run leaves the key verbatim: the ledger stamp never touches it.
  need_prog "${LEDGER_PROG}" "AC7 ledger program" || return
  out="$(jq_apply "${LEDGER_PROG}" "${planning_cfg}" --argjson offered '["planning"]')"
  assert_json_eq "AC7 a delta-run stamp leaves planning verbatim" \
    "$(jq -c '.planning' <<<"${out:-null}" 2>/dev/null)" '{"autonomy":"lean","x":1}'
}

case_AC8_q14_keep_leaves_serialized_blocks_identical() {
  local mono_cfg cfg_file delta_out all_out stamped
  mono_cfg='{"isMonorepo":true,"projects":[{"slug":"api","path":"packages/api","automerge":{"maxChangedFiles":3,"protectedPaths":["*auth*"]}},{"slug":"web","path":"packages/web"}]}'
  cfg_file="$(write_cfg ac8-mono "${mono_cfg}")" || { fail "AC8: fixture write failed"; return; }
  delta_out="$(run_resolver "${cfg_file}")"
  assert_json_eq "AC8 a per-project-only automerge block counts as present (not asked on a delta run)" \
    "$(jq -c '(.carried | index("automerge") != null) and (.ask | index("automerge") == null)' <<<"${delta_out:-null}" 2>/dev/null)" 'true'
  all_out="$(run_resolver "${cfg_file}" --all)"
  assert_json_eq "AC8 --all re-asks automerge even though only projects[] carries it" \
    "$(jq -c '.ask | index("automerge") != null' <<<"${all_out:-null}" 2>/dev/null)" 'true'

  need_prog "${LEDGER_PROG}" "AC8 ledger program" || return
  # "Keep current block(s)" writes nothing but the ledger stamp.
  stamped="$(jq_apply "${LEDGER_PROG}" "${mono_cfg}" --argjson offered '["automerge"]')"
  # Serialized (compact) text comparison, not parsed equality: the blocks must
  # come through the Keep path unchanged down to key order and number format.
  local kept_projects expected_projects
  kept_projects="$(jq -c '.projects' <<<"${stamped:-null}" 2>/dev/null)"
  expected_projects="$(jq -c '.projects' <<<"${mono_cfg}")"
  [[ -n "${kept_projects}" && "${kept_projects}" == "${expected_projects}" ]] \
    || fail "AC8: Keep must leave every projects[] block serialized identically (never narrowed or removed); expected ${expected_projects}, got ${kept_projects}"
  local top_cfg top_stamped kept_top expected_top
  top_cfg='{"automerge":{"maxChangedFiles":9,"protectedPaths":[".github/"],"mergeMethod":"squash"}}'
  top_stamped="$(jq_apply "${LEDGER_PROG}" "${top_cfg}" --argjson offered '["automerge"]')"
  kept_top="$(jq -c '.automerge' <<<"${top_stamped:-null}" 2>/dev/null)"
  expected_top="$(jq -c '.automerge' <<<"${top_cfg}")"
  [[ -n "${kept_top}" && "${kept_top}" == "${expected_top}" ]] \
    || fail "AC8: Keep must leave the top-level block serialized identically; expected ${expected_top}, got ${kept_top}"
}

case_AC9_q14_remove_deletes_every_block() {
  need_prog "${Q14_PROG}" "AC9 Q14 remove program" || return
  local both_cfg removed
  both_cfg='{"stack":{"backend":"dotnet10"},"automerge":{"maxChangedFiles":9},"isMonorepo":true,"projects":[{"slug":"api","path":"packages/api","automerge":{"maxChangedFiles":3}},{"slug":"web","path":"packages/web"},{"slug":"cli","path":"packages/cli","automerge":{"mergeMethod":"squash"},"babysitInterval":"5m"}],"planning":{"autonomy":"lean"}}'
  removed="$(jq_apply "${Q14_PROG}" "${both_cfg}")"
  # Guard: an empty result would make the zero-count assertion below pass on
  # the "null" fallback only by accident; fail on the empty output itself.
  [[ -n "${removed}" ]] || { fail "AC9: the Q14 remove program produced no output on the fixture"; return; }
  assert_json_eq "AC9 no automerge key survives anywhere (top-level or any projects[] entry)" \
    "$(jq -c '[.. | objects | select(has("automerge"))] | length' <<<"${removed:-null}" 2>/dev/null)" '0'
  assert_json_eq "AC9 everything other than automerge is unchanged" \
    "${removed}" \
    "$(jq -c 'del(.automerge) | .projects |= map(del(.automerge))' <<<"${both_cfg}")"
  assert_json_eq "AC9 projects[] entries and their other keys are preserved" \
    "$(jq -c '[.projects[] | .slug]' <<<"${removed:-null}" 2>/dev/null)" '["api","web","cli"]'

  local top_only removed_top per_project_only removed_pp no_block removed_none
  top_only='{"automerge":{"maxChangedFiles":2},"stack":{"backend":"go"}}'
  removed_top="$(jq_apply "${Q14_PROG}" "${top_only}")"
  assert_json_eq "AC9 a top-level-only block is removed" "${removed_top}" '{"stack":{"backend":"go"}}'
  per_project_only='{"projects":[{"slug":"a","automerge":{"maxChangedFiles":2}}]}'
  removed_pp="$(jq_apply "${Q14_PROG}" "${per_project_only}")"
  assert_json_eq "AC9 a per-project-only block is removed" "${removed_pp}" '{"projects":[{"slug":"a"}]}'
  no_block='{"stack":{"backend":"go"}}'
  removed_none="$(jq_apply "${Q14_PROG}" "${no_block}")"
  assert_json_eq "AC9 removing from a config with no block is a no-op" "${removed_none}" "${no_block}"
}

case_AC14_ledger_is_sorted_deduplicated_union() {
  need_prog "${LEDGER_PROG}" "AC14 ledger program" || return
  local out
  out="$(jq_apply "${LEDGER_PROG}" '{"configureOffered":["b","a",3]}' --argjson offered '["a","c"]')"
  assert_json_eq "AC14 existing [b,a,3] + offered [a,c] -> sorted, de-duplicated, non-strings dropped" \
    "$(jq -c '.configureOffered' <<<"${out:-null}" 2>/dev/null)" '["a","b","c"]'

  out="$(jq_apply "${LEDGER_PROG}" '{"stack":{"backend":"go"}}' --argjson offered '["z","a","z"]')"
  assert_json_eq "AC14 an absent ledger becomes the sorted offered list" \
    "$(jq -c '.configureOffered' <<<"${out:-null}" 2>/dev/null)" '["a","z"]'
  assert_json_eq "AC14 the stamp preserves every other key" \
    "$(jq -c 'del(.configureOffered)' <<<"${out:-null}" 2>/dev/null)" '{"stack":{"backend":"go"}}'

  out="$(jq_apply "${LEDGER_PROG}" '{"configureOffered":["a","b","c"]}' --argjson offered '["a"]')"
  assert_json_eq "AC14 a subset offered (the --all-style re-ask) never shrinks the ledger" \
    "$(jq -c '.configureOffered' <<<"${out:-null}" 2>/dev/null)" '["a","b","c"]'

  out="$(jq_apply "${LEDGER_PROG}" '{"stack":{"backend":"go"}}' --argjson offered '[]')"
  assert_json_eq "AC14 an empty result is still written as []" \
    "$(jq -c '.configureOffered' <<<"${out:-null}" 2>/dev/null)" '[]'

  if [[ -n "${out}" ]]; then
    printf '%s' "${out}" | jq empty >/dev/null 2>&1 || fail "AC14: ledger program output is not valid JSON"
  fi
}

case_AC15_sandbox_questions_carried_on_a_delta_run() {
  # Behavior half of AC15: with sandbox.enabled/dind/azure all present, the
  # resolver carries Q9/Q9b/Q9c, so a Dockerfile refresh has nothing to ask.
  local cfg_file out
  cfg_file="$(write_cfg ac15-complete "${COMPLETE_CFG}")" || { fail "AC15: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  assert_json_eq "AC15 sandbox.enabled/dind/azure are carried (no Q9/Q9b/Q9c prompt)" \
    "$(jq -c '[.carried[] | select(startswith("sandbox."))]' <<<"${out:-null}" 2>/dev/null)" '["sandbox.enabled","sandbox.dind","sandbox.azure"]'
  assert_json_eq "AC15 none of the sandbox questions is asked" \
    "$(jq -c '[.ask[] | select(startswith("sandbox."))]' <<<"${out:-null}" 2>/dev/null)" '[]'
}

case_B_sandbox_flags_are_per_id_not_all_carried() {
  # Premise of the per-ID sandbox-flag rule in step 6 / 5e: a config whose
  # sandbox.enabled predates questions 9b/9c carries Q9 but ASKS 9b and 9c, so
  # "Q9/Q9b/Q9c are carried" is not a valid blanket statement on a delta run.
  local predating_cfg cfg_file out
  predating_cfg="$(jq -c '.sandbox = {"enabled": true}' <<<"${COMPLETE_CFG}")"
  cfg_file="$(write_cfg b-predating "${predating_cfg}")" || { fail "B: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  assert_json_eq "B sandbox.dind and sandbox.azure are asked when their keys are absent and never offered" \
    "$(jq -c '[.ask[] | select(startswith("sandbox."))]' <<<"${out:-null}" 2>/dev/null)" '["sandbox.dind","sandbox.azure"]'
  assert_json_eq "B sandbox.enabled stays carried on the same run" \
    "$(jq -c '[.carried[] | select(startswith("sandbox."))]' <<<"${out:-null}" 2>/dev/null)" '["sandbox.enabled"]'
}

case_C_overlay_gate_rejects_what_jq_empty_accepts() {
  # Premise: an overlay that prints nothing still passes `jq empty`, so the
  # gate SKILL.md/codex.md document (exit 0 AND `jq -e 'type == "object"'`)
  # must reject it.
  local silent
  silent="$(printf '%s' '{}' | jq -c 'empty')"
  printf '%s' "${silent}" | jq empty >/dev/null 2>&1 || fail "C: test premise broken: jq empty should accept empty overlay output"
  passes_object_gate "${silent}" && fail "C: the object gate must reject an overlay that printed nothing"
  passes_object_gate '[]' && fail "C: the object gate must reject a non-object overlay result"
  passes_object_gate '{"a":1}' || fail "C: the object gate must accept an object"

  # Every shipped overlay program produces an object on a representative config.
  local out
  if need_prog "${LEDGER_PROG}" "C ledger program"; then
    out="$(jq_apply "${LEDGER_PROG}" "${COMPLETE_CFG}" --argjson offered '["x"]')"
    passes_object_gate "${out}" || fail "C: the ledger program output does not pass the object gate"
  fi
  if need_prog "${Q13_PROG}" "C Q13 program"; then
    out="$(jq_apply "${Q13_PROG}" "${COMPLETE_CFG}" --arg autonomy lean)"
    passes_object_gate "${out}" || fail "C: the Q13 program output does not pass the object gate"
  fi
  if need_prog "${Q14_PROG}" "C Q14 program"; then
    out="$(jq_apply "${Q14_PROG}" "${COMPLETE_CFG}")"
    passes_object_gate "${out}" || fail "C: the Q14 program output does not pass the object gate"
  fi
}

case_E_shape_check_rejects_unusable_resolver_results() {
  local cfg_file out bad
  cfg_file="$(write_cfg e-complete "${COMPLETE_CFG}")" || { fail "E: fixture write failed"; return; }
  out="$(run_resolver "${cfg_file}")"
  [[ -n "${out}" ]] || { fail "E: resolver produced no output on a complete config"; return; }
  passes_shape_check "${out}" || fail "E: a real resolver result must pass the documented shape check"
  for bad in '[]' '"x"' 'null' '{}' '{"ask":null,"carried":[]}' '{"ask":[],"carried":"x"}' '{"ask":{},"carried":[]}'; do
    passes_shape_check "${bad}" && fail "E: the shape check must reject the unusable result ${bad}"
  done
}

# ============================================================================
# CONTRACT cases
# ============================================================================

case_contract_frontmatter_and_arguments() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  local hint_line tools_line
  hint_line="$(frontmatter_line_raw "${skill}" "argument-hint")"
  [[ "${hint_line}" == "${ARGUMENT_HINT_LINE}" ]] || fail "SKILL.md frontmatter: expected [${ARGUMENT_HINT_LINE}], got [${hint_line}]"
  tools_line="$(frontmatter_line_raw "${skill}" "allowed-tools")"
  assert_contains "${tools_line}" "${RESOLVE_DELTA_GRANT}" "SKILL.md allowed-tools grants the resolve-delta.sh call"

  if [[ -z "${parse_body}" ]]; then
    fail "SKILL.md ${PARSE_HEADING} section body not found"
  else
    assert_contains_ws "${parse_body}" "${ARGS_ALL_RULE}" "SKILL.md Parse \$ARGUMENTS names --all as a reserved leading token"
  fi
  assert_contains_ws "${skill}" "${ARGS_SLUG_RULE}" "SKILL.md strips --all before deriving the slug"
}

case_contract_rerun_message_modes() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  assert_contains_ws "${skill}" "${RERUN_MODE_LEAD}" "SKILL.md re-run message is chosen by run mode"
  assert_contains_ws "${skill}" "${RERUN_DELTA_MESSAGE}" "SKILL.md delta-mode re-run message"
  assert_contains_ws "${skill}" "${RERUN_ALL_MESSAGE}" "SKILL.md --all-mode re-run message keeps today's wording"
}

case_contract_delta_resolution_section() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  local scripted platform delta
  scripted="$(offset_of "${skill}" '### Scripted Detection')"
  platform="$(offset_of "${skill}" '### Platform Detection')"
  delta="$(offset_of "${skill}" "${DELTA_HEADING}")"
  if [[ "${delta}" -lt 0 ]]; then
    fail "SKILL.md: ${DELTA_HEADING} subsection missing"
    return
  fi
  if ! [[ "${scripted}" -ge 0 && "${scripted}" -lt "${delta}" && "${delta}" -lt "${platform}" ]]; then
    fail "SKILL.md: ${DELTA_HEADING} must sit after Scripted Detection and before Platform Detection"
  fi
  assert_contains_ws "${delta_body}" "${DR_MODE_RULE}" "Delta Resolution run-mode rule"
  assert_contains_ws "${delta_body}" "${DR_OWN_CALL}" "Delta Resolution resolver runs as its own Bash call"
  assert_contains_ws "${delta_body}" "${DR_CALL_CMD}" "Delta Resolution names the resolver invocation"
  assert_contains_ws "${delta_body}" "${DR_MIGRATION_INPUT}" "Delta Resolution materializes migrationBase to a slug-scoped temp file"
  assert_contains_ws "${delta_body}" "${DR_ASK_RULE}" "Delta Resolution asks only delta.ask and carries delta.carried"
  assert_contains_ws "${delta_body}" "${DR_ZERO_ASK}" "Delta Resolution: an empty delta.ask asks nothing and still generates"
  assert_contains_ws "${delta_body}" "${DR_CARRIED_DRIVES_GENERATION}" "Delta Resolution: a carried answer still drives generation"
  assert_contains_ws "${delta_body}" "${DR_FALLBACK}" "Delta Resolution resolver-failure fallback to a full replay"
  assert_absent_paired_ws "${delta_body}" "${DR_FALLBACK}" "${OLD_DR_FALLBACK}" "Delta Resolution retired fallback that ignored a wrongly shaped result"
  assert_contains_ws "${delta_body}" "${DR_SHAPE_CHECK}" "Delta Resolution requires ask/carried to be arrays after valid JSON"
  assert_contains_ws "${delta_body}" "${DR_SHAPE_CHECK_EXPR}" "Delta Resolution names the jq -e shape-check expression"
  assert_contains_ws "${delta_body}" "${DR_FALLBACK_NO_REOFFER}" "Delta Resolution: a resolver-failure full run does not re-offer present planning/automerge"
  assert_contains_ws "${delta_body}" "${DR_LEGACY_ONLY_FULL}" "Delta Resolution: a legacy-only migration run is a full run"
  assert_contains_ws "${delta_body}" "${DR_LEGACY_ONLY_NEVER_CALLS}" "Delta Resolution: a legacy-only run never calls the resolver"
  assert_absent_paired_ws "${delta_body}" "${DR_MODE_RULE}" "${OLD_DR_LEGACY_INPUT}" "Delta Resolution retired legacyConfig resolver input (contradicted the run-mode rule)"
  assert_contains_ws "${delta_body}" "${DR_SCRATCH_RM_ON_FAILURE}" "Delta Resolution: the scratch input is removed whether the resolver succeeded or failed"
  assert_contains_ws "${delta_body}" "${DR_OFFERED_APPLIED_ONLY}" "Delta Resolution: an ID is recorded only for an applied or declined answer"
  assert_contains_ws "${delta_body}" "${DR_OFFERED_OVERLAY_FALLBACK}" "Delta Resolution: a fallen-back Q13/Q14 overlay drops its ID from the offered set"
  assert_contains_ws "${delta_body}" "${DR_OFFERED_Q11}" "Delta Resolution: Q11 is recorded only after a successful enroll or an explicit No"
  assert_contains_ws "${delta_body}" "${DR_OFFERED_CONDITIONAL_REASK}" "Delta Resolution: a dropped answer is re-asked only when the key is absent and the ID is not in the existing ledger"
  assert_absent_paired_ws "${delta_body}" "${DR_OFFERED_CONDITIONAL_REASK}" "${OLD_DR_OFFERED_UNCONDITIONAL}" "Delta Resolution retired unconditional 'next delta run asks it again'"
  assert_contains_ws "${delta_body}" "${DR_LEGACY_FULL_BY_DEFINITION}" "Delta Resolution: first/legacy-only/--all full runs are not resolver-failure runs"
  assert_absent_paired_ws "${delta_body}" "${DR_MODE_RULE}" "${OLD_DR_MODE_RULE_UNQUALIFIED}" "Delta Resolution retired run-mode sentence without the Q13/Q14 exception"
  assert_contains_ws "${delta_body}" "${DR_USER_CONTEXT}" "Delta Resolution: free-text context never re-opens an answered question"
  assert_contains_ws "${delta_body}" "${DR_STACK_REOFFERS}" "Delta Resolution limits stack-change re-offers"
  assert_contains_ws "${delta_body}" "${DR_CI_CARRIED}" "Delta Resolution: a carried question 8 leaves ci.yml alone"
  assert_contains_ws "${delta_body}" "${DR_UI_CARRIED}" "Delta Resolution: a carried uiConventions keeps the UI Conventions section"
  assert_absent_paired_ws "${skill}" "${DELTA_HEADING}" "${OLD_SLUG_EXAMPLE}" "SKILL.md retired 'refresh MCP servers' slug example (now a --all pointer)"
  assert_absent_paired_ws "${skill}" "${NEW_RESOLVER_WIRED}" "${OLD_RESOLVER_NOT_WIRED}" "SKILL.md retired 'resolver not wired yet' sentence"
}

case_AC1_contract_no_questions_and_stamp_only_pr() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  require_region "${step7_region}" "AC1 step 7 region" || return
  assert_contains_ws "${delta_body}" "${DR_ZERO_ASK}" "AC1 Delta Resolution: an empty delta.ask asks zero questions and still runs generation"
  assert_contains_ws "${step7_region}" "${STEP7_NOTHING_TO_COMMIT}" "AC1 step 7 still reports 'No configuration changes — nothing to commit'"
  assert_contains_ws "${step7_region}" "${STEP7_STAMP_ONLY}" "AC1 step 7: a stamp-only diff still opens a PR"
}

case_AC3_contract_dockerfile_delta_refresh() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  if [[ -z "${dockerfile_block}" ]]; then
    fail "AC3: 5e 'Merge-safe regeneration' block not found in SKILL.md"
    return
  fi
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_VALID}" "AC3 5e: valid markers on a delta run are refreshed silently, bytes outside the markers untouched"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_REPORT}" "AC3 5e: the block diff is reported via git -C <worktree-path> diff"
}

case_AC15_contract_no_q9_prompt_on_valid_markers() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  assert_contains_ws "${dockerfile_block}" 'no Q9/Q9b/Q9c prompt' "AC15 5e: the delta refresh asks no Q9/Q9b/Q9c question"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_VALID}" "AC15 5e: the full no-prompt refresh sentence"
  # Only carried answers are prompt-free: an ID in delta.ask is still asked,
  # and its answer drives this same generation (per-ID effective flags).
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_MARKERS_NEVER_ASK}" "5e: markers never cause a Q9/Q9b/Q9c prompt; only delta.ask does"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_EFFECTIVE_PER_ID}" "5e: effective sandbox flags are decided per question ID"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_EFFECTIVE_CONSUMED}" "5e: a just-answered Yes is consumed by the same generation"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_EFFECTIVE_ENABLED}" "5e: generate/refresh whenever the effective sandbox.enabled is true"
  assert_absent_paired_ws "${dockerfile_block}" "${DOCKERFILE_EFFECTIVE_PER_ID}" "${OLD_DOCKERFILE_ALL_CARRIED}" "5e retired blanket 'Q9/Q9b/Q9c are carried' claim"
  assert_absent_paired_ws "${skill}" "${DOCKERFILE_EFFECTIVE_ENABLED}" "${OLD_5E_HEADER_CARRIED}" "5e header retired 'carried sandbox.enabled' gate"
}

case_AC16_contract_dockerfile_untouched_on_delta() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  if [[ -z "${dockerfile_block}" ]]; then
    fail "AC16: 5e 'Merge-safe regeneration' block not found in SKILL.md"
    return
  fi
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_UNTOUCHED}" "AC16 5e: no/malformed markers on a delta run -> untouched and reported, no prompt"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_ALL_PROMPT}" "AC16 5e: --all/full run restores the Overwrite/Skip/Show prompt"
  assert_contains_ws "${dockerfile_block}" "${DOCKERFILE_OVERWRITE_OPTION}" "AC16 5e: the Overwrite option text survives for --all"
  assert_absent_paired_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_UNTOUCHED}" "${OLD_DOCKERFILE_NO_MARKERS_CLAIM}" "AC16 5e retired unconditional no-markers prompt"
  assert_absent_paired_ws "${dockerfile_block}" "${DOCKERFILE_DELTA_UNTOUCHED}" "${OLD_DOCKERFILE_MALFORMED_CLAIM}" "AC16 5e retired unconditional malformed-markers prompt"
}

case_AC4_contract_all_token() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  assert_contains_ws "${delta_body}" "${DR_MODE_RULE}" "AC4 Delta Resolution: --all selects the full run mode"
  assert_contains_ws "${parse_body}" "${ARGS_ALL_RULE}" "AC4 Parse \$ARGUMENTS: --all is a reserved leading token"
  assert_contains_ws "${autonomy_body}" "${Q13_ALL_REOFFER}" "AC4 Autonomy Settings: --all ignores the ledger/presence for Q13"
  assert_contains_ws "${autonomy_body}" "${Q14_ALL_REOFFER}" "AC4 Autonomy Settings: --all ignores the ledger/presence for Q14"
  assert_contains_ws "${autonomy_body}" "${Q13_Q14_FULL_NO_REOFFER}" "Autonomy Settings: a resolver-failure full run never re-offers a present planning/automerge value"
}

case_AC5_contract_carried_summary() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  require_region "${step7_region}" "AC5 step 7 region" || return
  assert_contains "${step7_region}" "${STEP7_CARRIED_HEADING}" "AC5 step 7 PR body carried-through section heading"
  assert_contains_ws "${step7_region}" "${STEP7_CARRIED_SUMMARY}" "AC5 step 7 carried-through summary lists IDs and keys only and names --all"
  assert_contains_ws "${step7_region}" "${STEP7_REPORT_SUMMARY}" "AC5 step 7 final report repeats the carried-through summary"
}

case_AC7_contract_q13_all_offer() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  if [[ -z "${autonomy_body}" ]]; then
    fail "AC7: ${AUTONOMY_HEADING} section body not found"
    return
  fi
  assert_contains_ws "${autonomy_body}" "${Q13_Q14_DELTA_RULE}" "AC7 Autonomy Settings: delta runs ask Q13/Q14 only when absent and not offered"
  assert_contains_ws "${autonomy_body}" "${Q13_ALL_REOFFER}" "AC7 Autonomy Settings: --all re-offers Q13 with the committed value pre-selected"
  assert_contains "${autonomy_body}" "${Q13_ALL_OPTION_KEEP}" "AC7 Q13 --all Keep option"
  assert_contains "${autonomy_body}" "${Q13_ALL_OPTION_SWITCH_INTERACTIVE}" "AC7 Q13 --all switch-to-interactive option"
  assert_contains "${autonomy_body}" "${Q13_ALL_OPTION_SWITCH_LEAN}" "AC7 Q13 switch-to-lean option"
  assert_contains_ws "${autonomy_body}" "${Q13_ALL_KEEP_WRITES_NOTHING}" "AC7 Q13 --all Keep writes nothing"
  assert_contains_ws "${autonomy_body}" "${Q13_ALL_SWITCH_EXPLICIT}" "AC7 Q13 --all switch to interactive is written explicitly"
  assert_absent_paired_ws "${autonomy_body}" "${Q13_Q14_DELTA_RULE}" "${OLD_UNCONDITIONAL_ABSENT_ONLY_RULE}" "AC7 retired unconditional absent-only rule"
}

case_AC8_AC9_contract_q14_all_offer() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  if [[ -z "${autonomy_body}" ]]; then
    fail "AC8/AC9: ${AUTONOMY_HEADING} section body not found"
    return
  fi
  assert_contains_ws "${autonomy_body}" "${Q14_ALL_REOFFER}" "AC8 Autonomy Settings: --all re-offers Q14 with Keep pre-selected"
  assert_contains "${autonomy_body}" "${Q14_ALL_OPTION_KEEP}" "AC8 Q14 --all Keep option"
  assert_contains "${autonomy_body}" "${Q14_ALL_OPTION_REMOVE}" "AC9 Q14 --all Remove option"
  assert_contains_ws "${autonomy_body}" "${Q14_ALL_KEEP_BYTE_IDENTICAL}" "AC8 Q14 Keep leaves every block byte-identical"
  assert_contains_ws "${autonomy_body}" "${Q14_ALL_REMOVE_SCOPE}" "AC9 Q14 Remove deletes the top-level and every projects[] automerge key"
  require_region "${step7_region}" "AC9 step 7 region" || return
  assert_contains "${step7_region}" "${STEP7_REMOVED_HEADING}" "AC9 step 7 PR body 'Review required — automerge policy removed' heading"
  assert_contains_ws "${step7_region}" "${STEP7_REMOVED_CONDITIONAL}" "AC9 step 7 removed section is conditional on a removal this run"
  assert_contains_ws "${step7_region}" "${STEP7_REMOVED_APPLIED}" "step 7 removed section is keyed to the overlay actually applied"
  assert_contains_ws "${step7_region}" "${STEP7_FINAL_REPORT_FELL_BACK}" "step 7 final report names fallen-back overlays, an unstamped ledger and a failed enroll"
  assert_absent_paired_ws "${step7_region}" "${STEP7_FINAL_REPORT_FELL_BACK}" "${OLD_STEP7_FALLBACK_UNCONDITIONAL}" "step 7 retired unconditional overlay re-ask claim"
  assert_absent_paired_ws "${step7_region}" "${STEP7_FINAL_REPORT_FELL_BACK}" "${OLD_STEP7_Q11_UNCONDITIONAL}" "step 7 retired unconditional Q11 re-ask claim"
}

case_marker_programs_sit_where_the_plan_puts_them() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  [[ -n "${Q13_PROG}" ]] || fail "SKILL.md: Q13-switch program not found between [${Q13_SWITCH_BEGIN}] / [${Q13_SWITCH_END}] inside ${AUTONOMY_HEADING}"
  [[ -n "${Q14_PROG}" ]] || fail "SKILL.md: Q14-remove program not found between [${Q14_REMOVE_BEGIN}] / [${Q14_REMOVE_END}] inside ${AUTONOMY_HEADING}"
  [[ -n "${LEDGER_PROG}" ]] || fail "SKILL.md: ledger program not found between [${LEDGER_BEGIN}] / [${LEDGER_END}]"
  local stamp_at ledger_at
  stamp_at="$(offset_of "${skill}" "${STEP6_ALWAYS_STAMP}")"
  ledger_at="$(offset_of "${skill}" "${LEDGER_BEGIN}")"
  if [[ "${stamp_at}" -lt 0 ]]; then
    fail "SKILL.md: step 6 '${STEP6_ALWAYS_STAMP}' anchor missing"
  elif [[ "${ledger_at}" -lt 0 ]]; then
    fail "SKILL.md: ledger begin marker missing, cannot check it sits after the configVersion stamp"
  elif [[ "${ledger_at}" -lt "${stamp_at}" ]]; then
    fail "SKILL.md: the ledger marker block must sit next to (after) the configVersion stamp in step 6"
  fi
}

case_AC14_contract_step6_ledger_and_overlay_order() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  require_region "${step6_region}" "AC14 step 6 region" || return
  assert_contains_ws "${step6_region}" "${STEP6_LEDGER_UNION}" "AC14 step 6: every write stamps the merged configureOffered ledger; --all never removes entries"
  assert_contains_ws "${step6_region}" "${STEP6_EMPTY_LEDGER}" "AC14 step 6: an empty ledger is still written as []"
  assert_contains_ws "${step6_region}" "${DR_OFFERED_ONLY_ASKED}" "AC14 step 6: only questions actually put to the user are recorded"
  assert_contains_ws "${step6_region}" "${STEP6_OVERLAY_ORDER}" "AC14 step 6: overlay order names the three marker programs it consumes"
  assert_contains_ws "${step6_region}" "${STEP6_OBJECT_GATE}" "step 6: an overlay must exit 0 and pass the object gate (jq empty alone is not a gate)"
  assert_contains_ws "${step6_region}" "${STEP6_GATE_TIMING}" "step 6: the gate runs after each overlay and before every write"
  assert_absent_paired_ws "${step6_region}" "${STEP6_OBJECT_GATE}" "${OLD_STEP6_JQ_EMPTY}" "step 6 retired jq-empty-only overlay validation"
  assert_contains_ws "${step6_region}" "${STEP6_SCRATCH_RM}" "step 6: the scratch file is removed even when an overlay fails"
  assert_contains_ws "${step6_region}" "${STEP6_FALLBACK_NOT_APPLIED}" "step 6: a fallen-back Q13/Q14 overlay is reported and its ID leaves the offered set"
  assert_absent_paired_ws "${step6_region}" "${STEP6_FALLBACK_NOT_APPLIED}" "${OLD_STEP6_FALLBACK_UNCONDITIONAL}" "step 6 retired unconditional 'will be asked again on the next delta run'"
  assert_contains_ws "${step6_region}" "${STEP6_FALLBACK_KEYS_STEP7}" "step 6: step 7 sections are keyed to the overlay actually applied"
  assert_contains_ws "${step6_region}" "${STEP6_LEDGER_FELL_BACK_IDS}" "step 6: the ledger offered set excludes fallen-back overlays and a failed enroll"
  assert_contains_ws "${step6_region}" "${STEP6_CARRIED_FLAGS}" "step 6: a carried Q9/Q9b/Q9c flag comes from the existing sandbox value"
  assert_contains_ws "${step6_region}" "${STEP6_ASKED_FLAGS}" "step 6: an asked sandbox ID (or an --all/full run) uses the collected answer"
  assert_contains_ws "${step6_region}" "${STEP6_EFFECTIVE_ENABLED}" "step 6: the Dockerfile is generated whenever the effective sandbox.enabled is true"
  assert_absent_paired_ws "${step6_region}" "${STEP6_CARRIED_FLAGS}" "${OLD_STEP6_CARRIED_FLAGS}" "step 6 retired blanket 'carried Q9/Q9b/Q9c flags' rule"
  assert_contains_ws "${fleet_body}" "${Q11_ENROLL_FAILURE}" "Fleet Dispatch: Q11 is recorded only after a successful enroll or an explicit No"
  assert_contains_ws "${fleet_body}" "${Q11_ENROLL_REASK_CONDITIONAL}" "Fleet Dispatch: a failed enroll is re-asked only when dispatchEnroll is not already in the existing ledger"
}

case_contract_q10_q11_board_and_fleet() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  assert_contains_ws "${board_body}" "${Q10_DECLINE_RECORDED}" "Board Config: a declined Q10 is recorded and not re-asked on a delta run"
  assert_contains_ws "${board_body}" "${Q10_CARRIED_MISSING_FILE}" "Board Config: a carried Q10 with a missing file is reported, not regenerated"
  assert_contains_ws "${board_body}" "${Q10_ANALYZER_REPORTS_ONLY}" "Board Config: the suggest-or-skip analyzer only reports on a delta run"
  assert_absent_paired_ws "${board_body}" "${Q10_DECLINE_RECORDED}" "${OLD_Q10_REASK_CLAIM}" "Board Config retired 'ask again this session' claim"

  assert_contains_ws "${fleet_body}" "${Q11_DECLINE_RECORDED}" "Fleet Dispatch: a declined Q11 is recorded and not re-asked on a delta run"
  assert_absent_paired_ws "${fleet_body}" "${Q11_DECLINE_RECORDED}" "${OLD_Q11_NOT_RECORDED}" "Fleet Dispatch retired 'a decline is not recorded anywhere' claim"
  assert_absent_paired_ws "${fleet_body}" "${Q11_DECLINE_RECORDED}" "${OLD_NEVER_REPROMPTS}" "Fleet Dispatch retired unconditional 'never re-prompts' sentence (scoped to the session-set branch)"
}

case_AC19_q12_repair_still_asked_on_delta() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  assert_contains_ws "${fleet_body}" "${Q12_REPAIR_STILL_ASKED}" "AC19 Fleet Dispatch: enrolled + empty session repair is still asked on a delta run"
  assert_contains_ws "${delta_body}" "${Q12_REPAIR_STILL_ASKED}" "AC19 Delta Resolution: Q12 repair listed as a named exception"
  assert_contains_ws "${codex}" "${CODEX_Q12}" "AC19 codex.md: Q12 repair still asked on a delta run"
}

case_restatement_sites_all_updated() {
  [[ -n "${skill}" ]] || { fail "contract: SKILL.md missing"; return; }
  local n
  # Count over whitespace-normalized text: a restatement hard-wrapped across a
  # line break must still count (assert_absent_paired_ws below is normalized too).
  n="$(count_occurrences "$(normalize_ws "${skill}")" "${RESTATE_MARKER}")"
  [[ "${n}" -ge "${RESTATE_MIN_COUNT}" ]] || fail "SKILL.md: expected the delta-scoped restatement [${RESTATE_MARKER}] at ${RESTATE_MIN_COUNT} sites (step 6 template note, planning field note, automerge field note), found ${n}"
  assert_absent_paired_ws "${skill}" "${RESTATE_MARKER}" "${OLD_PLANNING_RESTATE_A}" "SKILL.md retired planning restatement (step 6 template note)"
  assert_absent_paired_ws "${skill}" "${RESTATE_MARKER}" "${OLD_PLANNING_RESTATE_B}" "SKILL.md retired planning field restatement"
  assert_absent_paired_ws "${skill}" "${RESTATE_MARKER}" "${OLD_AUTOMERGE_RESTATE}" "SKILL.md retired automerge field restatement"
}

case_AC17_codex_parity() {
  if [[ -z "${codex}" ]]; then
    fail "AC17: codex.md missing"
    return
  fi
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_MIRROR}" "AC17 codex.md Autonomy Settings paragraph still mirrors the Claude section"
  assert_contains_ws "${codex}" "${CODEX_DELTA_MODE}" "AC17 codex.md describes delta mode"
  assert_contains_ws "${codex}" "${CODEX_ALL_TOKEN}" "AC17 codex.md describes the leading --all token"
  assert_contains_ws "${codex}" "${CODEX_Q13_Q14_ALL}" "AC17 codex.md names the Q13/Q14 --all options"
  assert_contains_ws "${codex}" "${CODEX_LEDGER}" "AC17 codex.md stamps the same configureOffered ledger"
  assert_contains_ws "${codex}" "${CODEX_SHARED_PROGRAMS}" "AC17 codex.md uses the same marker-bounded jq programs"
  assert_contains_ws "${codex}" "${CODEX_DOCKERFILE}" "AC17 codex.md describes the Dockerfile delta rule"
  assert_contains_ws "${codex}" "${CODEX_FALLBACK}" "AC17 codex.md describes the resolver-failure fallback"
  assert_contains_ws "${codex}" "${CODEX_PLAN_MODE}" "AC17 codex.md keeps the resolver read-only in /plan and writes in apply"
  assert_contains_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_DELTA}" "AC17 codex.md scopes the absent-only rule to delta runs"
  assert_absent_paired_ws "${codex}" "${CODEX_AUTONOMY_ABSENT_ONLY_DELTA}" "${OLD_CODEX_AUTONOMY_UNCONDITIONAL}" "AC17 codex.md retired unconditional absent-only claim"

  assert_contains_ws "${codex}" "${CODEX_SANDBOX_CARRIED}" "codex.md: a carried Q9/Q9b/Q9c flag is derived from the existing sandbox value, never a bare false"
  assert_contains_ws "${codex}" "${CODEX_SANDBOX_ASKED}" "codex.md: an asked sandbox ID uses the collected answer"
  assert_contains_ws "${codex}" "${CODEX_SANDBOX_EFFECTIVE}" "codex.md: the Dockerfile follows the effective sandbox.enabled"
  assert_contains_ws "${codex}" "${CODEX_OBJECT_GATE}" "codex.md: overlays must exit 0 and pass the object gate"
  assert_absent_paired_ws "${codex}" "${CODEX_OBJECT_GATE}" "${OLD_CODEX_JQ_EMPTY}" "codex.md retired jq-empty-only overlay validation"
  assert_contains_ws "${codex}" "${CODEX_SHAPE_CHECK}" "codex.md: a wrongly shaped resolver result falls back like a failure"
  assert_contains_ws "${codex}" "${CODEX_FALLBACK_NO_REOFFER}" "codex.md: a fallback full replay never re-offers a present planning/automerge value"
  assert_contains_ws "${codex}" "${CODEX_SCRATCH_RM}" "codex.md: the scratch migrationBase file is removed even on failure"
  assert_contains_ws "${codex}" "${CODEX_LEGACY_ONLY}" "codex.md: a legacy-only run is a full run that never calls the resolver"
  assert_contains_ws "${codex}" "${CODEX_OVERLAY_FELL_BACK}" "codex.md: a fallen-back Q13/Q14 overlay is reported and leaves the offered set"
  assert_absent_paired_ws "${codex}" "${CODEX_OVERLAY_FELL_BACK}" "the apply step tells the user, removes that question's ID" "codex.md retired unconditional fallen-back overlay wording"
  assert_contains_ws "${codex}" "${CODEX_Q11_ENROLL}" "codex.md: Q11 is recorded only after a successful enroll or an explicit No"
  assert_contains_ws "${codex}" "${CODEX_CHECKPOINT}" "codex.md: offered IDs and answers travel in the plan/apply checkpoint"
  assert_contains_ws "${codex}" "${CODEX_REPORT_CARRIED_SUMMARY}" "codex.md: the apply report carries the carried-through summary"
  assert_contains_ws "${codex}" "${CODEX_REPORT_DOCKERFILE}" "codex.md: the apply report carries the Dockerfile report"
  assert_contains_ws "${codex}" "${CODEX_REPORT_LAZYBOARDS_MISSING}" "codex.md: the apply report carries the carried-but-missing .lazyboards.yml notice"
  assert_contains_ws "${codex}" "${CODEX_REPORT_LEDGER_NOT_STAMPED}" "codex.md: the apply report carries the ledger-not-stamped notice"
}

case_AC17_no_cenci_cli_span_carries_all() {
  # Maintain's command-flags check flags a backtick span that starts with
  # "cenci " and carries a flag the Go CLI lacks. `--all` must therefore only
  # appear as `/cenci:configure --all` or in prose. Paired with --all being
  # documented at all, so a file that never mentions it cannot pass vacuously.
  local label content hits required
  for label in "SKILL.md" "codex.md" "docs/autonomous-loop.md" "README.md"; do
    case "${label}" in
      SKILL.md) content="${skill}"; required='/cenci:configure --all' ;;
      codex.md) content="${codex}"; required='`--all`' ;;
      docs/autonomous-loop.md) content="${loop_doc}"; required='/cenci:configure --all' ;;
      README.md) content="${readme}"; required='/cenci:configure --all' ;;
    esac
    if [[ "${content}" != *"${required}"* ]]; then
      fail "AC17 command-flags guard: ${label} never documents --all as [${required}], so the guard would pass vacuously"
      continue
    fi
    hits="$(grep -oE '`cenci [^`]*--all[^`]*`' <<<"${content}")"
    [[ -z "${hits}" ]] || fail "AC17 command-flags guard: ${label} carries a cenci CLI span with --all: ${hits}"
  done
}

case_AC18_autonomous_loop_doc() {
  if [[ -z "${loop_doc}" ]]; then
    fail "AC18: docs/autonomous-loop.md missing"
    return
  fi
  assert_contains_ws "${loop_doc}" "${LOOP_AUTOMERGE_DELTA_CLAIM}" "AC18 autonomous-loop.md section 3: delta-run absent-only claim"
  assert_contains_ws "${loop_doc}" "${LOOP_AUTOMERGE_ALL_CLAIM}" "AC18 autonomous-loop.md section 3: --all re-offer including Q14's remove option"
  assert_contains_ws "${loop_doc}" "${LOOP_PLANNING_CLAIM}" "AC18 autonomous-loop.md section 1: delta-run planning offer and --all re-offer"
  assert_absent_paired_ws "${loop_doc}" "${LOOP_SECTION3_HEADING}" "${OLD_LOOP_AUTOMERGE_UNCONDITIONAL}" "AC18 autonomous-loop.md retired unconditional automerge claim (old L280)"
  assert_absent_paired_ws "${loop_doc}" "${LOOP_PLANNING_CLAIM}" "${OLD_LOOP_PLANNING_UNCONDITIONAL}" "AC18 autonomous-loop.md retired unconditional planning claim (old L128)"
}

case_contract_readme_and_skill_authoring_docs() {
  assert_contains_ws "${readme}" "${README_DELTA_NOTE}" "flow/README.md configure row/notes describe delta re-runs and --all"
  assert_contains_ws "${readme}" "${README_CONTEXT7_MIGRATION}" "flow/README.md Context7 migration note says --all"
  assert_contains_ws "${readme}" "${README_PIN_200K}" "flow/README.md Pin-subagents note says --all"
  assert_absent_paired_ws "${readme}" "${README_CONTEXT7_MIGRATION}" "${OLD_README_CONTEXT7_MIGRATION}" "flow/README.md retired plain re-run Context7 instruction"
  assert_absent_paired_ws "${readme}" "${README_PIN_200K}" "${OLD_README_PIN_200K}" "flow/README.md retired plain re-run Pin-subagents instruction"
  assert_contains_ws "${skill_authoring}" "${SKILL_AUTHORING_UNATTENDED}" "flow/docs/skill-authoring.md malformed-marker rule has the no-prompt leave-untouched-and-report path"
  assert_absent_paired_ws "${skill_authoring}" "${SKILL_AUTHORING_UNATTENDED}" "${OLD_SKILL_AUTHORING_ALWAYS_PROMPT}" "flow/docs/skill-authoring.md retired always-prompt malformed-marker rule"
}

# --- Run every case ----------------------------------------------------------

case_AC1_zero_questions_and_serialized_stamp_noop
case_AC2_single_missing_key_asks_exactly_that_question
case_AC2_decline_chain_from_first_run
case_AC4_all_replay_asks_everything_and_stamps_the_full_list
case_AC5_resolver_output_carries_ids_and_keys_never_values
case_AC7_planning_delta_carried_and_q13_switch
case_AC8_q14_keep_leaves_serialized_blocks_identical
case_AC9_q14_remove_deletes_every_block
case_AC14_ledger_is_sorted_deduplicated_union
case_AC15_sandbox_questions_carried_on_a_delta_run
case_B_sandbox_flags_are_per_id_not_all_carried
case_C_overlay_gate_rejects_what_jq_empty_accepts
case_E_shape_check_rejects_unusable_resolver_results

case_contract_frontmatter_and_arguments
case_contract_rerun_message_modes
case_contract_delta_resolution_section
case_AC1_contract_no_questions_and_stamp_only_pr
case_AC3_contract_dockerfile_delta_refresh
case_AC15_contract_no_q9_prompt_on_valid_markers
case_AC16_contract_dockerfile_untouched_on_delta
case_AC4_contract_all_token
case_AC5_contract_carried_summary
case_AC7_contract_q13_all_offer
case_AC8_AC9_contract_q14_all_offer
case_marker_programs_sit_where_the_plan_puts_them
case_AC14_contract_step6_ledger_and_overlay_order
case_contract_q10_q11_board_and_fleet
case_AC19_q12_repair_still_asked_on_delta
case_restatement_sites_all_updated
case_AC17_codex_parity
case_AC17_no_cenci_cli_span_carries_all
case_AC18_autonomous_loop_doc
case_contract_readme_and_skill_authoring_docs

if [[ "${failures}" -gt 0 ]]; then
  echo "configure-delta-rerun.test.sh: ${failures} failure(s)." >&2
  exit 1
fi
echo "configure-delta-rerun.test.sh: all checks passed."
