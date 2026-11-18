ARG PYRIGHT_VERSION=1.1.414

USER root

# ── Pyright language server (LSP) ───────────────────────────────
# npm-distributed, so it needs only the mandatory Node runtime. Installed under
# /usr/local because the home volume replaces /home/dev at runtime.
RUN npm install -g --prefix /usr/local "pyright@${PYRIGHT_VERSION}" \
    && npm cache clean --force

USER dev
