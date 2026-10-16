# Selective model escalation

Keep the normal agent and skill model tiers. Use `domain-specialist` for a bounded,
read-only second opinion when one of the following triggers is supported by the
actual task or source evidence. A long diff, a large repository, or the name of a
skill alone is not a trigger. The specialist supplements the existing agent; it
does not replace required reviewers, verification, or the conversation owner.

## Triggers and timing

| Work | Escalate when | Invoke before |
|---|---|---|
| Refinement and planning | A cross-project change has competing architectural constraints, or a migration/recovery design has unresolved ordering, compatibility, or rollback semantics | Relaying the candidate's questions or accepting its proposal/plan |
| Security review | The diff changes sandbox boundaries, permissions, credential handling, command construction involving untrusted input, or autonomous approval logic | Accepting the review or acting on its findings |
| Silent-failure analysis | Evidence suggests validated config is not consumed, retries may duplicate mutations, or errors disappear across process boundaries | Accepting the review or applying a proposed fix |
| Implementation and CI repair | The same defect remains unexplained after two failed attempts, particularly concurrency or intermittent failures | Spending the next already-allowed fix attempt, or returning the unresolved diagnosis |
| Maintain and refactor synthesis | Completed audits contain conflicting findings whose resolution changes the proposed architecture or repair | Presenting recommendations for approval |

Run at most one specialist pass per phase for the same unresolved question; batch
overlapping security and silent-failure questions into that pass. Carry its result
through retries and resumed Q&A rather than re-running it for every reply. A changed
question needs new concrete evidence. Routine context gathering, implementation,
sync, polling, PR mechanics, and unambiguous audit consolidation stay on their
existing tiers. Do not deliberately fail twice to qualify for escalation.

## Dispatch

Read `subagent-safety` before dispatch. Keep the existing phase's normal agent calls
and output contracts. The conversation owner makes the specialist call after it has
the candidate output or failure evidence; an implementer/worker returns its evidence
to that owner instead of spawning its own specialist.

- **Claude Code:** use the `Task` tool with agent `domain-specialist`; its frontmatter
  pins `model: fable` and `effort: high` for the whole run. Do not pin the surrounding
  skill or change the session model. `CLAUDE_CODE_SUBAGENT_MODEL` overrides this
  frontmatter: if that override is set, preserve it and report that the specialist
  ran under the override, not that Fable ran. Do not change environment settings.
- **Codex:** use `agent_type="domain-specialist"`, whose installed role pins
  `gpt-6-astra` at high effort. If that role is not available, use a built-in worker
  with `fork_turns="none"`, `model="gpt-6-astra"`, and `reasoning_effort="high"`,
  explicitly supplying the specialist instructions from the role template plus the
  bounded context below. Do not override a fixed-model role or combine an explicit
  model with full-history inheritance. Honor an explicitly customized installed
  role and report its effective configured model rather than silently replacing it.
- **OpenCode:** delivery remains paused; no specialist integration or model-routing
  support is claimed. This policy is wired into Claude/Codex procedures only.

Pass the exact question, trigger evidence, absolute checkout/worktree path, affected
source paths, applicable guidance paths, settled decisions, candidate output, and
any failing-test/log paths and attempted fixes. Pass artifacts by path, not entire
conversation histories. The specialist has no write ownership: it must not change
files, run builds/tests, make network/authenticated calls, delegate, or ask user
questions. Claude uses Read/Grep/Glob only. Codex may use bounded read-only local
shell commands for file inspection when native file tools are unavailable; this
does not permit executing project scripts. The owner collects any missing test or
external-command output.

## Consume the result and handle failures

The owner must read the cited source before accepting a specialist recommendation.
Feed validated corrections back to the original refiner/planner or implementer as
appropriate, preserving that role's output schema and all settled decisions. Reviews
still require a concrete failure scenario; a more capable model is not evidence.
For synthesis, keep the original finding IDs, categories, and incomplete-audit status.
Record the trigger, requested model, override/fallback if any, and accepted or
discarded recommendation in the phase's existing summary or checkpoint; do not
invent a new checkpoint schema or post a separate GitHub comment.

Existing approval, sensitive-path, autonomy, retry, and stop rules take precedence.
Specialist advice never authorizes a product decision, security-posture change, new
scope, or GitHub mutation. It does not reset or extend a repair budget, suppress a
required question, or turn an incomplete audit into a successful one. At an existing
hard stop, return the diagnosis through the existing stop path; do not take another
fix attempt. A specialist cannot make an infrastructure failure count as repaired.

Do not retry a model-access failure or silently substitute a cheaper model. If the
role/model/tool is unavailable or the specialist errors, times out, or returns an
unsupported claim, report the specialist pass as unavailable/incomplete. Continue
the normal workflow only when its original evidence and gates independently permit
it; otherwise follow its existing escalation/stop path. No blanket extra approval
is required just to run this read-only analysis.

## Rationale and verification

Anthropic recommends [Fable for demanding reasoning and long-horizon work, or when
higher-effort Opus falls short](https://platform.claude.com/docs/en/models/fable-5-1/overview).
Official OpenAI documentation positions [Astra for the hardest work and Sol for a
balance of capability and cost](https://developers.openai.com/api/docs/guides/latest-model).
These are selection hypotheses, not measured cenci performance guarantees. Compare
actionable findings, plan corrections, elapsed time, and total cost against the
normal workflow before broadening these triggers.

`tests/model-escalation.test.sh` exercises real Codex role installation and asserts
the Markdown dispatch contracts. It does not exercise live model access or prove
that a model follows those contracts. Existing Codex role files are preserved by
the installer; running configure adds the missing specialist without overwriting
custom roles. A changed existing specialist file must be reconciled explicitly.
