ARG RUST_VERSION=stable

USER root

# ── Rust ─────────────────────────────────────────────────────────
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y \
    --default-toolchain "${RUST_VERSION}" \
    --profile minimal \
    && chmod -R a+w "${RUSTUP_HOME}" "${CARGO_HOME}"

USER dev
