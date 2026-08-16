USER root

# ── Azure CLI ────────────────────────────────────────────────────
# Config-selected (`sandbox.azure: true` in .cenci/config.json), not
# stack-selected: an Azure repo can be written in any language, so no stack
# token implies it. Installed from Microsoft's own apt repo rather than
# `pip install azure-cli` so the CLI's vendored Python dependencies never
# collide with the workspace's own virtualenv.
#
# The point of baking it in is that `az <group> <cmd> --help` answers from the
# installed CLI instead of the agent guessing command shapes it cannot verify.
# Deliberately unpinned, like the docker fragment: the apt repo publishes one
# rolling `azure-cli` package, and Azure's service-side APIs move under it, so
# a pinned CLI drifts out of date against the services it targets. Rebuild to
# pick up a newer CLI.
#
# Auth is never baked in — the launcher stages the host's ~/.azure auth files
# read-only and entrypoint.sh seeds them once into the home volume (see
# lib/seed-auth.sh, seed_azure_creds).
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://packages.microsoft.com/keys/microsoft.asc \
    | gpg --dearmor -o /etc/apt/keyrings/microsoft.gpg \
    && chmod a+r /etc/apt/keyrings/microsoft.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/microsoft.gpg] https://packages.microsoft.com/repos/azure-cli/ $(. /etc/os-release && echo "${VERSION_CODENAME}") main" \
    > /etc/apt/sources.list.d/azure-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends azure-cli \
    && rm -rf /var/lib/apt/lists/*

USER dev
