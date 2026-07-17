#!/bin/bash
# Tests for the installed_plugins.json healing logic in
# sandbox/lib/migrate-settings.sh.
#
# An interrupted plugin auto-install leaves installed_plugins.json recording an
# installPath whose cache directory was never populated. Claude Code trusts the
# metadata, skips reinstall, finds no skill files, and every skill of that
# plugin becomes "Unknown command" — permanently, because nothing self-heals.
# These tests assert heal_plugin_installs() drops exactly the broken records so
# Claude Code reinstalls on next launch.
#
# Runs on the host with the system `jq` — no Docker required. Sources the same
# heal_plugin_installs() the entrypoint uses.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/migrate-settings.sh
source "${SCRIPT_DIR}/../lib/migrate-settings.sh"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

# assert_file_jq <label> <file> <jq-filter-that-must-be-true>
assert_file_jq() {
    local label="$1" file="$2" filter="$3"
    if jq -e "${filter}" "${file}" >/dev/null 2>&1; then
        pass
    else
        fail "${label} (filter: ${filter})"
    fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# new_case <name>: fresh fake plugins dir in PLUGINS_DIR, path to the metadata
# file in META.
new_case() {
    PLUGINS_DIR="${WORK}/$1/plugins"
    META="${PLUGINS_DIR}/installed_plugins.json"
    mkdir -p "${PLUGINS_DIR}"
}

# seed_cache <marketplace/plugin/version>: create a populated-looking cache dir
# and echo its absolute path.
seed_cache() {
    local dir="${PLUGINS_DIR}/cache/$1"
    mkdir -p "${dir}"
    touch "${dir}/plugin.json"
    echo "${dir}"
}

echo "heal-plugins.test.sh"

# ── Case 1: healthy metadata is left untouched ────────────────────
new_case healthy
GOOD_PATH="$(seed_cache cenci/cenci/3.0.0)"
cat > "${META}" <<EOF
{"version":2,"plugins":{"cenci@cenci":[{"scope":"user","installPath":"${GOOD_PATH}","version":"3.0.0"}]}}
EOF
BEFORE="$(jq -S . "${META}")"
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: healthy metadata untouched"
if [[ -f "${META}" && "$(jq -S . "${META}")" == "${BEFORE}" ]]; then
    pass
else
    fail "healthy installed_plugins.json was modified or removed"
fi

# ── Case 2: one broken plugin among two → only the broken one dropped ─
new_case one-broken
GOOD_PATH="$(seed_cache cenci/cenci-watch/2.2.1)"
cat > "${META}" <<EOF
{"version":2,"plugins":{
  "cenci-watch@cenci":[{"scope":"user","installPath":"${GOOD_PATH}","version":"2.2.1"}],
  "cenci@cenci":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/cenci/cenci/3.0.0","version":"3.0.0"}]
}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: one broken plugin among two"
assert_file_jq "drops the plugin with a missing installPath" "${META}" '.plugins | has("cenci@cenci") | not'
assert_file_jq "keeps the healthy plugin"                    "${META}" '.plugins["cenci-watch@cenci"][0].version == "2.2.1"'
assert_file_jq "preserves the version field"                 "${META}" '.version == 2'

# ── Case 3: one broken record inside a plugin's record array ─────
new_case one-broken-record
GOOD_PATH="$(seed_cache cenci/cenci/3.0.0)"
cat > "${META}" <<EOF
{"version":2,"plugins":{"cenci@cenci":[
  {"scope":"user","installPath":"${GOOD_PATH}","version":"3.0.0"},
  {"scope":"user","installPath":"${PLUGINS_DIR}/cache/cenci/cenci/2.9.0","version":"2.9.0"}
]}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: one broken record inside a plugin"
assert_file_jq "drops only the broken record" "${META}" '.plugins["cenci@cenci"] | length == 1'
assert_file_jq "keeps the healthy record"     "${META}" '.plugins["cenci@cenci"][0].version == "3.0.0"'

# ── Case 4: all installPaths missing → empty plugins object ──────
new_case all-broken
cat > "${META}" <<EOF
{"version":2,"plugins":{"cenci@cenci":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/cenci/cenci/3.0.0","version":"3.0.0"}]}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: all installPaths missing"
assert_file_jq "file is still valid JSON with empty plugins" "${META}" '.plugins == {}'
assert_file_jq "preserves the version field"                 "${META}" '.version == 2'

# ── Case 5: invalid JSON → file removed so Claude Code regenerates it ─
new_case invalid-json
echo 'not json {' > "${META}"
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: invalid JSON"
if [[ ! -e "${META}" ]]; then
    pass
else
    fail "invalid installed_plugins.json was not removed"
fi

# ── Case 6: no metadata file → no-op, no error ────────────────────
new_case no-file
echo "case: no metadata file"
if heal_plugin_installs "${PLUGINS_DIR}" && [[ ! -e "${META}" ]]; then
    pass
else
    fail "missing installed_plugins.json should be a successful no-op"
fi

# ── Case 7: idempotency ──────────────────────────────────────────
new_case idempotent
GOOD_PATH="$(seed_cache cenci/cenci-watch/2.2.1)"
cat > "${META}" <<EOF
{"version":2,"plugins":{
  "cenci-watch@cenci":[{"scope":"user","installPath":"${GOOD_PATH}","version":"2.2.1"}],
  "cenci@cenci":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/cenci/cenci/3.0.0","version":"3.0.0"}]
}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
ONCE="$(jq -S . "${META}")"
heal_plugin_installs "${PLUGINS_DIR}"
TWICE="$(jq -S . "${META}")"
echo "case: idempotency"
if [[ "${ONCE}" == "${TWICE}" ]]; then
    pass
else
    fail "running heal twice differs from running it once"
fi

####################################################################
# provision_plugins tests
#
# Claude Code 2.1.207's settings-driven auto-install (enabledPlugins +
# extraKnownMarketplaces) writes installed_plugins.json without ever
# populating plugins/cache/ — deterministic, reproduced on a bare
# `claude -p "hi"`. provision_plugins() stops trusting that path and
# calls the official `claude plugin marketplace add` / `claude plugin
# install` CLI directly, which is idempotent and (per manual testing)
# repairs a metadata-present/cache-missing state on its own.
#
# `claude` is stubbed with a recording fake prepended to PATH: it logs
# every invocation to CLAUDE_FAKE_LOG and, unless CLAUDE_FAKE_EXIT is
# set, simulates the side effects a real install/marketplace-add would
# have (populates marketplaces/<name> or cache/<mkt>/<plugin>/<version>
# plus a matching installed_plugins.json entry) so the heal->provision
# composition case can be exercised without Docker or a real network.
####################################################################

FAKE_BIN="${WORK}/fake-bin"
mkdir -p "${FAKE_BIN}"

# make_fake_claude: (re)writes the fake `claude` executable. Reads
# CLAUDE_FAKE_LOG, CLAUDE_FAKE_PLUGINS_DIR, CLAUDE_FAKE_MARKETPLACE and
# (optionally) CLAUDE_FAKE_EXIT from the environment at call time.
make_fake_claude() {
    cat > "${FAKE_BIN}/claude" <<'FAKE'
#!/bin/bash
echo "$*" >> "${CLAUDE_FAKE_LOG}"
if [[ "${CLAUDE_FAKE_EXIT:-0}" -ne 0 ]]; then
    exit "${CLAUDE_FAKE_EXIT}"
fi
if [[ "$1" == "plugin" && "$2" == "marketplace" && "$3" == "add" ]]; then
    mkdir -p "${CLAUDE_FAKE_PLUGINS_DIR}/marketplaces/${CLAUDE_FAKE_MARKETPLACE}"
elif [[ "$1" == "plugin" && "$2" == "marketplace" && "$3" == "update" ]]; then
    : # refresh is a no-op: tests seed marketplace.json directly
elif [[ "$1" == "plugin" && "$2" == "update" ]]; then
    spec="$3"
    plugin="${spec%@*}"
    mkt="${spec#*@}"
    manifest="${CLAUDE_FAKE_PLUGINS_DIR}/marketplaces/${mkt}/.claude-plugin/marketplace.json"
    version="$(jq -r --arg name "${plugin}" '[.plugins[]? | select(.name == $name) | .version][0]' "${manifest}")"
    cache_dir="${CLAUDE_FAKE_PLUGINS_DIR}/cache/${mkt}/${plugin}/${version}"
    mkdir -p "${cache_dir}"
    touch "${cache_dir}/plugin.json"
    meta="${CLAUDE_FAKE_PLUGINS_DIR}/installed_plugins.json"
    jq --arg key "${spec}" --arg path "${cache_dir}" --arg v "${version}" \
        '.plugins[$key] = [{"scope":"user","installPath":$path,"version":$v}]' \
        "${meta}" > "${meta}.tmp" && mv "${meta}.tmp" "${meta}"
elif [[ "$1" == "plugin" && "$2" == "install" ]]; then
    spec="$3"
    plugin="${spec%@*}"
    mkt="${spec#*@}"
    cache_dir="${CLAUDE_FAKE_PLUGINS_DIR}/cache/${mkt}/${plugin}/1.0.0"
    mkdir -p "${cache_dir}"
    touch "${cache_dir}/plugin.json"
    meta="${CLAUDE_FAKE_PLUGINS_DIR}/installed_plugins.json"
    [[ -f "${meta}" ]] || echo '{"version":2,"plugins":{}}' > "${meta}"
    jq --arg key "${spec}" --arg path "${cache_dir}" \
        '.plugins[$key] = [{"scope":"user","installPath":$path,"version":"1.0.0"}]' \
        "${meta}" > "${meta}.tmp" && mv "${meta}.tmp" "${meta}"
fi
exit 0
FAKE
    chmod +x "${FAKE_BIN}/claude"
}

# new_provision_case <name>: fresh fake plugins dir + fake claude log, no
# claude on PATH by default (tests opt in with WITH_FAKE_CLAUDE).
new_provision_case() {
    PLUGINS_DIR="${WORK}/$1/plugins"
    mkdir -p "${PLUGINS_DIR}"
    CLAUDE_FAKE_LOG="${WORK}/$1/claude.log"
    : > "${CLAUDE_FAKE_LOG}"
    export CLAUDE_FAKE_LOG
    export CLAUDE_FAKE_PLUGINS_DIR="${PLUGINS_DIR}"
    export CLAUDE_FAKE_MARKETPLACE="cenci"
    unset CLAUDE_FAKE_EXIT
}

log_count() {
    wc -l < "${CLAUDE_FAKE_LOG}" | tr -d ' '
}

echo
echo "provision_plugins tests"

# ── Case 8: no claude on PATH → no-op, exit 0 ─────────────────────
new_provision_case no-claude
PATH_NO_CLAUDE="$(echo "${PATH}" | tr ':' '\n' | grep -v "^${FAKE_BIN}$" | paste -sd: -)"
echo "case: no claude on PATH"
if PATH="${PATH_NO_CLAUDE}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch; then
    pass
else
    fail "provision_plugins should return 0 when claude is not on PATH"
fi

# ── Case 9: marketplace present + both plugins already installed → zero calls ─
new_provision_case already-provisioned
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/cenci"
AF_PATH="${PLUGINS_DIR}/cache/cenci/cenci/3.0.0"
AW_PATH="${PLUGINS_DIR}/cache/cenci/cenci-watch/2.2.1"
mkdir -p "${AF_PATH}" "${AW_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{
  "cenci@cenci":[{"scope":"user","installPath":"${AF_PATH}","version":"3.0.0"}],
  "cenci-watch@cenci":[{"scope":"user","installPath":"${AW_PATH}","version":"2.2.1"}]
}}
EOF
echo "case: already provisioned"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch
if [[ "$(log_count)" -eq 0 ]]; then
    pass
else
    fail "expected zero claude invocations, got $(log_count): $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 10: marketplace dir missing → marketplace add invoked ───
new_provision_case missing-marketplace
make_fake_claude
AF_PATH="${PLUGINS_DIR}/cache/cenci/cenci/3.0.0"
mkdir -p "${AF_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"cenci@cenci":[{"scope":"user","installPath":"${AF_PATH}","version":"3.0.0"}]}}
EOF
echo "case: marketplace dir missing"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci
if grep -q "^plugin marketplace add matteobortolazzo/cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected 'plugin marketplace add matteobortolazzo/cenci' in log: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 11: one plugin missing from metadata → exactly one install call ─
new_provision_case one-missing-plugin
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/cenci"
AW_PATH="${PLUGINS_DIR}/cache/cenci/cenci-watch/2.2.1"
mkdir -p "${AW_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"cenci-watch@cenci":[{"scope":"user","installPath":"${AW_PATH}","version":"2.2.1"}]}}
EOF
echo "case: one plugin missing from metadata"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch
INSTALL_CALLS="$(grep -c "^plugin install " "${CLAUDE_FAKE_LOG}" || true)"
if [[ "${INSTALL_CALLS}" -eq 1 ]] && grep -q "^plugin install cenci@cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected exactly one 'plugin install cenci@cenci', got: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 12: heal -> provision composition ────────────────────────
new_provision_case heal-then-provision
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/cenci"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"cenci@cenci":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/cenci/cenci/3.0.0","version":"3.0.0"}]}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: heal drops broken record, provision reinstalls it"
assert_file_jq "heal dropped the broken record first" "${PLUGINS_DIR}/installed_plugins.json" '.plugins == {}'
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci
assert_file_jq "provision reinstalled cenci" "${PLUGINS_DIR}/installed_plugins.json" '.plugins["cenci@cenci"][0].installPath | length > 0'
if grep -q "^plugin install cenci@cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected provision to reinstall the plugin heal dropped: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 13: claude exits 1 → provision still returns 0 (never blocks boot) ─
new_provision_case claude-fails
make_fake_claude
export CLAUDE_FAKE_EXIT=1
echo "case: claude failures never block boot"
if PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch 2>/dev/null; then
    pass
else
    fail "provision_plugins must return 0 even when claude fails"
fi
unset CLAUDE_FAKE_EXIT

# ── Case 14: idempotency — second run makes zero claude calls ────
new_provision_case provision-idempotent
make_fake_claude
echo "case: idempotency"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch >/dev/null
FIRST_RUN_CALLS="$(log_count)"
: > "${CLAUDE_FAKE_LOG}"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch >/dev/null
SECOND_RUN_CALLS="$(log_count)"
if [[ "${FIRST_RUN_CALLS}" -gt 0 && "${SECOND_RUN_CALLS}" -eq 0 ]]; then
    pass
else
    fail "expected calls on first run and zero on second, got ${FIRST_RUN_CALLS} then ${SECOND_RUN_CALLS}"
fi

####################################################################
# update_plugins tests
#
# provision_plugins only installs what's missing — it never version-checks,
# so an existing home volume keeps stale plugins forever while the
# marketplace auto-bumps on every push to main. update_plugins() refreshes
# the marketplace clone (`claude plugin marketplace update`), then calls
# `claude plugin update` only for plugins whose installed version differs
# from the clone's marketplace.json. A stamp file TTL-gates the whole pass
# (ttl 0 = forced, the manual `cenci sandbox update-plugins` path), and like
# provisioning it never blocks boot.
####################################################################

STAMP_NAME=".cenci-sand-update-stamp"

# seed_marketplace <version>: write a marketplace.json advertising cenci
# and cenci-watch at <version> in the fake marketplace clone.
seed_marketplace() {
    local dir="${PLUGINS_DIR}/marketplaces/cenci/.claude-plugin"
    mkdir -p "${dir}"
    jq -n --arg v "$1" \
        '{name:"cenci", plugins:[{name:"cenci",version:$v},{name:"cenci-watch",version:$v}]}' \
        > "${dir}/marketplace.json"
}

# seed_installed <plugin> <version>: populated cache dir + metadata record.
seed_installed() {
    local path meta="${PLUGINS_DIR}/installed_plugins.json"
    path="$(seed_cache "cenci/$1/$2")"
    [[ -f "${meta}" ]] || echo '{"version":2,"plugins":{}}' > "${meta}"
    jq --arg key "$1@cenci" --arg path "${path}" --arg v "$2" \
        '.plugins[$key] = [{"scope":"user","installPath":$path,"version":$v}]' \
        "${meta}" > "${meta}.tmp" && mv "${meta}.tmp" "${meta}"
}

echo
echo "update_plugins tests"

# ── Case 15: stale plugin → exactly one plugin update call ───────
new_provision_case stale-plugin
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
seed_installed cenci-watch 4.7.1
echo "case: stale plugin is updated"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci cenci-watch
if grep -q "^plugin marketplace update cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected 'plugin marketplace update cenci' in log: $(cat "${CLAUDE_FAKE_LOG}")"
fi
UPDATE_CALLS="$(grep -c "^plugin update " "${CLAUDE_FAKE_LOG}" || true)"
if [[ "${UPDATE_CALLS}" -eq 1 ]] && grep -q "^plugin update cenci@cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected exactly one 'plugin update cenci@cenci', got: $(cat "${CLAUDE_FAKE_LOG}")"
fi
assert_file_jq "metadata ends at the marketplace version" \
    "${PLUGINS_DIR}/installed_plugins.json" '.plugins["cenci@cenci"][0].version == "4.7.1"'
if [[ -f "${PLUGINS_DIR}/${STAMP_NAME}" ]]; then
    pass
else
    fail "expected update stamp to be created"
fi

# ── Case 16: everything current → marketplace refresh only ───────
new_provision_case all-current
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.7.1
seed_installed cenci-watch 4.7.1
echo "case: up-to-date plugins are not updated"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci cenci-watch
if ! grep -q "^plugin update " "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected zero 'plugin update' calls, got: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 17: fresh stamp within TTL → zero claude calls ──────────
new_provision_case fresh-stamp
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
touch "${PLUGINS_DIR}/${STAMP_NAME}"
echo "case: fresh stamp within TTL skips everything"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci cenci-watch
if [[ "$(log_count)" -eq 0 ]]; then
    pass
else
    fail "expected zero claude invocations, got $(log_count): $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 18: stale stamp (older than TTL) → update runs ──────────
new_provision_case stale-stamp
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
touch -d '31 minutes ago' "${PLUGINS_DIR}/${STAMP_NAME}"
echo "case: stale stamp runs the update"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci
if grep -q "^plugin update cenci@cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected stale stamp to trigger an update: $(cat "${CLAUDE_FAKE_LOG}")"
fi
# ...and the stamp was refreshed (newer than the 31-minutes-ago seed).
if [[ -n "$(find "${PLUGINS_DIR}/${STAMP_NAME}" -mmin -1 2>/dev/null)" ]]; then
    pass
else
    fail "expected the stamp to be refreshed after an update pass"
fi

# ── Case 19: ttl 0 forces the update despite a fresh stamp ───────
new_provision_case forced-update
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
touch "${PLUGINS_DIR}/${STAMP_NAME}"
echo "case: ttl 0 forces the update"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 0 cenci
if grep -q "^plugin update cenci@cenci$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected ttl 0 to bypass the stamp: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 20: claude exits 1 → returns 0 (never blocks boot) ──────
new_provision_case update-claude-fails
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
export CLAUDE_FAKE_EXIT=1
echo "case: claude failures never block boot"
if PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci 2>/dev/null; then
    pass
else
    fail "update_plugins must return 0 even when claude fails"
fi
unset CLAUDE_FAKE_EXIT

# ── Case 21: marketplace.json missing → no update calls, exit 0 ──
new_provision_case no-manifest
make_fake_claude
seed_installed cenci 4.0.0
echo "case: missing marketplace.json"
if PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci; then
    pass
else
    fail "update_plugins must return 0 when marketplace.json is missing"
fi
if ! grep -q "^plugin update " "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected no 'plugin update' calls without a manifest: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 22: plugin not installed → left to provision_plugins ────
new_provision_case not-installed
make_fake_claude
seed_marketplace 4.7.1
seed_installed cenci-watch 4.7.1
echo "case: uninstalled plugin is not updated"
PATH="${FAKE_BIN}:${PATH}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci cenci-watch
if ! grep -q "^plugin update " "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected no 'plugin update' calls for an uninstalled plugin: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 23: no claude on PATH → no-op, exit 0, no stamp ─────────
# An empty PATH dir (not PATH_NO_CLAUDE, which only strips the fake bin and
# would fall through to a real host `claude`) — update_plugins must bail on
# `command -v claude` before touching anything.
new_provision_case update-no-claude
EMPTY_BIN="${WORK}/empty-bin"
mkdir -p "${EMPTY_BIN}"
seed_marketplace 4.7.1
seed_installed cenci 4.0.0
echo "case: no claude on PATH"
if PATH="${EMPTY_BIN}" update_plugins "${PLUGINS_DIR}" cenci 30 cenci; then
    pass
else
    fail "update_plugins should return 0 when claude is not on PATH"
fi
if [[ ! -f "${PLUGINS_DIR}/${STAMP_NAME}" ]]; then
    pass
else
    fail "expected no stamp without a claude binary"
fi

####################################################################
# Codex plugin provisioning/update tests
####################################################################

make_fake_codex() {
    cat > "${FAKE_BIN}/codex" <<'FAKE'
#!/bin/bash
echo "$*" >> "${CODEX_FAKE_LOG}"
if [[ "${CODEX_FAKE_EXIT:-0}" -ne 0 ]]; then
    exit "${CODEX_FAKE_EXIT}"
fi
case "$1 $2 $3" in
    "plugin marketplace list")
        [[ "${CODEX_FAKE_MARKETPLACE_PRESENT:-false}" == true ]] && echo "cenci /fake/cenci"
        ;;
    "plugin marketplace add")
        export CODEX_FAKE_MARKETPLACE_PRESENT=true
        ;;
    "plugin marketplace upgrade") ;;
    "plugin list --json")
        # Older codex releases have no --json; simulate them with a usage
        # error so the version gate falls back to the re-add path.
        if [[ "${CODEX_FAKE_NO_JSON:-false}" == true ]]; then
            echo "error: unknown flag --json" >&2
            exit 2
        fi
        printf '{"installed":['
        sep=""
        if [[ "${CODEX_FAKE_CENCI_PRESENT:-false}" == true ]]; then
            printf '%s{"pluginId":"cenci@cenci","version":"%s"}' "${sep}" "${CODEX_FAKE_CENCI_VERSION:-1.0.0}"
            sep=","
        fi
        if [[ "${CODEX_FAKE_CENCI_WATCH_PRESENT:-false}" == true ]]; then
            printf '%s{"pluginId":"cenci-watch@cenci","version":"%s"}' "${sep}" "${CODEX_FAKE_CENCI_WATCH_VERSION:-1.0.0}"
        fi
        printf ']}\n'
        ;;
    "plugin list "*)
        [[ "${CODEX_FAKE_CENCI_PRESENT:-false}" == true ]] && echo "cenci@cenci installed enabled"
        [[ "${CODEX_FAKE_CENCI_WATCH_PRESENT:-false}" == true ]] && echo "cenci-watch@cenci installed enabled"
        ;;
    "plugin add cenci@cenci")
        export CODEX_FAKE_CENCI_PRESENT=true
        ;;
    "plugin add cenci-watch@cenci")
        export CODEX_FAKE_CENCI_WATCH_PRESENT=true
        ;;
esac
exit 0
FAKE
    chmod +x "${FAKE_BIN}/codex"
}

new_codex_case() {
    CODEX_DIR="${WORK}/$1/codex"
    mkdir -p "${CODEX_DIR}"
    CODEX_FAKE_LOG="${WORK}/$1/codex.log"
    : > "${CODEX_FAKE_LOG}"
    export CODEX_FAKE_LOG
    export CODEX_FAKE_MARKETPLACE_PRESENT=false
    export CODEX_FAKE_CENCI_PRESENT=false
    export CODEX_FAKE_CENCI_WATCH_PRESENT=false
    unset CODEX_FAKE_EXIT CODEX_FAKE_NO_JSON CODEX_FAKE_CENCI_VERSION CODEX_FAKE_CENCI_WATCH_VERSION
}

# write_codex_snapshot_manifest <cenci-version> <cenci-watch-version>: seed
# the marketplace snapshot manifest the version gate reads, mirroring where
# codex clones marketplaces (<codex-home>/.tmp/marketplaces/<name>).
write_codex_snapshot_manifest() {
    mkdir -p "${CODEX_DIR}/.tmp/marketplaces/cenci/.claude-plugin"
    cat > "${CODEX_DIR}/.tmp/marketplaces/cenci/.claude-plugin/marketplace.json" <<EOF
{"name":"cenci","plugins":[{"name":"cenci","version":"$1"},{"name":"cenci-watch","version":"$2"}]}
EOF
}

codex_log_count() {
    wc -l < "${CODEX_FAKE_LOG}" | tr -d ' '
}

echo
echo "Codex plugin tests"

new_codex_case codex-missing-all
make_fake_codex
echo "case: Codex provisions a missing marketplace and plugins"
PATH="${FAKE_BIN}:${PATH}" provision_codex_plugins "${CODEX_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch
if grep -q '^plugin marketplace add matteobortolazzo/cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex marketplace was not added"; fi
if grep -q '^plugin add cenci@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci was not installed"; fi
if grep -q '^plugin add cenci-watch@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci-watch was not installed"; fi

new_codex_case codex-idempotent
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
echo "case: Codex provisioning is idempotent"
PATH="${FAKE_BIN}:${PATH}" provision_codex_plugins "${CODEX_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch
if ! grep -Eq '^plugin (marketplace add|add) ' "${CODEX_FAKE_LOG}"; then
    pass
else
    fail "Codex provisioning mutated an already provisioned home: $(cat "${CODEX_FAKE_LOG}")"
fi

new_codex_case codex-ttl
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
touch "${CODEX_DIR}/${STAMP_NAME}"
echo "case: Codex fresh TTL stamp skips refresh"
PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 30 cenci cenci-watch
if [[ "$(codex_log_count)" -eq 0 ]]; then pass; else fail "Codex TTL did not skip all CLI calls"; fi

new_codex_case codex-forced-current
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
export CODEX_FAKE_CENCI_VERSION=1.0.0 CODEX_FAKE_CENCI_WATCH_VERSION=2.0.0
write_codex_snapshot_manifest 1.0.0 2.0.0
touch "${CODEX_DIR}/${STAMP_NAME}"
echo "case: Codex ttl 0 refreshes the marketplace but skips re-adding current plugins"
PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 0 cenci cenci-watch
if grep -q '^plugin marketplace upgrade cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex marketplace was not forcibly refreshed"; fi
if ! grep -q '^plugin add ' "${CODEX_FAKE_LOG}"; then
    pass
else
    fail "Codex re-added plugins although installed versions match the snapshot: $(cat "${CODEX_FAKE_LOG}")"
fi

new_codex_case codex-forced-stale
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
export CODEX_FAKE_CENCI_VERSION=1.0.0 CODEX_FAKE_CENCI_WATCH_VERSION=2.0.0
write_codex_snapshot_manifest 1.0.0 2.1.0
touch "${CODEX_DIR}/${STAMP_NAME}"
echo "case: Codex ttl 0 re-adds only the plugin whose snapshot version moved"
PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 0 cenci cenci-watch
if grep -q '^plugin add cenci-watch@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex stale cenci-watch was not refreshed"; fi
if ! grep -q '^plugin add cenci@cenci$' "${CODEX_FAKE_LOG}"; then
    pass
else
    fail "Codex re-added cenci although its version is current: $(cat "${CODEX_FAKE_LOG}")"
fi

new_codex_case codex-forced-no-json
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
export CODEX_FAKE_NO_JSON=true
write_codex_snapshot_manifest 1.0.0 2.0.0
touch "${CODEX_DIR}/${STAMP_NAME}"
echo "case: Codex without plugin list --json falls back to re-adding (repair-safe)"
PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 0 cenci cenci-watch
if grep -q '^plugin add cenci@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci cache was not refreshed on --json fallback"; fi
if grep -q '^plugin add cenci-watch@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci-watch cache was not refreshed on --json fallback"; fi

new_codex_case codex-forced-no-manifest
make_fake_codex
export CODEX_FAKE_MARKETPLACE_PRESENT=true
export CODEX_FAKE_CENCI_PRESENT=true
export CODEX_FAKE_CENCI_WATCH_PRESENT=true
export CODEX_FAKE_CENCI_VERSION=1.0.0 CODEX_FAKE_CENCI_WATCH_VERSION=2.0.0
touch "${CODEX_DIR}/${STAMP_NAME}"
echo "case: Codex without a snapshot manifest falls back to re-adding (repair-safe)"
PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 0 cenci cenci-watch
if grep -q '^plugin add cenci@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci cache was not refreshed on manifest fallback"; fi
if grep -q '^plugin add cenci-watch@cenci$' "${CODEX_FAKE_LOG}"; then pass; else fail "Codex cenci-watch cache was not refreshed on manifest fallback"; fi

new_codex_case codex-fails
make_fake_codex
export CODEX_FAKE_EXIT=1
echo "case: Codex CLI failures never block boot"
if PATH="${FAKE_BIN}:${PATH}" provision_codex_plugins "${CODEX_DIR}" cenci matteobortolazzo/cenci cenci cenci-watch 2>/dev/null \
    && PATH="${FAKE_BIN}:${PATH}" update_codex_plugins "${CODEX_DIR}" cenci 0 cenci cenci-watch 2>/dev/null; then
    pass
else
    fail "Codex plugin helpers must remain non-fatal"
fi
unset CODEX_FAKE_EXIT

# ── Summary ──────────────────────────────────────────────────────
print_summary
[[ "${FAILURES}" -eq 0 ]]
