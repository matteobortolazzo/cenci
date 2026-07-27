#!/bin/sh
# run-gate.sh — resolve the configured gateCommand from .cenci/config.json
# (top-level for a single-project repo, or the matching project's
# gateCommand/path in a monorepo when a slug is given) and execute it,
# reporting the outcome as GATE_STATUS=<green|red|unset> on stdout.
#
# Trust boundary: the gateCommand string(s) come only from trusted,
# committed .cenci/config.json — config.json is trusted content, not
# external/untrusted input, so a resolved gateCommand is executed via
# `sh -c` without further sanitization. The ONLY externally-influenced
# input to this script is the optional slug argument ($1). That slug is
# therefore NEVER string-interpolated into a jq program or into the shell
# command; it is always passed to jq via `--arg` (parameterized, not
# interpolated) so it can only ever be compared as a plain string value.
#
# gateCommand/path resolution always uses an explicit emptiness check
# (`if (.field // "") != "" then .field else empty end`) rather than a bare
# jq `//`, because `//` only falls back on null/false and would otherwise
# treat a present-but-empty string as a valid value.

command -v jq >/dev/null 2>&1 || {
  echo "run-gate.sh: jq is required but was not found on PATH." >&2
  exit 2
}
ROOT="$(pwd -P)" || { echo "run-gate.sh: failed to resolve current working directory." >&2; exit 2; }
CONFIG="${ROOT}/.cenci/config.json"

if [ ! -f "${CONFIG}" ]; then
  echo "GATE_STATUS=unset"
  exit 0
fi

SLUG="${1:-}"

# mktemp-failure fallback (#550): if a temp file can be created, jq's stderr
# is captured there (fd 9 points at it) so the normal-path message keeps its
# exact inline-detail format. If mktemp fails (e.g. TMPDIR exhaustion), fd 9
# is instead dup'd straight to the script's own stderr — /dev/null is never
# assigned to JQ_ERR, so it can never be handed to `rm` (which as root would
# unlink the /dev/null device node), and jq's diagnostic is never dropped.
if JQ_ERR="$(mktemp 2>/dev/null)"; then
  exec 9>"${JQ_ERR}"
else
  JQ_ERR=""
  exec 9>&2
  echo "run-gate.sh: warning: mktemp failed; jq errors are written directly to stderr below" >&2
fi

# jq_err_detail — the jq diagnostic text to interpolate into a failure
# message: the captured temp-file content when one exists, else a pointer to
# the raw jq stderr already printed above (fd 9 passthrough case).
jq_err_detail() {
  if [ -n "${JQ_ERR}" ]; then
    cat "${JQ_ERR}" 2>/dev/null
  else
    echo "(see the jq error printed on stderr above)"
  fi
}

# jq_err_cleanup — close fd 9 and remove the temp file, only when one exists.
# MUST be called before this script execs the user's gateCommand below, or
# the child process would inherit the open fd 9 write handle.
# Always returns 0 on success: an `if` whose final command is unconditional,
# not a `&&`-chain whose truth value would become the function's return code
# (which would make the no-temp-file case return 1 even though cleanup
# succeeded — harmless today since no caller uses `set -e`, but latent).
jq_err_cleanup() {
  exec 9>&-
  if [ -n "${JQ_ERR}" ]; then
    rm -f "${JQ_ERR}"
  fi
}

if ! jq -e . "${CONFIG}" >/dev/null 2>&9; then
  echo "run-gate.sh: failed to parse ${CONFIG}: $(jq_err_detail)" >&2
  jq_err_cleanup
  exit 1
fi

GATE_COMMAND=""
PROJECT_PATH=""

if [ -n "${SLUG}" ]; then
  if ! MATCH_COUNT=$(jq -r --arg slug "${SLUG}" \
      '[.projects[]? | select(.slug == $slug)] | length' \
      "${CONFIG}" 2>&9); then
    echo "run-gate.sh: failed to evaluate slug match for '${SLUG}': $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi

  if [ "${MATCH_COUNT}" = "0" ]; then
    echo "run-gate.sh: no project with slug '${SLUG}' found in ${CONFIG}." >&2
    jq_err_cleanup
    exit 1
  fi

  if [ "${MATCH_COUNT}" != "1" ]; then
    echo "run-gate.sh: ambiguous slug '${SLUG}': ${MATCH_COUNT} projects match in ${CONFIG}." >&2
    jq_err_cleanup
    exit 1
  fi

  if ! GATE_COMMAND=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.gateCommand // "") != "" then .gateCommand else empty end
    ' "${CONFIG}" 2>&9); then
    echo "run-gate.sh: failed to read gateCommand for slug '${SLUG}': $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi

  if ! PROJECT_PATH=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.path // "") != "" then .path else empty end
    ' "${CONFIG}" 2>&9); then
    echo "run-gate.sh: failed to read path for slug '${SLUG}': $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi
else
  if ! GATE_COMMAND=$(jq -r '
      if (.gateCommand // "") != "" then .gateCommand else empty end
    ' "${CONFIG}" 2>&9); then
    echo "run-gate.sh: failed to read gateCommand: $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi
fi

jq_err_cleanup

if [ -z "${GATE_COMMAND}" ]; then
  echo "GATE_STATUS=unset"
  exit 0
fi

case "/${PROJECT_PATH}/" in
  */../*|*/./*)
    echo "run-gate.sh: refusing project path containing a dot segment: ${PROJECT_PATH}" >&2
    exit 1
    ;;
esac
case "${PROJECT_PATH}" in
  /*)
    echo "run-gate.sh: refusing absolute project path: ${PROJECT_PATH}" >&2
    exit 1
    ;;
esac

if [ -n "${SLUG}" ]; then
  ABS_DIR="${ROOT}/${PROJECT_PATH}"
else
  ABS_DIR="${ROOT}"
fi

# Resolve the nearest existing directory ancestor with `pwd -P`, then append
# the validated missing tail. This is portable across GNU and BSD/macOS hosts
# and still exposes a symlink ancestor that escapes the repository.
canonicalize_dir_allow_missing() {
  candidate="$1"
  tail=""
  while [ ! -d "$candidate" ]; do
    if [ -L "$candidate" ]; then
      return 1
    fi
    segment="${candidate##*/}"
    [ -n "$segment" ] || return 1
    if [ -z "$tail" ]; then
      tail="$segment"
    else
      tail="$segment/$tail"
    fi
    candidate="${candidate%/*}"
    [ -n "$candidate" ] || candidate="/"
  done
  resolved_ancestor=$(cd "$candidate" 2>/dev/null && pwd -P) || return 1
  if [ -n "$tail" ]; then
    printf '%s/%s\n' "$resolved_ancestor" "$tail"
  else
    printf '%s\n' "$resolved_ancestor"
  fi
}

if ! RESOLVED_DIR=$(canonicalize_dir_allow_missing "${ABS_DIR}"); then
  echo "run-gate.sh: failed to resolve project directory: ${ABS_DIR}" >&2
  exit 1
fi
case "${RESOLVED_DIR}" in
  "${ROOT}"|"${ROOT}"/*) ;;
  *)
    echo "run-gate.sh: refusing project path outside repository root: ${PROJECT_PATH}" >&2
    exit 1
    ;;
esac
ABS_DIR="${RESOLVED_DIR}"

if [ ! -d "${ABS_DIR}" ]; then
  echo "run-gate.sh: project directory not found: ${ABS_DIR}" >&2
  exit 1
fi

( cd "${ABS_DIR}" && sh -c "${GATE_COMMAND}" )
rc=$?

if [ "${rc}" -eq 0 ]; then
  echo "GATE_STATUS=green"
  exit 0
else
  echo "GATE_STATUS=red"
  exit "${rc}"
fi
