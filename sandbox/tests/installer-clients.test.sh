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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head tail cut tr rm mktemp jq mv sort; do
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
  "plugin marketplace update "*) [ ! -f "${CLAUDE_REFRESH_FAIL_FILE}" ]; exit $? ;;
  "plugin list")
    [ ! -f "${CLAUDE_LIST_FAIL_FILE}" ] || exit 1
    for p in cenci cenci-watch cenci-sandbox; do
      [ -f "${CLAUDE_INSTALLED_FILE}-${p}" ] && printf '%s@cenci\n' "${p}"
    done
    exit 0
    ;;
  "plugin install "*)
    p=${3%%@*}
    # CLAUDE_SKIP_INSTALL_PLUGIN (optional) simulates a specific plugin never
    # actually landing despite the install command reporting success — e.g. a
    # marketplace that doesn't (yet) carry it — so plugin_installed keeps
    # reporting it absent and prune_selected_to_installed prunes it from
    # SELECTED, the same as a real never-installed plugin.
    [ "${p}" = "${CLAUDE_SKIP_INSTALL_PLUGIN:-}" ] || touch "${CLAUDE_INSTALLED_FILE}-${p}"
    exit 0
    ;;
  "plugin update "*) exit 0 ;;
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

# make_opencode <bin> [version] — mocks the `opencode` executable that
# HAS_OPENCODE / doctor_opencode_support (#491, not yet implemented) resolve
# via `have opencode` + `opencode --version`. Unlike make_claude/make_codex,
# OpenCode plugin/skill registration never goes through the `opencode` binary
# itself (no marketplace CLI) — it's done by install-skills.sh symlinking and
# seed_opencode_config merging opencode.json directly — so this mock only
# needs to report a version and log invocations, following the same
# CALLS_FILE-logging convention as the other client mocks.
make_opencode() {
    local bin="$1" version="${2:-1.18.3}"
    cat > "${bin}/opencode" <<EOF
#!/bin/sh
printf 'opencode %s\n' "\$*" >>"\${CALLS_FILE}"
case "\$*" in
  --version) printf '%s\n' "${version}" ;;
esac
exit 0
EOF
    chmod +x "${bin}/opencode"
}

prepare_checkout() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}"
    if [[ -f "${ROOT}/cenci" ]]; then
        cp "${ROOT}/cenci" "${checkout}/cenci"
        chmod +x "${checkout}/cenci"
    fi
    cp "${ROOT}/install.sh" "${checkout}/install.sh"
}

# prepare_opencode_checkout_assets <home> <client> — stages the real,
# already-shipped OpenCode assets (flow/opencode/install-skills.sh,
# flow/skills/*, watch/plugin/opencode, sandbox/lib/opencode-config.sh) into
# the marketplace checkout at the exact relative paths find_plugin_path
# resolves first (marketplace-checkout-first, no cache-fallback stripping
# needed), so a real step_opencode_setup (#491, not yet implemented) has
# something genuine to symlink/merge — mirroring how prepare_checkout stages
# the real cenci wrapper + install.sh rather than a fake stand-in.
prepare_opencode_checkout_assets() {
    local home="$1" client="$2" checkout
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/opencode" "${checkout}/watch/plugin" "${checkout}/sandbox/lib"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
    cp -r "${ROOT}/watch/plugin/opencode" "${checkout}/watch/plugin/opencode"
    cp "${ROOT}/sandbox/lib/opencode-config.sh" "${checkout}/sandbox/lib/opencode-config.sh"
}

# prepare_opencode_cache_fallback_assets <home> <client> — like
# prepare_opencode_checkout_assets, but intentionally omits watch/plugin/opencode
# from the marketplace checkout, staging it only in the version-pinned plugin
# cache (mirroring prepare_bootstrap_cenci's cache root) so find_plugin_path
# falls through to its cache-fallback branch for that one asset: that branch
# strips the watch/plugin/ prefix before searching, producing a resolved path
# like .../cenci-watch/1.0.0/opencode with no watch/plugin/opencode substring
# in it at all — the exact shape that exposed the #491 review substring-
# matching bug in opencode_plugin_registered / uninstall_opencode_cleanup.
prepare_opencode_cache_fallback_assets() {
    local home="$1" client="$2" checkout cache_root
    checkout="${home}/.${client}/plugins/marketplaces/cenci"
    mkdir -p "${checkout}/flow/opencode" "${checkout}/sandbox/lib"
    cp "${ROOT}/flow/opencode/install-skills.sh" "${checkout}/flow/opencode/install-skills.sh"
    chmod +x "${checkout}/flow/opencode/install-skills.sh"
    cp -r "${ROOT}/flow/skills" "${checkout}/flow/skills"
    cp "${ROOT}/sandbox/lib/opencode-config.sh" "${checkout}/sandbox/lib/opencode-config.sh"
    if [[ "${client}" == claude ]]; then
        cache_root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    else
        cache_root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
    fi
    mkdir -p "${cache_root}"
    cp -r "${ROOT}/watch/plugin/opencode" "${cache_root}/opencode"
}

# prepare_bootstrap_cenci provisions the installed plugin cache without a
# binary. Its bootstrap creates the binary, proving a fresh installer run does
# not depend on a prior agent session or image build to make `cenci` runnable.
prepare_bootstrap_cenci() {
    local home="$1" client="$2" root manifest_dir bootstrap root_var
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
        bootstrap="${root}/hooks/bootstrap.sh"
        root_var=CLAUDE_PLUGIN_ROOT
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
        bootstrap="${root}/codex/bootstrap.sh"
        root_var=PLUGIN_ROOT
    fi
    [[ -e "${root}/${manifest_dir}/plugin.json" ]] && return 0
    mkdir -p "${root}/${manifest_dir}" "$(dirname "${bootstrap}")"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
    cat >"${bootstrap}" <<EOF
#!/bin/sh
root=\${${root_var}}
mkdir -p "\${root}/bin"
cat >"\${root}/bin/cenci" <<'BIN'
#!/bin/sh
printf 'cenci %s\n' "\$*" >>"\${CALLS_FILE}"
exit 0
BIN
chmod +x "\${root}/bin/cenci"
EOF
    chmod +x "${bootstrap}"
}

# prepare_broken_cenci_cache <home> <client> — provisions only the
# version-pinned plugin cache manifest, with no bootstrap script and no
# binary. Simulates an offline machine, unpublished release assets, or an
# otherwise-unpopulated plugin cache: current_cenci_binary can't provision
# anything from this cache.
prepare_broken_cenci_cache() {
    local home="$1" client="$2" root manifest_dir
    if [[ "${client}" == claude ]]; then
        root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.claude-plugin
    else
        root="${home}/.codex/plugins/cache/cenci/cenci-watch/1.0.0"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${root}/${manifest_dir}/plugin.json"
}

# run_case_broken_cenci_cache <name> <mode-and-flags...> — like run_case, but
# provisions a plugin cache with no bootstrap script (prepare_broken_cenci_cache)
# instead of a working one, so current_cenci_binary can't provision the cenci
# binary at all.
run_case_broken_cenci_cache() {
    local name="$1"
    shift
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    make_claude "${bin}"
    prepare_checkout "${home}" claude
    prepare_broken_cenci_cache "${home}" claude

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        bash "${ROOT}/install.sh" --yes --no-build "$@" >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
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
    local extra=()
    if [ "$#" -gt 3 ]; then extra=("${@:4}"); fi
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
      claude) make_claude "${bin}"; prepare_checkout "${home}" claude; prepare_bootstrap_cenci "${home}" claude ;;
      codex) make_codex "${bin}"; prepare_checkout "${home}" codex; prepare_bootstrap_cenci "${home}" codex ;;
      dual) make_claude "${bin}"; make_codex "${bin}"; prepare_checkout "${home}" claude; prepare_bootstrap_cenci "${home}" claude ;;
    esac

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" ${extra[@]+"${extra[@]}"} >"${output}" 2>&1
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

# run_case_opencode <name> <clients> <opencode-version|absent> [build_flag]
# [extra args...] — like run_case, but additionally provisions a mocked
# `opencode` executable (unless <opencode-version> is the literal string
# "absent") plus the real OpenCode assets step_opencode_setup will resolve
# via find_plugin_path (prepare_opencode_checkout_assets). step_opencode_setup
# (#491, not yet implemented) is assumed to gate its own ask_yn opt-in with a
# "y" default — mirroring the existing SwiftBar-widget precedent
# (step_cenci_watch_setup's `ask_yn "SwiftBar detected..." y`) for an
# already-detected optional integration — so plain --yes (no interactive tty
# stub) is expected to proceed with it once implemented.
run_case_opencode() {
    local name="$1" clients="$2" opencode_version="$3"
    shift 3
    local build_flag="${1:---no-build}"
    local extra=()
    if [ "$#" -gt 1 ]; then extra=("${@:2}"); fi
    local case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
    local bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
    mkdir -p "${home}" "${bin}"
    : >"${calls}"
    make_common_tools "${bin}"
    case "${clients}" in
    claude)
        make_claude "${bin}"
        prepare_checkout "${home}" claude
        prepare_bootstrap_cenci "${home}" claude
        prepare_opencode_checkout_assets "${home}" claude
        ;;
    codex)
        make_codex "${bin}"
        prepare_checkout "${home}" codex
        prepare_bootstrap_cenci "${home}" codex
        prepare_opencode_checkout_assets "${home}" codex
        ;;
    dual)
        make_claude "${bin}"
        make_codex "${bin}"
        prepare_checkout "${home}" claude
        prepare_bootstrap_cenci "${home}" claude
        prepare_opencode_checkout_assets "${home}" claude
        ;;
    esac
    if [ "${opencode_version}" != absent ]; then
        make_opencode "${bin}" "${opencode_version}"
    fi

    set +e
    env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
        CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
        CLAUDE_REFRESH_FAIL_FILE="${case_dir}/claude-refresh-fail" \
        CLAUDE_LIST_FAIL_FILE="${case_dir}/claude-list-fail" \
        CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
        CODEX_MARKETPLACE_FILE="${case_dir}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${case_dir}/codex-installed" \
        bash "${ROOT}/install.sh" --yes "${build_flag}" ${extra[@]+"${extra[@]}"} >"${output}" 2>&1
    CASE_EXIT=$?
    set -e
    CASE_OUTPUT="${output}"
    CASE_CALLS="${calls}"
    CASE_HOME="${home}"
    CASE_BIN="${bin}"
}

# run_opencode_doctor_case <name> <clients> <opencode-version|absent> — like
# run_doctor_case, but runs run_case_opencode first so the mocked `opencode`
# executable and checkout assets are in place for doctor to detect.
run_opencode_doctor_case() {
    local name="$1" clients="$2" opencode_version="$3"
    run_case_opencode "${name}" "${clients}" "${opencode_version}"
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
    HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CLAUDE_MARKETPLACE_FILE="${WORK}/wrapper-claude-marketplace" \
        CLAUDE_INSTALLED_FILE="${WORK}/wrapper-claude-installed" \
        CODEX_MARKETPLACE_FILE="${WORK}/wrapper-codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/wrapper-codex-installed" \
        "${CASE_HOME}/.local/bin/cenci-installer" doctor >"${output}"
    assert_contains "${output}" "cenci installer"
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
[[ -x "${CASE_HOME}/.local/bin/cenci" ]]
[[ -x "${CASE_HOME}/.local/bin/cn" ]]
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
assert_not_contains "${CASE_CALLS}" "--agents"

echo "case: image builds no longer select or bake agent CLIs"
prepare_cache_cenci "${WORK}/build-default/home" claude
run_case build-default dual --build
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "cenci sandbox build"
assert_not_contains "${CASE_CALLS}" "--agents"

echo "case: the removed installer --agents option is rejected"
prepare_cache_cenci "${WORK}/agents-removed/home" claude
run_case agents-removed dual --build --agents claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "unknown option '--agents'"
assert_not_contains "${CASE_CALLS}" "cenci sandbox build"

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
prepare_bootstrap_cenci "${home}" claude
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

echo "case: install repairs a missing core plugin without substring false positives"
name=repair-missing-core
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace"
touch "${WORK}/${name}/claude-installed-cenci-watch"
touch "${WORK}/${name}/claude-installed-cenci-sandbox"
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-sandbox@cenci"

echo "case: rerunning install updates every existing required plugin"
name=reconcile-existing
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace"
for p in cenci cenci-watch cenci-sandbox; do
    touch "${WORK}/${name}/claude-installed-${p}"
done
run_case "${name}" claude
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin update cenci@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-watch@cenci"
assert_contains "${CASE_CALLS}" "claude plugin update cenci-sandbox@cenci"

echo "case: marketplace refresh failure is visible and non-zero"
name=refresh-failure
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace" "${WORK}/${name}/claude-refresh-fail"
run_case "${name}" claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not refresh marketplace"
assert_not_contains "${CASE_OUTPUT}" "Claude: cenci updated"

echo "case: a plugin-list query failure is visible and non-zero, not a false 'not installed'"
name=list-failure
mkdir -p "${WORK}/${name}"
touch "${WORK}/${name}/claude-marketplace" "${WORK}/${name}/claude-list-fail"
run_case "${name}" claude
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not query installed plugins"

echo "case: a fresh install degrades gracefully when the cenci-watch bootstrap can't provision a binary yet"
run_case_broken_cenci_cache bootstrap-unavailable-install
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "could not provision the cenci binary yet"
assert_not_contains "${CASE_OUTPUT}" "could not provision the cenci binary from the installed cenci-watch plugin"

echo "case: an update still hard-fails when the cenci-watch bootstrap can't provision a binary"
run_case_broken_cenci_cache bootstrap-unavailable-update update
[[ "${CASE_EXIT}" -ne 0 ]]
assert_contains "${CASE_OUTPUT}" "could not provision the cenci binary from the installed cenci-watch plugin"

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
assert_not_contains "${DOCTOR_OUTPUT}" "binary not found yet"

echo "case: doctor's Codex notification hint reflects the user-level config.toml, never a differing project-level override Codex itself ignores (#416)"
name=doctor-codex-config-precedence
run_case "${name}" codex
[[ "${CASE_EXIT}" -eq 0 ]]
project="${WORK}/${name}/project"
mkdir -p "${project}/.codex" "${CASE_HOME}/.codex"
git -C "${project}" init -q
printf 'notification_method = "osc9"\n' >"${CASE_HOME}/.codex/config.toml"
printf 'notification_method = "bel"\n' >"${project}/.codex/config.toml"
output="${WORK}/${name}/doctor-precedence-output"
set +e
(
    cd "${project}" &&
    env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
        CODEX_MARKETPLACE_FILE="${WORK}/${name}/codex-marketplace" \
        CODEX_INSTALLED_FILE="${WORK}/${name}/codex-installed" \
        bash "${ROOT}/install.sh" doctor
) >"${output}" 2>&1
precedence_exit=$?
set -e
[[ "${precedence_exit}" -eq 0 ]]
assert_contains "${output}" "Codex notification_method: osc9"
assert_not_contains "${output}" "Codex notification_method: bel"

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

# --- Direct coverage of the repo-root `cenci` wrapper itself (the stable
# front door installed at ~/.local/bin/cenci-installer): it decides whether
# to refresh the owning client's marketplace checkout before exec'ing
# install.sh, and refuses to run as an updater from an unmanaged copy.

echo "case: the cenci wrapper's --help skips the marketplace refresh entirely"
name=wrapper-help
home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
prepare_checkout "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    "${home}/.claude/plugins/marketplaces/cenci/cenci" --help >"${output}" 2>&1
wrapper_help_exit=$?
set -e
[[ "${wrapper_help_exit}" -eq 0 ]]
assert_contains "${output}" "Usage:"
assert_not_contains "${calls}" "claude plugin marketplace update"

echo "case: the cenci wrapper still refreshes the marketplace when ~/.claude is itself a symlink"
name=wrapper-symlinked-claude-home
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"

real_claude_dir="${home}/dot-claude"
checkout="${real_claude_dir}/plugins/marketplaces/cenci"
mkdir -p "${checkout}"
cp "${ROOT}/cenci" "${checkout}/cenci"
chmod +x "${checkout}/cenci"
cp "${ROOT}/install.sh" "${checkout}/install.sh"
ln -s "${real_claude_dir}" "${home}/.claude"
prepare_bootstrap_cenci "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    "${home}/.claude/plugins/marketplaces/cenci/cenci" update --yes >"${output}" 2>&1
wrapper_update_exit=$?
set -e
[[ "${wrapper_update_exit}" -eq 0 ]]
assert_contains "${calls}" "claude plugin marketplace update cenci"

echo "case: the cenci wrapper refuses to update from an unexpected installer root, but --help still works"
name=wrapper-unexpected-root
plain="${WORK}/${name}/plain" bin="${WORK}/${name}/bin"
mkdir -p "${plain}" "${WORK}/${name}/home" "${bin}"
make_common_tools "${bin}"
cp "${ROOT}/cenci" "${plain}/cenci"
chmod +x "${plain}/cenci"
cp "${ROOT}/install.sh" "${plain}/install.sh"

update_output="${WORK}/${name}/update-output"
set +e
env -i HOME="${WORK}/${name}/home" PATH="${bin}" \
    "${plain}/cenci" update >"${update_output}" 2>&1
wrapper_root_update_exit=$?
set -e
[[ "${wrapper_root_update_exit}" -ne 0 ]]
assert_contains "${update_output}" "refusing to update from unexpected installer root"

help_output="${WORK}/${name}/help-output"
set +e
env -i HOME="${WORK}/${name}/home" PATH="${bin}" \
    "${plain}/cenci" --help >"${help_output}" 2>&1
wrapper_root_help_exit=$?
set -e
[[ "${wrapper_root_help_exit}" -eq 0 ]]
assert_contains "${help_output}" "Usage:"

# --- OpenCode wiring (#491): install.sh has no HAS_OPENCODE detection,
# doctor_opencode_support, or step_opencode_setup yet, so every case below
# currently fails (RED). It's expected to pass end-to-end once #491 lands.
#
# Assumed doctor-output contract these cases pin down for the implementation
# to satisfy (mirrors doctor_codex_support's existing wording style):
#   "OpenCode detected" / "OpenCode not detected"            — Supported clients
#   "OpenCode <ver> supports cenci integration"               — diagnostic 1 (installed+version)
#   "OpenCode <ver> is unsupported — cenci requires 1.18.3 or newer"
#   "OpenCode provider authenticated" / "... not authenticated" — diagnostic 2
#   "OpenCode skills linked" / "OpenCode skills not linked"     — diagnostic 3 (part a)
#   "OpenCode plugin registered" / "OpenCode plugin not registered" — diagnostic 3 (part b)

echo "case: OpenCode absent does not alter Claude/Codex install output (regression guard, #491)"
run_case_opencode opencode-absent-install claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_CALLS}" "claude plugin install cenci@cenci"
assert_not_contains "${CASE_OUTPUT}" "OpenCode"

echo "case: OpenCode absent still separates Claude/Codex detection from an explicit 'not detected' doctor line"
run_opencode_doctor_case opencode-absent-doctor claude absent
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "Claude Code detected"
assert_contains "${DOCTOR_OUTPUT}" "OpenCode not detected"

echo "case: OpenCode detected reports itself and its version in doctor output"
run_opencode_doctor_case opencode-version-ok claude 1.18.3
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode detected"
assert_contains "${DOCTOR_OUTPUT}" "OpenCode 1.18.3 supports cenci integration"

echo "case: an OpenCode version below 1.18.3 is flagged unsupported, not a hard doctor failure"
run_opencode_doctor_case opencode-version-old claude 1.17.0
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode 1.17.0 is unsupported — cenci requires 1.18.3 or newer"

echo "case: OpenCode provider authentication is flagged absent when neither env vars nor auth.json are present"
run_opencode_doctor_case opencode-provider-absent claude 1.18.3
[[ "${DOCTOR_EXIT}" -eq 0 ]]
assert_contains "${DOCTOR_OUTPUT}" "OpenCode provider not authenticated"

echo "case: OpenCode provider authentication is reported via an API key env var — no live API call"
name=opencode-provider-env
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
output="${WORK}/${name}/doctor-env-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    ANTHROPIC_API_KEY="sk-ant-test-not-a-real-key" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
provider_env_exit=$?
set -e
[[ "${provider_env_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode provider authenticated"

echo "case: OpenCode provider authentication is also reported via ~/.local/share/opencode/auth.json"
name=opencode-provider-authjson
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
mkdir -p "${CASE_HOME}/.local/share/opencode"
printf '{}\n' >"${CASE_HOME}/.local/share/opencode/auth.json"
output="${WORK}/${name}/doctor-authjson-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
provider_authjson_exit=$?
set -e
[[ "${provider_authjson_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode provider authenticated"

echo "case: OpenCode's cenci-integration diagnostics report independently — skills linked but the plugin not yet registered"
# Provisioned with opencode "absent" during the install run itself so
# step_opencode_setup never auto-links/auto-registers (which would otherwise
# fully complete both diagnostics before this case gets a chance to build a
# partial state — see the idempotent case below, which pins that a plain
# install run really does fully link+register when opencode is present).
# The opencode mock is added only for the follow-up doctor invocation, so
# doctor still detects and reports its version.
name=opencode-skills-only
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
make_opencode "${CASE_BIN}" 1.18.3
mkdir -p "${CASE_HOME}/.config/opencode/skills"
ln -s "${CASE_HOME}/.claude/plugins/marketplaces/cenci/flow/skills/testing" "${CASE_HOME}/.config/opencode/skills/testing"
output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
skills_only_exit=$?
set -e
[[ "${skills_only_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode 1.18.3 supports cenci integration"
assert_contains "${output}" "OpenCode skills linked"
assert_contains "${output}" "OpenCode plugin not registered"

echo "case: OpenCode's cenci-integration diagnostics report independently — plugin registered but skills not yet linked"
# Same "absent" provisioning rationale as the skills-only case above: keeps
# step_opencode_setup from auto-completing both diagnostics before this case
# builds its own partial (plugin-only) state.
name=opencode-plugin-only
run_case_opencode "${name}" claude absent
[[ "${CASE_EXIT}" -eq 0 ]]
make_opencode "${CASE_BIN}" 1.18.3
mkdir -p "${CASE_HOME}/.config/opencode"
printf '{"plugin": ["file://%s/watch/plugin/opencode"]}\n' "${CASE_HOME}/.claude/plugins/marketplaces/cenci" >"${CASE_HOME}/.config/opencode/opencode.json"
output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${output}" 2>&1
plugin_only_exit=$?
set -e
[[ "${plugin_only_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode plugin registered"
assert_contains "${output}" "OpenCode skills not linked"

echo "case: re-running install links OpenCode's portable skills idempotently and never duplicates the plugin entry"
name=opencode-idempotent
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
assert_contains "${CASE_OUTPUT}" "linked: 12, skipped: 0"
[[ -L "${CASE_HOME}/.config/opencode/skills/testing" ]]
[[ "$(grep -c 'watch/plugin/opencode' "${CASE_HOME}/.config/opencode/opencode.json")" -eq 1 ]]

second_output="${WORK}/${name}/second-run-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${second_output}" 2>&1
second_run_exit=$?
set -e
[[ "${second_run_exit}" -eq 0 ]]
assert_contains "${second_output}" "linked: 0, skipped: 12"
[[ "$(grep -c 'watch/plugin/opencode' "${CASE_HOME}/.config/opencode/opencode.json")" -eq 1 ]]

echo "case: uninstall removes only cenci's OpenCode skill symlinks and plugin entry, preserving the user's own config and skills"
name=opencode-uninstall
run_case_opencode "${name}" claude 1.18.3
[[ "${CASE_EXIT}" -eq 0 ]]
echo "user content" >"${CASE_HOME}/.config/opencode/skills/my-own-skill"
jq '. + {"theme": "dark"}' "${CASE_HOME}/.config/opencode/opencode.json" >"${CASE_HOME}/.config/opencode/opencode.json.tmp"
mv "${CASE_HOME}/.config/opencode/opencode.json.tmp" "${CASE_HOME}/.config/opencode/opencode.json"

uninstall_output="${WORK}/${name}/uninstall-output"
set +e
env -i HOME="${CASE_HOME}" PATH="${CASE_BIN}" CALLS_FILE="${CASE_CALLS}" \
    CLAUDE_MARKETPLACE_FILE="${WORK}/${name}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${WORK}/${name}/claude-installed" \
    bash "${ROOT}/install.sh" uninstall --yes >"${uninstall_output}" 2>&1
opencode_uninstall_exit=$?
set -e
[[ "${opencode_uninstall_exit}" -eq 0 ]]
[[ ! -e "${CASE_HOME}/.config/opencode/skills/testing" ]]
[[ -f "${CASE_HOME}/.config/opencode/skills/my-own-skill" ]]
assert_contains "${CASE_HOME}/.config/opencode/opencode.json" "theme"
assert_not_contains "${CASE_HOME}/.config/opencode/opencode.json" "watch/plugin/opencode"

echo "case: OpenCode plugin registration is recognized (install, doctor, and uninstall) even when find_plugin_path resolves watch/plugin/opencode via the plugin-cache fallback branch, not the marketplace checkout (#491 review fix — substring-matching bug)"
name=opencode-cache-fallback
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_opencode "${bin}" 1.18.3
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
prepare_opencode_cache_fallback_assets "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
cache_fallback_install_exit=$?
set -e
[[ "${cache_fallback_install_exit}" -eq 0 ]]
assert_contains "${output}" "OpenCode plugin registered"
assert_contains "${home}/.config/opencode/opencode.json" "cenci-watch/1.0.0/opencode"

doctor_output="${WORK}/${name}/doctor-output"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" doctor >"${doctor_output}" 2>&1
cache_fallback_doctor_exit=$?
set -e
[[ "${cache_fallback_doctor_exit}" -eq 0 ]]
assert_contains "${doctor_output}" "OpenCode plugin registered"

uninstall_output="${WORK}/${name}/uninstall-output"
set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    bash "${ROOT}/install.sh" uninstall --yes >"${uninstall_output}" 2>&1
cache_fallback_uninstall_exit=$?
set -e
[[ "${cache_fallback_uninstall_exit}" -eq 0 ]]
assert_not_contains "${home}/.config/opencode/opencode.json" "cenci-watch/1.0.0/opencode"

echo "case: a cenci-watch that never actually installs is pruned from SELECTED, so step_opencode_setup skips OpenCode plugin registration entirely instead of registering a deselected plugin's asset (#491 review fix — missing selected gate)"
name=opencode-cenci-watch-deselected
case_dir="${WORK}/${name}" home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin" output="${WORK}/${name}/output" calls="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${calls}"
make_common_tools "${bin}"
make_claude "${bin}"
make_opencode "${bin}" 1.18.3
prepare_checkout "${home}" claude
prepare_bootstrap_cenci "${home}" claude
prepare_opencode_checkout_assets "${home}" claude

set +e
env -i HOME="${home}" PATH="${bin}" CALLS_FILE="${calls}" \
    CLAUDE_MARKETPLACE_FILE="${case_dir}/claude-marketplace" \
    CLAUDE_INSTALLED_FILE="${case_dir}/claude-installed" \
    CLAUDE_SKIP_INSTALL_PLUGIN="cenci-watch" \
    bash "${ROOT}/install.sh" --yes --no-build >"${output}" 2>&1
deselected_exit=$?
set -e
[[ "${deselected_exit}" -eq 0 ]]
assert_not_contains "${output}" "Setting up OpenCode integration"
assert_not_contains "${output}" "OpenCode plugin registered"
[[ ! -e "${home}/.config/opencode/opencode.json" ]]

echo "passed: client detection, installation, launchers, and summaries"
