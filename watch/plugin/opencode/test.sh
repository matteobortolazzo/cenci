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

# --- Bootstrap end-to-end (mirrors lib/test-common.sh; self-contained) ------
# OpenCode's bootstrap.sh has no root env override, so build the real relative
# layout <tmp>/opencode|lib|bin and run the copied script from that position.
if command -v jq >/dev/null 2>&1 && command -v bash >/dev/null 2>&1; then
    VERSION="$(jq -r '.version' "$PACKAGE_JSON" 2>/dev/null)"
    if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
        # Re-run to capture jq's own diagnostic (discarded above) so a
        # malformed/unreadable package.json is distinguishable in the SKIP
        # message from a legitimately-absent .version field.
        jq_err="$(jq -r '.version' "$PACKAGE_JSON" 2>&1 >/dev/null)"
        echo "SKIP - could not read version from $PACKAGE_JSON (bootstrap e2e)${jq_err:+: $jq_err}"
    elif ! TMP="$(mktemp -d)"; then
        bad "bootstrap e2e: mktemp failed for scratch root"
    else
        trap 'rm -rf "$TMP"' EXIT

        # wait_for_file polls for $1 to become non-empty, up to ~$2 deciseconds.
        wait_for_file() {
            local file="$1" timeout_ds="${2:-20}" waited=0
            while [ ! -s "$file" ] && [ "$waited" -lt "$timeout_ds" ]; do
                sleep 0.1
                waited=$((waited + 1))
            done
        }

        # run_opencode_bootstrap <layout> <calls> <logdir> <cenci_sandbox>
        #
        # Builds the real relative <layout>/opencode|lib|bin tree (OpenCode's
        # bootstrap.sh has no root env override, so dirname "$0" must resolve
        # for real) with a stub bin/cenci that records every invocation to
        # <calls> and exits 0. The stub version marker is pre-stamped so
        # install_binary short-circuits without touching the network. TMPDIR
        # and HOME are redirected so the run neither writes the real
        # bootstrap log nor touches the developer's ~/.local/bin. Returns the
        # exit code of the bootstrap.sh invocation itself (or 1 if the $HOME
        # fixture couldn't be built) so callers can tell a broken fixture
        # apart from a genuine negative assertion result.
        run_opencode_bootstrap() {
            local layout="$1" calls="$2" logdir="$3" cenci_sandbox="$4"

            mkdir -p "$layout/opencode" "$layout/lib" "$layout/bin"
            cp "$BOOTSTRAP" "$layout/opencode/bootstrap.sh"
            cp "$DIR/../lib/bootstrap-common.sh" "$layout/lib/bootstrap-common.sh"
            cp "$PACKAGE_JSON" "$layout/opencode/package.json"

            cat >"$layout/bin/cenci" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$calls"
exit 0
STUB
            chmod +x "$layout/bin/cenci"
            printf '%s\n' "$VERSION" >"$layout/bin/.cenci-version"

            local home
            home="$(mktemp -d "$TMP/home.XXXX")" || {
                echo "FAIL - bootstrap e2e: mktemp failed for \$HOME fixture" >&2
                return 1
            }

            if [ -n "$cenci_sandbox" ]; then
                env TMPDIR="$logdir" HOME="$home" CENCI_SANDBOX="$cenci_sandbox" \
                    bash "$layout/opencode/bootstrap.sh"
            else
                (
                    unset CENCI_SANDBOX
                    export TMPDIR="$logdir" HOME="$home"
                    bash "$layout/opencode/bootstrap.sh"
                )
            fi
        }

        # --- Case 1: CENCI_SANDBOX=1 -> no daemon spawn, log line present ---
        root_sand="$TMP/root-sand"
        calls_sand="$TMP/calls-sand"
        log_sand="$TMP/log-sand"
        mkdir -p "$log_sand"
        : >"$calls_sand"

        run_opencode_bootstrap "$root_sand" "$calls_sand" "$log_sand" "1"
        bootstrap_rc_sand=$?

        # Bounded settle: start_daemon backgrounds via nohup, so give a
        # spurious spawn a moment to land before asserting its absence.
        sleep 0.3

        # Assert the fixture/invocation itself succeeded before trusting the
        # negative "no daemon call" assertion below -- otherwise a broken
        # fixture (e.g. a missing cp source) would leave calls_sand empty for
        # the wrong reason and this test would report a false "ok" without
        # ever having exercised the sandbox-skip code path.
        if [ "$bootstrap_rc_sand" -eq 0 ]; then
            ok "bootstrap.sh exits 0 when CENCI_SANDBOX=1"
        else
            bad "bootstrap.sh exits 0 when CENCI_SANDBOX=1 (exited $bootstrap_rc_sand)"
        fi

        if [ -s "$calls_sand" ]; then
            bad "daemon NOT invoked when CENCI_SANDBOX=1"
        else
            ok "daemon NOT invoked when CENCI_SANDBOX=1"
        fi

        if grep -q "not starting a local daemon" "$log_sand/cenci-bootstrap.log" 2>/dev/null; then
            ok "bootstrap logs the CENCI_SANDBOX skip reason"
        else
            bad "bootstrap logs the CENCI_SANDBOX skip reason"
        fi

        # --- Case 2: CENCI_SANDBOX unset -> daemon IS invoked (unchanged) ---
        root_normal="$TMP/root-normal"
        calls_normal="$TMP/calls-normal"
        log_normal="$TMP/log-normal"
        mkdir -p "$log_normal"
        : >"$calls_normal"

        run_opencode_bootstrap "$root_normal" "$calls_normal" "$log_normal" ""

        wait_for_file "$calls_normal" 20

        if [ -s "$calls_normal" ] && grep -q "daemon" "$calls_normal"; then
            ok "daemon invoked when CENCI_SANDBOX unset"
        else
            bad "daemon invoked when CENCI_SANDBOX unset"
        fi
    fi
else
    echo "SKIP - jq/bash unavailable; skipping bootstrap end-to-end test"
fi

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
