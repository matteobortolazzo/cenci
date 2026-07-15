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

# ── Case 3: forced reseed via AGENT_SAND_RESEED_CREDS=1 ────────────
echo "case: AGENT_SAND_RESEED_CREDS=1 forces overwrite"
STAGED="${TMPDIR_TEST}/case3/staged.json"
DEST="${TMPDIR_TEST}/case3/home/.claude/.credentials.json"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
echo '{"chain":"host-new"}' > "${STAGED}"
echo '{"chain":"volume-dead"}' > "${DEST}"
chmod 644 "${DEST}"
AGENT_SAND_RESEED_CREDS=1 seed_credential "${STAGED}" "${DEST}"
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

echo
echo "Passed: ${PASSES}, Failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
