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

# Structural assertion (#1152): plugin.ts must resolve the cenci binary from
# multiple known locations, not hard-pin the single plugin-local path a
# missing/failed release download leaves unusable.
if [ -f "$PLUGIN_TS" ] && grep -q 'candidateBinPaths' "$PLUGIN_TS" 2>/dev/null \
    && grep -q '/usr/local/bin/cenci' "$PLUGIN_TS" 2>/dev/null \
    && grep -q '/opt/homebrew/bin/cenci' "$PLUGIN_TS" 2>/dev/null; then
    ok "plugin.ts resolves multiple candidate binary locations, not a single hard-pinned path"
else
    bad "plugin.ts resolves multiple candidate binary locations, not a single hard-pinned path"
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
            cp "$DIR/../lib/resolve-bin.sh" "$layout/lib/resolve-bin.sh"
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

        # --- Binary fallback resolution (#1152), mirroring lib/test-common.sh
        # cases 1 and 3 -------------------------------------------------------
        #
        # run_opencode_bootstrap_nobin builds the same real-relative
        # <layout>/opencode|lib|bin tree as run_opencode_bootstrap above, but
        # WITHOUT pre-creating bin/cenci and WITHOUT stamping .cenci-version,
        # so install_binary() genuinely reaches its download path. A stub
        # curl/wget (both exit 1) ahead of a minimal PATH forces that download
        # to fail offline and reproducibly -- the real "download failed"
        # branch, with no network and no new production seam. The fixed-prefix
        # candidates are pointed at nonexistent test paths via resolve-bin.sh's
        # test-only overrides, and HOME is a fresh empty dir, so a real
        # /usr/local/bin/cenci or ambient plugin cache in the dev/CI container
        # can never make the "no candidate" case vacuously pass
        # (watch/docs/test-isolation.md). CENCI_SANDBOX=1 is pinned explicitly
        # per the same doc's ambient-daemon-socket guidance. Runs bootstrap.sh
        # under <shell> (bash or dash) so this new POSIX code is covered under
        # both.
        run_opencode_bootstrap_nobin() {
            local shell="$1" layout="$2" logdir="$3" candidate_bin="$4"

            mkdir -p "$layout/opencode" "$layout/lib"
            cp "$BOOTSTRAP" "$layout/opencode/bootstrap.sh"
            cp "$DIR/../lib/bootstrap-common.sh" "$layout/lib/bootstrap-common.sh"
            cp "$DIR/../lib/resolve-bin.sh" "$layout/lib/resolve-bin.sh"
            cp "$PACKAGE_JSON" "$layout/opencode/package.json"

            local stubdir
            stubdir="$(mktemp -d "$TMP/stub.XXXX")" || {
                echo "FAIL - bootstrap e2e: mktemp failed for stub PATH fixture" >&2
                return 1
            }
            cat >"$stubdir/curl" <<STUB
#!/usr/bin/env bash
printf 'curl\n' >> "$logdir/download-attempts"
exit 1
STUB
            cat >"$stubdir/wget" <<STUB
#!/usr/bin/env bash
printf 'wget\n' >> "$logdir/download-attempts"
exit 1
STUB
            chmod +x "$stubdir/curl" "$stubdir/wget"

            local home
            home="$(mktemp -d "$TMP/home.XXXX")" || {
                echo "FAIL - bootstrap e2e: mktemp failed for \$HOME fixture" >&2
                return 1
            }

            (
                unset CENCI_SANDBOX
                export TMPDIR="$logdir" HOME="$home" PATH="$stubdir:/usr/bin:/bin"
                export CENCI_SANDBOX=1
                export CENCI_RESOLVE_USR_LOCAL_BIN="$TMP/no-such-usr-local-cenci"
                export CENCI_RESOLVE_HOMEBREW_BIN="$TMP/no-such-homebrew-cenci"
                if [ -n "$candidate_bin" ]; then
                    export CENCI_BIN="$candidate_bin"
                fi
                "$shell" "$layout/opencode/bootstrap.sh"
            )
        }

        for HOOK_SHELL in bash dash; do
            if ! command -v "$HOOK_SHELL" >/dev/null 2>&1; then
                echo "SKIP - $HOOK_SHELL not found; skipping its fallback-resolution mirror case"
                continue
            fi

            shell_tmp="$TMP/$HOOK_SHELL"
            mkdir -p "$shell_tmp"

            # --- Mirrors case 1: adopts a fallback when the download fails --
            fixture_dir="$shell_tmp/fixture"
            mkdir -p "$fixture_dir"
            fixture_bin="$fixture_dir/cenci"
            cat >"$fixture_bin" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
            chmod +x "$fixture_bin"

            layout_adopt="$shell_tmp/layout-adopt"
            log_adopt="$shell_tmp/log-adopt"
            mkdir -p "$log_adopt"

            run_opencode_bootstrap_nobin "$HOOK_SHELL" "$layout_adopt" "$log_adopt" "$fixture_bin"

            adopted_bin="$layout_adopt/bin/cenci"
            if [ -x "$adopted_bin" ] && [ ! -L "$adopted_bin" ] && cmp -s "$adopted_bin" "$fixture_bin"; then
                ok "[$HOOK_SHELL] adopts a fallback binary (real copy, not a symlink) when the download fails"
            else
                bad "[$HOOK_SHELL] did not adopt the fallback binary as a real copy at bin/cenci"
            fi

            if grep -q "adopted fallback cenci from $fixture_bin" "$log_adopt/cenci-bootstrap.log" 2>/dev/null; then
                ok "[$HOOK_SHELL] log names the adopted fallback's source path"
            else
                bad "[$HOOK_SHELL] log did not name the adopted fallback's source path"
            fi

            # --- Mirrors case 3: no candidate -> still exits 0, and says why -
            layout_none="$shell_tmp/layout-none"
            log_none="$shell_tmp/log-none"
            mkdir -p "$log_none"

            run_opencode_bootstrap_nobin "$HOOK_SHELL" "$layout_none" "$log_none" ""
            rc_none=$?

            if [ "$rc_none" -eq 0 ]; then
                ok "[$HOOK_SHELL] bootstrap exits 0 when no cenci binary can be resolved anywhere"
            else
                bad "[$HOOK_SHELL] bootstrap exited $rc_none when no cenci binary could be resolved"
            fi

            if [ ! -e "$layout_none/bin/cenci" ]; then
                ok "[$HOOK_SHELL] no bin/cenci is created when nothing resolves"
            else
                bad "[$HOOK_SHELL] bin/cenci unexpectedly exists when nothing should have resolved"
            fi

            if grep -q "no cenci binary available" "$log_none/cenci-bootstrap.log" 2>/dev/null; then
                ok "[$HOOK_SHELL] log names the no-candidate-found reason"
            else
                bad "[$HOOK_SHELL] log did not name the no-candidate-found reason"
            fi
        done
    fi
else
    echo "SKIP - jq/bash unavailable; skipping bootstrap end-to-end test"
fi

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
