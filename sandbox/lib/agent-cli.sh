#!/bin/bash
# Shared, versioned agent CLI lifecycle. This script is only executed by the
# credential-free updater container; workload containers mount its volume ro.

set -o pipefail

agent_cli_package() {
    case "${1:-}" in
    claude) printf '%s\n' '@anthropic-ai/claude-code' ;;
    codex) printf '%s\n' '@openai/codex' ;;
    opencode) printf '%s\n' 'opencode-ai' ;;
    *)
        printf 'agent-cli: unknown agent %q. Valid agents: claude, codex, opencode.\n' "${1:-}" >&2
        return 2
        ;;
    esac
}

agent_cli_label() {
    case "${1:-}" in
    claude) printf '%s\n' 'Claude Code' ;;
    codex) printf '%s\n' 'Codex' ;;
    opencode) printf '%s\n' 'OpenCode' ;;
    *) agent_cli_package "${1:-}" >/dev/null ;;
    esac
}

agent_cli_root() {
    printf '%s\n' "${CENCI_AGENT_CLI_ROOT:-/opt/cenci-agent}"
}

agent_cli_is_exact_semver() {
    [[ "${1:-}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]
}

# The single definition of "populated" -- consulted from the pin-gate
# bypass, the same-version short-circuit, and `status`. A shared predicate
# prevents the three from drifting (sandbox/AGENTS.md #491).
agent_cli_populated() {
    local root="$1" agent="$2"
    [[ -x "${root}/current/node_modules/.bin/${agent}" ]]
}

# Reads a root-level fact file and prints *only* its content (trailing
# newline stripped by the caller's command substitution) -- never a
# warning, per the command-substitution-must-only-print-captured-value rule.
# Absent/unreadable files print nothing; callers default-deny on the result.
agent_cli_read_fact() {
    local path="$1"
    [[ -r "${path}" ]] || return 0
    cat -- "${path}" 2>/dev/null
}

# Atomically writes a root-level state file: mktemp (never $$-derived) ->
# printf -> chmod 0644 -> mv -Tf. The lock-free, read-only `status` reader
# (and #709's Go probe) must never observe a torn or transiently-0600 file.
agent_cli_write_atomic() {
    local path="$1" content="$2" dir state_staging
    dir="$(dirname -- "${path}")" || {
        printf 'agent-cli: failed to resolve directory of %s\n' "${path}" >&2
        return 1
    }
    [[ -n "${dir}" ]] || {
        printf 'agent-cli: empty directory resolved for %s\n' "${path}" >&2
        return 1
    }
    state_staging="$(mktemp "${dir}/.state-XXXXXXXX")" || {
        printf 'agent-cli: failed to create a staging file under %s.\n' "${dir}" >&2
        return 1
    }
    if ! printf '%s\n' "${content}" >"${state_staging}"; then
        printf 'agent-cli: failed to write %s.\n' "${state_staging}" >&2
        rm -f -- "${state_staging}"
        return 1
    fi
    if ! chmod 0644 -- "${state_staging}"; then
        printf 'agent-cli: failed to set permissions on %s.\n' "${state_staging}" >&2
        rm -f -- "${state_staging}"
        return 1
    fi
    if ! mv -Tf -- "${state_staging}" "${path}"; then
        printf 'agent-cli: failed to activate %s.\n' "${path}" >&2
        rm -f -- "${state_staging}"
        return 1
    fi
}

# Stamps ${root}/${name} with the current epoch seconds.
agent_cli_stamp() {
    local root="$1" name="$2" epoch
    epoch="$(date +%s)" || return 1
    agent_cli_write_atomic "${root}/${name}" "${epoch}"
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
    local agent="${1:-}" requested="${2:-}" skip_if_pinned="${3:-0}" package label root resolved version integrity metadata
    local staging release current_target executable lock_integrity pin_value current_version
    package="$(agent_cli_package "${agent}")" || return $?
    label="$(agent_cli_label "${agent}")" || return $?
    root="$(agent_cli_root)"

    # Best-effort sweep of orphaned state-file temps: a SIGKILL between
    # mktemp and mv -Tf inside agent_cli_write_atomic can leave a
    # .state-XXXXXXXX behind forever (no other cleanup path touches it).
    # Safe here specifically because this function runs inside the
    # exclusive flock, so any .state-* present at this point cannot belong
    # to a concurrent writer -- it is guaranteed to be a prior orphan.
    rm -f "${root}"/.state-* 2>/dev/null || true

    # Pin gate: evaluated first -- before any mkdir, any stamp, any npm
    # call -- so a refusal or skip never touches the volume or the
    # registry. Only a bare `update` (no explicit version) is subject to it.
    if [[ -z "${requested}" ]]; then
        pin_value="$(agent_cli_read_fact "${root}/PIN")"
        if agent_cli_is_exact_semver "${pin_value}"; then
            if ! agent_cli_populated "${root}" "${agent}"; then
                # Default-deny/full-reinstall-as-repair: an unpopulated
                # volume can never serve the pin, so fall through to a
                # normal (latest) install rather than refuse forever.
                # PIN is deliberately left untouched -- see the plan's
                # rejected alternatives for why.
                printf 'agent-cli: warning: shared %s CLI volume is pinned to %s but has no executable release; reinstalling the latest version to repair it. The pin no longer matches the installed version -- run agent-cli.sh unpin %s to clear it.\n' \
                    "${agent}" "${pin_value}" "${agent}" >&2
            elif [[ "${skip_if_pinned}" == 1 ]]; then
                printf 'Shared %s volume is pinned to %s; skipping update.\n' "${label}" "${pin_value}"
                return 0
            else
                printf 'agent-cli: shared %s CLI volume is pinned to %s; pass --unpin to clear the pin and update, or --version to change the pin\n' \
                    "${agent}" "${pin_value}" >&2
                return 2
            fi
        fi
    fi

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

    # .last-attempt is stamped before any registry contact so it stays fresh
    # after a failure at any later stage. A stamp-write failure only warns
    # and continues -- it must not block the update itself.
    agent_cli_stamp "${root}" ".last-attempt" \
        || printf 'agent-cli: warning: failed to record %s/.last-attempt.\n' "${root}" >&2

    resolved="$(agent_cli_resolve_metadata "${agent}" "${requested}")" || return $?
    version="$(sed -n '1p' <<<"${resolved}")"
    integrity="$(sed -n '2p' <<<"${resolved}")"
    metadata="$(sed -n '3,$p' <<<"${resolved}")"

    # Same-version short-circuit: a missing/unreadable VERSION or a
    # non-executable current binary can never equal a validated exact
    # semver, so default-deny falls out of this comparison -- no separate
    # "is VERSION present" check is needed.
    current_version="$(agent_cli_read_fact "${root}/current/VERSION")"
    if [[ "${current_version}" == "${version}" ]] && agent_cli_populated "${root}" "${agent}"; then
        if [[ -n "${requested}" ]]; then
            agent_cli_write_atomic "${root}/PIN" "${version}" || {
                printf 'agent-cli: failed to write %s/PIN.\n' "${root}" >&2
                return 1
            }
        fi
        agent_cli_stamp "${root}" ".last-success" \
            || printf 'agent-cli: warning: failed to record %s/.last-success.\n' "${root}" >&2
        printf 'Shared %s already at %s; nothing to install.\n' "${label}" "${version}"
        return 0
    fi

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

    # PIN is only written for an explicit version, on the same success path
    # as the short-circuit's PIN write above. A write failure here is loud
    # (return 1) because the activation itself already succeeded and the
    # caller's requested pin would otherwise be silently dropped.
    if [[ -n "${requested}" ]]; then
        agent_cli_write_atomic "${root}/PIN" "${version}" || {
            printf 'agent-cli: failed to write %s/PIN.\n' "${root}" >&2
            return 1
        }
    fi

    agent_cli_cleanup_versions "${root}"
    agent_cli_stamp "${root}" ".last-success" \
        || printf 'agent-cli: warning: failed to record %s/.last-success.\n' "${root}" >&2
    printf 'Activated %s %s. Running sandboxes keep using the version they started with —\n' "${label}" "${version}"
    printf 'relaunch them to pick up this update.\n'
}

update_agent_cli() {
    local agent="${1:-}" root lock_file arg requested="" skip_if_pinned=0
    agent_cli_package "${agent}" >/dev/null || return $?
    shift || true

    # Strict parsing: an unknown -* token or a second positional exits 2
    # (previously silently discarded). No in-repo caller passes extra args.
    for arg in "$@"; do
        case "${arg}" in
        --skip-if-pinned)
            skip_if_pinned=1
            ;;
        -*)
            printf 'agent-cli: unknown option %q for update.\n' "${arg}" >&2
            return 2
            ;;
        *)
            if [[ -n "${requested}" ]]; then
                printf 'agent-cli: update accepts at most one version argument (unexpected extra %q).\n' "${arg}" >&2
                return 2
            fi
            requested="${arg}"
            ;;
        esac
    done
    if [[ -n "${requested}" ]] && [[ "${skip_if_pinned}" == 1 ]]; then
        # With an explicit version the pin gate never fires, so the flag
        # would be silently inert -- a conflicting-flags usage error instead.
        printf 'agent-cli: --skip-if-pinned cannot be combined with an explicit version.\n' >&2
        return 2
    fi

    root="$(agent_cli_root)"
    mkdir -p "${root}"
    lock_file="${root}/.update.lock"
    if ! command -v flock >/dev/null 2>&1; then
        echo "agent-cli: flock is required to serialize shared CLI updates." >&2
        return 1
    fi
    (
        flock -x 9 || { echo "agent-cli: failed to lock the shared CLI volume." >&2; exit 1; }
        install_agent_cli_unlocked "${agent}" "${requested}" "${skip_if_pinned}"
    ) 9>"${lock_file}"
}

status_agent_cli() {
    local agent="${1:-}" root populated_str version pin last_success last_attempt
    agent_cli_package "${agent}" >/dev/null || return $?
    shift || true
    if [[ $# -gt 0 ]]; then
        printf 'agent-cli: status takes no arguments after the agent name.\n' >&2
        return 2
    fi

    # No mkdir, no flock, no writes: this runs as the non-root `dev` user
    # against a :ro mount, so it must be read-only end to end.
    root="$(agent_cli_root)"
    version="" # only fallback that matters: overwritten below in the "yes" branch

    if agent_cli_populated "${root}" "${agent}"; then
        populated_str=yes
        version="$(agent_cli_read_fact "${root}/current/VERSION")"
        agent_cli_is_exact_semver "${version}" || version=""
    else
        populated_str=no
    fi

    pin="$(agent_cli_read_fact "${root}/PIN")"
    agent_cli_is_exact_semver "${pin}" || pin=""

    last_success="$(agent_cli_read_fact "${root}/.last-success")"
    [[ "${last_success}" =~ ^[0-9]+$ ]] || last_success=""

    last_attempt="$(agent_cli_read_fact "${root}/.last-attempt")"
    [[ "${last_attempt}" =~ ^[0-9]+$ ]] || last_attempt=""

    # Always print all five lines, even on the unpopulated/error path, so a
    # captured value never breaks the key=value framing (default-deny on
    # every value validated whole-string before printing).
    printf 'populated=%s\n' "${populated_str}"
    printf 'version=%s\n' "${version}"
    printf 'pin=%s\n' "${pin}"
    printf 'last_success=%s\n' "${last_success}"
    printf 'last_attempt=%s\n' "${last_attempt}"

    [[ "${populated_str}" == yes ]]
}

unpin_agent_cli() {
    local agent="${1:-}" root lock_file
    agent_cli_package "${agent}" >/dev/null || return $?
    shift || true
    if [[ $# -gt 0 ]]; then
        printf 'agent-cli: unpin takes no arguments after the agent name.\n' >&2
        return 2
    fi

    root="$(agent_cli_root)"
    mkdir -p "${root}" || {
        printf 'agent-cli: failed to create %s\n' "${root}" >&2
        return 1
    }
    lock_file="${root}/.update.lock"
    if ! command -v flock >/dev/null 2>&1; then
        echo "agent-cli: flock is required to serialize shared CLI updates." >&2
        return 1
    fi
    (
        flock -x 9 || { echo "agent-cli: failed to lock the shared CLI volume." >&2; exit 1; }
        # rm -f is already idempotent: exit 0 whether or not PIN existed.
        if ! rm -f -- "${root}/PIN"; then
            printf 'agent-cli: failed to remove %s/PIN.\n' "${root}" >&2
            exit 1
        fi
        printf 'agent-cli: cleared the pin for %s.\n' "${agent}"
    ) 9>"${lock_file}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    command_name="${1:-}"
    shift || true
    case "${command_name}" in
    update) update_agent_cli "$@" ;;
    status) status_agent_cli "$@" ;;
    unpin) unpin_agent_cli "$@" ;;
    *)
        echo "usage: agent-cli.sh update <claude|codex|opencode> [exact-version] [--skip-if-pinned] | status <agent> | unpin <agent>" >&2
        exit 2
        ;;
    esac
fi
