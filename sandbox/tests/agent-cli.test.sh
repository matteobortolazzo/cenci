#!/usr/bin/env bash
# Host-runnable tests for the shared, versioned agent CLI lifecycle.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILURES=0
PASSES=0
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

make_fakes() {
    mkdir -p "${BIN}"
    cat >"${BIN}/npm" <<'EOF'
#!/bin/bash
set -u
printf '%s\n' "$*" >>"${CALL_LOG}"
[[ -z "${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "${CENCI_PARENT_SECRET}" >>"${CALL_LOG}"
case "${1:-}" in
view)
    # The concurrency marker lives here (not in install)) because npm view is
    # called from agent_cli_resolve_metadata *inside* the flock, on every
    # attempt including one that later short-circuits before ever reaching
    # `npm install` -- so this is the critical section that must still prove
    # two concurrent same-version updates never overlap (see agent-cli.sh's
    # flock case and the plan's Alternatives Considered).
    if [[ -n "${NPM_CONCURRENCY_MARKER:-}" ]]; then
        if ! mkdir "${NPM_CONCURRENCY_MARKER}" 2>/dev/null; then : >"${NPM_OVERLAP}"; fi
        sleep 0.2
        rmdir "${NPM_CONCURRENCY_MARKER}" 2>/dev/null || true
    fi
    [[ "${NPM_VIEW_FAIL:-0}" -eq 0 ]] || { echo 'registry exploded: original diagnostic' >&2; exit 41; }
    signatures='[{"sig":"signed","keyid":"SHA256:key"}]'
    [[ "${NO_SIGNATURES:-0}" -eq 0 ]] || signatures='[]'
    if [[ "${NO_PROVENANCE:-0}" -eq 1 ]]; then attestations='{}'; else attestations='{"url":"https://registry.npmjs.org/-/npm/v1/attestations/mock","provenance":{"predicateType":"https://slsa.dev/provenance/v1"}}'; fi
    printf '{"version":"%s","dist":{"integrity":"%s","signatures":%s,"attestations":%s}}\n' \
        "${MOCK_VERSION}" "${MOCK_INTEGRITY}" "${signatures}" "${attestations}"
    ;;
install)
    prefix=''
    previous=''
    for arg in "$@"; do
        [[ "${previous}" == --prefix ]] && prefix="${arg}"
        previous="${arg}"
    done
    [[ -n "${prefix}" ]] || exit 64
    mkdir -p "${prefix}/node_modules/@openai/codex" "${prefix}/node_modules/@anthropic-ai/claude-code" "${prefix}/node_modules/opencode-ai" "${prefix}/node_modules/.bin"
    printf '{"packages":{"node_modules/@openai/codex":{"integrity":"%s"},"node_modules/@anthropic-ai/claude-code":{"integrity":"%s"},"node_modules/opencode-ai":{"integrity":"%s"}}}\n' \
        "${LOCK_INTEGRITY:-${MOCK_INTEGRITY}}" "${LOCK_INTEGRITY:-${MOCK_INTEGRITY}}" "${LOCK_INTEGRITY:-${MOCK_INTEGRITY}}" >"${prefix}/package-lock.json"
    ;;
audit)
    [[ "${NPM_AUDIT_FAIL:-0}" -eq 0 ]] || { echo 'signature verification failed' >&2; exit 42; }
    ;;
rebuild)
    prefix=''
    previous=''
    for arg in "$@"; do
        [[ "${previous}" == --prefix ]] && prefix="${arg}"
        previous="${arg}"
    done
    if [[ -n "${REBUILD_WAIT_FILE:-}" ]]; then
        : >"${REBUILD_WAIT_FILE}.started"
        while [[ ! -e "${REBUILD_WAIT_FILE}" ]]; do sleep 0.05; done
    fi
    agent=claude
    [[ "$*" == *'@openai/codex'* ]] && agent=codex
    [[ "$*" == *'opencode-ai'* ]] && agent=opencode
    if [[ "${HEALTH_FAIL:-0}" -eq 1 ]]; then status=19; else status=0; fi
    printf '#!/bin/sh\necho "%s"\nexit %s\n' "${MOCK_VERSION}" "${status}" >"${prefix}/node_modules/.bin/${agent}"
    chmod +x "${prefix}/node_modules/.bin/${agent}"
    ;;
*) exit 64 ;;
esac
EOF
    chmod +x "${BIN}/npm"

    cat >"${BIN}/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "${ATTESTATION_JSON}"
EOF
    chmod +x "${BIN}/curl"
}

make_attestation() {
    local version="$1" payload
    payload="$(printf '{"subject":[{"name":"pkg:npm/%%40openai/codex@%s"}],"predicate":{"buildDefinition":{"externalParameters":{"workflow":{"repository":"https://github.com/openai/codex"}}}}}' "${version}" | base64 | tr -d '\n')"
    printf '{"attestations":[{"predicateType":"https://slsa.dev/provenance/v1","bundle":{"dsseEnvelope":{"payload":"%s"}}}]}\n' "${payload}"
}

new_case() {
    CASE_DIR="${WORK}/$1"
    ROOT_DIR="${CASE_DIR}/volume"
    BIN="${CASE_DIR}/bin"
    CALL_LOG="${CASE_DIR}/calls.log"
    MOCK_VERSION="${2:-1.2.3}"
    MOCK_INTEGRITY='sha512-YWJjZA=='
    mkdir -p "${ROOT_DIR}"
    : >"${CALL_LOG}"
    ATTESTATION_JSON="$(make_attestation "${MOCK_VERSION}")"
    USE_ATTESTATION_OVERRIDE=0
    make_fakes
}

run_update() {
    local agent="$1" version="${2:-}"
    shift 2 2>/dev/null || shift "$#"
    # Any remaining args ("$@") are passed straight through after the
    # resolved version -- e.g. --skip-if-pinned, or a second positional/bogus
    # flag for the strict-parsing cases -- so this one helper covers both the
    # existing update flows and the new pin/parse-error cases without a
    # second npm-mock-wired helper.
    if [[ "${USE_ATTESTATION_OVERRIDE:-0}" -eq 0 ]]; then
        ATTESTATION_JSON="$(make_attestation "${MOCK_VERSION}")"
    fi
    env -i PATH="${BIN}:/usr/bin:/bin" CALL_LOG="${CALL_LOG}" \
        CENCI_AGENT_CLI_ROOT="${ROOT_DIR}" MOCK_VERSION="${MOCK_VERSION}" \
        MOCK_INTEGRITY="${MOCK_INTEGRITY}" ATTESTATION_JSON="${ATTESTATION_JSON}" \
        NO_PROVENANCE="${NO_PROVENANCE:-0}" NO_SIGNATURES="${NO_SIGNATURES:-0}" \
        NPM_VIEW_FAIL="${NPM_VIEW_FAIL:-0}" NPM_AUDIT_FAIL="${NPM_AUDIT_FAIL:-0}" \
        LOCK_INTEGRITY="${LOCK_INTEGRITY:-}" HEALTH_FAIL="${HEALTH_FAIL:-0}" \
        NPM_CONCURRENCY_MARKER="${NPM_CONCURRENCY_MARKER:-}" NPM_OVERLAP="${NPM_OVERLAP:-}" \
        REBUILD_WAIT_FILE="${REBUILD_WAIT_FILE:-}" \
        bash "${ROOT}/sandbox/lib/agent-cli.sh" update "${agent}" ${version:+"${version}"} "$@"
}

# run_agent_cli: minimal-env invocation for status/unpin/usage, passing only
# PATH + CENCI_AGENT_CLI_ROOT (no CALL_LOG, no npm mock wiring at all). This
# proves structurally -- not just by assertion -- that these subcommands
# never need the registry: if production code ever called npm from status or
# unpin, the unset CALL_LOG in the mock's `printf >>"${CALL_LOG}"` would fail
# loudly instead of silently succeeding.
run_agent_cli() {
    env -i PATH="${BIN}:/usr/bin:/bin" CENCI_AGENT_CLI_ROOT="${ROOT_DIR}" \
        bash "${ROOT}/sandbox/lib/agent-cli.sh" "$@"
}

echo "agent-cli.test.sh"

echo "case: package allowlist and exact-version validation"
new_case validation
if [[ "$(bash -c 'source "$1"; agent_cli_package claude' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == '@anthropic-ai/claude-code' ]] \
    && [[ "$(bash -c 'source "$1"; agent_cli_package codex' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == '@openai/codex' ]] \
    && ! run_update codex latest >/dev/null 2>&1; then pass; else fail "package or exact-version allowlist failed"; fi

echo "case: OpenCode package/label resolve for the shared updater (#490)"
if [[ "$(bash -c 'source "$1"; agent_cli_package opencode' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == 'opencode-ai' ]] \
    && [[ "$(bash -c 'source "$1"; agent_cli_label opencode' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == 'OpenCode' ]]; then pass; else fail "opencode package or label resolution failed"; fi

echo "case: OpenCode takes the vendor-trust (non-provenance) branch, not Codex's hard gate"
new_case opencode-no-provenance
NO_PROVENANCE=1
if OPENCODE_OUTPUT="$(run_update opencode 2>&1)" \
    && [[ "${OPENCODE_OUTPUT}" == *'vendor release trust remains'* ]]; then pass; else fail "OpenCode did not take Claude's vendor-trust branch: ${OPENCODE_OUTPUT:-<update failed>}"; fi
unset NO_PROVENANCE

echo "case: latest resolves to exact version and SHA-512 before activation"
new_case latest 9.8.7
if run_update codex >/dev/null \
    && [[ "$(cat "${ROOT_DIR}/current/VERSION")" == 9.8.7 ]] \
    && grep -Fq -- 'view @openai/codex@latest --json' "${CALL_LOG}"; then pass; else fail "latest was not resolved and activated"; fi

echo "case: exact version supports controlled rollback"
new_case exact 1.4.0
if run_update claude 1.4.0 >/dev/null \
    && [[ "$(cat "${ROOT_DIR}/current/VERSION")" == 1.4.0 ]] \
    && grep -Fq -- '@anthropic-ai/claude-code@1.4.0' "${CALL_LOG}"; then pass; else fail "exact version was not honored"; fi

echo "case: signatures and lockfile integrity are mandatory"
new_case signatures
NO_SIGNATURES=1
if ! run_update codex >/dev/null 2>&1 && [[ ! -e "${ROOT_DIR}/current" ]]; then pass; else fail "unsigned release was accepted"; fi
unset NO_SIGNATURES
new_case integrity
LOCK_INTEGRITY='sha512-wrong'
if ! run_update codex >/dev/null 2>&1 && [[ ! -e "${ROOT_DIR}/current" ]]; then pass; else fail "integrity mismatch was accepted"; fi
unset LOCK_INTEGRITY

echo "case: Codex provenance must match openai/codex"
new_case provenance
ATTESTATION_JSON='{"attestations":[]}'
USE_ATTESTATION_OVERRIDE=1
if ! run_update codex >/dev/null 2>&1 && [[ ! -e "${ROOT_DIR}/current" ]]; then pass; else fail "invalid Codex provenance was accepted"; fi

echo "case: Claude permits missing provenance but reports residual trust"
new_case claude-no-provenance
NO_PROVENANCE=1
if CLAUDE_OUTPUT="$(run_update claude 2>&1)" \
    && [[ "${CLAUDE_OUTPUT}" == *'vendor release trust remains'* ]]; then pass; else fail "Claude provenance fallback was not explicit"; fi
unset NO_PROVENANCE

echo "case: failed staged health check leaves prior version active"
new_case health
run_update codex >/dev/null
OLD_TARGET="$(readlink "${ROOT_DIR}/current")"
MOCK_VERSION=1.2.4
HEALTH_FAIL=1
if ! run_update codex >/dev/null 2>&1 && [[ "$(readlink "${ROOT_DIR}/current")" == "${OLD_TARGET}" ]]; then pass; else fail "failed health check changed current"; fi
unset HEALTH_FAIL

echo "case: interrupted update leaves prior version active"
new_case interrupted
run_update codex >/dev/null
OLD_TARGET="$(readlink "${ROOT_DIR}/current")"
MOCK_VERSION=1.2.4
REBUILD_WAIT_FILE="${CASE_DIR}/continue"
run_update codex >/dev/null 2>&1 & update_pid=$!
for _ in {1..100}; do [[ -e "${REBUILD_WAIT_FILE}.started" ]] && break; sleep 0.02; done
kill -TERM "${update_pid}" 2>/dev/null || true
wait "${update_pid}" 2>/dev/null || true
if [[ "$(readlink "${ROOT_DIR}/current")" == "${OLD_TARGET}" ]]; then pass; else fail "interrupted update changed current"; fi
unset REBUILD_WAIT_FILE

echo "case: activated release tree is world-traversable for read-only workloads"
# Workload containers run as a non-root user against the ro-mounted volume, so
# every path from the root to the binary must grant other-read (and execute on
# directories and the CLI itself) regardless of the updater's umask.
new_case world-readable 3.1.0
run_update codex >/dev/null
RELEASE_DIR="${ROOT_DIR}/$(readlink "${ROOT_DIR}/current")"
if [[ "$(find "${ROOT_DIR}" "${ROOT_DIR}/versions" "${RELEASE_DIR}" -maxdepth 0 -perm -005 | wc -l)" -eq 3 ]] \
    && [[ -n "$(find "${RELEASE_DIR}/VERSION" -maxdepth 0 -perm -004)" ]] \
    && [[ -n "$(find "${RELEASE_DIR}/node_modules/.bin/codex" -maxdepth 0 -perm -005)" ]]; then
    pass
else
    fail "activated release tree is not world-readable/traversable"
fi

echo "case: activation retains exactly current and previous releases"
new_case retention 1.0.0
run_update codex >/dev/null
MOCK_VERSION=1.1.0 run_update codex >/dev/null
MOCK_VERSION=1.2.0 run_update codex >/dev/null
if [[ "$(find "${ROOT_DIR}/versions" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 2 ]] \
    && [[ -L "${ROOT_DIR}/previous" ]]; then pass; else fail "release retention is not current plus previous"; fi

echo "case: real flock serializes concurrent writers"
new_case locking
NPM_CONCURRENCY_MARKER="${CASE_DIR}/critical"
NPM_OVERLAP="${CASE_DIR}/overlap"
run_update codex >/dev/null 2>&1 & p1=$!
run_update codex >/dev/null 2>&1 & p2=$!
wait "${p1}"; s1=$?
wait "${p2}"; s2=$?
if [[ "${s1}" -eq 0 && "${s2}" -eq 0 && ! -e "${NPM_OVERLAP}" ]]; then pass; else fail "concurrent updates overlapped"; fi
unset NPM_CONCURRENCY_MARKER NPM_OVERLAP

echo "case: original registry stderr propagates"
new_case diagnostics
NPM_VIEW_FAIL=1
if OUTPUT="$(run_update codex 2>&1)"; then
    fail "registry failure unexpectedly succeeded"
elif [[ "${OUTPUT}" == *'registry exploded: original diagnostic'* ]]; then
    pass
else
    fail "original npm diagnostic was hidden"
fi
unset NPM_VIEW_FAIL

echo "case: parent-only sentinel secret is scrubbed from updater environment"
new_case secrets
CENCI_PARENT_SECRET='parent-only-sentinel' run_update codex >/dev/null
if ! grep -Fq 'parent-only-sentinel' "${CALL_LOG}"; then pass; else fail "parent secret reached updater"; fi

echo "case: failed activation reports failure and leaves no current symlink"
new_case activation-failure
# A regular file where "versions/" needs to be a directory makes the
# activation sequence's first mkdir -p fail, before anything is staged.
: >"${ROOT_DIR}/versions"
if OUTPUT="$(run_update codex 2>&1)"; then
    fail "activation with an unwritable versions path unexpectedly succeeded"
elif [[ ! -e "${ROOT_DIR}/current" ]] && [[ "${OUTPUT}" == *'failed to create'*'versions'* ]]; then
    pass
else
    fail "failed activation did not report failure cleanly: ${OUTPUT}"
fi

echo "case: same-version re-update from one PID atomically rotates current (missing VERSION forces no short-circuit)"
# In the updater container the script is always the PID-1 shell, so release
# names must not derive from $$: a same-version re-update would collide with
# the existing release directory, nest into it, and leave current unchanged.
# Sourcing the library and updating twice in one shell reproduces that PID
# reuse on the host. The `rm -f current/VERSION` between the two updates is
# load-bearing post-short-circuit: without it, the second update_agent_cli
# call would resolve the same version against an executable `current` and
# short-circuit (no rotation at all), silently making this regression pass
# for the wrong reason. Removing VERSION also doubles as the AC's missing-
# VERSION no-short-circuit case: a full reinstall must still occur.
new_case samepid 5.5.5
# shellcheck disable=SC2016  # $1 and ${CENCI_AGENT_CLI_ROOT} must expand in the child bash, not here
TARGETS="$(env -i PATH="${BIN}:/usr/bin:/bin" CALL_LOG="${CALL_LOG}" \
    CENCI_AGENT_CLI_ROOT="${ROOT_DIR}" MOCK_VERSION="5.5.5" \
    MOCK_INTEGRITY="${MOCK_INTEGRITY}" ATTESTATION_JSON="$(make_attestation 5.5.5)" \
    bash -c 'source "$1" \
        && update_agent_cli codex >/dev/null && readlink "${CENCI_AGENT_CLI_ROOT}/current" \
        && rm -f "${CENCI_AGENT_CLI_ROOT}/current/VERSION" \
        && update_agent_cli codex >/dev/null && readlink "${CENCI_AGENT_CLI_ROOT}/current" \
        && readlink "${CENCI_AGENT_CLI_ROOT}/previous"' _ "${ROOT}/sandbox/lib/agent-cli.sh")" \
    || fail "same-version re-update from a single PID failed"
FIRST_TARGET="$(sed -n '1p' <<<"${TARGETS}")"
SECOND_TARGET="$(sed -n '2p' <<<"${TARGETS}")"
PREVIOUS_TARGET="$(sed -n '3p' <<<"${TARGETS}")"
if [[ -n "${FIRST_TARGET}" && -n "${SECOND_TARGET}" \
    && "${SECOND_TARGET}" != "${FIRST_TARGET}" \
    && "${PREVIOUS_TARGET}" == "${FIRST_TARGET}" \
    && -x "${ROOT_DIR}/current/node_modules/.bin/codex" \
    && -d "${ROOT_DIR}/${PREVIOUS_TARGET}" ]]; then
    pass
else
    fail "same-version re-update did not rotate current atomically (first=${FIRST_TARGET}, second=${SECOND_TARGET}, previous=${PREVIOUS_TARGET})"
fi

echo "case: status on a populated volume prints all five facts (#708 case 1)"
new_case status-populated 1.2.3
run_update codex >/dev/null
STATUS_OUT="$(run_agent_cli status codex)"
STATUS_EXIT=$?
STATUS_LINES="$(wc -l <<<"${STATUS_OUT}")"
if [[ "${STATUS_EXIT}" -eq 0 ]] \
    && [[ "${STATUS_LINES}" -eq 5 ]] \
    && grep -Fxq 'populated=yes' <<<"${STATUS_OUT}" \
    && grep -Fxq 'version=1.2.3' <<<"${STATUS_OUT}" \
    && grep -Fxq 'pin=' <<<"${STATUS_OUT}" \
    && [[ "$(sed -n 's/^last_success=//p' <<<"${STATUS_OUT}")" =~ ^[0-9]+$ ]] \
    && [[ "$(sed -n 's/^last_attempt=//p' <<<"${STATUS_OUT}")" =~ ^[0-9]+$ ]]; then
    pass
else
    fail "status on a populated volume did not print the expected 5 key=value lines (exit=${STATUS_EXIT}): ${STATUS_OUT}"
fi

echo "case: status on an empty (unpopulated) volume defaults every value to empty (#708 case 2)"
new_case status-empty
STATUS_OUT="$(run_agent_cli status codex)"
STATUS_EXIT=$?
STATUS_LINES="$(wc -l <<<"${STATUS_OUT}")"
if [[ "${STATUS_EXIT}" -eq 1 ]] \
    && [[ "${STATUS_LINES}" -eq 5 ]] \
    && grep -Fxq 'populated=no' <<<"${STATUS_OUT}" \
    && grep -Fxq 'version=' <<<"${STATUS_OUT}" \
    && grep -Fxq 'pin=' <<<"${STATUS_OUT}" \
    && grep -Fxq 'last_success=' <<<"${STATUS_OUT}" \
    && grep -Fxq 'last_attempt=' <<<"${STATUS_OUT}"; then
    pass
else
    fail "status on an empty volume did not exit 1 with populated=no and every other value empty (exit=${STATUS_EXIT}): ${STATUS_OUT}"
fi

echo "case: status treats a non-executable release binary as unpopulated, distinct from a missing one (#708 case 3)"
new_case status-non-executable
run_update codex >/dev/null
chmod -x "${ROOT_DIR}/current/node_modules/.bin/codex"
STATUS_OUT="$(run_agent_cli status codex)"
STATUS_EXIT=$?
STATUS_LINES="$(wc -l <<<"${STATUS_OUT}")"
if [[ "${STATUS_EXIT}" -eq 1 ]] && [[ "${STATUS_LINES}" -eq 5 ]] && grep -Fxq 'populated=no' <<<"${STATUS_OUT}"; then
    pass
else
    fail "status did not report populated=no for a non-executable release binary (exit=${STATUS_EXIT}): ${STATUS_OUT}"
fi

echo "case: status on an unknown agent exits 2 with the allowlist diagnostic (#708 case 4)"
new_case status-unknown
STATUS_UNKNOWN_OUT="$(run_agent_cli status bogus-agent 2>&1 1>/dev/null)"
STATUS_UNKNOWN_EXIT=$?
if [[ "${STATUS_UNKNOWN_EXIT}" -eq 2 ]] && [[ "${STATUS_UNKNOWN_OUT}" == *'unknown agent'* ]]; then
    pass
else
    fail "status on an unknown agent did not exit 2 with an unknown-agent diagnostic (exit=${STATUS_UNKNOWN_EXIT}): ${STATUS_UNKNOWN_OUT}"
fi

echo "case: status needs no npm and leaves CALL_LOG byte-identical, proven structurally by a minimal env (#708 case 5)"
new_case status-no-npm
run_update codex >/dev/null
CALL_LOG_BEFORE="$(cat "${CALL_LOG}")"
if run_agent_cli status codex >/dev/null && [[ "$(cat "${CALL_LOG}")" == "${CALL_LOG_BEFORE}" ]]; then
    pass
else
    fail "status either failed under a minimal PATH+CENCI_AGENT_CLI_ROOT env or mutated CALL_LOG, implying an npm call"
fi

echo "case: status defaults last_success to empty on a corrupt multi-line file, still exactly 5 lines (#708 case 6)"
new_case status-corrupt-last-success
run_update codex >/dev/null
printf 'not-an-epoch\nsecond-line\n' >"${ROOT_DIR}/.last-success"
STATUS_OUT="$(run_agent_cli status codex)"
STATUS_LINES="$(wc -l <<<"${STATUS_OUT}")"
if [[ "${STATUS_LINES}" -eq 5 ]] && grep -Fxq 'last_success=' <<<"${STATUS_OUT}"; then
    pass
else
    fail "a corrupt multi-line .last-success broke the 5-line key=value framing: ${STATUS_OUT}"
fi

echo "case: status reports the pinned version (#708 case 7)"
new_case status-pinned 1.2.3
run_update codex 1.2.3 >/dev/null
STATUS_OUT="$(run_agent_cli status codex)"
if grep -Fxq 'pin=1.2.3' <<<"${STATUS_OUT}"; then pass; else fail "status did not report pin=1.2.3: ${STATUS_OUT}"; fi

echo "case: a successful update writes both stamps, numeric, in-window, mode 0644 (#708 case 8)"
new_case stamps-success 1.2.3
BEFORE_EPOCH="$(date +%s)"
run_update codex >/dev/null
AFTER_EPOCH="$(date +%s)"
LAST_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
LAST_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
if [[ "${LAST_SUCCESS}" =~ ^[0-9]+$ ]] && [[ "${LAST_ATTEMPT}" =~ ^[0-9]+$ ]] \
    && (( LAST_SUCCESS >= BEFORE_EPOCH && LAST_SUCCESS <= AFTER_EPOCH )) \
    && (( LAST_ATTEMPT >= BEFORE_EPOCH && LAST_ATTEMPT <= AFTER_EPOCH )) \
    && [[ -n "$(find "${ROOT_DIR}/.last-success" -maxdepth 0 -perm 0644 2>/dev/null)" ]] \
    && [[ -n "$(find "${ROOT_DIR}/.last-attempt" -maxdepth 0 -perm 0644 2>/dev/null)" ]]; then
    pass
else
    fail "successful update did not write both numeric, in-window, mode-0644 stamps (success=${LAST_SUCCESS}, attempt=${LAST_ATTEMPT}, window=[${BEFORE_EPOCH},${AFTER_EPOCH}])"
fi

echo "case: a registry-stage failure advances .last-attempt but preserves .last-success (#708 case 9)"
new_case stamps-registry-failure 1.2.3
run_update codex >/dev/null
SENTINEL_EPOCH=100000000
printf '%s\n' "${SENTINEL_EPOCH}" >"${ROOT_DIR}/.last-success"
printf '%s\n' "${SENTINEL_EPOCH}" >"${ROOT_DIR}/.last-attempt"
NPM_VIEW_FAIL=1
run_update codex >/dev/null 2>&1
unset NPM_VIEW_FAIL
LAST_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
LAST_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
if [[ "${LAST_SUCCESS}" == "${SENTINEL_EPOCH}" ]] && [[ "${LAST_ATTEMPT}" =~ ^[0-9]+$ ]] && [[ "${LAST_ATTEMPT}" != "${SENTINEL_EPOCH}" ]]; then
    pass
else
    fail "registry-stage failure did not advance .last-attempt while preserving .last-success (success=${LAST_SUCCESS}, attempt=${LAST_ATTEMPT})"
fi

echo "case: a late-stage (health-check) failure also advances .last-attempt but preserves .last-success (#708 case 10)"
new_case stamps-health-failure 1.2.3
run_update codex >/dev/null
SENTINEL_EPOCH=100000000
printf '%s\n' "${SENTINEL_EPOCH}" >"${ROOT_DIR}/.last-success"
printf '%s\n' "${SENTINEL_EPOCH}" >"${ROOT_DIR}/.last-attempt"
MOCK_VERSION=1.2.4 # different from current/VERSION so the short-circuit cannot fire
HEALTH_FAIL=1
run_update codex >/dev/null 2>&1
unset HEALTH_FAIL
LAST_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
LAST_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
if [[ "${LAST_SUCCESS}" == "${SENTINEL_EPOCH}" ]] && [[ "${LAST_ATTEMPT}" =~ ^[0-9]+$ ]] && [[ "${LAST_ATTEMPT}" != "${SENTINEL_EPOCH}" ]]; then
    pass
else
    fail "late-stage failure did not advance .last-attempt while preserving .last-success (success=${LAST_SUCCESS}, attempt=${LAST_ATTEMPT})"
fi

echo "case: update <agent> <version> writes PIN; unpin removes it and is idempotent (#708 case 11)"
new_case pin-unpin
run_update codex 1.2.3 >/dev/null
PIN_CONTENT="$(cat "${ROOT_DIR}/PIN" 2>/dev/null)"
PIN_MODE_OK="$(find "${ROOT_DIR}/PIN" -maxdepth 0 -perm 0644 2>/dev/null)"
run_agent_cli unpin codex >/dev/null
UNPIN_EXIT=$?
run_agent_cli unpin codex >/dev/null
UNPIN_AGAIN_EXIT=$?
if [[ "${PIN_CONTENT}" == 1.2.3 ]] && [[ -n "${PIN_MODE_OK}" ]] \
    && [[ "${UNPIN_EXIT}" -eq 0 ]] && [[ ! -e "${ROOT_DIR}/PIN" ]] \
    && [[ "${UNPIN_AGAIN_EXIT}" -eq 0 ]]; then
    pass
else
    fail "PIN write/unpin lifecycle failed (pin=${PIN_CONTENT}, mode_ok=${PIN_MODE_OK}, unpin_exit=${UNPIN_EXIT}, unpin_again_exit=${UNPIN_AGAIN_EXIT})"
fi

echo "case: PIN is written when the short-circuit fires, not only on a full install (#708 case 12)"
new_case pin-on-short-circuit 1.2.3
run_update codex >/dev/null # bare update, no explicit version -> no PIN yet
PIN_BEFORE_EXISTS=0
[[ -e "${ROOT_DIR}/PIN" ]] && PIN_BEFORE_EXISTS=1
run_update codex 1.2.3 >/dev/null # explicit version matching current -> short-circuits, must still write PIN
PIN_AFTER="$(cat "${ROOT_DIR}/PIN" 2>/dev/null)"
if [[ "${PIN_BEFORE_EXISTS}" -eq 0 ]] && [[ "${PIN_AFTER}" == 1.2.3 ]]; then
    pass
else
    fail "PIN was not written by the short-circuit path (before_exists=${PIN_BEFORE_EXISTS}, after=${PIN_AFTER})"
fi

echo "case: update with no version against a pinned volume refuses with exit 2 and writes nothing (#708 case 13)"
new_case pin-refusal 1.2.3
run_update codex 1.2.3 >/dev/null
BEFORE_TARGET="$(readlink "${ROOT_DIR}/current")"
BEFORE_CALLS="$(wc -l <"${CALL_LOG}")"
BEFORE_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
BEFORE_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
REFUSAL_STDERR="$(run_update codex 2>&1 1>/dev/null)"
REFUSAL_EXIT=$?
AFTER_TARGET="$(readlink "${ROOT_DIR}/current")"
AFTER_CALLS="$(wc -l <"${CALL_LOG}")"
AFTER_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
AFTER_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
if [[ "${REFUSAL_EXIT}" -eq 2 ]] \
    && [[ "${REFUSAL_STDERR}" == *'is pinned to 1.2.3'* ]] \
    && [[ "${AFTER_TARGET}" == "${BEFORE_TARGET}" ]] \
    && [[ "${AFTER_CALLS}" -eq "${BEFORE_CALLS}" ]] \
    && [[ "${AFTER_SUCCESS}" == "${BEFORE_SUCCESS}" ]] \
    && [[ "${AFTER_ATTEMPT}" == "${BEFORE_ATTEMPT}" ]]; then
    pass
else
    fail "pin refusal did not exit 2 cleanly without writing/calling anything (exit=${REFUSAL_EXIT}): ${REFUSAL_STDERR}"
fi

echo "case: --skip-if-pinned against a pinned volume exits 0 with a one-line notice and writes/calls nothing (#708 case 14)"
new_case skip-if-pinned 1.2.3
run_update codex 1.2.3 >/dev/null
BEFORE_TARGET="$(readlink "${ROOT_DIR}/current")"
BEFORE_CALLS="$(wc -l <"${CALL_LOG}")"
BEFORE_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
BEFORE_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
SKIP_STDOUT="$(run_update codex '' --skip-if-pinned 2>/dev/null)"
SKIP_EXIT=$?
SKIP_LINES="$(wc -l <<<"${SKIP_STDOUT}")"
AFTER_TARGET="$(readlink "${ROOT_DIR}/current")"
AFTER_CALLS="$(wc -l <"${CALL_LOG}")"
AFTER_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
AFTER_ATTEMPT="$(cat "${ROOT_DIR}/.last-attempt" 2>/dev/null)"
if [[ "${SKIP_EXIT}" -eq 0 ]] \
    && [[ "${SKIP_LINES}" -eq 1 ]] \
    && [[ "${SKIP_STDOUT}" == *'pinned to 1.2.3'* ]] \
    && [[ "${AFTER_TARGET}" == "${BEFORE_TARGET}" ]] \
    && [[ "${AFTER_CALLS}" -eq "${BEFORE_CALLS}" ]] \
    && [[ "${AFTER_SUCCESS}" == "${BEFORE_SUCCESS}" ]] \
    && [[ "${AFTER_ATTEMPT}" == "${BEFORE_ATTEMPT}" ]]; then
    pass
else
    fail "--skip-if-pinned did not exit 0 with a one-line stdout notice and no install/call/stamp (exit=${SKIP_EXIT}): ${SKIP_STDOUT}"
fi

echo "case: an unpopulated pinned volume bypasses the pin gate with a loud warning and repairs itself (#708 case 15)"
new_case pin-bypass-unpopulated 1.2.3
run_update codex 1.2.3 >/dev/null
chmod -x "${ROOT_DIR}/current/node_modules/.bin/codex"
# MOCK_VERSION now diverges from the pin (1.2.3) so the assertions below can
# tell "repair installed latest" apart from "repair installed the pin" -- the
# plan explicitly rejected installing the pinned version during this repair
# (see plan's Alternatives Considered), so this must prove `latest` truly ran.
MOCK_VERSION=1.2.4
BYPASS_STDERR="$(run_update codex 2>&1 1>/dev/null)"
BYPASS_EXIT=$?
if [[ "${BYPASS_EXIT}" -eq 0 ]] \
    && [[ "${BYPASS_STDERR}" == *'pinned to 1.2.3'* ]] \
    && [[ "${BYPASS_STDERR}" == *'no executable release'* ]] \
    && [[ -x "${ROOT_DIR}/current/node_modules/.bin/codex" ]] \
    && [[ "$(cat "${ROOT_DIR}/current/VERSION" 2>/dev/null)" == 1.2.4 ]] \
    && grep -Fq -- 'view @openai/codex@latest --json' "${CALL_LOG}" \
    && [[ "$(cat "${ROOT_DIR}/PIN" 2>/dev/null)" == 1.2.3 ]]; then
    pass
else
    fail "unpopulated-pinned repair did not install latest (1.2.4), or clobbered the stale pin (1.2.3) (exit=${BYPASS_EXIT}, current version=$(cat "${ROOT_DIR}/current/VERSION" 2>/dev/null)): ${BYPASS_STDERR}"
fi

echo "case: same-version short-circuit skips install/rotation and advances .last-success (#708 case 16)"
new_case short-circuit 1.2.3
run_update codex 1.2.3 >/dev/null
BEFORE_TARGET="$(readlink "${ROOT_DIR}/current")"
SENTINEL_EPOCH=100000000
printf '%s\n' "${SENTINEL_EPOCH}" >"${ROOT_DIR}/.last-success"
LOG_BEFORE="$(cat "${CALL_LOG}")"
SHORT_STDOUT="$(run_update codex 1.2.3 2>/dev/null)"
SHORT_EXIT=$?
AFTER_TARGET="$(readlink "${ROOT_DIR}/current")"
LOG_AFTER="$(cat "${CALL_LOG}")"
NEW_LOG="${LOG_AFTER#"${LOG_BEFORE}"}"
LAST_SUCCESS="$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)"
if [[ "${SHORT_EXIT}" -eq 0 ]] \
    && [[ "${SHORT_STDOUT}" == *'already at 1.2.3'* ]] \
    && [[ "${AFTER_TARGET}" == "${BEFORE_TARGET}" ]] \
    && [[ ! -e "${ROOT_DIR}/previous" ]] \
    && [[ "${NEW_LOG}" == *view* ]] \
    && [[ "${NEW_LOG}" != *install* ]] \
    && [[ "${NEW_LOG}" != *rebuild* ]] \
    && [[ "${LAST_SUCCESS}" =~ ^[0-9]+$ ]] && [[ "${LAST_SUCCESS}" != "${SENTINEL_EPOCH}" ]]; then
    pass
else
    fail "same-version short-circuit did not skip install/rotation cleanly (exit=${SHORT_EXIT}, target before=${BEFORE_TARGET} after=${AFTER_TARGET}, new_log=${NEW_LOG}, last_success=${LAST_SUCCESS}): ${SHORT_STDOUT}"
fi

echo "case: no short-circuit when the release binary is non-executable -- full reinstall instead (#708 case 17)"
new_case no-short-circuit-non-executable 1.2.3
run_update codex >/dev/null # bare update, no PIN, so only the short-circuit predicate is exercised
BEFORE_TARGET="$(readlink "${ROOT_DIR}/current")"
chmod -x "${ROOT_DIR}/current/node_modules/.bin/codex"
LOG_BEFORE="$(cat "${CALL_LOG}")"
run_update codex >/dev/null # same MOCK_VERSION, but populated is now false
REINSTALL_EXIT=$?
AFTER_TARGET="$(readlink "${ROOT_DIR}/current")"
LOG_AFTER="$(cat "${CALL_LOG}")"
NEW_LOG="${LOG_AFTER#"${LOG_BEFORE}"}"
if [[ "${REINSTALL_EXIT}" -eq 0 ]] \
    && [[ "${AFTER_TARGET}" != "${BEFORE_TARGET}" ]] \
    && [[ "${NEW_LOG}" == *install* ]] \
    && [[ -x "${ROOT_DIR}/current/node_modules/.bin/codex" ]] \
    && [[ "$(cat "${ROOT_DIR}/.last-success" 2>/dev/null)" =~ ^[0-9]+$ ]]; then
    pass
else
    fail "a non-executable release binary unexpectedly short-circuited instead of triggering a full reinstall (exit=${REINSTALL_EXIT}, target before=${BEFORE_TARGET} after=${AFTER_TARGET}, new_log=${NEW_LOG}, last_success=$(cat "${ROOT_DIR}/.last-success" 2>/dev/null))"
fi

# Case 18 (no short-circuit when current/VERSION is missing) is covered by
# the reworked same-version-re-update case above, per the plan's AC.

echo "case: usage and strict argument parsing (#708 case 19)"
new_case usage-parse-errors
USAGE_FAILS=()

NO_ARGS_OUT="$(run_agent_cli 2>&1)"
NO_ARGS_EXIT=$?
if [[ "${NO_ARGS_EXIT}" -ne 2 ]] || [[ "${NO_ARGS_OUT}" != *update* ]] || [[ "${NO_ARGS_OUT}" != *status* ]] || [[ "${NO_ARGS_OUT}" != *unpin* ]]; then
    USAGE_FAILS+=("no-args usage line missing a subcommand or wrong exit (${NO_ARGS_EXIT}): ${NO_ARGS_OUT}")
fi

run_update codex '' --bogus >/dev/null 2>&1
[[ $? -eq 2 ]] || USAGE_FAILS+=("update codex --bogus did not exit 2")

run_update codex 1.2.3 4.5.6 >/dev/null 2>&1
[[ $? -eq 2 ]] || USAGE_FAILS+=("update codex 1.2.3 4.5.6 (second positional) did not exit 2")

run_update codex 1.2.3 --skip-if-pinned >/dev/null 2>&1
[[ $? -eq 2 ]] || USAGE_FAILS+=("update codex 1.2.3 --skip-if-pinned (conflicting flag) did not exit 2")

run_agent_cli status codex extra >/dev/null 2>&1
[[ $? -eq 2 ]] || USAGE_FAILS+=("status codex extra (extra arg) did not exit 2")

if [[ "${#USAGE_FAILS[@]}" -eq 0 ]]; then
    pass
else
    fail "usage/parse-error checks failed: $(printf '%s; ' "${USAGE_FAILS[@]}")"
fi

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
