#!/bin/bash
# Tests for the Codex config.toml status-line seeding in
# dev-sandbox/lib/codex-config.sh.
#
# Runs on the host — no Docker required. Sources the same seed_codex_config()
# the entrypoint uses, exercising it against files in a temp directory.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/codex-config.sh
source "${SCRIPT_DIR}/../lib/codex-config.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_grep <label> <file> <pattern>
assert_grep() {
    local label="$1" file="$2" pattern="$3"
    if grep -Eq "${pattern}" "${file}" 2>/dev/null; then
        pass
    else
        fail "${label} (pattern: ${pattern})"
    fi
}

# assert_not_grep <label> <file> <pattern>
assert_not_grep() {
    local label="$1" file="$2" pattern="$3"
    if grep -Eq "${pattern}" "${file}" 2>/dev/null; then
        fail "${label} (pattern matched: ${pattern})"
    else
        pass
    fi
}

TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_TEST}"' EXIT

echo "codex-config.test.sh"

# ── Case 1: missing config.toml → created with [tui] status_line ──
CONFIG="${TMPDIR_TEST}/fresh/config.toml"
seed_codex_config "${CONFIG}"
echo "case: fresh config.toml"
assert_grep "creates [tui] table"      "${CONFIG}" '^\[tui\]$'
assert_grep "sets status_line"         "${CONFIG}" '^status_line = \['
assert_grep "includes context usage"   "${CONFIG}" 'context-usage'

# ── Case 2: existing config without [tui] → block appended ───────
CONFIG="${TMPDIR_TEST}/existing.toml"
printf 'model = "gpt-5.1-codex"\n\n[mcp_servers.context7]\ncommand = "npx"\n' > "${CONFIG}"
seed_codex_config "${CONFIG}"
echo "case: existing config without [tui]"
assert_grep "preserves user keys"      "${CONFIG}" '^model = "gpt-5.1-codex"$'
assert_grep "preserves other tables"   "${CONFIG}" '^\[mcp_servers.context7\]$'
assert_grep "appends [tui] table"      "${CONFIG}" '^\[tui\]$'
assert_grep "appends status_line"      "${CONFIG}" '^status_line = \['

# ── Case 3: existing [tui] table → left untouched ─────────────────
# Appending a second [tui] table would be invalid TOML, and a present table
# means the user already configured the TUI — never touch it.
CONFIG="${TMPDIR_TEST}/has-tui.toml"
printf '[tui]\nnotifications = true\n' > "${CONFIG}"
seed_codex_config "${CONFIG}"
echo "case: existing [tui] table untouched"
assert_grep "preserves user tui keys"  "${CONFIG}" '^notifications = true$'
assert_not_grep "does not add status_line" "${CONFIG}" 'status_line'
if [[ "$(grep -c '^\[tui\]$' "${CONFIG}")" -eq 1 ]]; then
    pass
else
    fail "duplicate [tui] tables"
fi

# ── Case 4: existing status_line → left untouched ─────────────────
CONFIG="${TMPDIR_TEST}/has-statusline.toml"
printf '[tui]\nstatus_line = ["model"]\n' > "${CONFIG}"
seed_codex_config "${CONFIG}"
echo "case: existing status_line untouched"
assert_grep "keeps user status_line"   "${CONFIG}" '^status_line = \["model"\]$'

# ── Case 5: idempotency ───────────────────────────────────────────
CONFIG="${TMPDIR_TEST}/idempotent/config.toml"
seed_codex_config "${CONFIG}"
ONCE="$(cat "${CONFIG}")"
seed_codex_config "${CONFIG}"
TWICE="$(cat "${CONFIG}")"
echo "case: idempotency"
if [[ "${ONCE}" == "${TWICE}" ]]; then
    pass
else
    fail "running seed_codex_config twice differs from running it once"
fi

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
