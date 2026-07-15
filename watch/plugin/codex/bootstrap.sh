#!/bin/sh
# cenci bootstrap (Codex) — provisions the plugin-local binary, puts it on
# $PATH, and starts the daemon.
#
# Runs detached from the SessionStart hook, so it MUST never block the agent and
# MUST never exit non-zero: every failure path logs one line and exits 0. When
# the release artifact matching the plugin version is missing from bin/, it is
# downloaded (with sha256 verification) from the GitHub release. Because Codex
# hooks invoke a bare `cenci`, the binary is then symlinked onto $PATH. The
# daemon is finally started if it isn't already (the daemon's own already-running
# guard makes a redundant start a harmless no-op — the common case when the
# Claude plugin already bootstrapped it).
#
# Shared install/download/daemon-start logic lives in ../lib/bootstrap-common.sh
# (sourced below); this file only resolves the Codex-specific ROOT, plugin.json
# path, and PATH hint.

set -u

# Codex sets PLUGIN_ROOT for native plugins. Keep the legacy variable as a
# compatibility fallback, then resolve one level above codex/ for manual runs.
ROOT="${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(dirname "$0")/..}}"
PLUGIN_MANIFEST_REL=".codex-plugin/plugin.json"
PATH_AUDIENCE="Codex hooks can find it"

# shellcheck source=../lib/bootstrap-common.sh
. "$(dirname "$0")/../lib/bootstrap-common.sh"
