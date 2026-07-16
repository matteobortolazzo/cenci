# cenci:managed-begin
ARG BASE_VERSION=1.2.0
FROM cenci-sandbox-base:${BASE_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG NODE_MAJOR=24

USER root

# ── Node.js + npm ────────────────────────────────────────────────
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

USER dev

ARG GO_VERSION=1.24.1

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev

ARG INSTALL_CODEX=1
ARG AGENTS_REFRESH=0

USER root

# ── Codex CLI ───────────────────────────────────────────────────
# Baked into the image so `cenci open --agent codex` works offline: Codex ships
# as an npm launcher that resolves a nested native binary, which a single-file
# bind-mount can't carry. Installed at @latest — AGENTS_REFRESH busts this
# layer's cache so a rebuild re-fetches the newest release. Skipped when
# INSTALL_CODEX=0 (a Claude-only sandbox).
RUN if [ "${INSTALL_CODEX}" = 1 ]; then \
        echo "agents-refresh: ${AGENTS_REFRESH}" >/dev/null; \
        npm install -g @openai/codex@latest; \
    fi

USER dev

ARG INSTALL_CLAUDE=1
ARG AGENTS_REFRESH=0

USER root

# ── Claude Code CLI ─────────────────────────────────────────────
# Baked into the image at @latest (Claude used to be bind-mounted from the host
# binary; it is now installed here so per-repo images and Claude-only sandboxes
# no longer depend on a host install). AGENTS_REFRESH busts this layer's cache so
# a rebuild re-fetches the newest release. Skipped when INSTALL_CLAUDE=0 (a
# Codex-only sandbox). Credentials are still staged from the host at launch.
RUN if [ "${INSTALL_CLAUDE}" = 1 ]; then \
        echo "agents-refresh: ${AGENTS_REFRESH}" >/dev/null; \
        npm install -g @anthropic-ai/claude-code@latest; \
    fi

USER dev
# cenci:managed-end
