#!/usr/bin/env bash
# check-config-staleness.test.sh — runtime behavior of the SessionStart
# config-staleness advisory. Modelled on resolve-babysit-interval.test.sh
# (a `failures=` counter, small assert_* helpers, self-contained,
# auto-discovered by the flow gate's `*.test.sh` glob).
#
# Contract pinned here: advisory-only (always exit 0), silent in hook mode
# unless the config is stale (major.minor behind the installed plugin) or
# unstamped; `--plain` prints one machine-readable line for check.sh's
# config-version check. Patch-only drift, an ahead-of-plugin config, a
# missing config, and every unreadable input stay silent.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="${SCRIPT_DIR}/check-config-staleness.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "$3: expected output to contain [$2], got [$1]"; }

# make_fixture <plugin-version> — creates a repo root + fake plugin root;
# sets ROOT and PLUGIN. Caller writes ROOT/.cenci/config.json as needed.
make_fixture() {
  ROOT="$(mktemp -d)"
  PLUGIN="$(mktemp -d)"
  mkdir -p "${ROOT}/.cenci" "${PLUGIN}/.claude-plugin"
  printf '{"name":"cenci","version":"%s"}' "$1" > "${PLUGIN}/.claude-plugin/plugin.json"
}

cleanup_fixture() { rm -rf "${ROOT}" "${PLUGIN}"; }

run_hook() {  # run_hook [--plain] — from ROOT with PLUGIN as plugin root
  OUT="$(cd "${ROOT}" && CLAUDE_PLUGIN_ROOT="${PLUGIN}" bash "${HOOK}" "$@" 2>&1)"
  CODE=$?
}

# --- Case 1: no config file → silent, exit 0; plain says "absent" ---------
make_fixture "0.21.0"
rmdir "${ROOT}/.cenci"
run_hook
assert_eq "${CODE}" "0" "case1 hook exit"
assert_eq "${OUT}" "" "case1 hook silent"
run_hook --plain
assert_eq "${OUT}" "absent" "case1 plain state"
assert_eq "${CODE}" "0" "case1 plain exit"
cleanup_fixture

# --- Case 2: matching version → silent; plain "current" -------------------
make_fixture "0.21.0"
printf '{"configVersion":"0.21.0"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${CODE}" "0" "case2 hook exit"
assert_eq "${OUT}" "" "case2 hook silent"
run_hook --plain
assert_eq "${OUT}" "current 0.21.0 0.21.0" "case2 plain state"
cleanup_fixture

# --- Case 3: patch-only drift → still current (silent) --------------------
make_fixture "0.21.7"
printf '{"configVersion":"0.21.0"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${OUT}" "" "case3 hook silent on patch-only drift"
run_hook --plain
assert_eq "${OUT}" "current 0.21.0 0.21.7" "case3 plain state"
cleanup_fixture

# --- Case 4: minor behind → stale advisory with both versions -------------
make_fixture "0.21.0"
printf '{"configVersion":"0.20.3"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${CODE}" "0" "case4 hook exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case4 hook output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext')"
assert_contains "${CTX}" "v0.20.3" "case4 advisory names the config version"
assert_contains "${CTX}" "v0.21.0" "case4 advisory names the plugin version"
assert_contains "${CTX}" "/cenci:configure" "case4 advisory points at configure"
run_hook --plain
assert_eq "${OUT}" "stale 0.20.3 0.21.0" "case4 plain state"
cleanup_fixture

# --- Case 5: major behind (incl. sort -V, 0.9.x vs 1.0.x) → stale ---------
make_fixture "1.0.0"
printf '{"configVersion":"0.9.9"}' > "${ROOT}/.cenci/config.json"
run_hook --plain
assert_eq "${OUT}" "stale 0.9.9 1.0.0" "case5 plain state (version sort, not lexical)"
cleanup_fixture

# --- Case 6: config ahead of plugin (downgrade) → current, silent ---------
make_fixture "0.21.0"
printf '{"configVersion":"0.22.0"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${OUT}" "" "case6 hook silent when config is ahead"
run_hook --plain
assert_eq "${OUT}" "current 0.22.0 0.21.0" "case6 plain state"
cleanup_fixture

# --- Case 7: config without configVersion → unstamped advisory ------------
make_fixture "0.21.0"
printf '{"branchPattern":"feature/<id>"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${CODE}" "0" "case7 hook exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case7 hook output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(echo "${OUT}" | jq -r '.hookSpecificOutput.additionalContext')"
assert_contains "${CTX}" "configVersion" "case7 advisory names the missing stamp"
assert_contains "${CTX}" "/cenci:configure" "case7 advisory points at configure"
run_hook --plain
assert_eq "${OUT}" "unstamped - 0.21.0" "case7 plain state"
cleanup_fixture

# --- Case 8: malformed config JSON → silent (maintain owns invalid-json) --
make_fixture "0.21.0"
printf '{ not json' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${CODE}" "0" "case8 hook exit"
assert_eq "${OUT}" "" "case8 hook silent on malformed config"
run_hook --plain
assert_eq "${OUT}" "unknown invalid-config" "case8 plain state"
cleanup_fixture

# --- Case 9: missing plugin manifest → silent --------------------------------
make_fixture "0.21.0"
rm "${PLUGIN}/.claude-plugin/plugin.json"
printf '{"configVersion":"0.1.0"}' > "${ROOT}/.cenci/config.json"
run_hook
assert_eq "${CODE}" "0" "case9 hook exit"
assert_eq "${OUT}" "" "case9 hook silent without a plugin manifest"
run_hook --plain
assert_eq "${OUT}" "unknown no-plugin-manifest" "case9 plain state"
cleanup_fixture

# --- Case 10: non-string configVersion → treated as unstamped -------------
make_fixture "0.21.0"
printf '{"configVersion":42}' > "${ROOT}/.cenci/config.json"
run_hook --plain
assert_eq "${OUT}" "unstamped - 0.21.0" "case10 non-string stamp treated as unstamped"
cleanup_fixture

echo "check-config-staleness.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
