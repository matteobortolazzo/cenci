#!/usr/bin/env bash
# Tests for check-paths-filters.sh (ticket #950). Follows the same precedent
# as check-version-bump-concurrency.test.sh: plain bash, no framework,
# PASS/FAIL counters, non-zero exit on any failure. Each case builds a fresh
# throwaway .github/workflows/ fixture tree under one mktemp root and runs
# the script with cwd set to that tree (the script reads relative
# `.github/workflows` paths, per the guard's Rule 2 discovery guard, which
# mirrors check-version-bump-concurrency.sh's rule 4).
#
# These fixtures assert STRUCTURAL properties of the guard's own output
# (exit code, which file/job/step/pattern gets named) against synthetic
# workflow YAML -- they never simulate picomatch's actual path-matching
# semantics. Modeling matching outcomes in bash inside this file would
# assert a model of dorny/paths-filter's semantics rather than the
# semantics themselves -- precisely the mistake that created the #950 bug
# (see the plan's "Rejected: simulate the picomatch matcher" alternative).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-paths-filters.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/check-paths-filters-test.XXXXXX")" || {
    echo "check-paths-filters.test.sh: failed to create fixture root" >&2
    exit 2
}
trap 'rm -rf "${TEST_ROOT}"' EXIT

# new_case_dir <name> -- creates <TEST_ROOT>/<name>/.github/workflows and
# echoes the case root path.
new_case_dir() {
    local name="$1"
    local dir="${TEST_ROOT}/${name}"
    mkdir -p "${dir}/.github/workflows"
    printf '%s' "${dir}"
}

# run_check <dir> -- runs check-paths-filters.sh with cwd set to <dir>.
# Sets CHECK_EXIT and CHECK_OUTPUT (combined stdout+stderr, since the
# guard's discovery-count reporting and its per-offender failure messages
# are not assumed to land on any single specific stream).
run_check() {
    local dir="$1"
    CHECK_OUTPUT="$(cd "${dir}" && bash "${CHECK_SH}" 2>&1)"
    CHECK_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${CHECK_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${CHECK_EXIT}, expected ${expected} (output: ${CHECK_OUTPUT})"
    fi
}

assert_contains() {
    local label="$1" needle="$2"
    if [[ "${CHECK_OUTPUT}" == *"${needle}"* ]]; then
        pass
    else
        fail "${label}: output did not contain '${needle}' (got: ${CHECK_OUTPUT})"
    fi
}

assert_not_contains() {
    local label="$1" needle="$2"
    if [[ "${CHECK_OUTPUT}" != *"${needle}"* ]]; then
        pass
    else
        fail "${label}: output unexpectedly contained '${needle}' (got: ${CHECK_OUTPUT})"
    fi
}

echo "check-paths-filters.test.sh"

# ── Case 1: negation pattern under default `some` → exit 1 ─────────────
# Rule 1: a dorny/paths-filter step whose filters yields a '!'-prefixed
# pattern must set predicate-quantifier: 'every'. No predicate-quantifier
# is set here, so the default ('some') applies and the step must fail.
# The failure message must name the file, the job key, the step index,
# and the offending pattern.
echo "case: negation pattern under default quantifier (some) must fail"
CASE1="$(new_case_dir case1-negation-some)"
cat > "${CASE1}/.github/workflows/case1-negation-some.yml" <<'EOF'
name: case1-negation-some
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: |
            project:
              - 'project/**'
              - '!project/AGENTS.md'
EOF
run_check "${CASE1}"
assert_exit "case1 negation under some" 1
assert_contains "case1 message names the file" "case1-negation-some.yml"
assert_contains "case1 message names the job" "changes"
assert_contains "case1 message names the step index" "step 0"
assert_contains "case1 message names the offending pattern" "!project/AGENTS.md"

# ── Case 2: same negation with predicate-quantifier: 'every' → exit 0 ──
echo "case: same negation with predicate-quantifier every must pass"
CASE2="$(new_case_dir case2-negation-every)"
cat > "${CASE2}/.github/workflows/case2-negation-every.yml" <<'EOF'
name: case2-negation-every
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          predicate-quantifier: 'every'
          filters: |
            project:
              - 'project/**'
              - '!project/AGENTS.md'
EOF
run_check "${CASE2}"
assert_exit "case2 negation under every" 0

# ── Case 3: negation-free filter under default `some` → exit 0 ─────────
# Also asserts the discovery-count report is printed on a normal run
# (rule 2: "print the discovered step count").
echo "case: negation-free filter under default quantifier must pass"
CASE3="$(new_case_dir case3-no-negation)"
cat > "${CASE3}/.github/workflows/case3-no-negation.yml" <<'EOF'
name: case3-no-negation
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: |
            project:
              - 'project/**'
              - 'shared/config.json'
EOF
run_check "${CASE3}"
assert_exit "case3 negation-free filter" 0
assert_contains "case3 discovery count reported" "1"

# ── Case 4: multi-step job, only one step carries negations → exit 1 ───
# The compliant sibling step must NOT be named in the failure output --
# specifically, a pattern unique to the compliant step's (negation-free)
# filter must never appear, proving the guard scopes its report to the
# offending step rather than dumping every discovered step's content.
echo "case: multi-step job where only one step carries negations must fail, and the compliant sibling step must not be named"
CASE4="$(new_case_dir case4-multi-step)"
cat > "${CASE4}/.github/workflows/case4-multi-step.yml" <<'EOF'
name: case4-multi-step
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: clean
        with:
          filters: |
            clean-project:
              - 'clean-project-unique-root/**'
              - 'clean-project-unique-root/shared.json'
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: offender
        with:
          filters: |
            offender-project:
              - 'offender-project/**'
              - '!offender-project/AGENTS.md'
EOF
run_check "${CASE4}"
assert_exit "case4 multi-step, one offender" 1
assert_contains "case4 message names the offending pattern" "!offender-project/AGENTS.md"
assert_not_contains "case4 message does not name the compliant sibling step's pattern" "clean-project-unique-root"

# ── Case 5: explicit predicate-quantifier: 'some' plus a negation → exit 1
# An explicit 'some' must fail identically to an absent predicate-quantifier.
echo "case: explicit predicate-quantifier some plus a negation must fail"
CASE5="$(new_case_dir case5-explicit-some)"
cat > "${CASE5}/.github/workflows/case5-explicit-some.yml" <<'EOF'
name: case5-explicit-some
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          predicate-quantifier: 'some'
          filters: |
            project:
              - 'project/**'
              - '!project/AGENTS.md'
EOF
run_check "${CASE5}"
assert_exit "case5 explicit some plus negation" 1
assert_contains "case5 message names the offending pattern" "!project/AGENTS.md"

# ── Case 6: zero paths-filter steps in the tree → exit 1 (discovery guard)
# Rule 2: at least one paths-filter step must be discovered across
# .github/workflows/, else FAIL -- mirrors
# check-version-bump-concurrency.sh's rule 4 (zero callers discovered).
echo "case: zero paths-filter steps discovered in the tree must fail"
CASE6="$(new_case_dir case6-zero-steps)"
cat > "${CASE6}/.github/workflows/case6-unrelated.yml" <<'EOF'
name: case6-unrelated
on:
  pull_request:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - run: echo noop
EOF
run_check "${CASE6}"
assert_exit "case6 zero paths-filter steps discovered" 1
assert_contains "case6 message identifies rule 2" "rule 2"
assert_contains "case6 message names zero discovered" "no dorny/paths-filter steps discovered"

# ── Case 7: malformed inline filters: YAML → exit 1 (rule 3, fail closed)
# The literal filters: block-scalar value below is not valid YAML on its
# own (an unterminated flow sequence) -- the guard must fail closed when
# it re-parses this string rather than silently treating it as having no
# negations.
echo "case: malformed inline filters YAML must fail closed"
CASE7="$(new_case_dir case7-malformed-yaml)"
cat > "${CASE7}/.github/workflows/case7-malformed-yaml.yml" <<'EOF'
name: case7-malformed-yaml
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: |
            project: ['a', 'b'
EOF
run_check "${CASE7}"
assert_exit "case7 malformed inline filters YAML" 1
assert_contains "case7 message identifies rule 3" "rule 3"

# ── Case 8a: filters: names an external YAML file containing a negation,
# step lacks 'every' → exit 1 (rule 3, single-line value ending in
# .yml/.yaml is a repo-root-relative filters file, and must be scanned).
echo "case: filters referencing an external file with a negation, step lacking every, must fail"
CASE8A="$(new_case_dir case8a-external-file-negation)"
mkdir -p "${CASE8A}/.github/filters"
cat > "${CASE8A}/.github/filters/project-filters.yml" <<'EOF'
project:
  - 'project/**'
  - '!project/AGENTS.md'
EOF
cat > "${CASE8A}/.github/workflows/case8a-external-file-negation.yml" <<'EOF'
name: case8a-external-file-negation
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: .github/filters/project-filters.yml
EOF
run_check "${CASE8A}"
assert_exit "case8a external filters file with negation, no every" 1
assert_contains "case8a message names the offending pattern" "!project/AGENTS.md"

# ── Case 8b: same filters: file reference, but the file is missing →
# exit 1 (rule 3, fail closed on unreadable filters file).
echo "case: filters referencing a missing external file must fail closed"
CASE8B="$(new_case_dir case8b-external-file-missing)"
cat > "${CASE8B}/.github/workflows/case8b-external-file-missing.yml" <<'EOF'
name: case8b-external-file-missing
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: .github/filters/does-not-exist.yml
EOF
run_check "${CASE8B}"
assert_exit "case8b external filters file missing" 1
assert_contains "case8b message identifies rule 3" "rule 3"
assert_contains "case8b message names the missing file" "does-not-exist.yml"

# ── Case 9: a non-paths-filter step whose with: contains a '!'-prefixed
# string → exit 0 (no false positive). A second, unrelated, negation-free
# paths-filter step exists in the same job so rule 2's discovery guard is
# satisfied and the overall run can legitimately reach exit 0.
echo "case: a non-paths-filter step with a bang-prefixed with: value must not be flagged"
CASE9="$(new_case_dir case9-non-paths-filter-bang)"
cat > "${CASE9}/.github/workflows/case9-non-paths-filter-bang.yml" <<'EOF'
name: case9-non-paths-filter-bang
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: some-org/some-action@v1
        with:
          pattern: '!not-a-paths-filter-pattern'
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: |
            project:
              - 'project/**'
EOF
run_check "${CASE9}"
assert_exit "case9 non-paths-filter step with bang value" 0
assert_not_contains "case9 the non-paths-filter step's value is not flagged" "not-a-paths-filter-pattern"

# ── Case 10: negation in the change-type object form
# (- added|modified: '!x/y.md') under default `some` → exit 1. Proves the
# guard re-parses filters (rather than regexing lines) so it also catches
# negations expressed as the value of a change-type key, not just plain
# string list items.
echo "case: negation in the change-type object form under default quantifier must fail"
CASE10="$(new_case_dir case10-change-type-object-negation)"
cat > "${CASE10}/.github/workflows/case10-change-type-object-negation.yml" <<'EOF'
name: case10-change-type-object-negation
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: |
            project:
              - added|modified: '!x/y.md'
EOF
run_check "${CASE10}"
assert_exit "case10 negation in change-type object form" 1
assert_contains "case10 message names the offending pattern" "!x/y.md"

# ── Case 11: filters: references an external path that traverses out of
# the repo root ('..') → exit 1, rule 3, fail closed. filters_raw comes
# from PR-authored workflow YAML, so an unrejected '..' is a bounded
# arbitrary-read primitive on the runner -- this must be rejected before
# the readability test ever touches the filesystem.
echo "case: filters referencing a traversing external path must fail closed"
CASE11="$(new_case_dir case11-external-file-traversal)"
cat > "${CASE11}/.github/workflows/case11-external-file-traversal.yml" <<'EOF'
name: case11-external-file-traversal
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: ../outside/filters.yml
EOF
run_check "${CASE11}"
assert_exit "case11 traversing external filters path" 1
assert_contains "case11 message identifies rule 3" "rule 3"

# ── Case 12: filters: references an absolute external path → exit 1,
# rule 3, fail closed. Same primitive as case 11, but via a leading '/'
# instead of '..'.
echo "case: filters referencing an absolute external path must fail closed"
CASE12="$(new_case_dir case12-external-file-absolute)"
cat > "${CASE12}/.github/workflows/case12-external-file-absolute.yml" <<'EOF'
name: case12-external-file-absolute
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter@7b450fff21473bca461d4b92ce414b9d0420d706 # v4.0.2
        id: filter
        with:
          filters: /etc/passwd.yml
EOF
run_check "${CASE12}"
assert_exit "case12 absolute external filters path" 1
assert_contains "case12 message identifies rule 3" "rule 3"

# ── Case 13: an UNPINNED dorny/paths-filter reference (no @ref) carrying a
# negation with no predicate-quantifier → exit 1. Proves the discovery
# predicate matches the action by name regardless of pinning -- an unpinned
# step must still be discovered and checked, not silently skipped (which
# would be this guard's own version of the #950 bug it exists to prevent).
echo "case: unpinned dorny/paths-filter reference with a negation must still be discovered and fail"
CASE13="$(new_case_dir case13-unpinned-negation)"
cat > "${CASE13}/.github/workflows/case13-unpinned-negation.yml" <<'EOF'
name: case13-unpinned-negation
on:
  pull_request:
jobs:
  changes:
    runs-on: ubuntu-latest
    steps:
      - uses: dorny/paths-filter
        id: filter
        with:
          filters: |
            project:
              - 'project/**'
              - '!project/AGENTS.md'
EOF
run_check "${CASE13}"
assert_exit "case13 unpinned reference with negation" 1
assert_contains "case13 message names the offending pattern" "!project/AGENTS.md"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
