# Codex implement procedure

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent, ask material questions, and return an approved plan in the conversation without
writing files or labels. Stop before mutations and instruct `cenci run implement apply
<ticket> --agent codex`. In normal apply mode persist `.plans/<ticket>-<slug>.md`, initialize
the checkpoint, create the worktree, arm
the native goal, implement test-first, run the configured reviews, fix accepted findings,
capture lessons, commit, push, and open the PR. Clear the goal before any question/error and
after PR creation. Never force-push or bypass security/design/approval gates.
