#!/usr/bin/env bash
# End-to-end regression tests for host installer update behavior:
#
#   1. The daemon restart path (restart_cenci_daemon in install.sh) must
#      delegate to the just-installed binary's own `cenci daemon restart`
#      lifecycle verb rather than install.sh reimplementing pkill/nohup
#      restart logic itself. The old ad-hoc pkill/nohup restart survives only
#      as a fallback for when the binary can't do it itself.
#
#   2. installed_plugin_version <claude|codex> <plugin> must report the
#      client's authoritative active-install state (#492) — Claude's
#      ~/.claude/plugins/installed_plugins.json, Codex's `plugin list --json`
#      — not a guess derived from plugin-cache manifest mtimes. The
#      cache-mtime machinery (newest_cenci_root/current_cenci_binary) stays
#      in place for binary/asset resolution, which is explicitly out of scope
#      for #492; the authoritative-state fixtures below deliberately keep
#      cache mtimes static or misleading so they can't accidentally make the
#      old heuristic pass.
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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep nohup chmod sed head rm mv jq; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/docker"
    # curl mock: install.sh's resolved-ref resolution (#625) now runs on
    # every dispatch path, including plain `update`, so every layout in this
    # file needs a curl on PATH — this suite is about the daemon-restart and
    # authoritative-state (#492) flows, not ref pinning, so the fake always
    # answers both probes as available (default "watch/v1.0.0" release tag,
    # always-published marketplace.json) rather than modeling #625's own
    # scenarios, which installer-clients.test.sh already covers directly.
    cat >"${bin}/curl" <<'EOF'
#!/bin/sh
out= url=
while [ "$#" -gt 0 ]; do
    case "$1" in
      -o) out=$2; shift 2 ;;
      -w) shift 2 ;;
      -*) shift ;;
      *) url=$1; shift ;;
    esac
done
case "${url}" in
*/releases/latest)
    printf 'https://github.com/matteobortolazzo/cenci/releases/tag/watch/v1.0.0'
    exit 0
    ;;
*/.claude-plugin/marketplace.json)
    exit 0
    ;;
esac
[ -n "${out}" ] && : >"${out}"
exit 0
EOF
    chmod +x "${bin}/curl"
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

# make_cenci writes a fake cenci binary at path that logs every
# invocation's argv (space-joined) to CALL_LOG. `daemon restart` exits
# restart_exit (0 = the binary handled its own restart; nonzero = simulate a
# binary that can't self-restart, so install.sh's fallback should kick in). A
# bare `daemon` invocation (the fallback's nohup-spawned form) loops until
# signaled, recording its pid to PIDS_FILE for the harness to reap.
# `sandbox update-plugins --all` (the running-sandbox plugin refresh, #461)
# exits refresh_exit (default 0), so a test can simulate a refresh failure
# and assert it only warns rather than failing the update. `sandbox build
# --check` (#519 — the freshness gate step_sandbox_setup consults before
# showing the rebuild prompt) exits check_exit: default 1 (not current /
# needs rebuild) so callers that never care about the check keep exercising
# today's always-ask behavior unless they opt into a current-image (0) fixture.
make_cenci() {
    local path="$1" restart_exit="${2:-0}" refresh_exit="${3:-0}" check_exit="${4:-1}"
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
if [ "\${1:-}" = sandbox ] && [ "\${2:-}" = build ] && [ "\${3:-}" = --check ]; then
    exit ${check_exit}
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
# `sandbox update-plugins --all` exit code (default 0); check_exit scripts
# that binary's `sandbox build --check` exit code (default 1 — see
# make_cenci) for the #519 rebuild-prompt-skip PTY cases below.
setup_layout() {
    local name="$1" client="$2" restart_exit="$3" refresh_exit="${4:-0}" check_exit="${5:-1}"
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
    make_cenci "${new_bin}" "${restart_exit}" "${refresh_exit}" "${check_exit}"
    mkdir -p "${new_root}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"2.0.0"}\n' >"${new_root}/${manifest_dir}/plugin.json"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${mock_bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_PKILL_LOG="${pkill_log}"
    LAYOUT_NEW_BIN="${new_bin}"
}

# run_installer_pty <install|update> <feed> drives install.sh through a real
# controlling terminal via `script -qec` (per the #519 plan's confirmed Q&A:
# BUILD_IMAGE=ask's confirmation is inherently TTY-only — --yes/no-tty force
# BUILD_IMAGE=no before ever reaching it, so a sourced/non-PTY test can never
# exercise the real interactive path). <feed> is piped into the pty's stdin
# (interpreted with printf '%b', so "y\n" answers the build prompt with
# "yes"; "" sends immediate EOF, which makes every ask_yn — including the
# build prompt itself, when it fires — silently take its default answer
# instead of blocking). --no-lazyboards is always appended so the unrelated
# lazyboards/cleanup prompts can never fire, leaving the sandbox build
# prompt (and, in install mode, the unconditional Linux GUI-bar PATH-link
# prompt in setup_cenci_linux_path, answered by its own EOF default) as the
# only interactive gates. Sets PTY_EXIT; output lands in
# "${WORK}/last-output" (mirrors run_update's capture convention).
run_installer_pty() {
    local mode="$1" feed="$2" mode_arg=""
    [ "${mode}" = update ] && mode_arg=update
    local wrapper="${WORK}/pty-wrapper-$$-${RANDOM}.sh"
    cat >"${wrapper}" <<EOF
#!/bin/sh
exec env -i HOME="${LAYOUT_HOME}" PATH="${LAYOUT_BIN}" CALL_LOG="${LAYOUT_CALL_LOG}" \
    PIDS_FILE="${PIDS_FILE}" PKILL_LOG="${LAYOUT_PKILL_LOG}" \
    bash "${ROOT}/install.sh" ${mode_arg} --no-lazyboards
EOF
    chmod +x "${wrapper}"
    set +e
    printf '%b' "${feed}" | timeout 20 script -qec "${wrapper}" /dev/null >"${WORK}/last-output" 2>&1
    PTY_EXIT=$?
    set -e
}

# setup_broken_binary_layout provisions a fake HOME with all three plugins
# reported installed (make_client), but a version-pinned cenci-watch cache
# manifest with no bootstrap script and no binary — current_cenci_binary can
# never provision a binary from this cache (mirrors
# prepare_broken_cenci_cache in installer-clients.test.sh). Regression for
# step_sandbox_refresh_plugins's silent-failure guard (#461 follow-up): an
# update run with cenci-sandbox selected must warn instead of silently
# skipping the running-sandbox plugin refresh when current_cenci_binary
# fails.
setup_broken_binary_layout() {
    local name="$1" client="$2"
    local home="${WORK}/${name}/home" mock_bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls" pkill_log="${WORK}/${name}/pkill-calls"
    mkdir -p "${home}"
    : >"${call_log}"
    : >"${pkill_log}"
    make_tools "${mock_bin}"
    make_logging_pkill_pgrep "${mock_bin}"
    make_client "${mock_bin}" "${client}"
    prepare_checkout "${home}" "${client}"

    local cache_dir manifest_dir
    if [[ "${client}" == claude ]]; then
        cache_dir="${home}/.claude/plugins/cache/cenci/cenci-watch"
        manifest_dir=.claude-plugin
    else
        cache_dir="${home}/.codex/plugins/cache/cenci/cenci-watch"
        manifest_dir=.codex-plugin
    fi
    mkdir -p "${cache_dir}/1.0.0/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${cache_dir}/1.0.0/${manifest_dir}/plugin.json"

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
# captured file (the daemon call log or run_update's output). Also reused
# below (#492) to assert that a foreign-marketplace/similarly-named plugin's
# noise version never leaks into the reconciliation output.
assert_not_leaked() {
    local needle="$1" file="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: sentinel value '${needle}' leaked into ${file}" >&2
        cat "${file}" >&2
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Authoritative-state fixtures (#492): installed_plugin_version <claude|codex>
# <plugin> must report the client's authoritative active-install state, not a
# guess derived from plugin-cache manifest mtimes. newest_cenci_root /
# current_cenci_binary (binary/asset resolution) stay cache-mtime based —
# explicitly out of scope for this ticket — so every fixture below still
# seeds a working version-pinned cenci-watch cache purely so the update can
# reach daemon restart, but never relies on that cache's mtime to prove a
# version-reporting assertion.
# ---------------------------------------------------------------------------

# new_layout <name> <client> [jq=yes|no] provisions a fresh fake HOME + mock
# PATH (shared tool/pkill/pgrep stubs) and the marketplace checkout, without
# any plugin-specific client stub or cache — callers add those.
new_layout() {
    local name="$1" client="$2" want_jq="${3:-yes}"
    local home="${WORK}/${name}/home" mock_bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls" pkill_log="${WORK}/${name}/pkill-calls"
    mkdir -p "${home}"
    : >"${call_log}"
    : >"${pkill_log}"
    make_tools "${mock_bin}"
    if [[ "${want_jq}" == no ]]; then
        rm -f "${mock_bin}/jq"
    fi
    make_logging_pkill_pgrep "${mock_bin}"
    prepare_checkout "${home}" "${client}"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${mock_bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_PKILL_LOG="${pkill_log}"
}

# watch_cache_dir/watch_manifest_dir resolve the version-pinned cenci-watch
# cache location for a client, matching newest_cenci_root's glob targets.
watch_cache_dir() {
    if [[ "$2" == claude ]]; then
        printf '%s/.claude/plugins/cache/cenci/cenci-watch\n' "$1"
    else
        printf '%s/.codex/plugins/cache/cenci/cenci-watch\n' "$1"
    fi
}

watch_manifest_dir() {
    if [[ "$1" == claude ]]; then printf '.claude-plugin\n'; else printf '.codex-plugin\n'; fi
}

# seed_watch_cache <home> <client> <version> [restart_exit] — populates a
# single version-pinned cenci-watch cache entry with a working binary: the
# minimum current_cenci_binary/newest_cenci_root need to provision a binary
# and let the update proceed to daemon restart. Deliberately never touched
# again by the authoritative-state fixtures below.
seed_watch_cache() {
    local home="$1" client="$2" version="$3" restart_exit="${4:-0}"
    local cache_dir manifest_dir
    cache_dir="$(watch_cache_dir "${home}" "${client}")"
    manifest_dir="$(watch_manifest_dir "${client}")"
    make_cenci "${cache_dir}/${version}/bin/cenci" "${restart_exit}"
    mkdir -p "${cache_dir}/${version}/${manifest_dir}"
    printf '{"name":"cenci-watch","version":"%s"}\n' "${version}" >"${cache_dir}/${version}/${manifest_dir}/plugin.json"
}

# claude_meta_file <home> — ensures and prints the path to Claude's
# authoritative active-install record.
claude_meta_file() {
    local meta="$1/.claude/plugins/installed_plugins.json"
    mkdir -p "$(dirname "${meta}")"
    printf '%s\n' "${meta}"
}

# claude_meta_json <key=version=installPath[=scope]> ... — builds a compact
# installed_plugins.json "plugins" object via the host's own jq (fixture
# construction only; the generated claude stub below never shells out to jq
# itself — it only ever printfs a pre-baked snapshot, per the plan's
# printf-only stub constraint). scope defaults to "user" when omitted.
# Passing multiple specs for the *same* key appends multiple coexisting
# records to that key's array (in the order given), letting a test construct
# the multi-record-per-key shape claude_plugin_version's scope preference
# resolves (#492 regression 6) instead of the single-record-per-key shape
# every other fixture uses.
claude_meta_json() {
    local json='{"version":2,"plugins":{}}' spec key rest version rest2 path scope
    for spec in "$@"; do
        key="${spec%%=*}"
        rest="${spec#*=}"
        version="${rest%%=*}"
        rest2="${rest#*=}"
        path="${rest2%%=*}"
        if [[ "${rest2}" == *"="* ]]; then
            scope="${rest2#*=}"
        else
            scope="user"
        fi
        json="$(jq -c --arg k "${key}" --arg v "${version}" --arg p "${path}" --arg s "${scope}" \
            '.plugins[$k] = ((.plugins[$k] // []) + [{"scope":$s,"installPath":$p,"version":$v}])' <<<"${json}")"
    done
    printf '%s' "${json}"
}

# codex_state_json <pluginId=version> ... — same idea for the JSON the fake
# Codex's `plugin list --json` reads from a state file.
codex_state_json() {
    local json='{"installed":[]}' spec id version
    for spec in "$@"; do
        id="${spec%%=*}"
        version="${spec#*=}"
        json="$(jq -c --arg id "${id}" --arg v "${version}" '.installed += [{"pluginId":$id,"version":$v}]' <<<"${json}")"
    done
    printf '%s' "${json}"
}

# make_claude_version_stub <bin> <plugin_list_body> <update_cases> writes the
# fake `claude` used by every authoritative-state case: `plugin list` /
# `plugin marketplace list` answer from literal fixture text (same convention
# as make_client above), and `plugin update <key>@cenci` rewrites the caller's
# installed_plugins.json via a plain `printf` of a pre-baked snapshot (see
# claude_meta_json) — never jq — matching real Claude's behavior of updating
# its own active-install record on a successful update. <update_cases> is
# built by the caller (which already knows the meta file path) and spliced
# verbatim into a `case "$2:$3" in ... esac` dispatch, so a scenario with no
# matching arm leaves the meta file completely untouched (used by the
# missing/corrupt-state cases).
make_claude_version_stub() {
    local bin="$1" plugin_list="$2" update_cases="$3"
    cat >"${bin}/claude" <<EOF
#!/bin/sh
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ]; then
    printf '%s\n' '${plugin_list}'
fi
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = marketplace ] && [ "\${3:-}" = list ]; then
    printf 'cenci installed\n'
fi
# Minimal #625/#490 interface-contract guard: this suite is about the
# daemon-restart and authoritative-state (#492) flows, not ref pinning, but a
# regression back to a ref-less \`marketplace add\` must not pass silently
# just because this fake never checked. See installer-clients.test.sh's
# make_claude for the full reference pattern.
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = marketplace ] && [ "\${3:-}" = add ]; then
    case "\${4:-}" in
      *@*) : ;;
      *) printf 'error: plugin marketplace add requires owner/repo@ref (#490)\n' >&2; exit 1 ;;
    esac
fi
case "\${2:-}:\${3:-}" in
${update_cases}
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

# make_codex_version_stub <bin> <json_state_file> <plugin_list_body>
# <add_cases> [list_json_exit=0] — Codex analog: `plugin list --json` cats the
# current contents of <json_state_file> (letting a test flip, corrupt, or
# delete it independently of `plugin add`); `plugin add <key>@cenci`
# overwrites it via printf per <add_cases>, same no-jq-in-the-stub constraint
# as Claude. <list_json_exit> lets a test simulate the `codex plugin list
# --json` command itself failing (#492 regression 8a), distinct from it
# succeeding but emitting invalid/empty JSON.
make_codex_version_stub() {
    local bin="$1" state_file="$2" plugin_list="$3" add_cases="$4" list_json_exit="${5:-0}"
    cat >"${bin}/codex" <<EOF
#!/bin/sh
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ] && [ "\${3:-}" = --json ]; then
    [ -f "${state_file}" ] && cat "${state_file}"
    exit ${list_json_exit}
fi
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ]; then
    printf '%s\n' '${plugin_list}'
fi
case "\${2:-}:\${3:-}" in
${add_cases}
esac
exit 0
EOF
    chmod +x "${bin}/codex"
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

echo "case: when current_cenci_binary can't provision a binary, the sandbox plugin refresh warns instead of failing silently (#461)"
setup_broken_binary_layout no-binary claude
run_update
if ! grep -q "cenci binary not available — skipping running-sandbox plugin refresh; run manually with: cenci sandbox update-plugins --all" "${WORK}/last-output"; then
    echo "FAIL: expected the sandbox plugin refresh to warn when current_cenci_binary fails" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if grep -qx "sandbox update-plugins --all" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the refresh to never invoke 'sandbox update-plugins --all' without a resolvable binary" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi

echo "case: Claude real transition — authoritative state moves 1.0.0 -> 2.0.0 despite coexisting caches with a misleading mtime (#492 regression 1)"
new_layout claude-coexist claude
CACHE_DIR="$(watch_cache_dir "${LAYOUT_HOME}" claude)"
seed_watch_cache "${LAYOUT_HOME}" claude 2.0.0
seed_watch_cache "${LAYOUT_HOME}" claude 1.0.0
# The old (1.0.0) manifest gets an equal/newer mtime than the new (2.0.0) one
# without ever touching 2.0.0 (the winner) — the fixture must not force the
# old mtime heuristic to point at the correct answer.
touch -r "${CACHE_DIR}/2.0.0/.claude-plugin/plugin.json" "${CACHE_DIR}/1.0.0/.claude-plugin/plugin.json"
META="$(claude_meta_file "${LAYOUT_HOME}")"
printf '%s\n' "$(claude_meta_json "cenci-watch@cenci=1.0.0=${CACHE_DIR}/1.0.0")" >"${META}"
AFTER_JSON="$(claude_meta_json "cenci-watch@cenci=2.0.0=${CACHE_DIR}/2.0.0")"
UPDATE_CASES=$(cat <<CASE_EOF
update:cenci-watch@cenci)
    printf '%s\n' '${AFTER_JSON}' > "${META}"
    ;;
CASE_EOF
)
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" "${UPDATE_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch 1.0.0 → 2.0.0' despite coexisting 1.0.0/2.0.0 caches" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
# This layout's plugin list reports only cenci-watch. Reconciliation must add
# both missing components instead of treating update as a no-op for
# partially-installed stacks.
if ! grep -q 'Claude: cenci installed$' "${WORK}/last-output" || \
    ! grep -q 'Claude: cenci-sandbox installed$' "${WORK}/last-output"; then
    echo "FAIL: expected update to repair the missing cenci and cenci-sandbox plugins" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: Codex real transition — authoritative state moves 1.0.0 -> 2.0.0 while the version-pinned cache stays untouched (#492 regression 2)"
new_layout codex-transition codex
seed_watch_cache "${LAYOUT_HOME}" codex 1.0.0
STATE_FILE="${WORK}/codex-transition/codex-state.json"
printf '%s\n' "$(codex_state_json "cenci-watch@cenci=1.0.0")" >"${STATE_FILE}"
ADD_CASES=$(cat <<CASE_EOF
add:cenci-watch@cenci)
    printf '%s\n' '$(codex_state_json "cenci-watch@cenci=2.0.0")' > "${STATE_FILE}"
    ;;
CASE_EOF
)
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-watch@cenci installed" "${ADD_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Codex: cenci-watch 1.0.0 → 2.0.0' in the update output" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: Claude true no-op — authoritative state is 2.0.0 before and after (#492 regression 3, Claude)"
new_layout claude-noop claude
seed_watch_cache "${LAYOUT_HOME}" claude 2.0.0
CACHE_DIR="$(watch_cache_dir "${LAYOUT_HOME}" claude)"
META="$(claude_meta_file "${LAYOUT_HOME}")"
STATE_JSON="$(claude_meta_json "cenci-watch@cenci=2.0.0=${CACHE_DIR}/2.0.0")"
printf '%s\n' "${STATE_JSON}" >"${META}"
UPDATE_CASES=$(cat <<CASE_EOF
update:cenci-watch@cenci)
    printf '%s\n' '${STATE_JSON}' > "${META}"
    ;;
CASE_EOF
)
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" "${UPDATE_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch 2.0.0 (already up to date)' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch 2.0.0 (already up to date)' for a true no-op" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: Codex true no-op — authoritative state is 2.0.0 before and after (#492 regression 3, Codex)"
new_layout codex-noop codex
seed_watch_cache "${LAYOUT_HOME}" codex 2.0.0
STATE_FILE="${WORK}/codex-noop/codex-state.json"
STATE_JSON="$(codex_state_json "cenci-watch@cenci=2.0.0")"
printf '%s\n' "${STATE_JSON}" >"${STATE_FILE}"
ADD_CASES=$(cat <<CASE_EOF
add:cenci-watch@cenci)
    printf '%s\n' '${STATE_JSON}' > "${STATE_FILE}"
    ;;
CASE_EOF
)
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-watch@cenci installed" "${ADD_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch 2.0.0 (already up to date)' "${WORK}/last-output"; then
    echo "FAIL: expected 'Codex: cenci-watch 2.0.0 (already up to date)' for a true no-op" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update output reports 'updated to <version>' when no authoritative record existed before the update"
new_layout claude-fresh-record claude
seed_watch_cache "${LAYOUT_HOME}" claude 2.0.0
CACHE_DIR="$(watch_cache_dir "${LAYOUT_HOME}" claude)"
META="$(claude_meta_file "${LAYOUT_HOME}")"
rm -f "${META}"
AFTER_JSON="$(claude_meta_json "cenci-watch@cenci=2.0.0=${CACHE_DIR}/2.0.0")"
UPDATE_CASES=$(cat <<CASE_EOF
update:cenci-watch@cenci)
    printf '%s\n' '${AFTER_JSON}' > "${META}"
    ;;
CASE_EOF
)
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" "${UPDATE_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch updated to 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Claude: cenci-watch updated to 2.0.0' when no prior authoritative record existed" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update succeeds despite a corrupt installed_plugins.json — falls back to the plain 'updated' wording (#492 regression 4a)"
new_layout claude-corrupt-meta claude
seed_watch_cache "${LAYOUT_HOME}" claude 1.0.0
META="$(claude_meta_file "${LAYOUT_HOME}")"
printf 'not json {\n' >"${META}"
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" ""
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch updated$' "${WORK}/last-output"; then
    echo "FAIL: expected the bare 'Claude: cenci-watch updated' fallback with a corrupt installed_plugins.json" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if ! grep -Fq 'not json {' "${META}"; then
    echo "FAIL: expected the corrupt installed_plugins.json to remain unreadable (before and after)" >&2
    exit 1
fi

echo "case: update succeeds when jq is absent from PATH — falls back to the plain 'updated' wording (#492 regression 4b, jq-absent guard)"
new_layout claude-no-jq claude no
seed_watch_cache "${LAYOUT_HOME}" claude 1.0.0
CACHE_DIR="$(watch_cache_dir "${LAYOUT_HOME}" claude)"
META="$(claude_meta_file "${LAYOUT_HOME}")"
printf '%s\n' "$(claude_meta_json "cenci-watch@cenci=1.0.0=${CACHE_DIR}/1.0.0")" >"${META}"
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" ""
if [[ -e "${LAYOUT_BIN}/jq" ]]; then
    echo "FAIL: test setup error — jq must be absent from the mock PATH for this case" >&2
    exit 1
fi
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch updated$' "${WORK}/last-output"; then
    echo "FAIL: expected the bare 'Claude: cenci-watch updated' fallback when jq is unavailable" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: exact <plugin>@cenci identity — a foreign marketplace and a similarly-named plugin never influence version resolution, and 'cenci' never resolves to cenci-watch's version (#492 regression 5)"
new_layout claude-identity claude
seed_watch_cache "${LAYOUT_HOME}" claude 2.0.0
META="$(claude_meta_file "${LAYOUT_HOME}")"
STATE_JSON="$(claude_meta_json \
    "cenci@cenci=3.0.0=/fake/cache/cenci/3.0.0" \
    "cenci-watch@cenci=2.0.0=/fake/cache/cenci-watch/2.0.0" \
    "cenci-sandbox@cenci=1.5.0=/fake/cache/cenci-sandbox/1.5.0" \
    "cenci-watch@other=9.9.9=/fake/other-cache/cenci-watch/9.9.9" \
    "cenci-watch-extra@cenci=8.8.8=/fake/cache/cenci-watch-extra/8.8.8")"
printf '%s\n' "${STATE_JSON}" >"${META}"
PLUGIN_LIST=$'cenci@cenci\ncenci-watch@cenci\ncenci-sandbox@cenci'
UPDATE_CASES=$(cat <<CASE_EOF
update:cenci@cenci)
    printf '%s\n' '${STATE_JSON}' > "${META}"
    ;;
update:cenci-watch@cenci)
    printf '%s\n' '${STATE_JSON}' > "${META}"
    ;;
update:cenci-sandbox@cenci)
    printf '%s\n' '${STATE_JSON}' > "${META}"
    ;;
CASE_EOF
)
make_claude_version_stub "${LAYOUT_BIN}" "${PLUGIN_LIST}" "${UPDATE_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
for line in \
    'Claude: cenci 3.0.0 (already up to date)' \
    'Claude: cenci-watch 2.0.0 (already up to date)' \
    'Claude: cenci-sandbox 1.5.0 (already up to date)'; do
    if ! grep -Fq "${line}" "${WORK}/last-output"; then
        echo "FAIL: expected '${line}' in the update output" >&2
        cat "${WORK}/last-output" >&2
        exit 1
    fi
done
assert_not_leaked "9.9.9" "${WORK}/last-output"
assert_not_leaked "8.8.8" "${WORK}/last-output"

echo "case: claude_plugin_version prefers the 'user'-scope record when multiple coexist for one key, not the first array element (#492 regression 6, scope-preference)"
new_layout claude-scope-pref claude
seed_watch_cache "${LAYOUT_HOME}" claude 2.0.0
META="$(claude_meta_file "${LAYOUT_HOME}")"
# Order deliberately puts the non-"user" record first and the "user" record
# last, so a naive "first record wins" implementation would report 9.9.9
# (the "project"-scope record) as the old version instead of the correct
# "user"-scope 1.0.0.
STATE_JSON="$(claude_meta_json \
    "cenci-watch@cenci=9.9.9=/fake/cache/cenci-watch/9.9.9=project" \
    "cenci-watch@cenci=1.0.0=/fake/cache/cenci-watch/1.0.0=user")"
printf '%s\n' "${STATE_JSON}" >"${META}"
AFTER_JSON="$(claude_meta_json "cenci-watch@cenci=2.0.0=/fake/cache/cenci-watch/2.0.0=user")"
UPDATE_CASES=$(cat <<CASE_EOF
update:cenci-watch@cenci)
    printf '%s\n' '${AFTER_JSON}' > "${META}"
    ;;
CASE_EOF
)
make_claude_version_stub "${LAYOUT_BIN}" "cenci-watch@cenci" "${UPDATE_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Claude: cenci-watch 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected claude_plugin_version to prefer the 'user'-scope record (1.0.0), not the first array element ('project' scope, 9.9.9)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
assert_not_leaked "9.9.9" "${WORK}/last-output"

echo "case: Codex real transition — authoritative state moves 1.0.0 -> 2.0.0 for cenci-sandbox (#492 regression 7, resolver genericity)"
new_layout codex-sandbox-transition codex
seed_watch_cache "${LAYOUT_HOME}" codex 1.0.0
STATE_FILE="${WORK}/codex-sandbox-transition/codex-state.json"
printf '%s\n' "$(codex_state_json "cenci-sandbox@cenci=1.0.0")" >"${STATE_FILE}"
ADD_CASES=$(cat <<CASE_EOF
add:cenci-sandbox@cenci)
    printf '%s\n' '$(codex_state_json "cenci-sandbox@cenci=2.0.0")' > "${STATE_FILE}"
    ;;
CASE_EOF
)
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-sandbox@cenci installed" "${ADD_CASES}"
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-sandbox 1.0.0 → 2.0.0' "${WORK}/last-output"; then
    echo "FAIL: expected 'Codex: cenci-sandbox 1.0.0 → 2.0.0' in the update output (resolver must be generic across plugins, not cenci-watch-specific)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: Codex update succeeds when 'codex plugin list --json' itself fails — falls back to the plain 'updated' wording (#492 regression 8a, codex command-failure guard)"
new_layout codex-list-fails codex
seed_watch_cache "${LAYOUT_HOME}" codex 1.0.0
# STATE_FILE deliberately never created: the stub's --json branch exits
# non-zero instead of emitting any state, exercising codex_plugin_version's
# `codex plugin list --json ... || return 0` guard rather than its
# invalid-JSON guard (covered separately below).
STATE_FILE="${WORK}/codex-list-fails/codex-state.json"
ADD_CASES=$(cat <<CASE_EOF
add:cenci-watch@cenci)
    printf '%s\n' '$(codex_state_json "cenci-watch@cenci=2.0.0")' > "${STATE_FILE}"
    ;;
CASE_EOF
)
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-watch@cenci installed" "${ADD_CASES}" 1
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch updated$' "${WORK}/last-output"; then
    echo "FAIL: expected the bare 'Codex: cenci-watch updated' fallback when 'codex plugin list --json' fails" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: Codex update succeeds despite a corrupt state file — falls back to the plain 'updated' wording (#492 regression 8b, codex invalid-JSON guard)"
new_layout codex-corrupt-state codex
seed_watch_cache "${LAYOUT_HOME}" codex 1.0.0
STATE_FILE="${WORK}/codex-corrupt-state/codex-state.json"
printf 'not json {\n' >"${STATE_FILE}"
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-watch@cenci installed" ""
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch updated$' "${WORK}/last-output"; then
    echo "FAIL: expected the bare 'Codex: cenci-watch updated' fallback with a corrupt codex state file" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if ! grep -Fq 'not json {' "${STATE_FILE}"; then
    echo "FAIL: expected the corrupt codex state file to remain unreadable (before and after)" >&2
    exit 1
fi

echo "case: Codex update succeeds when jq is absent from PATH — falls back to the plain 'updated' wording (#492 regression 8c, codex jq-absent guard)"
new_layout codex-no-jq codex no
seed_watch_cache "${LAYOUT_HOME}" codex 1.0.0
STATE_FILE="${WORK}/codex-no-jq/codex-state.json"
printf '%s\n' "$(codex_state_json "cenci-watch@cenci=1.0.0")" >"${STATE_FILE}"
make_codex_version_stub "${LAYOUT_BIN}" "${STATE_FILE}" "cenci-watch@cenci installed" ""
if [[ -e "${LAYOUT_BIN}/jq" ]]; then
    echo "FAIL: test setup error — jq must be absent from the mock PATH for this case" >&2
    exit 1
fi
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -q 'Codex: cenci-watch updated$' "${WORK}/last-output"; then
    echo "FAIL: expected the bare 'Codex: cenci-watch updated' fallback when jq is unavailable (codex path)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# #519 — step_sandbox_setup must consult `sandbox build --check` before
# showing the BUILD_IMAGE=ask rebuild prompt, skipping it entirely when the
# resolved image is already current, for both install and update modes. The
# ask branch is inherently TTY-only (--yes/no-tty force BUILD_IMAGE=no first),
# so these cases drive the real interactive path via a PTY (script -qec) per
# the plan's confirmed Q&A, rather than a sourceable helper.
# ---------------------------------------------------------------------------

echo "case: update — a current sandbox image skips the rebuild prompt entirely, no 'sandbox build' call (#519)"
setup_layout check-current-update claude 0 0 0
run_installer_pty update ""
[[ "${PTY_EXIT}" -eq 0 ]]
if grep -qx "sandbox build" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected no 'sandbox build' call when the image is already current (update mode)" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if grep -q "Build the sandbox container image now" "${WORK}/last-output"; then
    echo "FAIL: expected the rebuild confirmation prompt to be skipped entirely when the image is current (update mode)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: install — a current sandbox image skips the rebuild prompt entirely, no 'sandbox build' call (#519)"
setup_layout check-current-install claude 0 0 0
run_installer_pty install ""
[[ "${PTY_EXIT}" -eq 0 ]]
if grep -qx "sandbox build" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected no 'sandbox build' call when the image is already current (install mode)" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if grep -q "Build the sandbox container image now" "${WORK}/last-output"; then
    echo "FAIL: expected the rebuild confirmation prompt to be skipped entirely when the image is current (install mode)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "case: update — a stale/missing sandbox image still shows the rebuild prompt, and builds once confirmed (#519)"
setup_layout check-stale-update claude 0 0 1
run_installer_pty update "y\n"
[[ "${PTY_EXIT}" -eq 0 ]]
if ! grep -q "Build the sandbox container image now" "${WORK}/last-output"; then
    echo "FAIL: expected the rebuild confirmation prompt to show when the image needs a rebuild (update mode)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if ! grep -qx "sandbox build" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected a 'sandbox build' call once the rebuild prompt is confirmed (update mode)" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi

echo "case: install — a stale/missing sandbox image still shows the rebuild prompt, and builds once confirmed (#519)"
setup_layout check-stale-install claude 0 0 1
run_installer_pty install "y\n"
[[ "${PTY_EXIT}" -eq 0 ]]
if ! grep -q "Build the sandbox container image now" "${WORK}/last-output"; then
    echo "FAIL: expected the rebuild confirmation prompt to show when the image needs a rebuild (install mode)" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi
if ! grep -qx "sandbox build" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected a 'sandbox build' call once the rebuild prompt is confirmed (install mode)" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi

echo "passed: restart path delegates to 'cenci daemon restart', falling back to pkill/nohup only on failure; update output reports per-plugin version transitions from authoritative client state (#492); sandbox plugin refresh runs best-effort on update (#461); rebuild prompt is skipped when the sandbox image is already current, for both install and update (#519)"
