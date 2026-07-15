#!/bin/bash
# Shared settings migration + plugin provisioning/update helpers for the
# sandbox home volume.
#
# Sourced by entrypoint.sh (in the container), by `cenci-sand
# --update-plugins` (via the copy baked at /usr/local/bin/lib/), and by the
# test harnesses (dev-sandbox/tests/settings-merge.test.sh,
# dev-sandbox/tests/heal-plugins.test.sh) on the host, so this logic lives in
# exactly one place.
#
# What the migration does, in one idempotent pass:
#   * seeds the container-only bypass-mode keys (see entrypoint.sh for why
#     these are safe only inside the container),
#   * enables the current cenci-watch/cenci plugins from the cenci
#     marketplace so coding-agent sessions are visible on the host status bar,
#   * removes the stale pre-rename muxwatch/ccflow plugins and the old
#     claude-tools marketplace, which would otherwise 404 on bootstrap and
#     shadow the renamed plugins,
#   * seeds the ccline status line and the default UI preferences (fullscreen
#     TUI, clear-context-on-plan-accept) when the volume has none of its own.

# Container-only bypass settings. The container boundary is what makes bypass
# mode safe — these must never reach the host ~/.claude/settings.json.
BYPASS_SETTINGS='{"skipDangerousModePermissionPrompt":true,"permissions":{"defaultMode":"bypassPermissions"}}'

# Current marketplace + plugins that make sandbox sessions visible to the host
# cenci daemon.
PLUGIN_SETTINGS='{"extraKnownMarketplaces":{"cenci":{"source":{"source":"github","repo":"matteobortolazzo/cenci"}}},"enabledPlugins":{"cenci-watch@cenci":true,"cenci@cenci":true}}'

# Status line rendered by the CCometixLine binary baked into the base image
# (Dockerfile.base). Seeded only when the volume has no statusLine of its own:
# unlike the bypass keys there is no safety reason to force it, so a user
# customization in the home volume wins.
STATUSLINE_SETTINGS='{"statusLine":{"type":"command","command":"/usr/local/bin/ccline","padding":0}}'

# Default UI preferences for a fresh sandbox session: the flicker-free
# fullscreen renderer, and offering to clear context from the plan-approval
# dialog. Seeded key-by-key only when the volume has none of its own — same
# non-forcing rationale as STATUSLINE_SETTINGS above.
UI_PREFERENCE_SETTINGS='{"tui":"fullscreen","showClearContextOnPlanAccept":true}'

# migrate_settings: read a settings.json object from stdin, write the merged +
# migrated object to stdout. Reads `{}` when given an empty/whitespace input so
# fresh and invalid volumes get seeded too. Idempotent: running it on its own
# output is a no-op.
migrate_settings() {
    jq --argjson bypass "${BYPASS_SETTINGS}" --argjson plugins "${PLUGIN_SETTINGS}" --argjson statusline "${STATUSLINE_SETTINGS}" --argjson uiprefs "${UI_PREFERENCE_SETTINGS}" '
        (. * $bypass * $plugins)
        | del(.enabledPlugins["muxwatch@claude-tools"], .enabledPlugins["ccflow@claude-tools"])
        | del(.extraKnownMarketplaces["claude-tools"])
        | if has("statusLine") then . else . + $statusline end
        | reduce ($uiprefs | to_entries[]) as $pref (.; if has($pref.key) then . else . + {($pref.key): $pref.value} end)
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
# `claude` binary (Codex-agent containers mount none) or an offline/failed
# CLI call just warns to stderr and returns 0.
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

# update_plugins <plugins-dir> <marketplace-name> <ttl-minutes> <plugin>...
#
# provision_plugins only installs what's missing — it never version-checks, so
# an existing home volume would keep stale plugins forever while the
# marketplace auto-bumps on every push to main. This refreshes the marketplace
# clone (`claude plugin marketplace update`, the only network step), then calls
# `claude plugin update` only for plugins whose installed version differs from
# the refreshed clone's marketplace.json — so a current volume costs exactly
# one git pull, and nothing else.
#
# The whole pass is TTL-gated on a stamp file so rapid stop/start cycles stay
# quiet: <ttl-minutes> 0 forces it (the manual `cenci-sand --update-plugins`
# path). The stamp is touched even when the refresh fails, so an offline boot
# doesn't retry on every restart within the window. Same guarantee as
# provisioning: failures warn to stderr and never block container start.
update_plugins() {
    local plugins_dir="$1" marketplace_name="$2" ttl_minutes="$3"
    shift 3

    command -v claude >/dev/null 2>&1 || return 0

    local stamp="${plugins_dir}/.cenci-sand-update-stamp"
    if [[ "${ttl_minutes}" -gt 0 && -f "${stamp}" ]] \
        && [[ -n "$(find "${stamp}" -mmin "-${ttl_minutes}" 2>/dev/null)" ]]; then
        return 0
    fi

    if ! claude plugin marketplace update "${marketplace_name}" >/dev/null 2>&1; then
        echo "warning: failed to refresh marketplace ${marketplace_name}; plugins may be stale this session" >&2
    fi
    touch "${stamp}"

    local manifest="${plugins_dir}/marketplaces/${marketplace_name}/.claude-plugin/marketplace.json"
    local meta="${plugins_dir}/installed_plugins.json"
    jq -e . "${manifest}" >/dev/null 2>&1 || return 0
    jq -e . "${meta}" >/dev/null 2>&1 || return 0

    local plugin key desired
    for plugin in "$@"; do
        key="${plugin}@${marketplace_name}"
        desired="$(jq -r --arg name "${plugin}" '[.plugins[]? | select(.name == $name) | .version][0] // empty' "${manifest}")"
        [[ -n "${desired}" ]] || continue
        # Not installed at all → provision_plugins' job, not ours.
        jq -e --arg key "${key}" '(.plugins // {})[$key] // [] | length > 0' "${meta}" >/dev/null 2>&1 || continue
        # Any record already at the marketplace version → up to date.
        if jq -e --arg key "${key}" --arg v "${desired}" '(.plugins // {})[$key] // [] | any(.version == $v)' "${meta}" >/dev/null 2>&1; then
            continue
        fi
        if ! claude plugin update "${key}" >/dev/null 2>&1; then
            echo "warning: failed to update plugin ${key}; it may be stale this session" >&2
        fi
    done

    return 0
}

# provision_codex_plugins <codex-dir> <marketplace-name> <marketplace-repo> <plugin>...
#
# Codex keeps its own marketplace registry and plugin cache under ~/.codex;
# Claude's installed_plugins.json and cache are not shared with it. Register the
# marketplace and install only missing plugins through the native Codex CLI.
# Every failure is deliberately non-fatal so an offline marketplace cannot
# prevent the sandbox from starting.
provision_codex_plugins() {
    local codex_dir="$1" marketplace_name="$2" marketplace_repo="$3"
    shift 3

    command -v codex >/dev/null 2>&1 || return 0
    mkdir -p "${codex_dir}" || {
        echo "warning: failed to create Codex config directory ${codex_dir}; plugins may be unavailable this session" >&2
        return 0
    }

    local marketplaces=""
    if ! marketplaces="$(codex plugin marketplace list 2>/dev/null)"; then
        echo "warning: failed to list Codex marketplaces; attempting to provision ${marketplace_name}" >&2
    fi
    if ! grep -Eq "^${marketplace_name}[[:space:]]" <<< "${marketplaces}"; then
        if ! codex plugin marketplace add "${marketplace_repo}" >/dev/null 2>&1; then
            echo "warning: failed to add Codex marketplace ${marketplace_repo}; plugins may be unavailable this session" >&2
        fi
    fi

    local installed=""
    if ! installed="$(codex plugin list 2>/dev/null)"; then
        echo "warning: failed to list Codex plugins; attempting to provision required plugins" >&2
    fi

    local plugin key
    for plugin in "$@"; do
        key="${plugin}@${marketplace_name}"
        if grep -Eq "^${key}[[:space:]]+installed" <<< "${installed}"; then
            continue
        fi
        if ! codex plugin add "${key}" >/dev/null 2>&1; then
            echo "warning: failed to install Codex plugin ${key}; it may be unavailable this session" >&2
        fi
    done

    return 0
}

# update_codex_plugins <codex-dir> <marketplace-name> <ttl-minutes> <plugin>...
#
# Refresh the Codex marketplace snapshot, then idempotently re-add each
# installed required plugin so its versioned cache follows the new snapshot.
# A shared 30-minute-style stamp prevents network work on rapid restarts; ttl 0
# forces a refresh. Missing plugins are provisioned by provision_codex_plugins,
# which callers run first for both normal startup and forced updates.
update_codex_plugins() {
    local codex_dir="$1" marketplace_name="$2" ttl_minutes="$3"
    shift 3

    command -v codex >/dev/null 2>&1 || return 0

    local stamp="${codex_dir}/.cenci-sand-update-stamp"
    if [[ "${ttl_minutes}" -gt 0 && -f "${stamp}" ]] \
        && [[ -n "$(find "${stamp}" -mmin "-${ttl_minutes}" 2>/dev/null)" ]]; then
        return 0
    fi

    if ! codex plugin marketplace upgrade "${marketplace_name}" >/dev/null 2>&1; then
        echo "warning: failed to refresh Codex marketplace ${marketplace_name}; plugins may be stale this session" >&2
    fi
    if ! touch "${stamp}"; then
        echo "warning: failed to record Codex plugin refresh time; startup will retry next time" >&2
    fi

    local installed=""
    if ! installed="$(codex plugin list 2>/dev/null)"; then
        echo "warning: failed to list Codex plugins after refreshing ${marketplace_name}" >&2
        return 0
    fi

    local plugin key
    for plugin in "$@"; do
        key="${plugin}@${marketplace_name}"
        grep -Eq "^${key}[[:space:]]+installed" <<< "${installed}" || continue
        if ! codex plugin add "${key}" >/dev/null 2>&1; then
            echo "warning: failed to refresh Codex plugin ${key}; it may be stale this session" >&2
        fi
    done

    return 0
}
