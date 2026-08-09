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

# ── Case 9: GitHub CLI hosts.yml is seeded only when it carries a token ──
# gh has no refresh cycle, so its copies never fork into the independent token
# chains the seed-once contract above guards against, and the host copy stays
# canonical — but only when the host file actually holds the token. (Canonical
# is not the same as stable: GitHub issues one OAuth token per user per app, so
# a fresh `gh auth login` anywhere invalidates every other copy, and the copy
# below is what recovers from that.) With gh's default secure storage the
# token lives in the host OS keyring and hosts.yml lists the account with no
# `oauth_token:` at all. Copying that token-less file into the container is
# worse than copying nothing: gh's multi-account config migration finds an
# account whose token it cannot resolve and aborts, failing *every* gh
# invocation (even `gh --version`, even with GH_TOKEN set, because the
# migration runs on config load before auth resolution). With no hosts.yml,
# gh runs fine and honours GH_TOKEN.
HOSTS_WITH_TOKEN=$'github.com:\n    git_protocol: https\n    users:\n        octocat:\n            oauth_token: gho_hosttoken\n    user: octocat\n    oauth_token: gho_hosttoken\n'
HOSTS_NO_TOKEN=$'github.com:\n    git_protocol: https\n    users:\n        octocat:\n    user: octocat\n'

echo "case: seeds hosts.yml when the staged host file carries an oauth_token"
STAGED="${TMPDIR_TEST}/case9/hosts.yml"
DEST="${TMPDIR_TEST}/case9/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")"
printf '%s' "${HOSTS_WITH_TOKEN}" > "${STAGED}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies staged hosts.yml" "${HOSTS_WITH_TOKEN%$'\n'}" "$(cat "${DEST}" 2>/dev/null)"
assert_eq "sets mode 600" "600" "$(stat -c '%a' "${DEST}" 2>/dev/null)"

echo "case: a token-carrying host file still overwrites an existing hosts.yml"
STAGED="${TMPDIR_TEST}/case9b/hosts.yml"
DEST="${TMPDIR_TEST}/case9b/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
printf '%s' "${HOSTS_WITH_TOKEN}" > "${STAGED}"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "host re-auth propagates" "${HOSTS_WITH_TOKEN%$'\n'}" "$(cat "${DEST}")"

echo "case: never copies a token-less host file over an existing container login"
STAGED="${TMPDIR_TEST}/case9c/hosts.yml"
DEST="${TMPDIR_TEST}/case9c/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
printf '%s' "${HOSTS_NO_TOKEN}" > "${STAGED}"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
STDERR_FILE="${TMPDIR_TEST}/case9c/stderr.txt"
seed_gh_hosts "${STAGED}" "${DEST}" 2> "${STDERR_FILE}"
assert_eq "returns success" "0" "$?"
assert_eq "keeps the in-container gh login" \
    'github.com:
    oauth_token: gho_containertoken' "$(cat "${DEST}")"
if grep -qF "keeping this sandbox's own gh login" "${STDERR_FILE}"; then
    pass
else
    fail "expected a warning naming the kept container login, got: $(cat "${STDERR_FILE}")"
fi

echo "case: a token-less host file with no container login is skipped, not copied"
STAGED="${TMPDIR_TEST}/case9d/hosts.yml"
DEST="${TMPDIR_TEST}/case9d/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")"
printf '%s' "${HOSTS_NO_TOKEN}" > "${STAGED}"
STDERR_FILE="${TMPDIR_TEST}/case9d/stderr.txt"
seed_gh_hosts "${STAGED}" "${DEST}" 2> "${STDERR_FILE}"
assert_eq "returns success" "0" "$?"
if [[ -e "${DEST}" ]]; then
    fail "token-less hosts.yml must not be copied — it bricks every gh command"
else
    pass
fi
if grep -qF "gh auth login --insecure-storage" "${STDERR_FILE}"; then
    pass
else
    fail "expected the warning to name the --insecure-storage remedy, got: $(cat "${STDERR_FILE}")"
fi

echo "case: an oauth_token key with an empty value counts as token-less"
STAGED="${TMPDIR_TEST}/case9e/hosts.yml"
DEST="${TMPDIR_TEST}/case9e/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")"
printf 'github.com:\n    user: octocat\n    oauth_token:\n' > "${STAGED}"
seed_gh_hosts "${STAGED}" "${DEST}" 2>/dev/null
assert_eq "returns success" "0" "$?"
if [[ -e "${DEST}" ]]; then
    fail "an empty oauth_token value must not count as a usable token"
else
    pass
fi

echo "case: missing staged hosts.yml is a no-op"
STAGED="${TMPDIR_TEST}/case9f/absent.yml"
DEST="${TMPDIR_TEST}/case9f/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${DEST}")"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "leaves destination untouched" \
    'github.com:
    oauth_token: gho_containertoken' "$(cat "${DEST}")"

# ── Case 10: entrypoint wires gh staging → home volume via seed_gh_hosts ──
# The contract above is meaningless if entrypoint.sh still does its own
# unconditional `cp` of the staged hosts.yml.
echo "case: entrypoint.sh seeds the staged gh hosts.yml through seed_gh_hosts"
if grep -qF "seed_gh_hosts /tmp/host-gh-config/hosts.yml /home/dev/.config/gh/hosts.yml" \
    "${SCRIPT_DIR}/../entrypoint.sh"; then
    pass
else
    fail "entrypoint.sh does not seed /tmp/host-gh-config/hosts.yml to /home/dev/.config/gh/hosts.yml via seed_gh_hosts"
fi

echo "case: entrypoint.sh no longer copies the staged hosts.yml unconditionally"
if grep -qE '^[[:space:]]*cp /tmp/host-gh-config/hosts\.yml' "${SCRIPT_DIR}/../entrypoint.sh"; then
    fail "entrypoint.sh still contains an unconditional cp of the staged gh hosts.yml"
else
    pass
fi

echo
echo "Passed: ${PASSES}, Failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
