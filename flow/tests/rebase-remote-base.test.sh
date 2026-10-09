#!/usr/bin/env bash
# Exercise the actual helper against local bare origins; no GitHub/network needed.
set -euo pipefail
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 2
SCRIPT="${TEST_DIR}/../hooks/scripts/rebase-remote-base.sh"
FIXTURE="$(mktemp -d)" || exit 2
trap 'rm -rf "$FIXTURE"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid

setup() {
  CASE_DIR="$FIXTURE/$1"
  mkdir -p "$CASE_DIR"
  git init -q --bare --initial-branch=main "$CASE_DIR/origin"
  git clone -q "$CASE_DIR/origin" "$CASE_DIR/main"
  printf 'initial\n' > "$CASE_DIR/main/shared"
  git -C "$CASE_DIR/main" add shared
  git -C "$CASE_DIR/main" commit -qm initial
  git -C "$CASE_DIR/main" push -q origin main
  git -C "$CASE_DIR/main" worktree add -q -b feature "$CASE_DIR/feature"
  git clone -q "$CASE_DIR/origin" "$CASE_DIR/upstream"
}

advance() {
  printf 'remote\n' > "$CASE_DIR/upstream/remote-file"
  git -C "$CASE_DIR/upstream" add remote-file
  git -C "$CASE_DIR/upstream" commit -qm advance
  git -C "$CASE_DIR/upstream" push -q origin main
}

for TEST_SHELL in sh bash; do
  setup "$TEST_SHELL-stale-main"
  LOCAL_MAIN="$(git -C "$CASE_DIR/main" rev-parse main)" || exit 2
  printf 'feature\n' > "$CASE_DIR/feature/feature-file"
  git -C "$CASE_DIR/feature" add feature-file
  git -C "$CASE_DIR/feature" commit -qm feature
  advance
  # A custom fetch mapping must not leave origin/main stale.
  git -C "$CASE_DIR/main" config remote.origin.fetch '+refs/heads/main:refs/remotes/custom/main'
  printf 'uncommitted\n' >> "$CASE_DIR/feature/feature-file"
  printf 'new implementation file\n' > "$CASE_DIR/feature/new-file"
  git -C "$CASE_DIR/feature" add new-file
  "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" main
  REMOTE_HEAD="$(git -C "$CASE_DIR/upstream" rev-parse HEAD)" || exit 2
  git -C "$CASE_DIR/feature" merge-base --is-ancestor "$REMOTE_HEAD" HEAD
  [[ "$(git -C "$CASE_DIR/main" rev-parse main)" == "$LOCAL_MAIN" ]]
  [[ "$(<"$CASE_DIR/feature/feature-file")" == $'feature\nuncommitted' ]]
  [[ -f "$CASE_DIR/feature/remote-file" ]]
  [[ "$(<"$CASE_DIR/feature/new-file")" == 'new implementation file' ]]

  setup "$TEST_SHELL-fetch-failure"
  BEFORE="$(git -C "$CASE_DIR/feature" rev-parse HEAD)" || exit 2
  git -C "$CASE_DIR/main" remote set-url origin "$CASE_DIR/missing"
  if "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" main; then
    echo 'FAIL: unavailable origin accepted' >&2; exit 1
  fi
  [[ "$(git -C "$CASE_DIR/feature" rev-parse HEAD)" == "$BEFORE" ]]

  setup "$TEST_SHELL-commit-conflict"
  printf 'feature\n' > "$CASE_DIR/feature/shared"
  git -C "$CASE_DIR/feature" commit -qam feature
  printf 'upstream\n' > "$CASE_DIR/upstream/shared"
  git -C "$CASE_DIR/upstream" commit -qam upstream
  git -C "$CASE_DIR/upstream" push -q origin main
  if "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" main; then
    echo 'FAIL: conflicting commit accepted' >&2; exit 1
  fi
  [[ -n "$(git -C "$CASE_DIR/feature" diff --name-only --diff-filter=U)" ]]

  setup "$TEST_SHELL-autostash-conflict"
  printf 'work in progress\n' > "$CASE_DIR/feature/shared"
  printf 'upstream\n' > "$CASE_DIR/upstream/shared"
  git -C "$CASE_DIR/upstream" commit -qam upstream
  git -C "$CASE_DIR/upstream" push -q origin main
  if "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" main; then
    echo 'FAIL: autostash application conflict accepted' >&2; exit 1
  fi
  [[ -n "$(git -C "$CASE_DIR/feature" diff --name-only --diff-filter=U)" ]]
  [[ -n "$(git -C "$CASE_DIR/feature" stash list)" ]]

  setup "$TEST_SHELL-main-refused"
  if "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/main" main; then
    echo 'FAIL: main checkout accepted' >&2; exit 1
  fi

  setup "$TEST_SHELL-non-main-base"
  git -C "$CASE_DIR/upstream" checkout -qb release/next
  advance
  git -C "$CASE_DIR/upstream" push -q origin release/next
  "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" release/next
  git -C "$CASE_DIR/feature" merge-base --is-ancestor origin/release/next HEAD
  [[ -f "$CASE_DIR/feature/remote-file" ]]

  setup "$TEST_SHELL-lease-race"
  printf 'feature\n' > "$CASE_DIR/feature/feature-file"
  git -C "$CASE_DIR/feature" add feature-file
  git -C "$CASE_DIR/feature" commit -qm feature
  git -C "$CASE_DIR/feature" push -q origin feature
  EXPECTED_HEAD="$(git -C "$CASE_DIR/feature" rev-parse HEAD)" || exit 2
  advance
  "$TEST_SHELL" "$SCRIPT" "$CASE_DIR/feature" main
  git -C "$CASE_DIR/upstream" fetch -q origin feature
  git -C "$CASE_DIR/upstream" checkout -qb feature FETCH_HEAD
  printf 'another writer\n' > "$CASE_DIR/upstream/concurrent-file"
  git -C "$CASE_DIR/upstream" add concurrent-file
  git -C "$CASE_DIR/upstream" commit -qm concurrent
  git -C "$CASE_DIR/upstream" push -q origin feature
  CONCURRENT_HEAD="$(git -C "$CASE_DIR/upstream" rev-parse HEAD)" || exit 2
  # Refreshing tracking refs must not relax the retained explicit lease.
  git -C "$CASE_DIR/feature" fetch -q origin feature
  if git -C "$CASE_DIR/feature" push --force-with-lease="refs/heads/feature:$EXPECTED_HEAD" origin HEAD:refs/heads/feature; then
    echo 'FAIL: concurrent writer overwritten' >&2; exit 1
  fi
  [[ "$(git -C "$CASE_DIR/origin" rev-parse refs/heads/feature)" == "$CONCURRENT_HEAD" ]]
done
echo 'rebase-remote-base: all integration cases passed under sh and bash'
