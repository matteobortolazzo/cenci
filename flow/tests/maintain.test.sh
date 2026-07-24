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

# The live flow plugin version — check.sh's config-version check compares
# fixture configs against the *bundled* plugin manifest, so the base fixture
# stamps this value dynamically to stay version-proof across releases.
FLOW_PLUGIN_VERSION="$(jq -r .version "${FLOW_DIR}/.claude-plugin/plugin.json")"

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

# run_advisory <root> [args...] — advisory mode emits its report as the only
# stdout payload and must never create the default repository report.
run_advisory() {
  local root="$1"; shift
  local json_path="${root}/.cenci/maintain-report.json"
  local stderr_path
  stderr_path="$(mktemp)"
  rm -f "${json_path}"
  REPORT_STDOUT="$(cd "${root}" && bash "${CHECK}" --advisory "$@" 2>"${stderr_path}")"
  REPORT_CODE=$?
  REPORT_STDERR="$(cat "${stderr_path}")"
  rm -f "${stderr_path}"
  REPORT_JSON="${REPORT_STDOUT}"
  REPORT_TEXT="${REPORT_STDOUT}${REPORT_STDERR}"
}

# run_explicit_report <root> <path> [args...] — selects a non-default report.
run_explicit_report() {
  local root="$1" report_path="$2"; shift 2
  local stderr_path
  stderr_path="$(mktemp)"
  rm -f "${root}/.cenci/maintain-report.json"
  [[ -d "${report_path}" ]] || rm -f "${report_path}"
  REPORT_STDOUT="$(cd "${root}" && bash "${CHECK}" --report-file "${report_path}" "$@" 2>"${stderr_path}")"
  REPORT_CODE=$?
  REPORT_STDERR="$(cat "${stderr_path}")"
  rm -f "${stderr_path}"
  REPORT_TEXT="${REPORT_STDOUT}${REPORT_STDERR}"
  if [[ -f "${report_path}" ]]; then
    REPORT_JSON="$(cat "${report_path}")"
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

# assert_check_ran <check-id> <label> — opposite of assert_no_result: asserts
# the given check produced at least one result (any status), proving a
# --changed mapping actually invoked it instead of leaving it a silent no-op.
assert_check_ran() {
  local check="$1" label="$2"
  local n
  n="$(printf '%s' "${REPORT_JSON}" | jq --arg c "$1" '[.results[]? | select(.check==$c)] | length' 2>/dev/null)"
  is_ge1 "${n}" || fail "${label}: expected '${check}' to have produced at least one result (not a no-op), got '${n:-N/A}'"
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
assert_exit_two() { [[ "${REPORT_CODE}" -eq 2 ]] || fail "$1: expected exit 2, got ${REPORT_CODE}"; }

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

  cat > "${root}/.cenci/config.json" <<EOF
{
  "configVersion": "${FLOW_PLUGIN_VERSION}",
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

add_fixture_client_matrix() {
  local root="$1"
  awk '
    /^<!-- cenci-maintain:skills:start -->/ && !done {
      print "## Skill portability"
      print ""
      print "| Skill | Claude Code | Codex | OpenCode | Notes |"
      print "|-------|-------------|-------|----------|-------|"
      print "| `demo` | Yes | Yes | Yes | Fixture skill |"
      print ""
      done=1
    }
    { print }
  ' "${root}/flow/README.md" > "${root}/flow/README.md.tmp"
  mv "${root}/flow/README.md.tmp" "${root}/flow/README.md"
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

# =====================================================================
# Case 25: command-flags — stale `cenci <verb>` references (ticket #532,
# Q2 cross-reference: watch/*_cmd.go, docs/cli-conventions.md, skills)
# =====================================================================

# 25a. A `cenci <renamed-verb>` span in a repo-root doc that resolves in none
# of the definition surfaces must fail, with a fix pointer.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/example.md" <<'EOF'
# Example doc

Run `cenci ghost-verb --ghost-flag` to do a thing that was renamed away.
EOF
run_check "${ROOT}"
assert_has_result "command-flags" "fail" "case25a stale cenci command/flag reference"
assert_all_fixes_present "case25a command-flags fail must carry a fix"
rm -rf "${ROOT}"

# 25b. A valid `cenci <verb>` present in a watch/*_cmd.go fixture must not be
# flagged for the doc span that references it.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch"
cat > "${ROOT}/watch/real_cmd.go" <<'EOF'
package main

import "flag"

func realVerbCmd() {
	fs := flag.NewFlagSet("real-verb", flag.ExitOnError)
	_ = fs.Bool("real-flag", false, "real flag")
}
EOF
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/example.md" <<'EOF'
# Example doc

Run `cenci real-verb` to do a real, still-supported thing.
EOF
run_check "${ROOT}"
n_cf_fail="$(count_results "command-flags" "fail")"
is_eq0 "${n_cf_fail}" || fail "case25b: valid cenci command present in watch/*_cmd.go must not fail command-flags (got ${n_cf_fail})"
rm -rf "${ROOT}"

# 25c. Comments and unrelated string literals in Go sources are not command
# or flag registrations and therefore cannot validate stale documentation.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch" "${ROOT}/docs"
cat > "${ROOT}/watch/ghost_cmd.go" <<'EOF'
package main

// fs := flag.NewFlagSet("ghost-verb", flag.ExitOnError)
const note = `fs.Bool("ghost-flag", false, "not registration")`
EOF
cat > "${ROOT}/docs/example.md" <<'EOF'
# Example doc

Run `cenci ghost-verb --ghost-flag`.
EOF
run_check "${ROOT}"
assert_has_result "command-flags" "fail" "case25c comments and unrelated strings cannot register CLI tokens"
rm -rf "${ROOT}"

# =====================================================================
# Case 26: config-examples — config-shaped fenced ```json blocks (ticket #532,
# Q3 cross-reference: the live .cenci/config.json key set)
# =====================================================================

# 26a. A config-shaped block with a renamed/invalid field absent from the
# fixture's live .cenci/config.json schema must fail, with a fix pointer.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/getting-started.md" <<'EOF'
# Getting started

Example `.cenci/config.json`:

```json
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "gateCommandRenamed": "npm test"
}
```
EOF
run_check "${ROOT}"
assert_has_result "config-examples" "fail" "case26a config example uses a field not present in the live config schema"
assert_all_fixes_present "case26a config-examples fail must carry a fix"
rm -rf "${ROOT}"

# 26b. A malformed (unparseable) config-shaped block must fail with a
# parse-error message, not be silently skipped.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/health-gates.md" <<'EOF'
# Health gates

Example (malformed) `.cenci/config.json` snippet:

```json
{
  "isMonorepo": true,
  "gateCommand": "npm test",
```
EOF
run_check "${ROOT}"
assert_has_result "config-examples" "fail" "case26b malformed config example fails to parse as JSON"
assert_all_fixes_present "case26b config-examples parse-error fail must carry a fix"
rm -rf "${ROOT}"

# 26c. A config-shaped block whose fields all match the live schema must not
# be flagged.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/getting-started.md" <<'EOF'
# Getting started

Example `.cenci/config.json`:

```json
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow", "gateCommand": "true" }
  ]
}
```
EOF
run_check "${ROOT}"
n_ce_fail="$(count_results "config-examples" "fail")"
is_eq0 "${n_ce_fail}" || fail "case26c: valid config example matching the live schema must not fail config-examples (got ${n_ce_fail})"
rm -rf "${ROOT}"

# 26d. A non-config-shaped json block (e.g. a plugin manifest) must be
# ignored entirely -- no fail or warn triggered by it.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/plugin-example.md" <<'EOF'
# Plugin manifest example

```json
{
  "name": "cenci-sandbox",
  "version": "1.0.0",
  "author": "cenci"
}
```
EOF
run_check "${ROOT}"
n_ce_fail="$(count_results "config-examples" "fail")"
is_eq0 "${n_ce_fail}" || fail "case26d: plugin-manifest-shaped json block must not be flagged by config-examples (got ${n_ce_fail} fail)"
n_ce_warn="$(count_results "config-examples" "warn")"
is_eq0 "${n_ce_warn}" || fail "case26d: plugin-manifest-shaped json block must not warn either (got ${n_ce_warn})"
rm -rf "${ROOT}"

# 26e. Optional-only top-level blocks are detected from the canonical schema.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/security.md" <<'EOF'
# Security config

```json
{
  "security": {
    "sensitivePaths": "wrong"
  }
}
```
EOF
run_check "${ROOT}"
assert_has_result "config-examples" "fail" "case26e optional-only security block has wrong nested type"
rm -rf "${ROOT}"

# 26f. playwrightCli is a configure-owned optional boolean.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/playwright.md" <<'EOF'
# Playwright config

```json
{
  "playwrightCli": true
}
```
EOF
run_check "${ROOT}"
n_ce_fail="$(count_results "config-examples" "fail")"
is_eq0 "${n_ce_fail}" || fail "case26f: valid playwrightCli boolean must not fail config-examples (got ${n_ce_fail})"
sed -i 's/"playwrightCli": true/"playwrightCli": "true"/' "${ROOT}/docs/playwright.md"
run_check "${ROOT}"
assert_has_result "config-examples" "fail" "case26f wrong playwrightCli type fails"
rm -rf "${ROOT}"

# =====================================================================
# Case 27: roadmap-status — docs/roadmap.md name-reference staleness only
# (ticket #532; label words like `Planned`/`Working` must never false-positive)
# =====================================================================

# 27a. docs/roadmap.md naming a deleted docs/gone.md and a missing
# /cenci:ghost skill must produce two fails.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/roadmap.md" <<'EOF'
# Roadmap

- See `docs/gone.md` for background (file was deleted).
- The `/cenci:ghost` skill will land in a future milestone.
EOF
run_check "${ROOT}"
n_rm_fail="$(count_results "roadmap-status" "fail")"
[[ "${n_rm_fail:-0}" =~ ^[0-9]+$ ]] && [[ "${n_rm_fail}" -ge 2 ]] || fail "case27a: expected >=2 roadmap-status fails (deleted doc + missing skill), got ${n_rm_fail:-N/A}"
assert_all_fixes_present "case27a roadmap-status fails must carry a fix"
rm -rf "${ROOT}"

# 27b. Label words like `Planned`/`Working` appearing in roadmap.md must not
# be misread as broken file/skill references.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/roadmap.md" <<'EOF'
# Roadmap

- Ticket #532 status: `Planned`.
- Ticket #533 status: `Working`.
- Ticket #534 status: `Refined`.
EOF
run_check "${ROOT}"
n_rm_fail="$(count_results "roadmap-status" "fail")"
is_eq0 "${n_rm_fail}" || fail "case27b: label words like Planned/Working must not be flagged as broken references (got ${n_rm_fail} fail)"
rm -rf "${ROOT}"

# =====================================================================
# Case 28: watch/sandbox --changed relevance mapping (ticket #532, Q4:
# relevance-mapping-only -- watch/** and sandbox/** stop being silent no-ops
# for the cross-project checks, but existing flow-scoped check bodies are
# unaffected)
# =====================================================================

# 28a. `--changed watch/foo_cmd.go` must trigger command-flags and
# roadmap-status (not a silent no-op).
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
run_check "${ROOT}" --changed watch/foo_cmd.go
assert_check_ran "command-flags" "case28a watch/foo_cmd.go must trigger command-flags (not a silent no-op)"
assert_check_ran "roadmap-status" "case28a watch/foo_cmd.go must trigger roadmap-status (not a silent no-op)"
rm -rf "${ROOT}"

# 28b. `--changed sandbox/lib/x.sh` must trigger roadmap-status via the new
# file_under_project mechanism (not a silent no-op). file_under_project maps
# a changed path to a *configured* project's directory, so this fixture's
# .cenci/config.json must register "sandbox" as a project (setup_base's base
# config only registers "flow") for the mapping to have anything to match --
# without this, the assertion below could never legitimately pass regardless
# of the checker's implementation.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
cat > "${ROOT}/.cenci/config.json" <<'EOF'
{
  "isMonorepo": true,
  "guidanceLocation": "AGENTS.md",
  "projects": [
    { "slug": "flow", "path": "flow", "gateCommand": "true" },
    { "slug": "sandbox", "path": "sandbox", "gateCommand": "true" }
  ]
}
EOF
run_check "${ROOT}" --changed sandbox/lib/x.sh
assert_check_ran "roadmap-status" "case28b sandbox/lib/x.sh must trigger roadmap-status via file_under_project (not a silent no-op)"
rm -rf "${ROOT}"

# 28c. Existing flow-only check bodies (e.g. invalid-json) stay flow-scoped:
# a broken watch/*.json file must not be picked up by invalid-json's body,
# even though it is relevant under --changed (per Q4, only is_relevant's
# --changed mapping and the three new checks gain project awareness).
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch"
printf '{ "broken": true, oops' > "${ROOT}/watch/broken.json"
run_check "${ROOT}" --changed watch/broken.json
n_ij_fail="$(count_results "invalid-json" "fail")"
is_eq0 "${n_ij_fail}" || fail "case28c: invalid-json check body must stay flow-scoped and not fail on watch/broken.json (got ${n_ij_fail})"
rm -rf "${ROOT}"

# =====================================================================
# Case 29: command-flags tokenizer/corpus fix cycle (ticket #532 review
# round 2, item A) -- placeholder/bracket-shaped doc phrasing must not be
# required to match a definition surface verbatim, and a command dispatched
# only from a main.go-style file (no *_cmd.go) must still resolve.
# =====================================================================

# 29a. A doc's shortened bracket-flag phrasing (`[--volumes]`) must resolve
# against a definition surface whose real text has additional bracketed
# flags (`[--images] [--volumes]`) -- the bracketed token must stop the verb
# chain instead of forcing the whole span to match verbatim.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch"
cat > "${ROOT}/watch/sandbox_cmd.go" <<'EOF'
package main

import "flag"

func sandboxPruneCmd() {
	fs := flag.NewFlagSet("sandbox prune", flag.ExitOnError)
	_ = fs
}
EOF
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/example.md" <<'EOF'
# Example doc

Run `cenci sandbox prune [--volumes]` to clean up.
EOF
run_check "${ROOT}"
n_cf_fail="$(count_results "command-flags" "fail")"
is_eq0 "${n_cf_fail}" || fail "case29a: doc's bracketed-flag phrasing must resolve against a corpus fixture with additional bracketed flags (got ${n_cf_fail})"
rm -rf "${ROOT}"

# 29b. A command dispatched only from a main.go-style fixture (not a
# *_cmd.go file) must still resolve -- watch/main.go is part of the corpus.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch"
cat > "${ROOT}/watch/main.go" <<'EOF'
package main

func main() {
	switch "doctor" {
	case "doctor":
	}
}
EOF
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/example2.md" <<'EOF'
# Example doc

Run `cenci doctor` to check your setup.
EOF
run_check "${ROOT}"
n_cf_fail="$(count_results "command-flags" "fail")"
is_eq0 "${n_cf_fail}" || fail "case29b: a command defined only in a main.go-style fixture must resolve (got ${n_cf_fail})"
rm -rf "${ROOT}"

# =====================================================================
# Case 30: extract_json_blocks must not silently drop an unterminated
# ```json fence (ticket #532 review round 2, item B) -- a fenced block that
# is opened but never closed before EOF must still produce a result.
# =====================================================================
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/unterminated.md" <<'EOF'
# Unterminated example

```json
{
  "isMonorepo": true,
  "gateCommand": "npm test"
EOF
run_check "${ROOT}"
assert_has_result "config-examples" "fail" "case30 unterminated \`\`\`json fence must still produce a config-examples result, not be silently ignored"
assert_all_fixes_present "case30 config-examples fail must carry a fix"
rm -rf "${ROOT}"

# =====================================================================
# Case 31: config-version — .cenci/config.json's configVersion stamp vs the
# bundled flow plugin version (advisory: warn, never fail). The staleness
# decision itself is owned by hooks/scripts/check-config-staleness.sh (see
# its own test file); these cases pin check.sh's wiring of that resolver.
# =====================================================================

# 31a: configVersion behind the plugin's major.minor -> warn with both versions
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
jq '.configVersion = "0.0.1"' "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp" \
  && mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}" --write   # bootstrap generated sections, as in case 1
run_check "${ROOT}"
assert_exit_zero "case31a stale configVersion is advisory (warn must not block default mode)"
assert_has_result "config-version" "warn" "case31a stale configVersion warns"
assert_contains "${REPORT_TEXT}" "0.0.1" "case31a warn names the stamped version"
assert_contains "${REPORT_TEXT}" "/cenci:configure" "case31a fix points at configure"
assert_all_fixes_present "case31a config-version warn must carry a fix"
rm -rf "${ROOT}"

# 31b: config without configVersion (pre-stamping configure) -> warn
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
jq 'del(.configVersion)' "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp" \
  && mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_has_result "config-version" "warn" "case31b unstamped config warns"
assert_all_fixes_present "case31b config-version warn must carry a fix"
rm -rf "${ROOT}"

# 31c: configVersion ahead of the plugin (downgrade) -> pass, never a nag
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
jq '.configVersion = "999.999.999"' "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp" \
  && mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_has_result "config-version" "pass" "case31c ahead-of-plugin config passes"
n_cv_warn="$(count_results "config-version" "warn")"
is_eq0 "${n_cv_warn}" || fail "case31c: ahead-of-plugin config must not warn (got ${n_cv_warn})"
rm -rf "${ROOT}"

# 31d: patch-only drift stays current -> pass (re-run nags are reserved for
# minor/major feature bumps)
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
FLOW_PLUGIN_MM="$(printf '%s' "${FLOW_PLUGIN_VERSION}" | cut -d. -f1,2)"
jq --arg v "${FLOW_PLUGIN_MM}.999" '.configVersion = $v' "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp" \
  && mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_has_result "config-version" "pass" "case31d patch-only drift passes"
rm -rf "${ROOT}"

# 31e: --changed narrowing — unrelated file skips the check entirely; a
# changed .cenci/config.json runs it
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
run_check "${ROOT}" --changed flow/skills/sample/SKILL.md
assert_no_result "config-version" "case31e config-version not relevant to an unrelated changed file"
run_check "${ROOT}" --changed .cenci/config.json
assert_check_ran "config-version" "case31e config-version runs when .cenci/config.json changed"
rm -rf "${ROOT}"

# =====================================================================
# Ticket #666 checker hardening
# =====================================================================

# Case 32: every generated marker pair is required, including the case where
# both halves of one pair are absent.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
grep -v 'cenci-maintain:docs-nav:' "${ROOT}/flow/README.md" > "${ROOT}/flow/README.md.tmp"
mv "${ROOT}/flow/README.md.tmp" "${ROOT}/flow/README.md"
run_check "${ROOT}"
assert_has_result "stale-generated" "fail" "case32 an entirely missing marker pair fails"
assert_contains "${REPORT_TEXT}" "docs-nav" "case32 names the missing marker pair"
rm -rf "${ROOT}"

# Case 33: a clean generated-section comparison emits an explicit pass.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
run_check "${ROOT}"
assert_has_result "stale-generated" "pass" "case33 clean generated sections emit a pass"
rm -rf "${ROOT}"

# Case 34: zero structural test scripts is a failure, never a vacuous pass.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
rm -f "${ROOT}/flow/tests/sample.test.sh"
run_check "${ROOT}"
assert_has_result "structural-tests" "fail" "case34 zero structural tests fails"
rm -rf "${ROOT}"

# Case 35: scripts/ references resolve relative to their owning skill. A
# same-named script under another skill must not mask the missing owner file.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
rm -f "${ROOT}/flow/skills/demo/scripts/helper.sh"
mkdir -p "${ROOT}/flow/skills/other/scripts"
cat > "${ROOT}/flow/skills/other/SKILL.md" <<'EOF'
---
name: other
description: "Other fixture skill."
---

Uses `scripts/helper.sh`.
EOF
cat > "${ROOT}/flow/skills/other/scripts/helper.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
run_check "${ROOT}"
assert_has_result "broken-refs" "fail" "case35 duplicate basename in another skill cannot satisfy owner-relative reference"
assert_contains "${REPORT_TEXT}" "flow/skills/demo" "case35 identifies the owning skill"
rm -rf "${ROOT}"

# Case 36: phase, mode, and Codex companion files participate in dependency
# indexes, not only the owning SKILL.md.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/flow/skills/demo/phases" "${ROOT}/flow/skills/demo/modes"
cat > "${ROOT}/flow/skills/demo/phases/phase-one.md" <<'EOF'
Use the `phase-agent`.
EOF
cat > "${ROOT}/flow/skills/demo/modes/sample.md" <<'EOF'
Use the `mode-agent`.
EOF
cat > "${ROOT}/flow/skills/demo/codex.md" <<'EOF'
Use the `codex-agent`.
EOF
for agent in phase-agent mode-agent codex-agent; do
  cat > "${ROOT}/flow/agents/${agent}.md" <<EOF
---
name: ${agent}
description: "${agent} fixture."
---
EOF
done
run_check "${ROOT}" --write
README_CONTENT="$(cat "${ROOT}/flow/README.md")"
assert_contains "${README_CONTENT}" '| `phase-agent` | phase-agent fixture. | demo |' "case36 phase dependency is indexed"
assert_contains "${README_CONTENT}" '| `mode-agent` | mode-agent fixture. | demo |' "case36 mode dependency is indexed"
assert_contains "${README_CONTENT}" '| `codex-agent` | codex-agent fixture. | demo |' "case36 Codex dependency is indexed"
assert_contains "${README_CONTENT}" '| Skill | Procedure files | Reference skills | Scripts | Agents |' "case36 dependency index names its procedure-files column"
assert_contains "${README_CONTENT}" 'codex.md, modes/sample.md, phases/phase-one.md' "case36 dependency index lists Codex, mode, and phase files"
rm -rf "${ROOT}"

# Case 37: CLI documentation never validates itself. Repeating a stale
# command and flag in convention/skill prose cannot make them canonical.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/cli-conventions.md" <<'EOF'
Run `cenci ghost-verb --ghost-flag`.
EOF
cat >> "${ROOT}/flow/skills/demo/SKILL.md" <<'EOF'

Also run `cenci ghost-verb --ghost-flag`.
EOF
run_check "${ROOT}"
assert_has_result "command-flags" "fail" "case37 stale command repeated only in docs still fails"
rm -rf "${ROOT}"

# Case 38: commands and flags registered in Go CLI source remain valid.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/watch" "${ROOT}/docs"
cat > "${ROOT}/watch/real_cmd.go" <<'EOF'
package main

import "flag"

func realCmd(args []string) {
	fs := flag.NewFlagSet("real-verb", flag.ContinueOnError)
	_ = fs.Bool("real-flag", false, "fixture flag")
}
EOF
cat > "${ROOT}/docs/example.md" <<'EOF'
Run `cenci real-verb --real-flag`.
EOF
run_check "${ROOT}"
n_cf_fail="$(count_results "command-flags" "fail")"
is_eq0 "${n_cf_fail}" || fail "case38 Go-registered command and flag must pass (got ${n_cf_fail})"
rm -rf "${ROOT}"

# Case 39: canonical config metadata covers optional nested fields even when
# the live fixture omits them, and rejects wrong nesting/types.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/optional-config.md" <<'EOF'
```json
{
  "maintenance": {
    "enabled": true,
    "checkDuringImplement": false,
    "remindAfterDays": 14,
    "generatedDocs": true
  }
}
```
EOF
run_check "${ROOT}"
n_ce_fail="$(count_results "config-examples" "fail")"
is_eq0 "${n_ce_fail}" || fail "case39a absent optional maintenance schema fields must pass (got ${n_ce_fail})"
rm -rf "${ROOT}"

ROOT="$(mktemp -d)"
setup_base "${ROOT}"
mkdir -p "${ROOT}/docs"
cat > "${ROOT}/docs/wrong-config.md" <<'EOF'
```json
{
  "generatedDocs": true,
  "maintenance": {
    "enabled": "yes"
  }
}
```
EOF
run_check "${ROOT}"
n_ce_fail="$(count_results "config-examples" "fail")"
[[ "${n_ce_fail:-0}" =~ ^[0-9]+$ ]] && [[ "${n_ce_fail}" -ge 2 ]] || fail "case39b wrong config nesting and type must each fail (got ${n_ce_fail:-N/A})"
rm -rf "${ROOT}"

# Case 40: the hand-curated client matrix row set is exactly the union of
# portable and user-invocable skills.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
add_fixture_client_matrix "${ROOT}"
sed -i '/`demo`/d' "${ROOT}/flow/README.md"
run_check "${ROOT}"
assert_has_result "capability-table" "fail" "case40a missing expected client row fails"
rm -rf "${ROOT}"

ROOT="$(mktemp -d)"
setup_base "${ROOT}"
add_fixture_client_matrix "${ROOT}"
sed -i 's/`demo`/`renamed-demo`/' "${ROOT}/flow/README.md"
run_check "${ROOT}"
n_cap_fail="$(count_results "capability-table" "fail")"
[[ "${n_cap_fail:-0}" =~ ^[0-9]+$ ]] && [[ "${n_cap_fail}" -ge 2 ]] || fail "case40b renamed row reports missing and unexpected entries (got ${n_cap_fail:-N/A})"
rm -rf "${ROOT}"

# Case 41: configured project paths cannot escape the repository.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
jq '(.projects[] | select(.slug=="flow") | .path) = "../outside"' \
  "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp"
mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_exit_nonzero "case41 escaping project path is rejected"
assert_contains "${REPORT_TEXT}" "escapes repository root" "case41 path diagnostic is explicit"
rm -rf "${ROOT}"

# Case 41b: symlink escapes, including a missing tail below a symlink
# ancestor, fail closed and never execute the configured gate.
for project_path in linked linked/missing; do
  ROOT="$(mktemp -d)"
  OUTSIDE="$(mktemp -d)"
  setup_base "${ROOT}"
  ln -s "${OUTSIDE}" "${ROOT}/linked"
  jq --arg path "$project_path" '
    (.projects[] | select(.slug=="flow") | .path) = $path |
    (.projects[] | select(.slug=="flow") | .gateCommand) = "touch gate-ran"
  ' "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp"
  mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
  run_check "${ROOT}"
  assert_exit_nonzero "case41b symlink escaping project path is rejected"
  assert_has_result "gate-command" "skip" "case41b unsafe path skips executable gate"
  [[ ! -e "${OUTSIDE}/gate-ran" ]] || fail "case41b unsafe project path executed a gate outside the repository"
  rm -rf "${ROOT}" "${OUTSIDE}"
done

# Case 41c: path safety remains portable when a host realpath rejects GNU -m.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
mkdir -p "${ROOT}/bin"
cat > "${ROOT}/bin/realpath" <<'EOF'
#!/usr/bin/env bash
echo "fixture realpath must not be called" >&2
exit 2
EOF
chmod +x "${ROOT}/bin/realpath"
PATH="${ROOT}/bin:${PATH}" run_check "${ROOT}"
assert_exit_zero "case41c checker does not depend on GNU realpath -m"
[[ "${REPORT_TEXT}" != *"fixture realpath"* ]] || fail "case41c checker invoked realpath"
rm -rf "${ROOT}"

# Case 42: advisory mode is repository-read-only, emits pure JSON on stdout,
# and explicitly skips executable/network checks.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
rm -f "${ROOT}/.cenci/maintain-report.json"
cat > "${ROOT}/flow/tests/sample.test.sh" <<'EOF'
#!/usr/bin/env bash
touch advisory-structural-ran
exit 0
EOF
jq '(.projects[] | select(.slug=="flow") | .gateCommand) = "touch advisory-gate-ran"' \
  "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp"
mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
BEFORE_HASHES="$(find "${ROOT}" -type f ! -path '*/.git/*' -print0 | sort -z | xargs -0 sha256sum)"
run_advisory "${ROOT}"
assert_exit_zero "case42 advisory clean run exits zero"
jq -e '.summary.mode == "advisory"' <<< "${REPORT_STDOUT}" >/dev/null 2>&1 || fail "case42 advisory stdout is one valid JSON report"
assert_has_result "structural-tests" "skip" "case42 structural tests explicitly skip"
assert_has_result "gate-command" "skip" "case42 gate command explicitly skips"
assert_has_result "github-labels" "skip" "case42 GitHub check explicitly skips"
[[ ! -e "${ROOT}/advisory-gate-ran" && ! -e "${ROOT}/advisory-structural-ran" ]] || fail "case42 advisory executed a skipped check"
[[ ! -e "${ROOT}/.cenci/maintain-report.json" ]] || fail "case42 advisory wrote the default report"
AFTER_HASHES="$(find "${ROOT}" -type f ! -path '*/.git/*' -print0 | sort -z | xargs -0 sha256sum)"
assert_eq "${AFTER_HASHES}" "${BEFORE_HASHES}" "case42 advisory must not mutate repository files"
rm -rf "${ROOT}"

# Case 43: --report-file selects an explicit report, and report-write
# failures exit 2 without printing a summary.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
bootstrap_markers "${ROOT}"
run_explicit_report "${ROOT}" "${ROOT}/.cenci/custom-report.json" --strict
assert_exit_zero "case43a strict explicit report succeeds"
jq -e '.summary.mode == "strict"' <<< "${REPORT_JSON}" >/dev/null 2>&1 || fail "case43a strict explicit report is valid JSON"
[[ ! -e "${ROOT}/.cenci/maintain-report.json" ]] || fail "case43a explicit report also wrote the default"

printf 'not a directory\n' > "${ROOT}/blocked-parent"
run_explicit_report "${ROOT}" "${ROOT}/blocked-parent/report.json"
assert_exit_two "case43b report write failure exits 2"
[[ "${REPORT_TEXT}" != *"summary:"* ]] || fail "case43b report write failure printed a summary"
[[ ! -e "${ROOT}/.cenci/maintain-report.json" ]] || fail "case43b failure wrote the default report"

mkdir -p "${ROOT}/report-is-directory"
run_explicit_report "${ROOT}" "${ROOT}/report-is-directory"
assert_exit_two "case43c directory report destination exits 2"
[[ "${REPORT_TEXT}" != *"summary:"* ]] || fail "case43c directory report destination printed a summary"
rm -rf "${ROOT}"

# Case 44: generatedDocs=false skips only generated-section maintenance.
ROOT="$(mktemp -d)"
setup_base "${ROOT}"
jq '.maintenance = {"generatedDocs": false}' \
  "${ROOT}/.cenci/config.json" > "${ROOT}/.cenci/config.json.tmp"
mv "${ROOT}/.cenci/config.json.tmp" "${ROOT}/.cenci/config.json"
run_check "${ROOT}"
assert_has_result "stale-generated" "skip" "case44 generatedDocs=false explicitly skips generated sections"
assert_has_result "front-matter" "pass" "case44 core checks still run"
assert_has_result "structural-tests" "pass" "case44 executable checks still run outside advisory"
rm -rf "${ROOT}"

# =====================================================================
# Skill markdown contract cases (ticket #545) -- grep-based anchor-phrase
# assertions against flow/skills/maintain/{SKILL.md,codex.md,modes/*.md},
# none of which exist yet at RED-phase time (the skill/mode/agent files are
# Phase 4's job). Mirrors the anchor-phrase idiom in
# flow/tests/parity/parity.test.sh: assert the exact edit-site text, never a
# bare generic marker that could vacuously match unrelated prose -- see
# docs/shell-scripting-gotchas.md's grep-based contract-test-marker
# guidance. Every phrase below is written as a single unwrapped source line
# in the skill/mode markdown authored in Phase 4, specifically to avoid the
# Markdown line-wrap pitfall also documented there (a phrase split across
# two source lines would make a naive grep -F false-negative even though the
# semantic content is correct).
# =====================================================================

MAINTAIN_SKILL_DIR="${FLOW_DIR}/skills/maintain"
MAINTAIN_SKILL_MD="${MAINTAIN_SKILL_DIR}/SKILL.md"
MAINTAIN_CODEX_MD="${MAINTAIN_SKILL_DIR}/codex.md"
MAINTAIN_MODE_STRUCTURE="${MAINTAIN_SKILL_DIR}/modes/structure.md"
MAINTAIN_MODE_DOCS="${MAINTAIN_SKILL_DIR}/modes/docs.md"
MAINTAIN_MODE_CLIENTS="${MAINTAIN_SKILL_DIR}/modes/clients.md"
MAINTAIN_MODE_RULES="${MAINTAIN_SKILL_DIR}/modes/rules.md"
MAINTAIN_AGENT_RULES="${FLOW_DIR}/agents/rules-maintainer.md"

# assert_file_has_phrase <file> <phrase> <label> -- fails if the file is
# missing OR the exact (grep -F, literal) phrase is not present.
assert_file_has_phrase() {
  local file="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: test bug -- empty required phrase"; return; }
  [[ -f "${file}" ]] || { fail "${label}: file not found: ${file}"; return; }
  grep -qF -- "${phrase}" "${file}" || fail "${label}: expected ${file} to contain: ${phrase}"
}

# assert_file_lacks_phrase <file> <phrase> <label> -- fails if the file is
# missing (a "must not contain" claim about a nonexistent file is meaningless
# and must never vacuously pass) OR the exact phrase IS present.
assert_file_lacks_phrase() {
  local file="$1" phrase="$2" label="$3"
  [[ -n "${phrase}" ]] || { fail "${label}: test bug -- empty required phrase"; return; }
  [[ -f "${file}" ]] || { fail "${label}: file not found: ${file}"; return; }
  grep -qF -- "${phrase}" "${file}" && fail "${label}: expected ${file} to NOT contain: ${phrase}"
  return 0
}

# --- Mode parsing: each modes/*.md names its one analyzer -----------------
assert_file_has_phrase "${MAINTAIN_MODE_STRUCTURE}" "structure-maintainer" "MC1 structure.md names structure-maintainer"
assert_file_has_phrase "${MAINTAIN_MODE_DOCS}" "docs-maintainer" "MC2 docs.md names docs-maintainer"
assert_file_has_phrase "${MAINTAIN_MODE_CLIENTS}" "portability-maintainer" "MC3 clients.md names portability-maintainer"

# --- One-mode-one-analyzer gating: negative cross-mode assertions, plus
# mode "all" launching all three (SKILL.md must name all three agents by
# their backticked name so check.sh's generated workflow-deps/agents tables
# resolve the references -- see plan Files to Create) ----------------------
assert_file_lacks_phrase "${MAINTAIN_MODE_STRUCTURE}" "docs-maintainer" "MC4 structure.md must not name docs-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_STRUCTURE}" "portability-maintainer" "MC5 structure.md must not name portability-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_DOCS}" "structure-maintainer" "MC6 docs.md must not name structure-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_DOCS}" "portability-maintainer" "MC7 docs.md must not name portability-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_CLIENTS}" "structure-maintainer" "MC8 clients.md must not name structure-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_CLIENTS}" "docs-maintainer" "MC9 clients.md must not name docs-maintainer"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "structure-maintainer" "MC10 SKILL.md (mode 'all') names structure-maintainer"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "docs-maintainer" "MC11 SKILL.md (mode 'all') names docs-maintainer"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "portability-maintainer" "MC12 SKILL.md (mode 'all') names portability-maintainer"

# --- Approval options per mode: mode-scoped options incl. "report only".
# Ticket #546 flips MC17 to assert presence of the rules-only option in
# SKILL.md (the only file that ever documents approval options -- Phase 5 is
# shared/central, not per-mode). MC18-MC20 stay lacks-assertions: mode files
# never document approval options themselves regardless of whether rules
# mode exists (see modes/structure.md's own "Approval" section: it points at
# SKILL.md's Phase 5, it doesn't restate options). MC21 also stays a
# lacks-assertion: codex.md gets a one-line rule-curation mention (tested
# separately below) and now carries the full rules-only approval contract. ---
MAINTAIN_OPT_ALL_REPAIRS="all deterministic repairs"
MAINTAIN_OPT_CRIT_HIGH="critical+high findings"
MAINTAIN_OPT_SELECT="let me select findings"
MAINTAIN_OPT_REPORT_ONLY="report only"
MAINTAIN_OPT_RULES_ONLY="rules only"

assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_OPT_ALL_REPAIRS}" "MC13 approval options include: all deterministic repairs"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_OPT_CRIT_HIGH}" "MC14 approval options include: critical+high findings"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_OPT_SELECT}" "MC15 approval options include: let me select findings"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_OPT_REPORT_ONLY}" "MC16 approval options include: report only"

assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_OPT_RULES_ONLY}" "MC17 SKILL.md now offers a rules only approval option"
assert_file_lacks_phrase "${MAINTAIN_MODE_STRUCTURE}" "${MAINTAIN_OPT_RULES_ONLY}" "MC18 structure.md must not offer a rules only approval option"
assert_file_lacks_phrase "${MAINTAIN_MODE_DOCS}" "${MAINTAIN_OPT_RULES_ONLY}" "MC19 docs.md must not offer a rules only approval option"
assert_file_lacks_phrase "${MAINTAIN_MODE_CLIENTS}" "${MAINTAIN_OPT_RULES_ONLY}" "MC20 clients.md must not offer a rules only approval option"
assert_file_has_phrase "${MAINTAIN_CODEX_MD}" "${MAINTAIN_OPT_RULES_ONLY}" "MC21 codex.md offers the full rules only approval option"

# --- Report-only no-mutation: SKILL.md documents report-only as terminal
# (no worktree/file/ticket/label/commit/push/PR) and restates that
# pre-approval phases are read-only ----------------------------------------
MAINTAIN_REPORT_ONLY_TERMINAL="Choosing it must end after reporting: no worktree, file mutation, ticket/label mutation, commit, push, or pull request."
MAINTAIN_PRE_APPROVAL_READONLY="Pre-approval phases must remain read-only"

assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_REPORT_ONLY_TERMINAL}" "MC22 SKILL.md documents report-only as terminal (no worktree/file/ticket/label/commit/push/PR)"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_PRE_APPROVAL_READONLY}" "MC23 SKILL.md restates that pre-approval phases are read-only"

# --- Scope no-op: watch/sandbox is an explicit "not yet covered" no-op ----
MAINTAIN_SCOPE_NOOP_PHRASE="not yet covered"
MAINTAIN_SCOPE_PROJECTS_PHRASE='`watch`/`sandbox`'

assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_SCOPE_NOOP_PHRASE}" "MC24 SKILL.md documents the out-of-scope-project no-op wording (not yet covered)"
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_SCOPE_PROJECTS_PHRASE}" "MC25 SKILL.md names watch/sandbox as the out-of-scope no-op projects"

# =====================================================================
# Ticket #546 -- port Garden's rule curation into /cenci:maintain rules mode.
# Grep-based anchor-phrase assertions against modes/rules.md,
# agents/rules-maintainer.md, SKILL.md's rules wiring, and codex.md's
# rule-curation mention -- none of these production files/edits exist yet at
# RED-phase time (Phase 4's job). Mirrors the #545 anchor-phrase idiom above:
# assert the exact edit-site text, single unwrapped source line, never a bare
# generic marker that could vacuously match unrelated prose.
# =====================================================================

# --- Mode parsing: rules.md names rules-maintainer as its sole analyzer,
# mirroring MC1-3 -----------------------------------------------------------
assert_file_has_phrase "${MAINTAIN_MODE_RULES}" "rules-maintainer" "MC26 rules.md names rules-maintainer"

# --- One-mode-one-analyzer gating: rules.md must not name the other three
# analyzers, mirroring the MC4-9 cross-mode negative-test pattern -----------
assert_file_lacks_phrase "${MAINTAIN_MODE_RULES}" "structure-maintainer" "MC27 rules.md must not name structure-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_RULES}" "docs-maintainer" "MC28 rules.md must not name docs-maintainer"
assert_file_lacks_phrase "${MAINTAIN_MODE_RULES}" "portability-maintainer" "MC29 rules.md must not name portability-maintainer"

# --- SKILL.md wiring: mode "all" also launches rules-maintainer, backtick-
# named so check.sh's generated agents/workflow-deps tables resolve the
# reference (mirrors MC10-12) -----------------------------------------------
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "rules-maintainer" "MC30 SKILL.md (mode 'all') names rules-maintainer"

# --- SKILL.md Phase 3 dispatch: mode rules launches only rules-maintainer,
# same phrasing pattern as the other three modes' dispatch lines ("launch
# only \`structure-maintainer\`" etc.) --------------------------------------
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "launch only \`rules-maintainer\`" "MC31 SKILL.md Phase 3 dispatches mode rules to launch only rules-maintainer"

# --- SKILL.md grammar: rules is now a valid first-token mode alongside
# structure/docs/clients, and the "not yet a valid mode" sentence is gone --
MAINTAIN_MODE_LIST_PHRASE='`structure`, `docs`, `clients`, or `rules`'
assert_file_has_phrase "${MAINTAIN_SKILL_MD}" "${MAINTAIN_MODE_LIST_PHRASE}" "MC32 SKILL.md grammar lists rules as a valid mode token"
assert_file_lacks_phrase "${MAINTAIN_SKILL_MD}" "is not yet a valid mode" "MC33 SKILL.md no longer says rules is not yet a valid mode"

# --- codex.md parity mention: a rule/lesson-curation mention is added to the
# audited-drift sentence, per Q&A round 1 -- literal quoted from the plan's
# Files to Modify example so Phase 4 has an unambiguous target -------------
MAINTAIN_CODEX_RULE_MENTION='`## Critical Rules` / topic-doc rule curation'
assert_file_has_phrase "${MAINTAIN_CODEX_MD}" "${MAINTAIN_CODEX_RULE_MENTION}" "MC34 codex.md mentions rule/lesson curation for Claude/Codex parity"

# --- agents/rules-maintainer.md: shared finding schema + Garden's evidence
# discipline preserved verbatim ---------------------------------------------
assert_file_has_phrase "${MAINTAIN_AGENT_RULES}" "Rule hygiene" "MC35 rules-maintainer.md documents Category: Rule hygiene"
assert_file_has_phrase "${MAINTAIN_AGENT_RULES}" "Demote and Archive require fresh \`Grep\`/\`Read\` evidence" "MC36 rules-maintainer.md preserves fresh Grep/Read evidence discipline for Demote/Archive"
assert_file_has_phrase "${MAINTAIN_AGENT_RULES}" "Quote each bullet being merged" "MC37 rules-maintainer.md preserves quoted-bullets evidence discipline for Merge"
assert_file_has_phrase "${MAINTAIN_AGENT_RULES}" "Default is Keep" "MC38 rules-maintainer.md preserves default-Keep classification"
assert_file_has_phrase "${MAINTAIN_AGENT_RULES}" "you never edit files" "MC39 rules-maintainer.md states it is read-only and never edits files"

# --- modes/rules.md: references check.sh as the threshold source of truth,
# never restates the numeric marks, and states the legacy lessons-learned*.md
# migration behavior (survivors relocated + legacy file deleted, same PR) --
assert_file_has_phrase "${MAINTAIN_MODE_RULES}" "scripts/check.sh" "MC40 rules.md references scripts/check.sh as the source of truth for context-budget thresholds"
assert_file_lacks_phrase "${MAINTAIN_MODE_RULES}" "~10" "MC41 rules.md must not restate the Critical-Rules numeric threshold"
assert_file_lacks_phrase "${MAINTAIN_MODE_RULES}" "~25" "MC42 rules.md must not restate the topic-doc numeric threshold"
assert_file_has_phrase "${MAINTAIN_MODE_RULES}" "lessons-learned*.md" "MC43 rules.md names legacy lessons-learned*.md files"
assert_file_has_phrase "${MAINTAIN_MODE_RULES}" "same PR" "MC44 rules.md states legacy survivors are relocated and the legacy file deleted in the same PR"

# --- Garden retirement (#547): flow/skills/garden/ must be gone, and no
# retired-command literal (the old skill's slash-invocation prefixed with
# either the Claude or Codex client sigil) may remain live anywhere in the
# repo. The needle is built from concatenated parts so this assertion's own
# source line never self-matches its own grep. -------------------------------
if [[ -d "${FLOW_DIR}/skills/garden" ]]; then
  fail "MC45 flow/skills/garden/ must be deleted (garden is retired)"
fi

REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)"
if [[ -z "${REPO_ROOT}" || ! -d "${REPO_ROOT}" ]]; then
  fail "MC46 setup: REPO_ROOT resolution failed (cd \"${FLOW_DIR}/..\" did not resolve to a real directory)"
else
  GARDEN_NEEDLE_PART1="cenci:"
  GARDEN_NEEDLE_PART2="garden"
  GARDEN_NEEDLE="${GARDEN_NEEDLE_PART1}${GARDEN_NEEDLE_PART2}"
  GARDEN_GREP_STDERR="$(mktemp)"
  GARDEN_HITS="$(grep -rlF \
    --exclude-dir=.git \
    --exclude-dir=.worktrees \
    --exclude-dir=worktrees \
    --exclude="migrating-to-cenci.md" \
    -- "${GARDEN_NEEDLE}" "${REPO_ROOT}" \
    2>"${GARDEN_GREP_STDERR}")"
  GARDEN_GREP_CODE=$?
  if [[ "${GARDEN_GREP_CODE}" -gt 1 ]]; then
    fail "MC46 setup: grep search over ${REPO_ROOT} failed (exit ${GARDEN_GREP_CODE}): $(cat "${GARDEN_GREP_STDERR}")"
  elif [[ -n "${GARDEN_HITS}" ]]; then
    fail "MC46 no live retired-garden-command reference may remain outside docs/migrating-to-cenci.md; found in: ${GARDEN_HITS}"
  fi
  rm -f "${GARDEN_GREP_STDERR}"
fi

echo "maintain.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
