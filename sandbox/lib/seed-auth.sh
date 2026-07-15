#!/bin/bash
# seed_credential — seed-once copy of a staged host credential (#259).
#
# Claude Code and Codex OAuth use refresh-token rotation: every refresh issues
# a new refresh token and invalidates the old one. The moment a container
# refreshes a credential seeded from the host, the host copy and the volume
# copy become independent token chains — re-copying the (now dead) host file
# over the volume's live chain on a later start logs the sandbox out for no
# reason. So a credential is seeded only when the volume has none yet;
# CENCI_SANDBOX_RESEED_CREDS=1 (cenci-sand --reseed-creds) forces a re-copy for
# recovery, e.g. after revoking all sessions.
#
# Non-rotating tokens (GitHub CLI hosts.yml) don't need this and keep the
# unconditional copy so host re-auths propagate.

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
