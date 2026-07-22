#!/bin/sh
# resolve-babysit-interval.sh — resolve the configured babysit polling interval
# from .cenci/config.json (top-level for a single-project repo, or the matching
# project's babysitInterval in a monorepo when a slug is given) and echo it on
# stdout for a caller to pass to `cenci babysit ... --interval <val>`.
#
# Sibling of run-gate.sh, and modelled directly on it — same config-path
# resolution, same `--arg`-parameterized (never interpolated) slug handling,
# same present-but-empty guard. The ONE deliberate behavioral difference: the
# babysit interval is OPTIONAL and `cenci babysit` supplies its own `15m`
# default, so a missing config file or an unset/empty babysitInterval is NOT an
# error — this script echoes nothing and exits 0 in those cases. The caller
# contract is: non-empty stdout → pass `--interval <val>`; empty stdout → omit
# `--interval` and let the CLI use its built-in default.
#
# Genuine errors (missing jq, malformed config, no-match/ambiguous slug) still
# fail closed with a non-zero exit and a stderr diagnostic, exactly like
# run-gate.sh — the caller in Phase 9 treats any such failure as a non-fatal
# babysit-launch problem to report, falling back to the CLI default interval.
#
# Trust boundary: as with run-gate.sh, the only externally-influenced input is
# the optional slug argument ($1); it is NEVER string-interpolated into a jq
# program, always passed via `--arg`. Unlike run-gate.sh this script executes
# nothing (it only reads a field), so there is no gateCommand/path/directory
# handling here.
#
# babysitInterval resolution uses an explicit emptiness check
# (`if (.field // "") != "" then .field else empty end`) rather than a bare jq
# `//`, because `//` only falls back on null/false and would otherwise treat a
# present-but-empty string as a valid value.

command -v jq >/dev/null 2>&1 || {
  echo "resolve-babysit-interval.sh: jq is required but was not found on PATH." >&2
  exit 2
}

ROOT="$(pwd -P)" || { echo "resolve-babysit-interval.sh: failed to resolve current working directory." >&2; exit 2; }
CONFIG="${ROOT}/.cenci/config.json"

# Missing config → interval simply unset; emit nothing, exit 0 (not an error).
if [ ! -f "${CONFIG}" ]; then
  exit 0
fi

SLUG="${1:-}"

JQ_ERR="$(mktemp 2>/dev/null)" || {
  echo "resolve-babysit-interval.sh: warning: mktemp failed, jq error detail will be unavailable" >&2
  JQ_ERR=/dev/null
}

if ! jq -e . "${CONFIG}" >/dev/null 2>"${JQ_ERR}"; then
  echo "resolve-babysit-interval.sh: failed to parse ${CONFIG}: $(cat "${JQ_ERR}" 2>/dev/null)" >&2
  [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
  exit 1
fi

INTERVAL=""

if [ -n "${SLUG}" ]; then
  if ! MATCH_COUNT=$(jq -r --arg slug "${SLUG}" \
      '[.projects[]? | select(.slug == $slug)] | length' \
      "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "resolve-babysit-interval.sh: failed to evaluate slug match for '${SLUG}': $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if [ "${MATCH_COUNT}" = "0" ]; then
    echo "resolve-babysit-interval.sh: no project with slug '${SLUG}' found in ${CONFIG}." >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if [ "${MATCH_COUNT}" != "1" ]; then
    echo "resolve-babysit-interval.sh: ambiguous slug '${SLUG}': ${MATCH_COUNT} projects match in ${CONFIG}." >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi

  if ! INTERVAL=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.babysitInterval // "") != "" then .babysitInterval else empty end
    ' "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "resolve-babysit-interval.sh: failed to read babysitInterval for slug '${SLUG}': $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi
else
  if ! INTERVAL=$(jq -r '
      if (.babysitInterval // "") != "" then .babysitInterval else empty end
    ' "${CONFIG}" 2>"${JQ_ERR}"); then
    echo "resolve-babysit-interval.sh: failed to read babysitInterval: $(cat "${JQ_ERR}" 2>/dev/null)" >&2
    [ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"
    exit 1
  fi
fi

[ "${JQ_ERR}" != /dev/null ] && rm -f "${JQ_ERR}"

# Unset/empty babysitInterval → emit nothing, exit 0; caller uses the CLI default.
[ -n "${INTERVAL}" ] && printf '%s\n' "${INTERVAL}"
exit 0
