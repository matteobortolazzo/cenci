# Config Schema

Read this file on every run, from step 6 in `phases/write-config.md` (see `SKILL.md` `### Step Map`), before `.cenci/config.json` is written. It holds the complete `.cenci/config.json` examples and the field-by-field schema of every field the file can carry, closing with the migration-removal list and the `mcpServers`/`lspServers` inclusion rules. The `sandbox` field's merge into the config is described in `phases/write-config.md`.

```json
{
  "configVersion": "0.22.0",
  "branchPattern": "feature/<id>-<description>",
  "stack": {
    "backend": "dotnet10",
    "frontend": "angular21",
    "testing": ["xunit", "jasmine"]
  },
  "guidanceLocation": "AGENTS.md",
  "mcpServers": {
    "context7": true,
    "angular": false,
    "primeng": false
  },
  "lspServers": {
    "typescript": true,
    "csharp-ls": true
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "cenci": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "implementerConcurrency": "parallel",
    "implementerMaxConcurrency": 2,
    "diffContextMode": "inline",
    "liteReviewEnabled": true,
    "planComment": false,
    "gateOutputLines": 120
  },
  "planning": { "autonomy": "interactive" },
  "refinement": { "autonomy": "interactive" },
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  },
  "sandbox": {
    "enabled": true,
    "dind": true,
    "azure": true
  },
  "lazyboards": {
    "enabled": true,
    "serveCommand": "ng serve",
    "boardKey": "S"
  }
}
```

(The `planning` and `refinement` values shown above are illustrative of the schema, not something this skill
writes by default: question 13 (`### Autonomy Settings` in `phases/autonomy-settings.md`) writes `planning.autonomy` only when you explicitly choose `lean` (or switch under `--all`), and question 15 (`### Autonomy Settings` in `phases/autonomy-settings.md`) writes `refinement.autonomy` only when you explicitly choose `lean` (or switch under `--all`); a `planning` or `refinement` key already present in `existingConfig` is left verbatim
on a delta run; `--all` re-offers it. Declining — keeping `interactive` — writes nothing, since a missing key already resolves to `interactive`.)

`configVersion` records the flow plugin version that last wrote this config (managed by this skill — always overwritten with the current plugin version by step 6 in `phases/write-config.md`, never a question). Consumers:
- the plugin's SessionStart hook (`hooks/scripts/check-config-staleness.sh`) — nudges the user at session start when the stamp's major.minor is behind the installed plugin (or missing entirely), so they learn a `/cenci:configure` re-run would pick up new configure features without watching the changelog;
- maintain's `config-version` check (`/cenci:maintain`, via the same staleness resolver) — reports the same staleness as an advisory `warn`.

Both consumers name the unconditional questions a re-run would ask, computed by `scripts/resolve-delta.sh` from the committed `question-keys.json` table (tested by its own `scripts/resolve-delta.test.sh`). The configure flow calls this resolver itself — see `### Delta Resolution` in `phases/detection-and-delta.md`.

Patch-only drift is deliberately silent: configure features that warrant a re-run land in minor (`feat`) or major bumps per the plugin-version-bump workflow. Do **not** add `configVersion` to the migration-removal list below — it is a managed field, not a legacy one.

The `cenci` field is optional. If present, preserve existing user values during reconfiguration. Schema:
- `compactImplementation` — exactly `true` allows small S, low-risk tickets with one delegated lane to combine red/green/refactor into one implementer subagent turn while preserving all TDD/reporting gates, lint, reuse checks and UI evidence. Default: `false`; keep an existing repository opt-in on reconfiguration.
- `reviewConcurrency` — `"parallel"` runs security, code, and silent-failure reviews together; `"sequential"` runs the same reviews one after another to smooth usage limits. Default: `"parallel"`.
- `implementerConcurrency` — `"parallel"` (default) runs eligible planner-declared `### Parallel Lanes` within each dependency wave; `"sequential"` runs one worker per preflight lane in wave order. The planner always records a `### Parallelism Assessment`; missing/unsafe declarations stay sequential. Quality gates are identical, including UI/sensitive restrictions and one combined verification barrier after all waves. The setting never creates lanes on its own.
- `implementerMaxConcurrency` — positive integer, default `2`, limiting simultaneous implementers further to available worker slots. Invalid values warn and use `2`; sequential mode always uses one. Preserve a configured value on reconfiguration. Shared foundations can precede independent consumers, but every file has one owner across all waves and consumers only read foundation files.
- `diffContextMode` — `"inline"` passes small diffs directly to reviewers; `"file"` writes the diff to this implement run's artifact directory (`$RUN_DIR/diff.patch` — see implement Phase 6 + 7) and passes paths so reviewers read targeted hunks. Default: `"inline"`.
- `liteReviewEnabled` — `true` (default) lets Phase 6 + 7 classify each diff into `full` (all three reviewers), `lite-docs` (no reviewers, docs-only), or `lite-small` (`code-reviewer` only, small config/data-only diffs); `false` forces the full trio on every run regardless of diff size or content. See Phase 6 + 7 for the precedence-ordered classification rules.
- `planComment` — `true` makes implement Phase 1 also post the saved plan as a ticket comment (ticket mode only) right before marking the ticket `Planned` (the label call stays the last ticket edit — it records the plan-freshness baseline), for audit / off-host visibility; `.plans/` stays the executable source of truth. Default: `false` (no comment).
- `gateOutputLines` — the number of trailing lines of a gate command's combined stdout+stderr that `hooks/scripts/run-gate.sh` prints (see `docs/health-gates.md`); the full untruncated output stays retrievable at `GATE_LOG` on a red gate. Default: `120`.

Configure always writes `sandbox: { "enabled": false }` in `.claude/settings.json` (no `network`/`excludedCommands`/`autoAllowBashIfSandboxed`) — the cenci-sandbox container is the security boundary. The config no longer carries `profile` or `sandboxEnabled` fields; re-config strips them from older configs (see the migration-removal list below).

Optional external usage reducer: RTK (`https://github.com/rtk-ai/rtk`) can compress shell command output before it reaches Claude Code. It is not required for cenci and should not be installed automatically, but it is worth recommending when users are hitting usage limits from command-heavy sessions. After separate installation, `rtk init -g` enables Claude Code Bash command rewriting where supported. Built-in tools like `Read`, `Grep`, and `Glob` do not pass through RTK hooks.

The `cicd` field is only present when the user selected Yes in question 8. Schema:
- `cicd.enabled` — `true` if user opted in, omit `cicd` entirely if declined
- `cicd.platform` — `"github-actions"`

Omit `cicd` entirely when the user says No.

The `sandbox` field carries three **independently toggled** sub-answers (questions 9, 9b and 9c). Its schema, its merge into `existingConfig` and its outcomes are in the `sandbox` merge section of `phases/write-config.md`.

The `lazyboards` field is present when question 10 was answered Yes **or** a
`.lazyboards.yml` already existed (the suggest-or-skip branch also records
`enabled: true`). Schema:
- `lazyboards.enabled` — `true` if a board config exists (generated or pre-existing); omit `lazyboards` entirely when the user declines question 10 and no file exists (same pattern as `cicd`/`sandbox`)
- **Single project**: `lazyboards.serveCommand` + `lazyboards.boardKey` record the generated serve action, and `lazyboards.testCommand` + `lazyboards.testKey` record the generated test action (command and its key — a single letter, or a multi-key mnemonic sequence like `"S b"`/`"T f"` in a monorepo). Omit the test pair when the project is not testable. `W` (Open worktree) is never recorded here — it carries no command and isn't project-specific.
- `boardKey`/`testKey` are written in lazyboards' **canonical space-separated** sequence form (`"S b"`, `"T f"`) — the same spelling the generated `.lazyboards.yml` uses. A concatenated value read from `existingConfig` (`"Sb"`, `"Tf"`) is the **same key**, not a different one: normalize it to the space-separated form on write rather than treating it as a distinct binding or as a key collision. A single-letter key (`"S"`, `"T"`) is already canonical and is written unchanged.
- **Monorepo**: `serveCommand`/`boardKey` and `testCommand`/`testKey` live on each project entry in the `projects` array instead (a project gets the serve pair only when runnable and the test pair only when testable), and the top-level `lazyboards` field carries only `enabled`
- These recorded values are advisory: the suggest-or-skip analyzer re-derives serve/test commands from the derivation tables in `### Board Config (lazyboards)` in `phases/lazyboards.md`, so a config missing them still works.

Omit `lazyboards` entirely when the user says No (same pattern as `cicd`/`sandbox`).

The `security` field is optional and is **never written by a configure prompt** — there is
no question for it. It is a manually-editable escape hatch that lets a project extend
implement's Trivial-Ticket Triage sensitive-path backstop (see `skills/implement/SKILL.md`,
"Sensitive-path backstop"). Schema:
- `security.sensitivePaths` — an array of glob-pattern strings. Each pattern is matched
  whole-path / substring-style (`*` matches any characters including `/`, case-insensitive)
  against the file path(s) a trivial-candidate ticket names; a match disqualifies the ticket
  from the trivial fast path and forces full planning.

These entries are **additive to** implement's built-in default pattern list (`*auth*`,
`*payment*`, `*billing*`, `*migrat*`, `*secret*`, `*credential*`, `*oauth*`, `*token*`, and
more) — they extend it and never replace it, so the defaults apply even when `security` is
absent. Add patterns here only to cover project-specific sensitive areas the defaults miss
(e.g. a domain-specific module name).

Because the config write in `phases/write-config.md` uses merge semantics (step 6 — start from `existingConfig` and
overwrite only the fields the skill manages), a hand-added `security` block is **preserved
untouched** across re-configuration. Do **not** add `security` to the migration-removal list
below — it is a supported optional field, not a legacy one.

The `planning` field is optional. `### Autonomy Settings` question 13 in `phases/autonomy-settings.md` asks for it only
when absent, defaulting to `interactive`. A key already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it with the committed value pre-selected. Merge semantics (step 6 in `phases/write-config.md`) preserve a
hand-added or previously-written value untouched across reconfiguration unless you choose a switch under `--all`. It is a
safety/checkpoint toggle read by the implement skill's Phase 1 (see `skills/implement/SKILL.md`
and `skills/implement/phases/phase-1-plan.md`'s `## Lean Approval Path`). Schema:
- `planning.autonomy` — `"lean"` or `"interactive"`, default `"interactive"`. Only the exact
  string `"lean"` changes behavior — a missing `planning` block, a missing `autonomy` key, or
  any other value all resolve to `"interactive"` (the planner resolves repo-grounded choices,
  asks remaining consequential or unresolved questions via `AskUserQuestion`, and stops for
  human plan review). Both modes use `agents/planner.md`'s `## Self-Answer Policy`; in `"lean"`, the planner self-resolves everything except
  its three stop classes (security-sensitive, destructive or irreversible, scope blowup), and
  decides the contradiction and product-ambiguity questions it can justify from the repo
  (recorded as `decided:` entries, announced in one ticket comment and the PR body; a
  contradiction or ambiguity with exactly one grounded resolution is still self-resolved
  silently), and a plan persisted with no escalations — including an
  `L` estimate carrying a reasoned `None — <rationale>` no-split verdict — is implicitly
  approved and continues straight into Phase 2 in the same session.

  `planning.autonomy` has a second consumer besides `skills/implement/phases/phase-1-plan.md`:
  `cenci dispatch`'s pickup gate (`watch/internal/dispatch/autonomy.go`) reads this same
  `.cenci/config.json` field to authorize an unattended planning pickup or autonomous
  re-plan (`dispatch.planRefined: true`) — only the exact string `"lean"` authorizes
  either consumer; both default-deny identically on a missing block, missing key, or any
  other value. Unlike the implement-side consumer above (which reads the local working
  tree), the dispatch-side consumer reads this field from the remote-confirmed
  `refs/remotes/origin/main` object, never local `HEAD` or the working tree, and only
  when that pass's `git fetch origin` actually succeeded (#877) — a committed-but-unpushed
  `planning` block on local `main` can never authorize unattended planning, and a fetch
  failure holds the gate with a distinct retryable reason rather than falling back to
  stale local state. See [`watch/README.md`'s Planning pickup and autonomous
  re-plan](../../../../watch/README.md#planning-pickup-and-autonomous-re-plan) for the
  dispatch-side gate.

Because the config write in `phases/write-config.md` uses merge semantics, a hand-added `planning` block is
**preserved untouched** across re-configuration, exactly like `security`. Do **not** add
`planning` to the migration-removal list below — it is a supported optional field, not a
legacy one.

The `refinement` field is optional. `### Autonomy Settings` question 15 in `phases/autonomy-settings.md` asks for it only when absent
(recommending `lean`, while an absent key still resolves to `interactive`). A key already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it with the committed value pre-selected. Merge semantics (step 6 in `phases/write-config.md`) preserve a
hand-added or previously-written autonomy untouched across reconfiguration unless you choose a switch under `--all`; independent leaf answers preserve its siblings.
`refinement.autonomy` is consumed by `skills/refine/SKILL.md`. Schema:
- `refinement.autonomy` — `"lean"` or `"interactive"`, default `"interactive"`. Only the exact
  string `"lean"` changes behavior — a missing `refinement` block, a missing `autonomy` key, or
  any other value all resolve to `"interactive"` (questions only when consequential or unresolved). In `"lean"`,
  `/cenci:refine` asks one round of at most 3 questions and records grounded decisions
  as A1…An in the refinement summary. Resolved questionnaires apply immediately unless
  `refinement.confirmBeforeApply` explicitly requires confirmation.

Unlike `planning.autonomy`, it is read from the local `.cenci/config.json` through `project-core`, never from `origin/main`, and no fleet switch exists for it, so there is no dispatch-side consumer. Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently. Because the config write in `phases/write-config.md` uses merge semantics, a hand-added `refinement` block is
**preserved with carried siblings untouched** across re-configuration; questions 16 and 17 update only their answered leaves. Do **not** add `refinement` to the migration-removal list below — it is a supported optional field, not a legacy one.

Two independent optional leaves share that local `refinement` object:

- `refinement.confirmBeforeApply` — boolean, default `false`. False applies once the
  questionnaire is resolved; true retains the final confirmation. Invalid values
  resolve to true with a notice. Question 16 can write either boolean explicitly.
- `refinement.automerge` — `"off"` or `"eligible"`, default `"off"`. Invalid values
  resolve to off with a notice. Question 17 controls grants for eligible tickets,
  independently of both planning and refinement question modes. This is separate
  from the top-level `automerge` risk policy and the host's `automerge.enabled` flag.

**Monorepo config** — when `isMonorepo` is true, add `isMonorepo` and `projects` fields:

```json
{
  "configVersion": "0.22.0",
  "isMonorepo": true,
  "projects": [
    {
      "slug": "api",
      "path": "packages/api",
      "name": "API",
      "description": "REST API backend",
      "stack": { "framework": "dotnet10", "testing": "xunit" },
      "buildCommand": "dotnet build",
      "testCommand": "dotnet test",
      "lintCommand": "dotnet format --verify-no-changes",
      "gateCommand": "dotnet build && dotnet test",
      "serveCommand": "dotnet run",
      "boardKey": "S b",
      "testKey": "T b"
    },
    {
      "slug": "web-client",
      "path": "apps/web-client",
      "name": "Web Client",
      "description": "Angular frontend",
      "stack": { "framework": "angular21", "testing": "jasmine" },
      "buildCommand": "npm run build",
      "testCommand": "npm test",
      "lintCommand": "ng lint",
      "gateCommand": "npm run build && npm test -- --watch=false",
      "serveCommand": "ng serve",
      "boardKey": "S f",
      "testKey": "T f"
    }
  ],
  "lazyboards": {
    "enabled": true
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  },
  "sandbox": {
    "enabled": true
  }
}
```

Each project entry's `lintCommand` is optional — it is omitted for stacks with no Lint row in the Stack-to-CI mapping table (e.g. `markdown-shell`, `docker-shell`), the same way `buildCommand`/`testCommand` are already handled for stacks without one.

Each project entry's `gateCommand` is optional — unlike `lintCommand`, its presence isn't tied to the Stack-to-CI mapping table; it may simply be omitted for any project. Like the other `<verb>Command` fields, its value is executed as a shell command, so it must come only from trusted project configuration, never from untrusted input.

`babysitInterval` is an optional field — there is **no configure question for it**; it is a manually-editable escape hatch, like `security` above, and merge semantics (step 6 in `phases/write-config.md`) preserve a hand-added value untouched across reconfiguration. It sets the polling cadence for the `cenci babysit` supervisor that implement Phase 9 auto-launches (and that the standalone `babysit` skill uses), resolved the same way as `gateCommand` — a top-level `babysitInterval` for a single-project repo, or a per-`projects[]` `babysitInterval` in a monorepo. Its value is a Go duration string (e.g. `"15m"`, `"30m"`, `"1h"`); when unset (or when no config file exists) `cenci babysit` falls back to its built-in `15m` default. Both forms are valid:

```json
{ "babysitInterval": "20m" }
```

```json
{ "isMonorepo": true, "projects": [ { "slug": "api", "path": "packages/api", "babysitInterval": "5m" } ] }
```

Existing single-project configs (no `isMonorepo` field) work unchanged.

The `automerge` field is optional. `### Autonomy Settings` question 14 in `phases/autonomy-settings.md` offers to
scaffold a conservative starter block only when absent (top-level or per-`projects[]`).
A block already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it ("Keep current block(s) (Recommended)" or "Remove the block(s)"). Merge semantics (step 6 in `phases/write-config.md`) preserve a hand-added or
previously-scaffolded value untouched across reconfiguration unless "Remove the block(s)" is chosen under `--all`. Do **not** add
`automerge` to the migration-removal list below — it is a supported optional field, not a
legacy one.

`automerge` is split across two config locations, each with a distinct scope and default:

- `automerge.enabled` — the fleet-wide kill switch, read from `~/.config/cenci/config.json`
  (or `$XDG_CONFIG_HOME/cenci/config.json`), **not** `.cenci/config.json`. Default `false`;
  the `cenci babysit` supervisor never evaluates automerge for any repo until this is
  explicitly set `true`.
- A per-repo `automerge` block in this repo's `.cenci/config.json` — top-level for a
  single-project repo, or per-`projects[]` entry in a monorepo (falling back to the
  top-level block when a project sets none). Schema:
  - `automerge.protectedPaths` — an array of glob-pattern strings (`*` matches any
    characters including `/`, case-insensitive, same as `security.sensitivePaths`).
    Unlike `security.sensitivePaths`, each pattern here is anchored against the
    **whole repo-relative path from the root** (not substring-matched), and a pattern
    ending in `/` with no trailing `*` matches that directory and everything under it
    — see `docs/autonomous-loop.md`. A changed file matching any pattern denies
    automerge for that PR. Absent means an empty denylist — a genuine "nothing is
    protected" statement, not a malformed block.
  - `automerge.maxChangedFiles` — required whenever an `automerge` block is present; a
    missing or non-positive value makes the whole block malformed (denied), it does **not**
    fall back to a built-in default.
  - `automerge.maxDiffLines` — required on the same terms as `maxChangedFiles` (additions +
    deletions combined). Both thresholds are the **S bound** of the ticket-size table in
    `flow/docs/ticket-sizing.md` (`10` files / `400` lines) and stay there: S is the only
    automergeable size, and the thresholds and the table change together.
  - `automerge.mergeMethod` — one of `"squash"`, `"merge"`, `"rebase"`; defaults to
    `"squash"` when omitted. The field stays readable for configuration compatibility, but
    only `"squash"` is ever executed at merge time: a `"merge"` or `"rebase"` value produces
    its own logged policy hold instead of being validated against the repo's allowed merge
    methods or executed, and is never a retry loop.

In a monorepo, a changed file is resolved to the `projects[]` entry with the longest
matching `path` prefix; that project's own `automerge` block applies, falling back to the
top-level block when the project sets none, and to the top-level block directly for files
owned by no project. When a PR's changed files span more than one applicable block, the
effective policy is the **most restrictive** merge: the minimum of each numeric cap and the
union of `protectedPaths` across every block actually touched. **Any required block
missing is a deny** — an `.cenci/config.json` that is unreadable, not valid JSON, or simply
has no applicable `automerge` key (top-level or per-project) denies automerge for that PR;
there is no fallback to built-in thresholds. This means enabling `automerge.enabled`
fleet-wide with only one `projects[]` entry's `automerge` block defined immediately denies
every PR touching files outside that project — expected, not a bug, until a top-level
block (or per-project blocks for the other projects) is added.

The per-repo block is always read from the **PR's base branch**, never the PR's own head
branch or worktree — this is deliberate: reading policy from the head ref would let a PR
widen its own `protectedPaths`/caps and self-approve, exactly the fail-open hole this
feature exists to close.

See the `automerge:ok` row of the plugin's `docs/board-labels.md` for the complementary per-ticket grant —
`.cenci/config.json`'s `automerge` block is the repo-wide risk *policy* (paths, size caps,
merge method); `automerge:ok` is the per-ticket *grant* refinement adds under
`refinement.automerge: "eligible"` when eligibility checks pass. `refinement.autonomy`
and `planning.autonomy` never imply that grant; there is no `automerge.defaultRisk`
setting. Both policy and grant must hold, alongside green CI and a mergeable PR, before
`cenci babysit` will merge.

The `maintenance` field is optional and is **never written by a configure prompt** —
there is no question for it. It is a manually-editable escape hatch, like `security`
above, and merge semantics (step 6 in `phases/write-config.md`) preserve a hand-added value untouched across
reconfiguration. Do **not** add `maintenance` to the migration-removal list below — it
is a supported optional field, not a legacy one.

Maintenance is a core cenci workflow feature and is independent of
[lazyboards](https://github.com/matteobortolazzo/lazyboards): automatic changed-file
maintenance checks run during normal `/cenci:implement` runs, with no `.lazyboards.yml`
or `lazyboards` config required. A full, on-demand audit is always available via
`/cenci:maintain`.

Schema — every field below defaults as shown, so omitting `maintenance` entirely is
equivalent to writing all four defaults explicitly:
- `maintenance.enabled` — gates optional scheduled-maintenance and reminder UX only
  (default `true`); it never disables core correctness checks
- `maintenance.checkDuringImplement` — whether Phase 8 may repair findings
  automatically (default `true`); when `false`, Phase 8 still runs its changed-file
  correctness check but carries every non-pass result forward as report-only
- `maintenance.remindAfterDays` — days between reminders to run a full
  `/cenci:maintain` audit (default `30`)
- `maintenance.generatedDocs` — whether marker-bounded generated documentation sections are checked and regenerated
  (default `true`); when `false`, generated-section maintenance is skipped without
  suppressing other checks

```json
{
  "maintenance": {
    "enabled": true,
    "checkDuringImplement": true,
    "remindAfterDays": 30,
    "generatedDocs": true
  }
}
```

Core correctness checks always run when the maintenance checker is applicable, regardless
of whether this block is present or `maintenance.enabled` is false. The block controls
optional scheduled/reminder UX, whether Phase 8 may repair versus report findings, and
whether generated documentation sections participate. Omitting it keeps all optional UX
and repair/generated-section behavior enabled by default.

The `docs` field is optional and, like `maintenance` above, has no configure question.
`docs.policy` is an optional field that no configure prompt writes, and a configure re-run preserves it together with every unrelated key.
Merge semantics (step 6 in `phases/write-config.md`) keep a hand-added value untouched; it adds no `configVersion` bump and no `question-keys.json` entry. Do **not** add `docs` to the migration-removal list below — it is a supported optional field, not a legacy one.

Schema: `docs.policy` — the exact, case-sensitive string `"minimal"` selects the minimal documentation policy for `/cenci:implement`'s Phase 8; an absent `docs` block, an absent `docs.policy`, or any other value means the default policy (an unknown value warns once, naming `docs.policy`). Example: `{"docs": {"policy": "minimal"}}`.
Plan-required and correctness documentation work always runs, while incidental documentation edits and lesson capture are what `minimal` suppresses.
`flow/README.md`'s `### Documentation policy` subsection is the user-facing reference for the resulting Phase 8 and PR-body behavior.

Only include servers in `mcpServers` that were presented as options (i.e., detected or always-available). Value is `true` if enabled, `false` if declined.

Only include servers in `lspServers` that were detected and presented in question 6. Value is `true` if enabled, `false` if declined. Omit `lspServers` entirely if no LSP servers were detected.

When migrating from an older config that has `ticketSystem`, `prSystem`, `ticketPrefix`, `adoOrg`, `adoProject`, `adoRepo`, `profile`, or `sandboxEnabled` fields, remove them during the merge.
