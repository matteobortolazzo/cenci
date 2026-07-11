#!/bin/bash
# Tests for the installed_plugins.json healing logic in
# dev-sandbox/lib/migrate-settings.sh.
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
# shellcheck source=../lib/migrate-settings.sh
source "${SCRIPT_DIR}/../lib/migrate-settings.sh"

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
GOOD_PATH="$(seed_cache agent-stack/agentflow/3.0.0)"
cat > "${META}" <<EOF
{"version":2,"plugins":{"agentflow@agent-stack":[{"scope":"user","installPath":"${GOOD_PATH}","version":"3.0.0"}]}}
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
GOOD_PATH="$(seed_cache agent-stack/agentwatch/2.2.1)"
cat > "${META}" <<EOF
{"version":2,"plugins":{
  "agentwatch@agent-stack":[{"scope":"user","installPath":"${GOOD_PATH}","version":"2.2.1"}],
  "agentflow@agent-stack":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0","version":"3.0.0"}]
}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: one broken plugin among two"
assert_file_jq "drops the plugin with a missing installPath" "${META}" '.plugins | has("agentflow@agent-stack") | not'
assert_file_jq "keeps the healthy plugin"                    "${META}" '.plugins["agentwatch@agent-stack"][0].version == "2.2.1"'
assert_file_jq "preserves the version field"                 "${META}" '.version == 2'

# ── Case 3: one broken record inside a plugin's record array ─────
new_case one-broken-record
GOOD_PATH="$(seed_cache agent-stack/agentflow/3.0.0)"
cat > "${META}" <<EOF
{"version":2,"plugins":{"agentflow@agent-stack":[
  {"scope":"user","installPath":"${GOOD_PATH}","version":"3.0.0"},
  {"scope":"user","installPath":"${PLUGINS_DIR}/cache/agent-stack/agentflow/2.9.0","version":"2.9.0"}
]}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: one broken record inside a plugin"
assert_file_jq "drops only the broken record" "${META}" '.plugins["agentflow@agent-stack"] | length == 1'
assert_file_jq "keeps the healthy record"     "${META}" '.plugins["agentflow@agent-stack"][0].version == "3.0.0"'

# ── Case 4: all installPaths missing → empty plugins object ──────
new_case all-broken
cat > "${META}" <<EOF
{"version":2,"plugins":{"agentflow@agent-stack":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0","version":"3.0.0"}]}}
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
GOOD_PATH="$(seed_cache agent-stack/agentwatch/2.2.1)"
cat > "${META}" <<EOF
{"version":2,"plugins":{
  "agentwatch@agent-stack":[{"scope":"user","installPath":"${GOOD_PATH}","version":"2.2.1"}],
  "agentflow@agent-stack":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0","version":"3.0.0"}]
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
    export CLAUDE_FAKE_MARKETPLACE="agent-stack"
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
if PATH="${PATH_NO_CLAUDE}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch; then
    pass
else
    fail "provision_plugins should return 0 when claude is not on PATH"
fi

# ── Case 9: marketplace present + both plugins already installed → zero calls ─
new_provision_case already-provisioned
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/agent-stack"
AF_PATH="${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0"
AW_PATH="${PLUGINS_DIR}/cache/agent-stack/agentwatch/2.2.1"
mkdir -p "${AF_PATH}" "${AW_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{
  "agentflow@agent-stack":[{"scope":"user","installPath":"${AF_PATH}","version":"3.0.0"}],
  "agentwatch@agent-stack":[{"scope":"user","installPath":"${AW_PATH}","version":"2.2.1"}]
}}
EOF
echo "case: already provisioned"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch
if [[ "$(log_count)" -eq 0 ]]; then
    pass
else
    fail "expected zero claude invocations, got $(log_count): $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 10: marketplace dir missing → marketplace add invoked ───
new_provision_case missing-marketplace
make_fake_claude
AF_PATH="${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0"
mkdir -p "${AF_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"agentflow@agent-stack":[{"scope":"user","installPath":"${AF_PATH}","version":"3.0.0"}]}}
EOF
echo "case: marketplace dir missing"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow
if grep -q "^plugin marketplace add matteobortolazzo/agent-stack$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected 'plugin marketplace add matteobortolazzo/agent-stack' in log: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 11: one plugin missing from metadata → exactly one install call ─
new_provision_case one-missing-plugin
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/agent-stack"
AW_PATH="${PLUGINS_DIR}/cache/agent-stack/agentwatch/2.2.1"
mkdir -p "${AW_PATH}"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"agentwatch@agent-stack":[{"scope":"user","installPath":"${AW_PATH}","version":"2.2.1"}]}}
EOF
echo "case: one plugin missing from metadata"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch
INSTALL_CALLS="$(grep -c "^plugin install " "${CLAUDE_FAKE_LOG}" || true)"
if [[ "${INSTALL_CALLS}" -eq 1 ]] && grep -q "^plugin install agentflow@agent-stack$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected exactly one 'plugin install agentflow@agent-stack', got: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 12: heal -> provision composition ────────────────────────
new_provision_case heal-then-provision
make_fake_claude
mkdir -p "${PLUGINS_DIR}/marketplaces/agent-stack"
cat > "${PLUGINS_DIR}/installed_plugins.json" <<EOF
{"version":2,"plugins":{"agentflow@agent-stack":[{"scope":"user","installPath":"${PLUGINS_DIR}/cache/agent-stack/agentflow/3.0.0","version":"3.0.0"}]}}
EOF
heal_plugin_installs "${PLUGINS_DIR}"
echo "case: heal drops broken record, provision reinstalls it"
assert_file_jq "heal dropped the broken record first" "${PLUGINS_DIR}/installed_plugins.json" '.plugins == {}'
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow
assert_file_jq "provision reinstalled agentflow" "${PLUGINS_DIR}/installed_plugins.json" '.plugins["agentflow@agent-stack"][0].installPath | length > 0'
if grep -q "^plugin install agentflow@agent-stack$" "${CLAUDE_FAKE_LOG}"; then
    pass
else
    fail "expected provision to reinstall the plugin heal dropped: $(cat "${CLAUDE_FAKE_LOG}")"
fi

# ── Case 13: claude exits 1 → provision still returns 0 (never blocks boot) ─
new_provision_case claude-fails
make_fake_claude
export CLAUDE_FAKE_EXIT=1
echo "case: claude failures never block boot"
if PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch 2>/dev/null; then
    pass
else
    fail "provision_plugins must return 0 even when claude fails"
fi
unset CLAUDE_FAKE_EXIT

# ── Case 14: idempotency — second run makes zero claude calls ────
new_provision_case provision-idempotent
make_fake_claude
echo "case: idempotency"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch >/dev/null
FIRST_RUN_CALLS="$(log_count)"
: > "${CLAUDE_FAKE_LOG}"
PATH="${FAKE_BIN}:${PATH}" provision_plugins "${PLUGINS_DIR}" agent-stack matteobortolazzo/agent-stack agentflow agentwatch >/dev/null
SECOND_RUN_CALLS="$(log_count)"
if [[ "${FIRST_RUN_CALLS}" -gt 0 && "${SECOND_RUN_CALLS}" -eq 0 ]]; then
    pass
else
    fail "expected calls on first run and zero on second, got ${FIRST_RUN_CALLS} then ${SECOND_RUN_CALLS}"
fi

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
