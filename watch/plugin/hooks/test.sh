#!/usr/bin/env bash
#
# Dependency-free gate test for the Claude Code bootstrap script.
#
# Shared assertions live in ../lib/test-common.sh (sourced below); this file
# only points it at the Claude Code-specific bootstrap.sh, plugin.json,
# manifest dir, and root-resolution env var.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
BOOTSTRAP="$DIR/bootstrap.sh"
PLUGIN_JSON="$DIR/../.claude-plugin/plugin.json"
MANIFEST_DIR=".claude-plugin"
ROOT_VAR_NAME="CLAUDE_PLUGIN_ROOT"

# shellcheck source=../lib/test-common.sh
source "$DIR/../lib/test-common.sh"
