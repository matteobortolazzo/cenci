#!/bin/bash
# Shared settings migration for the sandbox home volume.
#
# Sourced by entrypoint.sh (in the container) and by the test harness
# (dev-sandbox/tests/settings-merge.test.sh) on the host, so the jq that
# provisions/migrates /home/dev/.claude/settings.json lives in exactly one
# place.
#
# What the migration does, in one idempotent pass:
#   * seeds the container-only bypass-mode keys (see entrypoint.sh for why
#     these are safe only inside the container),
#   * enables the current agentwatch/agentflow plugins from the agent-stack
#     marketplace so coding-agent sessions are visible on the host status bar,
#   * removes the stale pre-rename muxwatch/ccflow plugins and the old
#     claude-tools marketplace, which would otherwise 404 on bootstrap and
#     shadow the renamed plugins.

# Container-only bypass settings. The container boundary is what makes bypass
# mode safe — these must never reach the host ~/.claude/settings.json.
BYPASS_SETTINGS='{"skipDangerousModePermissionPrompt":true,"permissions":{"defaultMode":"bypassPermissions"}}'

# Current marketplace + plugins that make sandbox sessions visible to the host
# agentwatch daemon.
PLUGIN_SETTINGS='{"extraKnownMarketplaces":{"agent-stack":{"source":{"source":"github","repo":"matteobortolazzo/agent-stack"}}},"enabledPlugins":{"agentwatch@agent-stack":true,"agentflow@agent-stack":true}}'

# migrate_settings: read a settings.json object from stdin, write the merged +
# migrated object to stdout. Reads `{}` when given an empty/whitespace input so
# fresh and invalid volumes get seeded too. Idempotent: running it on its own
# output is a no-op.
migrate_settings() {
    jq --argjson bypass "${BYPASS_SETTINGS}" --argjson plugins "${PLUGIN_SETTINGS}" '
        (. * $bypass * $plugins)
        | del(.enabledPlugins["muxwatch@claude-tools"], .enabledPlugins["ccflow@claude-tools"])
        | del(.extraKnownMarketplaces["claude-tools"])
    '
}

# Onboarding state lives in /home/dev/.claude.json (NOT settings.json). Marking
# it complete skips Claude Code's first-run wizard — theme picker, terminal
# "anti-flicker" setup, and the account/login step — on a fresh home volume.
# Login itself still works via the host credentials the entrypoint injects.
ONBOARDING_SETTINGS='{"hasCompletedOnboarding":true}'

# seed_onboarding: read a .claude.json object from stdin, write it back with the
# onboarding flag set. Deep-merges so oauthAccount, project trust, and history
# are preserved. Idempotent. The caller must only pipe VALID JSON through this:
# .claude.json holds unrecoverable state, so a corrupt file is left untouched
# rather than clobbered.
seed_onboarding() {
    jq --argjson onboarding "${ONBOARDING_SETTINGS}" '. * $onboarding'
}
