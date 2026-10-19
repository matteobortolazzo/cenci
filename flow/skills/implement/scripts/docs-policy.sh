#!/usr/bin/env bash
# docs-policy.sh <repo-root> -- resolve the repo's documentation policy (#1301).
#
# Prints `minimal` or `default` on stdout:
#   minimal  iff the config's `docs.policy` is exactly the string "minimal"
#            (case-sensitive).
#   default  everything else.
#
# Config source: <repo-root>/.cenci/config.json. Only when that file does not
# exist at all does the resolver fall back, read-only, to the legacy
# <repo-root>/.claude/config.json. No config at all resolves to `default`.
# A `.cenci/config.json` that exists but cannot be used (malformed JSON,
# several top-level documents, not an object, a directory, a dangling symlink)
# is an error -- it never falls through to the legacy file.
#
# The config directory itself is checked before its file: when
# <repo-root>/.cenci exists (or is a symlink, dangling or not) but is not a
# searchable directory -- a regular file, a dangling symlink, an unsearchable
# directory -- `.cenci/config.json` cannot be tested (ENOTDIR/EACCES make `-e`
# false), so that is an error too, never a silent fall-through to the legacy
# file or to `default`. The same check applies to <repo-root>/.claude when it
# is the fallback location (no `.cenci/config.json`).
#
# Exit codes:
#   0  a policy was printed. When `docs` or `docs.policy` is present but is not
#      a usable value (any value other than "minimal", including "default",
#      null, numbers, and a non-object `docs`), exactly one stderr warning line
#      naming the `docs.policy` key is written -- the warning
#      never echoes the value and never contains `minimal`, so a caller that
#      merges stdout and stderr cannot misread it as the minimal policy.
#      An absent `docs` / `docs.policy` is the normal default and silent.
#   2  the config cannot be trusted, jq is missing, or the argument is bad;
#      exactly one stderr line is written and nothing is printed to stdout.
#
# The script never writes either config file. Equality checks are explicit in
# the jq program (no bare `//` fallback chains), and no `BASH_*` variable names
# are used (docs/shell-scripting-gotchas.md).

# jq is checked first, before any other external command, so a host without jq
# fails with one clear line.
command -v jq >/dev/null 2>&1 || {
  echo "docs-policy.sh: jq is required but was not found on PATH." >&2
  exit 2
}

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "docs-policy.sh: usage: docs-policy.sh <repo-root>" >&2
  exit 2
fi

REPO_ROOT="$1"
if [ ! -d "${REPO_ROOT}" ]; then
  echo "docs-policy.sh: repo root is not a directory: ${REPO_ROOT}" >&2
  exit 2
fi

CENCI_DIR="${REPO_ROOT}/.cenci"
LEGACY_DIR="${REPO_ROOT}/.claude"
CENCI_CONFIG="${CENCI_DIR}/config.json"
LEGACY_CONFIG="${LEGACY_DIR}/config.json"

# config_exists <path> -- true when anything lives at the path, including a
# dangling symlink (which `-e` alone would call missing).
config_exists() {
  [ -e "$1" ] || [ -L "$1" ]
}

# require_config_dir <dir> -- exits 2 when something lives at <dir> but it is
# not a searchable directory, since its config.json could then never be
# tested; an absent <dir> is fine (the caller falls through).
require_config_dir() {
  if config_exists "$1"; then
    if [ ! -d "$1" ] || [ ! -x "$1" ]; then
      echo "docs-policy.sh: config directory $1 exists but is not a directory (or is not searchable)" >&2
      exit 2
    fi
  fi
}

CONFIG=""
require_config_dir "${CENCI_DIR}"
if config_exists "${CENCI_CONFIG}"; then
  CONFIG="${CENCI_CONFIG}"
else
  require_config_dir "${LEGACY_DIR}"
  if config_exists "${LEGACY_CONFIG}"; then
    CONFIG="${LEGACY_CONFIG}"
  fi
fi
if [ -z "${CONFIG}" ]; then
  printf 'default\n'
  exit 0
fi

# Present but not a readable regular file (directory, dangling symlink, ...).
if [ ! -f "${CONFIG}" ] || [ ! -r "${CONFIG}" ]; then
  echo "docs-policy.sh: cannot read config ${CONFIG}" >&2
  exit 2
fi

# One token out of jq: notobject | absent | minimal | invalid. jq reads the file
# itself; a parse failure, or anything but a single token (empty file, several
# top-level documents), is a malformed config.
if ! STATE="$(jq -r '
    if type != "object" then "notobject"
    elif (has("docs") | not) then "absent"
    elif (.docs | type) != "object" then "invalid"
    elif (.docs | has("policy") | not) then "absent"
    elif (.docs.policy | type) == "string" and .docs.policy == "minimal" then "minimal"
    else "invalid"
    end
  ' "${CONFIG}" 2>/dev/null)"; then
  echo "docs-policy.sh: failed to parse ${CONFIG} as JSON" >&2
  exit 2
fi

case "${STATE}" in
  minimal)
    printf 'minimal\n'
    ;;
  absent)
    printf 'default\n'
    ;;
  invalid)
    echo "docs-policy.sh: warning: docs.policy has an unrecognized value; using the default policy" >&2
    printf 'default\n'
    ;;
  notobject)
    echo "docs-policy.sh: ${CONFIG} must contain a JSON object at the top level" >&2
    exit 2
    ;;
  *)
    echo "docs-policy.sh: failed to parse ${CONFIG} as JSON" >&2
    exit 2
    ;;
esac
exit 0
