#!/usr/bin/env bash
# End-to-end regressions for client detection and client-aware installer output.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

make_common_tools() {
    local bin="$1"
    mkdir -p "${bin}"
    local tool
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head rm mktemp; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat > "${bin}/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then exit 1; fi
exit 0
EOF
    chmod +x "${bin}/docker"
    cat > "${bin}/curl" <<'EOF'
#!/bin/sh
out=
while [ "$#" -gt 0 ]; do
    case "$1" in
      -o) out=$2; shift 2 ;;
      *) shift ;;
    esac
done
[ -n "${out}" ] || exit 1
cat >"${out}" <<'INSTALLER'
printf 'forwarded installer args: %s\n' "$*"
INSTALLER
EOF
    chmod +x "${bin}/curl"
}

make_claude() {
    local bin="$1"
    cat > "${bin}/claude" <<'EOF'
#!/bin/sh
printf 'claude %s\n' "$*" >>"${CALLS_FILE}"
# Regression probe (#353): if a host secret survives into this subprocess's
# environment, surface it in the captured calls so the sentinel-secret case
# can prove this test harness's env -i scrub keeps host secrets out.
[ -n "${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY}" >>"${CALLS_FILE}"
[ -n "${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "${CONTEXT7_API_KEY}" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CLAUDE_MARKETPLACE_FILE}" ] && echo cenci; exit 0 ;;
  "plugin marketplace add "*) touch "${CLAUDE_MARKETPLACE_FILE}"; exit 0 ;;
  "plugin list")
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CLAUDE_INSTALLED_FILE}-${p}" ] && printf '%s@cenci\n' "${p}"
    done
    exit 0
    ;;
  "plugin install "*) p=${3%%@*}; touch "${CLAUDE_INSTALLED_FILE}-${p}"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

make_codex() {
    local bin="$1"
    cat > "${bin}/codex" <<'EOF'
#!/bin/sh
printf 'codex %s\n' "$*" >>"${CALLS_FILE}"
case "$*" in
  "plugin marketplace list") [ -f "${CODEX_MARKETPLACE_FILE}" ] && echo 'cenci local'; exit 0 ;;
  "plugin marketplace add "*) touch "${CODEX_MARKETPLACE_FILE}"; exit 0 ;;
  "plugin list")
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CODEX_INSTALLED_FILE}-${p}" ] && printf '%s@cenci installed\n' "${p}"
    done
    exit 0
    ;;
  "plugin add "*) p=${3%%@*}; touch "${CODEX_INSTALLED_FILE}-${p}"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "${bin}/codex"
}

prepare_checkout() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
}

# prepare_cache_cenci <home> <client> — provision a fake cenci binary in the
# version-pinned plugin cache (what current_cenci_binary resolves), logging
# every invocation to CALLS_FILE, so the `cenci sandbox build` step can run.
prepare_cache_cenci() {
    local home="$1" client="$2" root manifest_dir
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${root}/bin" "${root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
    cat > "${root}/bin/cenci" <<'EOF'
#!/bin/sh
printf 'cenci %s\n' "$*" >>"${CALLS_FILE}"
exit 0
EOF
    chmod +x "${root}/bin/cenci"
}

run_case() {
    local name="$1" clients="$2"
    local build_flag="${3:---no-build}"
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
      claude) make_claude "${bin}"; prepare_checkout "${home}" claude ;;
      codex) make_codex "${bin}"; prepare_checkout "${home}" codex ;;
      dual) make_claude "${bin}"; make_codex "${bin}"; prepare_checkout "${home}" claude ;;
    esac

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

run_doctor_case() {
    local name="$1" clients="$2"
    run_case "${name}" "${clients}"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/doctor-output"
    set +e
    env -i HOME="${CASE_HOME}" PATH="${bin}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
    DOCTOR_EXIT=$?
    set -e
    DOCTOR_OUTPUT="${output}"
}

# run_doctor_case_with_daemon_status is run_doctor_case plus a fake
# `cenci` on the doctor-run PATH that exits daemon_exit on `daemon
# status` (0 = running, 1 = not running — mirrors runDaemonStatus in
# watch/main.go). Pre-creating "${WORK}/${name}/bin/cenci" before
# run_case's `mkdir -p` (idempotent) and make_common_tools (which only
# symlinks its own fixed tool list) leaves this fake in place for the doctor
# run that follows.
run_doctor_case_with_daemon_status() {
    local name="$1" clients="$2" daemon_exit="$3"
    local bin="${WORK}/${name}/bin"
    mkdir -p "${bin}"
    cat > "${bin}/cenci" <<EOF
#!/bin/sh
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = status ]; then
    exit ${daemon_exit}
fi
exit 0
EOF
    chmod +x "${bin}/cenci"
    run_doctor_case "${name}" "${clients}"
}

assert_contains() {
    local file="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: expected '${needle}' in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

assert_not_contains() {
    local file="$1" needle="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: did not expect '${needle}' in ${file}" >&2
        sed -n '1,240p' "${file}" >&2
        exit 1
    fi
}

assert_cenci_installer_utility() {
    local output="${CASE_HOME}/cenci-installer-output"
    [[ -L "${CASE_HOME}/.local/bin/cenci-installer" ]]
    HOME="${CASE_HOME}" PATH="${CASE_BIN}" \
        "${CASE_HOME}/.local/bin/cenci-installer" update --yes >"${output}"
    assert_contains "${output}" "forwarded installer args: update --yes"
}

echo "installer-clients.test.sh"

echo "case: Claude-only installs every component and prints only Claude launch guidance"
run_case claude claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin install cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin install cenci-sandbox@cenci"
assert_contains "${CASE_OUTPUT}" "cn ch|cs|co|cf"
assert_not_contains "${CASE_OUTPUT}" "cn xl|xt|xs"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
assert_cenci_installer_utility

echo "case: Codex-only installs every component without invoking or recommending Claude"
run_case codex codex
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "codex plugin add cenci@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci-sandbox@cenci"
assert_contains "${CASE_OUTPUT}" "cn xl|xt|xs"
assert_not_contains "${CASE_OUTPUT}" "cn ch|cs|co|cf"
assert_not_contains "${CASE_OUTPUT}" "/cenci:configure"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ ! -e "${CASE_HOME}/.local/bin/sb" ]]
[[ ! -e "${CASE_HOME}/.local/bin/codex-sand" ]]
assert_cenci_installer_utility

echo "case: Codex-only image build runs 'cenci sandbox build' via the cached binary"
prepare_cache_cenci "${WORK}/codex-build/home" codex
run_case codex-build codex --build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "sandbox image built"
assert_contains "${CASE_CALLS}" "cenci sandbox build"

echo "case: a stale cenci-sand symlink is repointed at cenci, never recreated"
name=stale-sand
mkdir -p "${WORK}/${name}/home/.local/bin"
ln -s /nonexistent/old-cenci-sand "${WORK}/${name}/home/.local/bin/cenci-sand"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
[[ -L "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ "$(readlink "${CASE_HOME}/.local/bin/cenci-sand")" == "${CASE_HOME}/.local/bin/cenci" ]]
assert_contains "${CASE_OUTPUT}" "cenci-sand is deprecated"

echo "case: dual-client installs independently and exposes the cn launcher"
run_case dual dual
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "codex plugin add cenci@cenci"
[[ -L "${CASE_HOME}/.local/bin/cn" ]]
[[ ! -e "${CASE_HOME}/.local/bin/cenci-sand" ]]
[[ ! -e "${CASE_HOME}/.local/bin/sb" ]]
[[ ! -e "${CASE_HOME}/.local/bin/codex-sand" ]]
assert_cenci_installer_utility

echo "case: already-registered marketplace is refreshed, not just confirmed"
name=refresh
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_codex "${bin}"
prepare_checkout "${home}" claude
touch "${case_dir}/claude-marketplace" "${case_dir}/codex-marketplace"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
    CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
refresh_exit=$?
set -e
[[ "${refresh_exit}" -eq 0 ]]
assert_contains "${calls}" "claude plugin marketplace update cenci"
assert_contains "${calls}" "codex plugin marketplace upgrade cenci"
assert_not_contains "${calls}" "claude plugin marketplace add "
assert_not_contains "${calls}" "codex plugin marketplace add "

echo "case: no supported client fails with a client-specific diagnostic"
run_case none none
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "Install Claude Code, Codex, or both"

echo "case: doctor separates clients, components, launchers, and image readiness"
run_doctor_case doctor-codex codex
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Required platform dependencies"
assert_contains "${DOCTOR_OUTPUT}" "Supported clients (at least one required)"
assert_contains "${DOCTOR_OUTPUT}" "Installed stack components"
assert_contains "${DOCTOR_OUTPUT}" "Launchers and container image"
assert_contains "${DOCTOR_OUTPUT}" "cn launcher (cenci open)"
assert_not_contains "${DOCTOR_OUTPUT}" "cenci-sand launcher"
assert_contains "${DOCTOR_OUTPUT}" "Codex: cenci"
assert_contains "${DOCTOR_OUTPUT}" "cenci-installer utility"
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon"
assert_contains "${DOCTOR_OUTPUT}" "bootstraps on your first agent session"

echo "case: doctor reports the cenci daemon as running"
run_doctor_case_with_daemon_status doctor-daemon-up claude 0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: running"

echo "case: doctor reports the cenci daemon as not running, without failing doctor"
run_doctor_case_with_daemon_status doctor-daemon-down claude 1
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "cenci daemon: not running"

echo "case: doctor fails when no supported client is available"
run_doctor_case doctor-none none
[[ "${DOCTOR_EXIT}" -ne 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "no supported client"

echo "case: host secrets in the parent env never reach captured calls or output (regression, #353)"
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_case sentinel-secrets claude
unset OPENAI_API_KEY CONTEXT7_API_KEY
[[ "${CASE_EXIT}" -eq 0 ]]
assert_not_contains "${CASE_CALLS}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${CASE_OUTPUT}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${CASE_CALLS}" "ctx7-test-sentinel-should-not-leak"
assert_not_contains "${CASE_OUTPUT}" "ctx7-test-sentinel-should-not-leak"

echo "passed: client detection, installation, launchers, and summaries"
