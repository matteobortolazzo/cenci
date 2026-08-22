#!/bin/sh
# Template source lives at templates/codex/agent-roles, not .../agents: an
# unscoped directory literally named "agents" inside this Codex plugin's own
# root is suspected of being auto-discovered by Codex's plugin loader as a
# second source of the same role names, producing "duplicate agent role name
# ... declared in the same config layer" warnings (#1040). Renaming removes
# the collision even though the exact loader mechanism isn't confirmed.
set -eu
ROOT=${1:-.}
SOURCE=${PLUGIN_ROOT:?PLUGIN_ROOT is required}/templates/codex/agent-roles
DEST="$ROOT/.codex/agents"
mkdir -p "$DEST"
for source in "$SOURCE"/*.toml; do
  target="$DEST/$(basename "$source")"
  if [ -e "$target" ]; then continue; fi
  cp "$source" "$target"
done
