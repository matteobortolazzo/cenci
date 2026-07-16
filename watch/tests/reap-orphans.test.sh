#!/usr/bin/env bash
# Host-runnable regressions for `cenci sandbox reap-orphans` (#291): scans
# every running sandbox container across all installed runtimes (docker AND
# podman), reads TMUX_PANE from /proc/<pid>/environ of every container-side
# process, and kills (SIGTERM -> grace -> SIGKILL) any process whose recorded
# pane no longer exists on the host tmux server. Uses a mock-PATH + CALLS_FILE
# pattern, plus a FAILURES/PASSES summary so every case below runs (and
# reports) independently instead of aborting the suite on the first failing
# assertion.
#
# Requires CENCI_BIN pointing at a built cenci binary (e.g.
# `cd watch && make build && CENCI_BIN=$PWD/cenci bash tests/reap-orphans.test.sh`).
#
# The CLI contract this suite pins (mirrors the original ticket's
# architectural constraints):
#   - Container enumeration: `<runtime> ps --format '{{.Names}}'`, filtered
#     host-side to running containers matching ^(claude-cenci-|codex-cenci-)$.
#   - In-container scan: `<runtime> exec -u dev <container> sh -c '<POSIX
#     script scanning /proc/*/environ for a line matching ^TMUX_PANE=>'`,
#     emitting one `<pid>\t<pane>\t<start>` line per process that carries the
#     TMUX_PANE key (pane may be empty; <start> is that process's start time,
#     read from /proc/<pid>/stat field 22 in the same scan pass), to stdout,
#     exit 0 on success. Scan runs as the fixed `dev` sandbox agent user so
#     same-uid /proc/<pid>/environ reads succeed without CAP_SYS_PTRACE
#     (root lacks it under docker's default caps). For a process whose environ
#     failed to read (and still exists), the scan instead emits a
#     `__UNREADABLE__\t<pid>` marker line (#361); the host counts these per
#     container, excludes pid 1 (root-owned init, always unreadable by design
#     on a healthy container), and prints an always-on
#     "Note: <n> process environ(s) in container <container> were unreadable
#     during the -u dev scan; ..." diagnostic when the excluding-pid-1 count is
#     > 0, so a future scan-user/agent-UID drift is visible instead of
#     masquerading as "no orphans found".
#   - Pane-format validation (host-side, after the scan): a non-empty <pane>
#     must match tmux's real pane-id format `^%[0-9]+$` before being treated
#     as a live-ownership record. A malformed value (e.g. `%foo`, `bad`,
#     `%1x`) is treated like a missing/empty pane (never signaled) and logged
#     with a distinct note: "Note: process <pid> in container <container>
#     has a malformed TMUX_PANE value; skipping."
#   - Liveness / signaling, always `-u root` (see sandbox/CLAUDE.md's
#     "docker run --user X persists" entrypoint pattern for why):
#       `<runtime> exec -u root <container> kill -TERM <pid>`
#       Pre-SIGKILL probe (replaces the ambiguous `kill -0`): an in-container
#       `sh -c '<script>' _ <pid>` that reads /proc/<pid>/stat field 22,
#       passed the pid as an argument (never interpolated), and always exits
#       0 if it ran at all. It prints the process's *current* start time on
#       stdout if the pid is still alive, or the sentinel `__GONE__` if it is
#       not. Host-side classification:
#         - `<runtime> exec` itself exits non-zero -> container-exec
#           transport failure (daemon unreachable, container gone, etc.); a
#           hard error ("Error: ..."), reap_orphans() returns non-zero,
#           distinct from a genuine "no such process" result.
#         - stdout is `__GONE__` -> process already gone; no-op, not an
#           error.
#         - stdout is a start time that differs from the one recorded at
#           scan time -> the pid was reused by an unrelated process during
#           the grace window; skip SIGKILL, treat as already-gone, not an
#           error.
#         - stdout matches the recorded start time -> genuinely still the
#           same process; proceed to SIGKILL.
#       `<runtime> exec -u root <container> kill -KILL <pid>`
#   - Host tmux liveness: `tmux list-panes -a -F '#{pane_id}'`, capturing
#     stdout/stderr/exit separately. Non-zero exit + stderr matching
#     "no server running" -> proceed with an empty live-pane set and print a
#     note containing "No tmux server detected". Any other non-zero exit is a
#     hard error (exit non-zero, reap nothing, print an "Error:"-prefixed
#     message, matching this script's existing error convention).
#   - Output: one `reaped\t<container>\t<pid>\t<pane>` line per reaped
#     process (SIGTERM already sent, regardless of later grace-window
#     outcome), plus a final count line ("Reaped N orphaned process(es)." /
#     "No orphaned processes found.").
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -z "${CENCI_BIN:-}" || ! -x "${CENCI_BIN}" ]]; then
    echo "Error: CENCI_BIN must point at a built cenci binary (cd watch && make build && CENCI_BIN=\$PWD/cenci bash tests/reap-orphans.test.sh)." >&2
    exit 1
fi

# slugify <input>: lowercase and replace each character outside [a-z0-9_.-]
# with a dash — inlined from the launcher's Slugify (one place in code, this
# local copy exists only to derive a realistic container name for fixtures).
slugify() {
    echo "$1" | LC_ALL=C.UTF-8 tr '[:upper:]' '[:lower:]' | LC_ALL=C.UTF-8 sed -E 's/[^a-z0-9_.-]/-/g'
}

REPO_ROOT="$(git -C "${SCRIPT_DIR}" rev-parse --show-toplevel)"
REPO_SLUG="$(slugify "$(basename "${REPO_ROOT}")")"
MAIN_CONTAINER="claude-cenci-${REPO_SLUG}"

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
                # decision logic (skip-empty-pane, skip-malformed-pane,
                # skip-live-pane, reap-dead-pane, TERM->KILL escalation,
                # PID-reuse detection, no-tmux handling) lives host-side in
                # reap_orphans(), never here. Fixture lines are three
                # tab-separated columns: <pid>\t<pane>\t<start>.
                if [[ "${USER_FLAG}" != dev ]]; then
                    echo "mock: TMUX_PANE scan on ${CONTAINER} must run as -u dev" >&2
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
                # state file (set_live_start/set_gone in the test), not
                # decided here.
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
                printf 'gone' > "${LIVENESS_DIR}/${PID}"
                exit 0
                ;;
            *"__GONE__"*)
                # Pre-SIGKILL probe (replaces kill -0): a canned in-container
                # /proc/<pid>/stat read. Always exits 0 unless
                # MOCK_LIVENESS_FAIL simulates a container-exec transport
                # failure (daemon unreachable / container gone). On success,
                # prints the pid's current start time, or the sentinel
                # __GONE__ if the state was set via set_gone. State is keyed
                # by pid via LIVENESS_DIR, same as the old kill -0 model, but
                # start-time-aware (see set_live_start/set_gone below).
                if [[ "${USER_FLAG}" != root ]]; then
                    echo "mock: kill on ${CONTAINER} must run as -u root" >&2
                    exit 6
                fi
                if [[ "${MOCK_LIVENESS_FAIL:-}" == "${CONTAINER}" ]]; then
                    echo "mock ${RUNTIME_NAME} exec: simulated liveness-probe transport failure on ${CONTAINER}" >&2
                    exit 10
                fi
                PID="$(grep -oE '[0-9]+' <<<"${CMD}" | tail -1)"
                STATE_FILE="${LIVENESS_DIR}/${PID}"
                if [[ -f "${STATE_FILE}" ]]; then
                    STATE="$(cat "${STATE_FILE}")"
                    case "${STATE}" in
                        gone) printf '__GONE__\n' ;;
                        live:*) printf '%s\n' "${STATE#live:}" ;;
                        nostat) printf '__NOSTAT__\n' ;;
                        *) printf '__GONE__\n' ;;
                    esac
                else
                    printf '__GONE__\n'
                fi
                exit 0
                ;;
            *)
                # Readiness probe (test -e /tmp/cenci-ready) or the
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

# No `claude` mock: the reaper never resolves a host agent binary (both agent
# CLIs are baked into the image, not bind-mounted from the host).

export CALLS_FILE SCAN_DIR LIVENESS_DIR
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

# ── Test helpers ─────────────────────────────────────────────────────
# shellcheck source=../../sandbox/tests/lib/assert.sh
source "${REPO_ROOT}/sandbox/tests/lib/assert.sh"

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
    export MOCK_LIVENESS_FAIL=""
    export MOCK_TMUX_MODE="ok"
    export MOCK_LIVE_PANES=""
    export CENCI_SANDBOX_REAP_GRACE_SECS=0
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

# set_live_start <pid> <start>: pid is still alive at the pre-SIGKILL probe;
# the probe reports <start> as its current /proc/<pid>/stat start time. Pair
# with a matching <start> in the scan fixture for "genuinely the same
# process" (escalates to SIGKILL); use a mismatched <start> to simulate a
# PID-reuse during the grace window (SIGKILL skipped).
set_live_start() { printf 'live:%s' "$2" > "${LIVENESS_DIR}/$1"; }
# set_gone <pid>: pid is gone by the time of the pre-SIGKILL probe; the
# probe reports the __GONE__ sentinel (no-op, not an error).
set_gone() { printf 'gone' > "${LIVENESS_DIR}/$1"; }
# set_nostat <pid>: pid is still alive at the pre-SIGKILL probe, but its
# /proc/<pid>/stat field 22 is unverifiable; the probe reports the
# __NOSTAT__ sentinel (neither __GONE__ nor a parseable start time).
set_nostat() { printf 'nostat' > "${LIVENESS_DIR}/$1"; }

reaped_line() {
    printf 'reaped\t%s\t%s\t%s' "$1" "$2" "$3"
}

run_reap() {
    OUTPUT="$("${CENCI_BIN}" sandbox reap-orphans 2>&1)"
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

assert_calls_not_contains() {
    local needle="$1"
    if grep -Fq -- "${needle}" "${CALLS_FILE}"; then
        fail "did not expect calls log to contain: ${needle}"
    else
        pass
    fi
}

# assert_calls_not_match <extended-regex>: like assert_calls_not_contains but
# takes an ERE instead of a fixed string. Needed when a single fixed-string
# needle would also match an unrelated, legitimate call (e.g. distinguishing
# the TMUX_PANE scan's "-u root ... sh -c" shape, which must never occur,
# from the pre-SIGKILL liveness probe's "-u root ... sh -c" shape, which
# always does).
assert_calls_not_match() {
    local pattern="$1"
    if grep -Eq -- "${pattern}" "${CALLS_FILE}"; then
        fail "did not expect calls log to match: ${pattern}"
    else
        pass
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
    local container="claude-cenci-orphan1"
    add_podman_container "${container}"
    write_scan "${container}" $'5001\t%1\t1000'
    export MOCK_LIVE_PANES="%99"
    set_gone 5001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5001 "%1")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 5001"
}

# ---------------------------------------------------------------------------
case_2_term_resistant_escalates_to_kill() {
    echo "case: a pid that ignores SIGTERM is escalated to SIGKILL after the grace period"
    reset_state
    local container="claude-cenci-orphan2"
    add_podman_container "${container}"
    write_scan "${container}" $'5002\t%2\t2000'
    export MOCK_LIVE_PANES="%99"
    set_live_start 5002 2000
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5002 "%2")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 5002"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 5002"
}

# ---------------------------------------------------------------------------
case_3_empty_pane_never_signaled() {
    echo "case: empty-TMUX_PANE pid is never signaled, even when a paired orphan in the same container is reaped"
    reset_state
    local container="claude-cenci-orphan3"
    add_podman_container "${container}"
    write_scan "${container}" $'5003\t\t' $'5004\t%3\t3000'
    export MOCK_LIVE_PANES="%99"
    set_gone 5004
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5004 "%3")"
    local skip_needle
    skip_needle=$'\t5003\t'
    assert_not_contains "${skip_needle}"
    assert_contains "Reaped 1 orphaned process(es)."
}

# ---------------------------------------------------------------------------
case_4_live_pane_never_signaled() {
    echo "case: live-pane pid is never signaled, even when a paired orphan in the same container is reaped"
    reset_state
    local container="claude-cenci-orphan4"
    add_podman_container "${container}"
    write_scan "${container}" $'5005\t%4\t4000' $'5006\t%5\t5000'
    export MOCK_LIVE_PANES="%4"
    set_gone 5006
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 5006 "%5")"
    local skip_needle
    skip_needle=$'\t5005\t'
    assert_not_contains "${skip_needle}"
    assert_contains "Reaped 1 orphaned process(es)."
}

# ---------------------------------------------------------------------------
case_5_no_tmux_server_reaps_everything() {
    echo "case: no tmux server running treats every TMUX_PANE-carrying pid as orphaned, with an explicit note"
    reset_state
    local container="claude-cenci-notmux"
    add_podman_container "${container}"
    write_scan "${container}" $'6001\t%6\t6000' $'6002\t%7\t6001' $'6003\t\t'
    export MOCK_TMUX_MODE="noserver"
    set_gone 6001
    set_gone 6002
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 6001 "%6")"
    assert_contains "$(reaped_line "${container}" 6002 "%7")"
    local skip_needle
    skip_needle=$'\t6003\t'
    assert_not_contains "${skip_needle}"
    assert_contains "No tmux server detected"
}

# ---------------------------------------------------------------------------
case_6_genuine_tmux_error_hard_fails() {
    echo "case: a genuine tmux error (not 'no server running') hard-fails and reaps nothing"
    reset_state
    local container="claude-cenci-tmuxerr"
    add_podman_container "${container}"
    write_scan "${container}" $'6101\t%8\t6100'
    export MOCK_TMUX_MODE="error"
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
    local docker_container="claude-cenci-orphan-docker"
    local podman_container="codex-cenci-orphan-podman"
    add_docker_container "${docker_container}"
    add_podman_container "${podman_container}"
    write_scan "${docker_container}" $'7001\t%9\t7000'
    write_scan "${podman_container}" $'7002\t%10\t7001'
    export MOCK_LIVE_PANES="%20"
    set_gone 7001
    set_gone 7002
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${docker_container}" 7001 "%9")"
    assert_contains "$(reaped_line "${podman_container}" 7002 "%10")"
    assert_calls_contains "docker exec -u root ${docker_container}"
    assert_calls_contains "podman exec -u root ${podman_container}"
}

# ---------------------------------------------------------------------------
case_8_nothing_to_reap() {
    echo "case: a no-op run (all panes live) exits 0 with a clear nothing-to-reap message"
    reset_state
    local container="claude-cenci-clean"
    add_podman_container "${container}"
    write_scan "${container}" $'8001\t%11\t8000'
    export MOCK_LIVE_PANES="%11"
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
    local container="claude-cenci-failcase"
    add_podman_container "${container}"
    write_scan "${container}" $'9001\t%12\t9000'
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
    local container="claude-cenci-termfail"
    add_podman_container "${container}"
    write_scan "${container}" $'10001\t%13\t10000'
    export MOCK_LIVE_PANES="%99"
    export MOCK_TERM_FAIL="${container}"
    # The process is still alive at the post-failure probe, so this is a
    # genuine delivery failure — not the benign exited-before-TERM race
    # (case 18), which the probe classifies via __GONE__.
    set_live_start 10001 10000
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
    local container="claude-cenci-killfail"
    add_podman_container "${container}"
    write_scan "${container}" $'11001\t%14\t11000'
    export MOCK_LIVE_PANES="%99"
    export MOCK_KILL_FAIL="${container}"
    set_live_start 11001 11000
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
}

# ---------------------------------------------------------------------------
case_12_malformed_pane_skipped() {
    echo "case: malformed TMUX_PANE values (not matching ^%[0-9]+\$) are never signaled, and logged with a distinct note"
    reset_state
    local container="claude-cenci-malformed"
    add_podman_container "${container}"
    write_scan "${container}" \
        $'12001\t%foo\t1000' \
        $'12002\tbad\t1000' \
        $'12003\t%1x\t1000' \
        $'12004\t%15\t1000'
    export MOCK_LIVE_PANES="%99"
    set_gone 12004
    run_reap
    assert_exit_zero
    assert_calls_not_contains "kill -TERM 12001"
    assert_calls_not_contains "kill -TERM 12002"
    assert_calls_not_contains "kill -TERM 12003"
    local skip_needle
    skip_needle=$'\t12001\t'
    assert_not_contains "${skip_needle}"
    assert_contains "Note: process 12001 in container ${container} has a malformed TMUX_PANE value; skipping."
    assert_contains "Note: process 12002 in container ${container} has a malformed TMUX_PANE value; skipping."
    assert_contains "Note: process 12003 in container ${container} has a malformed TMUX_PANE value; skipping."
    assert_contains "$(reaped_line "${container}" 12004 "%15")"
}

# ---------------------------------------------------------------------------
case_13_pid_reuse_during_grace_skips_kill() {
    echo "case: a PID reused by an unrelated process during the grace window is not SIGKILL'd (identity mismatch)"
    reset_state
    local container="claude-cenci-reused"
    add_podman_container "${container}"
    write_scan "${container}" $'13001\t%16\t1000'
    export MOCK_LIVE_PANES="%99"
    # Recorded start (scan time) is 1000; the pre-SIGKILL probe reports 9999
    # -- an unrelated process now owns pid 13001. SIGTERM was already sent
    # (and is reported reaped) before the mismatch is discovered.
    set_live_start 13001 9999
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 13001 "%16")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 13001"
    assert_calls_not_contains "kill -KILL 13001"
}

# ---------------------------------------------------------------------------
case_14_liveness_transport_failure_hard_fails() {
    echo "case: a container-exec transport failure at the pre-SIGKILL probe surfaces as an Error, distinct from a genuine __GONE__ no-op"
    reset_state
    local container="claude-cenci-transportfail"
    add_podman_container "${container}"
    write_scan "${container}" $'14001\t%17\t1000'
    export MOCK_LIVE_PANES="%99"
    export MOCK_LIVENESS_FAIL="${container}"
    run_reap
    assert_exit_nonzero
    assert_contains "Error:"
    # SIGTERM had already succeeded and been reported before the probe's
    # transport failure -- this is a genuine mid-escalation error, not a
    # silent "process already gone" (__GONE__) no-op, and the run must not
    # reach the final success summary line.
    assert_contains "$(reaped_line "${container}" 14001 "%17")"
    assert_not_contains "No orphaned processes found."
}

# ---------------------------------------------------------------------------
case_15_corrupted_scan_start_falls_back_to_kill() {
    echo "case: a non-numeric recorded start (scan-time field-22 corruption, e.g. via a crafted comm) does not block SIGKILL; falls back to best-effort with a distinct note"
    reset_state
    local container="claude-cenci-corruptscan"
    add_podman_container "${container}"
    # Recorded start is corrupted (non-numeric) at scan time; the pre-SIGKILL
    # probe reports a genuine numeric start time. A strictly-numeric identity
    # comparison can't trust the corrupted recorded value, so it must fall
    # back to best-effort SIGKILL instead of treating this as a PID-reuse
    # mismatch.
    write_scan "${container}" $'15001\t%18\t9x9'
    export MOCK_LIVE_PANES="%99"
    set_live_start 15001 15000
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 15001 "%18")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 15001"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 15001"
    assert_contains "Note: process 15001 in container ${container} has an unparseable start-time value; proceeding best-effort."
}

# ---------------------------------------------------------------------------
case_16_corrupted_probe_output_falls_back_to_kill() {
    echo "case: a non-numeric pre-SIGKILL probe output (field-22 corruption via a crafted comm on the live process) does not block SIGKILL; falls back to best-effort with a distinct note"
    reset_state
    local container="claude-cenci-corruptprobe"
    add_podman_container "${container}"
    write_scan "${container}" $'16001\t%19\t16000'
    export MOCK_LIVE_PANES="%99"
    # Probe output is corrupted (non-numeric, and neither __GONE__ nor
    # __NOSTAT__) -- a strictly-numeric identity comparison can't trust it,
    # so it must fall back to best-effort SIGKILL instead of treating this as
    # a PID-reuse mismatch.
    set_live_start 16001 "abc123"
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 16001 "%19")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 16001"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 16001"
    assert_contains "Note: process 16001 in container ${container} has an unparseable start-time value; proceeding best-effort."
}

# ---------------------------------------------------------------------------
case_17_container_init_never_signaled() {
    echo "case: pid 1 (container init) is never signaled even with a dead pane; a paired orphan in the same container is still reaped"
    reset_state
    local container="claude-cenci-init"
    add_podman_container "${container}"
    # Containers created by pre-#356 launchers baked the creating pane's id
    # into the container-lifetime env, so pid 1 carries a stale TMUX_PANE once
    # that pane closes. Signaling init destroys the whole shared container and
    # every agent session exec'd into it. The skip precedes pane-liveness
    # classification entirely, so it also holds in no-tmux-server mode.
    write_scan "${container}" $'1\t%20\t50' $'27001\t%21\t17000'
    export MOCK_LIVE_PANES="%99"
    set_gone 27001
    run_reap
    assert_exit_zero
    assert_contains "Note: process 1 in container ${container} is the container init; skipping."
    assert_contains "$(reaped_line "${container}" 27001 "%21")"
    assert_calls_not_contains "podman exec -u root ${container} kill -TERM 1"
    assert_calls_not_contains "podman exec -u root ${container} kill -KILL 1"
    assert_contains "Reaped 1 orphaned process(es)."
}

# ---------------------------------------------------------------------------
case_18_gone_before_term_is_benign() {
    echo "case: a pid that exits between the scan and the SIGTERM is skipped with a note, not a hard error"
    reset_state
    local container="claude-cenci-goneterm"
    add_podman_container "${container}"
    # Guaranteed occurrence on containers created by pre-#356 launchers: the
    # scan's own in-container sh inherits the stale creation-baked TMUX_PANE
    # and reports itself, but has always exited by kill time. Must not abort
    # the run (which would leave later containers unscanned).
    write_scan "${container}" $'18001\t%22\t18000'
    export MOCK_LIVE_PANES="%99"
    export MOCK_TERM_FAIL="${container}"
    set_gone 18001
    run_reap
    assert_exit_zero
    assert_contains "Note: process 18001 in container ${container} exited before SIGTERM could be delivered; skipping."
    assert_not_contains "Error:"
    local reaped_needle
    reaped_needle=$'reaped\t'
    assert_not_contains "${reaped_needle}"
    assert_contains "No orphaned processes found."
}

# ---------------------------------------------------------------------------
# Dev-vs-root contract (#357): the mock's scan-branch guard requires -u dev
# (see lines ~156-159), mirroring production's scan call. A regression back
# to -u root on either side makes this case fail loudly (scan transport
# failure), since the guard's `exit 6` would never let the fixture's
# dev-owned orphan be reaped.
case_19_dev_scan_finds_dev_owned_orphan() {
    echo "case: a dev-owned orphan is found and reaped via a -u dev scan (root scan does not see it)"
    reset_state
    local container="claude-cenci-devowned"
    add_podman_container "${container}"
    write_scan "${container}" $'19001\t%23\t19000'
    export MOCK_LIVE_PANES="%99"
    set_gone 19001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 19001 "%23")"
    assert_calls_contains "podman exec -u dev ${container}"
    # "environ" is unique to reapScanScript (the TMUX_PANE scan) among the
    # in-container scripts; the pre-SIGKILL probe also runs "-u root ... sh
    # -c" but never mentions environ, so this only rules out a root-user
    # scan, not the (still-root) liveness probe that legitimately follows.
    assert_calls_not_match "-u root ${container} sh -c .*environ"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 19001"
}

# ---------------------------------------------------------------------------
case_20_empty_recorded_start_silently_falls_back_to_kill() {
    echo "case: an empty recorded start time (scan-time field-22 read failure) silently falls back to best-effort SIGKILL -- no unparseable-start-time Note"
    reset_state
    local container="claude-cenci-emptystart"
    add_podman_container "${container}"
    # Recorded start is empty (third column blank) at scan time; the
    # pre-SIGKILL probe reports a genuine numeric start time. recordedNumeric
    # is false, probeNumeric is true, and recordedStart == "" -- the silent
    # (Note-free) fallback branch, not the noted one.
    write_scan "${container}" $'20001\t%24\t'
    export MOCK_LIVE_PANES="%99"
    set_live_start 20001 20000
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 20001 "%24")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 20001"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 20001"
    assert_not_contains "unparseable start-time"
}

# ---------------------------------------------------------------------------
case_21_nostat_probe_silently_falls_back_to_kill() {
    echo "case: a __NOSTAT__ pre-SIGKILL probe result silently falls back to best-effort SIGKILL -- no unparseable-start-time Note"
    reset_state
    local container="claude-cenci-nostat"
    add_podman_container "${container}"
    # Recorded start is a genuine numeric value at scan time; the pre-SIGKILL
    # probe is alive but unverifiable (__NOSTAT__). recordedNumeric is true,
    # probeNumeric is false, and probeStart == "__NOSTAT__" -- the silent
    # (Note-free) fallback branch, not the noted one.
    write_scan "${container}" $'21001\t%25\t1000'
    export MOCK_LIVE_PANES="%99"
    set_nostat 21001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 21001 "%25")"
    assert_calls_contains "podman exec -u root ${container} kill -TERM 21001"
    assert_calls_contains "podman exec -u root ${container} kill -KILL 21001"
    assert_not_contains "unparseable start-time"
}

# ---------------------------------------------------------------------------
# A __UNREADABLE__\t<pid> marker for a non-init pid is a possible sign of a
# scan-user/agent-process UID drift (#361): the count-and-Note logic is
# host-side, so this fixture line is passed through verbatim by the mock scan
# branch (see the exec-case comment above) exactly like every other scan line.
case_22_unreadable_environ_note_fires() {
    echo "case: a non-PID-1 __UNREADABLE__ marker produces an always-on Note, and a paired real orphan is still reaped"
    reset_state
    local container="claude-cenci-unreadable"
    add_podman_container "${container}"
    write_scan "${container}" $'__UNREADABLE__\t22002' $'22001\t%26\t22000'
    export MOCK_LIVE_PANES="%99"
    set_gone 22001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 22001 "%26")"
    assert_contains "Note: 1 process environ(s) in container ${container} were unreadable during the -u dev scan; if this persists it may mean the scan user's UID no longer matches the agent process UID, so orphans could go undetected."
}

# ---------------------------------------------------------------------------
# PID 1 (root-owned sudo init, see the container user model) is always
# unreadable by the -u dev scan on a healthy container, so it is excluded from
# the unreadable count -- a __UNREADABLE__\t1-only marker must not fire the
# Note, keeping healthy/idle containers silent.
case_23_unreadable_pid1_no_note() {
    echo "case: a PID-1-only __UNREADABLE__ marker does not produce the Note, and a paired real orphan is still reaped"
    reset_state
    local container="claude-cenci-unreadablepid1"
    add_podman_container "${container}"
    write_scan "${container}" $'__UNREADABLE__\t1' $'23001\t%27\t23000'
    export MOCK_LIVE_PANES="%99"
    set_gone 23001
    run_reap
    assert_exit_zero
    assert_contains "$(reaped_line "${container}" 23001 "%27")"
    assert_not_contains "unreadable during the -u dev scan"
}

# ---------------------------------------------------------------------------
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
case_12_malformed_pane_skipped
case_13_pid_reuse_during_grace_skips_kill
case_14_liveness_transport_failure_hard_fails
case_15_corrupted_scan_start_falls_back_to_kill
case_16_corrupted_probe_output_falls_back_to_kill
case_17_container_init_never_signaled
case_18_gone_before_term_is_benign
case_19_dev_scan_finds_dev_owned_orphan
case_20_empty_recorded_start_silently_falls_back_to_kill
case_21_nostat_probe_silently_falls_back_to_kill
case_22_unreadable_environ_note_fires
case_23_unreadable_pid1_no_note

print_summary
[[ "${FAILURES}" -eq 0 ]]
