#!/bin/bash
# seed_credential — seed-once copy of a staged host credential (#259).
#
# Claude Code and Codex OAuth use refresh-token rotation: every refresh issues
# a new refresh token and invalidates the old one. The moment a container
# refreshes a credential seeded from the host, the host copy and the volume
# copy become independent token chains — re-copying the (now dead) host file
# over the volume's live chain on a later start logs the sandbox out for no
# reason. So a credential is seeded only when the volume has none yet;
# CENCI_SANDBOX_RESEED_CREDS=1 (cenci open --reseed-creds) forces a re-copy for
# recovery, e.g. after revoking all sessions.
#
# The GitHub CLI (hosts.yml) has no refresh cycle, so its copies never fork
# into independent chains and this seed-once caution doesn't apply — it keeps
# the unconditional copy so host re-auths propagate, but only when the host
# file actually carries the token; see seed_gh_hosts below.

# seed_credential <staged-src> <dest>
seed_credential() {
    local staged="$1" dest="$2"
    [[ -f "${staged}" ]] || return 0
    if [[ ! -f "${dest}" || "${CENCI_SANDBOX_RESEED_CREDS:-0}" == "1" ]]; then
        if [[ -L "${dest}" ]]; then
            rm -f "${dest}"
        fi
        mkdir -p "$(dirname "${dest}")"
        cp "${staged}" "${dest}"
        chmod 600 "${dest}"
    fi
    return 0
}

# seed_gh_hosts — token-aware seeding of the GitHub CLI's hosts.yml.
#
# gh has no refresh cycle, so its copies never fork into the independent token
# chains #259 guards against, and the host copy stays canonical: an
# unconditional copy propagates host re-auths. Note this is *not* the same as
# the token being stable — GitHub issues one OAuth token per user per app, so a
# fresh `gh auth login` anywhere invalidates every other copy. Overwriting the
# container copy from a token-carrying host file is the recovery path for that,
# not a hazard. What the copy does require is that the host file actually
# carries the token. Under gh's default secure storage the token
# lives in the host OS keyring and hosts.yml lists the account with no
# `oauth_token:` at all — and the container can't reach a host keyring (there
# is no secret-service provider in the image, and a session bus alone doesn't
# supply one).
#
# Copying such a token-less file in is strictly worse than copying nothing:
# gh's multi-account config migration finds an account whose token it cannot
# resolve and aborts, so *every* gh invocation fails — including `gh --version`,
# and including runs with GH_TOKEN set, because the migration happens on config
# load, before auth resolution. With no hosts.yml at all, gh works normally and
# honours GH_TOKEN. So a token-less staged file is skipped, never copied, and
# an in-container `gh auth login` result is never clobbered by one.

# _gh_hosts_has_token <file>
_gh_hosts_has_token() {
    [[ -f "$1" ]] || return 1
    grep -Eq '^[[:space:]]*oauth_token:[[:space:]]*[^[:space:]#]' "$1"
}

# seed_gh_hosts <staged-src> <dest>
seed_gh_hosts() {
    local staged="$1" dest="$2"
    [[ -f "${staged}" ]] || return 0

    if _gh_hosts_has_token "${staged}"; then
        if [[ -L "${dest}" ]]; then
            rm -f "${dest}"
        fi
        mkdir -p "$(dirname "${dest}")"
        cp "${staged}" "${dest}"
        chmod 600 "${dest}"
        return 0
    fi

    if _gh_hosts_has_token "${dest}"; then
        echo "warning: the host's ~/.config/gh/hosts.yml carries no oauth_token (host gh stores it in the OS keyring) — keeping this sandbox's own gh login instead of overwriting it" >&2
        return 0
    fi

    echo "warning: gh is unauthenticated in this sandbox. The host's ~/.config/gh/hosts.yml names an account but carries no oauth_token, because host gh keeps it in the OS keyring, which the container cannot reach. Not copying that file in: a token-less hosts.yml makes every gh command fail its config migration, even with GH_TOKEN set. Fix it on the host with 'gh auth login --insecure-storage' and re-open the sandbox, or run 'gh auth login' inside the container — an in-container login now persists in the home volume." >&2
    return 0
}
