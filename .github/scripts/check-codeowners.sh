#!/usr/bin/env bash
# Statically validates that every path referenced in .github/CODEOWNERS
# still exists in the repository tree, catching CODEOWNERS entries that
# have drifted from a since-renamed or since-deleted file/directory.
#
# Rules enforced:
#   1. Comment (`#`) and blank lines are skipped.
#   2. The first whitespace-delimited token on each remaining line is taken
#      as the path pattern (the rest of the line, e.g. owner handles, is
#      ignored).
#   3. A single leading `/` is stripped before the existence check, since
#      CODEOWNERS paths are repo-root-anchored, not filesystem-root-anchored.
#   4. Each remaining path must exist via `[ -e path ]`.
#
# Known limitation: lines whose path contains glob characters (`*`, `?`,
# `[`) or a bare trailing-slash directory pattern (e.g. `docs/`) are
# skipped rather than validated. Validating those would require
# reimplementing gitignore-style glob matching, which is out of scope for
# this script; only literal file/directory paths are checked today.
#
# Failures are accumulated rather than exiting on the first one, so a
# single run reports every offending line. GitHub Actions' default `run:`
# shell has errexit (-e) ON, which would otherwise abort this script at the
# first failed assertion before the rest of the lines are checked —
# `set +e` below disables that. This also makes the script safe to run
# manually as a plain bash script outside of Actions.
set -uo pipefail
set +e

CODEOWNERS_FILE=".github/CODEOWNERS"

FAILED=0

fail() {
  echo "FAIL: $*" >&2
  FAILED=1
}

if [ ! -f "$CODEOWNERS_FILE" ]; then
  fail "$CODEOWNERS_FILE not found"
  exit 1
fi

if [ ! -r "$CODEOWNERS_FILE" ]; then
  fail "$CODEOWNERS_FILE is not readable"
  exit 1
fi

while IFS= read -r line || [ -n "$line" ]; do
  # Skip blank lines (allowing for leading/trailing whitespace).
  trimmed="${line#"${line%%[![:space:]]*}"}"
  [ -n "$trimmed" ] || continue

  # Skip comment lines.
  case "$trimmed" in
    \#*) continue ;;
  esac

  # First whitespace-delimited token is the path pattern.
  path="${trimmed%%[[:space:]]*}"

  # Skip glob patterns and bare trailing-slash directory patterns — see
  # "Known limitation" above.
  case "$path" in
    *'*'*|*'?'*|*'['*)
      continue
      ;;
    */)
      continue
      ;;
  esac

  # Strip a single leading '/' — CODEOWNERS paths are repo-root-anchored.
  check_path="${path#/}"

  if [ ! -e "$check_path" ]; then
    fail "$path (from $CODEOWNERS_FILE) does not exist"
  fi
done < "$CODEOWNERS_FILE" || fail "could not read $CODEOWNERS_FILE"

if [ "$FAILED" -ne 0 ]; then
  echo "check-codeowners: FAILED" >&2
  exit 1
fi

echo "check-codeowners: all paths exist"
