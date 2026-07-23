ARG PEN_CLI_VERSION=0.3.0

USER root

# ── Pencil CLI (headless design reads) ───────────────────────────
# @pen.dev/cli runs the Pencil editor engine fully headless — CanvasKit
# rendering, no GUI or desktop app — so `pen interactive -i <file>.pen`
# powers the implement/verify-ui design reads inside the sandbox. Auth comes
# from a seeded ~/.pencil/session-cli.json or a per-exec PEN_CLI_KEY (see
# entrypoint.sh) — never baked into the image. Requires the Node fragment,
# which is mandatory in every generated image and ordered before this block.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g @pen.dev/cli@${PEN_CLI_VERSION}

USER dev
