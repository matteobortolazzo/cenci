#!/bin/bash
# Codex config.toml seeding for the sandbox home volume.
#
# Sourced by entrypoint.sh (in the container, via the copy baked at
# /usr/local/bin/lib/) and by the test harness
# (sandbox/tests/codex-config.test.sh) on the host, so this logic lives in
# exactly one place.
#
# Codex CLI renders its status line natively from `[tui] status_line` in
# ~/.codex/config.toml — no external tool needed (unlike Claude Code, whose
# statusLine is seeded in migrate-settings.sh and rendered by the baked-in
# ccline binary).

# The seeded status line: model, cwd, context left, tokens, and the 5-hour /
# weekly rate-limit gauges.
CODEX_TUI_SETTINGS='[tui]
status_line = ["model-with-reasoning", "current-dir", "context-usage", "used-tokens", "five-hour-limit", "weekly-limit"]'

CODEX_UPDATE_SETTING='check_for_update_on_startup = false'

# seed_codex_config <config-file>: create <config-file> with the [tui] block,
# or append the block to an existing file that has neither a [tui] table nor a
# status_line key. A file with either is left untouched: a second [tui] table
# would be invalid TOML, and a present one means the user already configured
# the TUI. Idempotent.
seed_codex_config() {
    local config_file="$1"

    if [[ ! -f "${config_file}" ]]; then
        mkdir -p "$(dirname "${config_file}")"
        printf '%s\n\n%s\n' "${CODEX_UPDATE_SETTING}" "${CODEX_TUI_SETTINGS}" > "${config_file}"
        return 0
    fi

    if grep -Eq '^[[:space:]]*check_for_update_on_startup[[:space:]]*=' "${config_file}"; then
        sed -E 's/^[[:space:]]*check_for_update_on_startup[[:space:]]*=.*/check_for_update_on_startup = false/' \
            "${config_file}" >"${config_file}.cenci-tmp"
        mv "${config_file}.cenci-tmp" "${config_file}"
    else
        {
            printf '%s\n\n' "${CODEX_UPDATE_SETTING}"
            cat "${config_file}"
        } >"${config_file}.cenci-tmp"
        mv "${config_file}.cenci-tmp" "${config_file}"
    fi

    if grep -Eq '^[[:space:]]*(\[tui\]|status_line[[:space:]]*=)' "${config_file}"; then
        return 0
    fi

    printf '\n%s\n' "${CODEX_TUI_SETTINGS}" >> "${config_file}"
}
