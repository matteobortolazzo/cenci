#!/usr/bin/env bash
# Host-runnable tests for sandbox/lib/remap.sh's chown_home_tree() (#1032).
#
# entrypoint.sh's root phase remaps the baked-in `dev` account (uid/gid 1000)
# onto the host user's HOST_UID/HOST_GID and then has to re-own the home
# volume so the remapped account can still write it. It used to do that with a
# bare recursive chown:
#
#     chown -R "${HOST_UID}:${HOST_GID}" /home/dev || { ...; exit 1; }
#
# That is fatal on any host whose git identity exists, because the launcher
# bind-mounts it READ-ONLY inside that very tree
# (watch/internal/sandbox/launcher/launch.go, assembleVolumeMounts):
#
#     -v <host gitconfig>:/home/dev/.gitconfig:ro
#
# `chown -R` issues the chown(2) syscall unconditionally — it does NOT skip
# entries whose ownership already matches the target — so it descends into the
# read-only bind mount, gets EROFS, and the `||` branch kills the container
# before the workload ever starts:
#
#     chown: changing ownership of '/home/dev/.gitconfig': Read-only file system
#
# chown_home_tree() fixes that by pruning every mount point nested under the
# home root, read from /proc/self/mountinfo rather than a hardcoded
# `.gitconfig` path — so a future read-only mount under /home/dev cannot
# reintroduce the same startup failure. Those nested mounts are host-owned by
# design; the workload only ever reads them, so their ownership is irrelevant
# to the remap.
#
# Host-runnable: no container and no root needed. `chown` is replaced by a
# recording fake on a scoped PATH (the same harness pattern as
# sandbox/tests/dind.test.sh and sandbox/tests/agent-cli.test.sh), so the
# assertions are about which paths the remap *hands to* chown — the actual
# behavior that broke — rather than about resulting inode ownership, which a
# non-root host user could not exercise anyway. /proc/self/mountinfo is
# redirected through the test-only CENCI_MOUNTINFO override (same convention
# as lib/dind.sh's CENCI_DIND_HOME_ROOT; production never sets it).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENTRYPOINT="${ROOT}/sandbox/entrypoint.sh"
REMAP_LIB="${ROOT}/sandbox/lib/remap.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/assert.sh
source "${SCRIPT_DIR}/lib/assert.sh"

echo "remap-chown.test.sh"

if [[ ! -f "${REMAP_LIB}" ]]; then
    fail "sandbox/lib/remap.sh does not exist"
    print_summary
    exit 1
fi

# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/remap.sh
source "${REMAP_LIB}"

# -- harness ----------------------------------------------------------------

CHOWN_LOG=""

# make_fake_chown [failing-path]
#   Installs a recording `chown` on a scoped PATH. Every invocation appends
#   one line per operand to ${CHOWN_LOG}, so the tests can assert exactly
#   which paths the remap tried to chown. When [failing-path] is given, the
#   fake exits 1 for the batch containing it — standing in for the EROFS the
#   real chown returns on a read-only bind mount.
make_fake_chown() {
    local bindir="$1" failing="${2:-}"
    mkdir -p "${bindir}"
    cat >"${bindir}/chown" <<EOF
#!/usr/bin/env bash
rc=0
for arg in "\$@"; do
    case "\${arg}" in
        -*) continue ;;
        *:*) continue ;;
    esac
    printf '%s\n' "\${arg}" >> "${CHOWN_LOG}"
    if [[ -n "${failing}" && "\${arg}" == "${failing}" ]]; then
        echo "chown: changing ownership of '\${arg}': Read-only file system" >&2
        rc=1
    fi
done
exit \${rc}
EOF
    chmod +x "${bindir}/chown"
}

# make_home_tree <root>: a representative /home/dev — ordinary files plus the
# two shapes a nested mount can take (a bind-mounted file, a bind-mounted
# directory with content beneath it).
make_home_tree() {
    local home="$1"
    mkdir -p "${home}/.claude" "${home}/mnt"
    touch "${home}/.bashrc" "${home}/.claude/settings.json" "${home}/.gitconfig" "${home}/mnt/inside"
}

# write_mountinfo <file> <mountpoint>...: a /proc/self/mountinfo fixture in the
# real 10+-field layout (mount point is field 5).
write_mountinfo() {
    local file="$1"; shift
    : >"${file}"
    local id=100 point
    for point in "$@"; do
        printf '%s 99 0:%s / %s rw,relatime - ext4 /dev/sda1 rw\n' \
            "${id}" "${id}" "${point}" >>"${file}"
        id=$((id + 1))
    done
}

# run_remap <bindir> <mountinfo> <root>
#   Calls chown_home_tree with the recording fake chown and the mountinfo
#   fixture scoped to this one invocation. The PATH/CENCI_MOUNTINFO edits are
#   deliberately confined to the subshell — no case may leak its fake chown or
#   its fixture into the next — so shellcheck's SC2030/SC2031 flag exactly the
#   property this harness wants, and are silenced here rather than worked
#   around. Every case routes through this one helper so the suppression lives
#   at a single site.
# shellcheck disable=SC2030,SC2031
run_remap() {
    local bindir="$1" mountinfo="$2" root="$3"
    (
        PATH="${bindir}:${PATH}"
        CENCI_MOUNTINFO="${mountinfo}"
        export PATH CENCI_MOUNTINFO
        chown_home_tree 502 20 "${root}"
    )
}

logged() { grep -Fxq "$1" "${CHOWN_LOG}"; }

assert_chowned() {
    if logged "$1"; then pass; else fail "expected chown of '$1', log:\n$(cat "${CHOWN_LOG}")"; fi
}

assert_not_chowned() {
    if logged "$1"; then fail "expected '$1' to be skipped, but it was chowned"; else pass; fi
}

# -- case 1: nested mount points are pruned, everything else is chowned ------

echo "case: read-only nested mounts are never handed to chown"
CASE1="${WORK}/case1"
HOME1="${CASE1}/home/dev"
make_home_tree "${HOME1}"
CHOWN_LOG="${CASE1}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE1}/bin"
write_mountinfo "${CASE1}/mountinfo" \
    "/" "/proc" "${HOME1}" "${HOME1}/.gitconfig" "${HOME1}/mnt" "${HOME1}-other/decoy"

run_remap "${CASE1}/bin" "${CASE1}/mountinfo" "${HOME1}"
CASE1_RC=$?

if [[ ${CASE1_RC} -eq 0 ]]; then
    pass
else
    fail "chown_home_tree returned ${CASE1_RC} on a tree whose only failures were pruned mounts"
fi

# The bind-mounted file and the bind-mounted directory (and everything under
# it) are the paths that made the real chown -R fatal.
assert_not_chowned "${HOME1}/.gitconfig"
assert_not_chowned "${HOME1}/mnt"
assert_not_chowned "${HOME1}/mnt/inside"

# The home root itself is a mount point too (the named home volume), but it is
# the target of the remap, not a nested mount — it must still be chowned.
assert_chowned "${HOME1}"
assert_chowned "${HOME1}/.bashrc"
assert_chowned "${HOME1}/.claude"
assert_chowned "${HOME1}/.claude/settings.json"

# -- case 2: a sibling path that merely shares the root's prefix -------------

echo "case: a mount whose path only shares the home root's prefix is not treated as nested"
CASE2="${WORK}/case2"
HOME2="${CASE2}/home/dev"
make_home_tree "${HOME2}"
mkdir -p "${HOME2}-other"
touch "${HOME2}-other/decoy"
CHOWN_LOG="${CASE2}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE2}/bin"
write_mountinfo "${CASE2}/mountinfo" "${HOME2}-other/decoy"

run_remap "${CASE2}/bin" "${CASE2}/mountinfo" "${HOME2}"

# Nothing under the home root is a mount here, so the whole tree is chowned —
# a prefix-only match must not silently prune a real home entry.
assert_chowned "${HOME2}/.gitconfig"
assert_chowned "${HOME2}/mnt/inside"

# -- case 3: octal-escaped mount points decode before matching ---------------

echo "case: octal-escaped mount points from mountinfo are decoded before pruning"
CASE3="${WORK}/case3"
HOME3="${CASE3}/home/dev"
make_home_tree "${HOME3}"
mkdir -p "${HOME3}/my mount"
touch "${HOME3}/my mount/inside"
CHOWN_LOG="${CASE3}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE3}/bin"
write_mountinfo "${CASE3}/mountinfo" "${HOME3}/my\\040mount"

run_remap "${CASE3}/bin" "${CASE3}/mountinfo" "${HOME3}"

assert_not_chowned "${HOME3}/my mount"
assert_not_chowned "${HOME3}/my mount/inside"
assert_chowned "${HOME3}/.bashrc"

# -- case 4: unreadable mountinfo falls open to the full tree ----------------

echo "case: an unreadable mountinfo chowns the whole tree rather than pruning blindly"
CASE4="${WORK}/case4"
HOME4="${CASE4}/home/dev"
make_home_tree "${HOME4}"
CHOWN_LOG="${CASE4}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE4}/bin"

run_remap "${CASE4}/bin" "${CASE4}/does-not-exist" "${HOME4}"

assert_chowned "${HOME4}"
assert_chowned "${HOME4}/.gitconfig"
assert_chowned "${HOME4}/.claude/settings.json"

# -- case 5: a genuine chown failure still fails loudly ----------------------

echo "case: a chown failure outside any nested mount still returns non-zero"
CASE5="${WORK}/case5"
HOME5="${CASE5}/home/dev"
make_home_tree "${HOME5}"
CHOWN_LOG="${CASE5}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE5}/bin" "${HOME5}/.claude/settings.json"
write_mountinfo "${CASE5}/mountinfo" "${HOME5}" "${HOME5}/.gitconfig"

run_remap "${CASE5}/bin" "${CASE5}/mountinfo" "${HOME5}" 2>/dev/null
CASE5_RC=$?

if [[ ${CASE5_RC} -ne 0 ]]; then
    pass
else
    fail "chown_home_tree swallowed a real chown failure (returned 0) — genuine remap breakage must stay fatal"
fi

echo "case: the home root itself failing to chown returns non-zero"
CASE6="${WORK}/case6"
HOME6="${CASE6}/home/dev"
make_home_tree "${HOME6}"
CHOWN_LOG="${CASE6}/chown.log"; : >"${CHOWN_LOG}"
make_fake_chown "${CASE6}/bin" "${HOME6}"
write_mountinfo "${CASE6}/mountinfo" "${HOME6}"

run_remap "${CASE6}/bin" "${CASE6}/mountinfo" "${HOME6}" 2>/dev/null
CASE6_RC=$?

if [[ ${CASE6_RC} -ne 0 ]]; then
    pass
else
    fail "chown_home_tree returned 0 even though the home root itself could not be chowned"
fi

# -- case 7: entrypoint.sh actually uses it ---------------------------------

echo "case: entrypoint.sh sources lib/remap.sh and remaps the home volume through chown_home_tree"
if grep -q 'lib/remap\.sh' "${ENTRYPOINT}"; then pass; else fail "entrypoint.sh does not source lib/remap.sh"; fi
# shellcheck disable=SC2016 # literal call site we grep for in entrypoint.sh, not expanded here
if grep -q 'chown_home_tree "\${HOST_UID}" "\${HOST_GID}" /home/dev' "${ENTRYPOINT}"; then
    pass
else
    fail "entrypoint.sh does not remap /home/dev through chown_home_tree"
fi

echo "case: entrypoint.sh no longer runs a bare recursive chown over /home/dev"
if grep -qE 'chown -R [^|]*/home/dev' "${ENTRYPOINT}"; then
    fail "entrypoint.sh still runs 'chown -R ... /home/dev', which fails with EROFS on the read-only .gitconfig bind mount"
else
    pass
fi

# -- case 8: the Playwright browser cache follows the remap ------------------
#
# The cache is owned by `dev` at build time (#1096) so a project-local
# `playwright install` can add the Chromium build its pinned @playwright/test
# needs. That ownership is recorded by uid, not by name, so a remapped `dev`
# loses write access to it unless the remap re-owns it too — reintroducing
# exactly the failure the build-time chown fixes, on every host whose user is
# not uid/gid 1000.

echo "case: entrypoint.sh re-owns the Playwright browser cache through chown_home_tree"
# shellcheck disable=SC2016 # literal call site we grep for in entrypoint.sh, not expanded here
if grep -q 'chown_home_tree "\${HOST_UID}" "\${HOST_GID}" /ms-playwright' "${ENTRYPOINT}"; then
    pass
else
    fail "entrypoint.sh does not re-own /ms-playwright during the remap — a non-1000 host user cannot write the browser cache"
fi

echo "case: the /ms-playwright re-own is guarded on the directory existing"
if grep -q '\[\[ -d /ms-playwright \]\]' "${ENTRYPOINT}"; then
    pass
else
    fail "entrypoint.sh does not guard the /ms-playwright re-own on the directory existing — per-repo images built without the playwright fragment have no such path"
fi

echo "case: a failed /ms-playwright re-own warns instead of killing the container"
# shellcheck disable=SC2016 # literal call site we grep for in entrypoint.sh, not expanded here
MSPW_LINE="$(grep -n -A2 -F 'chown_home_tree "${HOST_UID}" "${HOST_GID}" /ms-playwright' "${ENTRYPOINT}" || true)"
if [[ -z "${MSPW_LINE}" ]]; then
    fail "could not locate the /ms-playwright re-own in entrypoint.sh"
elif [[ "${MSPW_LINE}" == *"exit 1"* ]]; then
    fail "a failed /ms-playwright re-own exits the container; a stale browser cache degrades verify-ui, it does not make the sandbox unusable"
elif [[ "${MSPW_LINE}" == *"warning:"* ]]; then
    pass
else
    fail "a failed /ms-playwright re-own is silent — it must warn so a broken browser cache is diagnosable"
fi

echo "case: the remap failure message does not blame the home volume's age"
REMAP_FAIL_LINE="$(grep -F 'remap: chown /home/dev failed' "${ENTRYPOINT}" || true)"
if [[ -z "${REMAP_FAIL_LINE}" ]]; then
    fail "could not locate the remap chown failure message in entrypoint.sh"
elif [[ "${REMAP_FAIL_LINE}" == *"prune --volumes"* ]]; then
    fail "the remap failure message still points at 'cenci sandbox prune --volumes', which cannot fix a read-only nested mount"
else
    pass
fi

print_summary
[[ "${FAILURES}" -eq 0 ]]
