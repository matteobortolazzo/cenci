#!/bin/sh
set -eu
ROOT=${1:-.}
SOURCE=${PLUGIN_ROOT:?PLUGIN_ROOT is required}/templates/codex/agents
DEST="$ROOT/.codex/agents"
mkdir -p "$DEST"
for source in "$SOURCE"/*.toml; do
  target="$DEST/$(basename "$source")"
  if [ -e "$target" ]; then continue; fi
  cp "$source" "$target"
done
