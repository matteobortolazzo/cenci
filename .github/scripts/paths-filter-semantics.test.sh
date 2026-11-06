#!/usr/bin/env bash
# Tests for paths-filter-semantics.sh (ticket #956). Follows the precedent of
# check-paths-filters.test.sh: plain bash, no framework, PASS/FAIL counters,
# non-zero exit on any failure. Each case builds a fresh throwaway fixture tree
# (.github/workflows + .github/scripts/paths-filter-cases.yml) under one mktemp
# root and runs the script with cwd set to that tree.
#
# These tests assert the script's CONTRACT -- exit codes and the coordinates
# (workflow, job, step, case, key) named in its messages -- against synthetic
# workflow and cases files. They never evaluate which paths a filter pattern
# matches: that is the job of the real dorny/paths-filter action, which the
# workflow-lint.yml matrix runs. Re-implementing picomatch here would assert a
# model of the action instead of the action (the #950 mistake).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEM="${SCRIPT_DIR}/paths-filter-semantics.sh"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REAL_WORKFLOW="${REPO_ROOT}/.github/workflows/workflow-lint.yml"

PIN='dorny/paths-filter@ceb8a2b8f2d89434be7ff52d3de7ec3738c5cc9d'
OTHER_PIN='dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706'

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

for tool in yq jq git; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "paths-filter-semantics.test.sh: ${tool} is required but was not found on PATH" >&2
        exit 2
    fi
done

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/paths-filter-semantics-test.XXXXXX")" || {
    echo "paths-filter-semantics.test.sh: failed to create fixture root" >&2
    exit 2
}
trap 'rm -rf "${TEST_ROOT}"' EXIT

# ── fixture builders ─────────────────────────────────────────────────────

# write_selftest_workflow <dir> [uses] -- workflow-lint.yml carrying only the
# self-test step (job paths-filter-semantics-run, step id semantics).
write_selftest_workflow() {
    local dir="$1" uses="${2:-${PIN}}"
    cat > "${dir}/.github/workflows/workflow-lint.yml" <<EOF
name: lint
on:
  pull_request:
jobs:
  paths-filter-semantics-run:
    runs-on: ubuntu-latest
    steps:
      - uses: ${uses}
        id: semantics
        with:
          token: ''
          filters: \${{ steps.setup.outputs.filters_file }}
EOF
}

DEFAULT_FILTERS=$'          filters: |\n            proj:\n              - \'proj/**\'\n            other:\n              - \'other/**\''

# write_app_workflow <dir> [uses] [extra with: line] [filters lines] -- app.yml,
# job changes, step 0 checkout, step 1 the paths-filter step with id filter.
write_app_workflow() {
    local dir="$1" uses="${2:-${PIN}}" with_extra="${3:-}" filters="${4:-${DEFAULT_FILTERS}}"
    {
        printf '%s\n' 'name: app' 'on:' '  pull_request:' 'jobs:' '  changes:' \
            '    runs-on: ubuntu-latest' '    steps:' '      - uses: actions/checkout@v7' \
            "      - uses: ${uses}" '        id: filter' '        with:'
        if [[ -n "${with_extra}" ]]; then
            printf '%s\n' "${with_extra}"
        fi
        printf '%s\n' "${filters}"
    } > "${dir}/.github/workflows/app.yml"
}

# One positive and one negative case covering both baseline keys. The values
# are plain expectations; nothing here asserts that the patterns really match.
CASE_POS='{name: pos, workflow: app.yml, job: changes, step: filter, paths: [proj/a.txt, other/b.txt], expect: {proj: "true", other: "true"}}'
CASE_NEG='{name: neg, workflow: app.yml, job: changes, step: filter, paths: [unrelated/x.txt], expect: {proj: "false", other: "false"}}'

# write_cases <dir> <flow-style case>... -- writes the cases file.
write_cases() {
    local dir="$1" item
    shift
    {
        echo "cases:"
        for item in "$@"; do
            printf '  - %s\n' "${item}"
        done
    } > "${dir}/.github/scripts/paths-filter-cases.yml"
}

# new_case_dir <name> -- a fixture tree that passes `generate`: the self-test
# workflow, one app workflow with a single covered step, and a positive plus a
# negative case. Individual tests overwrite whatever they want to break.
# The directory is returned in the global CASE_DIR (never through a command
# substitution, which would swallow a failure in a subshell). A tree that was
# not built aborts the whole run: every later assertion would be meaningless.
CASE_DIR=""
new_case_dir() {
    CASE_DIR="${TEST_ROOT}/$1"
    mkdir -p "${CASE_DIR}/.github/workflows" "${CASE_DIR}/.github/scripts"
    write_selftest_workflow "${CASE_DIR}"
    write_app_workflow "${CASE_DIR}"
    write_cases "${CASE_DIR}" "${CASE_POS}" "${CASE_NEG}"
    if [[ ! -d "${CASE_DIR}/.github/workflows" || ! -s "${CASE_DIR}/.github/scripts/paths-filter-cases.yml" || ! -s "${CASE_DIR}/.github/workflows/app.yml" ]]; then
        fail "new_case_dir $1: the fixture tree under ${CASE_DIR} was not built"
        echo "paths-filter-semantics.test.sh: aborting, fixture tree '$1' could not be built" >&2
        exit 2
    fi
}

# ── runner and assertions ────────────────────────────────────────────────
# run_cmd <dir> <command...> -- runs the command with cwd <dir>. Sets
# RUN_EXIT, RUN_OUT (stdout) and RUN_ERR (stderr) separately: the matrix JSON
# must stay alone on stdout while every failure message goes to stderr.
run_cmd() {
    local dir="$1"
    shift
    : > "${TEST_ROOT}/last.stderr"
    RUN_OUT="$(cd "${dir}" && "$@" 2>"${TEST_ROOT}/last.stderr")"
    RUN_EXIT=$?
    RUN_ERR="$(cat "${TEST_ROOT}/last.stderr")"
}

run_gen() {
    run_cmd "$1" bash "${SEM}" generate
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${RUN_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${RUN_EXIT}, expected ${expected} (stderr: ${RUN_ERR})"
    fi
}

assert_err_has() {
    local label="$1" needle="$2"
    if [[ "${RUN_ERR}" == *"${needle}"* ]]; then
        pass
    else
        fail "${label}: stderr did not contain '${needle}' (got: ${RUN_ERR})"
    fi
}

assert_err_lacks() {
    local label="$1" needle="$2"
    if [[ "${RUN_ERR}" != *"${needle}"* ]]; then
        pass
    else
        fail "${label}: stderr unexpectedly contained '${needle}' (got: ${RUN_ERR})"
    fi
}

assert_out_empty() {
    local label="$1"
    if [[ -z "${RUN_OUT}" ]]; then
        pass
    else
        fail "${label}: stdout was not empty on failure (got: ${RUN_OUT})"
    fi
}

# assert_gen_fails <label> <dir> -- generate exited 1 with nothing on stdout.
assert_gen_fails() {
    assert_exit "$1" 1
    assert_out_empty "$1"
}

assert_jq() {
    local label="$1" filter="$2" input="$3"
    if jq -e "${filter}" >/dev/null 2>&1 <<<"${input}"; then
        pass
    else
        fail "${label}: jq '${filter}' was not true for: ${input}"
    fi
}

echo "paths-filter-semantics.test.sh"

# ═════════════════════════════ generate ═════════════════════════════════

# ── Happy path ───────────────────────────────────────────────────────────
echo "case: a covered step produces one matrix entry per case"
new_case_dir happy; D="${CASE_DIR}"
run_gen "${D}"
assert_exit "happy path" 0
assert_jq "happy: one entry per case" '.include | length == 2' "${RUN_OUT}"
assert_jq "happy: entry carries the coordinates" \
    '.include | all(.workflow == "app.yml" and .job == "changes" and .step == "filter")' "${RUN_OUT}"
assert_jq "happy: quantifier defaults to some" '.include | all(.quantifier == "some")' "${RUN_OUT}"
assert_jq "happy: entry carries the filters document" \
    '.include | all(.filters | contains("proj/**") and contains("other/**"))' "${RUN_OUT}"
assert_jq "happy: entry carries paths and expectations" \
    '.include | map(select(.case == "pos")) | .[0] | .paths == ["proj/a.txt", "other/b.txt"] and .expected == {proj: "true", other: "true"}' \
    "${RUN_OUT}"

# ── An explicit predicate-quantifier is passed through ───────────────────
echo "case: an explicit predicate-quantifier is carried into the entry"
new_case_dir quantifier-every; D="${CASE_DIR}"
write_app_workflow "${D}" "${PIN}" "          predicate-quantifier: 'every'"
run_gen "${D}"
assert_exit "explicit quantifier" 0
assert_jq "explicit quantifier is carried" '.include | all(.quantifier == "every")' "${RUN_OUT}"

# ── External filters file resolves repo-root-relative ────────────────────
echo "case: an external filters file is read and carried into the entry"
new_case_dir external-ok; D="${CASE_DIR}"
mkdir -p "${D}/.github/filters"
printf '%s\n' 'proj:' "  - 'proj/**'" 'other:' "  - 'other/**'" > "${D}/.github/filters/f.yml"
write_app_workflow "${D}" "${PIN}" "" "          filters: .github/filters/f.yml"
run_gen "${D}"
assert_exit "external filters file" 0
assert_jq "external filters file content is the entry's filters" \
    '.include | all(.filters | contains("proj/**"))' "${RUN_OUT}"

# ── Edge: step without id ────────────────────────────────────────────────
echo "case: a discovered step without id names the file, job and 0-based index"
new_case_dir no-id; D="${CASE_DIR}"
cat > "${D}/.github/workflows/app.yml" <<EOF
name: app
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: ${PIN}
        with:
          filters: |
            proj:
              - 'proj/**'
EOF
run_gen "${D}"
assert_gen_fails "step without id"
assert_err_has "no-id names the workflow file" "app.yml"
assert_err_has "no-id names the job" "'changes'"
assert_err_has "no-id names the 0-based step index" "step 1"

# ── Edge: step with no case ──────────────────────────────────────────────
echo "case: a discovered step with no case names the workflow, job and step id"
new_case_dir uncovered; D="${CASE_DIR}"
cat > "${D}/.github/workflows/second.yml" <<EOF
name: second
on:
  pull_request:
jobs:
  detect:
    runs-on: ubuntu-latest
    steps:
      - uses: ${PIN}
        id: second-step
        with:
          filters: |
            area:
              - 'area/**'
EOF
run_gen "${D}"
assert_gen_fails "uncovered step"
assert_err_has "uncovered names the workflow" "second.yml"
assert_err_has "uncovered names the job" "'detect'"
assert_err_has "uncovered names the step id" "'second-step'"
assert_err_lacks "uncovered does not name the covered step" "'filter'"

# ── Edge: stale case (workflow, job, step) ───────────────────────────────
echo "case: a case naming a missing workflow is stale and names the case"
new_case_dir stale-workflow; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: stale-wf-case, workflow: gone.yml, job: changes, step: filter, paths: [a.txt], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "stale workflow"
assert_err_has "stale workflow names the case" "stale-wf-case"
assert_err_has "stale workflow names the workflow" "gone.yml"

echo "case: a case naming a missing job is stale and names the case"
new_case_dir stale-job; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: stale-job-case, workflow: app.yml, job: gone-job, step: filter, paths: [a.txt], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "stale job"
assert_err_has "stale job names the case" "stale-job-case"
assert_err_has "stale job names the job" "gone-job"

echo "case: a case naming a missing step id is stale and names the case"
new_case_dir stale-step; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: stale-step-case, workflow: app.yml, job: changes, step: gone-step, paths: [a.txt], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "stale step"
assert_err_has "stale step names the case" "stale-step-case"
assert_err_has "stale step names the step id" "gone-step"

# ── Edge: unknown / missing / non-string expectations ────────────────────
echo "case: a case expecting a key the step does not define names the case and key"
new_case_dir unknown-key; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: unknown-key-case, workflow: app.yml, job: changes, step: filter, paths: [a.txt], expect: {proj: "true", other: "true", ghost: "true"}}'
run_gen "${D}"
assert_gen_fails "unknown key"
assert_err_has "unknown key names the case" "unknown-key-case"
assert_err_has "unknown key names the key" "ghost"

echo "case: a case omitting a key of its step names the case and the missing key"
new_case_dir missing-key; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: missing-key-case, workflow: app.yml, job: changes, step: filter, paths: [a.txt], expect: {proj: "true"}}'
run_gen "${D}"
assert_gen_fails "missing expectation"
assert_err_has "missing expectation names the case" "missing-key-case"
assert_err_has "missing expectation names the key" "other"

echo "case: an unquoted YAML boolean expectation is rejected and says to quote it"
new_case_dir unquoted-bool; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: bool-case, workflow: app.yml, job: changes, step: filter, paths: [a.txt], expect: {proj: true, other: "true"}}'
run_gen "${D}"
assert_gen_fails "unquoted boolean"
assert_err_has "unquoted boolean names the case" "bool-case"
assert_err_has "unquoted boolean tells the author to quote" "quote"

# ── Edge: unsafe or malformed changed paths ──────────────────────────────
# Each variant replaces the single changed path of an otherwise valid case.
check_bad_path() {
    local label="$1" path_item="$2" needle="$3" dir
    new_case_dir "bad-path-${label}"
    dir="${CASE_DIR}"
    write_cases "${dir}" "${CASE_POS}" "${CASE_NEG}" \
        "{name: bad-path-case, workflow: app.yml, job: changes, step: filter, paths: [${path_item}], expect: {proj: \"true\", other: \"true\"}}"
    run_gen "${dir}"
    assert_gen_fails "path ${label}"
    assert_err_has "path ${label} names the case" "bad-path-case"
    assert_err_has "path ${label} names the problem" "${needle}"
}
echo "case: unsafe changed paths are rejected with the case named"
check_bad_path "absolute" '"/etc/passwd"' "starts with '/'"
check_bad_path "traversal" '"proj/../../etc/passwd"' "contains '..'"
check_bad_path "git-segment" '"proj/.git/config"' "has a .git segment"
check_bad_path "empty" '""' "empty"
check_bad_path "control-char" '"proj/a\tb.txt"' "control"
check_bad_path "dot" '"."' "'.' segment"
check_bad_path "dot-segment" '"./proj/a.txt"' "'.' segment"
check_bad_path "trailing-slash" '"proj/"' "ends with '/'"
check_bad_path "double-slash" '"proj//a.txt"' "contains '//'"

echo "case: a path that is also a directory of another path is rejected with the case named"
new_case_dir path-collision; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: collision-case, workflow: app.yml, job: changes, step: filter, paths: [proj, proj/a.txt], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "file/directory collision"
assert_err_has "collision names the case" "collision-case"
assert_err_has "collision names the colliding path" "\"proj\""
assert_err_has "collision says it is also a directory" "also a directory"

echo "case: a case with an empty paths list is rejected"
new_case_dir empty-paths; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: no-paths-case, workflow: app.yml, job: changes, step: filter, paths: [], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "empty paths list"
assert_err_has "empty paths list names the case" "no-paths-case"

# ── Edge: external filters file guards ───────────────────────────────────
echo "case: an absolute external filters path is rejected"
new_case_dir external-absolute; D="${CASE_DIR}"
write_app_workflow "${D}" "${PIN}" "" "          filters: /etc/filters.yml"
run_gen "${D}"
assert_gen_fails "absolute filters path"
assert_err_has "absolute filters path names the path" "/etc/filters.yml"
assert_err_has "absolute filters path names the workflow" "app.yml"

echo "case: a traversing external filters path is rejected"
new_case_dir external-traversal; D="${CASE_DIR}"
write_app_workflow "${D}" "${PIN}" "" "          filters: ../outside/filters.yml"
run_gen "${D}"
assert_gen_fails "traversing filters path"
assert_err_has "traversing filters path names the path" "../outside/filters.yml"

echo "case: a missing external filters file fails"
new_case_dir external-missing; D="${CASE_DIR}"
write_app_workflow "${D}" "${PIN}" "" "          filters: .github/filters/does-not-exist.yml"
run_gen "${D}"
assert_gen_fails "missing filters file"
assert_err_has "missing filters file names the file" "does-not-exist.yml"

# ── Edge: malformed YAML ─────────────────────────────────────────────────
echo "case: a malformed workflow file fails naming the file"
new_case_dir malformed-workflow; D="${CASE_DIR}"
printf '%s\n' 'name: broken' 'jobs: [unterminated' > "${D}/.github/workflows/broken.yml"
run_gen "${D}"
assert_gen_fails "malformed workflow"
assert_err_has "malformed workflow names the file" "broken.yml"

echo "case: a malformed cases file fails naming the file"
new_case_dir malformed-cases; D="${CASE_DIR}"
printf '%s\n' 'cases: [unterminated' > "${D}/.github/scripts/paths-filter-cases.yml"
run_gen "${D}"
assert_gen_fails "malformed cases file"
assert_err_has "malformed cases file names the file" "paths-filter-cases.yml"

echo "case: an empty cases file fails naming the file instead of silently skipping validation"
new_case_dir empty-cases; D="${CASE_DIR}"
: > "${D}/.github/scripts/paths-filter-cases.yml"
run_gen "${D}"
assert_gen_fails "empty cases file"
assert_err_has "empty cases file names the file and says it parsed to empty output" "paths-filter-cases.yml: parsed to empty output"

echo "case: a comments-only cases file fails naming the file"
new_case_dir comments-cases; D="${CASE_DIR}"
printf '%s\n' '# nothing but a comment' '# cases: []' > "${D}/.github/scripts/paths-filter-cases.yml"
run_gen "${D}"
assert_gen_fails "comments-only cases file"
assert_err_has "comments-only cases file names the file and says it parsed to empty output" "paths-filter-cases.yml: parsed to empty output"

echo "case: a yq that prints nothing for the cases file and exits 0 still fails naming the file"
new_case_dir silent-yq; D="${CASE_DIR}"
mkdir -p "${TEST_ROOT}/silent-yq-bin"
REAL_YQ="$(command -v yq)"
cat > "${TEST_ROOT}/silent-yq-bin/yq" <<EOF
#!/usr/bin/env bash
for last; do :; done
if [[ "\${last}" == *paths-filter-cases.yml ]]; then exit 0; fi
exec "${REAL_YQ}" "\$@"
EOF
chmod +x "${TEST_ROOT}/silent-yq-bin/yq"
run_cmd "${D}" env PATH="${TEST_ROOT}/silent-yq-bin:${PATH}" bash "${SEM}" generate
assert_gen_fails "silent yq on the cases file"
assert_err_has "silent yq names the cases file" "paths-filter-cases.yml: parsed to empty output"

echo "case: a missing cases file fails naming the file"
new_case_dir missing-cases; D="${CASE_DIR}"
rm -f "${D}/.github/scripts/paths-filter-cases.yml"
run_gen "${D}"
assert_gen_fails "missing cases file"
assert_err_has "missing cases file names the file and says it is missing" "paths-filter-cases.yml is missing or unreadable"

# ── Edge: pin drift ──────────────────────────────────────────────────────
echo "case: a step pinned to a different ref than the self-test fails naming both refs and the workflow"
new_case_dir pin-drift; D="${CASE_DIR}"
write_app_workflow "${D}" "${OTHER_PIN}"
run_gen "${D}"
assert_gen_fails "pin drift"
assert_err_has "pin drift names the drifting ref" "${OTHER_PIN}"
assert_err_has "pin drift names the self-test ref" "${PIN}"
assert_err_has "pin drift names the workflow" "app.yml"

echo "case: an unpinned step also counts as drift"
new_case_dir pin-drift-unpinned; D="${CASE_DIR}"
write_app_workflow "${D}" "dorny/paths-filter"
run_gen "${D}"
assert_gen_fails "unpinned step"
assert_err_has "unpinned step names the workflow" "app.yml"

# ── Edge: per-key coverage ───────────────────────────────────────────────
echo "case: a key with no positive case fails naming the step and key"
new_case_dir no-positive; D="${CASE_DIR}"
write_cases "${D}" \
    '{name: pos-proj-only, workflow: app.yml, job: changes, step: filter, paths: [proj/a.txt], expect: {proj: "true", other: "false"}}' \
    '{name: neg-all, workflow: app.yml, job: changes, step: filter, paths: [unrelated/x.txt], expect: {proj: "false", other: "false"}}'
run_gen "${D}"
assert_gen_fails "no positive case"
assert_err_has "no positive names the step" "'filter'"
assert_err_has "no positive names the key" "'other'"
assert_err_has "no positive says which polarity is missing" "has no case expecting 'true'"
assert_err_lacks "no positive does not blame the covered key" "'proj'"

echo "case: a key with no negative case fails naming the step and key"
new_case_dir no-negative; D="${CASE_DIR}"
write_cases "${D}" \
    '{name: pos-all, workflow: app.yml, job: changes, step: filter, paths: [proj/a.txt, other/b.txt], expect: {proj: "true", other: "true"}}' \
    '{name: neg-proj-only, workflow: app.yml, job: changes, step: filter, paths: [unrelated/x.txt], expect: {proj: "false", other: "true"}}'
run_gen "${D}"
assert_gen_fails "no negative case"
assert_err_has "no negative names the step" "'filter'"
assert_err_has "no negative names the key" "'other'"
assert_err_has "no negative says which polarity is missing" "has no case expecting 'false'"
assert_err_lacks "no negative does not blame the covered key" "'proj'"

# ── Edge: matrix size limit ──────────────────────────────────────────────
# write_many_cases <dir> <count> -- the positive case plus count-1 uniquely
# named negative cases.
write_many_cases() {
    local dir="$1" count="$2" i
    {
        echo "cases:"
        printf '  - %s\n' "${CASE_POS}"
        for ((i = 1; i < count; i++)); do
            printf '  - {name: neg-%s, workflow: app.yml, job: changes, step: filter, paths: [unrelated/x.txt], expect: {proj: "false", other: "false"}}\n' "${i}"
        done
    } > "${dir}/.github/scripts/paths-filter-cases.yml"
}

echo "case: more than 256 entries fail naming the GitHub matrix limit"
new_case_dir matrix-257; D="${CASE_DIR}"
write_many_cases "${D}" 257
run_gen "${D}"
assert_gen_fails "257 entries"
assert_err_has "257 entries mention the matrix limit" "256"
assert_err_has "257 entries mention the matrix" "matrix"

echo "case: exactly 256 entries are accepted"
new_case_dir matrix-256; D="${CASE_DIR}"
write_many_cases "${D}" 256
run_gen "${D}"
assert_exit "256 entries" 0
assert_jq "256 entries emitted" '.include | length == 256' "${RUN_OUT}"

# ── Edge: self-test step exclusion ───────────────────────────────────────
echo "case: the self-test step needs no case and is not part of the matrix"
new_case_dir selftest-excluded; D="${CASE_DIR}"
run_gen "${D}"
assert_exit "self-test step has no case" 0
assert_jq "self-test step is not in the matrix" \
    '.include | all(.workflow != "workflow-lint.yml" and .step != "semantics")' "${RUN_OUT}"

echo "case: a case targeting the self-test step is rejected naming the case"
new_case_dir selftest-targeted; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: selftest-case, workflow: workflow-lint.yml, job: paths-filter-semantics-run, step: semantics, paths: [a.txt], expect: {proj: "true"}}'
run_gen "${D}"
assert_gen_fails "case targets the self-test step"
assert_err_has "self-test case names the case" "selftest-case"
assert_err_has "self-test case says the step is excluded" "self-test"

echo "case: a workflow without the self-test step fails"
new_case_dir selftest-missing; D="${CASE_DIR}"
printf '%s\n' 'name: lint' 'on:' '  pull_request:' 'jobs:' '  other:' '    runs-on: ubuntu-latest' '    steps:' '      - run: echo hi' > "${D}/.github/workflows/workflow-lint.yml"
run_gen "${D}"
assert_gen_fails "missing self-test step"
assert_err_has "missing self-test step names the job" "paths-filter-semantics-run"

# ── Case-name hygiene ────────────────────────────────────────────────────
echo "case: duplicate case names are rejected"
new_case_dir duplicate-name; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" "${CASE_NEG}"
run_gen "${D}"
assert_gen_fails "duplicate case name"
assert_err_has "duplicate case name is named" "case 'neg': duplicate case name"

echo "case: a case name outside [A-Za-z0-9._-] is rejected"
new_case_dir bad-name; D="${CASE_DIR}"
write_cases "${D}" "${CASE_POS}" "${CASE_NEG}" \
    '{name: "bad name;rm", workflow: app.yml, job: changes, step: filter, paths: [a.txt], expect: {proj: "true", other: "true"}}'
run_gen "${D}"
assert_gen_fails "bad case name"
assert_err_has "bad case name is reported by the name rule" "name must match ^[A-Za-z0-9._-]+\$"

# ── Discovery guard ──────────────────────────────────────────────────────
echo "case: a tree with no paths-filter step to check fails"
new_case_dir nothing-discovered; D="${CASE_DIR}"
rm -f "${D}/.github/workflows/app.yml"
run_gen "${D}"
assert_gen_fails "nothing discovered"
assert_err_has "nothing discovered says so" "discovered"

# ═════════════════════════════ setup ════════════════════════════════════

# entry_json <paths-json-array> -- a matrix entry as CASE_JSON carries it.
entry_json() {
    jq -nc --argjson paths "$1" --arg filters $'proj:\n  - \'proj/**\'\n' \
        '{case: "setup-case", workflow: "app.yml", job: "changes", step: "filter", quantifier: "some", filters: $filters, paths: $paths, expected: {proj: "true"}}'
}

echo "case: setup builds a scratch repo whose head commit adds exactly the listed paths"
WS="${TEST_ROOT}/ws-ok"
mkdir -p "${WS}"
: > "${TEST_ROOT}/ws-ok.out"
run_cmd "${TEST_ROOT}" env GITHUB_WORKSPACE="${WS}" GITHUB_OUTPUT="${TEST_ROOT}/ws-ok.out" \
    CASE_JSON="$(entry_json '["proj/a.txt","other/deep/b.txt"]')" bash "${SEM}" setup
assert_exit "setup" 0
WD="$(sed -n 's/^working_directory=//p' "${TEST_ROOT}/ws-ok.out")"
BASE="$(sed -n 's/^base_sha=//p' "${TEST_ROOT}/ws-ok.out")"
HEAD_SHA="$(sed -n 's/^head_sha=//p' "${TEST_ROOT}/ws-ok.out")"
FILTERS_FILE="$(sed -n 's/^filters_file=//p' "${TEST_ROOT}/ws-ok.out")"
if [[ -n "${WD}" && "${WD}" != /* && -d "${WS}/${WD}/.git" ]]; then pass; else fail "setup: working_directory '${WD}' is not a relative scratch repo under the workspace"; fi
if [[ -n "${BASE}" && -n "${HEAD_SHA}" && "${BASE}" != "${HEAD_SHA}" ]]; then pass; else fail "setup: base_sha '${BASE}' and head_sha '${HEAD_SHA}' must be distinct commits"; fi
CHANGED="$(git -C "${WS}/${WD}" diff --name-only "${BASE}" "${HEAD_SHA}" 2>&1 | sort | tr '\n' ' ')"
if [[ "${CHANGED}" == "other/deep/b.txt proj/a.txt " ]]; then pass; else fail "setup: base..head changed '${CHANGED}', expected the two listed paths"; fi
if git -C "${WS}/${WD}" merge-base --is-ancestor "${BASE}" "${HEAD_SHA}"; then pass; else fail "setup: base commit is not an ancestor of head"; fi
if [[ "${FILTERS_FILE}" == /* && -f "${FILTERS_FILE}" ]] && grep -q "proj/\*\*" "${FILTERS_FILE}"; then pass; else fail "setup: filters_file '${FILTERS_FILE}' is not an absolute file holding the entry's filters"; fi
if git -C "${WS}/${WD}" remote get-url origin >/dev/null 2>&1; then pass; else fail "setup: scratch repo has no origin remote"; fi

echo "case: setup rejects an unsafe path before creating anything"
for bad in '["/etc/passwd"]' '["proj/../../x"]' '["proj/.git/config"]' '[""]' '[]' '["."]' '["proj/"]' '["proj//a.txt"]' '["proj","proj/a.txt"]'; do
    WS="${TEST_ROOT}/ws-bad"
    rm -rf "${WS}"
    mkdir -p "${WS}"
    : > "${TEST_ROOT}/ws-bad.out"
    run_cmd "${TEST_ROOT}" env GITHUB_WORKSPACE="${WS}" GITHUB_OUTPUT="${TEST_ROOT}/ws-bad.out" \
        CASE_JSON="$(entry_json "${bad}")" bash "${SEM}" setup
    assert_exit "setup rejects ${bad}" 1
    if [[ -z "$(ls -A "${WS}")" ]]; then pass; else fail "setup ${bad}: workspace was not left empty ($(ls -A "${WS}"))"; fi
    if [[ ! -s "${TEST_ROOT}/ws-bad.out" ]]; then pass; else fail "setup ${bad}: wrote step outputs despite the rejection"; fi
done

echo "case: setup fails closed when the committed changeset differs from the declared paths"
# A global excludes file makes `git add -A` silently skip ignored.txt, so the
# head commit would carry fewer paths than the case declares and a negative case
# could pass vacuously.
printf '%s\n' 'ignored.txt' > "${TEST_ROOT}/global-excludes"
WS="${TEST_ROOT}/ws-mismatch"
mkdir -p "${WS}"
: > "${TEST_ROOT}/ws-mismatch.out"
run_cmd "${TEST_ROOT}" env GITHUB_WORKSPACE="${WS}" GITHUB_OUTPUT="${TEST_ROOT}/ws-mismatch.out" \
    GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.excludesFile GIT_CONFIG_VALUE_0="${TEST_ROOT}/global-excludes" \
    CASE_JSON="$(entry_json '["proj/a.txt","ignored.txt"]')" bash "${SEM}" setup
assert_exit "setup with an ignored path" 1
assert_err_has "setup mismatch names the path that was not committed" "ignored.txt"
assert_err_has "setup mismatch says the changeset differs" "differs from the declared paths"
if [[ ! -s "${TEST_ROOT}/ws-mismatch.out" ]]; then pass; else fail "setup mismatch: wrote step outputs despite the mismatch"; fi

echo "case: setup without CASE_JSON fails"
WS="${TEST_ROOT}/ws-nocase"
mkdir -p "${WS}"
run_cmd "${TEST_ROOT}" env -u CASE_JSON GITHUB_WORKSPACE="${WS}" GITHUB_OUTPUT="${TEST_ROOT}/ws-nocase.out" bash "${SEM}" setup
assert_exit "setup without CASE_JSON" 1
assert_err_has "setup without CASE_JSON names the variable" "CASE_JSON"

# ═════════════════════════════ assert ═══════════════════════════════════

ASSERT_CASE='{"case":"assert-case","workflow":"app.yml","job":"changes","step":"filter","quantifier":"some","expected":{"proj":"true","other":"false"}}'

run_assert() {
    run_cmd "${TEST_ROOT}" env CASE_JSON="$1" ACTUAL_JSON="$2" bash "${SEM}" assert
}

echo "case: assert passes when every expected key matches"
run_assert "${ASSERT_CASE}" '{"proj":"true","other":"false","changes":"true","proj_count":"1"}'
assert_exit "assert match" 0

echo "case: assert fails on a missing output key and never reads it as false"
run_assert "${ASSERT_CASE}" '{"proj":"true"}'
assert_exit "assert missing key" 1
assert_err_has "missing key names the workflow" "app.yml"
assert_err_has "missing key names the job" "changes"
assert_err_has "missing key names the step" "filter"
assert_err_has "missing key names the case" "assert-case"
assert_err_has "missing key names the key" "other"
assert_err_has "missing key is reported as missing" "missing"

echo "case: assert fails on an empty output value even when 'false' is expected"
run_assert "${ASSERT_CASE}" '{"proj":"true","other":""}'
assert_exit "assert empty value" 1
assert_err_has "empty value names the key" "other"
assert_err_has "empty value is reported as empty" "empty"

echo "case: assert reports a mismatch naming workflow, job, step, case and key"
run_assert "${ASSERT_CASE}" '{"proj":"false","other":"false"}'
assert_exit "assert mismatch" 1
assert_err_has "mismatch names the workflow" "app.yml"
assert_err_has "mismatch names the job" "changes"
assert_err_has "mismatch names the step" "filter"
assert_err_has "mismatch names the case" "assert-case"
assert_err_has "mismatch names the key" "proj"
assert_err_has "mismatch shows the expected value" "expected 'true'"
assert_err_has "mismatch shows the actual value" "got 'false'"
assert_err_lacks "a matching key is not reported" "key 'other'"

echo "case: assert accumulates every failing key in one run"
run_assert "${ASSERT_CASE}" '{"proj":"false","other":"true"}'
assert_exit "assert accumulates" 1
assert_err_has "accumulates the first key" "key 'proj'"
assert_err_has "accumulates the second key" "key 'other'"

echo "case: assert without ACTUAL_JSON fails"
run_cmd "${TEST_ROOT}" env -u ACTUAL_JSON CASE_JSON="${ASSERT_CASE}" bash "${SEM}" assert
assert_exit "assert without ACTUAL_JSON" 1
assert_err_has "assert without ACTUAL_JSON names the variable" "ACTUAL_JSON"

# ═════════════════ real repository and workflow wiring ══════════════════

echo "case: generate against the real repository succeeds with one entry per committed case"
run_cmd "${REPO_ROOT}" bash "${SEM}" generate
REAL_OUT="${RUN_OUT}"
assert_exit "real repo generate" 0
CASE_COUNT="$(yq '.cases | length' "${REPO_ROOT}/.github/scripts/paths-filter-cases.yml")"
assert_jq "real repo: entry count equals case count" ".include | length == ${CASE_COUNT}" "${REAL_OUT}"
assert_jq "real repo: every entry has filters and a quantifier" \
    '.include | all((.filters | length > 0) and (.quantifier | length > 0))' "${REAL_OUT}"

echo "case: the committed cases include the #950 isolation property for every exclusion step"
for root in flow watch sandbox; do
    assert_jq "${root}: its excluded AGENTS.md alone expects false" \
        ".include | any(.workflow == \"${root}-ci.yml\" and .step == \"code\" and .quantifier == \"every\" and .paths == [\"${root}/AGENTS.md\"] and .expected.${root} == \"false\")" "${REAL_OUT}"
    assert_jq "${root}: a non-excluded file under the root expects true" \
        ".include | any(.workflow == \"${root}-ci.yml\" and .step == \"code\" and .expected.${root} == \"true\" and (.paths | all(startswith(\"${root}/\") and (endswith(\"AGENTS.md\") | not))))" "${REAL_OUT}"
done

# wf_value <yq expression> -- evaluates against the real workflow-lint.yml.
wf_value() {
    yq "$1" "${REAL_WORKFLOW}" 2>&1
}

assert_wf() {
    local label="$1" expr="$2" expected="$3" actual
    actual="$(wf_value "${expr}")"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: '${expr}' gave '${actual}', expected '${expected}'"
    fi
}

echo "case: workflow-lint.yml gates the generator and the matrix on the workflows output so both report skipped when it is false"
GATE="needs.changes.outputs.workflows == 'true'"
for job in paths-filter-semantics-generate paths-filter-semantics-run; do
    assert_wf "${job} is gated" ".jobs[\"${job}\"].if | contains(\"${GATE}\")" "true"
done
for job in paths-filter-semantics-generate paths-filter-semantics-run paths-filter-semantics; do
    assert_wf "${job} needs the changes job" "[.jobs[\"${job}\"].needs] | flatten | contains([\"changes\"])" "true"
    assert_wf "${job} declares job-level contents: read" ".jobs[\"${job}\"].permissions.contents" "read"
    assert_wf "${job} declares no permission beyond contents" ".jobs[\"${job}\"].permissions | length" "1"
done
for job in paths-filter-semantics-generate paths-filter-semantics-run; do
    assert_wf "${job} checkout does not persist credentials" \
        "[.jobs[\"${job}\"].steps[] | select((.uses // \"\") | test(\"^actions/checkout@\")) | .with.\"persist-credentials\"] | (length == 1 and .[0] == false)" "true"
done
assert_wf "the matrix job waits for the generator" \
    '.jobs["paths-filter-semantics-run"].needs | contains(["paths-filter-semantics-generate"])' "true"
assert_wf "the matrix job does not stop at the first failing case" \
    '.jobs["paths-filter-semantics-run"].strategy["fail-fast"]' "false"
assert_wf "the matrix comes from the generator output" \
    '.jobs["paths-filter-semantics-run"].strategy.matrix | contains("fromJSON(needs.paths-filter-semantics-generate.outputs.matrix)")' "true"
assert_wf "the aggregator carries the required check name" '.jobs["paths-filter-semantics"].name' "paths-filter-semantics"
assert_wf "the aggregator runs even when upstream jobs fail and is never skipped by the workflows gate" \
    '.jobs["paths-filter-semantics"].if == "${{ always() }}"' "true"
assert_wf "the aggregator receives the changes result through env" \
    '.jobs["paths-filter-semantics"].steps[] | select(has("run")) | .env.CHANGES_RESULT == "${{ needs.changes.result }}"' "true"
assert_wf "the aggregator receives the workflows output through env" \
    '.jobs["paths-filter-semantics"].steps[] | select(has("run")) | .env.WORKFLOWS == "${{ needs.changes.outputs.workflows }}"' "true"
assert_wf "the aggregator waits for the generator and the matrix" \
    '.jobs["paths-filter-semantics"].needs | contains(["paths-filter-semantics-generate", "paths-filter-semantics-run"])' "true"

# ── Aggregator behavior ──────────────────────────────────────────────────
# The aggregator's real run: script is extracted from the committed workflow and
# executed with the job results GitHub would put in its env. GitHub's own
# expression evaluation (needs.*.result, always(), the workflows gate) is not
# modelled here; the structure contract above pins those wires.
#
# Documented behavior (plan Q&A 10, tightened in the review round): the
# aggregator job always runs (`if: always()`), because a SKIPPED required check
# counts as passing for branch protection and a failed or cancelled `changes`
# job would otherwise skip it (its `workflows` output is then empty). Its script
# decides: `changes` must be `success`, otherwise the check fails naming
# `changes` and its result. With `changes` green and `workflows == 'false'` the
# generator and matrix are legitimately skipped, so there is nothing to test and
# it passes. With `workflows == 'true'` only `success` of the generator and the
# matrix passes; a skipped upstream job there means the generator failed. Any
# other `workflows` value (empty, 'maybe', 'True') fails closed naming the value.
AGG_SCRIPT="${TEST_ROOT}/aggregator.sh"
AGG_STEP_COUNT="$(yq '[.jobs["paths-filter-semantics"].steps[] | select(has("run"))] | length' "${REAL_WORKFLOW}" 2>&1)"
if [[ "${AGG_STEP_COUNT}" == "1" ]] \
    && yq '.jobs["paths-filter-semantics"].steps[] | select(has("run")) | .run' "${REAL_WORKFLOW}" > "${AGG_SCRIPT}" 2>"${TEST_ROOT}/agg.stderr" \
    && [[ -s "${AGG_SCRIPT}" ]]; then
    pass
    # run_agg_with <changes result> <workflows output> <generate result> <run result>
    run_agg_with() {
        run_cmd "${TEST_ROOT}" env CHANGES_RESULT="$1" WORKFLOWS="$2" GENERATE_RESULT="$3" RUN_RESULT="$4" bash "${AGG_SCRIPT}"
    }
    # run_agg <generate result> <run result> -- the gate is open (changes green, workflows true).
    run_agg() {
        run_agg_with success true "$1" "$2"
    }

    echo "case: the aggregator fails when the changes job fails and names it and its result"
    run_agg_with failure "" skipped skipped
    assert_exit "aggregator changes failure" 1
    assert_err_has "aggregator changes failure names the changes job and result" "changes result: failure"

    echo "case: the aggregator fails when the changes job is cancelled"
    run_agg_with cancelled "" skipped skipped
    assert_exit "aggregator changes cancelled" 1
    assert_err_has "aggregator changes cancelled names the changes job and result" "changes result: cancelled"

    echo "case: the aggregator fails when the changes job failed even if its workflows output claims false"
    run_agg_with failure false skipped skipped
    assert_exit "aggregator changes failure with workflows=false" 1
    assert_err_has "aggregator changes failure with workflows=false names the result" "changes result: failure"

    echo "case: the aggregator passes when changes succeeded, workflows is false and the upstream jobs are skipped"
    run_agg_with success false skipped skipped
    assert_exit "aggregator workflows=false" 0
    assert_err_lacks "aggregator workflows=false is silent about failures" "result:"

    echo "case: the aggregator fails closed when changes succeeded but the workflows output is empty"
    run_agg_with success "" skipped skipped
    assert_exit "aggregator workflows empty" 1
    assert_err_has "aggregator workflows empty names the bad value" "workflows output is '', expected true or false"

    echo "case: the aggregator fails closed on an unrecognised workflows output"
    run_agg_with success maybe skipped skipped
    assert_exit "aggregator workflows=maybe" 1
    assert_err_has "aggregator workflows=maybe names the bad value" "workflows output is 'maybe', expected true or false"

    echo "case: the aggregator accepts only the exact lowercase strings true and false"
    run_agg_with success True skipped skipped
    assert_exit "aggregator workflows=True" 1
    assert_err_has "aggregator workflows=True names the bad value" "workflows output is 'True', expected true or false"

    echo "case: the aggregator still checks the upstream jobs when workflows is true"
    run_agg_with success true skipped skipped
    assert_exit "aggregator workflows=true with skipped upstream" 1
    assert_err_has "aggregator workflows=true with skipped upstream names the generator" "paths-filter-semantics-generate"

    echo "case: the aggregator passes when the generator and the matrix both succeed"
    run_agg success success
    assert_exit "aggregator success/success" 0
    assert_err_lacks "aggregator success/success is silent about failures" "result:"

    echo "case: the aggregator fails when the matrix job fails and names that job and result"
    run_agg success failure
    assert_exit "aggregator run failure" 1
    assert_err_has "aggregator run failure names the matrix job" "paths-filter-semantics-run"
    assert_err_has "aggregator run failure names the result" "failure"
    assert_err_lacks "aggregator run failure does not blame the generator" "paths-filter-semantics-generate"

    echo "case: the aggregator fails when the matrix job is cancelled and names the result"
    run_agg success cancelled
    assert_exit "aggregator run cancelled" 1
    assert_err_has "aggregator run cancelled names the matrix job" "paths-filter-semantics-run"
    assert_err_has "aggregator run cancelled names the result" "cancelled"

    echo "case: the aggregator fails when the generator fails and names that job and result"
    run_agg failure skipped
    assert_exit "aggregator generator failure" 1
    assert_err_has "aggregator generator failure names the generator" "paths-filter-semantics-generate"
    assert_err_has "aggregator generator failure names the result" "failure"

    echo "case: the aggregator fails when only the generator fails"
    run_agg failure success
    assert_exit "aggregator generator-only failure" 1
    assert_err_has "aggregator generator-only failure names the generator" "paths-filter-semantics-generate"
    assert_err_lacks "aggregator generator-only failure does not blame the matrix job" "paths-filter-semantics-run"

    echo "case: the aggregator does not count a skipped matrix job as passing"
    run_agg success skipped
    assert_exit "aggregator run skipped" 1
    assert_err_has "aggregator run skipped names the matrix job" "paths-filter-semantics-run"
    assert_err_has "aggregator run skipped names the result" "skipped"

    echo "case: the aggregator does not count a skipped generator as passing"
    run_agg skipped success
    assert_exit "aggregator generator skipped" 1
    assert_err_has "aggregator generator skipped names the generator" "paths-filter-semantics-generate"
    assert_err_has "aggregator generator skipped names the result" "skipped"

    echo "case: the aggregator reports both jobs when both fail"
    run_agg failure cancelled
    assert_exit "aggregator both fail" 1
    assert_err_has "aggregator both fail names the generator" "paths-filter-semantics-generate"
    assert_err_has "aggregator both fail names the matrix job" "paths-filter-semantics-run"
else
    fail "aggregator: could not extract a single run: step from the paths-filter-semantics job (count '${AGG_STEP_COUNT}', stderr: $(cat "${TEST_ROOT}/agg.stderr" 2>/dev/null))"
fi

echo "case: the self-test step uses the same pinned ref as the changes job and an empty token"
PINNED_REAL="$(wf_value '.jobs.changes.steps[] | select(.id == "filter") | .uses')"
assert_wf "self-test pin equals the changes job pin" \
    ".jobs[\"paths-filter-semantics-run\"].steps[] | select(.id == \"semantics\") | .uses == \"${PINNED_REAL}\"" "true"
assert_wf "self-test token is empty" \
    '.jobs["paths-filter-semantics-run"].steps[] | select(.id == "semantics") | .with.token == ""' "true"

echo "case: check-permissions runs this test file"
assert_wf "check-permissions wires the semantics unit tests" \
    '[.jobs["check-permissions"].steps[].run // ""] | any_c(contains("paths-filter-semantics.test.sh"))' "true"

# ── Summary ──────────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
