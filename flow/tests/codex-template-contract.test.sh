#!/usr/bin/env bash
# Contract test for ticket #695 -- flow/templates/agents-md-codex.md is short
# repository guidance that points Codex at the installed native `$cenci:*`
# skills instead of copying the pipeline, and the configure-generated root
# AGENTS templates point at the same skills.
#
# What it enforces (every claim maps to an explicit assertion below):
#   (a) the retired copy is gone from agents-md-codex.md: `## Explicitly
#       excluded` (which wrongly called .plans/, babysit and the board
#       lifecycle Claude-only), `## Golden rules`, the numbered `### N. ...`
#       workflow steps, and the "discard by default" / "**Track** it only
#       when" finding-triage paragraphs (that rule now lives once in
#       skills/implement/codex.md);
#   (b) the native pointers are present: `$cenci:refine`, `$cenci:implement`,
#       `$cenci:review`, `$cenci:babysit`, the plugin's `docs/codex.md`, one
#       statement that the native procedures use `.plans/`, the board
#       lifecycle labels and babysit, one that the full gated ticket-to-PR flow
#       is still under native acceptance, and the report-and-stop instruction;
#       agents-md-root.md and agents-md-root-monorepo.md carry a client-neutral
#       `## Workflow` section naming both `/cenci:<skill>` and `$cenci:<skill>`
#       with no Claude-only mechanics;
#   (c) `## Critical Rules` in agents-md-codex.md has five bullets and each
#       invariant (main worktree, test first, review before a pull request,
#       one ticket = one PR, keep going / stop before destructive) occurs on
#       exactly one logical line in the WHOLE file, inside that section; the
#       stop bullet is worded exactly like the one in agents-md-root.md;
#   (d) the Claude-only terms `.claude/settings.json`, `CLAUDE_PLUGIN_ROOT`,
#       `AskUserQuestion` and hooks are absent from the whole file;
#   (e) template-stop-rule-contract.test.sh lists agents-md-codex.md in
#       `TARGETS`.
#
# Idiom of flow/tests/template-stop-rule-contract.test.sh: `set -uo
# pipefail`, a `failures` counter, fence-aware awk section extraction bounded
# to the next `## ` heading, hard-wrap-tolerant logical-line joining
# (docs/shell-scripting-gotchas.md's line-wrapping rule) so a wrapped
# sentence can neither dodge nor vacuously satisfy an assertion, and
# extractors that are pure (no fail() side effect, safe inside $(...),
# compliant with read-helper-purity-contract.test.sh) with non-vacuity
# self-tests. Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "codex-template-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "codex-template-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
CODEX_TEMPLATE="${FLOW_DIR}/templates/agents-md-codex.md"
ROOT_TEMPLATE="${FLOW_DIR}/templates/agents-md-root.md"
MONO_TEMPLATE="${FLOW_DIR}/templates/agents-md-root-monorepo.md"
STOP_RULE_TEST="${FLOW_DIR}/tests/template-stop-rule-contract.test.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# require_doc <result-var> <absolute-path> -- assigns the file's content, or
# fails closed ("" assigned) so a missing file can never satisfy an absence
# assertion vacuously. Calls fail(), so never run it via $(...).
require_doc() {
  local -n _result="$1"
  local _path="$2" _content
  if ! _content="$(cat "${_path}")"; then
    fail "not found/unreadable: ${_path#"${FLOW_DIR}/"}"
    _result=""
    return 1
  fi
  if [[ -z "${_content}" ]]; then
    fail "empty: ${_path#"${FLOW_DIR}/"}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

# extract_section_raw <content> <heading> -- pure, fence-aware extraction
# bounded to the next "## " heading. Safe inside $(...).
extract_section_raw() {
  awk -v want="$2" '
    $0 == want { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

# extract_units_raw <content> -- pure. Prints one logical unit per line: a
# heading is its own unit, a "- "/"* "/"N. " item starts a unit, and any other
# non-blank line is a hard-wrapped continuation joined (single-spaced) onto the
# unit it belongs to; a blank line ends the unit; each line inside a code
# fence is its own unit. Safe inside $(...).
extract_units_raw() {
  awk '
    function flush() { if (cur != "") print cur; cur = "" }
    /^```/ { flush(); infence = !infence; next }
    infence { print; next }
    /^#+ / { flush(); print; next }
    /^[[:space:]]*$/ { flush(); next }
    /^([-*]|[0-9]+\.) / { flush(); cur = $0; next }
    { line = $0; sub(/^[[:space:]]+/, "", line); cur = (cur == "" ? line : cur " " line) }
    END { flush() }
  ' <<<"$1"
}

# extract_bullets_raw <units> -- pure; the "- " units only.
extract_bullets_raw() {
  grep -E '^- ' <<<"$1"
}

# count_matching_raw <units> <ERE> -- pure; the number of units matching the
# case-insensitive extended regex (0 when none). Safe inside $(...).
count_matching_raw() {
  grep -ciE -- "$2" <<<"$1" || true
}

# first_unit_with_all_raw <units> <ERE>... -- pure; prints the first unit that
# matches EVERY regex (case-insensitive), so a passing match can never be
# assembled from two unrelated units. Safe inside $(...).
first_unit_with_all_raw() {
  local units="$1" unit re all
  shift
  while IFS= read -r unit; do
    all=1
    for re in "$@"; do
      grep -qiE -- "${re}" <<<"${unit}" || { all=0; break; }
    done
    if [[ "${all}" -eq 1 ]]; then
      printf '%s\n' "${unit}"
      return 0
    fi
  done <<<"${units}"
  return 0
}

# extract_stop_rule_raw <bullets> -- pure; the bullet(s) mentioning BOTH a
# force-push and "outside" (the repository): the #1178 keep-going /
# stop-before-destructive rule.
extract_stop_rule_raw() {
  grep -iE 'force[- ]push' <<<"$1" | grep -iE 'outside'
}

# extract_targets_raw <content> -- pure; the body of the `TARGETS=( ... )`
# array literal, comment-only lines dropped.
extract_targets_raw() {
  awk '
    /^TARGETS=\(/ { on = 1; next }
    on && /^\)/ { exit }
    on && !/^[[:space:]]*#/ { print }
  ' <<<"$1"
}

# --- Extractor non-vacuity self-tests ----------------------------------------
SELFTEST_DOC="$(printf '%s\n' \
  '## Before' '- before-only-literal' \
  '## Critical Rules' '- wanted-literal that is' '  hard-wrapped.' '' \
  '```bash' '## Fenced other' '- fenced-literal' '```' \
  '- after-fence-literal' \
  '## After' '- after-only-literal')"
SELFTEST_SECTION="$(extract_section_raw "${SELFTEST_DOC}" '## Critical Rules')"
grep -qF 'wanted-literal' <<<"${SELFTEST_SECTION}" || fail "section extractor self-test: dropped the section's own body"
grep -qF 'fenced-literal' <<<"${SELFTEST_SECTION}" || fail "section extractor self-test: a heading inside a code fence ended the section early"
grep -qF 'after-fence-literal' <<<"${SELFTEST_SECTION}" || fail "section extractor self-test: a different '## ' heading inside a code fence ended the section early (the literal after the fence was lost)"
grep -qF 'before-only-literal' <<<"${SELFTEST_SECTION}" && fail "section extractor self-test: captured text above the heading"
grep -qF 'after-only-literal' <<<"${SELFTEST_SECTION}" && fail "section extractor self-test: ran past the next '## ' heading"

SELFTEST_UNITS="$(extract_units_raw "$(printf '%s\n' \
  '## Heading' \
  'A paragraph that is' 'wrapped over lines.' '' \
  '- first bullet is' '  wrapped too' \
  '- second bullet' \
  '```' '- fenced line' '```')")"
[[ "$(wc -l <<<"${SELFTEST_UNITS}" | tr -d '[:space:]')" == "5" ]] || fail "unit joiner self-test: expected 5 logical units (heading, paragraph, 2 bullets, fenced line), got: ${SELFTEST_UNITS}"
grep -qxF 'A paragraph that is wrapped over lines.' <<<"${SELFTEST_UNITS}" || fail "unit joiner self-test: a wrapped paragraph was not joined onto one logical line"
grep -qxF -- '- first bullet is wrapped too' <<<"${SELFTEST_UNITS}" || fail "unit joiner self-test: a wrapped bullet was not joined onto one logical line"
[[ "$(count_matching_raw "${SELFTEST_UNITS}" 'FIRST BULLET')" == "1" ]] || fail "count self-test: case-insensitive count of a present marker is not 1"
[[ "$(count_matching_raw "${SELFTEST_UNITS}" 'absent-marker')" == "0" ]] || fail "count self-test: count of an absent marker is not 0"
[[ "$(count_matching_raw "${SELFTEST_UNITS}" 'bullet')" == "2" ]] || fail "count self-test: a marker on two units did not count as 2 (the exactly-once check would miss a restatement)"
[[ "$(extract_bullets_raw "${SELFTEST_UNITS}" | wc -l | tr -d '[:space:]')" == "3" ]] || fail "bullet filter self-test: expected the 3 '- ' units (including the fenced one)"

[[ -n "$(first_unit_with_all_raw "$(printf '%s\n' 'uses .plans/ only' 'babysit and lifecycle labels only')" '\.plans/' 'lifecycle label' 'babysit')" ]] && fail "all-of matcher self-test: assembled a match from two unrelated units"
[[ -n "$(first_unit_with_all_raw "$(printf '%s\n' 'unrelated' 'uses .plans/, the board Lifecycle Labels and babysit')" '\.plans/' 'lifecycle label' 'babysit')" ]] || fail "all-of matcher self-test: failed to match a compliant unit"

[[ -z "$(extract_stop_rule_raw "$(printf '%s\n' '- Never force-push.' '- Stay outside nothing.')")" ]] || fail "stop-rule matcher self-test: matched two unrelated bullets that each carry one phrase"
[[ -n "$(extract_stop_rule_raw '- Stop before force-pushing or changing anything outside this repository.')" ]] || fail "stop-rule matcher self-test: failed to match a compliant bullet"

SELFTEST_TARGETS="$(extract_targets_raw "$(printf '%s\n' 'x=1' 'TARGETS=(' '  # a comment naming agents-md-codex.md' '  "${FLOW_DIR}/templates/a.md"' ')' 'y=2 agents-md-codex.md')")"
grep -qF 'templates/a.md' <<<"${SELFTEST_TARGETS}" || fail "TARGETS extractor self-test: dropped an array entry"
grep -qF 'agents-md-codex.md' <<<"${SELFTEST_TARGETS}" && fail "TARGETS extractor self-test: counted a comment or text outside the array"

# --- The contract ------------------------------------------------------------
require_doc codex "${CODEX_TEMPLATE}" || true
require_doc root "${ROOT_TEMPLATE}" || true
require_doc mono "${MONO_TEMPLATE}" || true

if [[ -n "${codex}" ]]; then
  # (a) The retired copy is gone.
  for gone in '## Explicitly excluded' '## Golden rules' '### 1. Understand the ticket' '### 7. PR'; do
    grep -qxF -- "${gone}" <<<"${codex}" && fail "templates/agents-md-codex.md: retired heading still present: [${gone}]"
  done
  grep -qE '^### [0-9]+\. ' <<<"${codex}" && fail "templates/agents-md-codex.md: a numbered workflow step heading ('### N. ...') is still present"
  grep -qiF 'discard by default' <<<"${codex}" && fail "templates/agents-md-codex.md: the 'discard by default' triage paragraph is still present (the rule lives once, in skills/implement/codex.md)"
  grep -qF '**Track** it only when' <<<"${codex}" && fail "templates/agents-md-codex.md: the '**Track** it only when' triage paragraph is still present (the rule lives once, in skills/implement/codex.md)"

  # (d) No Claude-only mechanics anywhere in the file.
  for term in '.claude/settings.json' 'CLAUDE_PLUGIN_ROOT' 'AskUserQuestion'; do
    grep -qF -- "${term}" <<<"${codex}" && fail "templates/agents-md-codex.md: Claude-only term still present: [${term}]"
  done
  grep -qiwE 'hooks?' <<<"${codex}" && fail "templates/agents-md-codex.md: mentions hooks (Claude-only mechanics have no place in the Codex guidance)"

  # (b) Native pointers.
  WORKFLOW="$(extract_section_raw "${codex}" '## Workflow')"
  if [[ -z "${WORKFLOW}" ]]; then
    fail "templates/agents-md-codex.md: missing or empty '## Workflow' section"
  else
    WORKFLOW_UNITS="$(extract_units_raw "${WORKFLOW}")"
    for skill in '$cenci:refine' '$cenci:implement' '$cenci:review' '$cenci:babysit'; do
      grep -qF -- "${skill}" <<<"${WORKFLOW}" || fail "templates/agents-md-codex.md: '## Workflow' must name the native skill ${skill}"
    done
    grep -qF 'docs/codex.md' <<<"${WORKFLOW}" || fail "templates/agents-md-codex.md: '## Workflow' must point at the cenci plugin's docs/codex.md for the full list and status"
    [[ -n "$(first_unit_with_all_raw "${WORKFLOW_UNITS}" '\.plans/' 'lifecycle label' 'babysit')" ]] || fail "templates/agents-md-codex.md: '## Workflow' must state, in one statement, that the native procedures use .plans/, the board lifecycle labels and babysit"
    [[ -n "$(first_unit_with_all_raw "${WORKFLOW_UNITS}" 'ticket-to-(pr|pull request)' 'native acceptance')" ]] || fail "templates/agents-md-codex.md: '## Workflow' must state, in one statement, that the full gated ticket-to-PR flow is still under native acceptance"
    [[ -n "$(first_unit_with_all_raw "${WORKFLOW_UNITS}" 'report and stop')" ]] || fail "templates/agents-md-codex.md: '## Workflow' must say to report and stop when a native procedure is unavailable or stops"
  fi

  # (c) Critical Rules: five bullets, each invariant on exactly one logical line.
  RULES_SECTION="$(extract_section_raw "${codex}" '## Critical Rules')"
  if [[ -z "${RULES_SECTION}" ]]; then
    fail "templates/agents-md-codex.md: missing or empty '## Critical Rules' section"
  else
    ALL_UNITS="$(extract_units_raw "${codex}")"
    RULES_UNITS="$(extract_units_raw "${RULES_SECTION}")"
    RULES_BULLETS="$(extract_bullets_raw "${RULES_UNITS}")"
    BULLET_COUNT="$(count_matching_raw "${RULES_BULLETS}" '^- ')"
    [[ "${BULLET_COUNT}" == "5" ]] || fail "templates/agents-md-codex.md: '## Critical Rules' must have exactly five bullets (worktree, test-first, review before PR, one ticket = one PR, keep going / stop before destructive), got: ${BULLET_COUNT}"
    for pair in \
      'main worktree::worktree (never work in the main worktree)' \
      'test(s)? first::test-first' \
      'before opening a pull request::review before opening a pull request' \
      'one ticket (equals|=) one (pull request|pr)|1 ticket = 1 pr::one ticket = one PR' \
      'force[- ]push::keep-going / stop-before-destructive (force-push)'; do
      marker="${pair%%::*}"
      label="${pair##*::}"
      whole="$(count_matching_raw "${ALL_UNITS}" "${marker}")"
      inside="$(count_matching_raw "${RULES_BULLETS}" "${marker}")"
      [[ "${whole}" == "1" ]] || fail "templates/agents-md-codex.md: invariant '${label}' [${marker}] must occur on exactly one logical line in the whole file, found ${whole}"
      [[ "${inside}" == "1" ]] || fail "templates/agents-md-codex.md: invariant '${label}' [${marker}] must occur in exactly one '## Critical Rules' bullet, found ${inside}"
    done

    # The stop bullet is worded exactly like the one in agents-md-root.md.
    if [[ -n "${root}" ]]; then
      ROOT_STOP="$(extract_stop_rule_raw "$(extract_bullets_raw "$(extract_units_raw "$(extract_section_raw "${root}" '## Critical Rules')")")")"
      CODEX_STOP="$(extract_stop_rule_raw "${RULES_BULLETS}")"
      if [[ -z "${ROOT_STOP}" ]]; then
        fail "templates/agents-md-root.md: no stop-rule bullet to compare the Codex template against"
      elif [[ "${CODEX_STOP}" != "${ROOT_STOP}" ]]; then
        fail "templates/agents-md-codex.md: the stop bullet must use the same wording as agents-md-root.md (expected [${ROOT_STOP}], got [${CODEX_STOP}])"
      fi
    fi
  fi
fi

# (b, root templates) Client-neutral ## Workflow section.
ROOT_TEMPLATE_VARS=(root mono)
ROOT_TEMPLATE_NAMES=(agents-md-root.md agents-md-root-monorepo.md)
for idx in "${!ROOT_TEMPLATE_VARS[@]}"; do
  content_var="${ROOT_TEMPLATE_VARS[idx]}"
  content="${!content_var:-}"
  name="${ROOT_TEMPLATE_NAMES[idx]}"
  [[ -n "${content}" ]] || continue
  section="$(extract_section_raw "${content}" '## Workflow')"
  if [[ -z "${section}" ]]; then
    fail "templates/${name}: missing or empty '## Workflow' section (client-neutral pointer to the installed cenci skills)"
    continue
  fi
  grep -qF '/cenci:' <<<"${section}" || fail "templates/${name}: '## Workflow' must name /cenci:<skill> for Claude Code"
  grep -qF '$cenci:' <<<"${section}" || fail "templates/${name}: '## Workflow' must name \$cenci:<skill> for Codex"
  grep -qiE 'AskUserQuestion|CLAUDE_PLUGIN_ROOT|\.claude/|hook' <<<"${section}" && fail "templates/${name}: '## Workflow' contains Claude-only mechanics (AskUserQuestion, CLAUDE_PLUGIN_ROOT, .claude/, hooks)"
  rules_line="$(grep -nxF '## Critical Rules' <<<"${content}" | head -1 | cut -d: -f1)"
  workflow_line="$(grep -nxF '## Workflow' <<<"${content}" | head -1 | cut -d: -f1)"
  if [[ -z "${rules_line}" || -z "${workflow_line}" ]] || (( workflow_line < rules_line )); then
    fail "templates/${name}: '## Workflow' must come after '## Critical Rules'"
  fi
done

# (e) template-stop-rule-contract.test.sh covers agents-md-codex.md.
if require_doc stop_test "${STOP_RULE_TEST}"; then
  grep -qF 'templates/agents-md-codex.md' <<<"$(extract_targets_raw "${stop_test}")" || fail "tests/template-stop-rule-contract.test.sh: TARGETS must list \${FLOW_DIR}/templates/agents-md-codex.md"
fi

if (( failures > 0 )); then
  echo "codex-template-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "codex-template-contract.test.sh: all checks passed"
