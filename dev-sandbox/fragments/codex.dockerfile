ARG CODEX_VERSION=0.144.4

USER root

# ── Codex CLI ───────────────────────────────────────────────────
# Baked into the image (unlike Claude, which is bind-mounted from the host):
# Codex ships as an npm launcher that resolves a nested native binary, which a
# single-file bind-mount can't carry. Update by bumping CODEX_VERSION + rebuild.
RUN npm install -g @openai/codex@${CODEX_VERSION}

USER dev
