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
