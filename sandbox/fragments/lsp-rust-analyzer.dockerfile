USER root

# ── rust-analyzer (Rust LSP) ────────────────────────────────────
# Requires the rust fragment; the component follows its RUST_VERSION toolchain,
# and rustup's rust-analyzer proxy is already on PATH under CARGO_HOME.
RUN rustup component add rust-analyzer \
    && chmod -R a+w "${RUSTUP_HOME}"

USER dev
