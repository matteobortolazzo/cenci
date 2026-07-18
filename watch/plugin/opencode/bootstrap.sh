#!/bin/sh
# cenci bootstrap (OpenCode) — provisions the plugin-local binary, puts it on
# $PATH, and starts the daemon.
#
# OpenCode has no declarative hooks.json like Claude Code/Codex, so nothing
# invokes this script for us: plugin.ts spawns it itself, detached, the first
# time the plugin loads. It MUST never block OpenCode and MUST never exit
# non-zero: every failure path logs one line and exits 0. When the release
# artifact matching the plugin version is missing from bin/, it is downloaded
# (with sha256 verification) from the GitHub release. The binary is then
# symlinked onto $PATH so bare `cenci` invocations (tmux run-shell,
# shell-spawned bar widgets) resolve it. The daemon is finally started if it
# isn't already (the daemon's own already-running guard makes a redundant
# start a harmless no-op — the common case when the Claude Code/Codex plugin
# already bootstrapped it).
#
# Shared install/download/daemon-start logic lives in ../lib/bootstrap-common.sh
# (sourced below); this file only resolves the OpenCode-specific ROOT,
# version-manifest path, and PATH hint.

set -u

# No plugin-root env var is set for OpenCode (unlike CLAUDE_PLUGIN_ROOT /
# PLUGIN_ROOT for Claude Code/Codex); resolve one level above opencode/, same
# as the Codex/Claude Code layout, so the plugin-local bin/ is shared.
ROOT="$(dirname "$0")/.."
PLUGIN_MANIFEST_REL="opencode/package.json"
PATH_AUDIENCE="the OpenCode plugin can find it"

# shellcheck source=../lib/bootstrap-common.sh
. "$(dirname "$0")/../lib/bootstrap-common.sh"
