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

# babysit is retired for OpenCode (#1217): removal deletes a leftover
# cenci-owned babysit link and counts it in `removed:`, under both shells and
# each case in its own XDG_CONFIG_HOME so no fixture leaks into another.
# run_remove <interpreter> <xdg> <plugin-root> — runs `remove`, captures
# stdout in $RM_OUT and stderr in $RM_ERR, and sets $RM_RC.
run_remove() {
  RM_RC=0
  RM_OUT="$(HOME="$TEST_HOME" XDG_CONFIG_HOME="$2" PLUGIN_ROOT="$3" "$1" "$SCRIPT" remove 2>"$TEST_HOME/rm-stderr")" || RM_RC=$?
  RM_ERR="$(cat "$TEST_HOME/rm-stderr")"
}

assert_eq() { # <label> <expected> <actual>
  if [ "$2" != "$3" ]; then
    echo "FAIL [$1]: expected [$2], got [$3]" >&2
    exit 1
  fi
}

# The retirement is a separate OpenCode-only list: babysit stays in
# PORTABLE_SKILLS (Codex keeps it portable) and is declared retired exactly once.
assert_eq "retired declaration count" 1 "$(grep -c '^OPENCODE_RETIRED_SKILLS="babysit"$' "$SCRIPT")"
assert_eq "retired declaration is the only one" 1 "$(grep -c '^OPENCODE_RETIRED_SKILLS=' "$SCRIPT")"
portable_line="$(grep '^PORTABLE_SKILLS=' "$SCRIPT")"
case " $portable_line " in *' babysit '*) ;; *)
  echo "FAIL: babysit must stay in PORTABLE_SKILLS: $portable_line" >&2
  exit 1
  ;;
esac

for interpreter in sh bash; do
  # Owned babysit link: removed and counted in removed:.
  xdg="$TEST_HOME/$interpreter-owned"
  mkdir -p "$xdg/opencode/skills"
  ln -s "$FLOW/skills/babysit" "$xdg/opencode/skills/babysit"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter owned: exit" 0 "$RM_RC"
  assert_eq "$interpreter owned: summary" "removed: 1, skipped: 0" "$RM_OUT"
  test ! -e "$xdg/opencode/skills/babysit"
  test ! -L "$xdg/opencode/skills/babysit"

  # Second remove after the link is gone: nothing removed, still exit 0.
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter second remove: exit" 0 "$RM_RC"
  assert_eq "$interpreter second remove: summary" "removed: 0, skipped: 0" "$RM_OUT"

  # Foreign babysit link (another plugin root): survives, counts as skipped.
  xdg="$TEST_HOME/$interpreter-foreign"
  mkdir -p "$xdg/opencode/skills"
  ln -s "$TEST_HOME/other-root/skills/babysit" "$xdg/opencode/skills/babysit"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter foreign: exit" 0 "$RM_RC"
  assert_eq "$interpreter foreign: summary" "removed: 0, skipped: 1" "$RM_OUT"
  test -L "$xdg/opencode/skills/babysit"
  assert_eq "$interpreter foreign: target" "$TEST_HOME/other-root/skills/babysit" "$(readlink "$xdg/opencode/skills/babysit")"
  case "$RM_ERR" in *'does not point at'*'leaving untouched'*) ;; *)
    echo "FAIL [$interpreter foreign: stderr]: $RM_ERR" >&2
    exit 1
    ;;
  esac

  # Real directory at babysit: untouched, and not counted as skipped.
  xdg="$TEST_HOME/$interpreter-dir"
  mkdir -p "$xdg/opencode/skills/babysit"
  printf 'user-owned\n' > "$xdg/opencode/skills/babysit/marker"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter dir: exit" 0 "$RM_RC"
  assert_eq "$interpreter dir: summary" "removed: 0, skipped: 0" "$RM_OUT"
  grep -q 'user-owned' "$xdg/opencode/skills/babysit/marker"

  # Plain file at babysit: untouched.
  xdg="$TEST_HOME/$interpreter-file"
  mkdir -p "$xdg/opencode/skills"
  printf 'user skill\n' > "$xdg/opencode/skills/babysit"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter file: exit" 0 "$RM_RC"
  assert_eq "$interpreter file: summary" "removed: 0, skipped: 0" "$RM_OUT"
  grep -q 'user skill' "$xdg/opencode/skills/babysit"

  # Dangling owned link (target never existed under this fake root): removed.
  xdg="$TEST_HOME/$interpreter-dangling"
  fake_root="$TEST_HOME/$interpreter-fake-root"
  mkdir -p "$xdg/opencode/skills" "$fake_root"
  ln -s "$fake_root/skills/babysit" "$xdg/opencode/skills/babysit"
  test ! -e "$xdg/opencode/skills/babysit"
  run_remove "$interpreter" "$xdg" "$fake_root"
  assert_eq "$interpreter dangling: exit" 0 "$RM_RC"
  assert_eq "$interpreter dangling: summary" "removed: 1, skipped: 0" "$RM_OUT"
  test ! -L "$xdg/opencode/skills/babysit"

  # Missing skills directory: nothing to do, exit 0.
  xdg="$TEST_HOME/$interpreter-missing"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter missing dir: exit" 0 "$RM_RC"
  assert_eq "$interpreter missing dir: summary" "removed: 0, skipped: 0" "$RM_OUT"
  test ! -e "$xdg"

  # Delivery set and retired link together: both owned links go, each counted once.
  xdg="$TEST_HOME/$interpreter-mixed"
  mkdir -p "$xdg/opencode/skills"
  ln -s "$FLOW/skills/babysit" "$xdg/opencode/skills/babysit"
  ln -s "$FLOW/skills/testing" "$xdg/opencode/skills/testing"
  run_remove "$interpreter" "$xdg" "$FLOW"
  assert_eq "$interpreter mixed: summary" "removed: 2, skipped: 0" "$RM_OUT"
  test ! -L "$xdg/opencode/skills/babysit"
  test ! -L "$xdg/opencode/skills/testing"

  # Refused install with an owned babysit link present: exit 1, disabled
  # message, and the link is still there (the guard runs before any cleanup).
  xdg="$TEST_HOME/$interpreter-refused"
  mkdir -p "$xdg/opencode/skills"
  ln -s "$FLOW/skills/babysit" "$xdg/opencode/skills/babysit"
  refused_rc=0
  HOME="$TEST_HOME" XDG_CONFIG_HOME="$xdg" PLUGIN_ROOT="$FLOW" "$interpreter" "$SCRIPT" install >"$TEST_HOME/refused-out" 2>&1 || refused_rc=$?
  assert_eq "$interpreter refused install: exit" 1 "$refused_rc"
  grep -q 'OpenCode is temporarily disabled' "$TEST_HOME/refused-out"
  test -L "$xdg/opencode/skills/babysit"
  assert_eq "$interpreter refused install: link" "$FLOW/skills/babysit" "$(readlink "$xdg/opencode/skills/babysit")"
done

echo "install-skills.test.sh: passed"
