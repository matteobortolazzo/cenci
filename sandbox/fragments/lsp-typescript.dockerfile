ARG TYPESCRIPT_LANGUAGE_SERVER_VERSION=6.1.0
ARG TYPESCRIPT_VERSION=6.0.3

USER root

# ── TypeScript language server (LSP) ────────────────────────────
# Installed under /usr/local, not NPM_CONFIG_PREFIX (/home/dev/.local): the
# per-scope home volume replaces /home/dev at runtime. typescript stays on 6.x
# because 7.x (the native port) no longer ships the tsserver this server
# drives; a workspace's own node_modules/typescript still takes precedence.
RUN npm install -g --prefix /usr/local \
        "typescript-language-server@${TYPESCRIPT_LANGUAGE_SERVER_VERSION}" \
        "typescript@${TYPESCRIPT_VERSION}" \
    && npm cache clean --force

USER dev
