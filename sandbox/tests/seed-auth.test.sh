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

# ── Case 7: GitHub CLI hosts.yml is seeded only when it carries a token ──
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
STAGED="${TMPDIR_TEST}/case7/hosts.yml"
DEST="${TMPDIR_TEST}/case7/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")"
printf '%s' "${HOSTS_WITH_TOKEN}" > "${STAGED}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "copies staged hosts.yml" "${HOSTS_WITH_TOKEN%$'\n'}" "$(cat "${DEST}" 2>/dev/null)"
assert_eq "sets mode 600" "600" "$(stat -c '%a' "${DEST}" 2>/dev/null)"

echo "case: a token-carrying host file still overwrites an existing hosts.yml"
STAGED="${TMPDIR_TEST}/case7b/hosts.yml"
DEST="${TMPDIR_TEST}/case7b/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
printf '%s' "${HOSTS_WITH_TOKEN}" > "${STAGED}"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "host re-auth propagates" "${HOSTS_WITH_TOKEN%$'\n'}" "$(cat "${DEST}")"

echo "case: never copies a token-less host file over an existing container login"
STAGED="${TMPDIR_TEST}/case7c/hosts.yml"
DEST="${TMPDIR_TEST}/case7c/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")" "$(dirname "${DEST}")"
printf '%s' "${HOSTS_NO_TOKEN}" > "${STAGED}"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
STDERR_FILE="${TMPDIR_TEST}/case7c/stderr.txt"
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
STAGED="${TMPDIR_TEST}/case7d/hosts.yml"
DEST="${TMPDIR_TEST}/case7d/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${STAGED}")"
printf '%s' "${HOSTS_NO_TOKEN}" > "${STAGED}"
STDERR_FILE="${TMPDIR_TEST}/case7d/stderr.txt"
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
STAGED="${TMPDIR_TEST}/case7e/hosts.yml"
DEST="${TMPDIR_TEST}/case7e/home/.config/gh/hosts.yml"
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
STAGED="${TMPDIR_TEST}/case7f/absent.yml"
DEST="${TMPDIR_TEST}/case7f/home/.config/gh/hosts.yml"
mkdir -p "$(dirname "${DEST}")"
printf 'github.com:\n    oauth_token: gho_containertoken\n' > "${DEST}"
seed_gh_hosts "${STAGED}" "${DEST}"
assert_eq "returns success" "0" "$?"
assert_eq "leaves destination untouched" \
    'github.com:
    oauth_token: gho_containertoken' "$(cat "${DEST}")"

# ── Case 8: entrypoint wires gh staging → home volume via seed_gh_hosts ──
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

# ── Case 9 (#1080): the Azure CLI auth set is seeded atomically ──────
# `az` splits its auth across several files under ~/.azure that only make
# sense together: azureProfile.json names the identity, msal_token_cache.json
# holds that identity's tokens, service_principal_entries.json holds SP
# secrets. Per-file seeding could splice the host's profile onto a
# container-side token cache (or the reverse) — two identity chains in one
# broken login — so the whole set is seeded only when the destination has NO
# azure credential at all. MSAL refresh tokens rotate like the OAuth chains
# above, so the same #259 seed-once caution applies.

# azure_staged <dir> — writes a full host-shaped staged credential set.
azure_staged() {
    local dir="$1"
    mkdir -p "${dir}"
    echo '{"subscriptions":[{"id":"host-sub"}]}' > "${dir}/azureProfile.json"
    echo '{"RefreshToken":{"host":"chain"}}' > "${dir}/msal_token_cache.json"
    echo '[{"servicePrincipalId":"host-sp"}]' > "${dir}/service_principal_entries.json"
}

echo "case: seeds the whole Azure auth set when the destination has none"
STAGED_DIR="${TMPDIR_TEST}/case9/staged"
DEST_DIR="${TMPDIR_TEST}/case9/home/.azure"
azure_staged "${STAGED_DIR}"
seed_azure_creds "${STAGED_DIR}" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "copies azureProfile.json" '{"subscriptions":[{"id":"host-sub"}]}' "$(cat "${DEST_DIR}/azureProfile.json" 2>/dev/null)"
assert_eq "copies msal_token_cache.json" '{"RefreshToken":{"host":"chain"}}' "$(cat "${DEST_DIR}/msal_token_cache.json" 2>/dev/null)"
assert_eq "copies service_principal_entries.json" '[{"servicePrincipalId":"host-sp"}]' "$(cat "${DEST_DIR}/service_principal_entries.json" 2>/dev/null)"
assert_eq "sets mode 600 on the token cache" "600" "$(stat -c '%a' "${DEST_DIR}/msal_token_cache.json" 2>/dev/null)"
assert_eq "sets mode 700 on the .azure directory" "700" "$(stat -c '%a' "${DEST_DIR}" 2>/dev/null)"

echo "case: a container-side login blocks the whole set, not just its own files"
STAGED_DIR="${TMPDIR_TEST}/case9b/staged"
DEST_DIR="${TMPDIR_TEST}/case9b/home/.azure"
azure_staged "${STAGED_DIR}"
mkdir -p "${DEST_DIR}"
# Only the profile exists in the volume — as after an in-container `az login`
# whose token cache has not been written back yet. Seeding the host's token
# cache here would pair the container's identity with the host's tokens.
echo '{"subscriptions":[{"id":"volume-sub"}]}' > "${DEST_DIR}/azureProfile.json"
seed_azure_creds "${STAGED_DIR}" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "keeps the volume profile" '{"subscriptions":[{"id":"volume-sub"}]}' "$(cat "${DEST_DIR}/azureProfile.json")"
assert_eq "does not seed the host token cache alongside it" "absent" "$([[ -e "${DEST_DIR}/msal_token_cache.json" ]] && echo present || echo absent)"

echo "case: CENCI_SANDBOX_RESEED_CREDS=1 forces the Azure set to be re-copied"
STAGED_DIR="${TMPDIR_TEST}/case9c/staged"
DEST_DIR="${TMPDIR_TEST}/case9c/home/.azure"
azure_staged "${STAGED_DIR}"
mkdir -p "${DEST_DIR}"
echo '{"subscriptions":[{"id":"volume-dead"}]}' > "${DEST_DIR}/azureProfile.json"
CENCI_SANDBOX_RESEED_CREDS=1 seed_azure_creds "${STAGED_DIR}" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "overwrites the dead profile" '{"subscriptions":[{"id":"host-sub"}]}' "$(cat "${DEST_DIR}/azureProfile.json")"
assert_eq "brings the matching token cache with it" '{"RefreshToken":{"host":"chain"}}' "$(cat "${DEST_DIR}/msal_token_cache.json" 2>/dev/null)"

echo "case: an absent staging directory is a no-op"
DEST_DIR="${TMPDIR_TEST}/case9d/home/.azure"
seed_azure_creds "${TMPDIR_TEST}/case9d/nonexistent" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "creates no destination" "absent" "$([[ -e "${DEST_DIR}" ]] && echo present || echo absent)"

echo "case: only the auth files are copied, never the rest of ~/.azure"
STAGED_DIR="${TMPDIR_TEST}/case9e/staged"
DEST_DIR="${TMPDIR_TEST}/case9e/home/.azure"
azure_staged "${STAGED_DIR}"
mkdir -p "${STAGED_DIR}/commands"
echo 'telemetry' > "${STAGED_DIR}/telemetry.txt"
echo 'log' > "${STAGED_DIR}/commands/2026-01-01.log"
seed_azure_creds "${STAGED_DIR}" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "skips telemetry.txt" "absent" "$([[ -e "${DEST_DIR}/telemetry.txt" ]] && echo present || echo absent)"
assert_eq "skips the commands cache" "absent" "$([[ -e "${DEST_DIR}/commands" ]] && echo present || echo absent)"

echo "case: replaces a dangling symlinked Azure credential on seed"
STAGED_DIR="${TMPDIR_TEST}/case9f/staged"
DEST_DIR="${TMPDIR_TEST}/case9f/home/.azure"
azure_staged "${STAGED_DIR}"
mkdir -p "${DEST_DIR}"
ln -s "${TMPDIR_TEST}/case9f/gone.json" "${DEST_DIR}/azureProfile.json"
seed_azure_creds "${STAGED_DIR}" "${DEST_DIR}"
assert_eq "returns success" "0" "$?"
assert_eq "destination is a regular file" "regular" "$([[ -L "${DEST_DIR}/azureProfile.json" ]] && echo symlink || echo regular)"
assert_eq "holds the staged profile" '{"subscriptions":[{"id":"host-sub"}]}' "$(cat "${DEST_DIR}/azureProfile.json" 2>/dev/null)"

# ── Case 10 (#1080): entrypoint wires the Azure staging → home volume ──
# The function contract above is meaningless if entrypoint.sh never calls it.
# The staged path must also match azureCredsStageDir in
# watch/internal/sandbox/launcher/azure.go, which mounts the files there.
echo "case: entrypoint.sh seeds the staged Azure credentials into the home volume"
if grep -qF "seed_azure_creds /tmp/host-azure-creds /home/dev/.azure" \
    "${SCRIPT_DIR}/../entrypoint.sh"; then
    pass
else
    fail "entrypoint.sh does not seed /tmp/host-azure-creds to /home/dev/.azure"
fi

echo "case: the launcher stages Azure credentials at the path the entrypoint reads"
LAUNCHER_AZURE="${SCRIPT_DIR}/../../watch/internal/sandbox/launcher/azure.go"
if [[ ! -f "${LAUNCHER_AZURE}" ]]; then
    fail "launcher azure.go not found at ${LAUNCHER_AZURE}"
elif grep -qF 'azureCredsStageDir = "/tmp/host-azure-creds"' "${LAUNCHER_AZURE}"; then
    pass
else
    fail "launcher azure.go does not stage credentials at /tmp/host-azure-creds — the entrypoint would seed an empty directory"
fi

echo "case: the launcher's staged file set matches AZURE_CRED_FILES"
if [[ ! -f "${LAUNCHER_AZURE}" ]]; then
    fail "launcher azure.go not found at ${LAUNCHER_AZURE}"
else
    for azure_file in "${AZURE_CRED_FILES[@]}"; do
        if grep -qF "\"${azure_file}\"" "${LAUNCHER_AZURE}"; then
            pass
        else
            fail "launcher azure.go never stages ${azure_file}, so seed_azure_creds can never seed it"
        fi
    done
fi

echo
echo "Passed: ${PASSES}, Failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
