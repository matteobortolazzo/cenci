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
  echo "resolve-babysit-interval.sh: warning: mktemp failed; jq errors are written directly to stderr below" >&2
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
  echo "resolve-babysit-interval.sh: failed to parse ${CONFIG}: $(jq_err_detail)" >&2
  jq_err_cleanup
  exit 1
fi

INTERVAL=""

if [ -n "${SLUG}" ]; then
  if ! MATCH_COUNT=$(jq -r --arg slug "${SLUG}" \
      '[.projects[]? | select(.slug == $slug)] | length' \
      "${CONFIG}" 2>&9); then
    echo "resolve-babysit-interval.sh: failed to evaluate slug match for '${SLUG}': $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi

  if [ "${MATCH_COUNT}" = "0" ]; then
    echo "resolve-babysit-interval.sh: no project with slug '${SLUG}' found in ${CONFIG}." >&2
    jq_err_cleanup
    exit 1
  fi

  if [ "${MATCH_COUNT}" != "1" ]; then
    echo "resolve-babysit-interval.sh: ambiguous slug '${SLUG}': ${MATCH_COUNT} projects match in ${CONFIG}." >&2
    jq_err_cleanup
    exit 1
  fi

  if ! INTERVAL=$(jq -r --arg slug "${SLUG}" '
      .projects[]? | select(.slug == $slug) |
      if (.babysitInterval // "") != "" then .babysitInterval else empty end
    ' "${CONFIG}" 2>&9); then
    echo "resolve-babysit-interval.sh: failed to read babysitInterval for slug '${SLUG}': $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi
else
  if ! INTERVAL=$(jq -r '
      if (.babysitInterval // "") != "" then .babysitInterval else empty end
    ' "${CONFIG}" 2>&9); then
    echo "resolve-babysit-interval.sh: failed to read babysitInterval: $(jq_err_detail)" >&2
    jq_err_cleanup
    exit 1
  fi
fi

jq_err_cleanup

# Unset/empty babysitInterval → emit nothing, exit 0; caller uses the CLI default.
[ -n "${INTERVAL}" ] && printf '%s\n' "${INTERVAL}"
exit 0
