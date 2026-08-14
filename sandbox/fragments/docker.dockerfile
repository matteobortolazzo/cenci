USER root

# ── Docker CLI + inner engine (dind, #586) ────────────────────────
# Config-selected, not universal (#831): included only when the repo turns on
# nested Docker (`sandbox.dind` in .cenci/config.json). Running the inner
# daemon additionally requires sysbox-runc registered with Docker on the host
# — `cenci open` hard-fails the launch otherwise. Lives here rather than in
# Dockerfile.base so images that never run nested Docker do not carry the
# engine and containerd.
# hadolint ignore=SC1091
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "${VERSION_CODENAME}") stable" \
    > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends docker-ce-cli docker-ce containerd.io \
    && rm -rf /var/lib/apt/lists/*

# dev's docker-group membership is baked here, at build time, and NOT left to
# lib/dind.sh's runtime `usermod -aG docker dev`. dind only ever launches under
# sysbox-runc, which clones the container's rootfs — so /etc/group edits made
# inside the container are invisible to the Docker daemon's own `docker exec -u
# dev` user resolution, while image-baked membership survives. Since the
# launcher attaches every agent session with exactly `docker exec -u dev`
# (assembleExecEnv, watch/internal/sandbox/launcher/launch.go), a runtime-only
# group add reaches PID 1's priv-dropped shell but never the agent, which lands
# with groups=dev and fails every docker call with "permission denied while
# trying to connect to the docker API at unix:///var/run/docker.sock".
# `groupadd -f` rather than relying on the docker-ce postinst having created the
# group: -f is a no-op when it already exists, and dockerd group-owns
# /var/run/docker.sock by the name `docker`, so the gid it picks does not matter.
RUN groupadd -f docker && usermod -aG docker dev

USER dev
