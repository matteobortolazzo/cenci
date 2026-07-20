# Codex implement procedure

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent, ask material questions, and return an approved plan in the conversation without
writing files or labels. Stop before mutations and instruct `cenci run implement apply
<ticket> --agent codex`. In normal apply mode persist `.plans/<ticket>-<slug>.md`, initialize
the checkpoint, create the worktree, arm
the native goal, implement test-first, run the configured reviews, fix accepted findings,
capture lessons, run the maintenance check (`flow/skills/maintain/scripts/check.sh --changed`)
when the change touches docs, skills, agents, config, or client adapters — auto-repair only
drift caused by this same change and route any ambiguous or policy-affecting finding through
the client's available user-input mechanism — commit, push, and open the PR. Clear the goal
before any question/error and after PR creation. Never force-push or bypass
security/design/approval gates.
