#!/usr/bin/env bash
# Host-runnable test for the timestamped startup-marker prefix (#572).
#
# sandbox/entrypoint.sh writes three startup-failure markers that
# watch/internal/sandbox/launcher/launch.go's startupFailureDetail reads back
# via a short-lived home-volume container:
#   1. the root-setup EXIT trap's generic marker
#      (/home/dev/.cenci-startup-failed)
#   2. the dev-side EXIT trap's generic marker (same file, fallback path when
#      the container boots directly as dev)
#   3. the agent-CLI-missing marker (/home/dev/.cenci-agent-startup-error)
#
# `cenci diagnose` needs each marker's write time, so every write site must
# prefix its message with a UTC ISO-8601 timestamp
# ($(date -u +%Y-%m-%dT%H:%M:%SZ)).
#
# (a) structurally asserts each of the 3 marker-write lines contains that
#     date token (catches a write site that forgot the prefix even though
#     some other site has it).
# (b) dynamically extracts the dev-side EXIT trap's real command from
#     entrypoint.sh, runs it in a representative `( trap ...; exit 1 )`
#     subshell against a scratch marker path, and asserts the marker content
#     that lands on disk actually starts with a timestamp — proving the
#     token isn't just present as inert script text but is expanded into the
#     written marker at runtime.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENTRYPOINT="${SANDBOX_DIR}/entrypoint.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

echo "startup-marker.test.sh"

# shellcheck disable=SC2016 # literal token we grep for in entrypoint.sh, not expanded here
DATE_TOKEN='$(date -u +%Y-%m-%dT%H:%M:%SZ)'
TIMESTAMP_REGEX='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z '

# -- (a) structural: each of the 3 marker-write lines carries the date token --

echo "case: root-setup EXIT trap marker (.cenci-startup-failed) is timestamped"
ROOT_TRAP_LINE="$(grep -F 'entrypoint exited before completing startup (root setup)' "${ENTRYPOINT}" || true)"
if [[ -n "${ROOT_TRAP_LINE}" ]]; then
    if [[ "${ROOT_TRAP_LINE}" == *"${DATE_TOKEN}"* ]]; then
        pass
    else
        fail "root-setup trap marker line does not contain ${DATE_TOKEN}: ${ROOT_TRAP_LINE}"
    fi
else
    fail "could not locate the root-setup EXIT trap marker line in ${ENTRYPOINT}"
fi

echo "case: dev-side EXIT trap marker (.cenci-startup-failed) is timestamped"
# The dev-side trap's message has no " (root setup)" suffix before the closing
# quote, unlike the root-setup trap's — this substring isolates it uniquely.
DEV_TRAP_LINE="$(grep -F 'entrypoint exited before completing startup" >' "${ENTRYPOINT}" || true)"
if [[ -n "${DEV_TRAP_LINE}" ]]; then
    if [[ "${DEV_TRAP_LINE}" == *"${DATE_TOKEN}"* ]]; then
        pass
    else
        fail "dev-side trap marker line does not contain ${DATE_TOKEN}: ${DEV_TRAP_LINE}"
    fi
else
    fail "could not locate the dev-side EXIT trap marker line in ${ENTRYPOINT}"
fi

echo "case: agent-CLI-missing marker (.cenci-agent-startup-error) is timestamped"
AGENT_CLI_LINE="$(grep -F '> /home/dev/.cenci-agent-startup-error' "${ENTRYPOINT}" || true)"
if [[ -n "${AGENT_CLI_LINE}" ]]; then
    if [[ "${AGENT_CLI_LINE}" == *"${DATE_TOKEN}"* ]]; then
        pass
    else
        fail "agent-CLI-missing marker line does not contain ${DATE_TOKEN}: ${AGENT_CLI_LINE}"
    fi
else
    fail "could not locate the agent-CLI-missing marker write line in ${ENTRYPOINT}"
fi

# -- (b) dynamic: the dev-side trap's real command actually writes a
#    timestamp-prefixed marker at runtime, not just inert script text ------

echo "case: dev-side EXIT trap writes a marker starting with a UTC ISO-8601 timestamp"
if [[ -n "${DEV_TRAP_LINE}" ]]; then
    TRAP_BODY="$(printf '%s' "${DEV_TRAP_LINE}" | sed -E "s/^[[:space:]]*trap '(.*)' EXIT[[:space:]]*\$/\\1/")"
    if [[ "${TRAP_BODY}" == "${DEV_TRAP_LINE}" || -z "${TRAP_BODY}" ]]; then
        fail "could not extract the dev-side trap's command body from: ${DEV_TRAP_LINE}"
    else
        MARKER_FILE="${WORK}/dev-trap-marker"
        # Redirect the real write target at our scratch file instead of the
        # unwritable /home/dev path, keeping the rest of the extracted
        # command (the date-token expansion, the message text) untouched.
        SCRATCH_BODY="${TRAP_BODY//\/home\/dev\/.cenci-startup-failed/${MARKER_FILE}}"
        bash -c "trap '${SCRATCH_BODY}' EXIT; exit 1" >/dev/null 2>&1 || true
        if [[ -f "${MARKER_FILE}" ]]; then
            MARKER_CONTENT="$(cat "${MARKER_FILE}")"
            if [[ "${MARKER_CONTENT}" =~ ${TIMESTAMP_REGEX} ]]; then
                pass
            else
                fail "dev-side trap marker content does not start with a UTC ISO-8601 timestamp: ${MARKER_CONTENT}"
            fi
        else
            fail "dev-side trap did not write a marker file at ${MARKER_FILE} (extracted body: ${SCRATCH_BODY})"
        fi
    fi
else
    fail "dev-side EXIT trap marker line was not found in an earlier case; skipping dynamic check"
fi

print_summary
[[ "${FAILURES}" -eq 0 ]]
