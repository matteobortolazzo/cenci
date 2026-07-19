#!/usr/bin/env bash
# Integration tests for flow/skills/maintain/scripts/check.sh — the deterministic,
# LLM-free repo checker for ticket #530. Follows the fixture-driven idiom of
# flow/hooks/scripts/run-gate.test.sh: mktemp -d synthetic repos, a `failures=`
# counter, self-contained, auto-discovered by the flow gate's `*.test.sh` glob.
#
# check.sh does not exist yet (RED phase) — every case below is expected to fail
# until it is implemented. This file pins the contract the implementation must
# satisfy (JSON schema is fixed by the plan; the CLI surface below is this test
# suite's concrete proposal for the parts the plan left open):
#
#   Modes:
#     check.sh                          full repo check (default)
#     check.sh --changed <file> ...      only checks relevant to the given
#                                        repo-root-relative paths; unrelated
#                                        context-budget breaches downgrade to
#                                        "warn"
#     check.sh --strict                 full check; any "warn" also fails CI
#     check.sh --write                  regenerate marker-bounded generated
#                                        sections in place from canonical
#                                        sources (never touches content
#                                        outside a marker pair); fails closed
#                                        (no mutation, non-zero exit, actionable
#                                        stderr) on malformed/missing/duplicate
#                                        marker pairs — see docs/skill-authoring.md
#
#   Output: concise text to stdout (one line per non-pass result:
#     "FAIL/WARN/SKIP <check> <target>: <message> -> fix: <fix>", plus a
#     summary line) and a JSON report written to
#     "<repo-root>/.cenci/maintain-report.json" per the plan's schema:
#     { "summary": {pass,warn,fail,skip,mode}, "results": [{check,target,status,message,fix}] }
#
#   Exit codes: 0 unless a "fail" result exists (default/--changed); --strict
#     additionally fails on any "warn". "skip" never blocks.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CHECK="${FLOW_DIR}/skills/maintain/scripts/check.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain: $2 (actual: $1)"; }

# --- JSON report helpers --------------------------------------------------
# REPORT_JSON / REPORT_TEXT / REPORT_CODE are populated by run_check.

run_check() {
  local root="$1"; shift
  local json_path="${root}/.cenci/maintain-report.json"
  rm -f "${json_path}"
  REPORT_TEXT="$(cd "${root}" && bash "${CHECK}" "$@" 2>&1)"
  REPORT_CODE=$?
  if [[ -f "${json_path}" ]]; then
    REPORT_JSON="$(cat "${json_path}")"
  else
    REPORT_JSON="null"
  fi
}

# count_results <check-id> <status> — number of results matching both.
count_results() {
  printf '%s' "${REPORT_JSON}" | jq --arg c "$1" --arg s "$2" \
    '[.results[]? | select(.check==$c and .status==$s)] | length' 2>/dev/null
}

is_ge1() { [[ "${1:-}" =~ ^[0-9]+$ ]] && [[ "$1" -ge 1 ]]; }
is_eq0() { [[ "${1:-}" =~ ^[0-9]+$ ]] && [[ "$1" -eq 0 ]]; }

assert_has_result() {
  local check="$1" status="$2" label="$3"
  local n; n="$(count_results "${check}" "${status}")"
  is_ge1 "${n}" || fail "${label}: expected >=1 '${check}' result with status=${status}, got '${n:-N/A}' (report: ${REPORT_JSON})"
}

assert_no_result() {
  local check="$1" label="$2"
  local n
  n="$(printf '%s' "${REPORT_JSON}" | jq --arg c "$1" '[.results[]? | select(.check==$c)] | length' 2>/dev/null)"
  is_eq0 "${n}" || fail "${label}: expected 0 results for '${check}' (narrowed-out category), got '${n:-N/A}'"
}

assert_all_fixes_present() {
  local label="$1"
  local n
  n="$(printf '%s' "${REPORT_JSON}" | jq \
    '[.results[]? | select((.status=="fail" or .status=="warn") and ((.fix // "")==""))] | length' 2>/dev/null)"
  is_eq0 "${n}" || fail "${label}: found fail/warn result(s) with empty fix (count='${n:-N/A}')"
}

assert_clean_report() {
  local label="$1"
  local n
  n="$(printf '%s' "${REPORT_JSON}" | jq '[.results[]? | select(.status=="fail" or .status=="warn")] | length' 2>/dev/null)"
  is_eq0 "${n}" || fail "${label}: expected a fully clean report (no fail/warn), got '${n:-N/A}'"
}

assert_exit_zero() { [[ "${REPORT_CODE}" -eq 0 ]] || fail "$1: expected exit 0, got ${REPORT_CODE}"; }
assert_exit_nonzero() { [[ "${REPORT_CODE}" -ne 0 ]] || fail "$1: expected non-zero exit, got 0"; }

# strip_marker_bodies — reads a file on stdin, prints it back with all lines
# strictly between a "cenci-maintain:<id>:start" and its matching ":end"
# marker removed (marker lines themselves are kept). Used to assert marker
# replacement only ever touches marker-bounded spans, without pinning the
# exact generated table format check.sh chooses.
strip_marker_bodies() {
  awk '
    /<!-- cenci-maintain:[a-zA-Z0-9_-]+:start -->/ { print; skip=1; next }
    /<!-- cenci-maintain:[a-zA-Z0-9_-]+:end -->/ { skip=0; print; next }
    skip { next }
    { print }
  '
}

# --- Base fixture: a repo that should pass every check cleanly -----------
setup_base() {
  local root="$1"
  mkdir -p "${root}/.cenci"
  git -C "${root}" init -q
  git -C "${root}" config user.email test@example.com
  git -C "${root}" config user.name "Test"

  cat > "${root}/.gitignore" <<'EOF'
.worktrees/
EOF

  cat > "${root}/AGENTS.md" <<'EOF'
# demo-repo

## Critical Rules
- Rule one.
- Rule two.
- Rule three.
EOF

  cat > "${root}/CLAUDE.md" <<'EOF'
@AGENTS.md
EOF

  cat > "${root}/.cenci/config.json" <<'EOF'
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow", "gateCommand": "true" }
  ]
}
EOF

  mkdir -p "${root}/flow/docs"
  cat > "${root}/flow/AGENTS.md" <<'EOF'
# Project: flow

## Critical Rules
- Flow rule one.
- Flow rule two.

## Reference Docs
- `docs/sample-topic.md` — sample topic doc.
EOF

  cat > "${root}/flow/CLAUDE.md" <<'EOF'
@AGENTS.md
EOF

  cat > "${root}/flow/docs/sample-topic.md" <<'EOF'
# Sample topic

## Rules
- Sample rule one.
- Sample rule two.
EOF

  mkdir -p "${root}/flow/skills/demo/scripts"
  cat > "${root}/flow/skills/demo/SKILL.md" <<'EOF'
---
name: demo
description: "Demo skill for maintain checker fixtures."
user-invocable: true
codex-support: yes
---

Uses `scripts/helper.sh` for local demo logic and delegates to the `demo-agent` agent.
EOF

  cat > "${root}/flow/skills/demo/scripts/helper.sh" <<'EOF'
#!/usr/bin/env bash
echo "helper"
EOF

  mkdir -p "${root}/flow/agents"
  cat > "${root}/flow/agents/demo-agent.md" <<'EOF'
---
name: demo-agent
description: "Demo agent for maintain checker fixtures."
---

Referenced by the `demo` skill only.
EOF

  mkdir -p "${root}/flow/opencode"
  cat > "${root}/flow/opencode/install-skills.sh" <<'EOF'
#!/bin/sh
# Portable skills installed as real symlinks for OpenCode.
# Drift guard: flow/skills/maintain/scripts/check.sh's capability-table check
# keeps this list in sync with flow/README.md's generated skill inventory.
PORTABLE_SKILLS="demo"
EOF

  mkdir -p "${root}/flow/tests"
  cat > "${root}/flow/tests/sample.test.sh" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
echo "sample.test.sh: failures=0"
exit 0
EOF

  cat > "${root}/flow/README.md" <<'EOF'
# flow

Intro prose kept outside every generated marker.

<!-- cenci-maintain:skills:start -->
<!-- cenci-maintain:skills:end -->

<!-- cenci-maintain:agents:start -->
<!-- cenci-maintain:agents:end -->

<!-- cenci-maintain:workflow-deps:start -->
<!-- cenci-maintain:workflow-deps:end -->

<!-- cenci-maintain:docs-nav:start -->
<!-- cenci-maintain:docs-nav:end -->

Trailing prose kept outside every generated marker.
EOF
}

bootstrap_markers() {
  run_check "$1" --write
}

# =====================================================================
# Case 1: a valid synthetic repo passes cleanly (bootstrap, then validate)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
assert_exit_zero "case1 bootstrap (--write)"
run_check "${ROOT}"
assert_exit_zero "case1 valid repo clean pass"
assert_clean_report "case1 valid repo clean pass"
assert_eq "$(printf '%s' "${REPORT_JSON}" | jq -r '.summary.mode')" "full" "case1 summary.mode"
assert_eq "$(printf '%s' "${REPORT_JSON}" | jq -r '.summary.fail')" "0" "case1 summary.fail"
assert_eq "$(printf '%s' "${REPORT_JSON}" | jq -r '.summary.warn')" "0" "case1 summary.warn"
assert_has_result "github-labels" "skip" "case1 github-labels must skip offline, not silently pass"
rm -rf "${ROOT}"

# =====================================================================
# Case 2: front-matter — missing required 'name' field fails; a
# colon-containing quoted description still parses cleanly (Risk mitigation)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/skills/demo/SKILL.md" <<'EOF'
---
description: "Demo skill for maintain checker fixtures."
user-invocable: true
---

Uses `scripts/helper.sh` for local demo logic and delegates to the `demo-agent` agent.
EOF
run_check "${ROOT}"
assert_has_result "front-matter" "fail" "case2 missing name in front matter"
assert_all_fixes_present "case2 front-matter fail must carry a fix"
assert_contains "${REPORT_TEXT}" "FAIL front-matter" "case2 text output line"
assert_contains "${REPORT_TEXT}" "fix:" "case2 text output fix pointer"
assert_exit_nonzero "case2 default mode exits non-zero on fail"
rm -rf "${ROOT}"

ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/skills/demo/SKILL.md" <<'EOF'
---
name: demo
description: "Demo: colon-containing, quoted description for parser robustness."
user-invocable: true
codex-support: yes
---

Uses `scripts/helper.sh` for local demo logic and delegates to the `demo-agent` agent.
EOF
run_check "${ROOT}"
n_fm_fail="$(count_results "front-matter" "fail")"
is_eq0 "${n_fm_fail}" || fail "case2b: quoted colon-containing description must not fail front-matter parsing (got ${n_fm_fail})"
rm -rf "${ROOT}"

# =====================================================================
# Case 3: duplicate-names — two skills declaring the same front-matter name
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/flow/skills/demo-dup"
cat > "${ROOT}/flow/skills/demo-dup/SKILL.md" <<'EOF'
---
name: demo
description: "Duplicate name of the demo skill."
user-invocable: true
---

Deliberately duplicates the `demo` skill's front-matter name.
EOF
run_check "${ROOT}"
assert_has_result "duplicate-names" "fail" "case3 duplicate skill name"
assert_all_fixes_present "case3 duplicate-names fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 4: broken-refs — a skill references a path that does not exist
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/skills/demo/SKILL.md" <<'EOF'
---
name: demo
description: "Demo skill for maintain checker fixtures."
user-invocable: true
codex-support: yes
---

Uses `scripts/helper.sh` and `scripts/does-not-exist.sh` for local demo logic,
and delegates to the `demo-agent` agent.
EOF
run_check "${ROOT}"
assert_has_result "broken-refs" "fail" "case4 broken script reference"
assert_all_fixes_present "case4 broken-refs fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 5: orphan-files — a script not referenced by any skill/agent/doc
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/skills/demo/scripts/orphan.sh" <<'EOF'
#!/usr/bin/env bash
echo "nobody calls me"
EOF
run_check "${ROOT}"
assert_has_result "orphan-files" "fail" "case5 orphan script"
assert_all_fixes_present "case5 orphan-files fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 6: instruction-docs — a configured project's instruction file is missing
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
rm -f "${ROOT}/flow/AGENTS.md"
run_check "${ROOT}"
assert_has_result "instruction-docs" "fail" "case6 missing flow/AGENTS.md"
assert_all_fixes_present "case6 instruction-docs fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 7: topic-docs — a referenced topic doc file is missing (dangling ref)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
rm -f "${ROOT}/flow/docs/sample-topic.md"
run_check "${ROOT}"
assert_has_result "topic-docs" "fail" "case7 dangling docs/sample-topic.md reference"
assert_all_fixes_present "case7 topic-docs fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 7b: topic-docs — a doc file exists under flow/docs/ but is not
# referenced by flow/AGENTS.md's Reference Docs list (orphan direction)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/docs/unreferenced-topic.md" <<'EOF'
# Unreferenced topic

Not referenced by flow/AGENTS.md's Reference Docs list.
EOF
run_check "${ROOT}"
assert_has_result "topic-docs" "fail" "case7b orphan topic doc not referenced by any Reference Docs list"
assert_all_fixes_present "case7b topic-docs fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 8: invalid-json — .cenci/config.json is not valid JSON
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
printf '{ "isMonorepo": true, oops' > "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_has_result "invalid-json" "fail" "case8 malformed config.json"
assert_all_fixes_present "case8 invalid-json fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 9: shell-syntax — a shell script fails `bash -n`
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/opencode/install-skills.sh" <<'EOF'
#!/bin/sh
PORTABLE_SKILLS="demo
EOF
run_check "${ROOT}"
assert_has_result "shell-syntax" "fail" "case9 unmatched quote in install-skills.sh"
assert_all_fixes_present "case9 shell-syntax fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 10: stale-generated — a canonical fact changes without regenerating
# the marker-bounded section that depends on it
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
sed -i 's/Demo skill for maintain checker fixtures\./Demo skill, description changed after bootstrap./' \
  "${ROOT}/flow/skills/demo/SKILL.md"
run_check "${ROOT}"
assert_has_result "stale-generated" "fail" "case10 skill description changed without regenerating README"
assert_all_fixes_present "case10 stale-generated fail must carry a fix"
# default (non-write) mode must never mutate the file it is reporting on
after_content="$(cat "${ROOT}/flow/README.md")"
run_check "${ROOT}"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${after_content}" "case10 default mode must not mutate README.md"
rm -rf "${ROOT}"

# =====================================================================
# Case 11: capability-table — README's generated OpenCode column disagrees
# with install-skills.sh's PORTABLE_SKILLS (folds in the retired
# flow/opencode/portability.test.sh drift guard)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
# Hand-edit the generated skills marker's OpenCode cell for "demo" to "No"
# while install-skills.sh still lists it as portable -- a direct disagreement.
sed -i '/cenci-maintain:skills:start/,/cenci-maintain:skills:end/ s/Yes/No/' "${ROOT}/flow/README.md"
run_check "${ROOT}"
assert_has_result "capability-table" "fail" "case11 README OpenCode column disagrees with PORTABLE_SKILLS"
assert_all_fixes_present "case11 capability-table fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 12: adapter-drift — install-skills.sh's PORTABLE_SKILLS names a skill
# that has no corresponding flow/skills/<name>/ directory
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
cat > "${ROOT}/flow/opencode/install-skills.sh" <<'EOF'
#!/bin/sh
PORTABLE_SKILLS="demo ghost-skill"
EOF
run_check "${ROOT}"
assert_has_result "adapter-drift" "fail" "case12 PORTABLE_SKILLS references a nonexistent skill directory"
assert_all_fixes_present "case12 adapter-drift fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 13: structural-tests — an existing flow/tests/*.test.sh fails
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/flow/tests/broken.test.sh" <<'EOF'
#!/usr/bin/env bash
echo "broken.test.sh: failures=1"
exit 1
EOF
run_check "${ROOT}"
assert_has_result "structural-tests" "fail" "case13 broken.test.sh fails"
assert_all_fixes_present "case13 structural-tests fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 14: worktree-ignored — the worktree directory is not git-ignored
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
printf '' > "${ROOT}/.gitignore"
run_check "${ROOT}"
assert_has_result "worktree-ignored" "fail" "case14 .worktrees/ missing from .gitignore"
assert_all_fixes_present "case14 worktree-ignored fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 15: gate-command
#  15a. configured gateCommand exits non-zero -> fail
#  15b. gateCommand key missing entirely -> fail
#  15c. gateCommand cannot be run in this environment -> explicit skip
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/.cenci/config.json" <<'EOF'
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow", "gateCommand": "false" }
  ]
}
EOF
run_check "${ROOT}"
assert_has_result "gate-command" "fail" "case15a gateCommand exits non-zero"
assert_all_fixes_present "case15a gate-command fail must carry a fix"
rm -rf "${ROOT}"

ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/.cenci/config.json" <<'EOF'
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow" }
  ]
}
EOF
run_check "${ROOT}"
assert_has_result "gate-command" "fail" "case15b gateCommand missing entirely"
assert_all_fixes_present "case15b gate-command fail must carry a fix"
rm -rf "${ROOT}"

ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/.cenci/config.json" <<'EOF'
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow", "gateCommand": "definitely-not-a-real-command-xyz" }
  ]
}
EOF
run_check "${ROOT}"
assert_has_result "gate-command" "skip" "case15c unrunnable gateCommand must skip, never silently pass"
n_gc_pass="$(count_results "gate-command" "pass")"
is_eq0 "${n_gc_pass}" || fail "case15c: unrunnable gateCommand must not report pass (got ${n_gc_pass} pass results)"
rm -rf "${ROOT}"

# =====================================================================
# Case 16: claude-rules-imports — .claude/rules/ contains a file not
# explicitly @-imported by the applicable project instructions
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/.claude/rules"
cat > "${ROOT}/.claude/rules/orphan.md" <<'EOF'
# Orphan rule file

Not imported by any AGENTS.md/CLAUDE.md.
EOF
run_check "${ROOT}"
assert_has_result "claude-rules-imports" "fail" "case16 unimported .claude/rules/ file"
assert_all_fixes_present "case16 claude-rules-imports fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 17: legacy-lessons — a legacy lessons-learned*.md file remains
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/.claude/rules"
cat > "${ROOT}/.claude/rules/lessons-learned.md" <<'EOF'
# Legacy lessons

- Some old incident-derived rule.
EOF
run_check "${ROOT}"
assert_has_result "legacy-lessons" "warn" "case17 legacy lessons-learned.md remains"
assert_all_fixes_present "case17 legacy-lessons warn must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 18: github-labels — offline/unauthenticated runs must skip, never pass
# (dedicated fixture; this synthetic repo has no real GitHub remote or gh auth)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
run_check "${ROOT}"
assert_has_result "github-labels" "skip" "case18 offline/unauthenticated github-labels must skip"
n_gh_pass="$(count_results "github-labels" "pass")"
is_eq0 "${n_gh_pass}" || fail "case18: offline github-labels must not report pass (got ${n_gh_pass} pass results)"
rm -rf "${ROOT}"

# =====================================================================
# Case 19: context-budget — over-threshold Critical Rules / topic-doc rules
# (CRITICAL_RULES_MAX=10, TOPIC_DOC_RULES_MAX=25 per the plan)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
{
  echo "# demo-repo"
  echo
  echo "## Critical Rules"
  for i in $(seq 1 11); do echo "- Rule number ${i}."; done
} > "${ROOT}/AGENTS.md"
run_check "${ROOT}"
assert_has_result "context-budget" "fail" "case19 root Critical Rules exceeds 10 bullets"
assert_all_fixes_present "case19 context-budget fail must carry a fix"
assert_contains "${REPORT_TEXT}" "maintain" "case19 fix should point at /cenci:maintain rules"
rm -rf "${ROOT}"

# =====================================================================
# Case 20: --changed narrows correctly — only checks relevant to the given
# file list run; unrelated categories produce no results at all
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
cat > "${ROOT}/flow/opencode/install-skills.sh" <<'EOF'
#!/bin/sh
PORTABLE_SKILLS="demo
EOF
printf '{ "isMonorepo": true, oops' > "${ROOT}/.cenci/config.json"

run_check "${ROOT}" --changed flow/opencode/install-skills.sh
assert_has_result "shell-syntax" "fail" "case20 changed file's own category still runs"
assert_no_result "invalid-json" "case20 unrelated invalid-json category must not run"
assert_exit_nonzero "case20 affected changed-file breach still blocks"

run_check "${ROOT}" --changed flow/docs/sample-topic.md
assert_no_result "shell-syntax" "case20b unrelated shell-syntax must not run"
assert_no_result "invalid-json" "case20b unrelated invalid-json must not run"
rm -rf "${ROOT}"

# =====================================================================
# Case 21: --changed context-budget affected-vs-unrelated (the CI gate
# behavior flow-ci.yml depends on)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
{
  echo "# demo-repo"
  echo
  echo "## Critical Rules"
  for i in $(seq 1 11); do echo "- Root rule number ${i}."; done
} > "${ROOT}/AGENTS.md"
{
  echo "# Sample topic"
  echo
  echo "## Rules"
  for i in $(seq 1 26); do echo "- Topic rule number ${i}."; done
} > "${ROOT}/flow/docs/sample-topic.md"

run_check "${ROOT}"
assert_has_result "context-budget" "fail" "case21 full mode: root breach is a fail"
n_cb_fail_full="$(count_results "context-budget" "fail")"
is_ge1 "${n_cb_fail_full}" || fail "case21: full mode expected >=1 context-budget fail, got ${n_cb_fail_full}"
[[ "${n_cb_fail_full}" -ge 2 ]] || fail "case21: full mode expected both breaches to fail, got ${n_cb_fail_full}"
assert_exit_nonzero "case21 full mode with two real breaches exits non-zero"

run_check "${ROOT}" --changed AGENTS.md
assert_has_result "context-budget" "fail" "case21b affected (AGENTS.md) breach still fails"
assert_has_result "context-budget" "warn" "case21b unrelated (sample-topic.md) breach downgrades to warn"
assert_exit_nonzero "case21b affected breach still blocks CI"

run_check "${ROOT}" --changed flow/opencode/install-skills.sh
n_cb_fail_unrelated="$(count_results "context-budget" "fail")"
is_eq0 "${n_cb_fail_unrelated}" || fail "case21c: both breaches unrelated to changed file must not fail, got ${n_cb_fail_unrelated} fail(s)"
n_cb_warn_unrelated="$(count_results "context-budget" "warn")"
[[ "${n_cb_warn_unrelated:-0}" -ge 2 ]] 2>/dev/null || fail "case21c: expected both breaches to warn, got ${n_cb_warn_unrelated:-N/A}"
assert_exit_zero "case21c both breaches unrelated to changed file: non-blocking"
rm -rf "${ROOT}"

# =====================================================================
# Case 22: --strict produces CI-appropriate exit codes (warn also blocks)
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
mkdir -p "${ROOT}/.claude/rules"
cat > "${ROOT}/.claude/rules/lessons-learned.md" <<'EOF'
# Legacy lessons

- Some old incident-derived rule.
EOF

run_check "${ROOT}"
assert_has_result "legacy-lessons" "warn" "case22 warn-only repo (default mode)"
assert_exit_zero "case22 default mode: warn-only repo does not block"

run_check "${ROOT}" --strict
assert_has_result "legacy-lessons" "warn" "case22b warn-only repo (--strict mode)"
assert_exit_nonzero "case22b --strict mode: warn-only repo blocks"
assert_eq "$(printf '%s' "${REPORT_JSON}" | jq -r '.summary.mode')" "strict" "case22b summary.mode"
rm -rf "${ROOT}"

# =====================================================================
# Case 23: marker replacement (--write) only ever touches marker-bounded
# content — everything outside every marker pair is byte-identical after
# a run that changes a canonical source
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
before_skeleton="$(strip_marker_bodies < "${ROOT}/flow/README.md")"

mkdir -p "${ROOT}/flow/skills/demo2"
cat > "${ROOT}/flow/skills/demo2/SKILL.md" <<'EOF'
---
name: demo2
description: "Second demo skill, added after bootstrap."
user-invocable: true
codex-support: yes
---

A second skill so regeneration has something new to add.
EOF
cat >> "${ROOT}/flow/opencode/install-skills.sh" <<'EOF'
EOF
sed -i 's/PORTABLE_SKILLS="demo"/PORTABLE_SKILLS="demo demo2"/' "${ROOT}/flow/opencode/install-skills.sh"

run_check "${ROOT}" --write
assert_exit_zero "case23 --write regenerates cleanly"
after_skeleton="$(strip_marker_bodies < "${ROOT}/flow/README.md")"
assert_eq "${after_skeleton}" "${before_skeleton}" "case23 content outside every marker must be byte-identical after --write"

run_check "${ROOT}"
assert_clean_report "case23 repo is clean again after --write"
rm -rf "${ROOT}"

# =====================================================================
# Case 24: malformed marker states fail closed (per docs/skill-authoring.md's
# "malformed marker states must be defined too" convention) -- never guess a
# replacement span, never mutate the file
# =====================================================================

# 24a. Missing end marker for one pair.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
grep -v '<!-- cenci-maintain:docs-nav:end -->' "${ROOT}/flow/README.md" > "${ROOT}/flow/README.md.tmp"
mv "${ROOT}/flow/README.md.tmp" "${ROOT}/flow/README.md"

before_content="$(cat "${ROOT}/flow/README.md")"
run_check "${ROOT}"
assert_exit_nonzero "case24a missing end marker must fail closed"
assert_contains "${REPORT_TEXT}" "docs-nav" "case24a error must name the malformed marker id"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${before_content}" "case24a must not mutate the file"
run_check "${ROOT}" --write
assert_exit_nonzero "case24a --write on missing end marker must also fail closed"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${before_content}" "case24a --write must not guess a replacement span"
rm -rf "${ROOT}"

# 24b. Duplicate marker pair for the same id.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
{
  echo ""
  echo "<!-- cenci-maintain:skills:start -->"
  echo "<!-- cenci-maintain:skills:end -->"
} >> "${ROOT}/flow/README.md"
before_content="$(cat "${ROOT}/flow/README.md")"
run_check "${ROOT}"
assert_exit_nonzero "case24b duplicate marker pair must fail closed"
assert_contains "${REPORT_TEXT}" "skills" "case24b error must name the duplicated marker id"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${before_content}" "case24b must not mutate the file"
rm -rf "${ROOT}"

# 24c. Reversed start/end marker pair for the same id: exactly one start and
# one end marker are present (counts pass), but the end line appears before
# the start line in the file. This is the silent-data-loss case:
# replace_marker_body's awk state machine goes "inside" at the (later) start
# marker and never sees a closing end marker again, so on --write it would
# otherwise delete every real line through EOF with no error and exit 0.
# validate_markers must also assert ordering, not just counts, and fail
# closed -- never mutate the file, in default mode or --write.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
awk '
  BEGIN { s="<!-- cenci-maintain:docs-nav:start -->"; e="<!-- cenci-maintain:docs-nav:end -->" }
  { lines[NR]=$0; if ($0==s) sidx=NR; if ($0==e) eidx=NR }
  END {
    for (i=1;i<=NR;i++) {
      if (i==eidx) continue
      if (i==sidx) print lines[eidx]
      print lines[i]
    }
  }
' "${ROOT}/flow/README.md" > "${ROOT}/flow/README.md.tmp"
mv "${ROOT}/flow/README.md.tmp" "${ROOT}/flow/README.md"

before_content="$(cat "${ROOT}/flow/README.md")"
run_check "${ROOT}"
assert_exit_nonzero "case24c reversed marker pair must fail closed"
assert_contains "${REPORT_TEXT}" "docs-nav" "case24c error must name the reversed marker id"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${before_content}" "case24c must not mutate the file (default mode)"

run_check "${ROOT}" --write
assert_exit_nonzero "case24c --write on reversed marker pair must also fail closed"
assert_eq "$(cat "${ROOT}/flow/README.md")" "${before_content}" "case24c --write must not silently delete content after the reversed start marker"
rm -rf "${ROOT}"

echo "maintain.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
