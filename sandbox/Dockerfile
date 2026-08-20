ARG BASE_VERSION=latest
FROM cenci-sandbox-base:${BASE_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG DOTNET_SDK_VERSION=10.0.100

USER root

# ── .NET SDK ─────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends libicu74 \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh \
    && chmod +x /tmp/dotnet-install.sh \
    && /tmp/dotnet-install.sh --version "${DOTNET_SDK_VERSION}" --install-dir /usr/share/dotnet \
    && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet \
    && rm /tmp/dotnet-install.sh
ENV DOTNET_ROOT=/usr/share/dotnet
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1
ENV DOTNET_NOLOGO=1

USER dev

ARG NODE_MAJOR=24

USER root

# ── Node.js + npm ────────────────────────────────────────────────
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
ENV NPM_CONFIG_PREFIX=/home/dev/.local

USER dev

ARG PLAYWRIGHT_VERSION=1.61.1

USER root

# ── Playwright (Chromium) ──────────────────────────────────────────
# Browsers cache at a shared, world-readable path so the non-root `dev`
# user (and any project-local Playwright install) can reuse this baked-in
# Chromium instead of re-downloading it. --with-deps also installs the OS
# libraries Chromium needs to launch headless, which requires root.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g playwright@${PLAYWRIGHT_VERSION} \
    && NPM_CONFIG_PREFIX=/usr/local npx --yes playwright@${PLAYWRIGHT_VERSION} install --with-deps chromium \
    && chmod -R a+rX /ms-playwright

USER dev

ARG GO_VERSION=1.27.0

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev

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
