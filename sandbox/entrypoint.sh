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
# entrypoint.sh is this image's literal ENTRYPOINT (no init wrapper baked in
# — see Dockerfile.base), so a container started without `--init` (production
# always passes it; ad-hoc/manual runs may not) makes this process PID 1 in
# the container's PID namespace: its exit SIGKILLs every other process in
# that namespace immediately, including a still-draining `tee` from the
# boot-log redirect below. See flush_boot_log further down for how the remap
# failure branches defend against that race.
#
# On a HOST_UID/HOST_GID mismatch, remap the account, chown the home volume
# (and, only for a bounded per-repo mount, /workspace), then unconditionally
# drop from root to dev via the final exec below — every start now begins as
# root and must land on dev (the host-remapped account, or the untouched
# 1000/1000 default) before the rest of the entrypoint runs. The
# usermod/groupmod/chown calls stay a no-op when HOST_UID/HOST_GID are unset
# or already match the current dev account — zero behavior change there
# beyond the one extra root->dev re-exec every start now pays.
if [[ "$(id -u)" -eq 0 ]]; then
    # A stale generic startup-failure marker or boot log from a previous
    # failed boot must not outlive the failure it describes, or
    # startupFailureDetail (watch/internal/sandbox/launcher/launch.go) would
    # surface it for an unrelated crash. Cleared here, as root, before any of
    # the rest of the entrypoint (including a possible re-exec below) runs.
    rm -f /home/dev/.cenci-startup-failed /home/dev/.cenci-boot.log /home/dev/.cenci-dockerd-startup-error

    # ── Tee entrypoint output to a boot log as early as possible (#495) ──
    # A failure inside THIS root block (the UID/GID remap below, or its
    # chown) used to be invisible to startupFailureDetail
    # (watch/internal/sandbox/launcher/launch.go) entirely: the tee+trap
    # pair was previously installed only after the drop-to-dev re-exec
    # further down, so a root-block failure only ever reached `docker
    # logs`, which is routinely gone by the time the launcher's fallback
    # reads it (containers run with --rm). File-descriptor redirection
    # survives `exec` (exec replaces the process image, but inherited fds
    # persist), so installing the tee here, once, covers the rest of this
    # root block AND the entire dev-side script that follows via the exec
    # below. EXIT traps do NOT survive exec (trap state is shell-internal,
    # discarded on process image replacement) — see the dev-side block
    # further down, which re-arms its own trap unconditionally even though
    # the tee itself must be installed at most once (a second live tee
    # would pipe through the first and double-write every line). Only
    # agent workloads get this (same gate as the dev-side block and the
    # agent-CLI check further below); utility containers (smoke tests,
    # ad-hoc runs) boot without it.
    if [[ -n "${CENCI_AGENT_CLI:-}" || -d /opt/cenci-agent ]]; then
        CENCI_BOOT_LOG="/home/dev/.cenci-boot.log"
        # Checked (unlike the original #495 landing): a silent failure here
        # (disk pressure, unusual volume permissions) would otherwise leave
        # the tee below writing into a file that was never truncated/created
        # or never got the intended 600 mode, defeating the whole point of
        # #495 — making startup failures diagnosable — with nothing to show
        # for it. Warn now, on stderr, BEFORE `exec >` below redirects
        # stderr through the tee: this line must reach `docker logs`
        # directly, since if the file itself is the problem, the boot log
        # can't be trusted to carry it.
        : > "${CENCI_BOOT_LOG}" && chmod 600 "${CENCI_BOOT_LOG}" \
            || echo "warning: boot-log setup failed at ${CENCI_BOOT_LOG} — startup diagnostics may be incomplete or world-readable" >&2
        # Exported (not just a local variable) so it survives the
        # `sudo --preserve-env -u dev` re-exec below (same --preserve-env
        # call that already carries CENCI_SANDBOX across).
        # CENCI_BOOT_LOG_INSTALLED — not CENCI_BOOT_LOG itself — is the
        # sentinel the dev-side block further down checks before installing
        # its own fallback tee. CENCI_BOOT_LOG is just a path; nothing stops
        # it being set externally (e.g. `docker run -e CENCI_BOOT_LOG=...`,
        # though the Go launcher never does this today) for an unrelated
        # reason, which would make the dev-side block wrongly skip
        # installing a real tee while its EXIT trap stays armed.
        export CENCI_BOOT_LOG
        export CENCI_BOOT_LOG_INSTALLED=1
        exec > >(tee -a "${CENCI_BOOT_LOG}") 2>&1
        # Captured immediately after the process substitution above — bash
        # sets $! to the backgrounded tee's PID here. flush_boot_log
        # (defined below, used by the remap failure branches) waits on this
        # before exiting, to avoid losing the failure line to the PID-1/
        # SIGKILL race described above.
        CENCI_TEE_PID=$!
        trap '[[ $? -eq 0 ]] || echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) entrypoint exited before completing startup (root setup)" > /home/dev/.cenci-startup-failed' EXIT
    fi

    # Waits for the background `tee` above (when one was installed — i.e.
    # CENCI_TEE_PID is set) to finish draining before letting an `exit`
    # below proceed. See the PID-1/SIGKILL comment near the top of this
    # block: when this script is the container's PID 1 (no `--init`), its
    # exit reaps every other process in the namespace immediately, including
    # a tee that hasn't yet read the failure line out of its pipe. Closing
    # our own stdout/stderr first makes tee see EOF on its input and
    # flush + exit on its own; `wait` then blocks until it has. This is
    # deliberate, not redundant — do not remove it.
    flush_boot_log() {
        if [[ -n "${CENCI_TEE_PID:-}" ]]; then
            exec 1>&- 2>&-
            wait "${CENCI_TEE_PID}" 2>/dev/null || true
        fi
    }

    CUR_UID="$(id -u dev)"
    CUR_GID="$(id -g dev)"
    if [[ -n "${HOST_UID:-}" && "${HOST_UID}" != "0" && -n "${HOST_GID:-}" && "${HOST_GID}" != "0" ]] \
       && { [[ "${HOST_UID}" != "${CUR_UID}" ]] || [[ "${HOST_GID}" != "${CUR_GID}" ]]; }; then
        if [[ "${HOST_GID}" != "${CUR_GID}" ]]; then
            EXISTING_HOST_GROUP="$(getent group "${HOST_GID}" | cut -d: -f1 || true)"
            if [[ -n "${EXISTING_HOST_GROUP}" ]]; then
                usermod -g "${HOST_GID}" dev || { echo "remap: usermod primary group failed — HOST_UID/HOST_GID may collide with a system account already baked into this image; check what account/group already owns that ID inside the container" >&2; flush_boot_log; exit 1; }
            else
                groupmod -g "${HOST_GID}" dev || { echo "remap: groupmod failed — HOST_UID/HOST_GID may collide with a system account already baked into this image; check what account/group already owns that ID inside the container" >&2; flush_boot_log; exit 1; }
            fi
        fi
        if [[ "${HOST_UID}" != "${CUR_UID}" ]]; then
            usermod -u "${HOST_UID}" dev || { echo "remap: usermod failed — HOST_UID/HOST_GID may collide with a system account already baked into this image; check what account/group already owns that ID inside the container" >&2; flush_boot_log; exit 1; }
        fi
        chown -R "${HOST_UID}:${HOST_GID}" /home/dev || { echo "remap: chown /home/dev failed — the home volume may predate this cenci-sandbox version; try 'cenci sandbox prune --volumes'" >&2; flush_boot_log; exit 1; }
        if [[ "${WORKSPACE_SCOPE:-}" == "repo" ]]; then
            find /workspace -mindepth 1 -exec chown -h "${HOST_UID}:${HOST_GID}" {} + \
                || echo "warning: failed to chown /workspace to ${HOST_UID}:${HOST_GID} — files in this mount may be misowned — the home volume may predate this cenci-sandbox version; try 'cenci sandbox prune --volumes'" >&2
        fi
    elif [[ "${HOST_UID:-}" == "0" || "${HOST_GID:-}" == "0" ]]; then
        echo "warning: HOST_UID/HOST_GID of 0 requested — ignoring remap to avoid running the workload as root" >&2
    fi

    # ── dind: start the inner Docker engine (#586) ────────────────────
    # Gated on CENCI_SANDBOX_DIND=1 (set by the launcher's dind mode, #585).
    # Must run here — while still root and before the priv-drop exec below —
    # because starting dockerd needs root, and adding dev to the docker group
    # must land in /etc/group before the `sudo -u dev` re-exec picks up
    # membership (a fresh re-exec re-reads group membership, so no `sg`
    # re-exec is needed here, unlike the removed DooD socket block).
    if [[ "${CENCI_SANDBOX_DIND:-}" == "1" ]]; then
        DIND_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib"
        # shellcheck source-path=SCRIPTDIR/lib
        # shellcheck source=lib/dind.sh
        source "${DIND_LIB_DIR}/dind.sh"
        start_dind
    fi

    # secure_path in /etc/sudoers strips PATH additions (~/.local/bin,
    # ~/go/bin) even with --preserve-env=PATH; the explicit env "PATH=..."
    # assignment defeats that. --preserve-env carries everything else
    # (TERM, XDG_RUNTIME_DIR, CENCI_SANDBOX, ...) across.
    exec sudo --preserve-env -u dev env "PATH=${PATH}" "HOME=/home/dev" \
        /usr/local/bin/entrypoint.sh "$@"
fi

# Fix home directory ownership (volume may be root-owned on first run)
sudo chown dev:"$(id -g dev)" /home/dev

# ── Tee entrypoint output to a boot log; mark any other startup failure ──
# (#473, #495) The agent-CLI-missing check further below already leaves a
# precise marker at /home/dev/.cenci-agent-startup-error for its own case,
# but any other entrypoint failure (plugin provisioning, settings
# migration, credential seeding, ...) used to vanish once --rm removed the
# container, leaving startupFailureDetail
# (watch/internal/sandbox/launcher/launch.go) with nothing but a fully
# generic message. A container that started as root already had this tee
# installed by the root block above (CENCI_BOOT_LOG and
# CENCI_BOOT_LOG_INSTALLED both survive the `sudo --preserve-env -u dev`
# re-exec into this script, since exec replaces the process image but
# inherited file descriptors AND exported env vars persist) — so the tee
# below only installs as a FALLBACK, gated on CENCI_BOOT_LOG_INSTALLED being
# unset (not on CENCI_BOOT_LOG itself, which is just a path and could in
# principle be set externally for an unrelated reason — see the root block's
# comment on CENCI_BOOT_LOG_INSTALLED), for the direct-as-dev entry point
# (e.g. a manual `docker run` without `--user root`, which skips the root
# block entirely — the image's default USER is dev). Installing it
# unconditionally here would pipe a second live tee through the first and
# double-write every line — do not "fix" that by dropping this guard. EXIT
# traps, unlike file descriptors, do NOT survive exec (trap state is
# shell-internal, discarded on process image replacement), so the trap below
# is always re-armed here regardless of which branch installed the tee. From
# here on, every subsequent stdout/stderr line is teed into CENCI_BOOT_LOG
# (/home/dev/.cenci-boot.log) for startupFailureDetail's second-choice read,
# and this EXIT trap writes the generic startup-failure marker
# (/home/dev/.cenci-startup-failed) — its third choice — whenever the
# entrypoint exits non-zero for any other reason. Both files were already
# cleared at boot by the root block above, so a stale marker never outlives
# the failure it describes. Only agent workloads get this (same gate as the
# agent-CLI check below); utility containers (smoke tests, ad-hoc runs)
# boot without it.
if [[ -n "${CENCI_AGENT_CLI:-}" || -d /opt/cenci-agent ]]; then
    if [[ -z "${CENCI_BOOT_LOG_INSTALLED:-}" ]]; then
        CENCI_BOOT_LOG="/home/dev/.cenci-boot.log"
        # Checked, same as the root block above: an unreported failure here
        # would leave the tee below writing into a file that was never
        # truncated/created or never got the intended 600 mode, defeating
        # the whole point of #495. Warn now, on stderr, BEFORE `exec >`
        # below redirects stderr through the tee, so it reaches `docker
        # logs` directly.
        : > "${CENCI_BOOT_LOG}" && chmod 600 "${CENCI_BOOT_LOG}" \
            || echo "warning: boot-log setup failed at ${CENCI_BOOT_LOG} — startup diagnostics may be incomplete or world-readable" >&2
        CENCI_BOOT_LOG_INSTALLED=1
        # From here on, every stdout/stderr line is persisted to CENCI_BOOT_LOG and
        # may later be surfaced verbatim in `cenci open` error output — never echo
        # credential/token content past this point.
        exec > >(tee -a "${CENCI_BOOT_LOG}") 2>&1
    fi
    trap '[[ $? -eq 0 ]] || echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) entrypoint exited before completing startup" > /home/dev/.cenci-startup-failed' EXIT
fi

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
# see SECURITY.md's threat model, and they must never reach the host
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
# shellcheck source=lib/opencode-config.sh
source "${SCRIPT_DIR}/lib/opencode-config.sh"
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
# blocks container start after the selected CLI has been installed above;
# marketplace/plugin network failures still only warn to stderr.
case "${CENCI_SANDBOX_AGENT:-claude}" in
codex)
    provision_codex_plugins /home/dev/.codex cenci matteobortolazzo/cenci cenci cenci-watch
    ;;
opencode)
    provision_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci
    ;;
*)
    provision_plugins /home/dev/.claude/plugins cenci matteobortolazzo/cenci cenci cenci-watch
    ;;
esac

# ── Keep plugins current (TTL-gated) ──────────────────────────────
# provision_plugins only installs what's missing, so an existing home volume
# would keep stale plugins forever while the marketplace auto-bumps on every
# push to main. update_plugins refreshes the marketplace clone (one git pull)
# and bumps only the plugins whose installed version differs, gated by a
# 30-minute stamp so rapid stop/start cycles make zero network calls. Forced
# variant (ttl 0) is `cenci sandbox update-plugins`. Same guarantee as above:
# failures warn to stderr and never block container start.
case "${CENCI_SANDBOX_AGENT:-claude}" in
codex)
    update_codex_plugins /home/dev/.codex cenci 30 cenci cenci-watch
    ;;
opencode)
    update_opencode_plugins /home/dev/.cenci-src matteobortolazzo/cenci 30
    ;;
*)
    update_plugins /home/dev/.claude/plugins cenci 30 cenci cenci-watch
    ;;
esac

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

# ── Seed OpenCode's config (permissions + plugin registration) ────
# seed_opencode_config (lib/opencode-config.sh) creates or merges
# ~/.config/opencode/opencode.json idempotently: an allow-all permission
# block and disabled native update checks (container-boundary-safe
# defaults, seeded only when absent), plus the cenci-watch OpenCode plugin
# registered by its file:// path into the cenci-src clone
# (provision_opencode_plugins above). Unconditional: harmless in
# Claude/Codex-only containers, and ready if OpenCode is launched later on
# the same home volume.
seed_opencode_config /home/dev/.config/opencode/opencode.json "file:///home/dev/.cenci-src/watch/plugin/opencode"

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

# OpenCode auth (ANTHROPIC_API_KEY/OPENAI_API_KEY are read natively from the
# environment — see execEnvArgs in watch/internal/sandbox/launcher/launch.go
# — so only the subscription/OAuth auth.json needs staging here)
seed_credential /tmp/host-opencode-creds/auth.json /home/dev/.local/share/opencode/auth.json

# ── Verify the shared agent CLI before signaling ready ─────────────
# The launcher mounts the shared, updater-populated volume read-only at
# /opt/cenci-agent and execs this absolute path directly (CENCI_AGENT_CLI, or
# its default derived from CENCI_SANDBOX_AGENT — see
# watch/internal/sandbox/launcher/{engine,launch}.go). If that volume is
# still bootstrapping, or a previous update left it broken, docker only
# surfaces a raw "exec: no such file" later, from whichever `exec` call hits
# it first. Fail fast here instead — before "$@" ever runs below and before
# the launcher's readiness marker (/tmp/cenci-ready) can be touched by it —
# and leave a precise diagnostic exactly where the Go launcher's
# startupFailureDetail reads it (watch/internal/sandbox/launcher/launch.go),
# so a broken volume reports its own cause instead of a generic timeout.
# Only agent workloads get CENCI_AGENT_CLI (and the volume mount) from the
# launcher; utility containers (smoke tests, ad-hoc runs) must boot without
# either, so the check is skipped when neither is present.
if [[ -n "${CENCI_AGENT_CLI:-}" || -d /opt/cenci-agent ]]; then
    AGENT_CLI="${CENCI_AGENT_CLI:-/opt/cenci-agent/current/node_modules/.bin/${CENCI_SANDBOX_AGENT:-claude}}"
    if [[ ! -x "${AGENT_CLI}" ]]; then
        AGENT_CLI_ERROR="agent CLI not found or not executable at ${AGENT_CLI} — the shared /opt/cenci-agent volume may still be bootstrapping, or a previous update failed; rerun 'cenci sandbox update-agent', or wait for the updater to finish and relaunch"
        printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${AGENT_CLI_ERROR}" > /home/dev/.cenci-agent-startup-error
        echo "entrypoint: ${AGENT_CLI_ERROR}" >&2
        exit 1
    fi
    # A marker from an earlier failed boot must not outlive the failure it
    # describes, or startupFailureDetail would surface it for unrelated crashes.
    rm -f /home/dev/.cenci-agent-startup-error
fi

# Put the shared agent CLI's bin directory ahead of PATH so an interactive
# --shell session (or any other child process in this container) can invoke
# the bare `claude`/`codex` command, not just the absolute path the launcher
# execs. Exported before the final exec below so it reaches the whole
# container session.
export PATH="/opt/cenci-agent/current/node_modules/.bin:${PATH}"

exec /bin/bash "$@"
