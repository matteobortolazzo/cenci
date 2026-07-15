#!/usr/bin/env bash
#
# Dependency-free gate test for the Codex bootstrap script.
#
# Shared assertions live in ../lib/test-common.sh (sourced below); this file
# only points it at the Codex-specific bootstrap.sh, plugin.json, manifest
# dir, and root-resolution env var.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
BOOTSTRAP="$DIR/bootstrap.sh"
PLUGIN_JSON="$DIR/../.codex-plugin/plugin.json"
MANIFEST_DIR=".codex-plugin"
ROOT_VAR_NAME="PLUGIN_ROOT"

# shellcheck source=../lib/test-common.sh
source "$DIR/../lib/test-common.sh"
