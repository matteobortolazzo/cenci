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

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
