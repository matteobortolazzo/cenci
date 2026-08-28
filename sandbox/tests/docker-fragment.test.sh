#!/usr/bin/env bash
# Guards the placement of the Docker engine in the image layout (#831).
#
# The Docker stack (docker-ce-cli + docker-ce + containerd.io) is opt-in
# twice over — a repo enables it via `sandbox.dind` in .cenci/config.json (or
# `--dind`), and the host additionally needs sysbox-runc registered with
# Docker — so it must not live in the stack-agnostic base image every derived
# image inherits ("Keep the image minimal", sandbox/AGENTS.md). It belongs in
# fragments/docker.dockerfile, config-selected by /cenci:configure exactly
# like azure, plus a verbatim block in the monolith so `cenci open --dind`
# keeps working in a git repo that has no .cenci/Dockerfile.
#
# The byte-identity of the fragment against the monolith block is owned by
# fragments-drift.test.sh; this suite owns placement — that the base carries
# no Docker packages and the fragment carries all of them.
#
# Runs on the host — no Docker required, plain file reads.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

BASE_DOCKERFILE="${SANDBOX_DIR}/Dockerfile.base"
FRAGMENT="${SANDBOX_DIR}/fragments/docker.dockerfile"

echo "docker-fragment.test.sh"

echo "case: sandbox/fragments/docker.dockerfile exists"
if [[ -f "${FRAGMENT}" ]]; then
    pass
else
    fail "expected the Docker engine fragment at ${FRAGMENT}"
    print_summary
    exit 1
fi

if ! FRAGMENT_CONTENT="$(cat "${FRAGMENT}")"; then
    fail "could not read ${FRAGMENT}"
    print_summary
    exit 1
fi
if ! BASE_CONTENT="$(cat "${BASE_DOCKERFILE}")"; then
    fail "could not read ${BASE_DOCKERFILE}"
    print_summary
    exit 1
fi

# ── The fragment carries the whole Docker stack ──────────────────────────
for pkg in docker-ce-cli docker-ce containerd.io; do
    echo "case: the docker fragment installs ${pkg}"
    if [[ "${FRAGMENT_CONTENT}" == *"${pkg}"* ]]; then
        pass
    else
        fail "${FRAGMENT} does not install ${pkg} — a dind image without it cannot run an inner daemon"
    fi
done

echo "case: the docker fragment provisions the Docker apt repository and signing key"
if [[ "${FRAGMENT_CONTENT}" == *"download.docker.com"* && "${FRAGMENT_CONTENT}" == *"/etc/apt/keyrings/docker.asc"* ]]; then
    pass
else
    fail "${FRAGMENT} is missing the download.docker.com apt source or its signed-by keyring"
fi

# ── dev's docker-group membership is baked, not added at runtime ─────────
# Under sysbox-runc — the only runtime dind ever launches with — the
# container's rootfs is cloned, so /etc/group edits made *inside* the
# container are invisible to the Docker daemon's `docker exec -u dev` user
# resolution. The launcher attaches every agent session with exactly that
# (assembleExecEnv, watch/internal/sandbox/launcher/launch.go), so the
# runtime `usermod -aG docker dev` in lib/dind.sh reaches PID 1's own
# priv-dropped shell but never the agent: the agent lands with groups=dev
# only and every `docker` call fails with "permission denied while trying to
# connect to the docker API at unix:///var/run/docker.sock". Image-baked
# group membership does survive the clone, so the membership has to be
# established here at build time.
echo "case: the docker fragment adds dev to the docker group at build time"
if [[ "${FRAGMENT_CONTENT}" == *"usermod -aG docker dev"* ]]; then
    pass
else
    fail "${FRAGMENT} does not run 'usermod -aG docker dev' — under sysbox-runc a runtime-only group add is invisible to 'docker exec -u dev', so the agent cannot reach the inner daemon's socket"
fi

echo "case: the docker fragment creates the docker group itself rather than relying on the docker-ce postinst"
if [[ "${FRAGMENT_CONTENT}" == *"groupadd -f docker"* ]]; then
    pass
else
    fail "${FRAGMENT} does not run 'groupadd -f docker' before adding dev to it — the membership must not depend on the docker-ce package's postinst having created the group"
fi

echo "case: the docker fragment is USER root / USER dev wrapped like every other fragment"
if [[ "${FRAGMENT_CONTENT}" == "USER root"* && "${FRAGMENT_CONTENT}" == *"USER dev" ]]; then
    pass
else
    fail "${FRAGMENT} must start with 'USER root' and end with 'USER dev' so it composes with the other fragments"
fi

# ── The base image carries none of it ────────────────────────────────────
# The regression this suite exists for: the engine was in Dockerfile.base
# from #586 until #831, so every user paid for it whether or not dind was
# ever enabled.
for pkg in docker-ce-cli docker-ce containerd.io; do
    echo "case: Dockerfile.base does not install ${pkg}"
    if [[ "${BASE_CONTENT}" == *"${pkg}"* ]]; then
        fail "${BASE_DOCKERFILE} installs ${pkg} — the Docker stack belongs in fragments/docker.dockerfile, not the stack-agnostic base every image inherits"
    else
        pass
    fi
done

echo "case: Dockerfile.base does not provision the Docker apt repository"
if [[ "${BASE_CONTENT}" == *"download.docker.com"* ]]; then
    fail "${BASE_DOCKERFILE} still configures the download.docker.com apt source — move it to fragments/docker.dockerfile"
else
    pass
fi

print_summary
[[ "${FAILURES}" -eq 0 ]]
