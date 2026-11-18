# cenci:managed-begin
ARG BASE_VERSION=latest
FROM cenci-sandbox-base:${BASE_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# cenci:fragment-begin node
ARG NODE_MAJOR=26

USER root

# ── Node.js + npm ────────────────────────────────────────────────
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
ENV NPM_CONFIG_PREFIX=/home/dev/.local

USER dev
# cenci:fragment-end node

# cenci:fragment-begin go
ARG GO_VERSION=1.27.4

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev
# cenci:fragment-end go

# cenci:fragment-begin lsp-pyright
ARG PYRIGHT_VERSION=1.1.414

USER root

# ── Pyright language server (LSP) ───────────────────────────────
# npm-distributed, so it needs only the mandatory Node runtime. Installed under
# /usr/local because the home volume replaces /home/dev at runtime.
RUN npm install -g --prefix /usr/local "pyright@${PYRIGHT_VERSION}" \
    && npm cache clean --force

USER dev
# cenci:fragment-end lsp-pyright

# cenci:fragment-begin lsp-gopls
ARG GOPLS_VERSION=0.24.0

USER root

# ── gopls (Go LSP) ──────────────────────────────────────────────
# Requires the go fragment. Built with a throwaway GOPATH/GOCACHE and installed
# to /usr/local/bin, because the home volume replaces /home/dev/go at runtime.
RUN GOBIN=/usr/local/bin GOPATH=/tmp/gopls-gopath GOCACHE=/tmp/gopls-cache \
        go install "golang.org/x/tools/gopls@v${GOPLS_VERSION}" \
    && rm -rf /tmp/gopls-gopath /tmp/gopls-cache

USER dev
# cenci:fragment-end lsp-gopls
# cenci:managed-end
