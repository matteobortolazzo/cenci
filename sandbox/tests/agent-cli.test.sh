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
    if [[ -n "${NPM_CONCURRENCY_MARKER:-}" ]]; then
        if ! mkdir "${NPM_CONCURRENCY_MARKER}" 2>/dev/null; then : >"${NPM_OVERLAP}"; fi
        sleep 0.2
        rmdir "${NPM_CONCURRENCY_MARKER}" 2>/dev/null || true
    fi
    mkdir -p "${prefix}/node_modules/@openai/codex" "${prefix}/node_modules/@anthropic-ai/claude-code" "${prefix}/node_modules/.bin"
    printf '{"packages":{"node_modules/@openai/codex":{"integrity":"%s"},"node_modules/@anthropic-ai/claude-code":{"integrity":"%s"}}}\n' \
        "${LOCK_INTEGRITY:-${MOCK_INTEGRITY}}" "${LOCK_INTEGRITY:-${MOCK_INTEGRITY}}" >"${prefix}/package-lock.json"
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
        bash "${ROOT}/sandbox/lib/agent-cli.sh" update "${agent}" ${version:+"${version}"}
}

echo "agent-cli.test.sh"

echo "case: package allowlist and exact-version validation"
new_case validation
if [[ "$(bash -c 'source "$1"; agent_cli_package claude' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == '@anthropic-ai/claude-code' ]] \
    && [[ "$(bash -c 'source "$1"; agent_cli_package codex' _ "${ROOT}/sandbox/lib/agent-cli.sh")" == '@openai/codex' ]] \
    && ! run_update codex latest >/dev/null 2>&1; then pass; else fail "package or exact-version allowlist failed"; fi

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

echo "case: same-version re-update from one PID atomically rotates current"
# In the updater container the script is always the PID-1 shell, so release
# names must not derive from $$: a same-version re-update would collide with
# the existing release directory, nest into it, and leave current unchanged.
# Sourcing the library and updating twice in one shell reproduces that PID
# reuse on the host.
new_case samepid 5.5.5
# shellcheck disable=SC2016  # $1 and ${CENCI_AGENT_CLI_ROOT} must expand in the child bash, not here
TARGETS="$(env -i PATH="${BIN}:/usr/bin:/bin" CALL_LOG="${CALL_LOG}" \
    CENCI_AGENT_CLI_ROOT="${ROOT_DIR}" MOCK_VERSION="5.5.5" \
    MOCK_INTEGRITY="${MOCK_INTEGRITY}" ATTESTATION_JSON="$(make_attestation 5.5.5)" \
    bash -c 'source "$1" \
        && update_agent_cli codex >/dev/null && readlink "${CENCI_AGENT_CLI_ROOT}/current" \
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

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
