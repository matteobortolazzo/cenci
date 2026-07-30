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

USER dev
