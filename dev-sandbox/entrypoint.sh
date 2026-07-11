#!/bin/bash
# Ensure hostname resolves (suppresses sudo warnings)
sudo sh -c 'grep -q "$(hostname)" /etc/hosts || echo "127.0.0.1 $(hostname)" >> /etc/hosts'

# Fix home directory ownership (volume may be root-owned on first run)
sudo chown dev:dev /home/dev

# First-run setup for empty home volume
if [[ ! -f /home/dev/.bashrc ]]; then
    echo 'export PS1="[\[\e[36m\]sandbox\[\e[0m\]:\[\e[33m\]\w\[\e[0m\]] $ "' >> /home/dev/.bashrc
    echo 'alias ll="ls -la --color=auto"' >> /home/dev/.bashrc
fi

# ── Ensure .claude directory and settings exist ───────────────────
# The home volume may retain stale symlinks from previous runs where
# Claude Code pointed these paths at the host filesystem.  Replace any
# dangling symlink with a real directory / file so plugin installs work.
#
# migrate_settings (lib/migrate-settings.sh) deep-merges three things into
# settings.json in one idempotent pass: the CONTAINER-ONLY bypass-mode keys
# (so --dangerously-skip-permissions never prompts and never downgrades to
# `default` in headless runs — the container boundary is what makes this safe;
# see docs/cohesive-package.md §2.1, and they must never reach the host
# ~/.claude/settings.json), the current agentwatch/agentflow plugins from the
# agent-stack marketplace (so sandbox sessions are visible on the host status
# bar), and a removal of the stale pre-rename muxwatch/ccflow/claude-tools
# stack that old home volumes still carry.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/migrate-settings.sh
source "${SCRIPT_DIR}/lib/migrate-settings.sh"

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

# ── Heal broken plugin install metadata ───────────────────────────
# An interrupted plugin auto-install leaves installed_plugins.json pointing at
# a cache directory that was never populated.  Claude Code trusts the metadata,
# skips reinstall, and every skill of that plugin is "Unknown command" — which
# permanently masks the enabledPlugins provisioning above.  Dropping the broken
# entries makes Claude Code reinstall from the marketplace on next launch.
heal_plugin_installs /home/dev/.claude/plugins

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

# ── Inject host credentials (staged read-only mounts → writable copies) ──

# Claude Code OAuth credentials
if [[ -f /tmp/host-claude-creds/.credentials.json ]]; then
    mkdir -p /home/dev/.claude
    cp /tmp/host-claude-creds/.credentials.json /home/dev/.claude/.credentials.json
    chmod 600 /home/dev/.claude/.credentials.json
fi

# GitHub CLI credentials
if [[ -f /tmp/host-gh-config/hosts.yml ]]; then
    mkdir -p /home/dev/.config/gh
    cp /tmp/host-gh-config/hosts.yml /home/dev/.config/gh/hosts.yml
    chmod 600 /home/dev/.config/gh/hosts.yml
fi

# Codex OAuth credentials (ChatGPT sign-in)
if [[ -f /tmp/host-codex-creds/auth.json ]]; then
    mkdir -p /home/dev/.codex
    cp /tmp/host-codex-creds/auth.json /home/dev/.codex/auth.json
    chmod 600 /home/dev/.codex/auth.json
fi

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
