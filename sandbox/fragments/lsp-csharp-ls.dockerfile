ARG CSHARP_LS_VERSION=0.29.0
ARG CSHARP_LS_DOTNET_CHANNEL=11.0

USER root

# ── csharp-ls (C# LSP) ──────────────────────────────────────────
# Requires the dotnet fragment. csharp-ls targets net11, so the matching
# runtime is installed alongside a lower pinned SDK (a no-op when the SDK
# already carries it). The tool goes under /usr/local, outside the home volume.
RUN curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh \
    && chmod +x /tmp/dotnet-install.sh \
    && /tmp/dotnet-install.sh --runtime dotnet --channel "${CSHARP_LS_DOTNET_CHANNEL}" --install-dir /usr/share/dotnet \
    && rm /tmp/dotnet-install.sh \
    && dotnet tool install --tool-path /usr/local/lib/csharp-ls --version "${CSHARP_LS_VERSION}" csharp-ls \
    && ln -s /usr/local/lib/csharp-ls/csharp-ls /usr/local/bin/csharp-ls \
    && rm -rf /root/.nuget /root/.dotnet

USER dev
