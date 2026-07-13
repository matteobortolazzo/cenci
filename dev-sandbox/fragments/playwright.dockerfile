ARG PLAYWRIGHT_VERSION=1.61.1

USER root

# ── Playwright (Chromium) ──────────────────────────────────────────
# Browsers cache at a shared, world-readable path so the non-root `dev`
# user (and any project-local Playwright install) can reuse this baked-in
# Chromium instead of re-downloading it. --with-deps also installs the OS
# libraries Chromium needs to launch headless, which requires root.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npm install -g playwright@${PLAYWRIGHT_VERSION} \
    && npx --yes playwright@${PLAYWRIGHT_VERSION} install --with-deps chromium \
    && chmod -R a+rX /ms-playwright

USER dev
