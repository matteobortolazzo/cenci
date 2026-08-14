#!/bin/bash
# start_dind — fire-and-forget inner Docker engine startup for dind mode
# (#586, gated by entrypoint.sh on CENCI_SANDBOX_DIND=1).
#
# Creates the docker group and adds dev to it BEFORE launching dockerd, so
# the priv-drop re-exec into `sudo -u dev` (entrypoint.sh) picks up the
# updated /etc/group membership — no `sg` re-exec needed, unlike the removed
# DooD socket-group block.
#
# That runtime add covers the priv-dropped PID-1 shell ONLY — it is not what
# gets the agent onto the socket, and must never be relied on as if it were.
# dind always launches under sysbox-runc, which clones the container's rootfs,
# so /etc/group edits made in here are invisible to the Docker daemon's own
# `docker exec -u dev` user resolution; the launcher attaches every agent
# session with exactly that (assembleExecEnv,
# watch/internal/sandbox/launcher/launch.go), so the agent lands with
# groups=dev and every docker call fails with a socket permission error.
# The membership the agent actually uses is baked into the image at build time
# by fragments/docker.dockerfile (`groupadd -f docker && usermod -aG docker
# dev`), which does survive the rootfs clone. The calls below stay as the
# root-phase belt-and-braces for that image-level guarantee.
#
# dockerd itself is launched fire-and-forget: agents don't touch docker until
# well after startup, and the docker CLI/Testcontainers clients already
# retry/wait on connect, so blocking container startup on daemon readiness
# would only add latency for no benefit. The background subshell — not the
# caller — owns the `wait` on dockerd's exit, so it keeps working after
# entrypoint.sh's later `exec sudo` reparents the rest of the process tree
# (a sibling `wait $PID` after that exec could never observe the reparented
# PID; the subshell that started dockerd has no such problem). `--init` is
# already passed by the launcher, so the process tree is reaped either way.
#
# Shutdown is classified with an explicit sentinel, not exit-code
# guesswork (#630): the launcher's dind-only PID-1 keepalive
# (watch/internal/sandbox/launcher/launch.go) traps TERM/INT and touches
# .cenci-dockerd-shutdown before dockerd is force-killed by container
# teardown. If dockerd exits non-zero AND that sentinel is NOT present, the
# subshell writes a timestamped .cenci-dockerd-startup-error marker under
# /home/dev (mirrors the existing .cenci-agent-startup-error convention,
# #572: UTC ISO-8601 prefix via `date -u +%Y-%m-%dT%H:%M:%SZ`, one line, real
# diagnostic content — here the tail of dockerd's own captured stderr) so a
# genuinely broken daemon (e.g. corrupted /var/lib/docker) surfaces its real
# cause on first `docker` touch instead of a generic connection error. A
# clean exit (rc==0), one still running when reaped, or one whose exit is
# superseded by the sentinel must never leave a marker behind. This replaces
# the previous exit-code allow-list (0|129|130|131|137|143), which could not
# distinguish an intentional `docker stop` from a genuine SIGKILL/OOM — the
# exact guesswork the ticket forbids relying on.
#
# start_dind() clears (rm -f) any stale .cenci-dockerd-shutdown sentinel and
# any prior .cenci-dockerd-startup-error marker at entry — supersede-only
# clearing, mirroring entrypoint.sh's own .cenci-agent-startup-error
# supersede-clear convention, so a leftover sentinel/marker from a previous
# container lifetime never pollutes this run's own classification.
#
# start_dind() runs in entrypoint.sh's ROOT phase, before the `exec sudo -u
# dev` re-exec that sets HOME=/home/dev — root's own $HOME is /root, NOT
# /home/dev. So the marker/log/sentinel paths are hardcoded to /home/dev,
# exactly like every other root-phase marker write in entrypoint.sh (e.g. the
# .cenci-startup-failed/.cenci-boot.log/.cenci-agent-startup-error paths),
# instead of reading $HOME — using $HOME here would silently land the marker
# in the ephemeral container root filesystem instead of the persisted
# /home/dev home volume, defeating the whole point of surviving `--rm` for
# #587 to later read. CENCI_DIND_HOME_ROOT is a narrow, test-only override
# (same convention as agent_cli_root()'s CENCI_AGENT_CLI_ROOT in
# lib/agent-cli.sh) so dind.test.sh can redirect it into a scratch dir
# without touching a real /home/dev — production never sets it.

start_dind() {
    # flush_boot_log (entrypoint.sh) must run before exit here — without
    # --init as PID 1, a bare `exit` can SIGKILL the still-draining `tee`
    # before it writes this failure line to .cenci-boot.log, silently losing
    # the diagnostic. It's in scope: dind.sh is sourced into entrypoint.sh's
    # own root-phase shell, after flush_boot_log is defined there.
    groupadd -f docker \
        || { echo "dind: groupadd -f docker failed — dev will not gain docker-group membership; docker CLI calls will fail with a permission error" >&2; flush_boot_log; exit 1; }
    usermod -aG docker dev \
        || { echo "dind: usermod -aG docker dev failed — dev will not gain docker-group membership; docker CLI calls will fail with a permission error" >&2; flush_boot_log; exit 1; }

    local home="${CENCI_DIND_HOME_ROOT:-/home/dev}"
    local log="${home}/.cenci-dockerd.log"
    local marker="${home}/.cenci-dockerd-startup-error"
    local sentinel="${home}/.cenci-dockerd-shutdown"

    # Supersede-only clearing: a stale sentinel/marker from a previous
    # container lifetime must never leak forward into this run's
    # classification.
    rm -f "${sentinel}" "${marker}"

    # The Docker engine is no longer part of Dockerfile.base — it moved to the
    # config-selected fragments/docker.dockerfile (#831). So an image whose
    # .cenci/Dockerfile predates that change, or was generated without the
    # fragment, can still be launched with CENCI_SANDBOX_DIND=1 and carry no
    # dockerd at all. Detect that explicitly instead of letting the generic
    # path report it as a bare "dockerd exited with status 127": the cause
    # (image built without the fragment) and the remedy (regenerate and
    # rebuild) are both knowable here, and a 127 tail states neither.
    #
    # Deliberately NOT fatal, unlike the groupadd/usermod guards above: those
    # leave a dind container that would fail confusingly later, whereas this
    # one is fully usable for every non-Docker task. The marker is the same
    # channel the daemon-crash path uses, so it still surfaces on the first
    # `docker` touch rather than vanishing.
    if ! command -v dockerd >/dev/null 2>&1; then
        echo "dind: dockerd is not installed in this image — nested Docker is unavailable for this session. Enable the docker fragment for this repo and rebuild (see the marker at ${marker})." >&2
        printf '%s dockerd is not installed in this image, so the inner Docker engine could not be started. This image was built without sandbox/fragments/docker.dockerfile. Re-run /cenci:configure to regenerate .cenci/Dockerfile with the docker fragment (it is selected when sandbox.dind is true), then rebuild with: cenci sandbox build\n' \
            "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "${marker}" \
            || echo "dind: failed to write dockerd startup-error marker at ${marker}" >&2
        return 0
    fi

    (
        dockerd >"${log}" 2>&1
        rc=$?
        if [[ ${rc} -eq 0 || -e "${sentinel}" ]]; then
            : # clean exit, or an intentional shutdown already superseded the exit — no marker
        else
            tail_diag="$(tail -c 2000 "${log}" 2>/dev/null | tr '\n' ' ')"
            printf '%s dockerd exited with status %s: %s\n' \
                "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${rc}" "${tail_diag}" > "${marker}" \
                || echo "dind: failed to write dockerd startup-error marker at ${marker}" >&2
        fi
    ) &
}
