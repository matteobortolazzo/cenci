# shellcheck shell=bash
# Shared body for the Codex and Claude Code bootstrap gate tests
# (watch/plugin/codex/test.sh, watch/plugin/hooks/test.sh).
#
# Inside a cenci sandbox container (CENCI_SANDBOX=1), cenci controls nothing
# on the host, so spawning a container-local daemon just masks real wiring
# failures (#195, #202). This asserts bootstrap.sh's start_daemon() never
# invokes the `daemon` subcommand when CENCI_SANDBOX=1, and that behavior is
# unchanged (daemon IS invoked) when CENCI_SANDBOX is unset.
#
# Callers must set, before sourcing this file:
#   BOOTSTRAP      - path to the bootstrap.sh under test
#   PLUGIN_JSON    - path to that plugin's real plugin.json (for VERSION)
#   MANIFEST_DIR   - plugin manifest dir name (".codex-plugin"/".claude-plugin")
#   ROOT_VAR_NAME  - env var name bootstrap.sh resolves its root from
#                    ("PLUGIN_ROOT"/"CLAUDE_PLUGIN_ROOT")

if ! command -v jq >/dev/null 2>&1; then
  echo "SKIP - jq not found"
  exit 0
fi
if ! command -v bash >/dev/null 2>&1; then
  echo "SKIP - bash not found"
  exit 0
fi

VERSION="$(jq -r '.version' "$PLUGIN_JSON" 2>/dev/null)"
if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
  echo "SKIP - could not read version from $PLUGIN_JSON"
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

# wait_for_file polls for $1 to become non-empty, up to ~$2 seconds.
wait_for_file() {
  local file="$1" timeout_ds="${2:-20}" waited=0
  while [ ! -s "$file" ] && [ "$waited" -lt "$timeout_ds" ]; do
    sleep 0.1
    waited=$((waited + 1))
  done
}

# run_bootstrap <root> <calls-file> <log-dir> <cenci_sandbox-value-or-empty>
#
# Builds a self-contained plugin ROOT with a stub bin/cenci that records
# every invocation to <calls-file> and exits 0. The stub version marker is
# pre-stamped so install_binary short-circuits without touching the network.
# TMPDIR and HOME are redirected into temp dirs so the run neither writes the
# real bootstrap log nor touches the developer's ~/.local/bin. $ROOT_VAR_NAME
# is the root-resolution env var bootstrap.sh itself reads (PLUGIN_ROOT for
# Codex, CLAUDE_PLUGIN_ROOT for Claude Code).
run_bootstrap() {
  local root="$1" calls="$2" logdir="$3" cenci_sandbox="$4"

  mkdir -p "$root/bin" "$root/$MANIFEST_DIR"
  cp "$PLUGIN_JSON" "$root/$MANIFEST_DIR/plugin.json"

  cat >"$root/bin/cenci" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$calls"
exit 0
STUB
  chmod +x "$root/bin/cenci"
  printf '%s\n' "$VERSION" >"$root/bin/.cenci-version"

  local home
  home="$(mktemp -d)"

  if [ -n "$cenci_sandbox" ]; then
    env "${ROOT_VAR_NAME}=${root}" TMPDIR="$logdir" HOME="$home" CENCI_SANDBOX="$cenci_sandbox" \
      bash "$BOOTSTRAP"
  else
    (
      unset CENCI_SANDBOX
      export TMPDIR="$logdir" HOME="$home"
      export "${ROOT_VAR_NAME}=${root}"
      bash "$BOOTSTRAP"
    )
  fi
}

# --- Case 1: CENCI_SANDBOX=1 -> no daemon spawn, log line present ---------
root_sand="$TMP/root-sand"
calls_sand="$TMP/calls-sand"
log_sand="$TMP/log-sand"
mkdir -p "$log_sand"
: >"$calls_sand"

run_bootstrap "$root_sand" "$calls_sand" "$log_sand" "1"

# Bounded settle: start_daemon backgrounds via nohup, so give a spurious
# spawn a moment to land before asserting its absence.
sleep 0.3

if [ -s "$calls_sand" ]; then
  echo "FAIL - daemon subcommand invoked when CENCI_SANDBOX=1"
  echo "         calls:"
  sed 's/^/           /' "$calls_sand"
  fail=$((fail + 1))
else
  echo "ok   - daemon subcommand NOT invoked when CENCI_SANDBOX=1"
  pass=$((pass + 1))
fi

LOGFILE_SAND="$log_sand/cenci-bootstrap.log"
if grep -q "not starting a local daemon" "$LOGFILE_SAND" 2>/dev/null; then
  echo "ok   - bootstrap logs the CENCI_SANDBOX skip reason"
  pass=$((pass + 1))
else
  echo "FAIL - bootstrap did not log a CENCI_SANDBOX skip reason"
  echo "         log contents:"
  sed 's/^/           /' "$LOGFILE_SAND" 2>/dev/null || echo "           (no log file)"
  fail=$((fail + 1))
fi

# --- Case 2: CENCI_SANDBOX unset -> daemon IS invoked (unchanged) ---------
root_normal="$TMP/root-normal"
calls_normal="$TMP/calls-normal"
log_normal="$TMP/log-normal"
mkdir -p "$log_normal"
: >"$calls_normal"

run_bootstrap "$root_normal" "$calls_normal" "$log_normal" ""

wait_for_file "$calls_normal" 20

if [ -s "$calls_normal" ] && grep -q "daemon" "$calls_normal"; then
  echo "ok   - daemon subcommand invoked when CENCI_SANDBOX is unset (unchanged)"
  pass=$((pass + 1))
else
  echo "FAIL - daemon subcommand not invoked when CENCI_SANDBOX is unset"
  fail=$((fail + 1))
fi

# --- Binary fallback resolution (#1152) -------------------------------------
#
# run_bootstrap_nobin builds a plugin ROOT WITHOUT pre-creating bin/cenci and
# WITHOUT stamping .cenci-version (unless the caller pre-seeds bin/ before
# calling), so install_binary() genuinely reaches its download path. A stub
# curl/wget (both exit 1, and each records its own invocation into
# <log-dir>/download-attempts) is prepended ahead of a minimal PATH so the
# download fails offline and reproducibly -- the real "download failed"
# branch, with no network and no new production seam. The plugin-cache
# glob candidates are pointed at a fresh, empty $HOME by construction, and
# the fixed-prefix candidates (/usr/local/bin, /opt/homebrew/bin) are
# pointed at nonexistent test paths via resolve-bin.sh's test-only
# overrides, so a real /usr/local/bin/cenci or ambient plugin cache in the
# dev/CI container can never make a "no candidate" case vacuously pass
# (watch/docs/test-isolation.md). CENCI_SANDBOX=1 is pinned explicitly per
# the same doc's ambient-daemon-socket guidance: none of these cases assert
# daemon-start behavior, so pinning it keeps start_daemon from nohup-ing a
# stub and keeps the ambient value irrelevant either way -- and the
# subshell's own `unset CENCI_SANDBOX` before re-exporting it keeps that
# hygiene consistent with run_bootstrap above. Runs bootstrap.sh under
# <shell> (bash or dash) so this new POSIX code is covered under both
# (flow/docs/shell-scripting-gotchas.md: these #!/bin/sh scripts are only
# ever executed under bash in tests otherwise, while ubuntu-latest's
# /bin/sh is dash).
run_bootstrap_nobin() {
  local shell="$1" root="$2" calls="$3" logdir="$4" candidate_bin="$5"

  mkdir -p "$root/$MANIFEST_DIR"
  cp "$PLUGIN_JSON" "$root/$MANIFEST_DIR/plugin.json"

  local stubdir
  stubdir="$(mktemp -d)"
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
  home="$(mktemp -d)"

  (
    unset CENCI_SANDBOX
    export TMPDIR="$logdir" HOME="$home" PATH="$stubdir:/usr/bin:/bin"
    export "${ROOT_VAR_NAME}=${root}"
    export CENCI_SANDBOX=1
    export CENCI_RESOLVE_USR_LOCAL_BIN="$TMP/no-such-usr-local-cenci"
    export CENCI_RESOLVE_HOMEBREW_BIN="$TMP/no-such-homebrew-cenci"
    if [ -n "$candidate_bin" ]; then
      export CENCI_BIN="$candidate_bin"
    fi
    "$shell" "$BOOTSTRAP"
  )
}

RESOLVE_BIN_SH="$DIR/../lib/resolve-bin.sh"

# run_resolve <shell> [env assignments...] -- runs resolve_cenci_bin() under
# <shell> (bash/dash) with resolve-bin.sh sourced, printing its stdout
# (empty on failure/non-zero exit). Env assignments are applied via `env` so
# BIN/ROOT/CENCI_BIN/CENCI_RESOLVE_*/HOME/PATH are scoped to this one
# invocation without leaking into the rest of the test process.
run_resolve() {
  local shell="$1"
  shift
  env "$@" "$shell" -c '. "$0"; resolve_cenci_bin' "$RESOLVE_BIN_SH" 2>/dev/null
}

for HOOK_SHELL in bash dash; do
  if ! command -v "$HOOK_SHELL" >/dev/null 2>&1; then
    echo "SKIP - $HOOK_SHELL not found; skipping its fallback-resolution cases"
    continue
  fi

  shell_tmp="$TMP/$HOOK_SHELL"
  mkdir -p "$shell_tmp"

  # --- New case: adopts a fallback when the download fails ------------------
  fixture_dir="$shell_tmp/fixture"
  mkdir -p "$fixture_dir"
  fixture_bin="$fixture_dir/cenci"
  cat >"$fixture_bin" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$fixture_bin"

  root_adopt="$shell_tmp/root-adopt"
  log_adopt="$shell_tmp/log-adopt"
  mkdir -p "$log_adopt"
  calls_adopt="$shell_tmp/calls-adopt"
  : >"$calls_adopt"

  run_bootstrap_nobin "$HOOK_SHELL" "$root_adopt" "$calls_adopt" "$log_adopt" "$fixture_bin"

  adopted_bin="$root_adopt/bin/cenci"
  if [ -x "$adopted_bin" ] && [ ! -L "$adopted_bin" ] && cmp -s "$adopted_bin" "$fixture_bin"; then
    echo "ok   - [$HOOK_SHELL] adopts a fallback binary (real copy, not a symlink) when the download fails"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] did not adopt the fallback binary as a real copy at \$ROOT/bin/cenci"
    fail=$((fail + 1))
  fi

  logfile_adopt="$log_adopt/cenci-bootstrap.log"
  if grep -q "adopted fallback cenci from $fixture_bin" "$logfile_adopt" 2>/dev/null; then
    echo "ok   - [$HOOK_SHELL] log names the adopted fallback's source path"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] log did not name the adopted fallback's source path"
    sed 's/^/           /' "$logfile_adopt" 2>/dev/null || echo "           (no log file)"
    fail=$((fail + 1))
  fi

  # --- New case: a fallback marker never suppresses a later real download --
  root_marker="$shell_tmp/root-marker"
  mkdir -p "$root_marker/bin"
  cat >"$root_marker/bin/cenci" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$root_marker/bin/cenci"
  printf 'fallback:/prior/fixture/cenci\n' >"$root_marker/bin/.cenci-version"

  log_marker="$shell_tmp/log-marker"
  mkdir -p "$log_marker"
  calls_marker="$shell_tmp/calls-marker"
  : >"$calls_marker"

  run_bootstrap_nobin "$HOOK_SHELL" "$root_marker" "$calls_marker" "$log_marker" ""

  recorder_marker="$log_marker/download-attempts"
  if [ -s "$recorder_marker" ]; then
    echo "ok   - [$HOOK_SHELL] a fallback: marker does not suppress a later real download attempt"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] install_binary's already-installed short-circuit latched onto a fallback: marker"
    fail=$((fail + 1))
  fi

  logfile_marker="$log_marker/cenci-bootstrap.log"
  if grep -q "download failed" "$logfile_marker" 2>/dev/null; then
    echo "ok   - [$HOOK_SHELL] log still records the real download failure after a fallback: marker"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] log did not record a download failure after a fallback: marker"
    fail=$((fail + 1))
  fi

  # --- New case: no candidate anywhere -> still exits 0, and says why -------
  root_none="$shell_tmp/root-none"
  log_none="$shell_tmp/log-none"
  mkdir -p "$log_none"
  calls_none="$shell_tmp/calls-none"
  : >"$calls_none"

  run_bootstrap_nobin "$HOOK_SHELL" "$root_none" "$calls_none" "$log_none" ""
  rc_none=$?

  if [ "$rc_none" -eq 0 ]; then
    echo "ok   - [$HOOK_SHELL] bootstrap exits 0 when no cenci binary can be resolved anywhere"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] bootstrap exited $rc_none when no cenci binary could be resolved"
    fail=$((fail + 1))
  fi

  if [ ! -e "$root_none/bin/cenci" ]; then
    echo "ok   - [$HOOK_SHELL] no \$ROOT/bin/cenci is created when nothing resolves"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] \$ROOT/bin/cenci unexpectedly exists when nothing should have resolved"
    fail=$((fail + 1))
  fi

  logfile_none="$log_none/cenci-bootstrap.log"
  if grep -q "no cenci binary available" "$logfile_none" 2>/dev/null; then
    echo "ok   - [$HOOK_SHELL] log names the no-candidate-found reason (silent otherwise, per #1152)"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] log did not name the no-candidate-found reason"
    fail=$((fail + 1))
  fi

  # --- New case: self-adoption guard ----------------------------------------
  #
  # Exercises resolve_cenci_bin() directly (not the full bootstrap flow):
  # the guard must reject a candidate that resolves -- directly, or through
  # a live symlink chain -- back to $BIN or anywhere under $ROOT/, and fall
  # through to the next usable, non-self candidate instead.
  guard_root="$shell_tmp/guard-root"
  mkdir -p "$guard_root/bin"
  guard_bin="$guard_root/bin/cenci"
  cat >"$guard_bin" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$guard_bin"

  outside_dir="$shell_tmp/guard-outside"
  mkdir -p "$outside_dir"
  outside_bin="$outside_dir/cenci"
  cp "$guard_bin" "$outside_bin"
  chmod +x "$outside_bin"

  under_root_bin="$guard_root/vendor/cenci"
  mkdir -p "$(dirname "$under_root_bin")"
  cp "$guard_bin" "$under_root_bin"
  chmod +x "$under_root_bin"

  symlink_dir="$shell_tmp/guard-symlink"
  mkdir -p "$symlink_dir"
  symlink_to_bin="$symlink_dir/cenci"
  ln -s "$guard_bin" "$symlink_to_bin"

  isolated_home="$(mktemp -d)"
  no_candidate_usr_local="$shell_tmp/no-usr-local-cenci"
  no_candidate_homebrew="$shell_tmp/no-homebrew-cenci"

  result_exact="$(run_resolve "$HOOK_SHELL" \
    BIN="$guard_bin" ROOT="$guard_root" CENCI_BIN="$guard_bin" \
    HOME="$isolated_home" PATH="/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$no_candidate_usr_local" \
    CENCI_RESOLVE_HOMEBREW_BIN="$no_candidate_homebrew")"
  if [ -z "$result_exact" ]; then
    echo "ok   - [$HOOK_SHELL] self-adoption guard rejects a candidate exactly equal to \$BIN"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] self-adoption guard did not reject a candidate exactly equal to \$BIN (got: $result_exact)"
    fail=$((fail + 1))
  fi

  result_symlink="$(run_resolve "$HOOK_SHELL" \
    BIN="$guard_bin" ROOT="$guard_root" CENCI_BIN="$symlink_to_bin" \
    HOME="$isolated_home" PATH="/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$no_candidate_usr_local" \
    CENCI_RESOLVE_HOMEBREW_BIN="$no_candidate_homebrew")"
  if [ -z "$result_symlink" ]; then
    echo "ok   - [$HOOK_SHELL] self-adoption guard rejects a live symlink chain back to \$BIN"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] self-adoption guard did not reject a symlink chain back to \$BIN (got: $result_symlink)"
    fail=$((fail + 1))
  fi

  result_under_root="$(run_resolve "$HOOK_SHELL" \
    BIN="$guard_bin" ROOT="$guard_root" CENCI_BIN="$under_root_bin" \
    HOME="$isolated_home" PATH="/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$outside_bin" \
    CENCI_RESOLVE_HOMEBREW_BIN="$no_candidate_homebrew")"
  if [ "$result_under_root" = "$outside_bin" ]; then
    echo "ok   - [$HOOK_SHELL] self-adoption guard rejects a candidate under \$ROOT/ and falls through to the next candidate"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] self-adoption guard did not reject/fall through for a candidate under \$ROOT/ (got: $result_under_root)"
    fail=$((fail + 1))
  fi

  # --- Regression: a non-self live-symlink candidate reached via the `for`
  # loop (not $CENCI_BIN) must be printed by its literal configured path,
  # not the canonicalized realpath _resolve_is_self resolves internally.
  # POSIX sh has no `local`; a helper reusing the bare name `c` would
  # silently clobber this loop's own `c` once an ACCEPTED (non-self)
  # candidate happened to be a live symlink -- printing the symlink's
  # target instead of the configured candidate path, which would corrupt
  # the "fallback:<path>"/"adopted fallback cenci from <path>" diagnostics
  # this ticket exists to add. This is deliberately unrelated to $BIN/$ROOT
  # (neither is set here) so the guard has nothing to reject; the only
  # thing under test is which path string comes out the other end.
  symlink_regression_target_dir="$shell_tmp/symlink-regression-target"
  mkdir -p "$symlink_regression_target_dir"
  symlink_regression_target_bin="$symlink_regression_target_dir/cenci"
  cat >"$symlink_regression_target_bin" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$symlink_regression_target_bin"

  symlink_regression_dir="$shell_tmp/symlink-regression-literal"
  mkdir -p "$symlink_regression_dir"
  symlink_regression_literal="$symlink_regression_dir/cenci"
  ln -s "$symlink_regression_target_bin" "$symlink_regression_literal"

  result_symlink_literal="$(run_resolve "$HOOK_SHELL" \
    HOME="$isolated_home" PATH="/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$symlink_regression_literal" \
    CENCI_RESOLVE_HOMEBREW_BIN="$no_candidate_homebrew")"
  if [ "$result_symlink_literal" = "$symlink_regression_literal" ]; then
    echo "ok   - [$HOOK_SHELL] a non-self live-symlink candidate is printed by its literal configured path, not its canonicalized target"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] a non-self live-symlink candidate was not printed by its literal path (got: $result_symlink_literal, want: $symlink_regression_literal)"
    fail=$((fail + 1))
  fi

  # --- New case: liveness probe ----------------------------------------------
  dead_dir="$shell_tmp/dead"
  mkdir -p "$dead_dir"
  dead_bin="$dead_dir/cenci"
  cat >"$dead_bin" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
  chmod +x "$dead_bin"

  alive_dir="$shell_tmp/alive"
  mkdir -p "$alive_dir"
  alive_bin="$alive_dir/cenci"
  cat >"$alive_bin" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$alive_bin"

  result_liveness="$(run_resolve "$HOOK_SHELL" \
    CENCI_BIN="$dead_bin" \
    HOME="$(mktemp -d)" PATH="/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$alive_bin" \
    CENCI_RESOLVE_HOMEBREW_BIN="$shell_tmp/no-homebrew-cenci-2")"
  if [ "$result_liveness" = "$alive_bin" ]; then
    echo "ok   - [$HOOK_SHELL] liveness probe rejects a -x candidate that exits non-zero on --version, falls through"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] liveness probe did not reject the dead candidate (got: $result_liveness)"
    fail=$((fail + 1))
  fi
done

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
