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

    mkdir -p "${root}/versions" || {
        printf 'agent-cli: failed to create %s/versions.\n' "${root}" >&2
        return 1
    }
    # Workload containers traverse this tree as a non-root user with the
    # volume mounted read-only, so world read/execute must not depend on the
    # updater's umask.
    chmod 0755 "${root}" "${root}/versions" || {
        printf 'agent-cli: failed to make %s world-traversable.\n' "${root}" >&2
        return 1
    }
    # Names must not derive from $$: the script always runs as the updater
    # container's PID-1 shell, so $$ collides across runs and a same-version
    # re-update would mv its staging tree INSIDE the existing release
    # directory, leaving `current` unchanged while reporting success.
    staging="$(mktemp -d "${root}/.staging-${version}-XXXXXXXX")" || {
        printf 'agent-cli: failed to create a staging directory under %s.\n' "${root}" >&2
        return 1
    }
    trap 'rm -rf -- "${staging:-}"' EXIT
    trap 'rm -rf -- "${staging:-}"; exit 130' HUP INT TERM
    printf '{"name":"cenci-agent-cli-stage","private":true}\n' >"${staging}/package.json" || {
        printf 'agent-cli: failed to write %s/package.json.\n' "${staging}" >&2
        return 1
    }

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

    printf '%s\n' "${version}" >"${staging}/VERSION" || {
        printf 'agent-cli: failed to write %s/VERSION.\n' "${staging}" >&2
        return 1
    }
    # mktemp creates the staging directory 0700; grant world read (and execute
    # where already executable) so non-root workloads can run the release, and
    # drop group/other write to keep the shared tree read-only for them.
    chmod -R a+rX,go-w -- "${staging}" || {
        printf 'agent-cli: failed to normalize permissions on staged %s.\n' "${label}" >&2
        return 1
    }
    # mktemp guarantees a fresh, empty release directory even for a
    # same-version re-update; rename(2) via mv -T atomically replaces the
    # empty placeholder with the staged tree instead of nesting into it.
    release="$(mktemp -d "${root}/versions/${version}-XXXXXXXX")" || {
        printf 'agent-cli: failed to create a release directory under %s/versions.\n' "${root}" >&2
        return 1
    }
    # The cleanup trap stays armed through this move: if it fails, staging is
    # still on disk under its original name and must be reaped on exit. Only
    # disarm once the release directory is verifiably in place.
    if ! mv -T -- "${staging}" "${release}"; then
        printf 'agent-cli: failed to activate %s release at %s.\n' "${label}" "${release}" >&2
        rmdir -- "${release}" 2>/dev/null
        return 1
    fi
    trap - EXIT HUP INT TERM

    current_target="$(readlink "${root}/current" 2>/dev/null || true)"
    if [[ -n "${current_target}" ]]; then
        rm -f -- "${root}/.previous-$$"
        if ! ln -s "${current_target}" "${root}/.previous-$$"; then
            printf 'agent-cli: failed to stage previous-version symlink for %s.\n' "${label}" >&2
            return 1
        fi
        if ! mv -Tf "${root}/.previous-$$" "${root}/previous"; then
            printf 'agent-cli: failed to activate previous-version symlink for %s.\n' "${label}" >&2
            rm -f -- "${root}/.previous-$$"
            return 1
        fi
    fi
    rm -f -- "${root}/.current-$$"
    if ! ln -s "versions/$(basename "${release}")" "${root}/.current-$$"; then
        printf 'agent-cli: failed to stage current-version symlink for %s.\n' "${label}" >&2
        return 1
    fi
    if ! mv -Tf "${root}/.current-$$" "${root}/current"; then
        printf 'agent-cli: failed to activate current-version symlink for %s.\n' "${label}" >&2
        rm -f -- "${root}/.current-$$"
        return 1
    fi

    if [[ ! -x "${root}/current/node_modules/.bin/${agent}" ]]; then
        printf 'agent-cli: activated %s does not resolve to an executable at %s/current/node_modules/.bin/%s.\n' \
            "${label}" "${root}" "${agent}" >&2
        return 1
    fi

    agent_cli_cleanup_versions "${root}"
    printf 'Activated %s %s. Running sandboxes keep using the version they started with —\n' "${label}" "${version}"
    printf 'relaunch them to pick up this update.\n'
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
