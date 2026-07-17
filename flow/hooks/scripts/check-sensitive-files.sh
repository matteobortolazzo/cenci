#!/bin/sh
# PreToolUse hook: warn before writing to sensitive files (env files,
# credentials/secrets/keys, SSH/keystore files). Runs unconditionally in
# every repo — unlike guard-main-worktree.sh, this hook does not gate on
# cenci configuration.
#
# Hardening: the file path is extracted via jq's field-scoped
# '.tool_input.file_path' with a fallback to '.tool_input.filePath' against
# properly parsed JSON (not a raw-text grep/sed), so a lookalike
# "file_path"-shaped substring in an unrelated field (e.g. old_string) can no
# longer be mistaken for the real path. The fallback is driven by an explicit
# emptiness check (not jq's bare '//', which only falls back on null/false,
# not on an empty string) so a present-but-empty file_path correctly falls
# through to filePath instead of short-circuiting to the empty-path allow
# below. Missing jq, a JSON parse failure, a non-absolute file_path, or the
# absence of both realpath and readlink -f each fail closed (block) rather
# than silently allowing the write.
#
# The extracted path is canonicalized (symlinks resolved, "."/".." segments
# lexically collapsed) using the same parent-anchored resolver as
# guard-main-worktree.sh. The blocklist case statement is then run against
# BOTH the raw file_path and the canonicalized path — a match on either
# blocks (union, not canonical-only). This is deliberate: canonical-only
# matching would let a symlink literally named e.g. ".env" that resolves to
# a benign target slip through, regressing today's raw-name behavior; raw-
# only matching would miss a benign-looking name that resolves to a
# sensitive canonical target. The union preserves today's raw-name blocking
# while adding canonical-path protection, with no regressions either way.

command -v jq >/dev/null 2>&1 || {
  echo "BLOCKED: jq is required by check-sensitive-files.sh but was not found on PATH." >&2
  exit 2
}

INPUT=$(cat)
JQ_ERR=$(mktemp 2>/dev/null) || JQ_ERR=/dev/null
if ! FILE_PATH=$(printf '%s' "$INPUT" | jq -r '
    if (.tool_input.file_path // "") != "" then
      .tool_input.file_path
    else
      (.tool_input.filePath // empty)
    end
  ' 2>"$JQ_ERR"); then
  echo "BLOCKED: check-sensitive-files.sh could not parse the tool call's JSON input: $(cat "$JQ_ERR" 2>/dev/null)" >&2
  rm -f "$JQ_ERR"
  exit 2
fi
rm -f "$JQ_ERR"

[ -z "$FILE_PATH" ] && exit 0

case "$FILE_PATH" in
  /*) ;;
  *)
    echo "BLOCKED: check-sensitive-files.sh received a non-absolute file_path: $FILE_PATH" >&2
    exit 2
    ;;
esac

# Detect a path resolver able to canonicalize an *existing* path. Prefer
# realpath; fall back to GNU readlink -f (probed via a known-existing path so
# BSD/other readlink implementations that lack -f are rejected here, not at
# use time). Neither available → fail closed.
if command -v realpath >/dev/null 2>&1; then
  resolve_path() { realpath "$1"; }
elif readlink -f / >/dev/null 2>&1; then
  resolve_path() { readlink -f "$1"; }
else
  echo "BLOCKED: check-sensitive-files.sh requires realpath or 'readlink -f' to canonicalize paths, neither was found on PATH." >&2
  exit 2
fi

# Lexically collapse "." and ".." segments in an absolute path, without
# touching the filesystem. Used both as the sole normalization for
# not-yet-existing paths and as a final cleanup pass after a parent-anchored
# resolve (whose re-appended tail may itself contain traversal segments).
lexical_collapse() {
  path="$1"
  # Split on '/', rebuild collapsing '.' and '..' against the accumulated stack.
  # Disable globbing for the unquoted split so path segments containing glob
  # metacharacters (*, ?, [) are never pathname-expanded.
  old_ifs=$IFS
  IFS='/'
  set -f
  # shellcheck disable=SC2086 # intentional IFS='/' word-splitting to
  # enumerate path segments; globbing is disabled via `set -f` above.
  set -- $path
  set +f
  IFS=$old_ifs
  result=""
  for seg in "$@"; do
    case "$seg" in
      "" | ".") ;;
      "..")
        result="${result%/*}"
        ;;
      *)
        result="$result/$seg"
        ;;
    esac
  done
  [ -z "$result" ] && result="/"
  printf '%s\n' "$result"
}

# Parent-anchored canonicalization: only ever feed *existing* paths to the
# resolver, sidestepping realpath/readlink -f divergence on non-existent
# trailing components (no -m / -f-on-missing-target usage). When FILE_PATH
# itself does not exist, walk up toward / to find the nearest *existing*
# ancestor — not just the immediate parent — so a symlink several levels
# above a not-yet-existing tail still gets resolved. "/" always exists, so
# the walk is guaranteed to terminate.
if [ -e "$FILE_PATH" ]; then
  if ! CANONICAL_PATH=$(resolve_path "$FILE_PATH"); then
    echo "BLOCKED: check-sensitive-files.sh could not resolve $FILE_PATH" >&2
    exit 2
  fi
else
  ANCESTOR="$FILE_PATH"
  TAIL=""
  while [ -n "$ANCESTOR" ] && [ ! -d "$ANCESTOR" ]; do
    SEG="${ANCESTOR##*/}"
    if [ -z "$TAIL" ]; then
      TAIL="$SEG"
    else
      TAIL="$SEG/$TAIL"
    fi
    ANCESTOR="${ANCESTOR%/*}"
  done
  [ -z "$ANCESTOR" ] && ANCESTOR="/"
  if ! RESOLVED_ANCESTOR=$(resolve_path "$ANCESTOR"); then
    echo "BLOCKED: check-sensitive-files.sh could not resolve $ANCESTOR" >&2
    exit 2
  fi
  CANONICAL_PATH=$(lexical_collapse "$RESOLVED_ANCESTOR/$TAIL")
fi

# Check against sensitive file patterns. Run against BOTH the raw path and
# the canonicalized path — block (exit 2) if either matches (see header
# comment for the union-matching rationale).
check_sensitive() {
  case "$1" in
    *.env|*.env.*|*/.env|*/.env.*)
      echo "BLOCKED: Refusing to write to environment file: $1" >&2
      echo "Environment files may contain secrets. Edit manually if needed." >&2
      exit 2
      ;;
    *credentials*|*secrets*|*secret.*|*.pem|*.key|*.pfx|*.p12)
      echo "BLOCKED: Refusing to write to sensitive file: $1" >&2
      echo "This file may contain credentials or keys. Edit manually if needed." >&2
      exit 2
      ;;
    *id_rsa*|*id_ed25519*|*id_ecdsa*|*.keystore|*.jks)
      echo "BLOCKED: Refusing to write to key file: $1" >&2
      echo "This file contains cryptographic keys. Edit manually if needed." >&2
      exit 2
      ;;
  esac
}

check_sensitive "$FILE_PATH"
check_sensitive "$CANONICAL_PATH"

exit 0
