#!/bin/sh
set -eu

PLUGIN_ROOT=${PLUGIN_ROOT:?PLUGIN_ROOT is required}
ACTION=${1:?usage: install-skills.sh install|remove}

# Portable convention skills that are safe to expose to any client (no
# Claude-only tool/UX dependency). Pipeline/interactive skills (implement,
# configure, refine, design, maintain, address-review, review, sync,
# ci-repair, ticket-ownership, babysit-attention) are intentionally excluded
# — they assume Claude Code's interactive approval flow. codex-runtime is
# excluded as a Codex-only adapter. This list is the single source of truth;
# the README's generated skill inventory
# (`<!-- cenci-maintain:skills:start -->`) OpenCode column must match it
# exactly — drift is caught by flow/skills/maintain/scripts/check.sh's
# capability-table and adapter-drift checks (run via
# flow/tests/maintain.test.sh), which replaced the retired
# flow/opencode/portability.test.sh.
#
# project-core is portable despite being a non-user-invocable reference skill:
# `babysit` is directed to "Read `project-core`" and OpenCode delivery symlinks
# each skill directory with no dependency resolution, so excluding it shipped
# `babysit` to OpenCode pointing at a skill that was not installed (#1042). Its
# text is already client-neutral ("the active client's invocation syntax", "the
# client harness"); it was grouped with the interactive skills by association,
# not because it assumes an approval flow. See docs/skill-authoring.md's
# "Client surfaces".
PORTABLE_SKILLS="attachments babysit frontend-classification pr-comment-filter project-core shell-rules stack-angular stack-dotnet stack-go subagent-safety testing verify-ui worktrees"

# Guard against an empty (not just unset) HOME, which would otherwise
# silently collapse TARGET_DIR to a root-relative path below.
: "${HOME:?HOME is required}"

TARGET_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/skills"

case "$ACTION" in
  install)
    if ! mkdir -p "$TARGET_DIR"; then
      echo "install-skills.sh: could not create $TARGET_DIR" >&2
      exit 1
    fi
    linked=0
    skipped=0
    for skill in $PORTABLE_SKILLS; do
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
    echo "linked: $linked, skipped: $skipped"
    ;;
  remove)
    removed=0
    skipped=0
    if [ -d "$TARGET_DIR" ]; then
      for skill in $PORTABLE_SKILLS; do
        source="$PLUGIN_ROOT/skills/$skill"
        target="$TARGET_DIR/$skill"
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
      done
    fi
    echo "removed: $removed, skipped: $skipped"
    ;;
  *)
    echo "usage: install-skills.sh install|remove" >&2
    exit 1
    ;;
esac
