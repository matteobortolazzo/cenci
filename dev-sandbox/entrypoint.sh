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
# Seed the bypass-mode settings so --dangerously-skip-permissions never
# prompts ("Yes, I accept") on fresh instances and never silently
# downgrades to `default` in headless (`claude -p`) runs.  These keys are
# CONTAINER-ONLY: the container boundary is what makes bypass mode safe
# (see docs/cohesive-package.md §2.1).  They must never reach the host
# ~/.claude/settings.json.

BYPASS_SETTINGS='{"skipDangerousModePermissionPrompt":true,"permissions":{"defaultMode":"bypassPermissions"}}'

if [[ -L /home/dev/.claude ]]; then
    rm -f /home/dev/.claude
fi
mkdir -p /home/dev/.claude

if [[ -L /home/dev/.claude/settings.json ]]; then
    rm -f /home/dev/.claude/settings.json
fi
if [[ ! -f /home/dev/.claude/settings.json ]]; then
    # Fresh volume → write the bypass settings.
    echo "${BYPASS_SETTINGS}" > /home/dev/.claude/settings.json
elif jq -e . /home/dev/.claude/settings.json >/dev/null 2>&1; then
    # Valid JSON → deep-merge our keys in (ours win on conflict; every
    # other top-level key, including permissions.*, is preserved).  This
    # upgrades old `{}` volumes and is idempotent.
    if jq --argjson bypass "${BYPASS_SETTINGS}" '. * $bypass' \
        /home/dev/.claude/settings.json > /home/dev/.claude/settings.json.tmp; then
        mv /home/dev/.claude/settings.json.tmp /home/dev/.claude/settings.json
    else
        rm -f /home/dev/.claude/settings.json.tmp
    fi
else
    # Present but not valid JSON → overwrite with a safe default.
    echo "${BYPASS_SETTINGS}" > /home/dev/.claude/settings.json
fi

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

# ── Provision agent-stack plugins (idempotent, best-effort) ─────────
# Register the matteobortolazzo/agent-stack marketplace and install the
# agentflow + agentwatch plugins into the container's home volume so
# slash commands like /agentflow:implement work out of the box.  This
# used to be a manual per-volume step (see dev-sandbox/README.md), which
# is why volumes rotted after the plugins were renamed.
#
# CONTAINER-ONLY: every write here lands in /home/dev/.claude (the
# persistent volume), never the host ~/.claude.  It never pins a model.
#
# Best-effort: the marketplace lives on GitHub, so provisioning needs
# network.  If GitHub is unreachable we warn and continue — a missing
# plugin must never block the launch (there is deliberately no `set -e`
# in this script).  `timeout` guards against a hang on an offline host.
#
# Only the `claude` agent bind-mounts the CLI (codex launches don't), so
# skip entirely when `claude` is absent.
provision_agent_stack_plugins() {
    command -v claude >/dev/null 2>&1 || return 0

    local marketplace_repo="matteobortolazzo/agent-stack"
    local marketplace_name="agent-stack"
    local plugins="agentflow agentwatch"
    local p

    # Fast path: if the marketplace is registered and every plugin is
    # already installed, there is nothing to do — skip all network calls
    # so steady-state launches only pay for two local list reads.
    local need_provision=false
    if ! claude plugin marketplace list 2>/dev/null | grep -q "${marketplace_name}"; then
        need_provision=true
    else
        for p in ${plugins}; do
            if ! claude plugin list 2>/dev/null | grep -q "${p}"; then
                need_provision=true
                break
            fi
        done
    fi
    "${need_provision}" || return 0

    echo "Provisioning agent-stack plugins (${plugins})..."

    if ! claude plugin marketplace list 2>/dev/null | grep -q "${marketplace_name}"; then
        if ! timeout 60 claude plugin marketplace add "${marketplace_repo}" >/dev/null 2>&1; then
            echo "  ! Could not register the '${marketplace_name}' marketplace (offline?). Plugins will retry next launch." >&2
            return 0
        fi
    fi

    for p in ${plugins}; do
        claude plugin list 2>/dev/null | grep -q "${p}" && continue
        if timeout 120 claude plugin install "${p}@${marketplace_name}" >/dev/null 2>&1 ||
           timeout 120 claude plugin install "${p}" >/dev/null 2>&1; then
            echo "  ✓ installed ${p}"
        else
            echo "  ! Could not install ${p} (offline?). It will retry next launch." >&2
        fi
    done
}

provision_agent_stack_plugins

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
