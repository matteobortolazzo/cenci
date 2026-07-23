#!/bin/bash
# Tests for the seed-once credential logic in sandbox/lib/seed-auth.sh.
#
# Runs on the host — no Docker required. Sources the same seed_credential()
# the entrypoint uses.
#
# Background (#259): Claude and Codex OAuth use refresh-token rotation, so the
# host copy and the volume copy of a credential fork into independent token
# chains after the first refresh. Overwriting a live volume chain with the
# host copy logs the sandbox out — seeding must happen only when the volume
# has no credential yet, unless a reseed is explicitly forced.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/seed-auth.sh
source "${SCRIPT_DIR}/../lib/seed-auth.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# assert_eq <label> <expected> <actual>
assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [[ "${expected}" == "${actual}" ]]; then
        pass
    else
        fail "${label} (expected: ${expected@Q}, got: ${actual@Q})"
    fi
}

TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_TEST}"' EXIT

echo "seed-auth.test.sh"

# ── Case 1: first provision — no credential in the volume yet ──────
echo "case: seeds when destination is missing"
STAGED="${TMPDIR_TEST}/case1/staged.json"
DEST="${TMPDIR_TEST}/case1/home/.claude/.credentials.json"
mkdir -p "$(dirname "${STAGED}")"
echo '{"chain":"host"}' > "${STAGED}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies staged credential" '{"chain":"host"}' "$(cat "${DEST}" 2>/dev/null)"
assert_eq "sets mode 600" "600" "$(stat -c '%a' "${DEST}" 2>/dev/null)"

# ── Case 2: volume already holds a (possibly fresher) credential ───
echo "case: never overwrites an existing destination"
STAGED="${TMPDIR_TEST}/case2/staged.json"
DEST="${TMPDIR_TEST}/case2/home/.claude/.credentials.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-stale"}' > "${STAGED}"
echo '{"chain":"volume-live"}' > "${DEST}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "keeps the volume credential" '{"chain":"volume-live"}' "$(cat "${DEST}")"

# ── Case 3: forced reseed via CENCI_SANDBOX_RESEED_CREDS=1 ────────────
echo "case: CENCI_SANDBOX_RESEED_CREDS=1 forces overwrite"
STAGED="${TMPDIR_TEST}/case3/staged.json"
DEST="${TMPDIR_TEST}/case3/home/.claude/.credentials.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-new"}' > "${STAGED}"
echo '{"chain":"volume-dead"}' > "${DEST}"
chmod 644 "${DEST}"
CENCI_SANDBOX_RESEED_CREDS=1 seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "overwrites with staged credential" '{"chain":"host-new"}' "$(cat "${DEST}")"
assert_eq "restores mode 600" "600" "$(stat -c '%a' "${DEST}")"

# ── Case 4: nothing staged (host has no credential) ────────────────
echo "case: missing staged file is a no-op"
STAGED="${TMPDIR_TEST}/case4/absent.json"
DEST="${TMPDIR_TEST}/case4/home/.claude/.credentials.json"
mkdir -p "$(dirname "${DEST}")"
echo '{"chain":"volume-live"}' > "${DEST}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "leaves destination untouched" '{"chain":"volume-live"}' "$(cat "${DEST}")"

echo "case: missing staged file with missing destination stays absent"
STAGED="${TMPDIR_TEST}/case4b/absent.json"
DEST="${TMPDIR_TEST}/case4b/home/.claude/.credentials.json"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
if [[ -e "${DEST}" ]]; then
    fail "destination should not be created"
else
    pass
fi

# ── Case 5: destination is a dangling symlink (pre-fix layouts) ────
echo "case: replaces a dangling symlinked destination on seed"
STAGED="${TMPDIR_TEST}/case5/staged.json"
DEST="${TMPDIR_TEST}/case5/home/.claude/.credentials.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host"}' > "${STAGED}"
ln -s /nonexistent-target "${DEST}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies over the dangling symlink" '{"chain":"host"}' "$(cat "${DEST}" 2>/dev/null)"

# ── Case 6: OpenCode auth.json seed-once (#490) ────────────────────
# Mirrors case 1/case 3 exactly, at OpenCode's own auth/credentials path
# (~/.local/share/opencode/auth.json) — seed_credential is path-agnostic, so
# the same seed-once + CENCI_SANDBOX_RESEED_CREDS contract applies verbatim.
echo "case: seeds OpenCode auth.json when destination is missing"
STAGED="${TMPDIR_TEST}/case6/staged.json"
DEST="${TMPDIR_TEST}/case6/home/.local/share/opencode/auth.json"
mkdir -p "$(dirname "${STAGED}")"
echo '{"chain":"host"}' > "${STAGED}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies staged OpenCode credential" '{"chain":"host"}' "$(cat "${DEST}" 2>/dev/null)"
assert_eq "sets mode 600" "600" "$(stat -c '%a' "${DEST}" 2>/dev/null)"

echo "case: never overwrites an existing OpenCode auth.json"
STAGED="${TMPDIR_TEST}/case6b/staged.json"
DEST="${TMPDIR_TEST}/case6b/home/.local/share/opencode/auth.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-stale"}' > "${STAGED}"
echo '{"chain":"volume-live"}' > "${DEST}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "keeps the volume OpenCode credential" '{"chain":"volume-live"}' "$(cat "${DEST}")"

echo "case: CENCI_SANDBOX_RESEED_CREDS=1 forces OpenCode auth.json overwrite"
STAGED="${TMPDIR_TEST}/case6c/staged.json"
DEST="${TMPDIR_TEST}/case6c/home/.local/share/opencode/auth.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-new"}' > "${STAGED}"
echo '{"chain":"volume-dead"}' > "${DEST}"
chmod 644 "${DEST}"
CENCI_SANDBOX_RESEED_CREDS=1 seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "overwrites with staged OpenCode credential" '{"chain":"host-new"}' "$(cat "${DEST}")"
assert_eq "restores mode 600" "600" "$(stat -c '%a' "${DEST}")"

# ── Case 7: Pencil CLI session seed-once ───────────────────────────
# Mirrors case 1/case 2 at Pencil's session path (~/.pencil/session-cli.json)
# — headless design reads (`pen interactive`) authenticate with this seeded
# session when no PEN_CLI_KEY is forwarded. Session tokens are treated as
# rotating like the agent OAuth chains, so the seed-once contract applies.
echo "case: seeds Pencil session-cli.json when destination is missing"
STAGED="${TMPDIR_TEST}/case7/staged.json"
DEST="${TMPDIR_TEST}/case7/home/.pencil/session-cli.json"
mkdir -p "$(dirname "${STAGED}")"
echo '{"chain":"host"}' > "${STAGED}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies staged Pencil session" '{"chain":"host"}' "$(cat "${DEST}" 2>/dev/null)"
assert_eq "sets mode 600" "600" "$(stat -c '%a' "${DEST}" 2>/dev/null)"

echo "case: never overwrites an existing Pencil session-cli.json"
STAGED="${TMPDIR_TEST}/case7b/staged.json"
DEST="${TMPDIR_TEST}/case7b/home/.pencil/session-cli.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-stale"}' > "${STAGED}"
echo '{"chain":"volume-live"}' > "${DEST}"
seed_credential "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "keeps the volume Pencil session" '{"chain":"volume-live"}' "$(cat "${DEST}")"

# ── Case 8: entrypoint wires the Pencil staging → home-volume path ─
# The generic function contract above is meaningless if entrypoint.sh never
# calls it for Pencil's paths. Assert the exact wiring line (full staged and
# destination paths on one line, matched as a fixed string).
echo "case: entrypoint.sh seeds the staged Pencil session into the home volume"
if grep -qF "seed_credential /tmp/host-pencil-creds/session-cli.json /home/dev/.pencil/session-cli.json" \
    "${SCRIPT_DIR}/../entrypoint.sh"; then
    pass
else
    fail "entrypoint.sh does not seed /tmp/host-pencil-creds/session-cli.json to /home/dev/.pencil/session-cli.json"
fi

echo
echo "Passed: ${PASSES}, Failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
