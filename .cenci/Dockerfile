# cenci:managed-begin
ARG BASE_VERSION=1.0.2
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

ARG CODEX_VERSION=0.144.1

USER root

# ── Codex CLI ───────────────────────────────────────────────────
# Baked into the image (unlike Claude, which is bind-mounted from the host):
# Codex ships as an npm launcher that resolves a nested native binary, which a
# single-file bind-mount can't carry. Update by bumping CODEX_VERSION + rebuild.
RUN npm install -g @openai/codex@${CODEX_VERSION}

USER dev
# cenci:managed-end
