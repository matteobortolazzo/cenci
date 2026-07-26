#!/usr/bin/env bash
# Host-runnable tests for sandbox/lib/dind.sh's start_dind() (#586, revised by
# #630 for sentinel-based shutdown classification).
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
#   2. at entry, clear (rm -f) any stale `.cenci-dockerd-shutdown` sentinel
#      and any prior `.cenci-dockerd-startup-error` marker — supersede-only
#      clearing, so a leftover sentinel/marker from a previous container
#      lifetime never pollutes this run's own classification.
#   3. launch dockerd fire-and-forget in a background subshell that owns the
#      `wait` on dockerd's own exit (the subshell — not the entrypoint's main
#      flow — blocks on it), so container startup never waits on daemon
#      readiness.
#   4. classify dockerd's exit via the shutdown sentinel, NOT exit-code
#      guesswork (#630 — the ticket explicitly forbids suppressing status 137
#      by exit-code allow-listing alone): a timestamped
#      .cenci-dockerd-startup-error marker is written for any non-zero
#      dockerd exit UNLESS rc==0 OR the `.cenci-dockerd-shutdown` sentinel is
#      present. The sentinel is written by the launcher's dind-only PID-1
#      keepalive TERM/INT trap (watch/internal/sandbox/launcher/launch.go)
#      when the container receives an intentional stop signal — a still-
#      running dockerd (never reaped this run) or a clean rc==0 exit must
#      never leave a marker either. This mirrors the existing
#      .cenci-agent-startup-error convention (#572): UTC ISO-8601 prefix via
#      `date -u +%Y-%m-%dT%H:%M:%SZ`, one line, real diagnostic content (not
#      just a generic pointer). A failure of the marker write itself must
#      still surface as a loud stderr warning rather than vanish silently.
#      CENCI_DIND_HOME_ROOT overrides the sentinel path exactly like it
#      already does the marker path (test-only; production never sets it).
#
# Host-runnable: no container/dockerd needed. groupadd/usermod/dockerd are
# mocked via a scoped, env -i-scrubbed PATH (see sandbox/tests/agent-cli.test.sh
# for the same recording-fake harness pattern), and each mocked command
# enforces the exact invocation it expects — a mock that silently accepted any
# args would mask a production bug (#490's lesson). The background subshell's
# async is made deterministic with `wait` (a child of the invoking bash -c),
# not sleep-polling. Since the real shutdown sentinel is written by a launcher
# keepalive process outside dind.sh's own scope, these tests simulate "the
# keepalive's trap already fired" by having the fake dockerd itself touch the
# sentinel file before exiting — dind.sh only ever reads the sentinel's
# presence, so it can't tell the difference.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

TIMESTAMP_REGEX='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z '
SENTINEL_NAME=".cenci-dockerd-shutdown"
MARKER_NAME=".cenci-dockerd-startup-error"

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
#            [groupadd-exit-code=0] [usermod-exit-code=0] [sentinel-path='']
#
# Every mock records "<command>|<args>" to CALL_LOG and rejects any
# invocation that doesn't match the real command's expected interface
# (groupadd -f docker; usermod -aG docker dev; dockerd with no flags — see
# the plan's Assumptions: dockerd runs with defaults, no --data-root or other
# custom flags), instead of accepting-and-passing every input. The optional
# groupadd/usermod exit codes let a case simulate a real (non-args-mismatch)
# failure of either call, independent of dockerd. The optional sentinel-path,
# when non-empty, makes the fake dockerd `touch` that path immediately before
# exiting — simulating the launcher's dind-only keepalive TERM/INT trap
# having already fired (and written the shutdown sentinel) concurrently with
# dockerd's own teardown, which is the real production race this sentinel
# design is meant to survive regardless of ordering.
make_mocks() {
    local bin="$1" dockerd_rc="$2" dockerd_stderr="$3" groupadd_rc="${4:-0}" usermod_rc="${5:-0}" sentinel_path="${6:-}"
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
if [[ -n "${sentinel_path}" ]]; then
    touch "${sentinel_path}"
fi
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

# ── Case 1: dockerd exits non-zero (no sentinel) → timestamped marker with
#    stderr tail — the baseline crash/OOM-like failure path ────────────────
echo "case: dockerd exiting non-zero with no shutdown sentinel writes a timestamped marker containing the stderr tail"
CASE1_DIR="${WORK}/failure"
BIN1="${CASE1_DIR}/bin"
HOME1="${CASE1_DIR}/home"
CALL_LOG1="${CASE1_DIR}/calls.log"
MARKER1="${HOME1}/${MARKER_NAME}"
mkdir -p "${HOME1}"
: >"${CALL_LOG1}"
DOCKERD_STDERR=$'INFO: starting up\nWARN: probing storage driver\nFATAL: failed to start daemon: mkdir /var/lib/docker/overlay2: read-only file system'
make_mocks "${BIN1}" 1 "${DOCKERD_STDERR}"
run_start_dind "${BIN1}" "${HOME1}" "${CALL_LOG1}"

if [[ -f "${MARKER1}" ]]; then
    pass
else
    fail "expected a marker at ${MARKER1} after dockerd exited non-zero with no sentinel, but none was written"
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
MARKER2="${HOME2}/${MARKER_NAME}"
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

# ── Case 6: sentinel present + dockerd exit 137 (SIGKILL — the real
#    production shape: docker/podman stop SIGTERMs the launcher's dind-only
#    keepalive directly, its trap writes the sentinel, then the container
#    teardown force-kills every other process, including dockerd, via
#    SIGKILL once the grace period elapses) writes no marker — #630's core
#    fix replaces the old exit-code allow-list with this sentinel check ──
echo "case: shutdown sentinel present + dockerd exit 137 (SIGKILL after the keepalive trap fired) writes no marker"
CASE6_DIR="${WORK}/sentinel-137"
BIN6="${CASE6_DIR}/bin"
HOME6="${CASE6_DIR}/home"
CALL_LOG6="${CASE6_DIR}/calls.log"
MARKER6="${HOME6}/${MARKER_NAME}"
SENTINEL6="${HOME6}/${SENTINEL_NAME}"
mkdir -p "${HOME6}"
: >"${CALL_LOG6}"
make_mocks "${BIN6}" 137 "killed by signal" 0 0 "${SENTINEL6}"
run_start_dind "${BIN6}" "${HOME6}" "${CALL_LOG6}"

if [[ ! -e "${MARKER6}" ]]; then
    pass
else
    fail "expected no marker when the shutdown sentinel is present for a 137 (SIGKILL) exit, but found one at ${MARKER6}: $(cat "${MARKER6}")"
fi

# ── Case 6b: sentinel authority is independent of the specific non-zero exit
#    code — sweep the same set of codes the old (now-removed) allow-list
#    special-cased, proving the sentinel alone (not the code) now decides ──
for SIGCASE_RC in 129 130 131 137 143 1; do
    echo "case: shutdown sentinel present + dockerd exit ${SIGCASE_RC} writes no marker (sentinel authority, not exit-code guesswork)"
    SIGCASE_DIR="${WORK}/sentinel-${SIGCASE_RC}"
    SIGCASE_BIN="${SIGCASE_DIR}/bin"
    SIGCASE_HOME="${SIGCASE_DIR}/home"
    SIGCASE_CALL_LOG="${SIGCASE_DIR}/calls.log"
    SIGCASE_MARKER="${SIGCASE_HOME}/${MARKER_NAME}"
    SIGCASE_SENTINEL="${SIGCASE_HOME}/${SENTINEL_NAME}"
    mkdir -p "${SIGCASE_HOME}"
    : >"${SIGCASE_CALL_LOG}"
    make_mocks "${SIGCASE_BIN}" "${SIGCASE_RC}" "killed/exited during shutdown" 0 0 "${SIGCASE_SENTINEL}"
    run_start_dind "${SIGCASE_BIN}" "${SIGCASE_HOME}" "${SIGCASE_CALL_LOG}"

    if [[ ! -e "${SIGCASE_MARKER}" ]]; then
        pass
    else
        fail "expected no marker for exit code ${SIGCASE_RC} when the shutdown sentinel is present, but found one at ${SIGCASE_MARKER}: $(cat "${SIGCASE_MARKER}")"
    fi
done

# ── Case 6c: WITHOUT the sentinel, none of those same exit codes are exempt
#    any more — this is the ticket's core acceptance criterion: OOM/crash-like
#    termination (including 137) must not be silently classified as normal
#    just because of its numeric code ──────────────────────────────────────
for CRASHCASE_RC in 129 130 131 137 143; do
    echo "case: no shutdown sentinel + dockerd exit ${CRASHCASE_RC} writes a marker (no exit code is automatically exempt without the sentinel)"
    CRASHCASE_DIR="${WORK}/no-sentinel-${CRASHCASE_RC}"
    CRASHCASE_BIN="${CRASHCASE_DIR}/bin"
    CRASHCASE_HOME="${CRASHCASE_DIR}/home"
    CRASHCASE_CALL_LOG="${CRASHCASE_DIR}/calls.log"
    CRASHCASE_MARKER="${CRASHCASE_HOME}/${MARKER_NAME}"
    mkdir -p "${CRASHCASE_HOME}"
    : >"${CRASHCASE_CALL_LOG}"
    make_mocks "${CRASHCASE_BIN}" "${CRASHCASE_RC}" "terminated with no shutdown sentinel"
    run_start_dind "${CRASHCASE_BIN}" "${CRASHCASE_HOME}" "${CRASHCASE_CALL_LOG}"

    if [[ -f "${CRASHCASE_MARKER}" ]]; then
        pass
    else
        fail "expected a marker for exit code ${CRASHCASE_RC} with no shutdown sentinel present (OOM/crash must not be silently suppressed by exit code alone), but none was written at ${CRASHCASE_MARKER}"
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
# Root-proof lever: the marker path is pre-created as a *directory*, so the
# final `> "${marker}"` redirection in dind.sh fails with EISDIR — uid 0
# cannot bypass it (`chmod 500` on the parent was a no-op for root, #642).
# start_dind's supersede-only `rm -f "${sentinel}" "${marker}"` cannot remove
# a directory and its failure is ignored (run_start_dind's driver runs
# `bash -c 'source ...; start_dind; wait'` with no `set -e`), so the directory
# survives to the redirection.
mkdir -p "${HOME7}/${MARKER_NAME}"
run_start_dind "${BIN7}" "${HOME7}" "${CALL_LOG7}" "${STDERR7}"

if grep -q 'failed to write dockerd startup-error marker' "${STDERR7}"; then
    pass
else
    fail "expected a stderr warning when the marker write itself fails; stderr: $(cat "${STDERR7}" 2>/dev/null)"
fi

# ── Case 8: still-running fake daemon (never reaped this run) writes no
#    marker — the background subshell must not have touched the marker path
#    at all while dockerd is still inside its own `dockerd >log 2>&1` call ─
echo "case: dockerd still running (not yet exited) writes no marker"
CASE8_DIR="${WORK}/still-running"
BIN8="${CASE8_DIR}/bin"
HOME8="${CASE8_DIR}/home"
CALL_LOG8="${CASE8_DIR}/calls.log"
MARKER8="${HOME8}/${MARKER_NAME}"
mkdir -p "${HOME8}" "${BIN8}"
: >"${CALL_LOG8}"
cat >"${BIN8}/groupadd" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "${BIN8}/groupadd"
cat >"${BIN8}/usermod" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "${BIN8}/usermod"
RELEASE8="${CASE8_DIR}/release"
cat >"${BIN8}/dockerd" <<EOF
#!/bin/bash
printf 'dockerd|%s\n' "\$*" >>"${CALL_LOG8}"
while [[ ! -f "${RELEASE8}" ]]; do sleep 0.05; done
exit 0
EOF
chmod +x "${BIN8}/dockerd"

# shellcheck disable=SC2016  # $1 must expand in the child bash, not here
env -i PATH="${BIN8}:/usr/bin:/bin" CENCI_DIND_HOME_ROOT="${HOME8}" CALL_LOG="${CALL_LOG8}" \
    bash -c 'source "$1"; start_dind' _ "${DIND_LIB}" &
STILL_RUNNING_PID=$!

# Bounded poll (not the final-outcome assertion — just synchronizing with the
# fake actually having started) until the fake dockerd records its own
# invocation, proving the background subshell is genuinely blocked inside
# `dockerd`, not merely that start_dind hasn't run yet.
for _ in $(seq 1 100); do
    [[ -s "${CALL_LOG8}" ]] && break
    sleep 0.05
done

if [[ ! -e "${MARKER8}" ]]; then
    pass
else
    fail "expected no marker while dockerd is still running (never reaped), but found one at ${MARKER8}: $(cat "${MARKER8}")"
fi

touch "${RELEASE8}"
wait "${STILL_RUNNING_PID}"

if [[ ! -e "${MARKER8}" ]]; then
    pass
else
    fail "expected no marker after dockerd's clean exit that followed the still-running phase, but found one: $(cat "${MARKER8}")"
fi

# ── Case 9: start_dind clears a stale marker from a previous container
#    lifetime at entry (supersede-only clearing) — a clean rc==0 exit this
#    run must leave no marker behind, including a pre-existing stale one ──
echo "case: a stale marker from a previous run is cleared at start_dind entry when this run exits cleanly"
CASE9_DIR="${WORK}/stale-marker-cleared"
BIN9="${CASE9_DIR}/bin"
HOME9="${CASE9_DIR}/home"
CALL_LOG9="${CASE9_DIR}/calls.log"
MARKER9="${HOME9}/${MARKER_NAME}"
mkdir -p "${HOME9}"
: >"${CALL_LOG9}"
printf '2026-07-01T00:00:00Z stale failure from a previous container lifetime\n' >"${MARKER9}"
make_mocks "${BIN9}" 0 ""
run_start_dind "${BIN9}" "${HOME9}" "${CALL_LOG9}"

if [[ ! -e "${MARKER9}" ]]; then
    pass
else
    fail "expected the stale marker to be cleared at start_dind entry and not restored by a clean exit, but found: $(cat "${MARKER9}" 2>/dev/null)"
fi

# ── Case 10: a stale sentinel left over from a previous container lifetime
#    must NOT suppress a genuinely new crash this run — start_dind clearing
#    it at entry (supersede-only) is what makes this run's own classification
#    correct regardless of what a prior life left behind ───────────────────
echo "case: a stale shutdown sentinel from a previous run does not suppress a genuinely new crash this run"
CASE10_DIR="${WORK}/stale-sentinel-not-suppressing"
BIN10="${CASE10_DIR}/bin"
HOME10="${CASE10_DIR}/home"
CALL_LOG10="${CASE10_DIR}/calls.log"
MARKER10="${HOME10}/${MARKER_NAME}"
SENTINEL10="${HOME10}/${SENTINEL_NAME}"
mkdir -p "${HOME10}"
: >"${CALL_LOG10}"
touch "${SENTINEL10}"
# This run's fake dockerd does NOT rewrite the sentinel — it exits non-zero
# as if newly crashed, with only the stale (should-be-cleared) sentinel on
# disk from before start_dind ever ran this time.
make_mocks "${BIN10}" 1 "genuinely new crash, unrelated to the earlier stale sentinel"
run_start_dind "${BIN10}" "${HOME10}" "${CALL_LOG10}"

if [[ -f "${MARKER10}" ]]; then
    pass
else
    fail "expected a marker for this run's genuine crash despite a stale sentinel left over from a previous run — start_dind must clear the stale sentinel at entry (supersede-only), not let it leak forward"
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
