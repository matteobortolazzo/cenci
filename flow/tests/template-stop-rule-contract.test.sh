#!/usr/bin/env bash
# Contract test for ticket #1178: every generated root CLAUDE.md/AGENTS.md
# template -- and this repo's own root AGENTS.md, which eats its own
# templates' rules -- carries a "keep going / stop before destructive"
# Critical Rule: proceed without asking when a step needs no user input, and
# stop only when blocked on the user or before something destructive
# (deleting data, force-pushing, changing anything outside the repository).
#
# Fixture-light, grep-based idiom of flow/tests/ci-status-read-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, fence-aware awk section extraction
# bounded to the next `## ` heading, and hard-wrap-tolerant bullet joining
# (docs/shell-scripting-gotchas.md's line-wrapping rule) so a wrapped bullet
# can neither dodge nor vacuously satisfy the assertion. Every extractor is
# named `extract_*`, is pure, and has no fail() side effect, so it is safe
# inside $(...) and compliant with read-helper-purity-contract.test.sh.
# Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "template-stop-rule-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "template-stop-rule-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "template-stop-rule-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

SECTION_HEADING='## Critical Rules'

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

# extract_bullets_raw <section> -- pure; prints one logical bullet per line,
# joining hard-wrapped continuation lines (any non-blank line that does not
# itself start a new "- " bullet) onto the bullet they belong to. Safe
# inside $(...).
extract_bullets_raw() {
  awk '
    /^- / { if (cur != "") print cur; cur = $0; next }
    /^[[:space:]]*$/ { next }
    cur != "" { sub(/^[[:space:]]+/, ""); cur = cur " " $0 }
    END { if (cur != "") print cur }
  ' <<<"$1"
}

# extract_stop_rule_raw <bullets> -- pure; prints the bullet(s) that mention
# BOTH a force-push and "outside" (the repository), i.e. the destructive
# boundary the rule draws. Both phrases must sit on the same logical bullet
# so a passing match can never be assembled from two unrelated rules.
extract_stop_rule_raw() {
  grep -iE 'force[- ]push' <<<"$1" | grep -iE 'outside'
}

# --- Extractor non-vacuity self-tests ----------------------------------------
# (docs/shell-scripting-gotchas.md's awk-section-boundary rule.)
SELFTEST_DOC="$(printf '%s\n' \
  '## Before' '- before-only-literal' \
  "${SECTION_HEADING}" '- wanted-literal' '```bash' "${SECTION_HEADING}" '- fenced-literal' '```' \
  '## After' '- after-only-literal')"
SELFTEST_SECTION="$(extract_section_raw "${SELFTEST_DOC}" "${SECTION_HEADING}")"
grep -qF 'wanted-literal' <<<"${SELFTEST_SECTION}" || fail "extractor self-test: dropped the section's own body"
grep -qF 'fenced-literal' <<<"${SELFTEST_SECTION}" || fail "extractor self-test: a heading inside a code fence ended the section early"
grep -qF 'before-only-literal' <<<"${SELFTEST_SECTION}" && fail "extractor self-test: captured text above the heading"
grep -qF 'after-only-literal' <<<"${SELFTEST_SECTION}" && fail "extractor self-test: ran past the next '## ' heading"

# Bullet joiner: a hard-wrapped bullet must become one logical line, and the
# stop-rule matcher must reject two separate bullets that each carry only
# one of the two phrases.
SELFTEST_BULLETS="$(extract_bullets_raw "$(printf '%s\n' \
  '- Never force-push to' '  shared branches.' \
  '' \
  '- Stay inside the repo.')")"
[[ "$(wc -l <<<"${SELFTEST_BULLETS}" | tr -d '[:space:]')" == "2" ]] || fail "bullet-joiner self-test: expected 2 logical bullets, got: ${SELFTEST_BULLETS}"
grep -qF -- '- Never force-push to shared branches.' <<<"${SELFTEST_BULLETS}" || fail "bullet-joiner self-test: continuation line was not joined onto its bullet"
[[ -z "$(extract_stop_rule_raw "${SELFTEST_BULLETS}")" ]] || fail "stop-rule matcher self-test: matched two unrelated bullets that each carry one phrase"
[[ -n "$(extract_stop_rule_raw '- Stop before force pushing or changing anything outside the repository.')" ]] || fail "stop-rule matcher self-test: failed to match a compliant bullet (space-separated force push)"
[[ -n "$(extract_stop_rule_raw '- Stop before force-pushing or changing anything outside the repository.')" ]] || fail "stop-rule matcher self-test: failed to match a compliant bullet (hyphenated force-push)"

# --- The contract: each template and the root AGENTS.md carry the rule ------
TARGETS=(
  "${FLOW_DIR}/templates/claude-md-root.md"
  "${FLOW_DIR}/templates/claude-md-root-monorepo.md"
  "${FLOW_DIR}/templates/agents-md-root.md"
  "${FLOW_DIR}/templates/agents-md-root-monorepo.md"
  "${REPO_ROOT}/AGENTS.md"
)

for target in "${TARGETS[@]}"; do
  rel="${target#"${REPO_ROOT}/"}"
  if ! CONTENT="$(cat "${target}" 2>/dev/null)"; then
    fail "${rel}: not found/unreadable"
    continue
  fi
  SECTION="$(extract_section_raw "${CONTENT}" "${SECTION_HEADING}")"
  if [[ -z "${SECTION}" ]]; then
    fail "${rel}: missing or empty '${SECTION_HEADING}' section"
    continue
  fi
  BULLETS="$(extract_bullets_raw "${SECTION}")"
  RULE="$(extract_stop_rule_raw "${BULLETS}")"
  if [[ -z "${RULE}" ]]; then
    fail "${rel}: Critical Rules has no bullet mentioning both force-push(ing) and anything 'outside' the repository (#1178 keep-going / stop-before-destructive rule)"
    continue
  fi
  # The same bullet must also carry the keep-going half and the data-loss
  # half, so the rule is not reduced to a bare force-push prohibition.
  grep -qiE 'keep going|keep working|continue' <<<"${RULE}" || fail "${rel}: the stop rule must tell the agent to keep going when a step needs no user input"
  grep -qiE 'delet' <<<"${RULE}" || fail "${rel}: the stop rule must name deleting data as destructive"
  grep -qiE 'destructive' <<<"${RULE}" || fail "${rel}: the stop rule must frame the stop condition as 'destructive'"
done

if (( failures > 0 )); then
  echo "template-stop-rule-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "template-stop-rule-contract.test.sh: all checks passed"
