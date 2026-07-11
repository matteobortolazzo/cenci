#!/bin/bash
# Tests for the settings.json provision/migrate logic in
# dev-sandbox/lib/migrate-settings.sh.
#
# Runs on the host with the system `jq` — no Docker required. Sources the same
# migrate_settings() the entrypoint uses, so the jq under test is the shipped
# jq.
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

# assert_jq <label> <json> <jq-filter-that-must-be-true>
assert_jq() {
    local label="$1" json="$2" filter="$3"
    if echo "${json}" | jq -e "${filter}" >/dev/null 2>&1; then
        pass
    else
        fail "${label} (filter: ${filter})"
    fi
}

echo "settings-merge.test.sh"

# ── Case 1: stale pre-rename volume ───────────────────────────────
# The real contents of the claude-sand-home-default volume that triggered this
# fix: old muxwatch/ccflow plugins from the renamed claude-tools marketplace.
STALE='{
  "enabledPlugins": { "muxwatch@claude-tools": true, "ccflow@claude-tools": true },
  "extraKnownMarketplaces": { "claude-tools": { "source": { "source": "github", "repo": "matteobortolazzo/claude-tools" } } },
  "someUserKey": "keepme"
}'
OUT_STALE="$(echo "${STALE}" | migrate_settings)"

echo "case: stale pre-rename volume"
assert_jq "enables agentwatch@agent-stack"        "${OUT_STALE}" '.enabledPlugins["agentwatch@agent-stack"] == true'
assert_jq "enables agentflow@agent-stack"         "${OUT_STALE}" '.enabledPlugins["agentflow@agent-stack"] == true'
assert_jq "adds agent-stack marketplace"          "${OUT_STALE}" '.extraKnownMarketplaces["agent-stack"].source.repo == "matteobortolazzo/agent-stack"'
assert_jq "drops muxwatch@claude-tools"           "${OUT_STALE}" '.enabledPlugins | has("muxwatch@claude-tools") | not'
assert_jq "drops ccflow@claude-tools"             "${OUT_STALE}" '.enabledPlugins | has("ccflow@claude-tools") | not'
assert_jq "drops claude-tools marketplace"        "${OUT_STALE}" '.extraKnownMarketplaces | has("claude-tools") | not'
assert_jq "no *@claude-tools plugin keys remain"  "${OUT_STALE}" '[.enabledPlugins | keys[] | select(endswith("@claude-tools"))] | length == 0'
assert_jq "preserves unrelated user keys"         "${OUT_STALE}" '.someUserKey == "keepme"'
assert_jq "seeds bypass mode"                     "${OUT_STALE}" '.permissions.defaultMode == "bypassPermissions" and .skipDangerousModePermissionPrompt == true'

# ── Case 2: empty object (upgrade of an old `{}` volume) ──────────
OUT_EMPTY="$(echo '{}' | migrate_settings)"
echo "case: empty object"
assert_jq "seeds bypass mode"          "${OUT_EMPTY}" '.permissions.defaultMode == "bypassPermissions" and .skipDangerousModePermissionPrompt == true'
assert_jq "enables agentwatch"         "${OUT_EMPTY}" '.enabledPlugins["agentwatch@agent-stack"] == true'
assert_jq "enables agentflow"          "${OUT_EMPTY}" '.enabledPlugins["agentflow@agent-stack"] == true'
assert_jq "adds agent-stack marketplace" "${OUT_EMPTY}" '.extraKnownMarketplaces["agent-stack"] != null'

# ── Case 3: fresh volume (entrypoint seeds `{}` into migrate) ─────
# The fresh-volume branch pipes `{}` through migrate_settings, so it is
# equivalent to case 2 — assert it explicitly for regression coverage.
OUT_FRESH="$(echo '{}' | migrate_settings)"
echo "case: fresh volume"
assert_jq "has both plugins + bypass" "${OUT_FRESH}" '.enabledPlugins["agentwatch@agent-stack"] == true and .enabledPlugins["agentflow@agent-stack"] == true and .permissions.defaultMode == "bypassPermissions"'

# ── Case 4: idempotency ──────────────────────────────────────────
echo "case: idempotency"
OUT_ONCE="$(echo "${STALE}" | migrate_settings)"
OUT_TWICE="$(echo "${STALE}" | migrate_settings | migrate_settings)"
if [[ "$(echo "${OUT_ONCE}" | jq -S .)" == "$(echo "${OUT_TWICE}" | jq -S .)" ]]; then
    pass
else
    fail "running migrate twice differs from running it once"
fi

# ── Case 5: user override of a bypass key is replaced, plugins win ─
# ours-win-on-conflict: a home volume that disabled a plugin gets re-enabled.
OVERRIDE='{"enabledPlugins":{"agentwatch@agent-stack":false},"permissions":{"defaultMode":"default"}}'
OUT_OVERRIDE="$(echo "${OVERRIDE}" | migrate_settings)"
echo "case: our keys win on conflict"
assert_jq "re-enables agentwatch"   "${OUT_OVERRIDE}" '.enabledPlugins["agentwatch@agent-stack"] == true'
assert_jq "forces bypass mode"      "${OUT_OVERRIDE}" '.permissions.defaultMode == "bypassPermissions"'

# ── Case 6: onboarding seed into .claude.json ────────────────────
# Fresh volume: the entrypoint writes ONBOARDING_SETTINGS directly.
echo "case: onboarding seed (fresh)"
assert_jq "fresh seed marks onboarding complete" "${ONBOARDING_SETTINGS}" '.hasCompletedOnboarding == true'

# Existing .claude.json: seed_onboarding must preserve account/trust/history.
EXISTING_CLAUDE_JSON='{"oauthAccount":{"emailAddress":"x@y.z"},"projects":{"/workspace":{"hasTrustDialogAccepted":true}},"hasCompletedOnboarding":false}'
OUT_ONBOARD="$(echo "${EXISTING_CLAUDE_JSON}" | seed_onboarding)"
echo "case: onboarding seed (existing .claude.json)"
assert_jq "flips onboarding to true"   "${OUT_ONBOARD}" '.hasCompletedOnboarding == true'
assert_jq "preserves oauthAccount"     "${OUT_ONBOARD}" '.oauthAccount.emailAddress == "x@y.z"'
assert_jq "preserves project trust"    "${OUT_ONBOARD}" '.projects["/workspace"].hasTrustDialogAccepted == true'

echo "case: onboarding seed idempotency"
if [[ "$(echo "${EXISTING_CLAUDE_JSON}" | seed_onboarding | jq -S .)" == "$(echo "${EXISTING_CLAUDE_JSON}" | seed_onboarding | seed_onboarding | jq -S .)" ]]; then
    pass
else
    fail "running seed_onboarding twice differs from running it once"
fi

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
