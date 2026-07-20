# Codex implement procedure

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent, ask material questions, and return an approved plan in the conversation without
writing files or labels. Stop before mutations and instruct `cenci run implement apply
<ticket> --agent codex`. In normal apply mode persist `.plans/<ticket>-<slug>.md`, initialize
the checkpoint, create the worktree, then run the baseline gate: resolve affected projects
by matching the plan's `## Project Context` `# Project: <name>` headers against
`.cenci/config.json`'s `projects[].name` to get each `slug` (dedupe; a no-match prints a
one-line notice and no-ops, while an unset gate is a fully silent no-op), and for each
resolved target run `( cd "<abs-worktree-path>" && sh "${PLUGIN_ROOT}/hooks/scripts/run-gate.sh" "<slug>" )`.
`GATE_STATUS=green` or `GATE_STATUS=unset` → proceed.
`GATE_STATUS=red` → stop as **gate failed**, capturing the resolved slug (or "top-level") and
the command's output; a non-zero exit with no `GATE_STATUS=` line → stop as **gate could not
run** (a script/config error, e.g. missing `jq`, malformed config, bad project path), capturing
the same slug and output. Either stop: run `checkpoint.mjs block`, clear the goal, retain the worktree and branch, report
the distinct diagnostic plus its captured output, and tell the user to fix the baseline gate
and re-run `cenci run implement apply`.
Arm
the native goal, implement test-first, run the configured reviews, fix accepted findings,
capture lessons, run the maintenance check (`flow/skills/maintain/scripts/check.sh --changed`)
when the change touches docs, skills, agents, config, or client adapters — auto-repair only
drift caused by this same change and route any ambiguous or policy-affecting finding through
the client's available user-input mechanism — commit, push, and open the PR. Clear the goal
before any question/error and after PR creation. Never force-push or bypass
security/design/approval gates.
