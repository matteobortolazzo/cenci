#!/bin/bash
# Tests for the settings.json provision/migrate logic in
# sandbox/lib/migrate-settings.sh.
#
# Runs on the host with the system `jq` — no Docker required. Sources the same
# migrate_settings() the entrypoint uses, so the jq under test is the shipped
# jq.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
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
# The real contents of the claude-cenci-home-default volume that triggered this
# fix: old muxwatch/ccflow plugins from the renamed claude-tools marketplace.
STALE='{
  "enabledPlugins": { "muxwatch@claude-tools": true, "ccflow@claude-tools": true },
  "extraKnownMarketplaces": { "claude-tools": { "source": { "source": "github", "repo": "matteobortolazzo/claude-tools" } } },
  "someUserKey": "keepme"
}'
OUT_STALE="$(echo "${STALE}" | migrate_settings)"

echo "case: stale pre-rename volume"
assert_jq "enables cenci-watch@cenci"        "${OUT_STALE}" '.enabledPlugins["cenci-watch@cenci"] == true'
assert_jq "enables cenci@cenci"         "${OUT_STALE}" '.enabledPlugins["cenci@cenci"] == true'
assert_jq "adds cenci marketplace"          "${OUT_STALE}" '.extraKnownMarketplaces["cenci"].source.repo == "matteobortolazzo/cenci"'
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
assert_jq "enables cenci-watch"         "${OUT_EMPTY}" '.enabledPlugins["cenci-watch@cenci"] == true'
assert_jq "enables cenci"          "${OUT_EMPTY}" '.enabledPlugins["cenci@cenci"] == true'
assert_jq "adds cenci marketplace" "${OUT_EMPTY}" '.extraKnownMarketplaces["cenci"] != null'

# ── Case 3: fresh volume (entrypoint seeds `{}` into migrate) ─────
# The fresh-volume branch pipes `{}` through migrate_settings, so it is
# equivalent to case 2 — assert it explicitly for regression coverage.
OUT_FRESH="$(echo '{}' | migrate_settings)"
echo "case: fresh volume"
assert_jq "has both plugins + bypass" "${OUT_FRESH}" '.enabledPlugins["cenci-watch@cenci"] == true and .enabledPlugins["cenci@cenci"] == true and .permissions.defaultMode == "bypassPermissions"'

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
OVERRIDE='{"enabledPlugins":{"cenci-watch@cenci":false},"permissions":{"defaultMode":"default"}}'
OUT_OVERRIDE="$(echo "${OVERRIDE}" | migrate_settings)"
echo "case: our keys win on conflict"
assert_jq "re-enables cenci-watch"   "${OUT_OVERRIDE}" '.enabledPlugins["cenci-watch@cenci"] == true'
assert_jq "forces bypass mode"      "${OUT_OVERRIDE}" '.permissions.defaultMode == "bypassPermissions"'

# ── Case 6: statusLine seeding ───────────────────────────────────
# Absent statusLine → seed the ccline binary baked into the image. Unlike the
# bypass keys there is no safety reason to force it, so a user-customized
# statusLine in the home volume must be preserved.
echo "case: statusLine seeded when absent"
assert_jq "seeds ccline statusLine" "${OUT_EMPTY}" '.statusLine.type == "command" and .statusLine.command == "/usr/local/bin/ccline" and .statusLine.padding == 0'

CUSTOM_STATUSLINE='{"statusLine":{"type":"command","command":"/home/dev/bin/my-line"}}'
OUT_CUSTOM="$(echo "${CUSTOM_STATUSLINE}" | migrate_settings)"
echo "case: user statusLine preserved"
assert_jq "keeps user statusLine command" "${OUT_CUSTOM}" '.statusLine.command == "/home/dev/bin/my-line"'

# ── Case 7: default UI preference seeding ─────────────────────────
# Absent tui/showClearContextOnPlanAccept → seed the fullscreen renderer and
# the plan-approval clear-context option. Same non-forcing rationale as
# statusLine: a user's own choice in the home volume must be preserved.
echo "case: UI preferences seeded when absent"
assert_jq "seeds fullscreen tui"                  "${OUT_EMPTY}" '.tui == "fullscreen"'
assert_jq "seeds showClearContextOnPlanAccept"    "${OUT_EMPTY}" '.showClearContextOnPlanAccept == true'

CUSTOM_UI_PREFS='{"tui":"default","showClearContextOnPlanAccept":false}'
OUT_CUSTOM_UI="$(echo "${CUSTOM_UI_PREFS}" | migrate_settings)"
echo "case: user UI preferences preserved"
assert_jq "keeps user tui choice"                 "${OUT_CUSTOM_UI}" '.tui == "default"'
assert_jq "keeps user showClearContextOnPlanAccept" "${OUT_CUSTOM_UI}" '.showClearContextOnPlanAccept == false'

# ── Case 8: onboarding seed into .claude.json ────────────────────
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
