#!/bin/bash
# Persistent, user-local lifecycle for the agent CLIs used in the sandbox.

agent_cli_package() {
    case "${1:-}" in
    claude) printf '%s\n' '@anthropic-ai/claude-code' ;;
    codex) printf '%s\n' '@openai/codex' ;;
    *)
        printf 'agent-cli: unknown agent %q. Valid agents: claude, codex.\n' "${1:-}" >&2
        return 2
        ;;
    esac
}

agent_cli_label() {
    case "${1:-}" in
    claude) printf '%s\n' 'Claude Code' ;;
    codex) printf '%s\n' 'Codex' ;;
    *) agent_cli_package "${1:-}" >/dev/null ;;
    esac
}

agent_cli_executable() {
    agent_cli_package "${1:-}" >/dev/null || return $?
    printf '%s/.local/bin/%s\n' "${CENCI_SANDBOX_AGENT_HOME:-/home/dev}" "$1"
}

install_agent_cli_unlocked() {
    local agent="${1:-}" package label executable prefix had_executable=0
    package="$(agent_cli_package "${agent}")" || return $?
    label="$(agent_cli_label "${agent}")" || return $?
    executable="$(agent_cli_executable "${agent}")" || return $?
    prefix="${CENCI_SANDBOX_AGENT_HOME:-/home/dev}/.local"
    [[ -x "${executable}" ]] && had_executable=1

    export HOME="${CENCI_SANDBOX_AGENT_HOME:-/home/dev}"
    export NPM_CONFIG_PREFIX="${prefix}"
    mkdir -p "${prefix}/bin"
    # Claude Code's package uses postinstall to place its platform-native binary.
    if ! npm install --global "${package}@latest"; then
        if [[ "${had_executable}" -eq 0 ]] || ! "${executable}" --version >/dev/null 2>&1; then
            rm -f "${executable}"
        fi
        printf 'agent-cli: failed to install %s into %s. First launch and explicit updates require network access to npm.\n' \
            "${label}" "${prefix}" >&2
        return 1
    fi
    if [[ ! -x "${executable}" ]]; then
        printf 'agent-cli: npm completed but %s was not created at %s.\n' "${label}" "${executable}" >&2
        return 1
    fi
}

install_agent_cli() {
    local prefix lock_file
    prefix="${CENCI_SANDBOX_AGENT_HOME:-/home/dev}/.local"
    mkdir -p "${prefix}"
    lock_file="${prefix}/.cenci-agent-cli.lock"
    if ! command -v flock >/dev/null 2>&1; then
        echo "agent-cli: flock is required to update the persistent agent package safely" >&2
        return 1
    fi
    (
        if ! flock -x 9; then
            echo "agent-cli: failed to lock the persistent agent package tree" >&2
            exit 1
        fi
        install_agent_cli_unlocked "${1:-}"
    ) 9>"${lock_file}"
}

ensure_agent_cli() {
    local agent="${1:-}" executable label
    executable="$(agent_cli_executable "${agent}")" || return $?
    if [[ -x "${executable}" ]]; then
        return 0
    fi
    label="$(agent_cli_label "${agent}")" || return $?
    printf 'Installing %s into the persistent sandbox home...\n' "${label}"
    install_agent_cli "${agent}"
}

update_agent_cli() {
    local agent="${1:-}" executable label
    executable="$(agent_cli_executable "${agent}")" || return $?
    label="$(agent_cli_label "${agent}")" || return $?
    printf 'Updating %s in the persistent sandbox home...\n' "${label}"
    install_agent_cli "${agent}" || return $?
    "${executable}" --version
}
