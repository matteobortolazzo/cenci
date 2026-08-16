#!/usr/bin/env bash
# merge-sandbox-config.test.sh — runtime behavior of configure's deterministic
# `sandbox`-object merge script (#632). Modelled on detect-project.test.sh (a
# `failures=` counter, small assert_* helpers, mktemp fixture files,
# self-contained, auto-discovered by the flow gate's `*.test.sh` glob).
#
# Contract pinned here: merge-sandbox-config.sh takes an existing config
# (path or "-" for stdin), plus --dockerfile <true|false>,
# --base-version <ver|null>, --dind <true|false> and --azure <true|false>,
# and prints the full merged config to stdout. Enabling/disabling any one
# sibling never touches the others' fields or unrelated keys; an emptied
# `sandbox` object is
# dropped entirely; both Claude's SKILL.md and Codex's codex.md must invoke
# this script (byte-equivalence between clients is guaranteed by
# construction — same script, same jq — not by prose parity).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MERGE="${SCRIPT_DIR}/merge-sandbox-config.sh"
SKILL_MD="${SCRIPT_DIR}/../SKILL.md"
CODEX_MD="${SCRIPT_DIR}/../codex.md"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: expected [$2], got [$1]"; }

# assert_json_eq compares two JSON documents structurally (key-order
# independent) via jq's canonical sorted-compact form, so each case's
# assertion is about the merge's actual shape, not incidental key ordering.
assert_json_eq() {
  local actual expected
  actual="$(jq -Sc . <<<"$1" 2>/dev/null)" || actual="<invalid JSON: $1>"
  expected="$(jq -Sc . <<<"$2" 2>/dev/null)" || expected="<invalid JSON: $2>"
  [[ "${actual}" == "${expected}" ]] || fail "$3: expected [${expected}], got [${actual}] (raw: $1)"
}

# write_config <content> → path to a temp file holding content.
write_config() {
  local f
  f="$(mktemp)"
  printf '%s' "$1" > "${f}"
  echo "${f}"
}

# run_merge <config-path> [flags...] — runs the script against a config file
# path.
run_merge() {
  local cfg="$1"
  shift
  OUT="$(bash "${MERGE}" "${cfg}" "$@" 2>&1)"
  CODE=$?
}

# --- Case 1 (Q9/Q9b transition 1/4): dockerfile ON, dind ON, from {} --------
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile true --base-version 24.04 --dind true --azure false
assert_eq "${CODE}" "0" "case1 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":"24.04","dind":true}}' "case1 both siblings on"
rm -f "${CFG}"

# --- Case 2 (Q9/Q9b transition 2/4): dockerfile ON, dind OFF, from {} -------
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile true --base-version 24.04 --dind false --azure false
assert_eq "${CODE}" "0" "case2 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":"24.04"}}' "case2 dockerfile alone"
rm -f "${CFG}"

# --- Case 3 (Q9/Q9b transition 3/4): dockerfile OFF, dind ON, from {} -------
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind true --azure false
assert_eq "${CODE}" "0" "case3 exit"
assert_json_eq "${OUT}" '{"sandbox":{"dind":true}}' "case3 dind alone"
rm -f "${CFG}"

# --- Case 4 (Q9/Q9b transition 4/4): dockerfile OFF, dind OFF, from {} ------
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind false --azure false
assert_eq "${CODE}" "0" "case4 exit"
assert_json_eq "${OUT}" '{}' "case4 both siblings off, sandbox omitted"
rm -f "${CFG}"

# --- Case 5: disabling dind removes only "dind", not enabled/baseVersion ---
CFG="$(write_config '{"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true}}')"
run_merge "${CFG}" --dockerfile true --base-version 22.04 --dind false --azure false
assert_eq "${CODE}" "0" "case5 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":"22.04"}}' "case5 dind-only-off preserves dockerfile siblings"
rm -f "${CFG}"

# --- Case 6: disabling dockerfile removes only enabled/baseVersion, not dind
CFG="$(write_config '{"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true}}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind true --azure false
assert_eq "${CODE}" "0" "case6 exit"
assert_json_eq "${OUT}" '{"sandbox":{"dind":true}}' "case6 dockerfile-only-off preserves dind sibling"
rm -f "${CFG}"

# --- Case 7: unknown top-level AND unknown sandbox sub-keys survive --------
CFG="$(write_config '{"configVersion":"0.22.0","mcpServers":["angular"],"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true,"futureKey":"keep-me"}}')"
run_merge "${CFG}" --dockerfile true --base-version 22.04 --dind true --azure false
assert_eq "${CODE}" "0" "case7 exit"
assert_json_eq "${OUT}" '{"configVersion":"0.22.0","mcpServers":["angular"],"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true,"futureKey":"keep-me"}}' "case7 unknown top-level and sandbox sub-keys preserved"
rm -f "${CFG}"

# --- Case 8: an emptied sandbox object is omitted, sibling top-level kept --
CFG="$(write_config '{"configVersion":"0.22.0","sandbox":{"enabled":true,"baseVersion":"22.04","dind":true}}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind false --azure false
assert_eq "${CODE}" "0" "case8 exit"
assert_json_eq "${OUT}" '{"configVersion":"0.22.0"}' "case8 emptied sandbox omitted, configVersion preserved"
printf '%s' "${OUT}" | jq -e 'has("sandbox") | not' >/dev/null 2>&1 || fail "case8 output must not contain a \"sandbox\" key at all (got: ${OUT})"
rm -f "${CFG}"

# --- Case 9: baseVersion:null passes through as JSON null, not omitted -----
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile true --base-version null --dind false --azure false
assert_eq "${CODE}" "0" "case9 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":null}}' "case9 baseVersion:null passthrough"
printf '%s' "${OUT}" | jq -e '.sandbox | has("baseVersion") and (.baseVersion == null)' >/dev/null 2>&1 \
  || fail "case9 sandbox.baseVersion must be present and JSON null (got: ${OUT})"
rm -f "${CFG}"

# --- Case 10: unreadable existing config fails closed, not silently {} -----
# Analogous to watch/internal/sandbox/launcher/dind_test.go's "unreadable
# file errors" case, adapted to bash: the script's `-f` guard already
# excludes non-regular-files (e.g. a directory at the config path) before
# ever reaching `cat`, so the only way to force `cat` itself to fail on an
# *existing regular file* is chmod 000 (this suite assumes it does not run
# as root, matching CI/dev sandboxes — root's DAC override would bypass the
# permission bit). The script must fail closed (non-zero exit) rather than
# have `cat`'s unchecked failure collapse to `{}` and silently drop every
# other top-level key on stdout.
UNREADABLE_CFG="$(write_config '{"configVersion":"0.22.0"}')"
chmod 000 "${UNREADABLE_CFG}"
if [[ "$(id -u)" -eq 0 ]]; then
  echo "SKIP: case10 unreadable-config test requires a non-root user"
else
  run_merge "${UNREADABLE_CFG}" --dockerfile true --base-version 24.04 --dind true --azure false
  [[ "${CODE}" -ne 0 ]] || fail "case10 unreadable config must fail closed (non-zero exit), got exit 0 with output [${OUT}]"
fi
chmod 644 "${UNREADABLE_CFG}"
rm -f "${UNREADABLE_CFG}"

# --- Case 12 (#1080): --azure true writes sandbox.azure alongside siblings -
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile true --base-version 24.04 --dind false --azure true
assert_eq "${CODE}" "0" "case12 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":"24.04","azure":true}}' "case12 azure on"
rm -f "${CFG}"

# --- Case 13 (#1080): azure is an independent sibling, on its own ----------
# Mirrors case 3 for dind: an azure-only answer must still produce a sandbox
# object, so a repo can opt into the Azure CLI without a per-repo Dockerfile
# answer or dind.
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind false --azure true
assert_eq "${CODE}" "0" "case13 exit"
assert_json_eq "${OUT}" '{"sandbox":{"azure":true}}' "case13 azure alone"
rm -f "${CFG}"

# --- Case 14 (#1080): --azure false removes only azure, never its siblings -
CFG="$(write_config '{"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true,"azure":true}}')"
run_merge "${CFG}" --dockerfile true --base-version 22.04 --dind true --azure false
assert_eq "${CODE}" "0" "case14 exit"
assert_json_eq "${OUT}" '{"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true}}' "case14 azure-only-off preserves dockerfile and dind siblings"
printf '%s' "${OUT}" | jq -e '.sandbox | has("azure") | not' >/dev/null 2>&1 \
  || fail "case14 sandbox.azure must be deleted, never written as false (got: ${OUT})"
rm -f "${CFG}"

# --- Case 15 (#1080): turning the siblings off preserves an azure opt-in ---
CFG="$(write_config '{"sandbox":{"enabled":true,"baseVersion":"22.04","dind":true,"azure":true}}')"
run_merge "${CFG}" --dockerfile false --base-version null --dind false --azure true
assert_eq "${CODE}" "0" "case15 exit"
assert_json_eq "${OUT}" '{"sandbox":{"azure":true}}' "case15 siblings off preserves azure"
rm -f "${CFG}"

# --- Case 16 (#1080): an omitted --azure fails closed, never defaults off --
# A silent false default would DELETE an existing sandbox.azure and strip
# Azure support from a repo that had opted in — the flag is required so a
# caller that has not been updated exits 2 visibly instead.
CFG="$(write_config '{"sandbox":{"azure":true}}')"
run_merge "${CFG}" --dockerfile true --base-version 24.04 --dind false
assert_eq "${CODE}" "2" "case16 omitted --azure must exit 2"
grep -q -- "--azure must be true or false" <<<"${OUT}" \
  || fail "case16 must name --azure in its error (got: ${OUT})"
rm -f "${CFG}"

# --- Case 17 (#1080): a non-boolean --azure fails closed -------------------
CFG="$(write_config '{}')"
run_merge "${CFG}" --dockerfile true --base-version 24.04 --dind false --azure yes
assert_eq "${CODE}" "2" "case17 non-boolean --azure must exit 2"
rm -f "${CFG}"

# --- Case 11: both client adapters invoke merge-sandbox-config.sh ----------
# Prose presence alone is not evidence generated JSON is correct — this only
# asserts BOTH adapters call the shared script (byte-equivalence is
# guaranteed by construction), not that either adapter's prose is otherwise
# correct.
if [[ ! -f "${SKILL_MD}" ]]; then
  fail "case11 SKILL.md not found at ${SKILL_MD}"
elif ! grep -q "merge-sandbox-config.sh" "${SKILL_MD}"; then
  fail "case11 SKILL.md does not invoke merge-sandbox-config.sh"
fi
if [[ ! -f "${CODEX_MD}" ]]; then
  fail "case11 codex.md not found at ${CODEX_MD}"
elif ! grep -q "merge-sandbox-config.sh" "${CODEX_MD}"; then
  fail "case11 codex.md does not invoke merge-sandbox-config.sh"
fi

echo "merge-sandbox-config.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
