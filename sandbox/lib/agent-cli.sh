#!/bin/bash
# Shared, versioned agent CLI lifecycle. This script is only executed by the
# credential-free updater container; workload containers mount its volume ro.

set -o pipefail

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

agent_cli_root() {
    printf '%s\n' "${CENCI_AGENT_CLI_ROOT:-/opt/cenci-agent}"
}

agent_cli_is_exact_semver() {
    [[ "${1:-}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]
}

agent_cli_resolve_metadata() {
    local agent="${1:-}" requested="${2:-}" package spec metadata version integrity
    package="$(agent_cli_package "${agent}")" || return $?
    spec="${package}@latest"
    if [[ -n "${requested}" ]]; then
        if ! agent_cli_is_exact_semver "${requested}"; then
            printf 'agent-cli: version %q is not an exact semantic version.\n' "${requested}" >&2
            return 2
        fi
        spec="${package}@${requested}"
    fi

    metadata="$(npm view "${spec}" --json)" || return $?
    if ! version="$(jq -er '.version | select(type == "string")' <<<"${metadata}")" \
        || ! integrity="$(jq -er '.dist.integrity | select(type == "string" and startswith("sha512-"))' <<<"${metadata}")"; then
        echo "agent-cli: registry metadata is missing an exact version or SHA-512 integrity." >&2
        return 1
    fi
    if ! agent_cli_is_exact_semver "${version}" || { [[ -n "${requested}" ]] && [[ "${version}" != "${requested}" ]]; }; then
        printf 'agent-cli: registry resolved unexpected version %q for %q.\n' "${version}" "${requested:-latest}" >&2
        return 1
    fi
    if ! jq -e '.dist.signatures | type == "array" and length > 0 and all(.[]; (.sig | type == "string" and length > 0) and (.keyid | type == "string" and length > 0))' \
        <<<"${metadata}" >/dev/null; then
        echo "agent-cli: registry metadata has no verifiable package signatures." >&2
        return 1
    fi

    printf '%s\n' "${version}" "${integrity}" "${metadata}"
}

agent_cli_verify_codex_provenance() {
    local metadata="$1" version="$2" url attestations
    url="$(jq -er '.dist.attestations.url | select(type == "string")' <<<"${metadata}")" || {
        echo "agent-cli: Codex release has no npm provenance attestation." >&2
        return 1
    }
    case "${url}" in
    https://registry.npmjs.org/-/npm/v1/attestations/*) ;;
    *)
        printf 'agent-cli: refusing unexpected provenance URL %q.\n' "${url}" >&2
        return 1
        ;;
    esac
    attestations="$(curl -fsSL "${url}")" || return $?
    if ! jq -e --arg version "${version}" '
        any(.attestations[]?;
            .predicateType == "https://slsa.dev/provenance/v1"
            and ((.bundle.dsseEnvelope.payload | @base64d | fromjson) as $p
                | $p.subject | any(.[]?; .name == ("pkg:npm/%40openai/codex@" + $version))
                and $p.predicate.buildDefinition.externalParameters.workflow.repository == "https://github.com/openai/codex"))
    ' <<<"${attestations}" >/dev/null; then
        echo "agent-cli: Codex provenance does not match openai/codex." >&2
        return 1
    fi
}

agent_cli_cleanup_versions() {
    local root="$1" current previous dir
    current="$(readlink "${root}/current" 2>/dev/null || true)"
    previous="$(readlink "${root}/previous" 2>/dev/null || true)"
    for dir in "${root}"/versions/*; do
        [[ -d "${dir}" ]] || continue
        if [[ "versions/$(basename "${dir}")" != "${current}" && "versions/$(basename "${dir}")" != "${previous}" ]]; then
            rm -rf -- "${dir}"
        fi
    done
}

install_agent_cli_unlocked() {
    local agent="${1:-}" requested="${2:-}" package label root resolved version integrity metadata
    local staging release current_target executable lock_integrity
    package="$(agent_cli_package "${agent}")" || return $?
    label="$(agent_cli_label "${agent}")" || return $?
    root="$(agent_cli_root)"
    resolved="$(agent_cli_resolve_metadata "${agent}" "${requested}")" || return $?
    version="$(sed -n '1p' <<<"${resolved}")"
    integrity="$(sed -n '2p' <<<"${resolved}")"
    metadata="$(sed -n '3,$p' <<<"${resolved}")"

    mkdir -p "${root}/versions"
    staging="${root}/.staging-${version}-$$"
    release="${root}/versions/${version}-$$"
    rm -rf -- "${staging}"
    trap 'rm -rf -- "${staging:-}"' EXIT
    trap 'rm -rf -- "${staging:-}"; exit 130' HUP INT TERM
    mkdir -p "${staging}"
    printf '{"name":"cenci-agent-cli-stage","private":true}\n' >"${staging}/package.json"

    # Fetch and unpack without executing publisher-controlled lifecycle code.
    npm install --prefix "${staging}" --ignore-scripts --save-exact "${package}@${version}" || return $?
    lock_integrity="$(jq -er --arg package "node_modules/${package}" '.packages[$package].integrity' "${staging}/package-lock.json")" || return $?
    if [[ "${lock_integrity}" != "${integrity}" ]]; then
        echo "agent-cli: installed package integrity differs from resolved registry metadata." >&2
        return 1
    fi

    # npm verifies registry signatures and any available Sigstore provenance.
    npm audit signatures --prefix "${staging}" || return $?
    if [[ "${agent}" == codex ]]; then
        agent_cli_verify_codex_provenance "${metadata}" "${version}" || return $?
    elif ! jq -e '.dist.attestations.provenance' <<<"${metadata}" >/dev/null 2>&1; then
        echo "agent-cli: note: Claude Code publishes registry signatures but no npm provenance; vendor release trust remains." >&2
    fi

    # Lifecycle code is required to materialize the platform-native binary.
    # It runs only now, inside the isolated updater container.
    npm rebuild --prefix "${staging}" "${package}" --foreground-scripts || return $?
    executable="${staging}/node_modules/.bin/${agent}"
    if [[ ! -x "${executable}" ]]; then
        printf 'agent-cli: verified install did not create %s at %s.\n' "${label}" "${executable}" >&2
        return 1
    fi
    "${executable}" --version || {
        printf 'agent-cli: staged %s failed its --version health check.\n' "${label}" >&2
        return 1
    }

    printf '%s\n' "${version}" >"${staging}/VERSION"
    mv -- "${staging}" "${release}"
    trap - EXIT HUP INT TERM

    current_target="$(readlink "${root}/current" 2>/dev/null || true)"
    if [[ -n "${current_target}" ]]; then
        ln -s "${current_target}" "${root}/.previous-$$"
        mv -Tf "${root}/.previous-$$" "${root}/previous"
    fi
    ln -s "versions/$(basename "${release}")" "${root}/.current-$$"
    mv -Tf "${root}/.current-$$" "${root}/current"
    agent_cli_cleanup_versions "${root}"
    printf 'Activated %s %s.\n' "${label}" "${version}"
}

update_agent_cli() {
    local agent="${1:-}" requested="${2:-}" root lock_file
    agent_cli_package "${agent}" >/dev/null || return $?
    root="$(agent_cli_root)"
    mkdir -p "${root}"
    lock_file="${root}/.update.lock"
    if ! command -v flock >/dev/null 2>&1; then
        echo "agent-cli: flock is required to serialize shared CLI updates." >&2
        return 1
    fi
    (
        flock -x 9 || { echo "agent-cli: failed to lock the shared CLI volume." >&2; exit 1; }
        install_agent_cli_unlocked "${agent}" "${requested}"
    ) 9>"${lock_file}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    command_name="${1:-}"
    shift || true
    case "${command_name}" in
    update) update_agent_cli "$@" ;;
    *) echo "usage: agent-cli.sh update <claude|codex> [exact-version]" >&2; exit 2 ;;
    esac
fi
