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

# =====================================================================
# #1245 (AC6): the nudge names the unconditional config keys a re-run would
# ask about. Cases 1-10 above use a fake plugin root with NO resolver, so
# they pin the fallback (today's three-field line and message). The cases
# below add the real resolve-delta.sh + question-keys.json to the fake root.
#
# Plugin/config versions are chosen so the digits 1 and 2 never occur in the
# advisory text except as the missing-key count (3.4.0 behind 3.5.0).
# =====================================================================
REAL_CONFIGURE_DIR="${SCRIPT_DIR}/../../skills/configure"
CFG_OLD="3.4.0"
PLUG_NEW="3.5.0"

assert_not_contains() {
  [[ -n "$2" ]] || { fail "assert_not_contains: needle must not be empty ($3)"; return; }
  [[ "$1" != *"$2"* ]] || fail "$3: expected output to NOT contain [$2], got [$1]"
}

# make_fixture_with_resolver <plugin-version> — make_fixture plus the real
# resolver and question table copied into ${PLUGIN}/skills/configure/.
make_fixture_with_resolver() {
  make_fixture "$1"
  mkdir -p "${PLUGIN}/skills/configure/scripts"
  cp "${REAL_CONFIGURE_DIR}/scripts/resolve-delta.sh" "${PLUGIN}/skills/configure/scripts/resolve-delta.sh" 2>/dev/null \
    || fail "make_fixture_with_resolver: cannot copy the real resolve-delta.sh"
  cp "${REAL_CONFIGURE_DIR}/question-keys.json" "${PLUGIN}/skills/configure/question-keys.json" 2>/dev/null \
    || fail "make_fixture_with_resolver: cannot copy the real question-keys.json"
}

# Every unconditional configure key, answered. Conditional questions depend on
# detection the hook cannot run, so their keys are deliberately left out.
ANSWERED='{
  "stack": {"backend": "go"},
  "isMonorepo": false,
  "branchPattern": "feature/<id>",
  "mcpServers": {},
  "autoCompactDisabled": false,
  "pinSubagents200K": false,
  "cicd": {"enabled": false},
  "sandbox": {"enabled": false, "dind": false, "azure": false},
  "planning": {"autonomy": "interactive"},
  "automerge": {"enabled": false},
  "refinement": {"autonomy": "interactive", "confirmBeforeApply": false, "automerge": "off"}
}'

# write_config <jq-filter> [version] — writes ROOT/.cenci/config.json as
# ANSWERED transformed by the filter; "-" omits configVersion (unstamped).
write_config() {
  local filter="$1" version="${2:-$CFG_OLD}"
  if [[ "$version" == "-" ]]; then
    printf '%s' "${ANSWERED}" | jq "${filter}" > "${ROOT}/.cenci/config.json"
  else
    printf '%s' "${ANSWERED}" | jq --arg v "${version}" "${filter} | .configVersion = \$v" > "${ROOT}/.cenci/config.json"
  fi
}

# The pre-#1245 stale/unstamped messages, byte for byte — the fallback contract.
STALE_MSG_TODAY="This repo's .cenci/config.json was written by /cenci:configure v${CFG_OLD}, but the installed cenci flow plugin is v${PLUG_NEW}. Re-running /cenci:configure would pick up configure features added since — existing answers are offered back as defaults. Surface this to the user at a natural moment; do not run /cenci:configure unprompted."
UNSTAMPED_MSG_TODAY="This repo's .cenci/config.json has no configVersion stamp — it was written by a /cenci:configure older than the installed cenci flow plugin (v${PLUG_NEW}). Re-running /cenci:configure would pick up configure features added since and stamp configVersion — existing answers are offered back as defaults. Surface this to the user at a natural moment; do not run /cenci:configure unprompted."

hook_context() { printf '%s' "${OUT}" | jq -r '.hookSpecificOutput.additionalContext' 2>/dev/null; }

# --- Case 11: stale, two unconditional keys missing -> named, counted ------
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning, .automerge)'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} planning,automerge" "case11 plain line ends in the comma-joined missing keys"
assert_eq "${CODE}" "0" "case11 plain exit"
run_hook
assert_eq "${CODE}" "0" "case11 hook exit"
echo "${OUT}" | jq -e '.hookSpecificOutput.hookEventName == "SessionStart"' >/dev/null 2>&1 \
  || fail "case11 hook output must be SessionStart hookSpecificOutput JSON (got: ${OUT})"
CTX="$(hook_context)"
assert_contains "${CTX}" "planning" "case11 advisory names planning"
assert_contains "${CTX}" "automerge" "case11 advisory names automerge"
assert_contains "${CTX}" "2" "case11 advisory counts the two missing keys"
assert_contains "${CTX}" "v${CFG_OLD}" "case11 advisory still names the config version"
assert_contains "${CTX}" "/cenci:configure" "case11 advisory still points at configure"
cleanup_fixture

# --- Case 12: stale, exactly one key missing -> only that key named --------
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning)'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} planning" "case12 plain line names the single missing key"
run_hook
CTX="$(hook_context)"
assert_contains "${CTX}" "planning" "case12 advisory names planning"
assert_contains "${CTX}" "1" "case12 advisory counts one missing key"
assert_not_contains "${CTX}" "2" "case12 advisory must not claim two keys"
assert_not_contains "${CTX}" "automerge" "case12 advisory must not name a present key"
cleanup_fixture

# --- Case 12b (#1274): a missing refinement key is named like any other ----
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.refinement) | .configureOffered = ["refinement.confirmBeforeApply", "refinement.automerge"]'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} refinement" "case12b plain line names the missing refinement key"
run_hook
CTX="$(hook_context)"
assert_contains "${CTX}" "refinement" "case12b advisory names refinement"
assert_not_contains "${CTX}" "planning" "case12b advisory must not name a present key"
cleanup_fixture

# --- Independent refinement leaves remain visible despite old parent ledger ---
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.refinement.confirmBeforeApply, .refinement.automerge) | .configureOffered = ["refinement"]'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} refinement.confirmBeforeApply,refinement.automerge" "new leaves advisory plain line names both independent questions"
run_hook
CTX="$(hook_context)"
assert_contains "${CTX}" "2 new questions" "new leaves advisory counts both questions"
assert_contains "${CTX}" "refinement.confirmBeforeApply, refinement.automerge" "new leaves advisory names both leaves in order"
cleanup_fixture

# --- Case 13: unstamped, keys missing -> named alongside the stamp nudge ----
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning, .automerge)' "-"
run_hook --plain
assert_eq "${OUT}" "unstamped - ${PLUG_NEW} planning,automerge" "case13 unstamped plain line carries the missing keys"
run_hook
assert_eq "${CODE}" "0" "case13 hook exit"
CTX="$(hook_context)"
assert_contains "${CTX}" "configVersion" "case13 advisory still names the missing stamp"
assert_contains "${CTX}" "planning" "case13 advisory names planning"
assert_contains "${CTX}" "automerge" "case13 advisory names automerge"
assert_contains "${CTX}" "2" "case13 advisory counts the two missing keys"
cleanup_fixture

# --- Case 14: unstamped, everything answered -> '-' and no key sentence ----
make_fixture_with_resolver "${PLUG_NEW}"
write_config '.' "-"
run_hook --plain
assert_eq "${OUT}" "unstamped - ${PLUG_NEW} -" "case14 nothing missing -> fourth field is '-'"
run_hook
assert_eq "${OUT}" "$(jq -n --arg ctx "${UNSTAMPED_MSG_TODAY}" '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}')" \
  "case14 nothing missing -> message is exactly today's"
cleanup_fixture

# --- Case 15: ledgered (declined) keys are not reported --------------------
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning, .automerge) | .configureOffered = ["planning","automerge"]' "-"
run_hook --plain
assert_eq "${OUT}" "unstamped - ${PLUG_NEW} -" "case15 ledgered keys are not reported missing"
cleanup_fixture

# --- Case 16: stale, everything answered -> '-' and today's message --------
make_fixture_with_resolver "${PLUG_NEW}"
write_config '.'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} -" "case16 nothing missing -> fourth field is '-'"
run_hook
CTX="$(hook_context)"
assert_eq "${CTX}" "${STALE_MSG_TODAY}" "case16 nothing missing -> message is exactly today's"
cleanup_fixture

# --- Case 17: only conditional (detection-dependent) rows open -> '-' ------
# ANSWERED omits lspServers, lazyboards, playwrightCli and the keyless rows;
# the resolver asks about them but the hook cannot run detection, so it must
# not count them.
make_fixture_with_resolver "${PLUG_NEW}"
write_config '.'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} -" "case17 conditional questions are not counted"
run_hook
assert_eq "${CODE}" "0" "case17 hook exit"
CTX="$(hook_context)"
assert_eq "${CTX}" "${STALE_MSG_TODAY}" "case17 advisory is exactly today's message (positive: the nudge fired, with no key sentence)"
assert_contains "${CTX}" "/cenci:configure" "case17 advisory still points at configure"
assert_not_contains "${CTX}" "lspServers" "case17 advisory must not name a conditional question"
assert_not_contains "${CTX}" "lazyboards" "case17 advisory must not name a conditional question"
cleanup_fixture

# --- Case 18: automerge only under projects[] counts as present ------------
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning, .automerge) | .projects = [{"slug":"a","automerge":{"enabled":true}}]'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW} planning" "case18 automerge in projects[] is not named"
run_hook
assert_eq "${CODE}" "0" "case18 hook exit"
CTX="$(hook_context)"
assert_contains "${CTX}" "/cenci:configure" "case18 advisory fired and points at configure"
assert_contains "${CTX}" "planning" "case18 advisory names the genuinely missing key"
assert_not_contains "${CTX}" "automerge" "case18 advisory must not name a projects[]-satisfied key"
cleanup_fixture

# --- Case 19: current config + resolver present -> unchanged, silent -------
make_fixture_with_resolver "${PLUG_NEW}"
write_config 'del(.planning, .automerge)' "${PLUG_NEW}"
run_hook --plain
assert_eq "${OUT}" "current ${PLUG_NEW} ${PLUG_NEW}" "case19 current line stays three fields"
run_hook
assert_eq "${CODE}" "0" "case19 hook exit"
assert_eq "${OUT}" "" "case19 missing keys alone never trigger the nudge"
cleanup_fixture

# --- Case 20: fallback — resolver absent from the plugin root --------------
make_fixture "${PLUG_NEW}"
write_config 'del(.planning, .automerge)'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW}" "case20 no resolver -> three-field line"
run_hook
assert_eq "${CODE}" "0" "case20 hook exit"
assert_eq "$(hook_context)" "${STALE_MSG_TODAY}" "case20 no resolver -> today's message"
cleanup_fixture

# --- Case 21: fallback — invalid table -------------------------------------
make_fixture_with_resolver "${PLUG_NEW}"
printf '{ not json' > "${PLUGIN}/skills/configure/question-keys.json"
write_config 'del(.planning, .automerge)'
run_hook --plain
assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW}" "case21 invalid table -> three-field line"
assert_eq "${CODE}" "0" "case21 plain exit"
run_hook
assert_eq "${CODE}" "0" "case21 hook exit"
assert_eq "$(hook_context)" "${STALE_MSG_TODAY}" "case21 invalid table -> today's message"
cleanup_fixture

# --- Case 22: fallback — resolver printing junk or failing midway ----------
# install_fake_resolver <body> — replaces the real resolver with a stub script.
install_fake_resolver() {
  printf '%s\n' "$1" > "${PLUGIN}/skills/configure/scripts/resolve-delta.sh"
}
FAKE_RESOLVERS=(
  "echo 'this is not json'"
  "echo '{\"ask\":\"planning\",\"carried\":[],\"missingKeys\":[],\"conditional\":[]}'"
  "echo '{\"ask\":[\"bad id; rm -rf\"],\"carried\":[],\"missingKeys\":[],\"conditional\":[]}'"
  "echo '{\"ask\":[\"planning\"],\"carried\":[],\"missingKeys\":[\"planning\"]}'"
  "echo '{\"ask\":[\"planning\\n\"],\"carried\":[],\"missingKeys\":[],\"conditional\":[]}'"
  "echo '{\"ask\":[\"planning\"'; exit 2"
  "exit 2"
  ": # prints nothing"
)
for body in "${FAKE_RESOLVERS[@]}"; do
  make_fixture_with_resolver "${PLUG_NEW}"
  install_fake_resolver "${body}"
  write_config 'del(.planning, .automerge)'
  run_hook --plain
  assert_eq "${OUT}" "stale ${CFG_OLD} ${PLUG_NEW}" "case22 [${body}] -> three-field line"
  assert_eq "${CODE}" "0" "case22 [${body}] plain exit"
  run_hook
  assert_eq "${CODE}" "0" "case22 [${body}] hook exit"
  assert_eq "$(hook_context)" "${STALE_MSG_TODAY}" "case22 [${body}] -> today's message"
  cleanup_fixture
done

echo "check-config-staleness.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
