#!/bin/bash
# Tests for OpenCode's cenci-src provisioning helpers in
# sandbox/lib/migrate-settings.sh (#490): provision_opencode_plugins
# (clone-once + skill install) and update_opencode_plugins (TTL-gated pull
# refresh).
#
# OpenCode has no marketplace CLI like `claude plugin marketplace add` /
# `codex plugin marketplace add`, so the analogous mechanism is a plain,
# TTL-gated `git clone`/`git pull` of matteobortolazzo/cenci into a home-volume
# "cenci-src" directory, giving PLUGIN_ROOT=<cenci-src-dir>/flow for
# flow/opencode/install-skills.sh. In the real container this lands at
# /home/dev/.cenci-src (PLUGIN_ROOT=/home/dev/.cenci-src/flow); these tests
# parameterize that path so the suite runs on the host without touching a real
# /home/dev.
#
# `git` is mocked via PATH (mirrors heal-plugins.test.sh's fake
# `claude`/`codex` recording-fake pattern). `install-skills.sh` is NOT put on
# PATH: the production code resolves it at
# `<cenci-src-dir>/flow/opencode/install-skills.sh` (the location a real git
# clone lands it at), never via `command -v`, so the fake here is staged at
# that same resolved path — the fake `git clone` action stages it there too,
# standing in for the real clone bringing the file along. A second, decoy
# `install-skills.sh` also lives on PATH; every case asserts the decoy is
# never invoked, so a regression back to bare PATH lookup is caught. Every
# invocation of the functions under test runs inside an `env -i` subshell
# (mirrors agent-cli.test.sh's run_update) so the sentinel-secret regression
# case below actually proves the environment is scrubbed, not just that PATH
# resolves to the fakes (#363).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILURES=0
PASSES=0
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

STAMP_NAME=".cenci-sand-update-stamp"
REPO_URL="matteobortolazzo/cenci"
EXPECTED_CLONE_URL="https://github.com/${REPO_URL}.git"

FAKE_BIN="${WORK}/fake-bin"
mkdir -p "${FAKE_BIN}"

# make_fake_git: (re)writes the fake `git` executable. `clone <url> <dest>`
# creates <dest> (standing in for a real clone) instead of touching the
# network, and stages a fake install-skills.sh at
# <dest>/flow/opencode/install-skills.sh (standing in for the real clone
# bringing that file along) so provisioning can find it at the resolved path;
# `-C <dest> pull` just logs the call. Reads GIT_FAKE_LOG and GIT_FAKE_EXIT
# from the environment at call time.
make_fake_git() {
    cat >"${FAKE_BIN}/git" <<'FAKE'
#!/bin/bash
echo "$*" >> "${GIT_FAKE_LOG}"
[[ -z "${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "${CENCI_PARENT_SECRET}" >> "${GIT_FAKE_LOG}"
if [[ "${GIT_FAKE_EXIT:-0}" -ne 0 ]]; then
    echo "git: simulated failure (offline)" >&2
    exit "${GIT_FAKE_EXIT}"
fi
if [[ "$1" == "clone" ]]; then
    mkdir -p "$3"
    if [[ "${STAGE_INSTALL_SKILLS_ON_CLONE:-1}" == "1" ]]; then
        mkdir -p "$3/flow/opencode"
        cat > "$3/flow/opencode/install-skills.sh" <<'INSTALLFAKE'
#!/bin/bash
printf 'argv=%s PLUGIN_ROOT=%s script=%s\n' "$*" "${PLUGIN_ROOT:-}" "$0" >> "${INSTALL_SKILLS_FAKE_LOG}"
[[ -z "${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "${CENCI_PARENT_SECRET}" >> "${INSTALL_SKILLS_FAKE_LOG}"
exit "${INSTALL_SKILLS_FAKE_EXIT:-0}"
INSTALLFAKE
        chmod +x "$3/flow/opencode/install-skills.sh"
    fi
fi
exit 0
FAKE
    chmod +x "${FAKE_BIN}/git"
}

# make_decoy_install_skills: a recording fake install-skills.sh reachable via
# PATH. Production code must never resolve install-skills.sh through PATH —
# it always invokes the fully-qualified <src-dir>/flow/opencode/install-skills.sh
# path. Every case below asserts DECOY_LOG stays empty; if a regression brings
# back a bare `command -v install-skills.sh` lookup, this decoy is what would
# get called instead, and the assertion would catch it.
make_decoy_install_skills() {
    cat >"${FAKE_BIN}/install-skills.sh" <<'FAKE'
#!/bin/bash
printf 'argv=%s PLUGIN_ROOT=%s script=%s\n' "$*" "${PLUGIN_ROOT:-}" "$0" >> "${DECOY_LOG}"
exit "${DECOY_FAKE_EXIT:-0}"
FAKE
    chmod +x "${FAKE_BIN}/install-skills.sh"
}

# stage_install_skills <src-dir>: stages the resolved-path fake directly, for
# cases that pre-create <src-dir> themselves (bypassing the fake git clone
# staging above).
stage_install_skills() {
    local dir="$1"
    mkdir -p "${dir}/flow/opencode"
    cat > "${dir}/flow/opencode/install-skills.sh" <<'INSTALLFAKE'
#!/bin/bash
printf 'argv=%s PLUGIN_ROOT=%s script=%s\n' "$*" "${PLUGIN_ROOT:-}" "$0" >> "${INSTALL_SKILLS_FAKE_LOG}"
[[ -z "${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "${CENCI_PARENT_SECRET}" >> "${INSTALL_SKILLS_FAKE_LOG}"
exit "${INSTALL_SKILLS_FAKE_EXIT:-0}"
INSTALLFAKE
    chmod +x "${dir}/flow/opencode/install-skills.sh"
}

# new_case <name>: fresh cenci-src dir (not created — tests create it only
# when simulating an "already cloned" state) plus fresh fake call logs.
new_case() {
    CASE_DIR="${WORK}/$1"
    SRC_DIR="${CASE_DIR}/cenci-src"
    GIT_FAKE_LOG="${CASE_DIR}/git.log"
    INSTALL_SKILLS_FAKE_LOG="${CASE_DIR}/install-skills.log"
    DECOY_LOG="${CASE_DIR}/decoy-install-skills.log"
    mkdir -p "${CASE_DIR}"
    : >"${GIT_FAKE_LOG}"
    : >"${INSTALL_SKILLS_FAKE_LOG}"
    : >"${DECOY_LOG}"
    unset GIT_FAKE_EXIT INSTALL_SKILLS_FAKE_EXIT STAGE_INSTALL_SKILLS_ON_CLONE
}

# assert_no_decoy_calls <case-label>: the resolved-path fix must never let a
# bare `install-skills.sh` PATH lookup fire.
assert_no_decoy_calls() {
    if [[ ! -s "${DECOY_LOG}" ]]; then
        pass
    else
        fail "$1: install-skills.sh was resolved via PATH instead of the cenci-src path, got: $(cat "${DECOY_LOG}")"
    fi
}

# run_provision <src-dir> <repo-url>: invokes provision_opencode_plugins in a
# scrubbed `env -i` child so only the explicitly listed variables (never an
# ambient host secret) reach it or anything it shells out to.
run_provision() {
    local src_dir="$1" repo="$2"
    # shellcheck disable=SC2016  # $1/$2/$3 must expand in the child bash, not here
    env -i PATH="${FAKE_BIN}:/usr/bin:/bin" \
        GIT_FAKE_LOG="${GIT_FAKE_LOG}" GIT_FAKE_EXIT="${GIT_FAKE_EXIT:-0}" \
        STAGE_INSTALL_SKILLS_ON_CLONE="${STAGE_INSTALL_SKILLS_ON_CLONE:-1}" \
        INSTALL_SKILLS_FAKE_LOG="${INSTALL_SKILLS_FAKE_LOG}" INSTALL_SKILLS_FAKE_EXIT="${INSTALL_SKILLS_FAKE_EXIT:-0}" \
        DECOY_LOG="${DECOY_LOG}" \
        bash -c 'source "${1}/sandbox/lib/migrate-settings.sh"; provision_opencode_plugins "$2" "$3"' _ "${ROOT}" "${src_dir}" "${repo}"
}

# run_update <src-dir> <repo-url> <ttl-minutes>: same scrubbed-env contract as
# run_provision, for update_opencode_plugins.
run_update() {
    local src_dir="$1" repo="$2" ttl="$3"
    # shellcheck disable=SC2016  # $1/$2/$3/$4 must expand in the child bash, not here
    env -i PATH="${FAKE_BIN}:/usr/bin:/bin" \
        GIT_FAKE_LOG="${GIT_FAKE_LOG}" GIT_FAKE_EXIT="${GIT_FAKE_EXIT:-0}" \
        STAGE_INSTALL_SKILLS_ON_CLONE="${STAGE_INSTALL_SKILLS_ON_CLONE:-1}" \
        INSTALL_SKILLS_FAKE_LOG="${INSTALL_SKILLS_FAKE_LOG}" INSTALL_SKILLS_FAKE_EXIT="${INSTALL_SKILLS_FAKE_EXIT:-0}" \
        DECOY_LOG="${DECOY_LOG}" \
        bash -c 'source "${1}/sandbox/lib/migrate-settings.sh"; update_opencode_plugins "$2" "$3" "$4"' _ "${ROOT}" "${src_dir}" "${repo}" "${ttl}"
}

echo "opencode-plugins.test.sh"

echo "case: clone-once when the cenci-src directory is absent"
new_case clone-once
make_fake_git
make_decoy_install_skills
run_provision "${SRC_DIR}" "${REPO_URL}" >/dev/null
if grep -Fq "clone ${EXPECTED_CLONE_URL} ${SRC_DIR}" "${GIT_FAKE_LOG}"; then
    pass
else
    fail "expected 'git clone ${EXPECTED_CLONE_URL} ${SRC_DIR}' (a real git-understood URL, not the bare 'owner/repo' shorthand), got: $(cat "${GIT_FAKE_LOG}")"
fi

echo "case: install-skills runs with PLUGIN_ROOT=<cenci-src-dir>/flow"
if grep -Fq "PLUGIN_ROOT=${SRC_DIR}/flow" "${INSTALL_SKILLS_FAKE_LOG}"; then
    pass
else
    fail "expected install-skills.sh to see PLUGIN_ROOT=${SRC_DIR}/flow, got: $(cat "${INSTALL_SKILLS_FAKE_LOG}")"
fi

echo "case: install-skills is invoked with the install action"
if grep -Fq "argv=install" "${INSTALL_SKILLS_FAKE_LOG}"; then
    pass
else
    fail "expected install-skills.sh to be called with 'install', got: $(cat "${INSTALL_SKILLS_FAKE_LOG}")"
fi

echo "case: install-skills is invoked by its resolved cenci-src path, not a bare PATH command"
if grep -Fq "script=${SRC_DIR}/flow/opencode/install-skills.sh" "${INSTALL_SKILLS_FAKE_LOG}"; then
    pass
else
    fail "expected install-skills.sh to be invoked as ${SRC_DIR}/flow/opencode/install-skills.sh, got: $(cat "${INSTALL_SKILLS_FAKE_LOG}")"
fi

echo "case: install-skills.sh is never resolved via PATH (clone-once)"
assert_no_decoy_calls "clone-once"

echo "case: an already-cloned cenci-src is not re-cloned"
new_case already-cloned
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
run_provision "${SRC_DIR}" "${REPO_URL}" >/dev/null
if ! grep -Fq "clone " "${GIT_FAKE_LOG}"; then
    pass
else
    fail "expected no 'git clone' for an existing cenci-src, got: $(cat "${GIT_FAKE_LOG}")"
fi
assert_no_decoy_calls "already-cloned"

echo "case: install-skills.sh missing at the resolved cenci-src path is non-fatal and never falls back to PATH"
new_case install-skills-missing
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
STAGE_INSTALL_SKILLS_ON_CLONE=0
if run_provision "${SRC_DIR}" "${REPO_URL}" 2>/dev/null; then
    pass
else
    fail "provision_opencode_plugins should return 0 when install-skills.sh is missing at the resolved path"
fi
if [[ ! -s "${INSTALL_SKILLS_FAKE_LOG}" ]]; then
    pass
else
    fail "expected no invocation of the resolved-path install-skills.sh when it doesn't exist, got: $(cat "${INSTALL_SKILLS_FAKE_LOG}")"
fi
assert_no_decoy_calls "install-skills-missing"
unset STAGE_INSTALL_SKILLS_ON_CLONE

echo "case: a fresh TTL stamp skips the pull entirely"
new_case fresh-stamp
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
touch "${SRC_DIR}/${STAMP_NAME}"
run_update "${SRC_DIR}" "${REPO_URL}" 30 >/dev/null
if [[ ! -s "${GIT_FAKE_LOG}" ]]; then
    pass
else
    fail "expected zero git invocations within the TTL window, got: $(cat "${GIT_FAKE_LOG}")"
fi

echo "case: a stale stamp (older than TTL) triggers a pull and refreshes the stamp"
new_case stale-stamp
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
touch -d '31 minutes ago' "${SRC_DIR}/${STAMP_NAME}"
run_update "${SRC_DIR}" "${REPO_URL}" 30 >/dev/null
if grep -Fq -- "-C ${SRC_DIR} pull" "${GIT_FAKE_LOG}"; then
    pass
else
    fail "expected 'git -C ${SRC_DIR} pull' after the TTL expired, got: $(cat "${GIT_FAKE_LOG}")"
fi
if [[ -n "$(find "${SRC_DIR}/${STAMP_NAME}" -mmin -1 2>/dev/null)" ]]; then
    pass
else
    fail "expected the update stamp to be refreshed after a pull pass"
fi
assert_no_decoy_calls "stale-stamp"

echo "case: ttl 0 forces the pull despite a fresh stamp"
new_case forced-update
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
touch "${SRC_DIR}/${STAMP_NAME}"
run_update "${SRC_DIR}" "${REPO_URL}" 0 >/dev/null
if grep -Fq -- "-C ${SRC_DIR} pull" "${GIT_FAKE_LOG}"; then
    pass
else
    fail "expected ttl 0 to bypass the fresh stamp, got: $(cat "${GIT_FAKE_LOG}")"
fi
assert_no_decoy_calls "forced-update"

echo "case: missing git is non-fatal — provisioning warns and returns 0"
new_case no-git-provision
EMPTY_BIN="${CASE_DIR}/empty-bin"
mkdir -p "${EMPTY_BIN}"
# EMPTY_BIN must resolve `bash` itself (env -i execs it via the NEW PATH it
# just set, not the caller's), but must NOT resolve `git` — a directory
# housing both (e.g. /usr/bin) would let a real host git mask the very
# absence this case exercises. A lone symlink gives `bash` a home on PATH
# while leaving `git` genuinely unresolvable.
ln -s "$(command -v bash)" "${EMPTY_BIN}/bash"
# shellcheck disable=SC2016  # $1/$2/$3 must expand in the child bash, not here
if env -i PATH="${EMPTY_BIN}" GIT_FAKE_LOG="${GIT_FAKE_LOG}" INSTALL_SKILLS_FAKE_LOG="${INSTALL_SKILLS_FAKE_LOG}" \
    bash -c 'source "${1}/sandbox/lib/migrate-settings.sh"; provision_opencode_plugins "$2" "$3"' _ "${ROOT}" "${SRC_DIR}" "${REPO_URL}" 2>/dev/null; then
    pass
else
    fail "provision_opencode_plugins should return 0 when git is not on PATH"
fi

echo "case: missing git is non-fatal — update warns and returns 0"
new_case no-git-update
EMPTY_BIN="${CASE_DIR}/empty-bin"
mkdir -p "${EMPTY_BIN}"
ln -s "$(command -v bash)" "${EMPTY_BIN}/bash"
# shellcheck disable=SC2016  # $1/$2/$3 must expand in the child bash, not here
if env -i PATH="${EMPTY_BIN}" GIT_FAKE_LOG="${GIT_FAKE_LOG}" INSTALL_SKILLS_FAKE_LOG="${INSTALL_SKILLS_FAKE_LOG}" \
    bash -c 'source "${1}/sandbox/lib/migrate-settings.sh"; update_opencode_plugins "$2" "$3" 0' _ "${ROOT}" "${SRC_DIR}" "${REPO_URL}" 2>/dev/null; then
    pass
else
    fail "update_opencode_plugins should return 0 when git is not on PATH"
fi

echo "case: a failed clone (offline) is non-fatal"
new_case clone-fails
make_fake_git
make_decoy_install_skills
GIT_FAKE_EXIT=1
if run_provision "${SRC_DIR}" "${REPO_URL}" 2>/dev/null; then
    pass
else
    fail "provision_opencode_plugins must not fail the sandbox boot on an offline clone"
fi
unset GIT_FAKE_EXIT

echo "case: a failed pull (offline) is non-fatal and still refreshes the stamp"
new_case pull-fails
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
GIT_FAKE_EXIT=1
if run_update "${SRC_DIR}" "${REPO_URL}" 0 2>/dev/null; then
    pass
else
    fail "update_opencode_plugins must not fail the sandbox boot on an offline pull"
fi
unset GIT_FAKE_EXIT

echo "case: a failed touch of the update stamp is reported, not silently swallowed"
new_case touch-fails
make_fake_git
make_decoy_install_skills
mkdir -p "${SRC_DIR}/flow"
stage_install_skills "${SRC_DIR}"
# Root-proof lever: the stamp path is a self-referential symlink, so
# `touch "${stamp}"` fails with ELOOP regardless of uid (`chmod a-w` was a
# no-op for root, #642).
ln -s "${STAMP_NAME}" "${SRC_DIR}/${STAMP_NAME}"
STDERR_LOG="${CASE_DIR}/stderr.log"
run_update "${SRC_DIR}" "${REPO_URL}" 0 >/dev/null 2>"${STDERR_LOG}"
if grep -Fq "failed to record OpenCode plugin refresh time" "${STDERR_LOG}"; then
    pass
else
    fail "expected a warning when the update stamp can't be touched, got: $(cat "${STDERR_LOG}")"
fi

echo "case: parent-only sentinel secret never reaches git or install-skills (#363)"
new_case secrets
make_fake_git
make_decoy_install_skills
CENCI_PARENT_SECRET='parent-only-sentinel' run_provision "${SRC_DIR}" "${REPO_URL}" >/dev/null
if ! grep -Fq 'parent-only-sentinel' "${GIT_FAKE_LOG}" "${INSTALL_SKILLS_FAKE_LOG}"; then
    pass
else
    fail "parent-only sentinel secret leaked into a provisioning subprocess: $(cat "${GIT_FAKE_LOG}" "${INSTALL_SKILLS_FAKE_LOG}")"
fi

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
