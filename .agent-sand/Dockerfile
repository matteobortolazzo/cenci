# agentflow:managed-begin
ARG BASE_VERSION=0.9.1
FROM agent-sandbox-base:${BASE_VERSION}

ARG GO_VERSION=1.24.1

USER root

# ── Go ───────────────────────────────────────────────────────────
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:${PATH}"

USER dev
# agentflow:managed-end
