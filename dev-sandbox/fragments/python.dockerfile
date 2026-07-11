USER root

# ── Python (dev headers) ─────────────────────────────────────────
# The interpreter (python3/python3-pip/python3-venv) and uv already ship in
# the base image; this fragment only adds headers needed to build native
# extensions.
RUN apt-get update && apt-get install -y --no-install-recommends python3-dev \
    && rm -rf /var/lib/apt/lists/*

USER dev
