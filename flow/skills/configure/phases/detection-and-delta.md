# Detection and Delta Resolution

Read this file on every run, after Create Worktree and before the first question (see `SKILL.md` `### Step Map`). It holds the scripted detection, the delta resolution that decides which questions this run asks, and the platform and container detections with their manual fallbacks.

### Scripted Detection

The deterministic detections this skill needs (platform, container, package manager, MCP/LSP/dind/Playwright catalog triggers, plugin version) are scripted, not re-derived ad hoc. Run the bundled detector once, as its **own** Bash call (per `cenci:shell-rules`), from the repository root of the main checkout — it only reads, nothing is written:

```bash
bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/detect-project.sh"
```

(the detector is `scripts/detect-project.sh` inside this skill; its tests live next to it). Parse its stdout as a JSON object and keep it as `detection` for the rest of this run:

- `detection.pluginVersion` — installed flow plugin version; stamped into config as `configVersion` (step 6)
- `detection.platform` — `{name, owner, repo}` parsed from the git remote, or `null`
- `detection.inContainer` — cenci-sandbox container detection result
- `detection.packageManager` — `pnpm` / `yarn` / `npm` from lockfiles, or `null`
- `detection.mcpServers` — MCP catalog triggers found in the dependency scan (e.g. `angular`, `primeng`)
- `detection.lspServers` — LSP catalog triggers found (e.g. `typescript`, `gopls`)
- `detection.dindDetected` — Testcontainers/Docker-SDK trigger found (question 9b default)
- `detection.playwrightTest` — `@playwright/test` in root `devDependencies`
- `detection.warnings` — non-fatal detection notes; relay them to the user as informational messages

If the script exits non-zero or its output is not valid JSON, fall back to the **manual fallback** procedure kept in each consuming section (in this file or in the later phase files named in `SKILL.md`'s `### Step Map`) — a detection either comes from `detection` or from its manual fallback, never silently skipped. The script only detects: every question, catalog decision, and user choice stays in this skill.

### Delta Resolution

A re-run of this skill is a **delta** by default: questions whose answer is already recorded, or that an earlier run already offered, are skipped, while every generation step still runs. The deterministic answer to "which questions does this run ask?" comes from `scripts/resolve-delta.sh` and the committed `question-keys.json` table (see the `ID` column of the defaults table in `SKILL.md`'s `### Question Defaults`).

**Run mode.** The run mode is `delta` only when `existingConfig` (or `migrationBase`) exists, `--all` is absent, and the resolver succeeds; otherwise the run mode is `full` and every question is asked with `--all` semantics, except questions 13, 14 and 15 (see Fallback). That is today's behavior: each question shows the current setting as its default. A first run (`existingConfig` null), a legacy-only migration run (only `.claude/config.json` exists, so there is no `.cenci/config.json`, no `existingConfig` and no `migrationBase` to resolve against) and a `--all` run never call the resolver; all three are `full` runs by the first and second conditions, not by a resolver failure.

**Resolver call.** Run the resolver as its **own** Bash call (per `cenci:shell-rules`), after Scripted Detection and before the first question — it only reads, nothing is written:

```bash
bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh" <config-path>
```

`<config-path>` is the file `existingConfig` was read from (`.cenci/config.json` in the repository root of the main checkout). On a migration run where both `.cenci/config.json` and `.claude/config.json` exist, the input is `migrationBase`: `migrationBase` is written to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-delta-input.json` first and removed afterwards (`mkdir -p ${TMPDIR:-/tmp}/cenci` beforehand, then `rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-delta-input.json` as its own Bash call once the resolver has run — whether it succeeded, failed or failed the shape check below). A legacy-only run has no canonical config to resolve against, so it is a `full` run and never calls the resolver. Parse its single stdout JSON line as `delta`: `delta.ask` (question IDs to ask), `delta.carried` (IDs to skip), `delta.missingKeys` and `delta.conditional`. The output holds question IDs and config key names only, never config values.

**Result shape check.** Valid JSON is not enough: before using it, require that `delta.ask` and `delta.carried` are both JSON arrays — for example `jq -e '(.ask | type) == "array" and (.carried | type) == "array"'` over the stdout line, exit 0. A result that fails the check (a bare scalar, an object without the arrays, `null`) is treated exactly like a resolver failure in the fallback below; never ask "nothing" because an unusable result was read as an empty `delta.ask`.

**Fallback.** If the resolver exits non-zero, prints invalid JSON, or its JSON fails the shape check above, treat the run as `full` and tell the user why. Give one informational line naming the stderr cause (or the failed shape check); a resolver failure never blocks configuration and never silently skips a question. A `full` run caused by a resolver failure does **not** re-offer a present `planning`, `automerge` or `refinement` value — only an explicit `--all` does (see `### Autonomy Settings` in `phases/autonomy-settings.md`): a fallback must never put the destructive "Remove the block(s)" option in front of the user.

**Using the result.** Ask only the question IDs in `delta.ask` and carry every ID in `delta.carried`. When `delta.ask` is empty, ask no questions and still run every generation step. A carried answer skips only the prompt — its value still drives every generation step. That covers `.mcp.json`, `.lsp.json`, `~/.claude/settings.json`, the AGENTS/CLAUDE marker sections and `.cenci/Dockerfile`: read the value from `existingConfig` instead of asking. Question IDs map to the numbered questions through the `ID` column of the defaults table in `SKILL.md`'s `### Question Defaults`; a question whose ID is not in `delta.ask` is skipped even when its numbered section in the phase files says "ask".

**Offered ledger.** Only questions actually put to the user this run are recorded as offered (step 6 stamps them into `configureOffered`). A conditional question whose detection condition fails (5c without a frontend framework, Playwright without `@playwright/test`, question 6 without detected LSP servers, question 10 when `.lazyboards.yml` exists, question 11 in a container or when the status probe fails) is not recorded, and question 12 and Steps 2a/2b have no ID and are never recorded. An ID is recorded only for an answer that was applied or explicitly declined: when the question 13/14/15 overlay falls back in step 6, that question's ID is removed from the offered set before the ledger is stamped (the next delta run asks it again only when its key is absent and its ID is not already in the existing ledger; otherwise `/cenci:configure --all` applies it), and question 11 is recorded only after a successful `cenci dispatch enroll` or an explicit "No" (see `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`).

**Named exceptions** — the only things asked or re-opened beyond `delta.ask` on a delta run:
- The `enrolled: true, session: ""` repair (question 12) is still asked on a delta run (see `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`).
- The only re-offers a detected stack change causes are question 1 pre-filled with the detected stack, Steps 2a/2b for new monorepo projects, and questions 5/6 for servers missing from the existing maps. The keep-or-change gate still applies to the servers already recorded.

**Things that are not configure questions on a delta run.**
- Free-text user context never re-opens an answered question on a delta run; if it names one, say that `/cenci:configure --all` re-opens it. Context still steers the questions that are asked and the generation steps.
- When question 8 is carried, `ci.yml` is neither regenerated nor prompted for. The conflict check is part of question 8, and `ci.yml` carries no managed markers.
- A carried `uiConventions` keeps the existing `## UI Conventions` section verbatim. That answer lives only in the generated `AGENTS.md`.
- The substantive-guidance diff-approval gate in step 1 stays a safety gate, not a configure question: it still fires whenever existing guidance would be replaced.
- The `.lazyboards.yml` suggest-or-skip analyzer only reports on a delta run, and a managed `.cenci/Dockerfile` is refreshed without asking (see step 5e and `### Board Config (lazyboards)` in `phases/lazyboards.md`).

### Platform Detection

Use `detection.platform`: when non-null it carries the platform name plus extracted owner/repo; when `null`, fall back to manual questions.

**Manual fallback** (only if Scripted Detection failed): run `git remote get-url origin 2>/dev/null` and parse the result:

| Remote URL pattern | Platform | Extracted values |
|---|---|---|
| `git@github.com:OWNER/REPO.git` | github | owner, repo |
| `https://github.com/OWNER/REPO.git` | github | owner, repo |
| Unrecognized / no remote | — | Fall back to manual questions |

Strip trailing `.git` suffix from repo names.

If user context was provided, use it to steer the configuration (e.g., skip certain questions, pre-select options, focus on specific areas).

### Container Detection

cenci runs inside `sandbox`'s cenci-sandbox container with `--dangerously-skip-permissions`. The **container is the security boundary** — there is no host profile. Claude Code's own host sandbox stays disabled, and `permissions.allow`/`deny` are kept only as defense-in-depth for plain `claude` runs inside the container (e.g. `cenci open --shell`).

Use `detection.inContainer` (the detector checks `CENCI_SANDBOX`, then `/.dockerenv`).

**Manual fallback** (only if Scripted Detection failed) — detect the container (stop at the first match; run each check as its **own** Bash call, per `cenci:shell-rules` — never compound them):

1. **`CENCI_SANDBOX` env var** (works for both Docker and Podman): run `test "${CENCI_SANDBOX:-}" = "1"` as its own Bash call. Exit 0 → in container.
2. **Docker fallback**: run `test -f /.dockerenv` as its own Bash call. Exit 0 → in container.

This detection is a **non-blocking advisory** — it never gates configuration and never uses `AskUserQuestion`:

- **In container**: emit an informational message: "Detected the cenci-sandbox container — the container is the security boundary. Claude Code's host sandbox stays disabled." Then continue normally.
- **Not in container**: emit an advisory and continue anyway: "cenci is designed to run inside the cenci-sandbox container (the security boundary). You appear to be running on the host — continuing, but running outside the container is unsupported." Proceed with the same container-shaped output.
