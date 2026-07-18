#!/usr/bin/env bash
#
# Dependency-free gate test for the OpenCode plugin (#488).
#
# Unlike Codex/Claude Code (declarative hooks.json dispatched by their own
# hook runner), OpenCode's plugin is a JS/TS module (@opencode-ai/plugin)
# registering `event`, `tool.execute.before/after`, and `permission.ask`
# hooks programmatically. This asserts the structural contract that plugin.ts
# must uphold:
#   1. it exists, and package.json declares @opencode-ai/plugin
#   2. every hook is wrapped fail-open (try/catch) so a missing cenci
#      binary/socket/daemon never throws into OpenCode
#   3. it reports events via `cenci notify -agent opencode` (the Go binary's
#      existing socket resolution/daemon-start-on-demand/retry), never by
#      writing directly to the daemon's Unix socket
#   4. it never logs prompt/tool-arg/credential content
#   5. bootstrap.sh exists (provisions the plugin-local cenci binary + starts
#      the daemon, mirroring plugin/codex/bootstrap.sh)

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
PLUGIN_TS="$DIR/plugin.ts"
BOOTSTRAP="$DIR/bootstrap.sh"
PACKAGE_JSON="$DIR/package.json"

pass=0
fail=0

ok() {
    echo "ok   - $1"
    pass=$((pass + 1))
}

bad() {
    echo "FAIL - $1"
    fail=$((fail + 1))
}

if [ -f "$PLUGIN_TS" ]; then
    ok "plugin.ts exists"
else
    bad "plugin.ts exists (not found at $PLUGIN_TS)"
fi

if [ -f "$PACKAGE_JSON" ] && grep -q '"@opencode-ai/plugin"' "$PACKAGE_JSON" 2>/dev/null; then
    ok "package.json declares @opencode-ai/plugin"
else
    bad "package.json declares @opencode-ai/plugin"
fi

if [ -f "$BOOTSTRAP" ]; then
    ok "bootstrap.sh exists"
else
    bad "bootstrap.sh exists (not found at $BOOTSTRAP)"
fi

if [ -f "$PLUGIN_TS" ] && grep -Eq 'catch[[:space:]]*\(' "$PLUGIN_TS" 2>/dev/null; then
    ok "plugin.ts wraps hooks in a fail-open try/catch"
else
    bad "plugin.ts wraps hooks in a fail-open try/catch"
fi

if [ -f "$PLUGIN_TS" ] && grep -q 'notify' "$PLUGIN_TS" 2>/dev/null && grep -q -- '-agent opencode' "$PLUGIN_TS" 2>/dev/null; then
    ok "plugin.ts reports events via 'cenci notify -agent opencode'"
else
    bad "plugin.ts reports events via 'cenci notify -agent opencode'"
fi

if [ -f "$PLUGIN_TS" ] && grep -q 'createConnection\|net\.connect\|Bun\.connect' "$PLUGIN_TS" 2>/dev/null; then
    bad "plugin.ts must never write directly to the daemon socket"
else
    ok "plugin.ts does not write directly to the daemon socket"
fi

if [ -f "$PLUGIN_TS" ] && grep -Eq 'console\.(log|error|warn|info|debug)\([^)]*(prompt|args|apiKey|api_key|token|credential)' "$PLUGIN_TS" 2>/dev/null; then
    bad "plugin.ts must never log prompt/tool-arg/credential content"
else
    ok "plugin.ts does not log prompt/tool-arg/credential content"
fi

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
