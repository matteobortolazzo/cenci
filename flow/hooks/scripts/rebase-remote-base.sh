#!/bin/sh
# Refresh the remote base explicitly; local main is never a rebase authority.
# Leave conflicts intact for the owning agent. Autostash preserves Phase 9 edits.
set -eu
WORKTREE=${1:?usage: rebase-remote-base.sh <worktree> [base-branch]}
BASE_BRANCH=${2:-main}

fail() { echo "rebase-remote-base: $*" >&2; exit 1; }
git check-ref-format "refs/heads/$BASE_BRANCH" || fail "invalid base branch"
GIT_DIR=$(git -C "$WORKTREE" rev-parse --path-format=absolute --git-dir) || fail "cannot resolve git directory"
COMMON_DIR=$(git -C "$WORKTREE" rev-parse --path-format=absolute --git-common-dir) || fail "cannot resolve common directory"
[ "$GIT_DIR" != "$COMMON_DIR" ] || fail "use a linked feature worktree, never the main checkout"
BRANCH=$(git -C "$WORKTREE" symbolic-ref --quiet --short HEAD) || fail "detached HEAD or unfinished rebase; resolve before restarting"
[ "$BRANCH" != "$BASE_BRANCH" ] || fail "cannot rebase the base branch itself"
[ ! -d "$GIT_DIR/rebase-merge" ] && [ ! -d "$GIT_DIR/rebase-apply" ] || fail "rebase already in progress"
UNMERGED=$(git -C "$WORKTREE" diff --name-only --diff-filter=U) || fail "cannot read conflicts"
[ -z "$UNMERGED" ] || fail "unresolved conflicts remain: $UNMERGED"

# An explicit destination works even with a narrow/custom remote.origin.fetch.
REMOTE_REF="refs/remotes/origin/$BASE_BRANCH"
git -C "$WORKTREE" fetch origin "+refs/heads/$BASE_BRANCH:$REMOTE_REF" || fail "fetch failed; do not rebase onto a stale ref"
git -C "$WORKTREE" rebase --autostash "$REMOTE_REF" || fail "rebase failed; inspect and resolve in this worktree"
# Git can exit 0 when rebase succeeded but restoring the autostash conflicted.
UNMERGED=$(git -C "$WORKTREE" diff --name-only --diff-filter=U) || fail "cannot read conflicts after rebase"
[ -z "$UNMERGED" ] || fail "autostash restoration has conflicts: $UNMERGED; preserve the recovery stash"
git -C "$WORKTREE" merge-base --is-ancestor "$REMOTE_REF" HEAD || fail "remote base is not an ancestor of HEAD"
echo 'REBASE_STATUS=ready'
