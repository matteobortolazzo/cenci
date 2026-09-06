#!/bin/sh
# cenci bootstrap — provisions the plugin-local binary, puts it on $PATH,
# and starts the daemon.
#
# Runs detached from the SessionStart hook, so it MUST never block the agent and
# MUST never exit non-zero: every failure path logs one line and exits 0. When
# the release artifact matching the plugin version is missing from bin/, it is
# downloaded (with sha256 verification) from the GitHub release. The binary is
# then symlinked onto $PATH so bare `cenci` invocations (tmux run-shell,
# shell-spawned bar widgets) resolve it even though the plugin cache is on no
# login PATH. The daemon is finally started if it isn't already (the daemon's
# own already-running guard makes a redundant start a harmless no-op).
#
# Shared install/download/daemon-start logic lives in ../lib/bootstrap-common.sh
# (sourced below); this file only resolves the Claude Code-specific ROOT,
# plugin.json path, and PATH hint.

set -u

# Resolve the plugin root. CLAUDE_PLUGIN_ROOT is set by Claude Code; fall back to
# this script's parent directory for manual/dev invocation.
ROOT="${CLAUDE_PLUGIN_ROOT:-$(dirname "$0")/..}"
PLUGIN_MANIFEST_REL=".claude-plugin/plugin.json"
PATH_AUDIENCE="bare cenci invocations resolve"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/bootstrap-common.sh
. "$(dirname "$0")/../lib/bootstrap-common.sh"
