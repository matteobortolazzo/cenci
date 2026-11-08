#!/usr/bin/env bash
# Exercise host setup against real JSON/rule files in an isolated home.
# Functions/variables are consumed by the sourced installer functions below.
# shellcheck disable=SC2034,SC2329
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)" || exit 1
[[ -d "$WORK" && -n "$WORK" ]] || exit 1
trap 'rm -rf "$WORK"' EXIT
# Load the installer's actual functions without starting marketplace setup.
sed '/^# ------------------------------------------------------------------- main ----/,$d' "$ROOT/install.sh" >"$WORK/functions.sh"
# shellcheck source=/dev/null
source "$WORK/functions.sh"
export HOME="$WORK/home"
export CODEX_HOME="$WORK/custom-codex"
export CLAUDE_CONFIG_DIR="$HOME/custom-claude"
mkdir -p "$CLAUDE_CONFIG_DIR" "$CODEX_HOME/rules" "$HOME/.local/bin"
HAS_CLAUDE=1 HAS_CODEX=1 MODE=install ASSUME_YES=1 INSTALL_FAILED=0
HOST_CCLINE=ask HOST_PERMISSIONS=ask
printf '%s\n' '{"permissions":{"deny":["Bash(gh repo delete *)"],"allow":["Read"]},"theme":"dark"}' >"$CLAUDE_CONFIG_DIR/settings.json"
cp "$CLAUDE_CONFIG_DIR/settings.json" "$WORK/before.json"
step_host_setup
cmp "$WORK/before.json" "$CLAUDE_CONFIG_DIR/settings.json"
[[ ! -e "$CODEX_HOME/rules/cenci-host.rules" ]]

HOST_PERMISSIONS=yes
step_host_setup
jq -e '.theme == "dark" and .permissions.deny == ["Bash(gh repo delete *)"] and (.permissions.allow | index("Read")) and (.permissions.allow | index("Bash(gh:*)"))' "$CLAUDE_CONFIG_DIR/settings.json" >/dev/null
grep -Fq 'pattern=["gh"], decision="allow"' "$CODEX_HOME/rules/cenci-host.rules"
cp "$CLAUDE_CONFIG_DIR/settings.json" "$WORK/once.json"
cp "$CODEX_HOME/rules/cenci-host.rules" "$WORK/once.rules"
step_host_setup
cmp "$WORK/once.json" "$CLAUDE_CONFIG_DIR/settings.json"
cmp "$WORK/once.rules" "$CODEX_HOME/rules/cenci-host.rules"

# An unrelated rule file and malformed settings are never overwritten.
printf 'custom rules\n' >"$CODEX_HOME/rules/cenci-host.rules"
printf '{invalid\n' >"$CLAUDE_CONFIG_DIR/settings.json"
step_host_setup
[[ "$INSTALL_FAILED" -eq 1 ]]
grep -qx '{invalid' "$CLAUDE_CONFIG_DIR/settings.json"
grep -qx 'custom rules' "$CODEX_HOME/rules/cenci-host.rules"

# Existing ccline can be selected without any download; preserve custom status lines.
INSTALL_FAILED=0 HOST_PERMISSIONS=no HOST_CCLINE=yes
printf '#!/bin/sh\nprintf ccline\n' >"$HOME/.local/bin/ccline"
chmod +x "$HOME/.local/bin/ccline"
export PATH="$HOME/.local/bin:$PATH"
printf '{"theme":"dark"}\n' >"$CLAUDE_CONFIG_DIR/settings.json"
step_host_setup
jq -e '.theme == "dark" and .statusLine.type == "command" and (.statusLine.command | contains("ccline"))' "$CLAUDE_CONFIG_DIR/settings.json" >/dev/null
printf '{"statusLine":{"type":"command","command":"custom"}}\n' >"$CLAUDE_CONFIG_DIR/settings.json"
step_host_setup
jq -e '.statusLine.command == "custom"' "$CLAUDE_CONFIG_DIR/settings.json" >/dev/null

# A symlink target outside the managed home is left untouched.
printf '{"theme":"outside"}\n' >"$WORK/outside.json"
rm "$CLAUDE_CONFIG_DIR/settings.json"
ln -s "$WORK/outside.json" "$CLAUDE_CONFIG_DIR/settings.json"
HOST_PERMISSIONS=yes HOST_CCLINE=no INSTALL_FAILED=0
step_host_setup
[[ "$INSTALL_FAILED" -eq 1 ]]
jq -e '.theme == "outside" and (has("permissions") | not)' "$WORK/outside.json" >/dev/null

bash "$ROOT/install.sh" --host-ccline --host-permissions --help >"$WORK/help"
grep -q -- '--host-ccline' "$WORK/help"
grep -q -- '--host-permissions' "$WORK/help"

# Download the pinned archive through the real extraction/install path.
mkdir -p "$WORK/archive"
printf '#!/bin/sh\nprintf installed-ccline\n' >"$WORK/archive/ccline"
tar -czf "$WORK/ccline.tar.gz" -C "$WORK/archive" ccline
curl() { cp "$WORK/ccline.tar.gz" "$3"; }
sha256sum() { printf '%s  archive\n' '9b7d4b2bdd8f24e6db09c837f4e464ec7aa5155d963cbec191b683b760bc2706'; }
OS=linux ARCH=amd64
rm "$HOME/.local/bin/ccline"
install_host_ccline
[[ "$("$HOME/.local/bin/ccline")" == installed-ccline ]]
sha256sum() { printf '%s  archive\n' 'wrong-digest'; }
if install_host_ccline; then echo 'FAIL: corrupt download accepted'; exit 1; fi
[[ "$("$HOME/.local/bin/ccline")" == installed-ccline ]]

# Invalid settings and conflicting flags fail without starting installation.
for option in host-ccline host-permissions; do
    if bash "$ROOT/install.sh" "--$option" "--no-$option" --help >"$WORK/conflict" 2>&1; then
        echo "FAIL: conflicting $option accepted"; exit 1
    fi
done
echo 'passed: optional host settings, idempotency, preservation and unsafe-file refusal'
