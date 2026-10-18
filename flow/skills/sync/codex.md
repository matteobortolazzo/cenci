# Codex sync procedure

Read `project-core`, `codex-runtime`, `shell-rules`, and `worktrees`. Checkpoint the worktree
inventory, fetch in the root agent, rebase active feature worktrees without destructive
reset, prune only merged branches, and stop for conflicts with one blocking question.

The cenci-watch daemon also reclaims finished and stale worktrees automatically
(`-worktree-cleanup-interval`, default 10m), so this procedure is the manual fallback for
repos where that pass is disabled or not running.
