package main_test

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox/launcher"
)

// -- fakes -------------------------------------------------------------
//
// These black-box tests exercise the real built `cenci` binary as a
// subprocess (binaryPath, built once in TestMain in main_test.go) with PATH
// overridden to a temp dir containing fake `docker`/`podman` (and, for the
// open path, `claude`) scripts. Fakes are plain POSIX `/bin/sh` scripts (not
// `#!/usr/bin/env ...`) so they resolve without depending on the overridden
// PATH containing a shell.

// writeFakeDocker writes a fake `docker` (or `podman`) to dir that appends
// each invocation's argv (space-joined) as a line to callLog, and — when
// invoked as `<name> ps ...` — answers psOutput (a tab-separated
// name/status/image row per line, ListContainers' `ps -a --format ...`
// shape): `ps -a` prints psOutput verbatim, while a plain `ps` (the
// RunningSandboxContainers(All) `--format {{.Names}}` shape `sandbox stop`
// relies on) prints only the first (name) field of each line — so a caller
// asserting on the exact stopped-container name (not just a call-log
// substring) gets a clean name, never the whole tab-joined row (#629).
func writeFakeDocker(t *testing.T, dir, name, callLog, psOutput string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + exectest.ShellQuote(callLog) + "\n" +
		"psout=" + exectest.ShellQuote(psOutput) + "\n" +
		`if [ "$1" = "ps" ]; then
  if [ "$2" = "-a" ]; then
    printf '%s' "$psout"
  else
    tab=$(printf '\t')
    IFS='
'
    for line in $psout; do
      fname=${line%%"$tab"*}
      [ -n "$fname" ] && printf '%s\n' "$fname"
    done
  fi
fi
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// readCapturedArgv reads a fake binary's capture file into a []string, one
// element per line (trailing blank line from the final \n dropped). Shared
// with doctor_update_test.go's fake cenci-installer.
func readCapturedArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joinArgv(argv []string) string {
	return strings.Join(argv, " ")
}

// -- sandbox <batch verb> ------------------------------------------------
//
// build/build-base/prune/update-agent/update-plugins run natively against docker/podman
// via internal/sandbox/launcher, so these tests assert what the fake
// *runtime* received. Both docker and podman fakes are always written so the
// podman-first runtime detection can never escape to a real runtime on
// machines (or CI runners) that have one. reap-orphans is pinned by the
// relocated bash contract suite (tests/reap-orphans.test.sh); reseed-creds
// is an alias for `cenci open --reseed-creds` and is covered in
// sandbox_subcmds_test.go.

// writeScriptedRuntime writes a fake docker/podman to dir that appends each
// invocation's argv (space-joined) as a line to callLog and answers scripted
// responses from env vars:
//
//	FAKE_INSPECT_EXIT    — image listing exit code (default 0 = image exists)
//	FAKE_IMAGE_AGENT_LIFECYCLE — shared-agent image label (default shared-v2)
//	FAKE_IMAGE_BASE_VERSION — baked cenci.base-version image label (default
//	                        set per-test by batchEnv/openTestEnv to the
//	                        fixture's current BaseTag, so pre-existing
//	                        "image current -> skip build" tests keep passing
//	                        without every caller having to know the tag)
//	FAKE_BUILD_EXIT      — `build` exit code (default 0)
//	FAKE_IMAGES          — `images ...` stdout
//	FAKE_PS              — `ps ...` stdout
//	FAKE_PS_EXIT         — `ps ...` exit code (default 0); set nonzero to
//	                        simulate a container-listing failure (e.g.
//	                        support-bundle's ListContainers call)
//	FAKE_VOLUMES         — `volume ls` stdout
//	FAKE_INFO_RUNTIMES   — `info --format {{json .Runtimes}}` stdout (default
//	                        "{}"); the dind preflight's sysbox-registration
//	                        probe reads this, e.g.
//	                        `{"sysbox-runc":{},"runc":{}}` to simulate a
//	                        registered sysbox runtime.
//	FAKE_INFO_EXIT       — `info ...` exit code (default 0); set nonzero to
//	                        simulate a `docker info` failure (daemon down),
//	                        distinct from FAKE_INFO_RUNTIMES's stdout-shape
//	                        control — note the asymmetric naming: `fv
//	                        INFO_RUNTIMES` controls stdout, `fe INFO` (→
//	                        FAKE_INFO_EXIT, not FAKE_INFO_RUNTIMES_EXIT)
//	                        controls the exit code. Must stay byte-parallel
//	                        with internal/sandbox/launcher/faketest_test.go's
//	                        writeFakeRuntime (#493 keep-in-sync note).
//	FAKE_INSPECT_LABEL   — container `inspect` stdout for label lookups
//	FAKE_INSPECT_MOUNTS  — container `inspect` stdout for warnIfUnwired's
//	                        mount probe. One "<source>::<destination>" line
//	                        per mount — the same shape as FAKE_REUSE_POSTURE's
//	                        mount lines below — so warnIfUnwired can compare a
//	                        reused container's cenci socket-mount source
//	                        against the currently-resolved host socket dir, not
//	                        just its destination (ticket #1143). Must stay
//	                        byte-parallel with
//	                        internal/sandbox/launcher/faketest_test.go's
//	                        writeFakeRuntime FAKE_INSPECT_MOUNTS doc comment
//	                        (#493 keep-in-sync note).
//	FAKE_REUSE_POSTURE   — container `inspect` stdout for the combined
//	                        reuse-posture probe (ticket #628's
//	                        inspectReusePosture: the `cenci-sand.dind` label,
//	                        HostConfig.Runtime, and mount source/destination
//	                        pairs), told apart by the `cenci-sand.dind`
//	                        format-string token. Line 1 is
//	                        "<label>|<runtime>|<dindenv 0-or-1>"; remaining
//	                        lines are "<source>::<destination>" per mount,
//	                        followed by one trailing blank line — real
//	                        `docker inspect --format` appends its own
//	                        trailing newline on top of the template's last
//	                        mount's own "\n", and parseReusePosture trims
//	                        exactly that trailing blank line rather than
//	                        rejecting it (ticket #684); omitting it here would
//	                        let these fakes drift from the real runtime shape
//	                        again. Defaults to an empty label, non-sysbox
//	                        runtime, no dind env, and a workspace-only mount
//	                        (derives dindOff, no host socket) so existing
//	                        reuse tests that don't care about posture keep
//	                        passing; watch/docs/test-strategy.md #493
//	                        keep-in-sync note applies — this must stay
//	                        byte-parallel with
//	                        internal/sandbox/launcher/faketest_test.go's
//	                        writeFakeRuntime.
//	FAKE_INSPECT_STATE   — container startup state (default "running 0")
//	FAKE_CONTAINER_INSPECT_EXIT — container `inspect` exit code, for all three
//	                        format-string lookups (State.Status, .RW mounts,
//	                        Mounts) — default 0; set nonzero to simulate
//	                        inspect erroring (e.g. the container was already
//	                        removed by `--rm`)
//	FAKE_READY_EXIT      — readiness probe exit code (default 0)
//	FAKE_STARTUP_ERROR   — persistent agent-CLI-missing marker text, read
//	                        from /home/dev/.cenci-agent-startup-error
//	FAKE_BOOT_LOG        — entrypoint boot-log content, read from
//	                        /home/dev/.cenci-boot.log
//	FAKE_STARTUP_MARKER  — generic entrypoint-failure trap marker text, read
//	                        from /home/dev/.cenci-startup-failed
//	FAKE_AGENT_CHECK_EXIT — the `agent-cli.sh status <agent>` populated-check
//	                        probe's exit code (default 0 = the shared volume
//	                        is populated)
//	FAKE_AGENT_STATUS    — the `agent-cli.sh status <agent>` probe's stdout:
//	                        the five populated/version/pin/last_success/
//	                        last_attempt key=value lines (ticket #710's
//	                        agent-cli.sh swap). Defaults to a freshly
//	                        succeeded, unpinned status
//	                        (last_success=$(date +%s), evaluated at fake-
//	                        invocation time, not fixture-write time) so
//	                        pre-existing open tests that don't care about TTL
//	                        staleness never trigger the launch-time refresh.
//	                        Must stay byte-parallel with engine_test.go's
//	                        volumeCheckEngine/volumeCheckEngineStatus and
//	                        dual_runtime_test.go's writeAgentRuntimeStub
//	                        (watch/docs/test-strategy.md #493).
//	FAKE_AGENT_UNPIN_EXIT — 'agent-cli.sh unpin <agent>' exit code (default 0);
//	                        `update-agent --unpin`'s unpin-then-update
//	                        sequence for a given owner runs the plain
//	                        'agent-cli.sh update' only when the unpin call
//	                        itself succeeds (#709).
//	FAKE_ATTACH_EXIT     — exit code of the final `exec -it ...` attach
//	FAKE_PLUGIN_MANIFEST — `cenci diagnose`'s best-effort plugin-manifest
//	                        read, from either agent's marketplace.json
//	                        (/home/dev/.claude/plugins/marketplaces/cenci/
//	                        .claude-plugin/marketplace.json or the Codex
//	                        equivalent under /home/dev/.codex/...) via the
//	                        same short-lived `run --entrypoint /bin/cat`
//	                        home-volume read pattern as the three
//	                        startupFailureDetail vars above; unset/empty
//	                        simulates a read failure (diagnose falls back to
//	                        "unknown")
//	FAKE_DOCKERD_MARKER  — #630's persistent dockerd-startup-failure marker
//	                        text, read from /home/dev/.cenci-dockerd-startup-
//	                        error via the same short-lived `run --entrypoint
//	                        /bin/cat` home-volume read pattern; consumed by
//	                        both the launcher's before-attach warning
//	                        (warnDockerdStartupFailure) and `cenci diagnose`'s
//	                        "Nested Docker:" section. Unset/empty simulates no
//	                        recorded failure. FAKE_DOCKERD_MARKER_EXIT
//	                        (default 0) is that same read's exit code — set
//	                        it to 125/126/127 (#1163) to script
//	                        readHomeVolumeFile's read-failed classification;
//	                        any other non-zero exit (e.g. 1) scripts the
//	                        legitimately-absent classification instead.
//	FAKE_EVENTS_MARKER   — content returned by the short-lived
//	                        `run --entrypoint /bin/cat ... .cenci-events-
//	                        undelivered` home-volume read (#1122's
//	                        dropped-event marker); unset/empty simulates "no
//	                        failure recorded". FAKE_EVENTS_MARKER_EXIT
//	                        (default 0) is that same read's exit code, same
//	                        125/126/127 scripting convention as
//	                        FAKE_DOCKERD_MARKER_EXIT above (#1163).
//	FAKE_IMAGE_ID        — `image inspect --format '{{.Id}}' <image>` stdout
//	                        (ticket #947's printStaleContainerNotice: the
//	                        freshly built image's ID), told apart by the
//	                        distinctive `{{.Id}}` format-string token, checked
//	                        before the plain agent-cli/base-version label
//	                        answer above (which would otherwise answer any
//	                        format string, including this one). Defaults to
//	                        "sha256:fresh".
//	FAKE_IMAGE_ID_EXIT   — that probe's exit code (default 0); nonzero
//	                        simulates the image-ID probe itself failing.
//	FAKE_CONTAINER_IMAGES — space-separated `name=<ref>|<id>` pairs answering
//	                        the combined per-container probe `inspect
//	                        --format '{{.Config.Image}}|{{.Image}}' <name>`
//	                        (ticket #947), told apart by the distinctive
//	                        `{{.Image}}` format-string token. The fake looks
//	                        up the requested container's name and emits
//	                        `${pair#*=}` verbatim as the probe's stdout; a
//	                        container absent from the list defaults to
//	                        "cenci-sandbox:latest|<FAKE_IMAGE_ID default>".
//	                        Must stay byte-parallel with
//	                        internal/sandbox/launcher/engine_test.go's
//	                        buildEngine and faketest_test.go's
//	                        writeFakeRuntime (#493 keep-in-sync note).
//	FAKE_INSPECT_IMAGE_EXIT — the per-container combined probe's exit code
//	                        (default 0); nonzero simulates that probe
//	                        failing.
//	FAKE_INFO_MEMTOTAL   — (#1216) `info --format ...` stdout when the format
//	                        names MemTotal ({{.MemTotal}} on docker,
//	                        {{.Host.MemTotal}} on podman): the runtime's total
//	                        host memory in bytes; defaults to "17179869184"
//	                        (16 GiB, so the default memory limit resolves to
//	                        50% = 8192 MiB). Told apart from the sysbox-
//	                        registration probe by the `MemTotal` format-string
//	                        token, checked first so FAKE_INFO_RUNTIMES never
//	                        shadows it. FAKE_INFO_MEMTOTAL_EXIT (default 0) is
//	                        that probe's own exit code — deliberately NOT
//	                        FAKE_INFO_EXIT, so a test can fail just the memory
//	                        query without failing dind's sysbox probe. Must
//	                        stay byte-parallel with
//	                        internal/sandbox/launcher/faketest_test.go's
//	                        writeFakeRuntime (#493 keep-in-sync note).
//	FAKE_CONTAINER_LIMITS — (#1216) container `inspect` stdout for the
//	                        container-limits probe (attach-path drift check and
//	                        Diagnose's limits line), told apart by the
//	                        `.HostConfig.PidsLimit` format-string token
//	                        (placed before every broader inspect arm). The value
//	                        is "<memory bytes>|<memory+swap bytes>|<pids limit>"
//	                        verbatim — the raw .HostConfig.Memory,
//	                        .HostConfig.MemorySwap and .HostConfig.PidsLimit
//	                        values, which for an unlimited container are "0"
//	                        (or "-1" on podman) and may be "<nil>" for pids.
//	                        Defaults to "8589934592|8589934592|4096" (an 8 GiB,
//	                        no-swap, 4096-pid container: what a default launch
//	                        creates against the FAKE_INFO_MEMTOTAL default, so
//	                        pre-existing attach tests stay drift-free). Its
//	                        exit code is the SEPARATE FAKE_CONTAINER_LIMITS_EXIT
//	                        (default 0), not FAKE_CONTAINER_INSPECT_EXIT, so a
//	                        test can fail just this best-effort probe. Must stay
//	                        byte-parallel with the launcher fake (#493).
//	FAKE_MEMORY_EVENTS   — (#1216) `exec <name> /bin/cat
//	                        /sys/fs/cgroup/memory.events` stdout (Diagnose's
//	                        cgroup OOM-kill count), told apart by the
//	                        `memory.events` token alone (not the cat path, so
//	                        the fake survives a change of cat binary). Defaults to
//	                        "oom 0\noom_kill 0\n". FAKE_MEMORY_EVENTS_EXIT
//	                        (default 0) is that exec's exit code. Must stay
//	                        byte-parallel with the launcher fake (#493).
//	FAKE_CREATE_STDERR   — (#1216) text written to stderr by the container
//	                        CREATE `run --name ...` call (not by any other
//	                        `run`); FAKE_CREATE_EXIT (default 0) is that
//	                        call's exit code. Together they script a runtime
//	                        rejecting the create (e.g. an undelegated memory
//	                        cgroup controller) with its real stderr text.
//	FAKE_CONTAINER_ENV   — (#1002) the attach-path CENCI_SANDBOX_PLUGINS
//	                        drift probe's `inspect --format
//	                        '{{range .Config.Env}}{{if eq (index (split . "=")
//	                        0) "CENCI_SANDBOX_PLUGINS"}}{{.}}{{end}}{{end}}'`
//	                        stdout — the template filters server-side, so the
//	                        real probe's stdout is either the single raw
//	                        "CENCI_SANDBOX_PLUGINS=VALUE" line or empty (never
//	                        the container's whole env — #1002 security
//	                        review), told apart by the distinctive
//	                        `.Config.Env` format-string token. Defaults to
//	                        empty (no CENCI_SANDBOX_PLUGINS line at all — the
//	                        legacy-container "unset" signal, compared against
//	                        the default pair, so pre-existing attach tests
//	                        that never set this stay drift-free). Its exit
//	                        code is the SEPARATE FAKE_CONTAINER_ENV_EXIT (not
//	                        FAKE_CONTAINER_INSPECT_EXIT, which every other
//	                        inspect probe above shares) so a test can fail
//	                        just this best-effort probe without also failing
//	                        the compatibility/reuse-posture/readiness probes
//	                        the attach path depends on — proving the drift
//	                        probe never blocks the attach. This branch is
//	                        placed AFTER the existing `*'cenci-sand.dind'*`
//	                        branch on purpose: inspectReusePosture's own
//	                        format string also contains the literal
//	                        `.Config.Env` substring, so matching it first
//	                        would silently break every reuse-posture test
//	                        (plan Risks: fake-runtime case-order shadowing).
//
// The open path drives the extra verbs: `rm` (exit 0), `run` (prints a
// container id), container `inspect` (label vs mounts told apart by the
// format string), non-interactive `exec` (readiness probe, exit 0), and the
// final `exec -it` attach — which the binary syscall.Execs, so the fake's
// exit code IS the binary's exit code. `run --entrypoint /bin/cat ... <path>`
// (startupFailureDetail's short-lived home-volume reads, and diagnose's
// plugin-manifest read) is told apart by the requested path, one of the four
// FAKE_* vars above.
//
// Every FAKE_<VERB>[_EXIT] var above also has a FAKE_<VERB>_DOCKER/
// FAKE_<VERB>_PODMAN (or FAKE_<VERB>_EXIT_DOCKER/FAKE_<VERB>_EXIT_PODMAN)
// override, resolved from the invoking binary's own name and falling back to
// the plain var — see internal/sandbox/launcher/faketest_test.go's
// writeFakeRuntime doc comment for the full rationale (ticket #629's
// dual-runtime test-fake keying: dual-runtime tests put both a docker and a
// podman fake on PATH at once, one shared process environment, so a single
// FAKE_PS could never script them to answer differently in the same test).
// This fv/fe/fset trio must stay byte-parallel with that one
// (watch/docs/test-strategy.md's #493 keep-in-sync rule).
func writeScriptedRuntime(t *testing.T, dir, name, callLog string) {
	t.Helper()
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
rtname=${0##*/}
rt=""
case "$rtname" in
docker) rt=DOCKER ;;
podman) rt=PODMAN ;;
esac
fv() {
  n=$1; d=$2
  if [ -n "$rt" ]; then
    v="FAKE_${n}_${rt}"
    eval "s=\${${v}+x}"
    if [ "$s" = x ]; then eval "printf '%s' \"\${${v}}\""; return; fi
  fi
  eval "s=\${FAKE_${n}+x}"
  if [ "$s" = x ]; then eval "printf '%s' \"\${FAKE_${n}}\""; else printf '%s' "$d"; fi
}
fe() {
  n=$1
  if [ -n "$rt" ]; then
    v="FAKE_${n}_EXIT_${rt}"
    eval "s=\${${v}+x}"
    if [ "$s" = x ]; then eval "printf '%s' \"\${${v}}\""; return; fi
  fi
  eval "s=\${FAKE_${n}_EXIT+x}"
  if [ "$s" = x ]; then eval "printf '%s' \"\${FAKE_${n}_EXIT}\""; else printf '%s' 0; fi
}
fset() {
  n=$1
  if [ -n "$rt" ]; then
    v="FAKE_${n}_${rt}"
    eval "s=\${${v}+x}"
    if [ "$s" = x ]; then printf x; return; fi
  fi
  eval "s=\${FAKE_${n}+x}"
  [ "$s" = x ] && printf x
}
# fvb is fv's %b-format counterpart (interprets backslash escapes, e.g. a
# literal "\n" default). fv/fvb are called directly as statements (never
# wrapped in "$(...)") wherever the resolved value is the entire printed
# output: command substitution unconditionally strips every trailing
# newline, which would silently corrupt a verbatim multi-line value like
# FAKE_LOGS (must stay byte-parallel with faketest_test.go's writeFakeRuntime
# fv/fe/fvb — #629's dual-runtime keying commit).
fvb() {
  n=$1; d=$2
  if [ -n "$rt" ]; then
    v="FAKE_${n}_${rt}"
    eval "s=\${${v}+x}"
    if [ "$s" = x ]; then eval "printf '%b' \"\${${v}}\""; return; fi
  fi
  eval "s=\${FAKE_${n}+x}"
  if [ "$s" = x ]; then eval "printf '%b' \"\${FAKE_${n}}\""; else printf '%b' "$d"; fi
}
case "$1" in
image)
  if [ "$2" = inspect ]; then
    case "$*" in
    *'{{.Id}}'*) fv IMAGE_ID "sha256:fresh"; exit "$(fe IMAGE_ID)" ;;
    esac
    printf '%s|%s\n' "$(fv IMAGE_AGENT_LIFECYCLE "shared-v2")" "$(fv IMAGE_BASE_VERSION "")"
    exit "$(fe IMAGE_INSPECT)"
  fi
  ;;
build) exit "$(fe BUILD)" ;;
images)
  if [ -n "$(fset IMAGES)" ]; then
    fv IMAGES ""
  else
    for last do :; done
    printf '%s\n' "${last}"
  fi
  exit "$(fe INSPECT)"
  ;;
ps)
  case "$*" in
  *'{{.Status}}'*)
    # ListContainers/ListAllContainers/RuntimesWithContainer's tri-field
    # "ps -a --format {{.Names}}\t{{.Status}}\t{{.Image}}" shape (#629):
    # every other ps invocation in this codebase (RunningSandboxContainers,
    # containerRunning, prune's name-only "ps -a --format {{.Names}}") wants
    # bare names, so FAKE_PS is conventionally scripted as one plain name
    # per line; synthesize a status/image suffix per name here rather than
    # requiring every existing FAKE_PS value to carry tabs, while still
    # passing an already-tab-shaped FAKE_PS line through verbatim.
    tab=$(printf '\t')
    IFS='
'
    for n in $(fv PS ""); do
      [ -n "$n" ] || continue
      case "$n" in
      *"$tab"*) printf '%s\n' "$n" ;;
      *) printf '%s\tUp 1 hour\tcenci-sandbox:latest\n' "$n" ;;
      esac
    done
    ;;
  *) fv PS "" ;;
  esac
  exit "$(fe PS)"
  ;;
volume)
  case "$2" in
  ls) fv VOLUMES ""; exit "$(fe VOLUME_LS)" ;;
  esac
  ;;
info)
  case "$*" in
  *MemTotal*) fv INFO_MEMTOTAL "17179869184"; printf '\n'; exit "$(fe INFO_MEMTOTAL)" ;;
  esac
  fv INFO_RUNTIMES "{}"; exit "$(fe INFO)"
  ;;
rm) exit 0 ;;
run) case "$*" in
  *'/bin/cat'*)
    case "$*" in
    *'.cenci-agent-startup-error'*) fv STARTUP_ERROR ""; exit "$(fe STARTUP_ERROR)" ;;
    *'.cenci-boot.log'*) fv BOOT_LOG ""; exit "$(fe BOOT_LOG)" ;;
    *'.cenci-startup-failed'*) fv STARTUP_MARKER ""; exit "$(fe STARTUP_MARKER)" ;;
    *'.cenci-dockerd-startup-error'*) fv DOCKERD_MARKER ""; exit "$(fe DOCKERD_MARKER)" ;;
    *'.cenci-events-undelivered'*) fv EVENTS_MARKER ""; exit "$(fe EVENTS_MARKER)" ;;
    *'marketplace.json'*) fv PLUGIN_MANIFEST ""; exit "$(fe PLUGIN_MANIFEST)" ;;
    esac
    ;;
  *'agent-cli.sh unpin'*) exit "$(fe AGENT_UNPIN)" ;;
  *'agent-cli.sh update'*) rs="$(fv RUN_STDERR "")"; [ -z "$rs" ] || printf '%s\n' "$rs" >&2; exit "$(fe RUN)" ;;
  *'agent-cli.sh status'*)
    fvb AGENT_STATUS "populated=yes\nversion=1.2.3\npin=\nlast_success=$(date +%s)\nlast_attempt=\n"
    exit "$(fe AGENT_CHECK)"
    ;;
  esac
  if [ "$2" = "--name" ]; then
    cs="$(fv CREATE_STDERR "")"; [ -z "$cs" ] || printf '%s\n' "$cs" >&2
    ce="$(fe CREATE)"; [ "$ce" = 0 ] || exit "$ce"
  fi
  printf '%s\n' fake-container-id ;;
inspect)
  case "$*" in
  *'.HostConfig.PidsLimit'*) fv CONTAINER_LIMITS "8589934592|8589934592|4096"; printf '\n'; exit "$(fe CONTAINER_LIMITS)" ;;
  *'{{.Image}}'*)
    name="$4"
    result=""
    found=0
    for pair in $FAKE_CONTAINER_IMAGES; do
      case "$pair" in
      "$name="*)
        result="${pair#*=}"
        found=1
        ;;
      esac
    done
    if [ "$found" = 0 ]; then
      result="cenci-sandbox:latest|$(fv IMAGE_ID "sha256:fresh")"
    fi
    printf '%s\n' "$result"
    exit "$(fe INSPECT_IMAGE)"
    ;;
  *'cenci-sand.dind'*) fvb REUSE_POSTURE "|runc|0\nworkspace-vol::/workspace\n\n"; exit "$(fe CONTAINER_INSPECT)" ;;
  *'.Config.Env'*) fvb CONTAINER_ENV ""; exit "$(fe CONTAINER_ENV)" ;;
  *State.Status*) printf '%s\n' "$(fv INSPECT_STATE "running 0")"; exit "$(fe CONTAINER_INSPECT)" ;;
  *Labels*) printf '%s\n' "$(fv INSPECT_LABEL "")" ;;
  *'.RW'*) fvb AGENT_MOUNTS "cenci-agent-cli-claude|/opt/cenci-agent|false\n"; exit "$(fe CONTAINER_INSPECT)" ;;
  *Mounts*) fvb INSPECT_MOUNTS ""; exit "$(fe CONTAINER_INSPECT)" ;;
  esac
  ;;
logs) fv LOGS "" ;;
exec) if [ "$2" = "-it" ]; then exit "$(fe ATTACH)"; fi; case "$*" in *'/tmp/cenci-ready'*) exit "$(fe READY)" ;; *'memory.events'*) fvb MEMORY_EVENTS "oom 0\noom_kill 0\n"; exit "$(fe MEMORY_EVENTS)" ;; esac; exit 0 ;;
esac
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, name), body)
}

// fakeTmuxLogPath is where writeFakeTmux's fake logs its invocations. It is a
// file of its own (not the runtime call log): the many exact-call-log
// assertions in this suite must not see tmux lines whenever a test's ambient
// $TMUX/$TMUX_PANE makes the launcher tag its pane (#1252).
func fakeTmuxLogPath(dir string) string {
	return filepath.Join(dir, "tmux-calls.txt")
}

// fakeTmuxFailMarkerPath is the file whose existence makes writeFakeTmux's
// fake fail every call.
func fakeTmuxFailMarkerPath(dir string) string {
	return filepath.Join(dir, "tmux-fail")
}

// writeFakeTmux writes a fake `tmux` to dir so the real /usr/bin/tmux (which
// sits on the same `PATH=<dir>:/usr/bin:/bin` the open tests use) can never
// tag a developer's real pane when the launcher records its launch ID
// (#1252, watch/docs/test-isolation.md). It appends each invocation's argv
// (space-joined) to fakeTmuxLogPath(dir) and exits 0; the presence of
// fakeTmuxFailMarkerPath(dir) makes every call fail, to exercise the launcher's
// "no launch ID, warn" fallback. The switch is a file, not an env var: a
// socket-carrying tmux call runs with only {PATH, HOME, TERM, LANG}, so a
// test-only variable never reaches the fake.
func writeFakeTmux(t *testing.T, dir string) {
	t.Helper()
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(fakeTmuxLogPath(dir)) + `
if [ -e ` + exectest.ShellQuote(fakeTmuxFailMarkerPath(dir)) + ` ]; then
  echo "fake tmux: set-option refused" >&2
  exit 1
fi
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), body)
}

// writeScriptedRuntimes writes the same scripted fake as both docker and
// podman, plus a fake tmux (writeFakeTmux), and returns the shared call log
// path.
func writeScriptedRuntimes(t *testing.T, dir string) string {
	t.Helper()
	callLog := filepath.Join(dir, "calls.txt")
	writeScriptedRuntime(t, dir, "docker", callLog)
	writeScriptedRuntime(t, dir, "podman", callLog)
	writeFakeTmux(t, dir)
	return callLog
}

// writeScriptedRuntimePair writes both a docker and a podman fake to dir,
// each with its OWN call log — unlike writeScriptedRuntimes' single shared
// log (fine when only one of the two ever actually runs, the pre-#629
// single-preferred-runtime world), a dual-runtime test needs to tell which
// binary actually ran a given argv apart, since the sandbox commands under
// test now genuinely invoke both docker and podman in the same run (#629).
func writeScriptedRuntimePair(t *testing.T, dir string) (dockerLog, podmanLog string) {
	t.Helper()
	dockerLog = filepath.Join(dir, "docker-calls.txt")
	podmanLog = filepath.Join(dir, "podman-calls.txt")
	writeScriptedRuntime(t, dir, "docker", dockerLog)
	writeScriptedRuntime(t, dir, "podman", podmanLog)
	writeFakeTmux(t, dir)
	return dockerLog, podmanLog
}

// writeDockerOnlyRuntime writes only a fake docker (no podman) to dir. Dind
// mode requires Docker as the outer runtime, and podman-first detection
// would otherwise win were both fakes present, so dind happy-path tests use
// this instead of writeScriptedRuntimes.
func writeDockerOnlyRuntime(t *testing.T, dir string) string {
	t.Helper()
	callLog := filepath.Join(dir, "calls.txt")
	writeScriptedRuntime(t, dir, "docker", callLog)
	writeFakeTmux(t, dir)
	return callLog
}

// writeAssetFixture creates a minimal sandbox asset dir (Dockerfile.base,
// entrypoint.sh, lib/) for CENCI_SANDBOX_ASSETS.
func writeAssetFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.base"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile.base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM cenci-sandbox-base\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entrypoint.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint.sh: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatalf("mkdir lib: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "seed.sh"), []byte("# lib\n"), 0o644); err != nil {
		t.Fatalf("write lib/seed.sh: %v", err)
	}
	return dir
}

// batchEnv is the black-box environment for native batch-verb runs: fake
// runtimes first on PATH (with git/sh still resolvable from the system
// dirs), an isolated HOME, and the asset fixture pinned via
// CENCI_SANDBOX_ASSETS. os/exec keeps the LAST duplicate env key, so these
// appends override the inherited values. FAKE_IMAGE_BASE_VERSION defaults to
// the fixture's own current BaseTag so pre-existing "image current -> skip
// build" tests keep passing now that the fake's `image inspect` branch
// carries a base-version field too; tests that want to exercise base-drift
// staleness override it with their own append.
func batchEnv(t *testing.T, fakeDir, assets string) []string {
	t.Helper()
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	return append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+t.TempDir(),
		"CENCI_SANDBOX_ASSETS="+assets,
		"FAKE_VOLUMES=cenci-agent-cli-claude\\ncenci-agent-cli-codex\\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
	)
}

// callLogLines reads the shared runtime call log (missing file = no calls).
func callLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func findLineWithPrefix(lines []string, prefix string) (string, bool) {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

func anyLineContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// stageCodexAuth plants the host ~/.codex/auth.json codex refuses to launch
// without, inside the fixture home returned by openTestEnv.
func stageCodexAuth(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// openTestEnv builds the black-box environment for native `cenci open` runs:
// the scripted runtimes on PATH, an isolated HOME, the asset fixture,
// deterministic TERM/TMUX_PANE, scrubbed optional env passthroughs, and a live
// events socket under a private 0700 CENCI_SOCKET_DIR — pre-created here so
// the subprocess wires cenci without ever spawning a real daemon.
// CENCI_SOCKET_DIR's tier-1 override is verbatim (no appended "cenci/"
// segment, #1142), so socketDir (a fresh t.TempDir(), chmod'd to 0700) IS the
// resolved socket dir directly. No host `claude` is installed: agents live in
// the container's persistent home, so the launcher never resolves an agent
// binary from the host.
func openTestEnv(t *testing.T, fakeDir, assets string) (env []string, home, socketDir string) {
	t.Helper()
	// Fail safe (#1252): TMUX/TMUX_PANE below make the launcher tag its pane
	// through `tmux` on PATH, and PATH keeps /usr/bin, so a fakeDir built by
	// hand without a fake tmux would reach the real tmux server at the fixed
	// TMUX socket. Install the logging fake when the test did not.
	if _, statErr := os.Stat(filepath.Join(fakeDir, "tmux")); errors.Is(statErr, fs.ErrNotExist) {
		writeFakeTmux(t, fakeDir)
	}
	home = t.TempDir()
	socketDir = t.TempDir()
	if err := os.Chmod(socketDir, 0700); err != nil {
		t.Fatalf("chmod socketDir: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(socketDir, "cenci-events.sock"))
	if err != nil {
		t.Fatalf("listen events socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	tag, err := launcher.BaseTag(assets)
	if err != nil {
		t.Fatalf("BaseTag: %v", err)
	}
	// os/exec keeps the LAST duplicate env key, so these appends override the
	// inherited values; the empty assignments scrub optional passthroughs
	// (and a possible ambient CENCI_SANDBOX) for deterministic argv.
	// FAKE_IMAGE_BASE_VERSION defaults to the fixture's own current BaseTag so
	// pre-existing "image current -> skip build" tests keep passing; tests
	// that want to exercise base-drift staleness override it with their own
	// append (see batchEnv, whose FAKE_IMAGE_BASE_VERSION convention this
	// mirrors).
	env = append(os.Environ(),
		"PATH="+fakeDir+":/usr/bin:/bin",
		"HOME="+home,
		"CENCI_SANDBOX_ASSETS="+assets,
		"CENCI_SOCKET_DIR="+socketDir,
		"TERM=xterm-256color",
		"TMUX_PANE=%7",
		// CENCI_TMUX_SOCKET (#1007) is derived from the first comma-separated
		// field of $TMUX; set explicitly here (rather than left ambient) so
		// the derived value is deterministic across test environments,
		// including ones not run inside tmux at all.
		"TMUX=/tmp/tmux-1000/cenci,12345,0",
		"COLORTERM=", "CONTEXT7_API_KEY=", "OPENAI_API_KEY=", "CENCI_SANDBOX=",
		"ANTHROPIC_API_KEY=",
		// #1087: the launcher resolves planning.attended from the fleet
		// config, whose path comes from XDG_CONFIG_HOME (falling back to
		// $HOME/.config). Pin it inside the fixture home so these black-box
		// runs can never read the developer's or CI runner's REAL fleet
		// config — an ambient `planning.attended: true` would otherwise flip
		// the forwarded CENCI_ATTENDED value and make the assertions below
		// machine-dependent. CENCI_ATTENDED itself is scrubbed to the empty
		// value for the same reason -- an ambient pin would override the
		// fixture config entirely. Empty is deliberately NOT a pin
		// (resolveAttendedFlag's "set and non-empty" gate), so the scrub
		// leaves the fixture home's own fleet config in charge: absent by
		// default, and whatever a test writes there when it exercises the
		// attended path.
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"CENCI_ATTENDED=",
		"FAKE_VOLUMES=cenci-agent-cli-claude\ncenci-agent-cli-codex\n",
		"FAKE_IMAGE_BASE_VERSION="+tag,
	)
	return env, home, socketDir
}

// attachLine returns the final `exec -it ...` attach argv line from the call
// log (the line the binary syscall.Exec'd the fake runtime with).
func attachLine(t *testing.T, lines []string) string {
	t.Helper()
	line, ok := findLineWithPrefix(lines, "exec -it ")
	if !ok {
		t.Fatalf("expected an interactive attach exec, got calls:\n%s", strings.Join(lines, "\n"))
	}
	return line
}

// dindRepoEnv builds a git-initialized repo (dind is only ever on in repo
// scope) with an optional `.cenci/config.json` `sandbox.dind` value, and
// returns the repo's resolved root and slug.
func dindRepoEnv(t *testing.T, dindConfig bool) (repoRoot, slug string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if dindConfig {
		if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
			t.Fatalf("mkdir .cenci: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".cenci", "config.json"), []byte(`{"sandbox":{"dind":true}}`), 0o644); err != nil {
			t.Fatalf("write .cenci/config.json: %v", err)
		}
	}
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}
	return repoRoot, launcher.Slugify(filepath.Base(repoRoot))
}

// malformedDindRepoEnv builds a git-initialized repo with an unparsable
// `.cenci/config.json` (#632): RepoDindConfig must hard-fail on this input
// rather than silently resolving dind off, since a corrupt config must not
// silently change launch posture.
func malformedDindRepoEnv(t *testing.T) (repoRoot string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cenci", "config.json"), []byte(`{not valid json`), 0o644); err != nil {
		t.Fatalf("write .cenci/config.json: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}
	return resolved
}

// buildArgv0Alias copies the built cenci binary to <dir>/<name> so
// filepath.Base(os.Args[0]) == name inside the copy ("cn" routes to open,
// "cenci-sand" hits the tombstone).
func buildArgv0Alias(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	aliasPath := filepath.Join(dir, name)
	if err := os.WriteFile(aliasPath, data, 0o755); err != nil {
		t.Fatalf("write %s alias: %v", name, err)
	}
	return aliasPath
}

// writeFleetConfigFixture writes body to the fleet config path openTestEnv's
// pinned XDG_CONFIG_HOME resolves to, so a black-box run reads it exactly the
// way the launcher resolves the real one.
func writeFleetConfigFixture(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "cenci")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir fleet config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write fleet config: %v", err)
	}
}
