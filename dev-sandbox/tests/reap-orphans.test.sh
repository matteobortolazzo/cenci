#!/usr/bin/env bash
# Host-runnable regressions for `agent-sand --reap-orphans` (#291): scans every
# running agent-sand container across all installed runtimes (docker AND
# podman), reads TMUX_PANE from /proc/<pid>/environ of every container-side
# process, and kills (SIGTERM -> grace -> SIGKILL) any process whose recorded
# pane no longer exists on the host tmux server. Uses the mock-PATH + CALLS_FILE
# pattern from launcher-lifecycle.test.sh / installer-clients.test.sh, plus a
# FAILURES/PASSES summary in the style of smoke.test.sh so every case below
# runs (and reports) independently instead of aborting the suite on the first
# failing assertion.
#
# RED PHASE (#291): `--reap-orphans` and reap_orphans() do not exist yet in
# agent-sand, so every case below is expected to fail right now — the flag
# falls through to the unrecognized-argument branch and gets forwarded to the
# agent CLI like any other positional arg, producing a normal (non-reaping)
# launch instead of a scan-and-kill run.
#
# Assumed CLI contract this suite defines for the eventual implementation
# (mirrors the ticket's architectural constraints):
#   - Container enumeration: `<runtime> ps --format '{{.Names}}'`, filtered
#     host-side to running containers matching ^(claude-sand-|codex-sand-)$.
#   - In-container scan: `<runtime> exec -u root <container> sh -c '<POSIX
#     script scanning /proc/*/environ for a line matching ^TMUX_PANE=>'`,
#     emitting one `<pid>\t<pane>` line per process that carries the
#     TMUX_PANE key (pane may be empty), to stdout, exit 0 on success.
#   - Liveness / signaling, always `-u root` (see dev-sandbox/CLAUDE.md's
#     "docker run --user X persists" entrypoint pattern for why):
#       `<runtime> exec -u root <container> kill -TERM <pid>`
#       `<runtime> exec -u root <container> kill -0    <pid>`   (0 = alive)
#       `<runtime> exec -u root <container> kill -KILL <pid>`
#   - Host tmux liveness: `tmux list-panes -a -F '#{pane_id}'`, capturing
#     stdout/stderr/exit separately. Non-zero exit + stderr matching
#     "no server running" -> proceed with an empty live-pane set and print a
#     note containing "No tmux server detected". Any other non-zero exit is a
#     hard error (exit non-zero, reap nothing, print an "Error:"-prefixed
#     message, matching this script's existing error convention).
#   - Output: one `reaped\t<container>\t<pid>\t<pane>` line per reaped
#     process, plus a final count line ("Reaped N orphaned process(es)." /
#     "No orphaned processes found.").
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
REPO_SLUG="$(slugify "$(basename "${REPO_ROOT}")")"
MAIN_CONTAINER="claude-sand-${REPO_SLUG}"

TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/calls"
SCAN_DIR="${TEST_ROOT}/scan"
LIVENESS_DIR="${TEST_ROOT}/liveness"
mkdir -p "${BIN_DIR}" "${SCAN_DIR}" "${LIVENESS_DIR}" "${TEST_ROOT}/home/.claude"
touch "${TEST_ROOT}/home/.claude/.credentials.json"

# ── Mock docker/podman ──────────────────────────────────────────────
# A single script, symlinked as both docker and podman (basename "$0" tells
# them apart) so runtime-specific container lists / call logs can be asserted
# independently (case 7: both runtimes scanned).
cat > "${BIN_DIR}/docker" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail

RUNTIME_NAME="$(basename "$0")"
{
    printf '%s ' "${RUNTIME_NAME}"
    printf '%q ' "$@"
    printf '\n'
} >> "${CALLS_FILE}"

case "${1:-}" in
    image)
        exit 0
        ;;
    inspect)
        # Lifecycle-label / mount lookups used by the pre-existing (unrelated
        # to --reap-orphans) launch path. Always answer empty so
        # wait_until_ready / warn_if_unwired take their fast no-op branches.
        exit 0
        ;;
    ps)
        if [[ "${MOCK_PS_FAIL:-}" == "${RUNTIME_NAME}" ]]; then
            echo "mock ${RUNTIME_NAME} ps: simulated runtime failure" >&2
            exit 5
        fi
        if [[ "${RUNTIME_NAME}" == docker ]]; then
            printf '%s\n' "${MOCK_DOCKER_CONTAINERS:-}"
        else
            printf '%s\n' "${MOCK_PODMAN_CONTAINERS:-}"
        fi
        ;;
    rm)
        exit 0
        ;;
    run)
        printf '%s\n' sandbox-container-id
        ;;
    exec)
        shift
        USER_FLAG=""
        CONTAINER=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                -u) USER_FLAG="$2"; shift 2 ;;
                -e) shift 2 ;;
                -it|-i|-t) shift ;;
                *) CONTAINER="$1"; shift; break ;;
            esac
        done
        CMD="$(printf '%s ' "$@")"
        case "${CMD}" in
            *"TMUX_PANE="*)
                # In-container /proc/*/environ scan. This mock is a dumb,
                # canned data source keyed by container name (SCAN_DIR) — all
                # decision logic (skip-empty-pane, skip-live-pane,
                # reap-dead-pane, TERM->KILL escalation, no-tmux handling)
                # lives host-side in reap_orphans(), never here.
                if [[ "${USER_FLAG}" != root ]]; then
                    echo "mock: TMUX_PANE scan on ${CONTAINER} must run as -u root" >&2
                    exit 6
                fi
                if [[ "${MOCK_SCAN_FAIL:-}" == "${CONTAINER}" ]]; then
                    echo "mock ${RUNTIME_NAME} exec: simulated scan failure on ${CONTAINER}" >&2
                    exit 7
                fi
                [[ -f "${SCAN_DIR}/${CONTAINER}" ]] && cat "${SCAN_DIR}/${CONTAINER}"
                exit 0
                ;;
            *"kill -TERM"*)
                if [[ "${USER_FLAG}" != root ]]; then
                    echo "mock: kill on ${CONTAINER} must run as -u root" >&2
                    exit 6
                fi
                if [[ "${MOCK_TERM_FAIL:-}" == "${CONTAINER}" ]]; then
                    echo "mock ${RUNTIME_NAME} exec: simulated kill -TERM failure on ${CONTAINER}" >&2
                    exit 8
                fi
                # Real SIGTERM semantics are simulated via the preset liveness
                # state file (set_alive/set_dead in the test), not decided here.
                exit 0
                ;;
            *"kill -KILL"*)
                if [[ "${USER_FLAG}" != root ]]; then
                    echo "mock: kill on ${CONTAINER} must run as -u root" >&2
                    exit 6
                fi
                if [[ "${MOCK_KILL_FAIL:-}" == "${CONTAINER}" ]]; then
                    echo "mock ${RUNTIME_NAME} exec: simulated kill -KILL failure on ${CONTAINER}" >&2
                    exit 9
                fi
                PID="$(grep -oE '[0-9]+' <<<"${CMD}" | tail -1)"
                printf 'dead' > "${LIVENESS_DIR}/${PID}"
                exit 0
                ;;
            *"kill -0"*)
                if [[ "${USER_FLAG}" != root ]]; then
                    echo "mock: kill on ${CONTAINER} must run as -u root" >&2
                    exit 6
                fi
                PID="$(grep -oE '[0-9]+' <<<"${CMD}" | tail -1)"
                STATE_FILE="${LIVENESS_DIR}/${PID}"
                if [[ -f "${STATE_FILE}" ]] && [[ "$(cat "${STATE_FILE}")" == dead ]]; then
                    exit 1
                fi
                exit 0
                ;;
            *)
                # Readiness probe (test -e /tmp/agent-sand-ready) or the
                # actual agent launch exec — always succeeds.
                exit 0
                ;;
        esac
        ;;
    *)
        exit 0
        ;;
esac
MOCK
chmod +x "${BIN_DIR}/docker"
ln -s docker "${BIN_DIR}/podman"

# ── Mock tmux ────────────────────────────────────────────────────────
cat > "${BIN_DIR}/tmux" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
{
    printf 'tmux '
    printf '%q ' "$@"
    printf '\n'
} >> "${CALLS_FILE}"

case "${MOCK_TMUX_MODE:-ok}" in
    ok)
        printf '%s\n' "${MOCK_LIVE_PANES:-}"
        exit 0
        ;;
    noserver)
        echo "no server running on /tmp/tmux-1000/default" >&2
        exit 1
        ;;
    error)
        echo "tmux: error connecting to /tmp/tmux-1000/default (unrelated failure)" >&2
        exit 1
        ;;
    *)
        exit 0
        ;;
esac
MOCK
chmod +x "${BIN_DIR}/tmux"

# ── Mock claude (only needed to resolve AGENT_BIN for the bind-mount) ──
cat > "${BIN_DIR}/claude" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
chmod +x "${BIN_DIR}/claude"

export CALLS_FILE SCAN_DIR LIVENESS_DIR
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

# ── Test helpers ─────────────────────────────────────────────────────
FAILURES=0
PASSES=0
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

reset_state() {
    : > "${CALLS_FILE}"
    rm -rf "${SCAN_DIR}" "${LIVENESS_DIR}"
    mkdir -p "${SCAN_DIR}" "${LIVENESS_DIR}"
    export MOCK_PODMAN_CONTAINERS="${MAIN_CONTAINER}"
    export MOCK_DOCKER_CONTAINERS=""
    export MOCK_PS_FAIL=""
    export MOCK_SCAN_FAIL=""
    export MOCK_TERM_FAIL=""
    export MOCK_KILL_FAIL=""
    export MOCK_TMUX_MODE="ok"
    export MOCK_LIVE_PANES=""
    export AGENT_SAND_REAP_GRACE_SECS=0
}

add_podman_container() {
    MOCK_PODMAN_CONTAINERS="${MOCK_PODMAN_CONTAINERS}
${1}"
    export MOCK_PODMAN_CONTAINERS
}

add_docker_container() {
    MOCK_DOCKER_CONTAINERS="${MOCK_DOCKER_CONTAINERS}
${1}"
    export MOCK_DOCKER_CONTAINERS
}

write_scan() {
    local container="$1"
    shift
    printf '%s\n' "$@" >> "${SCAN_DIR}/${container}"
}

set_alive() { printf 'alive' > "${LIVENESS_DIR}/$1"; }
set_dead() { printf 'dead' > "${LIVENESS_DIR}/$1"; }

reaped_line() {
    printf 'reaped\t%s\t%s\t%s' "$1" "$2" "$3"
}

run_reap() {
    OUTPUT="$("${SANDBOX_DIR}/agent-sand" --reap-orphans 2>&1)"
    EXIT_CODE=$?
}

assert_contains() {
    local needle="$1"
    if grep -Fq -- "${needle}" <<<"${OUTPUT}"; then
        pass
    else
        fail "expected output to contain: $(printf '%q' "${needle}")"
    fi
}

assert_not_contains() {
    local needle="$1"
    if grep -Fq -- "${needle}" <<<"${OUTPUT}"; then
        fail "did not expect output to contain: $(printf '%q' "${needle}")"
    else
        pass
    fi
}

assert_calls_contains() {
    local needle="$1"
    if grep -Fq -- "${needle}" "${CALLS_FILE}"; then
        pass
    else
        fail "expected calls log to contain: ${needle}"
    fi
}

assert_exit_zero() {
    if [[ "${EXIT_CODE}" -eq 0 ]]; then
        pass
    else
        fail "expected exit 0, got ${EXIT_CODE} (output: ${OUTPUT})"
    fi
}

assert_exit_nonzero() {
    if [[ "${EXIT_CODE}" -ne 0 ]]; then
        pass
    else
        fail "expected a non-zero exit, got 0"
    fi
}

echo "reap-orphans.test.sh"

# ---------------------------------------------------------------------------
case_1_orphan_termed() {
    echo "case: orphan (dead-pane) pid is SIGTERM'd and reported reaped"
    reset_state
    local container="claude-sand-orphan1"
    add_podman_container "${container}"
    write_scan "${container}" $'5001\tpane-dead-1'
    export MOCK_LIVE_PANES="pane-live-x"
    set_dead 5001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5001 pane-dead-1)"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 5001"
}

# ---------------------------------------------------------------------------
case_2_term_resistant_escalates_to_kill() {
    echo "case: a pid that ignores SIGTERM is escalated to SIGKILL after the grace period"
    reset_state
    local container="claude-sand-orphan2"
    add_podman_container "${container}"
    write_scan "${container}" $'5002\tpane-dead-2'
    export MOCK_LIVE_PANES="pane-live-x"
    set_alive 5002
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5002 pane-dead-2)"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 5002"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 5002"
}

# ---------------------------------------------------------------------------
case_3_empty_pane_never_signaled() {
    echo "case: empty-TMUX_PANE pid is never signaled, even when a paired orphan in the same container is reaped"
    reset_state
    local container="claude-sand-orphan3"
    add_podman_container "${container}"
    write_scan "${container}" $'5003\t' $'5004\tpane-dead-3'
    export MOCK_LIVE_PANES="pane-live-x"
    set_dead 5004
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5004 pane-dead-3)"
    local skip_needle
    skip_needle=$'\t5003\t'
    assert_not_contains "${skip_needle}"
    assert_contains "Reaped 1 orphaned process(es)."
}

# ---------------------------------------------------------------------------
case_4_live_pane_never_signaled() {
    echo "case: live-pane pid is never signaled, even when a paired orphan in the same container is reaped"
    reset_state
    local container="claude-sand-orphan4"
    add_podman_container "${container}"
    write_scan "${container}" $'5005\tpane-live-y' $'5006\tpane-dead-4'
    export MOCK_LIVE_PANES="pane-live-y"
    set_dead 5006
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5006 pane-dead-4)"
    local skip_needle
    skip_needle=$'\t5005\t'
    assert_not_contains "${skip_needle}"
    assert_contains "Reaped 1 orphaned process(es)."
}

# ---------------------------------------------------------------------------
case_5_no_tmux_server_reaps_everything() {
    echo "case: no tmux server running treats every TMUX_PANE-carrying pid as orphaned, with an explicit note"
    reset_state
    local container="claude-sand-notmux"
    add_podman_container "${container}"
    write_scan "${container}" $'6001\tpane-a' $'6002\tpane-b' $'6003\t'
    export MOCK_TMUX_MODE="noserver"
    set_dead 6001
    set_dead 6002
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 6001 pane-a)"
    assert_contains "$(reaped_line "${container}" 6002 pane-b)"
    local skip_needle
    skip_needle=$'\t6003\t'
    assert_not_contains "${skip_needle}"
    assert_contains "No tmux server detected"
}

# ---------------------------------------------------------------------------
case_6_genuine_tmux_error_hard_fails() {
    echo "case: a genuine tmux error (not 'no server running') hard-fails and reaps nothing"
    reset_state
    local container="claude-sand-tmuxerr"
    add_podman_container "${container}"
    write_scan "${container}" $'6101\tpane-c'
    export MOCK_TMUX_MODE="error"
    set_dead 6101
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
    local reaped_needle
    reaped_needle=$'reaped\t'
    assert_not_contains "${reaped_needle}"
}

# ---------------------------------------------------------------------------
case_7_both_runtimes_scanned() {
    echo "case: both docker and podman are scanned when both have matching running containers"
    reset_state
    local docker_container="claude-sand-orphan-docker"
    local podman_container="codex-sand-orphan-podman"
    add_docker_container "${docker_container}"
    add_podman_container "${podman_container}"
    write_scan "${docker_container}" $'7001\tpane-d'
    write_scan "${podman_container}" $'7002\tpane-p'
    export MOCK_LIVE_PANES="pane-live-z"
    set_dead 7001
    set_dead 7002
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${docker_container}" 7001 pane-d)"
    assert_contains "$(reaped_line "${podman_container}" 7002 pane-p)"
    assert_calls_contains "docker exec -u root ${docker_container}"
    assert_calls_contains "podman exec -u root ${podman_container}"
}

# ---------------------------------------------------------------------------
case_8_nothing_to_reap() {
    echo "case: a no-op run (all panes live) exits 0 with a clear nothing-to-reap message"
    reset_state
    local container="claude-sand-clean"
    add_podman_container "${container}"
    write_scan "${container}" $'8001\tpane-live-clean'
    export MOCK_LIVE_PANES="pane-live-clean"
    run_reap
    assert_exit_zero
    assert_contains "No orphaned processes found."
    local reaped_needle
    reaped_needle=$'reaped\t'
    assert_not_contains "${reaped_needle}"
}

# ---------------------------------------------------------------------------
case_9_genuine_exec_failure_hard_fails() {
    echo "case: a genuine exec (scan) failure surfaces as a non-zero exit with a clear error, nothing reaped"
    reset_state
    local container="claude-sand-failcase"
    add_podman_container "${container}"
    write_scan "${container}" $'9001\tpane-e'
    export MOCK_SCAN_FAIL="${container}"
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
    local reaped_needle
    reaped_needle=$'reaped\t'
    assert_not_contains "${reaped_needle}"
}

case_10_genuine_term_failure_hard_fails() {
    echo "case: a genuine kill -TERM failure surfaces as a non-zero exit with a clear error, nothing reaped"
    reset_state
    local container="claude-sand-termfail"
    add_podman_container "${container}"
    write_scan "${container}" $'10001\tpane-f'
    export MOCK_LIVE_PANES="pane-live-x"
    export MOCK_TERM_FAIL="${container}"
    set_dead 10001
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
    local reaped_needle
    reaped_needle=$'reaped\t'
    assert_not_contains "${reaped_needle}"
}

case_11_genuine_kill_failure_hard_fails() {
    echo "case: a genuine kill -KILL failure surfaces as a non-zero exit with a clear error"
    reset_state
    local container="claude-sand-killfail"
    add_podman_container "${container}"
    write_scan "${container}" $'11001\tpane-g'
    export MOCK_LIVE_PANES="pane-live-x"
    export MOCK_KILL_FAIL="${container}"
    set_alive 11001
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
}

case_1_orphan_termed
case_2_term_resistant_escalates_to_kill
case_3_empty_pane_never_signaled
case_4_live_pane_never_signaled
case_5_no_tmux_server_reaps_everything
case_6_genuine_tmux_error_hard_fails
case_7_both_runtimes_scanned
case_8_nothing_to_reap
case_9_genuine_exec_failure_hard_fails
case_10_genuine_term_failure_hard_fails
case_11_genuine_kill_failure_hard_fails

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
