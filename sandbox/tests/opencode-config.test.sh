#!/bin/bash
# Tests for the OpenCode opencode.json permission/plugin seeding in
# sandbox/lib/opencode-config.sh (#490).
#
# Runs on the host with the system `jq` — no Docker required. Sources the same
# seed_opencode_config() the entrypoint uses, exercising it against files in a
# temp directory. Mirrors sandbox/tests/codex-config.test.sh's structure, but
# opencode.json is JSON (not TOML), so assertions go through jq rather than
# grep.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/opencode-config.sh
source "${SCRIPT_DIR}/../lib/opencode-config.sh"

FAILURES=0
PASSES=0

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

TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_TEST}"' EXIT

# The plugin registration cenci-watch stages into opencode.json — a local
# file:// ref into the sandbox's cenci-src clone (see #390/#490's Q&A: the
# plugin isn't published, so it's referenced by path, not by npm spec).
PLUGIN_SPEC="file:///home/dev/.cenci-src/watch/plugin/opencode"

echo "opencode-config.test.sh"

# ── Case 1: missing opencode.json → created with permission/autoupdate/plugin ──
CONFIG="${TMPDIR_TEST}/fresh/opencode.json"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: fresh opencode.json"
assert_file_jq "creates valid JSON"              "${CONFIG}" '.'
assert_file_jq "seeds permission allow-all"      "${CONFIG}" '.permission["*"] == "allow"'
assert_file_jq "disables native update checks"   "${CONFIG}" '.autoupdate == false'
assert_file_jq "registers the plugin spec"       "${CONFIG}" ".plugin | index(\"${PLUGIN_SPEC}\")"

# ── Case 2: existing config without permission/plugin keys → merged in ────
CONFIG="${TMPDIR_TEST}/existing.json"
printf '{"model":"anthropic/claude-sonnet-4-5","mcp":{"context7":{"command":"npx"}}}\n' >"${CONFIG}"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: existing config without permission/plugin keys"
assert_file_jq "preserves user model key"     "${CONFIG}" '.model == "anthropic/claude-sonnet-4-5"'
assert_file_jq "preserves other user tables"  "${CONFIG}" '.mcp.context7.command == "npx"'
assert_file_jq "adds permission allow-all"    "${CONFIG}" '.permission["*"] == "allow"'
assert_file_jq "adds autoupdate false"        "${CONFIG}" '.autoupdate == false'
assert_file_jq "registers the plugin spec"    "${CONFIG}" ".plugin | index(\"${PLUGIN_SPEC}\")"

# ── Case 3: existing permission block → left untouched ────────────────────
# A present permission block means the user already made a boundary choice
# (possibly stricter than the container-boundary default) — never overwrite
# it, only add what's genuinely absent.
CONFIG="${TMPDIR_TEST}/has-permission.json"
printf '{"permission":{"*":"ask","bash":"allow"}}\n' >"${CONFIG}"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: existing permission block untouched"
assert_file_jq "keeps the user's stricter default" "${CONFIG}" '.permission["*"] == "ask"'
assert_file_jq "keeps the user's bash override"     "${CONFIG}" '.permission.bash == "allow"'
assert_file_jq "still adds autoupdate false"        "${CONFIG}" '.autoupdate == false'
assert_file_jq "still registers the plugin spec"    "${CONFIG}" ".plugin | index(\"${PLUGIN_SPEC}\")"

# ── Case 4: existing autoupdate value → left untouched ─────────────────────
# Unlike Codex's forced-off update check, autoupdate here is seeded only when
# absent — a user who explicitly opted back in keeps their choice.
CONFIG="${TMPDIR_TEST}/has-autoupdate.json"
printf '{"autoupdate":true}\n' >"${CONFIG}"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: existing autoupdate value untouched"
assert_file_jq "keeps the user's autoupdate choice" "${CONFIG}" '.autoupdate == true'

# ── Case 5: dedup plugin entry on repeat calls ─────────────────────────────
CONFIG="${TMPDIR_TEST}/dedup/opencode.json"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: plugin entry is deduped on repeat calls"
assert_file_jq "plugin array has exactly one matching entry" \
    "${CONFIG}" ".plugin | map(select(. == \"${PLUGIN_SPEC}\")) | length == 1"

# ── Case 6: idempotency ─────────────────────────────────────────────────────
CONFIG="${TMPDIR_TEST}/idempotent/opencode.json"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
ONCE="$(cat "${CONFIG}")"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
TWICE="$(cat "${CONFIG}")"
echo "case: idempotency"
if [[ "${ONCE}" == "${TWICE}" ]]; then
    pass
else
    fail "running seed_opencode_config twice differs from running it once"
fi

# ── Case 7: invalid JSON → left untouched ──────────────────────────────────
# Unlike installed_plugins.json (regenerable), a user's opencode.json holds
# hand-authored settings; a corrupt file is left alone rather than clobbered.
CONFIG="${TMPDIR_TEST}/invalid.json"
printf 'not json {' >"${CONFIG}"
BEFORE="$(cat "${CONFIG}")"
seed_opencode_config "${CONFIG}" "${PLUGIN_SPEC}"
echo "case: invalid JSON is left untouched"
if [[ "$(cat "${CONFIG}")" == "${BEFORE}" ]]; then
    pass
else
    fail "invalid opencode.json was modified instead of left untouched"
fi

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
