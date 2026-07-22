#!/usr/bin/env bash
set -euo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$FLOW/opencode/install-skills.sh"

# Detect a path resolver able to canonicalize an existing path, mirroring the
# realpath/readlink -f fallback convention used by
# flow/hooks/scripts/check-sensitive-files.sh.
if command -v realpath >/dev/null 2>&1; then
  resolve_path() { realpath "$1"; }
elif readlink -f / >/dev/null 2>&1; then
  resolve_path() { readlink -f "$1"; }
else
  echo "install-skills.test.sh requires realpath or 'readlink -f'" >&2
  exit 2
fi

# Portable convention skills the helper is expected to link into OpenCode's
# native global skills directory. Pipeline/interactive skills (implement,
# configure, refine, review, refactor, sync, maintain, design, address-review)
# must never appear here.
PORTABLE_SKILLS="attachments babysit frontend-classification pr-comment-filter shell-rules stack-angular stack-dotnet stack-go subagent-safety testing verify-ui worktrees"
PIPELINE_SKILLS="implement configure refine review refactor sync maintain design address-review"

HOME_A="$(mktemp -d)"
HOME_B="$(mktemp -d)"
trap 'rm -rf "$HOME_A" "$HOME_B"' EXIT

# --- Scenario A: clean home ------------------------------------------------
export HOME="$HOME_A"
export XDG_CONFIG_HOME="$HOME_A/.config"
DEST="$XDG_CONFIG_HOME/opencode/skills"

PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install

# Clean-home install creates exactly the portable-skill links, not more/fewer.
actual="$(find "$DEST" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort)"
expected="$(printf '%s\n' $PORTABLE_SKILLS | sort)"
test "$actual" = "$expected"

# No pipeline/interactive skill gets linked.
for skill in $PIPELINE_SKILLS; do
  test ! -e "$DEST/$skill"
done

# Each entry is a symlink pointing at the correct source inside the plugin's
# skills directory (not a copy).
for skill in $PORTABLE_SKILLS; do
  test -L "$DEST/$skill"
  resolved="$(resolve_path "$DEST/$skill")"
  test "$resolved" = "$FLOW/skills/$skill"
done

# --- Idempotent re-run ------------------------------------------------------
PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install
actual_again="$(find "$DEST" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort)"
test "$actual_again" = "$expected"
for skill in $PORTABLE_SKILLS; do
  # -e (not -L alone) additionally proves the symlink target still resolves,
  # i.e. re-running install left no broken links.
  test -e "$DEST/$skill"
done

# A pre-existing, unrelated directory already present in the destination is
# left untouched by a second install.
mkdir -p "$DEST/homegrown"
printf 'mine\n' > "$DEST/homegrown/marker.txt"
PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install
test -d "$DEST/homegrown"
test ! -L "$DEST/homegrown"
grep -q '^mine$' "$DEST/homegrown/marker.txt"

# --- remove cleans up only the links it created -----------------------------
PLUGIN_ROOT="$FLOW" sh "$SCRIPT" remove
for skill in $PORTABLE_SKILLS; do
  test ! -e "$DEST/$skill"
done
# Unrelated pre-existing content survives remove too.
test -d "$DEST/homegrown"
grep -q '^mine$' "$DEST/homegrown/marker.txt"

# --- Scenario B: a real (non-symlink) directory already occupies one of our
# own skill names before install ever runs. Both install and remove must
# leave it alone rather than clobbering or deleting it.
export HOME="$HOME_B"
export XDG_CONFIG_HOME="$HOME_B/.config"
DEST_B="$XDG_CONFIG_HOME/opencode/skills"
mkdir -p "$DEST_B/testing"
printf 'pre-existing\n' > "$DEST_B/testing/marker.txt"

PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install
test -d "$DEST_B/testing"
test ! -L "$DEST_B/testing"
grep -q '^pre-existing$' "$DEST_B/testing/marker.txt"
# Other portable skills are still linked normally alongside the collision.
test -L "$DEST_B/worktrees"

PLUGIN_ROOT="$FLOW" sh "$SCRIPT" remove
test -d "$DEST_B/testing"
grep -q '^pre-existing$' "$DEST_B/testing/marker.txt"
test ! -e "$DEST_B/worktrees"

# --- Scenario C: a symlink already occupies one of our own skill names before
# install ever runs, but it points somewhere other than
# $PLUGIN_ROOT/skills/<name>. Both install and remove must leave it alone
# rather than clobbering or deleting it.
HOME_C="$(mktemp -d)"
trap 'rm -rf "$HOME_A" "$HOME_B" "$HOME_C"' EXIT
export HOME="$HOME_C"
export XDG_CONFIG_HOME="$HOME_C/.config"
DEST_C="$XDG_CONFIG_HOME/opencode/skills"
UNRELATED="$(mktemp -d)"
mkdir -p "$DEST_C"
ln -s "$UNRELATED" "$DEST_C/testing"

PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install
test -L "$DEST_C/testing"
resolved_c="$(resolve_path "$DEST_C/testing")"
test "$resolved_c" = "$(resolve_path "$UNRELATED")"
# Other portable skills are still linked normally alongside the collision.
test -L "$DEST_C/worktrees"

PLUGIN_ROOT="$FLOW" sh "$SCRIPT" remove
test -L "$DEST_C/testing"
resolved_c_after_remove="$(resolve_path "$DEST_C/testing")"
test "$resolved_c_after_remove" = "$(resolve_path "$UNRELATED")"
test ! -e "$DEST_C/worktrees"

rm -rf "$UNRELATED"

echo "install-skills.test.sh: passed"
