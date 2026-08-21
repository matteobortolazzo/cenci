ARG PLAYWRIGHT_VERSION=1.62.1

USER root

# ── Playwright (Chromium) ──────────────────────────────────────────
# Browsers cache at a shared, world-readable path so the non-root `dev`
# user (and any project-local Playwright install) can reuse this baked-in
# Chromium instead of re-downloading it. --with-deps also installs the OS
# libraries Chromium needs to launch headless, which requires root.
# The cache is then handed to `dev` rather than left root-owned (#1096): the
# baked Chromium *build revision* is tied to PLAYWRIGHT_VERSION, so a repo
# pinning a different @playwright/test needs a build this image does not
# carry. `npx playwright install` can fetch it into the shared path — but
# only if the user running it may write there. Root ownership turned a
# recoverable version drift into a hard failure. entrypoint.sh re-owns this
# path during the UID/GID remap so the same holds on non-1000 hosts.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g playwright@${PLAYWRIGHT_VERSION} \
    && NPM_CONFIG_PREFIX=/usr/local npx --yes playwright@${PLAYWRIGHT_VERSION} install --with-deps chromium \
    && chown -R dev:dev /ms-playwright \
    && chmod -R a+rX,u+w /ms-playwright

USER dev
