#!/bin/bash
# remap.sh — home-volume ownership remap for entrypoint.sh's root phase
# (#1032, sourced by the UID/GID remap block in entrypoint.sh).
#
# entrypoint.sh renumbers the baked-in `dev` account (uid/gid 1000) onto the
# launcher-supplied HOST_UID/HOST_GID, then has to re-own the persisted home
# volume so the remapped account can still write it. The obvious `chown -R
# "${HOST_UID}:${HOST_GID}" /home/dev` is wrong, and fatally so on any host
# with a git identity: the launcher bind-mounts it READ-ONLY *inside* that
# tree (watch/internal/sandbox/launcher/launch.go, assembleVolumeMounts):
#
#     -v <host gitconfig>:/home/dev/.gitconfig:ro
#
# `chown -R` issues the chown(2) syscall unconditionally — it does NOT skip
# entries that already carry the target ownership — so it descends into the
# read-only bind mount, gets EROFS, and (because the remap treats a failed
# chown as fatal, correctly: a half-owned home is worse than no container)
# kills the container before the workload ever starts:
#
#     chown: changing ownership of '/home/dev/.gitconfig': Read-only file system
#
# entrypoint.sh calls chown_home_tree() for a second tree too: the Playwright
# browser cache at /ms-playwright (#1096), which is owned by `dev` at build
# time so a project-local `playwright install` can add the Chromium build its
# pinned @playwright/test needs. That ownership is by uid, so it has to follow
# the remap as well. The helper is tree-generic despite its name; only that
# call site treats a failure as a warning rather than fatal.
#
# chown_home_tree() prunes every mount point nested under the home root
# instead. Those nested mounts are host-owned by design and the workload only
# reads them, so their ownership is none of the remap's business — while
# everything the home volume itself holds is still re-owned exactly as before,
# and a genuine failure there is still fatal.
#
# The prune list is derived from /proc/self/mountinfo rather than a hardcoded
# `.gitconfig` path deliberately: `.gitconfig` is merely the only read-only
# mount that lands under /home/dev *today* (every credential staging mount
# lands under /tmp/, outside this tree), and a hardcoded exclusion would let
# the next one reintroduce exactly this startup failure. CENCI_MOUNTINFO is a
# narrow, test-only override of that path — the same convention as
# lib/dind.sh's CENCI_DIND_HOME_ROOT — so remap-chown.test.sh can run on a
# host with no /proc; production never sets it.

# collect_nested_mounts <root>
#   Populates the CENCI_NESTED_MOUNTS array with every mount point strictly
#   under <root>, decoded from mountinfo's octal escaping. <root> itself is
#   deliberately excluded: it is a mount point too (the named home volume),
#   but it is the target of the remap, not something to skip.
#
#   An unreadable mountinfo yields an empty list, which falls open to chowning
#   the whole tree — the pre-#1032 behavior. Failing open is the right default
#   here: pruning blindly would silently leave parts of a real home volume
#   owned by the wrong uid, which is the harder failure to notice.
collect_nested_mounts() {
    local root="$1"
    local mountinfo="${CENCI_MOUNTINFO:-/proc/self/mountinfo}"

    CENCI_NESTED_MOUNTS=()
    [[ -r "${mountinfo}" ]] || return 0

    local point
    # Field 5 of every mountinfo line is the mount point; the variable-length
    # optional fields only start at field 7, so a fixed positional read is
    # safe here.
    while read -r _ _ _ _ point _; do
        # mountinfo octal-escapes the four characters that would otherwise
        # break its own field format. Backslash is decoded last so a literal
        # backslash already in the path cannot swallow the escape that
        # follows it.
        point="${point//\\040/ }"
        point="${point//\\011/$'\t'}"
        point="${point//\\012/$'\n'}"
        point="${point//\\134/\\}"
        # Prefix match on "${root}/" — not "${root}*" — so a sibling that
        # merely shares the root's name (/home/dev-other/...) is not mistaken
        # for a nested mount.
        [[ "${point}" == "${root}"/* ]] || continue
        CENCI_NESTED_MOUNTS+=("${point}")
    done <"${mountinfo}"
}

# chown_home_tree <uid> <gid> <root>
#   Re-owns <root> and everything beneath it to <uid>:<gid>, skipping nested
#   mount points. Returns non-zero if any chown that was actually attempted
#   failed, so real remap breakage stays as fatal as it was before.
chown_home_tree() {
    local uid="$1" gid="$2" root="$3"

    # The root itself is chowned directly rather than through the find below,
    # which uses -mindepth 1 so a pruned nested mount can never take the root
    # with it.
    chown "${uid}:${gid}" "${root}" || return 1

    collect_nested_mounts "${root}"

    if [[ ${#CENCI_NESTED_MOUNTS[@]} -eq 0 ]]; then
        find "${root}" -mindepth 1 -exec chown -h "${uid}:${gid}" {} +
        return
    fi

    # -path takes a glob, so a mount point containing *, ? or [ would match
    # more loosely than intended. Nothing the launcher mounts under /home/dev
    # has such a name, and over-pruning would at worst leave a nested mount's
    # neighbour unowned rather than break startup — the failure mode this
    # whole helper exists to avoid.
    local prune=() p
    for p in "${CENCI_NESTED_MOUNTS[@]}"; do
        [[ ${#prune[@]} -gt 0 ]] && prune+=(-o)
        prune+=(-path "${p}")
    done

    # `-exec ... +` propagates the command's non-zero exit through find's own
    # status, so a failed chown outside the pruned mounts still surfaces.
    find "${root}" -mindepth 1 \( "${prune[@]}" \) -prune -o -exec chown -h "${uid}:${gid}" {} +
}
