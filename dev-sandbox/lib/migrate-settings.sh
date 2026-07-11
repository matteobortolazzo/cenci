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

# heal_plugin_installs <plugins-dir>: repair <plugins-dir>/installed_plugins.json
# so Claude Code reinstalls plugins whose cache is gone. An interrupted
# auto-install writes the metadata before populating the cache directory it
# points at; Claude Code then trusts the metadata, skips reinstall, and every
# skill of that plugin is "Unknown command" until the entry is removed.
#
# .plugins maps each plugin key to an ARRAY of install records: drop records
# whose installPath is missing on disk, then drop keys left with no records.
# jq cannot stat, so the paths are checked in bash and fed back in via --args.
# An invalid file is deleted outright — unlike .claude.json it holds no
# unrecoverable state, Claude Code just regenerates it. Atomic write,
# idempotent.
heal_plugin_installs() {
    local plugins_file="$1/installed_plugins.json"
    [[ -f "${plugins_file}" ]] || return 0

    if ! jq -e . "${plugins_file}" >/dev/null 2>&1; then
        rm -f "${plugins_file}"
        return 0
    fi

    local path missing=()
    while IFS= read -r path; do
        [[ -e "${path}" ]] || missing+=("${path}")
    done < <(jq -r '(.plugins // {})[][]?.installPath // empty' "${plugins_file}")
    [[ "${#missing[@]}" -eq 0 ]] && return 0

    if jq '
        .plugins |= (
            (. // {})
            | map_values([ .[] | select(.installPath as $p | ($ARGS.positional | index($p)) | not) ])
            | with_entries(select(.value | length > 0))
        )
    ' "${plugins_file}" --args "${missing[@]}" > "${plugins_file}.tmp"; then
        mv "${plugins_file}.tmp" "${plugins_file}"
    else
        rm -f "${plugins_file}.tmp"
    fi
}

# provision_plugins <plugins-dir> <marketplace-name> <marketplace-repo> <plugin>...
#
# Claude Code 2.1.207's settings-driven auto-install (enabledPlugins +
# extraKnownMarketplaces in settings.json) is broken: at session startup it
# writes installed_plugins.json with correct versions and installPaths but
# never populates plugins/cache/ — deterministic, reproduced on a bare
# `claude -p "hi"` with no login. heal_plugin_installs alone can't fix this:
# it drops the resulting broken records, but the next session just rewrites
# them broken again, looping forever with no cache ever materializing.
#
# The official CLI path works and is idempotent: `claude plugin marketplace
# add <repo>` clones+registers (or no-ops if already on disk), and
# `claude plugin install <plugin>@<marketplace>` populates the cache and
# writes correct metadata — even when metadata already claims the plugin is
# installed. So provision_plugins stops trusting the settings-driven path
# and calls the CLI directly. Never blocks container start: a missing
# `claude` binary (codex-sand mounts none) or an offline/failed CLI call
# just warns to stderr and returns 0.
provision_plugins() {
    local plugins_dir="$1" marketplace_name="$2" marketplace_repo="$3"
    shift 3

    command -v claude >/dev/null 2>&1 || return 0

    if [[ ! -d "${plugins_dir}/marketplaces/${marketplace_name}" ]]; then
        if ! claude plugin marketplace add "${marketplace_repo}" >/dev/null 2>&1; then
            echo "warning: failed to add marketplace ${marketplace_repo}; plugins may be unavailable this session" >&2
        fi
    fi

    local meta="${plugins_dir}/installed_plugins.json"
    local plugin key
    for plugin in "$@"; do
        key="${plugin}@${marketplace_name}"
        if [[ -f "${meta}" ]] && jq -e --arg key "${key}" '(.plugins // {})[$key] // [] | length > 0' "${meta}" >/dev/null 2>&1; then
            continue
        fi
        if ! claude plugin install "${key}" >/dev/null 2>&1; then
            echo "warning: failed to install plugin ${key}; it may be unavailable this session" >&2
        fi
    done

    return 0
}
