#!/usr/bin/env bash
set -euo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || exit 1
SCRIPT="$FLOW/opencode/install-skills.sh"
TEST_HOME="$(mktemp -d)" || exit 1
trap 'rm -rf "$TEST_HOME"' EXIT

# Installation refuses under both shells without creating a directory.
for interpreter in sh bash; do
  output="$TEST_HOME/$interpreter-output"
  if HOME="$TEST_HOME" XDG_CONFIG_HOME="$TEST_HOME/$interpreter-config" PLUGIN_ROOT="$FLOW" "$interpreter" "$SCRIPT" install >"$output" 2>&1; then
    echo "disabled installation unexpectedly succeeded under $interpreter" >&2
    exit 1
  fi
  grep -q 'OpenCode is temporarily disabled' "$output"
  test ! -e "$TEST_HOME/$interpreter-config"
done

# Simulate a legacy install: removal still cleans only our own links.
# Pin XDG_CONFIG_HOME as well as HOME: install-skills.sh prefers
# XDG_CONFIG_HOME, and CI runners export it, so an unpinned value would point
# removal at the runner's real config dir instead of this fixture.
export HOME="$TEST_HOME"
export XDG_CONFIG_HOME="$TEST_HOME/.config"
DEST="$XDG_CONFIG_HOME/opencode/skills"
mkdir -p "$DEST/stack-go"
ln -s "$FLOW/skills/testing" "$DEST/testing"
ln -s "$TEST_HOME/foreign-target" "$DEST/babysit"
printf 'user-owned\n' > "$DEST/stack-go/marker"
printf 'user skill\n' > "$DEST/my-skill"
HOME="$TEST_HOME" PLUGIN_ROOT="$FLOW" sh "$SCRIPT" remove
test ! -L "$DEST/testing"
test -L "$DEST/babysit"
test "$(readlink "$DEST/babysit")" = "$TEST_HOME/foreign-target"
grep -q 'user-owned' "$DEST/stack-go/marker"
grep -q 'user skill' "$DEST/my-skill"

# Retrying removal is safe, and disabled install preserves legacy/user files.
HOME="$TEST_HOME" PLUGIN_ROOT="$FLOW" bash "$SCRIPT" remove
if HOME="$TEST_HOME" PLUGIN_ROOT="$FLOW" sh "$SCRIPT" install >"$TEST_HOME/retry-output" 2>&1; then
  exit 1
fi
test -L "$DEST/babysit"
grep -q 'user-owned' "$DEST/stack-go/marker"
grep -q 'user skill' "$DEST/my-skill"

echo "install-skills.test.sh: passed"
