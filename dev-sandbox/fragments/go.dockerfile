ARG GO_VERSION=1.24.1

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev
