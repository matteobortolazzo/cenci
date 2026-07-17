#!/usr/bin/env bash
# End-to-end regression tests for host installer update behavior: the daemon
# restart path (restart_cenci_daemon in install.sh) must delegate to the
# just-installed binary's own `cenci daemon restart` lifecycle verb
# rather than install.sh reimplementing pkill/nohup restart logic itself. The
# old ad-hoc pkill/nohup restart survives only as a fallback for when the
# binary can't do it itself.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
PIDS_FILE="${WORK}/daemon-pids"

cleanup() {
    if [[ -f "${PIDS_FILE}" ]]; then
        while IFS= read -r pid; do kill "${pid}" 2>/dev/null || true; done <"${PIDS_FILE}"
    fi
    rm -rf "${WORK}"
}
trap cleanup EXIT

make_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep nohup chmod sed head rm mv; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/docker"
}

# make_logging_pkill_pgrep installs pkill/pgrep stubs (instead of the real
# system tools) that record every invocation to PKILL_LOG and behave like
# "nothing found" (pgrep's documented no-match exit code is 1), so a test can
# assert whether install.sh's own fallback path ever shelled out to them.
make_logging_pkill_pgrep() {
    local bin="$1"
    cat >"${bin}/pkill" <<'EOF'
#!/bin/sh
printf 'pkill %s\n' "$*" >>"${PKILL_LOG}"
exit 0
EOF
    cat >"${bin}/pgrep" <<'EOF'
#!/bin/sh
printf 'pgrep %s\n' "$*" >>"${PKILL_LOG}"
exit 1
EOF
    chmod +x "${bin}/pkill" "${bin}/pgrep"
}

make_client() {
    local bin="$1" client="$2"
    cat >"${bin}/${client}" <<EOF
#!/bin/sh
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ]; then
    if [ "${client}" = claude ]; then
        printf 'cenci@cenci\ncenci-watch@cenci\ncenci-sandbox@cenci\n'
    else
        printf 'cenci@cenci installed\ncenci-watch@cenci installed\ncenci-sandbox@cenci installed\n'
    fi
fi
exit 0
EOF
    chmod +x "${bin}/${client}"
}

# make_bump_client installs a claude/codex stub whose update verb simulates a
# real marketplace pull: it moves a staged new-version cache directory into
# place, so the installer observes a newer version-pinned cache entry after
# the update than before it.
make_bump_client() {
    local bin="$1" client="$2" staged="$3" dest="$4" manifest_dir="$5"
    cat >"${bin}/${client}" <<EOF
#!/bin/sh
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ]; then
    if [ "${client}" = claude ]; then
        echo 'cenci-watch@cenci'
    else
        echo 'cenci-watch@cenci installed'
    fi
fi
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = marketplace ] && [ "\${3:-}" = list ]; then
    echo 'cenci installed'
fi
case "\${2:-}:\${3:-}" in
update:cenci-watch* | add:cenci-watch*)
    if [ -d "${staged}" ]; then
        mv "${staged}" "${dest}"
        touch "${dest}/${manifest_dir}/plugin.json"
    fi
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/${client}"
}

# make_cenci writes a fake cenci binary at path that logs every
# invocation's argv (space-joined) to CALL_LOG. `daemon restart` exits
# restart_exit (0 = the binary handled its own restart; nonzero = simulate a
# binary that can't self-restart, so install.sh's fallback should kick in). A
# bare `daemon` invocation (the fallback's nohup-spawned form) loops until
# signaled, recording its pid to PIDS_FILE for the harness to reap.
# `sandbox update-plugins --all` (the running-sandbox plugin refresh, #461)
# exits refresh_exit (default 0), so a test can simulate a refresh failure
# and assert it only warns rather than failing the update.
make_cenci() {
    local path="$1" restart_exit="${2:-0}" refresh_exit="${3:-0}"
    mkdir -p "$(dirname "${path}")"
    cat >"${path}" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"\${CALL_LOG}"
# Regression probe (#353): if a host secret survives into this subprocess's
# environment, surface it in the captured call log so the sentinel-secret
# case can prove this test harness's env -i scrub keeps host secrets out.
[ -n "\${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "\${OPENAI_API_KEY}" >>"\${CALL_LOG}"
[ -n "\${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "\${CONTEXT7_API_KEY}" >>"\${CALL_LOG}"
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = restart ]; then
    exit ${restart_exit}
fi
if [ "\${1:-}" = daemon ] && [ -z "\${2:-}" ]; then
    echo "\$\$" >>"\${PIDS_FILE}"
    trap 'exit 0' TERM INT
    while :; do sleep 1; done
fi
if [ "\${1:-}" = sandbox ] && [ "\${2:-}" = update-plugins ] && [ "\${3:-}" = --all ]; then
    exit ${refresh_exit}
fi
exit 0
EOF
    chmod +x "${path}"
}

# prepare_checkout provides the stable marketplace files used for the managed
# cenci-installer launcher. A successful plugin refresh must leave that
# launcher resolvable as well as refreshing the version-pinned watch cache.
prepare_checkout() {
    local home="$1" client="$2" checkout
    case "${client}" in
        claude) checkout="${home}/.claude/plugins/marketplaces/cenci" ;;
        codex) checkout="${home}/.codex/plugins/marketplaces/cenci" ;;
        *) echo "FAIL: unknown test client ${client}" >&2; exit 1 ;;
    esac
    mkdir -p "${checkout}"
    cp "${ROOT}/cenci" "${ROOT}/install.sh" "${checkout}/"
    chmod +x "${checkout}/cenci" "${checkout}/install.sh"
}

# setup_layout provisions a fake HOME with a client plugin cache containing
# a single "updated" cenci binary (current_cenci_binary always finds
# a binary already in place, so step_cenci_setup's update path calls
# restart_cenci_daemon immediately, no bootstrap needed). make_client reports
# cenci/cenci-watch/cenci-sandbox as all already installed, so
# step_sandbox_refresh_plugins's `selected cenci-sandbox` gate stays true and
# it also runs on update; refresh_exit scripts that step's
# `sandbox update-plugins --all` exit code (default 0).
setup_layout() {
    local name="$1" client="$2" restart_exit="$3" refresh_exit="${4:-0}"
    local home="${WORK}/${name}/home" mock_bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls" pkill_log="${WORK}/${name}/pkill-calls"
    mkdir -p "${home}"
    : >"${call_log}"
    : >"${pkill_log}"
    make_tools "${mock_bin}"
    make_logging_pkill_pgrep "${mock_bin}"
    make_client "${mock_bin}" "${client}"
    prepare_checkout "${home}" "${client}"

    local cache_dir manifest_dir new_root new_bin
    if [[ "${client}" == claude ]]; then
        cache_dir="${home}/.claude/plugins/cache/cenci/cenci-watch"
        manifest_dir=.claude-plugin
    else
        cache_dir="${home}/.codex/plugins/cache/cenci/cenci-watch"
        manifest_dir=.codex-plugin
    fi
    new_root="${cache_dir}/2.0.0"
    new_bin="${new_root}/bin/cenci"
    make_cenci "${new_bin}" "${restart_exit}" "${refresh_exit}"
    mkdir -p "${new_root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"2.0.0"}\n' >"${new_root}/${manifest_dir}/plugin.json"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${mock_bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_PKILL_LOG="${pkill_log}"
    LAYOUT_NEW_BIN="${new_bin}"
}

# setup_bump_layout provisions a fake HOME whose plugin cache holds an
# installed 1.0.0 cenci-watch entry, plus a staged 2.0.0 entry that the
# bump-client stub moves into the cache when the installer runs the client's
# update verb — the minimal simulation of a real version-bumping update.
# Passing `none` as the third argument leaves the cache empty of version
# entries (a client without a version-pinned cache before the update).
setup_bump_layout() {
    local name="$1" client="$2" current="${3:-1.0.0}"
    local home="${WORK}/${name}/home" mock_bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls" pkill_log="${WORK}/${name}/pkill-calls"
    mkdir -p "${home}"
    : >"${call_log}"
    : >"${pkill_log}"
    make_tools "${mock_bin}"
    make_logging_pkill_pgrep "${mock_bin}"
    prepare_checkout "${home}" "${client}"

    local cache_dir manifest_dir
    if [[ "${client}" == claude ]]; then
        cache_dir="${home}/.claude/plugins/cache/cenci/cenci-watch"
        manifest_dir=.claude-plugin
    else
        cache_dir="${home}/.codex/plugins/cache/cenci/cenci-watch"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${cache_dir}"
    if [[ "${current}" != none ]]; then
        make_cenci "${cache_dir}/${current}/bin/cenci" 0
        mkdir -p "${cache_dir}/${current}/${manifest_dir}"
        printf '{"name":"cenci-watch","version":"%s"}\n' "${current}" >"${cache_dir}/${current}/${manifest_dir}/plugin.json"
    fi

    local staged="${WORK}/${name}/staged-2.0.0"
    make_cenci "${staged}/bin/cenci" 0
    mkdir -p "${staged}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"2.0.0"}\n' >"${staged}/${manifest_dir}/plugin.json"
    make_bump_client "${mock_bin}" "${client}" "${staged}" "${cache_dir}/2.0.0" "${manifest_dir}"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${mock_bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_PKILL_LOG="${pkill_log}"
}

run_update() {
    set +e
    env -i HOME="${LAYOUT_HOME}" PATH="${LAYOUT_BIN}" CALL_LOG="${LAYOUT_CALL_LOG}" \
        PIDS_FILE="${PIDS_FILE}" PKILL_LOG="${LAYOUT_PKILL_LOG}" \
        bash "${ROOT}/install.sh" update --yes --no-build >"${WORK}/last-output" 2>&1
    UPDATE_EXIT=$?
    set -e
}

# assert_not_leaked fails the suite if a sentinel secret value shows up in a
# captured file (the daemon call log or run_update's output).
assert_not_leaked() {
    local needle="$1" file="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: sentinel value '${needle}' leaked into ${file}" >&2
        cat "${file}" >&2
        exit 1
    fi
}

echo "install-update.test.sh"

echo "case: update invokes 'daemon restart' on the updated Claude-cache binary, never pkill"
setup_layout happy-claude claude 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'daemon restart' invocation in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if [[ -s "${LAYOUT_PKILL_LOG}" ]]; then
    echo "FAIL: expected pkill/pgrep to never be invoked when 'daemon restart' succeeds" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi
if [[ ! -L "${LAYOUT_HOME}/.local/bin/cenci" ]] ||
    [[ "$(readlink "${LAYOUT_HOME}/.local/bin/cenci")" != "${LAYOUT_NEW_BIN}" ]]; then
    echo "FAIL: Claude cache launcher does not point to the updated binary" >&2
    exit 1
fi

echo "case: update invokes 'daemon restart' on the updated Codex-cache binary, never pkill"
setup_layout happy-codex codex 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'daemon restart' invocation (codex cache) in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if [[ -s "${LAYOUT_PKILL_LOG}" ]]; then
    echo "FAIL: expected pkill/pgrep to never be invoked when 'daemon restart' succeeds (codex cache)" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi

echo "case: when 'daemon restart' fails, the installer falls back to a manual pkill/nohup restart"
setup_layout fallback claude 1
run_update
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the installer to still try 'daemon restart' first" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if ! grep -q '^pkill ' "${LAYOUT_PKILL_LOG}"; then
    echo "FAIL: expected the fallback path to invoke pkill after 'daemon restart' failed" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi
if ! grep -qx "daemon" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the fallback path to nohup-spawn a bare 'daemon' invocation" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if ! grep -q "restarted cenci with the updated binary" "${WORK}/last-output"; then
    echo "FAIL: expected the fallback restart to report success" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: host secrets in the parent env never reach the daemon call log or run_update output (regression, #353)"
setup_layout sentinel-secrets claude 0
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_update
unset OPENAI_API_KEY CONTEXT7_API_KEY
[[ "${UPDATE_EXIT}" -eq 0 ]]
assert_not_leaked "sk-test-sentinel-should-not-leak" "${LAYOUT_CALL_LOG}"
assert_not_leaked "sk-test-sentinel-should-not-leak" "${WORK}/last-output"
assert_not_leaked "ctx7-test-sentinel-should-not-leak" "${LAYOUT_CALL_LOG}"
assert_not_leaked "ctx7-test-sentinel-should-not-leak" "${WORK}/last-output"

echo "case: update output shows the Claude version transition and repairs missing plugins"
setup_bump_layout bump-claude claude
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch 1.0.0 → 2.0.0' in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
# This layout lists only cenci-watch. Reconciliation must add both missing
# components instead of treating update as a no-op for partially-installed
# stacks.
if ! grep -q 'Claude: cenci installed$' "${WORK}/last-output" || \
    ! grep -q 'Claude: cenci-sandbox installed$' "${WORK}/last-output"; then
    echo "FAIL: expected update to repair the missing cenci and cenci-sandbox plugins" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update output reports (already up to date) when no new version appears"
setup_layout current-claude claude 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch 2.0.0 (already up to date)' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch 2.0.0 (already up to date)' in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update output reports 'updated to <version>' when no cache entry existed before the update"
setup_bump_layout fresh-claude claude none
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch updated to 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch updated to 2.0.0' in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update output shows the Codex version transition (old → new)"
setup_bump_layout bump-codex codex
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Codex: cenci-watch 1.0.0 → 2.0.0' in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update refreshes plugins in running sandbox containers via 'sandbox update-plugins --all' (#461)"
setup_layout sandbox-refresh claude 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -qx "sandbox update-plugins --all" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'sandbox update-plugins --all' invocation in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi

echo "case: a sandbox plugin refresh failure warns but does not fail the update (#461)"
setup_layout sandbox-refresh-fails claude 0 1
run_update
if ! grep -qx "sandbox update-plugins --all" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'sandbox update-plugins --all' to still be attempted" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if [[ "${UPDATE_EXIT}" -ne 0 ]]; then
    echo "FAIL: a sandbox plugin refresh failure must not fail the update (UPDATE_EXIT=${UPDATE_EXIT})" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if ! grep -q "sandbox update-plugins --all" "${WORK}/last-output"; then
    echo "FAIL: expected the refresh failure to be reported (warned) in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "passed: restart path delegates to 'cenci daemon restart', falling back to pkill/nohup only on failure; update output reports per-plugin version transitions; sandbox plugin refresh runs best-effort on update (#461)"
