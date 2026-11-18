ARG GOPLS_VERSION=0.24.0

USER root

# ── gopls (Go LSP) ──────────────────────────────────────────────
# Requires the go fragment. Built with a throwaway GOPATH/GOCACHE and installed
# to /usr/local/bin, because the home volume replaces /home/dev/go at runtime.
RUN GOBIN=/usr/local/bin GOPATH=/tmp/gopls-gopath GOCACHE=/tmp/gopls-cache \
        go install "golang.org/x/tools/gopls@v${GOPLS_VERSION}" \
    && rm -rf /tmp/gopls-gopath /tmp/gopls-cache

USER dev
