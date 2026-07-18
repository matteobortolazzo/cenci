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

JQ_ERR="$(mktemp 2>/dev/null)" || {
  echo "run-gate.sh: warning: mktemp failed, jq error detail will be unavailable" >&2
  JQ_ERR=/dev/null
}

if ! jq -e . "${CONFIG}" >/dev/null 2>"${JQ_ERR}"; then
  echo "run-gate.sh: failed to parse ${CONFIG}: $(cat "${JQ_ERR}" 2>/dev/null)" >&2
  [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
  exit 1
fi

GATE_COMMAND=""
PROJECT_PATH=""

if [ -n "${SLUG}" ]; then
  if ! MATCH_COUNT=$(jq -r --arg slug "${SLUG}" \
      '[.projects[]? | select(.slug == $slug)] | length' \
      "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "run-gate.sh: failed to evaluate slug match for '${SLUG}': $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if [ "${MATCH_COUNT}" = "0" ]; then
    echo "run-gate.sh: no project with slug '${SLUG}' found in ${CONFIG}." >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if [ "${MATCH_COUNT}" != "1" ]; then
    echo "run-gate.sh: ambiguous slug '${SLUG}': ${MATCH_COUNT} projects match in ${CONFIG}." >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if ! GATE_COMMAND=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.gateCommand // "") != "" then .gateCommand else empty end
    ' "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "run-gate.sh: failed to read gateCommand for slug '${SLUG}': $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if ! PROJECT_PATH=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.path // "") != "" then .path else empty end
    ' "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "run-gate.sh: failed to read path for slug '${SLUG}': $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi
else
  if ! GATE_COMMAND=$(jq -r '
      if (.gateCommand // "") != "" then .gateCommand else empty end
    ' "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "run-gate.sh: failed to read gateCommand: $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi
fi

[ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"

if [ -z "${GATE_COMMAND}" ]; then
  echo "GATE_STATUS=unset"
  exit 0
fi

case "${PROJECT_PATH}" in
  *..*)
    echo "run-gate.sh: refusing project path containing '..': ${PROJECT_PATH}" >&2
    exit 1
    ;;
esac

if [ -n "${SLUG}" ]; then
  ABS_DIR="${ROOT}/${PROJECT_PATH}"
else
  ABS_DIR="${ROOT}"
fi

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
