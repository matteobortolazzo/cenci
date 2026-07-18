#!/bin/bash
# OpenCode opencode.json permission/plugin seeding for the sandbox home
# volume.
#
# Sourced by entrypoint.sh (in the container, via the copy baked at
# /usr/local/bin/lib/) and by the test harness
# (sandbox/tests/opencode-config.test.sh) on the host, so this logic lives in
# exactly one place. Mirrors codex-config.sh's create-or-merge conventions,
# but opencode.json is JSON (not TOML), so seeding goes through jq rather
# than grep/sed.

# seed_opencode_config <config-file> <plugin-spec>: create-or-merge
# <config-file> idempotently:
#   * adds `permission: {"*": "allow"}` only when the file has no permission
#     block of its own (a present one means the user already made a boundary
#     choice, possibly stricter than the container-boundary default — never
#     overwrite it);
#   * adds `autoupdate: false` only when the key is absent (unlike Codex's
#     forced-off update check, a user who explicitly opted back in keeps
#     their choice);
#   * appends plugin-spec to the `.plugin` array, deduplicated, so repeat
#     calls never register it twice;
#   * never clobbers any other existing user key.
# A missing file is created fresh. An existing file that isn't valid JSON is
# left untouched — like a user's opencode.json, it holds hand-authored
# settings that a corrupt file should not have overwritten. Idempotent.
seed_opencode_config() {
    local config_file="$1" plugin_spec="$2"

    if [[ ! -f "${config_file}" ]]; then
        mkdir -p "$(dirname "${config_file}")"
        jq -n --arg plugin "${plugin_spec}" \
            '{"permission": {"*": "allow"}, "autoupdate": false, "plugin": [$plugin]}' \
            > "${config_file}"
        return 0
    fi

    jq -e . "${config_file}" >/dev/null 2>&1 || return 0

    if jq --arg plugin "${plugin_spec}" '
        (if has("permission") then . else . + {"permission": {"*": "allow"}} end)
        | (if has("autoupdate") then . else . + {"autoupdate": false} end)
        | .plugin |= (
            (. // []) as $arr
            | if ($arr | index($plugin)) then $arr else $arr + [$plugin] end
        )
    ' "${config_file}" >"${config_file}.cenci-tmp"; then
        mv "${config_file}.cenci-tmp" "${config_file}"
    else
        rm -f "${config_file}.cenci-tmp"
    fi
}
