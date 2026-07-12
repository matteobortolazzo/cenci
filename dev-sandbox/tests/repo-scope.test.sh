#!/bin/bash
# Tests for the pure repo-scoping logic in dev-sandbox/lib/repo-scope.sh.
#
# Runs on the host — no Docker required. Sources the same slugify(),
# resolve_repo_root(), compute_workdir(), compute_legacy_workdir() and
# select_image() that agent-sand uses to namespace containers/volumes/images
# per repo.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/repo-scope.sh
source "${SCRIPT_DIR}/../lib/repo-scope.sh"

TMP_DIRS=()
trap 'rm -rf "${TMP_DIRS[@]}"' EXIT

# mk_temp_dir: create a temp dir, register it for cleanup on exit, and print its path.
mk_temp_dir() {
    local dir
    dir="$(mktemp -d)"
    TMP_DIRS+=("${dir}")
    echo "${dir}"
}

FAILURES=0
PASSES=0

# fail <message>
fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_eq <label> <actual> <expected>
assert_eq() {
    local label="$1" actual="$2" expected="$3"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label} (expected: '${expected}', got: '${actual}')"
    fi
}

echo "repo-scope.test.sh"

####################################################################
# slugify
####################################################################

echo "case: slugify"
assert_eq "lowercases uppercase input"        "$(slugify "MyRepo")"        "myrepo"
assert_eq "replaces spaces with dashes"       "$(slugify "my repo name")"  "my-repo-name"
assert_eq "replaces unicode characters"       "$(slugify "répo-café")"     "r-po-caf-"
assert_eq "preserves existing dots and dashes" "$(slugify "my.repo-name")" "my.repo-name"
assert_eq "preserves underscores"             "$(slugify "my_repo")"      "my_repo"

####################################################################
# resolve_repo_root
####################################################################

echo "case: resolve_repo_root"

GIT_REPO="$(mk_temp_dir)"
git -C "${GIT_REPO}" init -q
GIT_REPO_REAL="$(cd "${GIT_REPO}" && pwd -P)"

RESOLVED_ROOT="$(cd "${GIT_REPO}" && resolve_repo_root)"
RC_GIT=$?
assert_eq "returns repo root inside a git repo" "${RESOLVED_ROOT}" "${GIT_REPO_REAL}"
assert_eq "returns success (0) inside a git repo" "${RC_GIT}" "0"

NON_GIT_DIR="$(mk_temp_dir)"

OUTPUT_NON_GIT="$(cd "${NON_GIT_DIR}" && resolve_repo_root 2>/dev/null)"
RC_NON_GIT=$?
echo "case: resolve_repo_root outside a git repo"
if [[ "${RC_NON_GIT}" -ne 0 ]]; then
    pass
else
    fail "expected a non-zero return code outside a git repo, got ${RC_NON_GIT} (output: '${OUTPUT_NON_GIT}')"
fi

####################################################################
# compute_workdir
####################################################################

echo "case: compute_workdir"
assert_eq "repo root maps to /workspace" \
    "$(compute_workdir "/home/user/repos/foo" "/home/user/repos/foo")" \
    "/workspace"
assert_eq "nested subdir maps to /workspace/<relative-subpath>" \
    "$(compute_workdir "/home/user/repos/foo" "/home/user/repos/foo/src/pkg")" \
    "/workspace/src/pkg"

####################################################################
# compute_legacy_workdir
####################################################################

echo "case: compute_legacy_workdir"
assert_eq "subdir under the legacy host root maps to /workspace/<relative-subpath>" \
    "$(compute_legacy_workdir "/home/user/repos" "/home/user/repos/scratch/notes")" \
    "/workspace/scratch/notes"
assert_eq "path outside the legacy host root maps to /workspace" \
    "$(compute_legacy_workdir "/home/user/repos" "/home/user/elsewhere")" \
    "/workspace"

####################################################################
# select_image
####################################################################

echo "case: select_image"

REPO_WITH_DOCKERFILE="$(mk_temp_dir)"
mkdir -p "${REPO_WITH_DOCKERFILE}/.agent-sand"
touch "${REPO_WITH_DOCKERFILE}/.agent-sand/Dockerfile"
assert_eq "uses per-repo image when .agent-sand/Dockerfile exists" \
    "$(select_image "${REPO_WITH_DOCKERFILE}" "foo")" \
    "agent-sandbox-foo:latest"

REPO_WITHOUT_DOCKERFILE="$(mk_temp_dir)"
assert_eq "falls back to the monolith image when no .agent-sand/Dockerfile" \
    "$(select_image "${REPO_WITHOUT_DOCKERFILE}" "foo")" \
    "agent-sandbox:latest"

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
