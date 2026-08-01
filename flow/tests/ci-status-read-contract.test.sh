#!/usr/bin/env bash
# Contract test for ticket #900: an agent must never report a PR as
# "CI green" while a check is still queued or running.
#
# The regression this guards: `gh pr view --json statusCheckRollup` leaves
# `conclusion` EMPTY for a queued/running check, so a conclusion-only scan
# reads "still running" as "passed". `cenci babysit` already classifies
# strictly in Go (automergeCIHold's pass-only bucket switch, #854), but that
# logic is invisible to an agent answering "is CI green?" in conversation or
# inside a skill phase -- so the canonical read has to be written down where
# every workflow skill already looks: the shared `shell-rules` skill.
#
# Three lanes must stay in sync, and this suite asserts all three:
#   1. flow/skills/shell-rules/SKILL.md -- the canonical "Reading PR CI
#      Status" section (command shape, bucket vocabulary, the empty-
#      `conclusion` trap, zero-checks, name-the-pending-checks, exit 8).
#   2. <repo-root>/AGENTS.md -- a Critical Rule pointing at it, so a
#      top-level agent working in this repo carries the rule without
#      loading a skill.
#   3. watch/internal/babysit/automerge.go -- the Go classifier whose
#      bucket vocabulary the doc mirrors; if a bucket name changes there,
#      the doc must change with it.
#
# Plus a repo-wide sweep (case 4) so no skill or agent doc reintroduces a
# `conclusion`/`statusCheckRollup`-based CI read, with a fixture self-test
# proving the sweep actually catches a violating file.
#
# Fixture-light, grep-based idiom of flow/tests/escalation-anchor-contract.test.sh:
# `set -uo pipefail`, a `failures` counter, `grep -qF` substring assertions,
# fence-aware awk section extraction bounded to the next `## ` heading. Every
# extractor here is named `extract_*`, is pure, and has no fail() side effect,
# so this file is compliant with flow/tests/read-helper-purity-contract.test.sh's
# repo-wide scan. Auto-discovered by scripts/run-checks.sh's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "ci-status-read-contract.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "ci-status-read-contract.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "ci-status-read-contract.test.sh: failed to resolve repo root." >&2; exit 2; }
SHELL_RULES="${FLOW_DIR}/skills/shell-rules/SKILL.md"
ROOT_AGENTS="${REPO_ROOT}/AGENTS.md"
AUTOMERGE_GO="${REPO_ROOT}/watch/internal/babysit/automerge.go"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# assert_contains <haystack> <needle> <description> -- pure aside from
# fail(); never invoked inside $(...).
assert_contains() {
  [[ -n "$2" ]] || { fail "$3: empty needle"; return; }
  grep -qF -- "$2" <<<"$1" || fail "$3 (expected to contain: $2)"
}

# extract_section_raw <content> <heading-line> -- pure, fence-aware
# extraction bounded to the next "## " heading. Safe inside $(...): no
# fail() side effect.
extract_section_raw() {
  awk -v want="$2" '
    $0 == want { on=1; next }
    /^```/ { infence = !infence; if (on) print; next }
    on && !infence && /^## / { exit }
    on { print }
  ' <<<"$1"
}

SECTION_HEADING='## Reading PR CI Status'

# Extractor non-vacuity self-test (docs/shell-scripting-gotchas.md's
# awk-section-boundary rule): prove extract_section_raw is fence-aware and
# stops at the next real `## ` heading, so a passing case-1 assertion can
# never come from an over-captured window that swept in a neighbouring
# section's text.
SELFTEST_DOC="$(printf '%s\n' \
  '## Before' 'before-only-literal' \
  "${SECTION_HEADING}" 'wanted-literal' '```bash' "${SECTION_HEADING}" 'fenced-literal' '```' \
  '## After' 'after-only-literal')"
SELFTEST_SECTION="$(extract_section_raw "${SELFTEST_DOC}" "${SECTION_HEADING}")"
grep -qF 'wanted-literal' <<<"${SELFTEST_SECTION}" || fail "extractor self-test: dropped the section's own body"
grep -qF 'fenced-literal' <<<"${SELFTEST_SECTION}" || fail "extractor self-test: a heading inside a code fence ended the section early"
grep -qF 'before-only-literal' <<<"${SELFTEST_SECTION}" && fail "extractor self-test: captured text above the heading"
grep -qF 'after-only-literal' <<<"${SELFTEST_SECTION}" && fail "extractor self-test: ran past the next '## ' heading"

if ! SHELL_RULES_CONTENT="$(cat "${SHELL_RULES}" 2>/dev/null)"; then
  fail "shell-rules/SKILL.md: not found/unreadable: ${SHELL_RULES}"
  SHELL_RULES_CONTENT=""
fi
if ! ROOT_AGENTS_CONTENT="$(cat "${ROOT_AGENTS}" 2>/dev/null)"; then
  fail "AGENTS.md: not found/unreadable: ${ROOT_AGENTS}"
  ROOT_AGENTS_CONTENT=""
fi
if ! AUTOMERGE_CONTENT="$(cat "${AUTOMERGE_GO}" 2>/dev/null)"; then
  fail "automerge.go: not found/unreadable: ${AUTOMERGE_GO}"
  AUTOMERGE_CONTENT=""
fi

# --- Case 1: shell-rules carries the canonical read --------------------------
CI_SECTION="$(extract_section_raw "${SHELL_RULES_CONTENT}" "${SECTION_HEADING}")"
if [[ -z "${CI_SECTION}" ]]; then
  fail "shell-rules/SKILL.md: missing or empty '${SECTION_HEADING}' section"
else
  # The command shape agents must actually run.
  assert_contains "${CI_SECTION}" 'gh pr checks' "shell-rules CI section: names the gh pr checks command"
  assert_contains "${CI_SECTION}" '--json bucket,name,state' "shell-rules CI section: reads the bucket field, not conclusions"

  # The trap that produced #900: an empty conclusion is "still running".
  assert_contains "${CI_SECTION}" 'statusCheckRollup' "shell-rules CI section: names the statusCheckRollup anti-pattern"
  assert_contains "${CI_SECTION}" 'conclusion' "shell-rules CI section: names the conclusion field as the trap"
  grep -qiE 'empty|blank' <<<"${CI_SECTION}" || fail "shell-rules CI section: must say an empty/blank conclusion means still running"

  # Green is pass-only, and it needs at least one check.
  grep -qiE 'every check' <<<"${CI_SECTION}" || fail "shell-rules CI section: must require EVERY check to be pass"
  grep -qiE 'zero checks|no checks' <<<"${CI_SECTION}" || fail "shell-rules CI section: must cover the zero-checks case"

  # A pending report has to be actionable: name the checks still running.
  grep -qiE 'name (the|those|them)|by name' <<<"${CI_SECTION}" || fail "shell-rules CI section: must require naming the pending checks"

  # `gh pr checks` exits 8 while checks are pending and still emits valid
  # JSON -- a nonzero exit is not itself a CI-failure signal.
  assert_contains "${CI_SECTION}" 'exit' "shell-rules CI section: covers gh pr checks' exit behavior"
  assert_contains "${CI_SECTION}" '8' "shell-rules CI section: names exit code 8 (pending)"
fi

# --- Case 2: the root AGENTS.md Critical Rule --------------------------------
CRITICAL_RULES="$(extract_section_raw "${ROOT_AGENTS_CONTENT}" '## Critical Rules')"
if [[ -z "${CRITICAL_RULES}" ]]; then
  fail "AGENTS.md: missing or empty '## Critical Rules' section"
else
  assert_contains "${CRITICAL_RULES}" 'gh pr checks' "AGENTS.md Critical Rules: rule names the bucket-based command"
  grep -qiE 'green' <<<"${CRITICAL_RULES}" || fail "AGENTS.md Critical Rules: rule must speak about reporting CI as green"
  assert_contains "${CRITICAL_RULES}" 'conclusion' "AGENTS.md Critical Rules: rule names the conclusion trap"
fi

# --- Case 3: bucket vocabulary parity with the Go classifier -----------------
# Every bucket the doc enumerates must be a real case in automergeCIHold's
# switch, so agent-reported status and supervisor behavior cannot drift.
for bucket in pass fail pending skipping cancel; do
  assert_contains "${AUTOMERGE_CONTENT}" "case \"${bucket}\":" "automerge.go: bucket '${bucket}' is a real classifier case"
  if [[ -n "${CI_SECTION}" ]]; then
    assert_contains "${CI_SECTION}" "\`${bucket}\`" "shell-rules CI section: enumerates the '${bucket}' bucket"
  fi
done

# --- Case 4: repo-wide sweep for conclusion-based CI reads -------------------
# scan_conclusion_reads <root> <exclude-relative-path> -- pure; prints one
# "<path>:<line>" per offending line, nothing when clean. Safe inside $(...).
# shell-rules/SKILL.md is excluded because it is the one file that names the
# anti-pattern in order to forbid it.
scan_conclusion_reads() {
  local root="$1" exclude="$2" f rel
  while IFS= read -r -d '' f; do
    rel="${f#"${root}/"}"
    [[ "${rel}" == "${exclude}" ]] && continue
    grep -nE 'statusCheckRollup|--json[^|]*conclusion' -- "${f}" 2>/dev/null | while IFS= read -r hit; do
      printf '%s:%s\n' "${rel}" "${hit%%:*}"
    done
  done < <(find "${root}" -type f -name '*.md' -print0 2>/dev/null | sort -z)
}

SKILLS_EXCLUDE='skills/shell-rules/SKILL.md'
SWEEP_HITS="$(scan_conclusion_reads "${FLOW_DIR}" "${SKILLS_EXCLUDE}")"
if [[ -n "${SWEEP_HITS}" ]]; then
  while IFS= read -r hit; do
    fail "flow doc derives CI status from a conclusion/statusCheckRollup read: ${hit}"
  done <<<"${SWEEP_HITS}"
fi

# Fixture self-test: the sweep must actually catch a violating doc, and must
# honour its exclusion. Without this, an over-narrow regex would report a
# false green forever.
if FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/ci-status-sweep-XXXXXX")"; then
  mkdir -p "${FIXTURE_ROOT}/skills/shell-rules"
  printf 'Check CI with gh pr view --json statusCheckRollup and read conclusion.\n' > "${FIXTURE_ROOT}/violating.md"
  printf 'Never derive green from statusCheckRollup conclusions.\n' > "${FIXTURE_ROOT}/skills/shell-rules/SKILL.md"
  printf 'Use gh pr checks --json bucket,name,state.\n' > "${FIXTURE_ROOT}/clean.md"
  FIXTURE_HITS="$(scan_conclusion_reads "${FIXTURE_ROOT}" "${SKILLS_EXCLUDE}")"
  grep -qF 'violating.md' <<<"${FIXTURE_HITS}" || fail "sweep self-test: failed to flag a violating doc"
  grep -qF 'clean.md' <<<"${FIXTURE_HITS}" && fail "sweep self-test: flagged a compliant doc"
  grep -qF 'shell-rules' <<<"${FIXTURE_HITS}" && fail "sweep self-test: excluded path was still flagged"
  rm -rf "${FIXTURE_ROOT}"
else
  fail "sweep self-test: could not create fixture directory"
fi

if (( failures > 0 )); then
  echo "ci-status-read-contract.test.sh: ${failures} failure(s)" >&2
  exit 1
fi
echo "ci-status-read-contract.test.sh: all checks passed"
