#!/bin/sh
set -eu

PLUGIN_ROOT=${PLUGIN_ROOT:?PLUGIN_ROOT is required}
ACTION=${1:?usage: install-skills.sh install|remove}

# Retain removal and the implementation for a future owner-authorized restart.
if [ "$ACTION" = install ]; then
  echo "OpenCode is temporarily disabled; skill installation is paused (https://github.com/matteobortolazzo/cenci/milestone/18)" >&2
  exit 1
fi

# Portable convention skills that are safe to expose to any client (no
# Claude-only tool/UX dependency). Pipeline/interactive skills (implement,
# configure, refine, close, maintain, address-review, review, sync,
# ci-repair, ticket-ownership, babysit-attention) are intentionally excluded
# — they assume Claude Code's interactive approval flow. codex-runtime is
# excluded as a Codex-only adapter. This list is the single source of truth;
# merge-repair is Claude-only: babysit explicitly dispatches it to Claude Opus
# for every supervising client; it has no Codex companion or OpenCode symlink.
# The OpenCode delivery set is this list minus OPENCODE_RETIRED_SKILLS below; the
# README's generated skill inventory (`<!-- cenci-maintain:skills:start -->`)
# OpenCode column must match the delivery set exactly — drift is caught by
# flow/skills/maintain/scripts/check.sh's capability-table and adapter-drift
# checks (run via flow/tests/maintain.test.sh), which replaced the retired
# flow/opencode/portability.test.sh.
#
# project-core is portable despite being a non-user-invocable reference skill:
# Codex's portable `babysit` is directed to "Read `project-core`", and the
# adapter-drift check requires every portable skill to reference only portable
# skills. (It was first added for #1042, when OpenCode still received `babysit`
# and shipped it pointing at a skill that was not installed.) Its text is already
# client-neutral ("the active client's invocation syntax", "the client harness");
# it was grouped with the interactive skills by association, not because it
# assumes an approval flow. See docs/skill-authoring.md's "Client surfaces".
PORTABLE_SKILLS="attachments babysit frontend-classification pr-comment-filter project-core shell-rules stack-angular stack-dotnet stack-go subagent-safety testing verify-ui worktrees"

# Portable skills that OpenCode does NOT receive (#1217). `babysit` stays in
# PORTABLE_SKILLS because Codex runs it portable (`codexWF("babysit").portable()`
# in watch/internal/run/template.go), but OpenCode cannot arm a babysit
# supervisor, so it is no longer linked and `remove` also deletes a leftover
# cenci-owned `babysit` link from a legacy install.
OPENCODE_RETIRED_SKILLS="babysit"

# Guard against an empty (not just unset) HOME, which would otherwise
# silently collapse TARGET_DIR to a root-relative path below.
: "${HOME:?HOME is required}"

TARGET_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/skills"

removed=0
skipped=0

# is_retired <skill> — true when the skill is in OPENCODE_RETIRED_SKILLS.
is_retired() {
  case " $OPENCODE_RETIRED_SKILLS " in
    *" $1 "*) return 0 ;;
  esac
  return 1
}

# remove_owned_link <skill> — delete $TARGET_DIR/<skill> only when it is a
# symlink pointing at this plugin's copy of the skill; a foreign symlink is left
# untouched and counted as skipped, and a real file/directory is never touched.
# Updates the global removed/skipped counters.
remove_owned_link() {
  source="$PLUGIN_ROOT/skills/$1"
  target="$TARGET_DIR/$1"
  if [ -L "$target" ]; then
    current=$(readlink "$target")
    if [ "$current" = "$source" ]; then
      rm -f "$target"
      removed=$((removed + 1))
    else
      echo "install-skills.sh: $target does not point at $source, leaving untouched" >&2
      skipped=$((skipped + 1))
    fi
  fi
}

# cleanup_retired — drop leftover cenci-owned links for every retired skill.
cleanup_retired() {
  for retired in $OPENCODE_RETIRED_SKILLS; do
    remove_owned_link "$retired"
  done
}

case "$ACTION" in
  install)
    if ! mkdir -p "$TARGET_DIR"; then
      echo "install-skills.sh: could not create $TARGET_DIR" >&2
      exit 1
    fi
    cleanup_retired
    linked=0
    for skill in $PORTABLE_SKILLS; do
      if is_retired "$skill"; then
        continue
      fi
      source="$PLUGIN_ROOT/skills/$skill"
      target="$TARGET_DIR/$skill"
      # Anything already at this path - our own matching symlink, a foreign
      # symlink, or a real file/dir - is left untouched rather than clobbered.
      if [ -e "$target" ] || [ -L "$target" ]; then
        echo "install-skills.sh: $target already exists, leaving untouched" >&2
        skipped=$((skipped + 1))
        continue
      fi
      ln -s "$source" "$target"
      linked=$((linked + 1))
    done
    echo "linked: $linked, skipped: $skipped, removed: $removed"
    ;;
  remove)
    if [ -d "$TARGET_DIR" ]; then
      for skill in $PORTABLE_SKILLS; do
        if is_retired "$skill"; then
          continue
        fi
        remove_owned_link "$skill"
      done
      cleanup_retired
    fi
    echo "removed: $removed, skipped: $skipped"
    ;;
  *)
    echo "usage: install-skills.sh install|remove" >&2
    exit 1
    ;;
esac
