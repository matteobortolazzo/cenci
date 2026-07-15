#!/usr/bin/env bash
set -euo pipefail

# Host-runnable regressions for cenci-sand's flag parser:
#   - Unknown long `--flags` are rejected with a clear error instead of being
#     silently forwarded to the agent CLI (a typo used to reach the container
#     unnoticed).
#   - A `--` sentinel escapes the parser: everything after it is forwarded to
#     the agent CLI verbatim, so flags cenci-sand doesn't know about (e.g. a
#     future `claude`/`codex` flag) still have a way through.
#   - Legitimate passthrough that predates this fix keeps working: short
#     flags (`-p`) and bare positional args are still forwarded directly.
#   - `--volumes` only means something combined with `--prune`; used alone it
#     is now a hard error instead of a silent no-op.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
REPO_SLUG="$(slugify "$(basename "${REPO_ROOT}")")"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/runtime-calls"
mkdir -p "${BIN_DIR}" "${TEST_ROOT}/home/.claude" "${TEST_ROOT}/home/.codex"
touch "${TEST_ROOT}/home/.claude/.credentials.json"
touch "${TEST_ROOT}/home/.codex/auth.json"

cat > "${BIN_DIR}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "${CALLS_FILE}"
printf '\n' >> "${CALLS_FILE}"

case "${1:-} ${2:-}" in
    "image inspect") exit 0 ;;
    "ps --format") ;;
    "inspect --format") printf '%s\n' detached ;;
    "rm "*) exit 0 ;;
    "run -d") printf '%s\n' sandbox-container-id ;;
    "exec "*) exit 0 ;;
    *) exit 0 ;;
esac
EOF
chmod +x "${BIN_DIR}/docker"
ln -s docker "${BIN_DIR}/podman"

cat > "${BIN_DIR}/claude" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/claude"

cat > "${BIN_DIR}/cenci" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/cenci"

export CALLS_FILE
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

echo "case: an unknown long flag is rejected instead of silently forwarded"
printf '' > "${CALLS_FILE}"
set +e
UNKNOWN_STDERR="$("${SANDBOX_DIR}/cenci-sand" --totally-bogus-flag -p test 2>&1 1>/dev/null)"
UNKNOWN_EXIT=$?
set -e
if [[ "${UNKNOWN_EXIT}" -eq 0 ]]; then
    echo "FAIL: an unrecognized long flag should have been rejected" >&2
    exit 1
fi
if ! grep -qi 'error' <<< "${UNKNOWN_STDERR}"; then
    echo "FAIL: expected an error message for the unknown flag, got: ${UNKNOWN_STDERR}" >&2
    exit 1
fi
if [[ -s "${CALLS_FILE}" ]]; then
    echo "FAIL: an unknown flag should be rejected before any container is touched" >&2
    exit 1
fi

echo "case: '--' passes everything after it through to the agent CLI verbatim"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" -- --resume --some-future-flag

if ! grep -Eq -- "^exec -it -u dev .*claude-cenci-${REPO_SLUG} claude --dangerously-skip-permissions --model sonnet --resume --some-future-flag " "${CALLS_FILE}"; then
    echo "FAIL: flags after '--' were not forwarded verbatim to the agent CLI" >&2
    exit 1
fi

echo "case: legitimate passthrough (short flags, bare positional args) still works"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" -p "fix the tests"

if ! grep -Eq -- "^exec -it -u dev .*claude-cenci-${REPO_SLUG} claude --dangerously-skip-permissions --model sonnet -p fix\\\\ the\\\\ tests " "${CALLS_FILE}"; then
    echo "FAIL: short-flag/positional passthrough regressed" >&2
    exit 1
fi

echo "case: --volumes without --prune is a hard error, not a silent no-op"
printf '' > "${CALLS_FILE}"
set +e
VOLUMES_STDERR="$("${SANDBOX_DIR}/cenci-sand" --volumes 2>&1 1>/dev/null)"
VOLUMES_EXIT=$?
set -e
if [[ "${VOLUMES_EXIT}" -eq 0 ]]; then
    echo "FAIL: '--volumes' without '--prune' should have been rejected" >&2
    exit 1
fi
if ! grep -qi -- '--prune' <<< "${VOLUMES_STDERR}"; then
    echo "FAIL: expected the error to mention --prune, got: ${VOLUMES_STDERR}" >&2
    exit 1
fi
if [[ -s "${CALLS_FILE}" ]]; then
    echo "FAIL: '--volumes' without '--prune' should be rejected before any container is touched" >&2
    exit 1
fi

echo "case: --volumes combined with --prune still works (regression guard)"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" --prune --volumes <<< "n"

if ! grep -Eq '^volume ls --format' "${CALLS_FILE}"; then
    echo "FAIL: '--prune --volumes' did not query sandbox volumes" >&2
    exit 1
fi

echo "passed: flag parsing (unknown-flag rejection, '--' sentinel, --volumes/--prune coupling)"
