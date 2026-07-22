#!/usr/bin/env bash
# Host-runnable tests for sandbox/lib/dind.sh's start_dind() (#586).
#
# start_dind() is called from entrypoint.sh's root phase, gated on
# CENCI_SANDBOX_DIND=1 (see entrypoint.sh and the plan's Architectural
# Context). It must:
#   1. create the docker group (groupadd -f docker) and add dev to it
#      (usermod -aG docker dev) BEFORE launching dockerd, so the priv-drop
#      re-exec into `sudo -u dev` picks up the updated /etc/group membership
#      (no `sg` re-exec needed, unlike the removed DooD block) — and fail
#      loudly (non-zero exit, clear stderr message) if either call fails,
#      matching entrypoint.sh's own UID/GID remap failure style.
#   2. launch dockerd fire-and-forget in a background subshell that owns the
#      `wait` on dockerd's own exit (the subshell — not the entrypoint's main
#      flow — blocks on it), so container startup never waits on daemon
#      readiness.
#   3. write a timestamped .cenci-dockerd-startup-error marker hardcoded under
#      /home/dev (root's own $HOME is /root at this point in entrypoint.sh —
#      before the `sudo -u dev` re-exec — so reading $HOME here would silently
#      lose the marker on `--rm`; CENCI_DIND_HOME_ROOT is this test's only,
#      narrow override point, same convention as agent_cli_root()'s
#      CENCI_AGENT_CLI_ROOT in lib/agent-cli.sh), containing the tail of
#      dockerd's captured stderr, but ONLY when dockerd exits non-zero AND
#      that exit code isn't a common termination signal (129/130/131/137/143 —
#      SIGHUP/SIGINT/SIGQUIT/SIGKILL/SIGTERM, i.e. an ordinary `docker stop`
#      shutdown, not a crash). A clean exit, a normal-shutdown signal exit, or
#      one still running when reaped must never leave a marker behind. This
#      mirrors the existing .cenci-agent-startup-error convention (#572): UTC
#      ISO-8601 prefix via `date -u +%Y-%m-%dT%H:%M:%SZ`, one line, real
#      diagnostic content (not just a generic pointer). A failure of the
#      marker write itself must still surface as a loud stderr warning rather
#      than vanish silently.
#
# Host-runnable: no container/dockerd needed. groupadd/usermod/dockerd are
# mocked via a scoped, env -i-scrubbed PATH (see sandbox/tests/agent-cli.test.sh
# for the same recording-fake harness pattern), and each mocked command
# enforces the exact invocation it expects — a mock that silently accepted any
# args would mask a production bug (#490's lesson). The background subshell's
# async is made deterministic with `wait` (a child of the invoking bash -c),
# not sleep-polling.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

TIMESTAMP_REGEX='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z '

echo "dind.test.sh"

DIND_LIB="${ROOT}/sandbox/lib/dind.sh"
if [[ -f "${DIND_LIB}" ]]; then
    # shellcheck source-path=SCRIPTDIR
    # shellcheck source=../lib/dind.sh
    source "${DIND_LIB}"
fi

echo "case: sandbox/lib/dind.sh defines start_dind()"
if declare -F start_dind >/dev/null; then
    pass
else
    fail "start_dind() is not defined — ${DIND_LIB} is missing or does not define start_dind()"
    print_summary
    exit 1
fi

# make_mocks <bin-dir> <dockerd-exit-code> <dockerd-stderr-text>
#            [groupadd-exit-code=0] [usermod-exit-code=0]
#
# Every mock records "<command>|<args>" to CALL_LOG and rejects any
# invocation that doesn't match the real command's expected interface
# (groupadd -f docker; usermod -aG docker dev; dockerd with no flags — see
# the plan's Assumptions: dockerd runs with defaults, no --data-root or other
# custom flags), instead of accepting-and-passing every input. The optional
# groupadd/usermod exit codes let a case simulate a real (non-args-mismatch)
# failure of either call, independent of dockerd.
make_mocks() {
    local bin="$1" dockerd_rc="$2" dockerd_stderr="$3" groupadd_rc="${4:-0}" usermod_rc="${5:-0}"
    mkdir -p "${bin}"

    cat >"${bin}/groupadd" <<EOF
#!/bin/bash
set -u
printf 'groupadd|%s\n' "\$*" >>"\${CALL_LOG}"
[[ -z "\${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "\${CENCI_PARENT_SECRET}" >>"\${CALL_LOG}"
# Real groupadd -f <group> requires the group name as the trailing arg.
[[ "\$*" == "-f docker" ]] || exit 64
exit ${groupadd_rc}
EOF
    chmod +x "${bin}/groupadd"

    cat >"${bin}/usermod" <<EOF
#!/bin/bash
set -u
printf 'usermod|%s\n' "\$*" >>"\${CALL_LOG}"
[[ -z "\${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "\${CENCI_PARENT_SECRET}" >>"\${CALL_LOG}"
# Real usermod -aG <group> <user> requires both the group and target user.
[[ "\$*" == "-aG docker dev" ]] || exit 64
exit ${usermod_rc}
EOF
    chmod +x "${bin}/usermod"

    cat >"${bin}/dockerd" <<EOF
#!/bin/bash
set -u
printf 'dockerd|%s\n' "\$*" >>"\${CALL_LOG}"
[[ -z "\${CENCI_PARENT_SECRET:-}" ]] || printf 'LEAK:%s\n' "\${CENCI_PARENT_SECRET}" >>"\${CALL_LOG}"
printf '%s' "${dockerd_stderr}" >&2
exit ${dockerd_rc}
EOF
    chmod +x "${bin}/dockerd"
}

# run_start_dind <bin-dir> <home-dir> <call-log> [stderr-capture-file]: sources
# lib/dind.sh in a scrubbed subshell, calls start_dind, then `wait`s (inside
# that same bash -c, a child of THIS test process) for the backgrounded
# self-monitoring subshell to finish — deterministic, no sleep-polling —
# before returning. <home-dir> is passed as CENCI_DIND_HOME_ROOT, the
# test-only override of dind.sh's hardcoded /home/dev (see lib/dind.sh's
# comment) — never $HOME, which the fix deliberately no longer reads. Returns
# the bash -c invocation's own exit status, so a `start_dind`-internal `exit
# 1` (the groupadd/usermod failure guard) propagates to the caller.
run_start_dind() {
    local bin="$1" home="$2" call_log="$3" stderr_capture="${4:-/dev/null}"
    # shellcheck disable=SC2016  # $1 must expand in the child bash, not here
    env -i PATH="${bin}:/usr/bin:/bin" CENCI_DIND_HOME_ROOT="${home}" CALL_LOG="${call_log}" \
        bash -c 'source "$1"; start_dind; wait' _ "${DIND_LIB}" 2>"${stderr_capture}"
}

# ── Case 1: dockerd exits non-zero → timestamped marker with stderr tail ──
echo "case: dockerd exiting non-zero writes a timestamped marker containing the stderr tail"
CASE1_DIR="${WORK}/failure"
BIN1="${CASE1_DIR}/bin"
HOME1="${CASE1_DIR}/home"
CALL_LOG1="${CASE1_DIR}/calls.log"
MARKER1="${HOME1}/.cenci-dockerd-startup-error"
mkdir -p "${HOME1}"
: >"${CALL_LOG1}"
DOCKERD_STDERR=$'INFO: starting up\nWARN: probing storage driver\nFATAL: failed to start daemon: mkdir /var/lib/docker/overlay2: read-only file system'
make_mocks "${BIN1}" 1 "${DOCKERD_STDERR}"
run_start_dind "${BIN1}" "${HOME1}" "${CALL_LOG1}"

if [[ -f "${MARKER1}" ]]; then
    pass
else
    fail "expected a marker at ${MARKER1} after dockerd exited non-zero, but none was written"
fi

MARKER1_CONTENT="$(cat "${MARKER1}" 2>/dev/null || true)"

echo "case: failure marker content starts with a UTC ISO-8601 timestamp"
if [[ "${MARKER1_CONTENT}" =~ ${TIMESTAMP_REGEX} ]]; then
    pass
else
    fail "marker content does not start with a UTC ISO-8601 timestamp: ${MARKER1_CONTENT}"
fi

echo "case: failure marker contains dockerd's captured stderr tail (the real diagnostic)"
if [[ "${MARKER1_CONTENT}" == *"failed to start daemon: mkdir /var/lib/docker/overlay2: read-only file system"* ]]; then
    pass
else
    fail "marker content is missing dockerd's real stderr diagnostic: ${MARKER1_CONTENT}"
fi

echo "case: start_dind creates the docker group and adds dev to it before launching dockerd"
if grep -Fxq -- 'groupadd|-f docker' "${CALL_LOG1}" && grep -Fxq -- 'usermod|-aG docker dev' "${CALL_LOG1}"; then
    pass
else
    fail "expected 'groupadd -f docker' and 'usermod -aG docker dev'; calls: $(cat "${CALL_LOG1}")"
fi

echo "case: dockerd is launched with defaults — no extra flags"
if grep -Eq '^dockerd\|[[:space:]]*$' "${CALL_LOG1}"; then
    pass
else
    fail "expected dockerd invoked with no arguments (defaults only); calls: $(cat "${CALL_LOG1}")"
fi

# ── Case 2: dockerd exits 0 → no marker ────────────────────────────────
echo "case: dockerd exiting 0 writes no marker"
CASE2_DIR="${WORK}/clean"
BIN2="${CASE2_DIR}/bin"
HOME2="${CASE2_DIR}/home"
CALL_LOG2="${CASE2_DIR}/calls.log"
MARKER2="${HOME2}/.cenci-dockerd-startup-error"
mkdir -p "${HOME2}"
: >"${CALL_LOG2}"
make_mocks "${BIN2}" 0 ""
run_start_dind "${BIN2}" "${HOME2}" "${CALL_LOG2}"

if [[ ! -e "${MARKER2}" ]]; then
    pass
else
    fail "expected no marker when dockerd exits 0, but found one at ${MARKER2}: $(cat "${MARKER2}")"
fi

# ── Regression: env -i scrub keeps a host-only secret out of the mocked
#    subprocess environment (#363's lesson — pair the scrub with a sentinel
#    assertion, don't just trust env -i silently). ─────────────────────────
echo "case: a host-only sentinel secret never reaches groupadd/usermod/dockerd"
CASE3_DIR="${WORK}/secrets"
BIN3="${CASE3_DIR}/bin"
HOME3="${CASE3_DIR}/home"
CALL_LOG3="${CASE3_DIR}/calls.log"
mkdir -p "${HOME3}"
: >"${CALL_LOG3}"
make_mocks "${BIN3}" 0 ""
export CENCI_PARENT_SECRET='parent-only-sentinel'
run_start_dind "${BIN3}" "${HOME3}" "${CALL_LOG3}"
unset CENCI_PARENT_SECRET
if grep -q 'LEAK:' "${CALL_LOG3}"; then
    fail "CENCI_PARENT_SECRET leaked into a mocked command's environment: $(cat "${CALL_LOG3}")"
else
    pass
fi

# ── Case 4: groupadd failure → start_dind fails loudly, never launches
#    dockerd (silent-failure-hunter finding: previously unchecked) ─────────
echo "case: groupadd failure makes start_dind exit non-zero"
CASE4_DIR="${WORK}/groupadd-fail"
BIN4="${CASE4_DIR}/bin"
HOME4="${CASE4_DIR}/home"
CALL_LOG4="${CASE4_DIR}/calls.log"
STDERR4="${CASE4_DIR}/stderr.log"
mkdir -p "${HOME4}"
: >"${CALL_LOG4}"
make_mocks "${BIN4}" 0 "" 1 0
if run_start_dind "${BIN4}" "${HOME4}" "${CALL_LOG4}" "${STDERR4}"; then
    fail "expected start_dind to exit non-zero when groupadd fails, but it succeeded"
else
    pass
fi

echo "case: groupadd failure is reported loudly on stderr"
if grep -q 'groupadd' "${STDERR4}"; then
    pass
else
    fail "expected a loud stderr message mentioning the groupadd failure; stderr: $(cat "${STDERR4}" 2>/dev/null)"
fi

echo "case: groupadd failure prevents dockerd from ever being launched"
if grep -q '^dockerd|' "${CALL_LOG4}"; then
    fail "dockerd was launched despite groupadd failing; calls: $(cat "${CALL_LOG4}")"
else
    pass
fi

# ── Case 5: usermod failure → start_dind fails loudly, never launches
#    dockerd ────────────────────────────────────────────────────────────
echo "case: usermod failure makes start_dind exit non-zero"
CASE5_DIR="${WORK}/usermod-fail"
BIN5="${CASE5_DIR}/bin"
HOME5="${CASE5_DIR}/home"
CALL_LOG5="${CASE5_DIR}/calls.log"
STDERR5="${CASE5_DIR}/stderr.log"
mkdir -p "${HOME5}"
: >"${CALL_LOG5}"
make_mocks "${BIN5}" 0 "" 0 1
if run_start_dind "${BIN5}" "${HOME5}" "${CALL_LOG5}" "${STDERR5}"; then
    fail "expected start_dind to exit non-zero when usermod fails, but it succeeded"
else
    pass
fi

echo "case: usermod failure is reported loudly on stderr"
if grep -q 'usermod' "${STDERR5}"; then
    pass
else
    fail "expected a loud stderr message mentioning the usermod failure; stderr: $(cat "${STDERR5}" 2>/dev/null)"
fi

echo "case: usermod failure prevents dockerd from ever being launched"
if grep -q '^dockerd|' "${CALL_LOG5}"; then
    fail "dockerd was launched despite usermod failing; calls: $(cat "${CALL_LOG5}")"
else
    pass
fi

# ── Case 6: dockerd exits 143 (SIGTERM, normal `docker stop`/shutdown) →
#    no marker (silent-failure-hunter finding: a genuine crash must not look
#    identical to a routine shutdown) ───────────────────────────────────────
echo "case: dockerd exiting 143 (SIGTERM / normal shutdown) writes no marker"
CASE6_DIR="${WORK}/sigterm"
BIN6="${CASE6_DIR}/bin"
HOME6="${CASE6_DIR}/home"
CALL_LOG6="${CASE6_DIR}/calls.log"
MARKER6="${HOME6}/.cenci-dockerd-startup-error"
mkdir -p "${HOME6}"
: >"${CALL_LOG6}"
make_mocks "${BIN6}" 143 "received SIGTERM, shutting down"
run_start_dind "${BIN6}" "${HOME6}" "${CALL_LOG6}"

if [[ ! -e "${MARKER6}" ]]; then
    pass
else
    fail "expected no marker for a normal-shutdown exit code (143/SIGTERM), but found one at ${MARKER6}: $(cat "${MARKER6}")"
fi

# ── Case 6b: the remaining excluded termination-signal codes (129/130/131/
#    137 — SIGHUP/SIGINT/SIGQUIT/SIGKILL) also write no marker. Only 143 had
#    a dedicated case above; the plan comment listed these four but nothing
#    exercised them, so a typo in any literal in the exclusion list would go
#    undetected. rc=137 (SIGKILL) is an accepted tradeoff — it can't be
#    distinguished from a genuine OOM-kill — this only proves it's still
#    excluded, not that every SIGKILL is actually a `docker stop`. ─────────
for SIGCASE_RC in 129 130 131 137; do
    echo "case: dockerd exiting ${SIGCASE_RC} (excluded termination signal) writes no marker"
    SIGCASE_DIR="${WORK}/sig-${SIGCASE_RC}"
    SIGCASE_BIN="${SIGCASE_DIR}/bin"
    SIGCASE_HOME="${SIGCASE_DIR}/home"
    SIGCASE_CALL_LOG="${SIGCASE_DIR}/calls.log"
    SIGCASE_MARKER="${SIGCASE_HOME}/.cenci-dockerd-startup-error"
    mkdir -p "${SIGCASE_HOME}"
    : >"${SIGCASE_CALL_LOG}"
    make_mocks "${SIGCASE_BIN}" "${SIGCASE_RC}" "killed by signal"
    run_start_dind "${SIGCASE_BIN}" "${SIGCASE_HOME}" "${SIGCASE_CALL_LOG}"

    if [[ ! -e "${SIGCASE_MARKER}" ]]; then
        pass
    else
        fail "expected no marker for excluded termination-signal exit code ${SIGCASE_RC}, but found one at ${SIGCASE_MARKER}: $(cat "${SIGCASE_MARKER}")"
    fi
done

# ── Case 7: the marker write itself failing must not vanish silently ──────
echo "case: a marker-write failure is reported loudly on stderr, not swallowed"
CASE7_DIR="${WORK}/marker-write-fail"
BIN7="${CASE7_DIR}/bin"
HOME7="${CASE7_DIR}/home"
CALL_LOG7="${CASE7_DIR}/calls.log"
STDERR7="${CASE7_DIR}/stderr.log"
mkdir -p "${HOME7}"
: >"${CALL_LOG7}"
make_mocks "${BIN7}" 1 "boom: dockerd crashed"
# Remove write permission on the marker's own directory so the final
# `> "${marker}"` redirection fails (e.g. an unwritable /home/dev in
# production) — restored immediately after so the outer WORK cleanup trap
# can still remove it.
chmod 500 "${HOME7}"
run_start_dind "${BIN7}" "${HOME7}" "${CALL_LOG7}" "${STDERR7}"
chmod 700 "${HOME7}"

if grep -q 'failed to write dockerd startup-error marker' "${STDERR7}"; then
    pass
else
    fail "expected a stderr warning when the marker write itself fails; stderr: $(cat "${STDERR7}" 2>/dev/null)"
fi

# ── lib/dind.sh must be sourceable standalone with no side effects ────────
echo "case: sandbox/lib/dind.sh is sourceable standalone"
if bash -c 'set -e; source "$1"' _ "${DIND_LIB}"; then
    pass
else
    fail "sourcing sandbox/lib/dind.sh standalone failed"
fi

print_summary
[[ "${FAILURES}" -eq 0 ]]
