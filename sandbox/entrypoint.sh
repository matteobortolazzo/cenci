#!/bin/bash
# Ensure hostname resolves (suppresses sudo warnings)
sudo sh -c 'grep -q "$(hostname)" /etc/hosts || echo "127.0.0.1 $(hostname)" >> /etc/hosts'

# ── UID/GID remap to match the host user, then drop to dev (#154) ─────
# Dockerfile.base bakes 'dev' as uid/gid 1000. On hosts where the invoking
# user isn't 1000, files the container writes into bind-mounted host paths
# end up owned by a UID that doesn't match the host user. The cenci launcher passes
# the host user's id as HOST_UID/HOST_GID and now starts the container as
# root (--user root) precisely so this block can run before any process
# exists under the 'dev' account: usermod/groupmod refuse to renumber an
# account that has a running process, and entrypoint.sh itself used to run
# as dev, which made every real remap fail. Starting as root sidesteps that.
#
# On a HOST_UID/HOST_GID mismatch, remap the account, chown the home volume
# (and, only for a bounded per-repo mount, /workspace), then unconditionally
# drop from root to dev via the final exec below — every start now begins as
# root and must land on dev (the host-remapped account, or the untouched
# 1000/1000 default) before the rest of the entrypoint runs. The
# usermod/groupmod/chown calls stay a no-op when HOST_UID/HOST_GID are unset
# or already match the current dev account — zero behavior change there
# beyond the one extra root->dev re-exec every start now pays.  Mirrors the
# exec-sg re-exec pattern used for the docker-socket GID switch further down
# this file.
if [[ "$(id -u)" -eq 0 ]]; then
    CUR_UID="$(id -u dev)"
    CUR_GID="$(id -g dev)"
    if [[ -n "${HOST_UID:-}" && "${HOST_UID}" != "0" && -n "${HOST_GID:-}" && "${HOST_GID}" != "0" ]] \
       && { [[ "${HOST_UID}" != "${CUR_UID}" ]] || [[ "${HOST_GID}" != "${CUR_GID}" ]]; }; then
        if [[ "${HOST_GID}" != "${CUR_GID}" ]]; then
            EXISTING_HOST_GROUP="$(getent group "${HOST_GID}" | cut -d: -f1 || true)"
            if [[ -n "${EXISTING_HOST_GROUP}" ]]; then
                usermod -g "${HOST_GID}" dev || { echo "remap: usermod primary group failed" >&2; exit 1; }
            else
                groupmod -g "${HOST_GID}" dev || { echo "remap: groupmod failed" >&2; exit 1; }
            fi
        fi
        if [[ "${HOST_UID}" != "${CUR_UID}" ]]; then
            usermod -u "${HOST_UID}" dev || { echo "remap: usermod failed" >&2; exit 1; }
        fi
        chown -R "${HOST_UID}:${HOST_GID}" /home/dev || { echo "remap: chown /home/dev failed" >&2; exit 1; }
        if [[ "${WORKSPACE_SCOPE:-}" == "repo" ]]; then
            find /workspace -mindepth 1 -exec chown -h "${HOST_UID}:${HOST_GID}" {} + \
                || echo "warning: failed to chown /workspace to ${HOST_UID}:${HOST_GID} — files in this mount may be misowned" >&2
        fi
    elif [[ "${HOST_UID:-}" == "0" || "${HOST_GID:-}" == "0" ]]; then
        echo "warning: HOST_UID/HOST_GID of 0 requested — ignoring remap to avoid running the workload as root" >&2
    fi

    # secure_path in /etc/sudoers strips PATH additions (~/.local/bin,
    # ~/go/bin) even with --preserve-env=PATH; the explicit env "PATH=..."
    # assignment defeats that. --preserve-env carries everything else
    # (TERM, XDG_RUNTIME_DIR, CENCI_SANDBOX, OPENAI_API_KEY, ...) across.
    exec sudo --preserve-env -u dev env "PATH=${PATH}" "HOME=/home/dev" \
        /usr/local/bin/entrypoint.sh "$@"
fi

# Fix home directory ownership (volume may be root-owned on first run)
sudo chown dev:"$(id -g dev)" /home/dev

# First-run setup for empty home volume
if [[ ! -f /home/dev/.bashrc ]]; then
    printf '%s\n' 'export PS1="[\[\e[36m\]sandbox\[\e[0m\]:\[\e[33m\]\w\[\e[0m\]] $ "' >> /home/dev/.bashrc
    echo 'alias ll="ls -la --color=auto"' >> /home/dev/.bashrc
fi

# ── Ensure .claude directory and settings exist ───────────────────
# The home volume may retain stale symlinks from previous runs where
# Claude Code pointed these paths at the host filesystem.  Replace any
# dangling symlink with a real directory / file so plugin installs work.
#
# migrate_settings (lib/migrate-settings.sh) deep-merges settings into
# settings.json in one idempotent pass: the CONTAINER-ONLY bypass-mode keys
# (so --dangerously-skip-permissions never prompts and never downgrades to
# `default` in headless runs — the container boundary is what makes this safe;
# see docs/cohesive-package.md §2.1, and they must never reach the host
# ~/.claude/settings.json), the current cenci-watch/cenci plugins from the
# cenci marketplace (so sandbox sessions are visible on the host status
# bar), a removal of the stale pre-rename muxwatch/ccflow/claude-tools
# stack that old home volumes still carry, and default UI preferences
# (fullscreen TUI, clear-context-on-plan-accept) seeded only when the volume
# has none of its own.
#
# This enabledPlugins/extraKnownMarketplaces settings alone do NOT actually
# install anything on Claude Code 2.1.207: its settings-driven auto-install
# writes installed_plugins.json (correct versions, correct installPaths) but
# never populates plugins/cache/ — deterministic, reproduced on a bare
# `claude -p "hi"`. The heal + provision_plugins calls below are what
# actually materialize the plugins' skills; this migration only makes sure
# Claude Code *wants* them enabled once the CLI has installed them.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/migrate-settings.sh
source "${SCRIPT_DIR}/lib/migrate-settings.sh"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/codex-config.sh
source "${SCRIPT_DIR}/lib/codex-config.sh"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/seed-auth.sh
source "${SCRIPT_DIR}/lib/seed-auth.sh"

if [[ -L /home/dev/.claude ]]; then
    rm -f /home/dev/.claude
fi
mkdir -p /home/dev/.claude

if [[ -L /home/dev/.claude/settings.json ]]; then
    rm -f /home/dev/.claude/settings.json
fi
if [[ ! -f /home/dev/.claude/settings.json ]]; then
    # Fresh volume → seed the merged object (bypass + plugins).
    echo '{}' | migrate_settings > /home/dev/.claude/settings.json
elif jq -e . /home/dev/.claude/settings.json >/dev/null 2>&1; then
    # Valid JSON → deep-merge our keys in (ours win on conflict; every
    # other top-level key, including permissions.*, is preserved) and drop the
    # stale plugin stack.  This upgrades old volumes and is idempotent.
    if migrate_settings < /home/dev/.claude/settings.json > /home/dev/.claude/settings.json.tmp; then
        mv /home/dev/.claude/settings.json.tmp /home/dev/.claude/settings.json
    else
        rm -f /home/dev/.claude/settings.json.tmp
    fi
else
    # Present but not valid JSON → overwrite with the merged default.
    echo '{}' | migrate_settings > /home/dev/.claude/settings.json
fi

# ── Heal broken Claude plugin install metadata ────────────────────
# An interrupted plugin auto-install leaves installed_plugins.json pointing at
# a cache directory that was never populated.  Claude Code trusts the metadata,
# skips reinstall, and every skill of that plugin is "Unknown command" — which
# permanently masks the enabledPlugins provisioning above.  Dropping the broken
# entries makes the "is it installed" check below truthful, and stays useful
# on its own once the upstream 2.1.207 bug is fixed.
if [[ "${CENCI_SANDBOX_AGENT:-claude}" == claude ]]; then
    heal_plugin_installs /home/dev/.claude/plugins
fi

# ── Provision plugins explicitly via the official CLI ─────────────
# Claude Code 2.1.207's settings-driven auto-install never populates
# plugins/cache/ (see comment above), so `enabledPlugins` alone never
# materializes the skills — sessions fail with "Unknown command" forever,
# even right after heal_plugin_installs, because nothing ever reinstalls.
# `claude plugin marketplace add`/`claude plugin install` are the CLI path
# that actually works and is idempotent — install even repairs a
# metadata-present/cache-missing state on its own. Costs one marketplace
# clone (~10-20s) on first boot only; a healthy volume makes zero `claude`
# calls here (the TTL-gated refresh below is the only recurring cost). Never
# blocks container start: Claude sandboxes use the mounted Claude CLI, while
# Codex sandboxes use the baked-in Codex CLI; missing CLIs and offline failures
# only warn to stderr.
if [[ "${CENCI_SANDBOX_AGENT:-claude}" == codex ]]; then
    provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci cenci cenci-watch
else
    provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci cenci cenci-watch
fi

# ── Keep plugins current (TTL-gated) ──────────────────────────────
# provision_plugins only installs what's missing, so an existing home volume
# would keep stale plugins forever while the marketplace auto-bumps on every
# push to main. update_plugins refreshes the marketplace clone (one git pull)
# and bumps only the plugins whose installed version differs, gated by a
# 30-minute stamp so rapid stop/start cycles make zero network calls. Forced
# variant (ttl 0) is `cenci sandbox update-plugins`. Same guarantee as above:
# failures warn to stderr and never block container start.
if [[ "${CENCI_SANDBOX_AGENT:-claude}" == codex ]]; then
    update_codex_plugins /home/dev/.codex cenci 30 cenci cenci-watch
else
    update_plugins /home/dev/.claude/plugins cenci 30 cenci cenci-watch
fi

# ── Skip Claude Code's first-run onboarding wizard ────────────────
# Onboarding state (theme picker, terminal "anti-flicker" setup, account step)
# lives in /home/dev/.claude.json, not settings.json.  Seeding
# hasCompletedOnboarding here means a fresh --name instance jumps straight to a
# usable session; login still works via the injected host credentials.  Unlike
# settings.json we NEVER overwrite an invalid .claude.json — it holds
# unrecoverable state (oauthAccount, project trust, history).

if [[ -L /home/dev/.claude.json ]]; then
    rm -f /home/dev/.claude.json
fi
if [[ ! -f /home/dev/.claude.json ]]; then
    # Fresh volume → seed onboarding-complete.
    echo "${ONBOARDING_SETTINGS}" > /home/dev/.claude.json
elif jq -e . /home/dev/.claude.json >/dev/null 2>&1; then
    # Valid JSON → merge the flag in, preserving account/trust/history.
    if seed_onboarding < /home/dev/.claude.json > /home/dev/.claude.json.tmp; then
        mv /home/dev/.claude.json.tmp /home/dev/.claude.json
    else
        rm -f /home/dev/.claude.json.tmp
    fi
fi
# Invalid JSON → leave untouched (never clobber unrecoverable state).

# ── Seed Codex's native status line ───────────────────────────────
# Codex renders its status line from `[tui] status_line` in config.toml — no
# external binary needed. seed_codex_config (lib/codex-config.sh) creates the
# file or appends the block, and leaves any existing [tui]/status_line
# untouched. Unconditional: harmless in Claude-only containers, and ready if
# codex is launched later on the same home volume.
if [[ -L /home/dev/.codex/config.toml ]]; then
    rm -f /home/dev/.codex/config.toml
fi
seed_codex_config /home/dev/.codex/config.toml

# ── Inject host credentials (staged read-only mounts → writable copies) ──
#
# Claude and Codex OAuth tokens rotate on refresh, so the volume's copy forks
# into its own token chain after first use — seed_credential (lib/seed-auth.sh)
# copies them only when the volume has none yet (or on --reseed-creds), never
# clobbering a live chain with a stale host copy (#259).

# Claude Code OAuth credentials
seed_credential /tmp/host-claude-creds/.credentials.json /home/dev/.claude/.credentials.json

# GitHub CLI credentials — non-rotating token, host stays canonical, so an
# unconditional copy is safe and propagates host re-auths.
if [[ -f /tmp/host-gh-config/hosts.yml ]]; then
    mkdir -p /home/dev/.config/gh
    cp /tmp/host-gh-config/hosts.yml /home/dev/.config/gh/hosts.yml
    chmod 600 /home/dev/.config/gh/hosts.yml
fi

# Codex OAuth credentials (ChatGPT sign-in)
seed_credential /tmp/host-codex-creds/auth.json /home/dev/.codex/auth.json

# ── Docker socket group alignment (DooD) ────────────────────────────
if [[ -S /var/run/docker.sock ]]; then
    SOCK_GID=$(stat -c '%g' /var/run/docker.sock)
    EXISTING_GROUP=$(getent group "${SOCK_GID}" | cut -d: -f1 || true)
    if [[ -z "${EXISTING_GROUP}" ]]; then
        sudo groupadd -g "${SOCK_GID}" docker
        EXISTING_GROUP="docker"
    fi
    if ! id -nG dev | grep -qw "${EXISTING_GROUP}"; then
        sudo usermod -aG "${EXISTING_GROUP}" dev
        if [[ $# -gt 0 ]]; then
            exec sg "${EXISTING_GROUP}" -c "/bin/bash $(printf '%q ' "$@")"
        else
            exec sg "${EXISTING_GROUP}" -c /bin/bash
        fi
    fi
fi

exec /bin/bash "$@"
