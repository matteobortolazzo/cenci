# Failure Atlas

Operator-facing recovery documentation for every [error code](error-codes.md)
registered in `watch/internal/errcode/errcode.go`. `error-codes.md` owns the
`CENCI-<AREA>-<SUBAREA>-<NNN>` naming convention and a short one-line meaning
per code; this page is the single home for the fuller picture — common
causes, diagnostic commands, a recovery procedure, and known
platform-specific issues — so the two never carry duplicated, driftable
recovery text.

Each code below is verified as covered by
`watch/internal/errcode/atlas_sync_test.go`, a CI test that fails when a
registered code has no entry here (or when an entry here references a code
that no longer exists in the registry). When you register a new `Code` in
`errcode.go`, add a matching entry here in the same change.

After running a recovery command, re-run `cenci diagnose <session> --verify`
where noted below to confirm the fix actually worked — it re-runs the same
read-only probe and reports `[pass]`/`[fail]` instead of the full report.

## CENCI-SANDBOX-START-001

**Meaning**: The sandboxed agent CLI path is missing or not executable.
`sandbox/entrypoint.sh` detects this before the readiness marker is ever
written and persists it to `/home/dev/.cenci-agent-startup-error`, which
`cenci open`/`cenci diagnose` surface verbatim.

**Common causes**:
- The agent-CLI host-global volume was never bootstrapped or was removed.
- The mounted agent CLI binary is not executable inside the container.

**Diagnostic commands**:
```bash
cenci diagnose <session>
docker/podman inspect the agent-CLI volume mount
```

**Recovery procedure**:
1. Run `cenci sandbox update-agent` to re-bootstrap the shared agent-CLI
   volume.
2. Relaunch the session with `cenci open <shortcut>`.
3. If the failure persists, inspect the volume mount directly (`docker
   volume inspect cenci-agent-cli-<agent>`) to confirm the binary is present
   and executable.

**Platform notes**: On Podman, rootless volume permissions can leave the
mounted binary non-executable even after a successful `update-agent`; check
the volume's owning UID matches the container's `dev` user.

## CENCI-SANDBOX-START-002

**Meaning**: The sandbox entrypoint failed during container startup, for any
reason other than the specific agent-CLI-missing case above. Surfaced from
whichever of the boot log, the generic `.cenci-startup-failed` EXIT-trap
marker, or the container's runtime logs yielded diagnostics — falling back
to a fully generic message when none did.

**Common causes**:
- An entrypoint step (credential seeding, plugin install, config migration)
  failed.
- The container exited before its readiness marker was written.

**Diagnostic commands**:
```bash
docker/podman logs <container> --tail 50
cenci diagnose <session>
```

**Recovery procedure**:
1. Read the last 50 log lines (`docker/podman logs <container> --tail 50`)
   to identify the failing entrypoint step.
2. Rebuild the image and retry: `cenci sandbox build`.
3. Relaunch with `cenci open <shortcut>`.
4. If credential seeding is the failing step, re-run `cenci sandbox
   reseed-creds` before relaunching.

**Platform notes**: On Docker Desktop (macOS/Windows), a cold VM start can
make the very first entrypoint run slow enough to look like a failure when
it is really just a timeout further down the boot sequence — check for
`CENCI-SANDBOX-START-003` instead before assuming a genuine entrypoint bug.

## CENCI-SANDBOX-START-003

**Meaning**: The sandbox container did not signal readiness within the
60-second readiness-poll budget. The container did not fail outright — it
simply never reached `/tmp/cenci-ready` in time.

Registry-only for now: registered here (message, causes, hints) and mapped
by `launcher.severityForCode`, but neither `waitUntilReady` nor `cenci
diagnose` currently detects a stuck/never-ready container and attaches this
code — wiring a detection path for either is left to a follow-up.

**Common causes**:
- Container startup is unusually slow (large plugin install, or a busy
  host).
- An earlier startup failure went undetected before the readiness poll
  budget expired.

**Diagnostic commands**:
```bash
cenci diagnose <session>
docker/podman logs <container> --tail 50
```

**Recovery procedure**:
1. Check the container's recent logs for a stall point.
2. Retry the launch — a busy host or a one-off slow plugin install often
   succeeds on a second attempt.
3. If the container is still running but never became ready, `cenci sandbox
   stop <session>` and relaunch.

**Platform notes**: First-launch plugin installs are noticeably slower on
constrained CI runners and low-resource VMs; a repeated readiness timeout
there is expected on first use and usually clears on the second launch once
image layers and plugin caches are warm.

## CENCI-SANDBOX-SESSION-001

**Meaning**: No container exists for the requested sandbox session. Attached
by `cenci diagnose <session>` when its container-existence probe cannot find
a matching container.

**Common causes**:
- The session was never launched, or was launched with a different `--name`
  or repo scope (each repo/session combination gets its own container name).
- The container already stopped and was auto-removed (`--rm`).

**Diagnostic commands**:
```bash
cenci sandbox ls
cenci diagnose <session>
```

**Recovery procedure**:
1. Run `cenci sandbox ls` to confirm the session's container truly does not
   exist (and to spot a naming/scope mismatch).
2. Relaunch the session with `cenci open <shortcut>`.
3. Re-run `cenci diagnose <session> --verify` — it re-runs the same
   container-existence probe and reports `[pass]` once the relaunch
   succeeded.

**Platform notes**: None specific to this code — container naming and `--rm`
auto-removal behave identically across Docker and Podman.

## CENCI-SANDBOX-DIND-001

**Meaning**: The nested Docker daemon (DinD) failed to start, or crashed/OOMed
after starting, without an intentional-shutdown sentinel superseding the
marker. `sandbox/lib/dind.sh` persists this to
`/home/dev/.cenci-dockerd-startup-error`. The launcher's
`warnDockerdStartupFailure` surfaces it as a non-fatal warning right before
the first agent attach (the session still attaches — only nested Docker is
unavailable), and `cenci diagnose <session>`'s "Nested Docker:" section
always reports it for dind sessions.

**Common causes**:
- The inner `dockerd` process failed to start (e.g. Sysbox is not registered
  with the container runtime, or resource limits prevent it from starting).
- The inner `dockerd` crashed or was OOM-killed sometime after starting.

**Diagnostic commands**:
```bash
cenci diagnose <session>
docker/podman logs <container> --tail 50
```

**Recovery procedure**:
1. Run `cenci diagnose <session>` to read the captured diagnostic.
2. Confirm Sysbox is registered with the runtime (`cenci doctor`) if nested
   Docker never started at all.
3. Stop and relaunch the session with `--dind` (`cenci sandbox stop
   <session>`, then `cenci open <shortcut> --dind`) once the underlying cause
   is addressed.
4. Re-run `cenci diagnose <session> --verify` — it re-reads the same marker
   and reports `[pass]` once a clean relaunch supersedes the prior failure.

**Platform notes**: DinD requires the Sysbox container runtime to be
registered with Docker; on Podman-preferred dual-runtime hosts, `cenci
doctor` still probes Docker's Sysbox registration independently since dind
sessions specifically require it. This code never appears on macOS — there
the launch degrades before any inner `dockerd` is attempted, reporting
`CENCI-SANDBOX-DIND-002` instead.

## CENCI-SANDBOX-DIND-002

**Meaning**: Nested Docker was requested for this launch — by `--dind` or by
the repo's `sandbox.dind` key in `.cenci/config.json` — but the host can
never register the `sysbox-runc` OCI runtime, so the sandbox launched
without it. Attached by the launcher's `warnDindPlatformUnsupported` warning
and reported by `cenci audit` as the `platform-unsupported` dind source.
Warning tier, not degraded: the session itself is fully functional, and
unlike `CENCI-SANDBOX-DIND-001` there is no host-side fix — this is a
platform capability, not a failed start.

**Common causes**:
- The host is macOS. Sysbox is a Linux-only runtime that must be installed on
  the machine running `dockerd`, and on macOS `dockerd` lives inside Docker
  Desktop's LinuxKit VM, which cannot be modified.

**Diagnostic commands**:
```bash
cenci audit                    # dind source: platform-unsupported
cenci open <shortcut> --dry-run # previews the degraded (non-dind) create argv
```

**Recovery procedure**:
1. Nothing is broken — the session runs, only in-container Docker is absent.
   Work that does not need Docker (Testcontainers, `docker build`/`docker
   run` in tests) proceeds normally.
2. To silence the warning for a session, launch with `cenci open <shortcut>
   --no-dind`.
3. To silence it for a repo that does not actually need nested Docker, set
   `"sandbox": {"dind": false}` in `.cenci/config.json`.
4. To actually get nested Docker on a Mac, run Docker Engine inside a Linux
   VM you control (Lima, Multipass) with `sysbox-ce` installed there, and
   point `DOCKER_HOST` at it. Cenci does not manage that VM.

**Platform notes**: Linux hosts never see this code. There an unregistered
`sysbox-runc` is a fixable setup gap, so `dindPreflight` still hard-fails
with install pointers (`sysbox-ce` via the Arch AUR, or the nestybox `.deb`
on Ubuntu) rather than degrading.

## CENCI-DAEMON-CONN-001

**Meaning**: The cenci daemon's event socket exists but nothing answers a
read-only dial. Attached by `cenci diagnose`'s daemon-reachability probe.

**Common causes**:
- The daemon process crashed or exited without cleaning up its socket file.
- Another process is holding the socket without answering connections.

**Diagnostic commands**:
```bash
cenci daemon status
cenci diagnose <session>
```

**Recovery procedure**:
1. Run `cenci daemon status` to confirm the daemon is not running.
2. Run `cenci daemon restart` (stops any stale process, then spawns a fresh
   daemon and waits for it to become reachable).
3. Re-run `cenci diagnose <session> --verify` — it re-dials the same event
   socket and reports `[pass]` once the daemon answers.

**Platform notes**: None specific to this code — the event socket is a Unix
domain socket under `$XDG_RUNTIME_DIR/cenci/`, resolved identically on every
supported Linux/macOS host.

## CENCI-DAEMON-SOCKET-001

**Meaning**: The cenci daemon's event socket does not exist at all.
Attached by `cenci diagnose`'s daemon-reachability probe when it finds no
socket file at the expected path (distinct from `CENCI-DAEMON-CONN-001`,
where the socket file exists but nothing answers).

**Common causes**:
- The daemon has never been started on this host.
- `XDG_RUNTIME_DIR` (or its fallback socket directory) was cleared, removing
  the socket.

**Diagnostic commands**:
```bash
cenci daemon status
cenci diagnose <session>
```

**Recovery procedure**:
1. Run `cenci daemon start` (or let the next hook-triggered `EnsureRunning`
   self-heal spawn it automatically).
2. Run `cenci daemon status` to confirm it is now running.
3. Re-run `cenci diagnose <session> --verify` — it re-checks for the same
   socket path and reports `[pass]` once the daemon has started.

**Platform notes**: On systems where `$XDG_RUNTIME_DIR` is unset, cenci
falls back to a per-user socket directory; if that fallback directory was
cleared by a tmp-cleaning cron job (common on some Linux distros), the
socket disappears with it even though the daemon process may still think it
is running — check `cenci daemon status` first before assuming the daemon
itself has died.
