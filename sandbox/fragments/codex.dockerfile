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
