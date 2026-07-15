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

USER dev

ARG PLAYWRIGHT_VERSION=1.61.1

USER root

# ── Playwright (Chromium) ──────────────────────────────────────────
# Browsers cache at a shared, world-readable path so the non-root `dev`
# user (and any project-local Playwright install) can reuse this baked-in
# Chromium instead of re-downloading it. --with-deps also installs the OS
# libraries Chromium needs to launch headless, which requires root.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npm install -g playwright@${PLAYWRIGHT_VERSION} \
    && npx --yes playwright@${PLAYWRIGHT_VERSION} install --with-deps chromium \
    && chmod -R a+rX /ms-playwright

USER dev

ARG GO_VERSION=1.24.1

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev

ARG CODEX_VERSION=0.144.1

USER root

# ── Codex CLI ────────────────────────────────────────────────────
# Baked into the image (unlike Claude, which is bind-mounted from the host):
# Codex ships as an npm launcher that resolves a nested native binary, which a
# single-file bind-mount can't carry. Update by bumping CODEX_VERSION + rebuild.
RUN npm install -g @openai/codex@${CODEX_VERSION}

USER dev
