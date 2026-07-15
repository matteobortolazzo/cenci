#!/usr/bin/env bash
#
# Dependency-free gate test for the Codex bootstrap script.
#
# Inside a cenci sandbox container (CENCI_SANDBOX=1), cenci controls nothing
# on the host, so spawning a container-local daemon just masks real wiring
# failures (#195, #202). This asserts bootstrap.sh's start_daemon() never
# invokes the `daemon` subcommand when CENCI_SANDBOX=1, and that behavior is
# unchanged (daemon IS invoked) when CENCI_SANDBOX is unset.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
BOOTSTRAP="$DIR/bootstrap.sh"
PLUGIN_JSON="$DIR/../.codex-plugin/plugin.json"

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
# real bootstrap log nor touches the developer's ~/.local/bin. PLUGIN_ROOT
# (not CLAUDE_PLUGIN_ROOT) is the resolution Codex's native plugin loader
# sets, per bootstrap.sh's own ROOT fallback chain.
run_bootstrap() {
  local root="$1" calls="$2" logdir="$3" cenci_sandbox="$4"

  mkdir -p "$root/bin" "$root/.codex-plugin"
  cp "$PLUGIN_JSON" "$root/.codex-plugin/plugin.json"

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
    PLUGIN_ROOT="$root" TMPDIR="$logdir" HOME="$home" CENCI_SANDBOX="$cenci_sandbox" \
      bash "$BOOTSTRAP"
  else
    (
      unset CENCI_SANDBOX
      export PLUGIN_ROOT="$root" TMPDIR="$logdir" HOME="$home"
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

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
