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

# Neutralize any ambient CENCI_SANDBOX_PLUGINS (suites can run inside a sandbox
# container that has it set) so every case without an explicit assignment
# exercises the unset/default behavior.
unset CENCI_SANDBOX_PLUGINS

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

# ── Case 9: CENCI_SANDBOX_PLUGINS selects which plugins are registered ─
# Unset keeps today's pair (every case above). A set list enables only the named
# plugins; an explicitly empty list enables none and seeds no marketplace.
OUT_ONLY_FLOW="$(echo '{}' | CENCI_SANDBOX_PLUGINS=cenci migrate_settings)"
echo "case: list = cenci"
assert_jq "enables cenci"                   "${OUT_ONLY_FLOW}" '.enabledPlugins["cenci@cenci"] == true'
assert_jq "does not enable cenci-watch"     "${OUT_ONLY_FLOW}" '.enabledPlugins | has("cenci-watch@cenci") | not'
assert_jq "seeds the cenci marketplace"     "${OUT_ONLY_FLOW}" '.extraKnownMarketplaces["cenci"].source.repo == "matteobortolazzo/cenci"'
assert_jq "still seeds bypass mode"         "${OUT_ONLY_FLOW}" '.permissions.defaultMode == "bypassPermissions" and .skipDangerousModePermissionPrompt == true'

OUT_ONLY_WATCH="$(echo '{}' | CENCI_SANDBOX_PLUGINS=cenci-watch migrate_settings)"
echo "case: list = cenci-watch"
assert_jq "enables cenci-watch"             "${OUT_ONLY_WATCH}" '.enabledPlugins["cenci-watch@cenci"] == true'
assert_jq "does not enable cenci"           "${OUT_ONLY_WATCH}" '.enabledPlugins | has("cenci@cenci") | not'
assert_jq "seeds the cenci marketplace"     "${OUT_ONLY_WATCH}" '.extraKnownMarketplaces["cenci"] != null'

OUT_BOTH_SPACED="$(echo '{}' | CENCI_SANDBOX_PLUGINS=' cenci   cenci-watch ' migrate_settings)"
echo "case: list with repeated spaces"
assert_jq "enables both plugins"            "${OUT_BOTH_SPACED}" '.enabledPlugins["cenci@cenci"] == true and .enabledPlugins["cenci-watch@cenci"] == true'

OUT_NO_PLUGINS="$(echo '{}' | CENCI_SANDBOX_PLUGINS='' migrate_settings)"
echo "case: explicitly empty list"
assert_jq "enables no cenci plugin"         "${OUT_NO_PLUGINS}" '[(.enabledPlugins // {}) | keys[] | select(endswith("@cenci"))] | length == 0'
assert_jq "seeds no cenci marketplace"      "${OUT_NO_PLUGINS}" '(.extraKnownMarketplaces // {}) | has("cenci") | not'
assert_jq "still seeds bypass mode"         "${OUT_NO_PLUGINS}" '.permissions.defaultMode == "bypassPermissions" and .skipDangerousModePermissionPrompt == true'
assert_jq "still seeds statusLine and UI prefs" "${OUT_NO_PLUGINS}" '.statusLine.command == "/usr/local/bin/ccline" and .tui == "fullscreen"'

OUT_UNKNOWN="$(echo '{}' | CENCI_SANDBOX_PLUGINS='cenci bogus' migrate_settings)"
echo "case: unknown token in the list"
assert_jq "enables the known plugin"        "${OUT_UNKNOWN}" '.enabledPlugins["cenci@cenci"] == true'
assert_jq "writes no key for the unknown token" "${OUT_UNKNOWN}" '.enabledPlugins | has("bogus@cenci") | not'

# ── Case 10: a narrowed list actively disables excluded plugins ───────
# migrate_settings force-merges on every boot, so an excluded plugin's existing
# enabledPlugins entry must be deleted — not merely left unwritten. Everything
# else on the volume is preserved.
EXISTING_VOLUME='{
  "enabledPlugins": { "cenci@cenci": true, "cenci-watch@cenci": true, "foo@bar": true },
  "extraKnownMarketplaces": { "other": { "source": { "source": "github", "repo": "x/y" } } },
  "permissions": { "allow": ["Bash(ls:*)"], "deny": ["Read(./.env)"] },
  "someUserKey": "keepme"
}'
OUT_NARROW="$(echo "${EXISTING_VOLUME}" | CENCI_SANDBOX_PLUGINS=cenci migrate_settings)"
echo "case: narrowed list on an existing volume"
assert_jq "deletes the excluded cenci-watch entry" "${OUT_NARROW}" '.enabledPlugins | has("cenci-watch@cenci") | not'
assert_jq "keeps the listed cenci enabled"         "${OUT_NARROW}" '.enabledPlugins["cenci@cenci"] == true'
assert_jq "preserves unrelated plugin keys"        "${OUT_NARROW}" '.enabledPlugins["foo@bar"] == true'
assert_jq "preserves permissions.allow and deny"   "${OUT_NARROW}" '.permissions.allow == ["Bash(ls:*)"] and .permissions.deny == ["Read(./.env)"]'
assert_jq "preserves other marketplaces"           "${OUT_NARROW}" '.extraKnownMarketplaces.other.source.repo == "x/y"'
assert_jq "preserves unrelated top-level keys"     "${OUT_NARROW}" '.someUserKey == "keepme"'
assert_jq "seeds the cenci marketplace"            "${OUT_NARROW}" '.extraKnownMarketplaces.cenci != null'

OUT_NARROW_WATCH="$(echo "${EXISTING_VOLUME}" | CENCI_SANDBOX_PLUGINS=cenci-watch migrate_settings)"
echo "case: narrowed list excluding cenci"
assert_jq "deletes the excluded cenci entry"       "${OUT_NARROW_WATCH}" '.enabledPlugins | has("cenci@cenci") | not'
assert_jq "keeps cenci-watch enabled"              "${OUT_NARROW_WATCH}" '.enabledPlugins["cenci-watch@cenci"] == true'

OUT_EMPTY_VOLUME="$(echo "${EXISTING_VOLUME}" | CENCI_SANDBOX_PLUGINS='' migrate_settings)"
echo "case: empty list on an existing volume"
assert_jq "deletes cenci@cenci"                    "${OUT_EMPTY_VOLUME}" '.enabledPlugins | has("cenci@cenci") | not'
assert_jq "deletes cenci-watch@cenci"              "${OUT_EMPTY_VOLUME}" '.enabledPlugins | has("cenci-watch@cenci") | not'
assert_jq "preserves unrelated plugin keys"        "${OUT_EMPTY_VOLUME}" '.enabledPlugins["foo@bar"] == true'
assert_jq "preserves permissions.allow and deny"   "${OUT_EMPTY_VOLUME}" '.permissions.allow == ["Bash(ls:*)"] and .permissions.deny == ["Read(./.env)"]'
assert_jq "preserves the pre-existing marketplace" "${OUT_EMPTY_VOLUME}" '.extraKnownMarketplaces.other.source.repo == "x/y"'
assert_jq "does not seed the cenci marketplace"    "${OUT_EMPTY_VOLUME}" '.extraKnownMarketplaces | has("cenci") | not'

# An already-registered cenci marketplace is not removed by an empty list: only
# enabledPlugins entries are deleted, installed files and registrations stay.
WITH_MARKETPLACE='{"enabledPlugins":{"cenci@cenci":true},"extraKnownMarketplaces":{"cenci":{"source":{"source":"github","repo":"matteobortolazzo/cenci"}}}}'
OUT_EMPTY_KEEP_MKT="$(echo "${WITH_MARKETPLACE}" | CENCI_SANDBOX_PLUGINS='' migrate_settings)"
echo "case: empty list keeps an existing cenci marketplace"
assert_jq "deletes the enabledPlugins entry"       "${OUT_EMPTY_KEEP_MKT}" '.enabledPlugins | has("cenci@cenci") | not'
assert_jq "keeps the existing marketplace"         "${OUT_EMPTY_KEEP_MKT}" '.extraKnownMarketplaces.cenci.source.repo == "matteobortolazzo/cenci"'

# ── Case 11: idempotency for narrowed and empty lists ─────────────────
echo "case: idempotency for subset and empty lists"
for list in cenci cenci-watch ''; do
    ONCE="$(echo "${EXISTING_VOLUME}" | CENCI_SANDBOX_PLUGINS="${list}" migrate_settings | jq -S .)"
    TWICE="$(echo "${EXISTING_VOLUME}" | CENCI_SANDBOX_PLUGINS="${list}" migrate_settings | CENCI_SANDBOX_PLUGINS="${list}" migrate_settings | jq -S .)"
    if [[ -n "${ONCE}" && "${ONCE}" == "${TWICE}" ]]; then
        pass
    else
        fail "running migrate twice differs from once for list '${list}'"
    fi
done

# ── Case 12: unset reproduces the pre-#1003 output byte for byte ──────
# CENCI_SANDBOX_PLUGINS unset (a container created before the launcher set it)
# must emit exactly what migrate_settings emitted before the variable existed,
# key order included, so an unchanged sandbox rewrites no settings.json bytes.
# The expected strings are literals captured from the pre-change implementation,
# not computed from the current one.
EXPECTED_FRESH='{
  "skipDangerousModePermissionPrompt": true,
  "permissions": {
    "defaultMode": "bypassPermissions"
  },
  "extraKnownMarketplaces": {
    "cenci": {
      "source": {
        "source": "github",
        "repo": "matteobortolazzo/cenci"
      }
    }
  },
  "enabledPlugins": {
    "cenci-watch@cenci": true,
    "cenci@cenci": true
  },
  "statusLine": {
    "type": "command",
    "command": "/usr/local/bin/ccline",
    "padding": 0
  },
  "tui": "fullscreen",
  "showClearContextOnPlanAccept": true
}'
echo "case: unset list is byte-identical to the pre-change output (fresh volume)"
if [[ "$(echo '{}' | migrate_settings)" == "${EXPECTED_FRESH}" ]]; then
    pass
else
    fail "unset-list output for a fresh '{}' volume differs from the pre-change bytes: $(echo '{}' | migrate_settings)"
fi

EXPECTED_STALE='{
  "enabledPlugins": {
    "cenci-watch@cenci": true,
    "cenci@cenci": true
  },
  "extraKnownMarketplaces": {
    "cenci": {
      "source": {
        "source": "github",
        "repo": "matteobortolazzo/cenci"
      }
    }
  },
  "someUserKey": "keepme",
  "skipDangerousModePermissionPrompt": true,
  "permissions": {
    "defaultMode": "bypassPermissions"
  },
  "statusLine": {
    "type": "command",
    "command": "/usr/local/bin/ccline",
    "padding": 0
  },
  "tui": "fullscreen",
  "showClearContextOnPlanAccept": true
}'
echo "case: unset list is byte-identical to the pre-change output (stale pre-rename volume)"
if [[ "$(echo "${STALE}" | migrate_settings)" == "${EXPECTED_STALE}" ]]; then
    pass
else
    fail "unset-list output for the stale volume differs from the pre-change bytes: $(echo "${STALE}" | migrate_settings)"
fi

BOTH_KEYS_VOLUME='{
  "enabledPlugins": { "cenci@cenci": true, "cenci-watch@cenci": false, "foo@bar": true },
  "extraKnownMarketplaces": { "cenci": { "source": { "source": "github", "repo": "matteobortolazzo/cenci" } }, "other": { "source": { "source": "github", "repo": "x/y" } } },
  "permissions": { "allow": ["Bash(ls:*)"] },
  "someUserKey": "keepme"
}'
EXPECTED_BOTH_KEYS='{
  "enabledPlugins": {
    "cenci@cenci": true,
    "cenci-watch@cenci": true,
    "foo@bar": true
  },
  "extraKnownMarketplaces": {
    "cenci": {
      "source": {
        "source": "github",
        "repo": "matteobortolazzo/cenci"
      }
    },
    "other": {
      "source": {
        "source": "github",
        "repo": "x/y"
      }
    }
  },
  "permissions": {
    "allow": [
      "Bash(ls:*)"
    ],
    "defaultMode": "bypassPermissions"
  },
  "someUserKey": "keepme",
  "skipDangerousModePermissionPrompt": true,
  "statusLine": {
    "type": "command",
    "command": "/usr/local/bin/ccline",
    "padding": 0
  },
  "tui": "fullscreen",
  "showClearContextOnPlanAccept": true
}'
echo "case: unset list is byte-identical to the pre-change output (volume already holding both keys)"
if [[ "$(echo "${BOTH_KEYS_VOLUME}" | migrate_settings)" == "${EXPECTED_BOTH_KEYS}" ]]; then
    pass
else
    fail "unset-list output for a volume already holding both keys differs from the pre-change bytes: $(echo "${BOTH_KEYS_VOLUME}" | migrate_settings)"
fi

# ── Case 13: wrong-typed keys with no list to enable ──────────────────
# The cleanup deletes entries out of enabledPlugins / extraKnownMarketplaces, so
# when the list contributes no merge for a key (empty, whitespace-only, or only
# unknown tokens) a settings.json holding a non-object there must not make jq
# fail: a failed migrate leaves the file untouched, skipping the bypass seed.
# The wrong-typed value is left as it is (there is nothing to delete from it);
# a non-empty list still replaces it with an object, as the old merge did.

# assert_wrong_typed <label> <list> <input-json> <preserved-key-filter>
assert_wrong_typed() {
    local label="$1" list="$2" input="$3" typed_filter="$4" out rc
    out="$(echo "${input}" | CENCI_SANDBOX_PLUGINS="${list}" migrate_settings 2>&1)"
    rc=$?
    if [[ "${rc}" -eq 0 ]]; then
        pass
    else
        fail "${label}: migrate_settings exited ${rc}: ${out}"
        return
    fi
    assert_jq "${label}: seeds bypass mode"        "${out}" '.permissions.defaultMode == "bypassPermissions" and .skipDangerousModePermissionPrompt == true'
    assert_jq "${label}: preserves unrelated keys" "${out}" '.someUserKey == "keepme"'
    assert_jq "${label}: seeds statusLine"         "${out}" '.statusLine.command == "/usr/local/bin/ccline"'
    if [[ -n "${typed_filter}" ]]; then
        assert_jq "${label}: leaves the wrong-typed value alone" "${out}" "${typed_filter}"
    fi
}

echo "case: wrong-typed keys with an empty or whitespace-only list"
for list in '' ' '; do
    assert_wrong_typed "string enabledPlugins, list '${list}'" "${list}" \
        '{"enabledPlugins":"x","someUserKey":"keepme"}' '.enabledPlugins == "x"'
    assert_wrong_typed "array enabledPlugins, list '${list}'" "${list}" \
        '{"enabledPlugins":["x"],"someUserKey":"keepme"}' '.enabledPlugins == ["x"]'
    assert_wrong_typed "string extraKnownMarketplaces, list '${list}'" "${list}" \
        '{"extraKnownMarketplaces":"x","someUserKey":"keepme"}' '.extraKnownMarketplaces == "x"'
done

echo "case: wrong-typed keys with only unknown tokens"
assert_wrong_typed "string enabledPlugins, list 'bogus'" 'bogus' \
    '{"enabledPlugins":"x","someUserKey":"keepme"}' '.enabledPlugins == "x"'
assert_wrong_typed "array enabledPlugins, list 'bogus'" 'bogus' \
    '{"enabledPlugins":["x"],"someUserKey":"keepme"}' '.enabledPlugins == ["x"]'
assert_wrong_typed "string extraKnownMarketplaces, list 'bogus'" 'bogus' \
    '{"extraKnownMarketplaces":"x","someUserKey":"keepme"}' ''

echo "case: a non-empty list still replaces a wrong-typed value with an object"
OUT_TYPED_LIST="$(echo '{"enabledPlugins":"x","extraKnownMarketplaces":"y"}' | CENCI_SANDBOX_PLUGINS=cenci migrate_settings)"
assert_jq "enabledPlugins becomes the listed object" "${OUT_TYPED_LIST}" '.enabledPlugins == {"cenci@cenci": true}'
assert_jq "marketplace becomes the cenci object"     "${OUT_TYPED_LIST}" '.extraKnownMarketplaces.cenci.source.repo == "matteobortolazzo/cenci"'

# ── Summary ──────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
