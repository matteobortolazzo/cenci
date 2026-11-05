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
# real bootstrap log nor touches the developer's ~/.local/bin, and
# XDG_STATE_HOME is pinned under the temp HOME so a bootstrap failure record
# (#1239) never reaches the ambient hook-health store. $ROOT_VAR_NAME
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
    env "${ROOT_VAR_NAME}=${root}" TMPDIR="$logdir" HOME="$home" XDG_STATE_HOME="$home/state" \
      CENCI_SANDBOX="$cenci_sandbox" bash "$BOOTSTRAP"
  else
    (
      unset CENCI_SANDBOX
      # shellcheck disable=SC2030,SC2031 # intentionally subshell-scoped
      export TMPDIR="$logdir" HOME="$home" XDG_STATE_HOME="$home/state"
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
    # shellcheck disable=SC2030,SC2031 # intentionally subshell-scoped
    export TMPDIR="$logdir" HOME="$home" XDG_STATE_HOME="$home/state" PATH="$stubdir:/usr/bin:/bin"
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
  # shellcheck disable=SC2016 # single-quoted: $0 expands in the invoked shell, not here
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

  # --- Isolation: a terminal bootstrap failure never reaches the ambient store -
  #
  # run_bootstrap_nobin must pin XDG_STATE_HOME itself (#1239): with no binary
  # anywhere the run writes a bootstrap-failure hook-health record, which would
  # otherwise land in whatever store the developer's environment points at
  # (watch/docs/test-isolation.md).
  ambient_state="$shell_tmp/ambient-state"
  mkdir -p "$ambient_state"
  root_iso="$shell_tmp/root-iso"
  log_iso="$shell_tmp/log-iso"
  mkdir -p "$log_iso"
  (
    # shellcheck disable=SC2031 # intentionally subshell-scoped
    export XDG_STATE_HOME="$ambient_state"
    run_bootstrap_nobin "$HOOK_SHELL" "$root_iso" "$shell_tmp/calls-iso" "$log_iso" ""
  )
  if grep -q "no cenci binary available" "$log_iso/cenci-bootstrap.log" 2>/dev/null &&
    [ -z "$(ls -A "$ambient_state")" ]; then
    echo "ok   - [$HOOK_SHELL] a terminal bootstrap failure leaves an ambient XDG_STATE_HOME untouched"
    pass=$((pass + 1))
  else
    echo "FAIL - [$HOOK_SHELL] a terminal bootstrap failure wrote into the ambient XDG_STATE_HOME (or never ran)"
    find "$ambient_state" -mindepth 1 2>/dev/null | sed 's/^/           /'
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

# --- Hook-health records (#1239) -----------------------------------------------
#
# The shell-side writers (missing binary in notify.sh, bootstrap failure in
# bootstrap-common.sh) must leave a bounded, closed-set record in
# $XDG_STATE_HOME/cenci/hook-health/ that survives the hook's outer
# `2>/dev/null || true`, without ever changing the hook's exit code or output.
#
# Callers supply HOOKS_JSON (this client's real hooks.json), NOTIFY_REL
# (notify.sh relative to the plugin root) and CLIENT (claude|codex). Each case
# runs the REAL hooks.json command through `<shell> -c` against a temp plugin
# ROOT holding copies of hooks/ codex/ lib/ whose shebangs point at <shell>
# (so the #!/bin/sh scripts are really run under bash and dash), with offline
# curl/wget stubs, CENCI_RESOLVE_* pointed at nonexistent paths, and an
# isolated HOME/XDG_STATE_HOME/TMPDIR (watch/docs/test-isolation.md).

HH_NAMES="missing-binary bootstrap-failure malformed-input socket-unavailable delivery-failure last-ok"
HH_SENT_PROMPT="SENTINEL_PROMPT_7c1e"
HH_SENT_TOOL="SENTINEL_TOOL_INPUT_7c1e"
HH_SENT_TRANSCRIPT="SENTINEL_TRANSCRIPT_PATH_7c1e"
HH_SENT_API="SENTINEL_ANTHROPIC_API_KEY_7c1e"
HH_SENT_GH="SENTINEL_GH_TOKEN_7c1e"
HH_CASE=0

# hh_assert <description> <command...> -- passes when the command succeeds.
hh_assert() {
  local desc="$1"
  shift
  if "$@"; then
    echo "ok   - $desc"
    pass=$((pass + 1))
  else
    echo "FAIL - $desc"
    fail=$((fail + 1))
  fi
}

# hh_record_valid <file> <client> -- a regular, non-symlink file of at most 32
# bytes holding exactly one "<epoch> <client>" line with its newline.
hh_record_valid() {
  [ -f "$1" ] && [ ! -L "$1" ] || return 1
  [ "$(wc -c <"$1" | tr -d ' ')" -le 32 ] || return 1
  [ "$(wc -l <"$1" | tr -d ' ')" -eq 1 ] || return 1
  grep -Eq "^[0-9]+ $2\$" "$1"
}

# hh_record_ts <file> -- prints the record's epoch field.
hh_record_ts() {
  cut -d' ' -f1 "$1"
}

# hh_setup <shell> <label> -- builds an isolated plugin ROOT/HOME/state/TMPDIR
# for one case and exports its locations through HH_* globals.
hh_setup() {
  HH_SHELL_PATH="$(command -v "$1")"
  HH_CASE=$((HH_CASE + 1))
  local base="$TMP/hh/$1-$HH_CASE-$2"
  HH_ROOT="$base/root"
  HH_HOME="$base/home"
  HH_STATE="$base/state"
  HH_TMPD="$base/tmp"
  HH_STUBS="$base/stubs"
  HH_STORE="$HH_STATE/cenci/hook-health"
  HH_OUT="$base/out"
  HH_ERR="$base/err"
  mkdir -p "$HH_ROOT/hooks" "$HH_ROOT/codex" "$HH_ROOT/lib" "$HH_ROOT/$MANIFEST_DIR" \
    "$HH_HOME" "$HH_STATE" "$HH_TMPD" "$HH_STUBS"
  cp "$PLUGIN_JSON" "$HH_ROOT/$MANIFEST_DIR/plugin.json"

  local d f
  for d in hooks codex lib; do
    for f in "$DIR/../$d/"*.sh; do
      sed "1s|^#!.*|#!$HH_SHELL_PATH|" "$f" >"$HH_ROOT/$d/$(basename "$f")"
      chmod +x "$HH_ROOT/$d/$(basename "$f")"
    done
  done

  local tool
  for tool in curl wget; do
    printf '#!/bin/sh\nexit 1\n' >"$HH_STUBS/$tool"
    chmod +x "$HH_STUBS/$tool"
  done
}

# hh_env [NAME=VALUE...] <command...> -- runs the command with the isolated
# environment. CENCI_BIN/CENCI_SANDBOX are scrubbed unless the caller sets them.
hh_env() {
  env -u CENCI_BIN -u CENCI_SANDBOX \
    HOME="$HH_HOME" XDG_STATE_HOME="$HH_STATE" TMPDIR="$HH_TMPD" \
    PATH="$HH_STUBS:/usr/bin:/bin" \
    CENCI_RESOLVE_USR_LOCAL_BIN="$TMP/hh-no-such-usr-local-cenci" \
    CENCI_RESOLVE_HOMEBREW_BIN="$TMP/hh-no-such-homebrew-cenci" \
    "${ROOT_VAR_NAME}=$HH_ROOT" "$@"
}

# hh_run <event> <stdin-file> [NAME=VALUE...] -- runs the real hooks.json
# command for <event>; sets HH_RC, HH_ELAPSED (whole seconds), HH_OUT, HH_ERR.
hh_run() {
  local ev="$1" stdin_file="$2" cmd t0 t1
  shift 2
  cmd="$(jq -r --arg ev "$ev" '.hooks[$ev][0].hooks[0].command' "$HOOKS_JSON")"
  t0="$(date +%s)"
  hh_env "$@" "$HH_SHELL_PATH" -c "$cmd" <"$stdin_file" >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  t1="$(date +%s)"
  HH_ELAPSED=$((t1 - t0))
}

# hh_hook_timeout <event> -- the manifest timeout (seconds) of the event's hook.
hh_hook_timeout() {
  jq -r --arg ev "$1" '.hooks[$ev][0].hooks[0].timeout' "$HOOKS_JSON"
}

# hh_silent -- stdout and stderr of the last run were both empty.
hh_silent() {
  [ ! -s "$HH_OUT" ] && [ ! -s "$HH_ERR" ]
}

# hh_store_names_allowed -- every entry of the store (dotfiles included) is one
# of the closed-set file names, and nothing else (no leftover temp files).
hh_store_names_allowed() {
  local path entry name allowed
  for path in "$HH_STORE"/* "$HH_STORE"/.[!.]*; do
    [ -e "$path" ] || [ -L "$path" ] || continue
    entry="${path##*/}"
    allowed=0
    for name in $HH_NAMES; do
      [ "$entry" = "$name" ] && allowed=1
    done
    [ "$allowed" -eq 1 ] || return 1
  done
  return 0
}

# hh_modes_ok -- state root and store are exactly 0700, the missing-binary
# record exactly 0600.
hh_modes_ok() {
  [ -n "$(find "$HH_STATE/cenci" -prune -type d -perm 700 -print)" ] &&
    [ -n "$(find "$HH_STORE" -prune -type d -perm 700 -print)" ] &&
    [ -n "$(find "$HH_STORE/missing-binary" -prune -type f -perm 600 -print)" ]
}

# hh_store_count -- number of entries (dotfiles included) in the store.
hh_store_count() {
  find "$HH_STORE" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' '
}

# hh_writer <category> <client> -- calls the sh writer directly under the
# case's shell, capturing its stdout/stderr in HH_OUT/HH_ERR.
hh_writer() {
  # shellcheck disable=SC2016 # single-quoted: $0/$1/$2 expand in the invoked shell, not here
  hh_env "$HH_SHELL_PATH" -c '. "$0"; cenci_hook_health_record "$1" "$2"' \
    "$HH_ROOT/lib/hook-health.sh" "$1" "$2" >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
}

# hh_seed_store -- creates a 0700 store the way the writer would.
hh_seed_store() {
  mkdir -p "$HH_STORE"
  chmod 700 "$HH_STATE/cenci" "$HH_STORE"
}

HH_EMPTY="$TMP/hh-empty-stdin"
: >"$HH_EMPTY"
HH_PAYLOAD="$TMP/hh-payload.json"
printf '{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"%s","tool_input":{"command":"%s"},"transcript_path":"/%s/t.jsonl"}\n' \
  "$HH_SENT_PROMPT" "$HH_SENT_TOOL" "$HH_SENT_TRANSCRIPT" >"$HH_PAYLOAD"

for HOOK_SHELL in bash dash; do
  if ! command -v "$HOOK_SHELL" >/dev/null 2>&1; then
    echo "SKIP - $HOOK_SHELL not found; skipping its hook-health cases"
    continue
  fi
  tag="[$HOOK_SHELL][$CLIENT]"

  # --- SessionStart, no binary anywhere: missing-binary + bootstrap-failure --
  hh_setup "$HOOK_SHELL" session-start
  st_timeout="$(hh_hook_timeout SessionStart)"
  hh_run SessionStart "$HH_EMPTY"
  hh_assert "$tag SessionStart hook exits 0 with no binary" test "$HH_RC" -eq 0
  hh_assert "$tag SessionStart hook prints nothing on stdout or stderr" hh_silent
  hh_assert "$tag SessionStart hook returns within the manifest timeout (${st_timeout}s)" \
    test "$HH_ELAPSED" -lt "$st_timeout"
  hh_assert "$tag missing-binary record written with client $CLIENT" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"
  wait_for_file "$HH_STORE/bootstrap-failure" 100
  hh_assert "$tag bootstrap-failure record written (terminal install failure) with client $CLIENT" \
    hh_record_valid "$HH_STORE/bootstrap-failure" "$CLIENT"
  hh_assert "$tag store holds the 0700 dirs and 0600 records" hh_modes_ok

  # --- UserPromptSubmit, no binary: missing-binary --------------------------
  hh_setup "$HOOK_SHELL" prompt
  hh_run UserPromptSubmit "$HH_PAYLOAD"
  hh_assert "$tag UserPromptSubmit exits 0 with no binary, silent" \
    test "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"
  hh_assert "$tag UserPromptSubmit with no binary records missing-binary ($CLIENT)" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"

  # --- The wrapper itself is silent (the outer 2>/dev/null hides nothing) ----
  hh_setup "$HOOK_SHELL" direct
  hh_env "$HH_ROOT/$NOTIFY_REL" <"$HH_PAYLOAD" >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag notify.sh exits 0 and is silent on stdout and stderr with no binary" \
    test "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"
  hh_assert "$tag notify.sh itself records missing-binary" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"

  # --- Fail open: a missing hook-health.sh must not break the wrapper --------
  # (a partial plugin update). dash exits on a failed `.`, so the wrapper has
  # to test readability before sourcing; the outcome is simply "no record".
  hh_setup "$HOOK_SHELL" no-lib
  rm -f "$HH_ROOT/lib/hook-health.sh"
  hh_env "$HH_ROOT/$NOTIFY_REL" <"$HH_PAYLOAD" >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag notify.sh exits 0 and is silent when hook-health.sh is missing" \
    test "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"
  hh_assert "$tag notify.sh without hook-health.sh writes no record" \
    test ! -e "$HH_STATE/cenci"

  # --- Records are also written inside a sandbox (no env gate) --------------
  hh_setup "$HOOK_SHELL" sandbox
  hh_run UserPromptSubmit "$HH_PAYLOAD" CENCI_SANDBOX=1
  hh_assert "$tag missing-binary is recorded with CENCI_SANDBOX=1 as well" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"

  # --- Happy path unchanged: a working binary leaves no record --------------
  hh_setup "$HOOK_SHELL" happy
  mkdir -p "$HH_ROOT/bin"
  printf '#!/bin/sh\nexit 0\n' >"$HH_ROOT/bin/cenci"
  chmod +x "$HH_ROOT/bin/cenci"
  hh_run UserPromptSubmit "$HH_PAYLOAD"
  hh_assert "$tag a resolvable binary writes no hook-health record" \
    test "$HH_RC" -eq 0 -a ! -e "$HH_STATE/cenci"

  # --- Bootstrap: only a terminal install failure records -------------------
  hh_setup "$HOOK_SHELL" bootstrap-adopt
  hh_fixture="$HH_STUBS/fixture-cenci"
  printf '#!/bin/sh\nexit 0\n' >"$hh_fixture"
  chmod +x "$hh_fixture"
  hh_env CENCI_BIN="$hh_fixture" CENCI_SANDBOX=1 "$HH_ROOT/${NOTIFY_REL%/*}/bootstrap.sh" \
    </dev/null >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag a fallback adoption after a failed download records nothing" \
    test "$HH_RC" -eq 0 -a -x "$HH_ROOT/bin/cenci" -a ! -e "$HH_STORE/bootstrap-failure"

  hh_setup "$HOOK_SHELL" bootstrap-link
  mkdir -p "$HH_ROOT/bin" "$HH_HOME/.local/bin"
  printf '#!/bin/sh\nexit 0\n' >"$HH_ROOT/bin/cenci"
  chmod +x "$HH_ROOT/bin/cenci"
  printf '%s\n' "$VERSION" >"$HH_ROOT/bin/.cenci-version"
  printf 'not a symlink\n' >"$HH_HOME/.local/bin/cenci"
  hh_env CENCI_SANDBOX=1 "$HH_ROOT/${NOTIFY_REL%/*}/bootstrap.sh" \
    </dev/null >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag a PATH-link failure after a good install records nothing" \
    test "$HH_RC" -eq 0 -a ! -e "$HH_STORE/bootstrap-failure"

  hh_setup "$HOOK_SHELL" bootstrap-fail
  hh_env CENCI_SANDBOX=1 "$HH_ROOT/${NOTIFY_REL%/*}/bootstrap.sh" \
    </dev/null >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag bootstrap exits 0 and records bootstrap-failure ($CLIENT) when install_binary fails" \
    test "$HH_RC" -eq 0 -a -s "$HH_STORE/bootstrap-failure"
  hh_assert "$tag bootstrap-failure record is a valid $CLIENT record" \
    hh_record_valid "$HH_STORE/bootstrap-failure" "$CLIENT"

  # A missing hook-health.sh (partial plugin update) must not abort bootstrap:
  # diagnostics are best-effort and the hook contract is exit 0 (dash exits on
  # a failed `.`, so the source has to be guarded).
  hh_setup "$HOOK_SHELL" bootstrap-no-lib
  rm -f "$HH_ROOT/lib/hook-health.sh"
  hh_fixture="$HH_STUBS/fixture-cenci"
  printf '#!/bin/sh\nexit 0\n' >"$hh_fixture"
  chmod +x "$hh_fixture"
  hh_env CENCI_BIN="$hh_fixture" CENCI_SANDBOX=1 "$HH_ROOT/${NOTIFY_REL%/*}/bootstrap.sh" \
    </dev/null >"$HH_OUT" 2>"$HH_ERR"
  HH_RC=$?
  hh_assert "$tag bootstrap still exits 0 and installs when hook-health.sh is missing" \
    test "$HH_RC" -eq 0 -a -x "$HH_ROOT/bin/cenci"
  hh_assert "$tag bootstrap logs that hook-health.sh is missing and diagnostics are disabled" \
    grep -q "hook-health.sh missing; diagnostics disabled" "$HH_TMPD/cenci-bootstrap.log"

  # --- AC3: nothing but category/client/timestamp reaches the store ---------
  hh_setup "$HOOK_SHELL" sentinels
  hh_run SessionStart "$HH_PAYLOAD" \
    ANTHROPIC_API_KEY="$HH_SENT_API" GH_TOKEN="$HH_SENT_GH"
  wait_for_file "$HH_STORE/bootstrap-failure" 100
  hh_assert "$tag sentinel run produced both shell-side records" \
    test -s "$HH_STORE/missing-binary" -a -s "$HH_STORE/bootstrap-failure"
  hh_assert "$tag no prompt/tool/transcript/secret sentinel appears anywhere in the store" \
    test -z "$(grep -rlF -e "$HH_SENT_PROMPT" -e "$HH_SENT_TOOL" -e "$HH_SENT_TRANSCRIPT" \
      -e "$HH_SENT_API" -e "$HH_SENT_GH" "$HH_STATE" 2>/dev/null)"
  hh_assert "$tag every record matches ^[0-9]+ $CLIENT\\n and names are closed-set" \
    test "$(hh_record_valid "$HH_STORE/missing-binary" "$CLIENT" && hh_record_valid "$HH_STORE/bootstrap-failure" "$CLIENT" && hh_store_names_allowed && echo yes)" = yes

  # --- AC4: bounded store, rate limit rules, parallel writers ----------------
  hh_setup "$HOOK_SHELL" loop
  hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  first_size="$(wc -c <"$HH_STORE/missing-binary" | tr -d ' ')"
  first_ts="$(hh_record_ts "$HH_STORE/missing-binary")"
  i=0
  while [ "$i" -lt 50 ]; do
    hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
    i=$((i + 1))
  done
  hh_assert "$tag 50 repeated failures leave only closed-set file names (no temp leftovers)" \
    hh_store_names_allowed
  hh_assert "$tag 50 repeated failures keep exactly one missing-binary record of constant size" \
    test "$(hh_store_count)" -eq 1 -a \
    "$(wc -c <"$HH_STORE/missing-binary" | tr -d ' ')" -eq "$first_size"
  hh_assert "$tag a failure record younger than 60s is not rewritten" \
    test "$(hh_record_ts "$HH_STORE/missing-binary")" = "$first_ts"

  hh_setup "$HOOK_SHELL" ratelimit
  hh_seed_store
  now="$(date +%s)"
  recent=$((now - 30))
  printf '%s %s\n' "$recent" "$CLIENT" >"$HH_STORE/missing-binary"
  hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  hh_assert "$tag a 30s-old record is left untouched" \
    test "$(hh_record_ts "$HH_STORE/missing-binary")" = "$recent"
  stale=$((now - 120))
  printf '%s %s\n' "$stale" "$CLIENT" >"$HH_STORE/missing-binary"
  hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  hh_assert "$tag a 120s-old record is rewritten" \
    test "$(hh_record_ts "$HH_STORE/missing-binary")" -ge "$now"
  printf 'garbage that is not a record\n' >"$HH_STORE/missing-binary"
  hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  hh_assert "$tag a malformed record is replaced by a valid one" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"
  printf '%s %s\n' "$((now + 3600))" "$CLIENT" >"$HH_STORE/missing-binary"
  hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  hh_assert "$tag a future-dated record is replaced by a current one" \
    test "$(hh_record_ts "$HH_STORE/missing-binary")" -le "$((now + 5))" -a \
    "$(hh_record_ts "$HH_STORE/missing-binary")" -ge "$now"

  hh_setup "$HOOK_SHELL" concurrent
  hh_seed_store
  printf '%s %s\n' "$(($(date +%s) - 300))" "$CLIENT" >"$HH_STORE/missing-binary"
  pids=""
  i=0
  while [ "$i" -lt 20 ]; do
    hh_env "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1 &
    pids="$pids $!"
    i=$((i + 1))
  done
  for pid in $pids; do
    wait "$pid"
  done
  hh_assert "$tag 20 concurrent wrappers leave a parseable record and no temp files" \
    test "$(hh_record_valid "$HH_STORE/missing-binary" "$CLIENT" && hh_store_names_allowed && echo yes)" = yes

  # --- Path resolution and closed sets ---------------------------------------
  hh_setup "$HOOK_SHELL" relative-xdg
  hh_env XDG_STATE_HOME="relative/state" "$HH_ROOT/$NOTIFY_REL" </dev/null >/dev/null 2>&1
  hh_assert "$tag a relative XDG_STATE_HOME falls back to \$HOME/.local/state" \
    hh_record_valid "$HH_HOME/.local/state/cenci/hook-health/missing-binary" "$CLIENT"
  hh_assert "$tag a relative XDG_STATE_HOME is never written relative to the cwd" \
    test ! -e "relative/state" -a ! -e "$HH_STATE/cenci"

  hh_setup "$HOOK_SHELL" closed-set
  hh_writer last-ok "$CLIENT"
  hh_writer ../escape "$CLIENT"
  hh_writer missing-binary opencode
  hh_writer missing-binary ""
  hh_assert "$tag the sh writer refuses categories and clients outside the closed set" \
    test ! -e "$HH_STATE/cenci" -a ! -e "$HH_STATE/escape" -a "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"

  # --- Hardening: the sh writer refuses unsafe store directories -------------
  hh_setup "$HOOK_SHELL" fresh-modes
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a fresh store is created 0700/0700 with a 0600 record, silently" \
    test "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR" -a "$(hh_modes_ok && echo yes)" = yes

  hh_setup "$HOOK_SHELL" harden-control
  hh_seed_store
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag control: a pre-existing 0700 store accepts a record" \
    hh_record_valid "$HH_STORE/missing-binary" "$CLIENT"

  hh_setup "$HOOK_SHELL" harden-leaf-symlink
  mkdir -p "$HH_STATE/cenci" "$HH_STATE/elsewhere"
  chmod 700 "$HH_STATE/cenci" "$HH_STATE/elsewhere"
  ln -s "$HH_STATE/elsewhere" "$HH_STORE"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a symlinked store directory is refused (nothing written through it)" \
    test -z "$(ls -A "$HH_STATE/elsewhere")" -a "$HH_RC" -eq 0 -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"

  hh_setup "$HOOK_SHELL" harden-root-symlink
  mkdir -p "$HH_STATE/elsewhere"
  chmod 700 "$HH_STATE/elsewhere"
  ln -s "$HH_STATE/elsewhere" "$HH_STATE/cenci"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a symlinked state root is refused (nothing created through it)" \
    test -z "$(ls -A "$HH_STATE/elsewhere")"

  hh_setup "$HOOK_SHELL" harden-leaf-0777
  hh_seed_store
  chmod 777 "$HH_STORE"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a 0777 store directory is refused" \
    test -z "$(ls -A "$HH_STORE")" -a ! -s "$HH_OUT" -a ! -s "$HH_ERR"

  hh_setup "$HOOK_SHELL" harden-leaf-0770
  hh_seed_store
  chmod 770 "$HH_STORE"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a group-writable store directory is refused" \
    test -z "$(ls -A "$HH_STORE")"

  hh_setup "$HOOK_SHELL" harden-root-0777
  hh_seed_store
  chmod 777 "$HH_STATE/cenci"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a 0777 state root is refused" \
    test -z "$(ls -A "$HH_STORE")"

  hh_setup "$HOOK_SHELL" harden-leaf-file
  mkdir -p "$HH_STATE/cenci"
  chmod 700 "$HH_STATE/cenci"
  printf 'plain file\n' >"$HH_STORE"
  hh_writer missing-binary "$CLIENT"
  hh_assert "$tag a non-directory at the store path is refused and left untouched" \
    test -f "$HH_STORE" -a "$(cat "$HH_STORE")" = "plain file"
done

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
