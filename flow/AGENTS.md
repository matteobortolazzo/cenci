# Project: flow

Claude Code plugin — Markdown skills, JSON config, shell hooks.
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- ALWAYS read relevant `docs/` files when working in their topic area (e.g., `docs/git-workflow.md` before commits/PRs).
- No secrets, credentials, API keys, PII, or stack traces in code or user-facing error responses.
- Interactive (Claude-only) skills must route every user question/confirmation through `AskUserQuestion` and never say a bare "ask the user"; cross-tool-portable skills use abstract wording instead (e.g., "the client's available user-input mechanism").
- Shared temp files written by phases or agents must be uniquely scoped by worktree path, run ID, or session UUID — never a fixed path. See `docs/pipeline-safety.md`.
- Multi-phase pipeline safety: a mandatory-restart rule must document recovery/idempotency for every downstream step, and any safety rule reused on a new automated path must be re-evaluated for its new risk profile. See `docs/pipeline-safety.md`.
- Read `docs/shell-scripting-gotchas.md` before writing verification-critical shell commands, jq fallback chains, or grep-based contract tests.
- Every flow-posted comment opens with a blockquoted cenci attribution banner; every flow-posted **issue** comment also carries a distinct `<!-- cenci-<kind> -->` marker on its own non-blockquoted line. See `docs/comment-attribution.md`.

## Build & Test

- No build step (Markdown skills, JSON config, shell hooks).
- Tests + JSON validation: `bash scripts/run-checks.sh` (run from `flow/`) — the single entry point shared by CI's `flow-test` job and this repo's flow `gateCommand`.

## Reference Docs
CLI grammar, alias, env-var, and naming conventions: `<repo-root>/docs/cli-conventions.md`.
On-demand topic docs live at `docs/`:
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- `docs/skill-authoring.md` — writing skills that generate/regenerate files, especially with external-sourced values
- `docs/ticket-sizing.md` — how tickets are sized against the ~200k agent context budget and when to split
- `docs/pipeline-safety.md` — restart/recovery, risk-profile re-evaluation, and shared-temp-file scoping rules for multi-phase pipelines
- `docs/shell-scripting-gotchas.md` — narrow shell/jq/grep pitfalls (CWD persistence, jq fallback semantics, contract-test markers)
- `docs/adapter-contract.md` — the 8-property behavioral-parity contract client adapters (Claude Code, Codex) must satisfy, and its enforcement points
- `docs/followup-triage.md` — the `Followup` capture-queue invariant and the `/cenci:maintain backlog` consolidation (merge/batch/supersede) mechanics
- `docs/comment-attribution.md` — the cenci attribution banner and `<!-- cenci-<kind> -->` marker convention every flow-posted comment follows, its `<kind>` registry, and why the marker must never be blockquoted

`.claude/rules/` is reserved for files explicitly `@`-imported by this AGENTS.md (auto-loaded at session start). It is not used today.
