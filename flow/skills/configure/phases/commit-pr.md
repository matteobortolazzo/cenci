# Commit, Push, and Open PR

Read this file on every run, after `phases/write-config.md` (see `SKILL.md` `### Step Map`). It holds step 7, which commits the worktree, pushes the branch and opens the PR, and the final report that follows it.

7. **Commit, Push, and Open PR**: configure's changes live in `<worktree-path>` — ship them the same way every other change in this repo ships.

   Read `<worktree-path>/docs/git-workflow.md` for the commit/branch/PR conventions used below. Target the worktree explicitly with `git -C <worktree-path>` on every command below so they resolve against the worktree and stay auto-approved (see `cenci:shell-rules` — never compound `cd` with the git command itself).

   **Commit**:
   ```bash
   git -C <worktree-path> add -A
   git -C <worktree-path> commit -m "chore(configure): <one-line summary of what changed>

   <bullet list of generated/updated files>"
   ```
   If nothing is staged (a re-run that changed nothing), skip Commit/Push/PR entirely and report "No configuration changes — nothing to commit" instead of opening an empty PR. A stamp-only diff still opens a PR; "No configuration changes — nothing to commit" applies only when nothing is staged. For example, a run that offered a new question and recorded the decline in `configureOffered` changes `.cenci/config.json` and so is not empty.

   **Push**:
   ```bash
   git -C <worktree-path> push -u origin chore/configure-<slug>
   ```
   If push fails due to sandbox/network/auth, show the exact command and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

   **PR**: write the body to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md` first (never a heredoc or large inline string), then:
   ```bash
   gh pr create --title "chore(configure): <one-line summary>" --body-file ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md
   ```
   Body:
   ```markdown
   ## Summary
   Project configuration generated/updated by `/cenci:configure`.

   ## Changes
   - <one bullet per file/section actually written this run>

   ## Testing
   N/A — configuration/tooling only, no application code changed.
   ```
   Append a `## Review required — automerge policy` section — emitted only when question
   14 scaffolded a block this run (`### Autonomy Settings` in `phases/autonomy-settings.md`) and that block was actually written (a scaffold whose overlay fell back in step 6 emits nothing here), never on a re-config that
   reported an existing block verbatim:
   ```markdown
   ## Review required — automerge policy
   This PR scaffolds a starter `automerge` policy block (`protectedPaths`, size caps,
   `mergeMethod`). Review it before enabling automerge fleet-wide — see
   `docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch`.
   ```
   Append a `## Review required — automerge policy removed` section — emitted only when question 14 removed the block(s) this run (`--all`, "Remove the block(s)") and the removal overlay passed step 6's validation and was written, never on a run that kept, never touched, or fell back from removing an existing block:
   ```markdown
   ## Review required — automerge policy removed
   This PR removes the `automerge` policy block(s) — the top-level block and every
   `projects[]` block. Without a block, automerge is denied for every PR (deny-by-default).
   Confirm this is intended — see
   `docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch`.
   ```
   On a delta run, also append a `## Carried through (delta run)` section that lists the carried question IDs and config key names only — never config values — and says `/cenci:configure --all` would re-open them. The PR may be public and config values can be sensitive, so the verbatim `planning`/`automerge`/`refinement` report stays in the terminal:
   ```markdown
   ## Carried through (delta run)
   Question IDs carried from the existing configuration: <comma-separated `delta.carried` IDs>.
   Config keys left as-is: <comma-separated key names, e.g. `stack`, `mcpServers`, `planning`>.
   Run `/cenci:configure --all` to re-open them.
   ```
   If `gh pr create` fails because a PR for this branch already exists (re-entry after a prior turn already created it), recover the URL with `gh pr view chore/configure-<slug> --json url -q .url` and continue. For any other failure (auth, network, validation), show the exact error and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve it before continuing.

   Delete the PR-body temp file after a successful `gh pr create` (or a successful recovery via `gh pr view`):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md
   ```

Report what was created and suggest next steps (e.g., "Try `/cenci:refine <ticket-id>` on a ticket"), including the PR URL from step 7. If question 14 (`### Autonomy Settings` in `phases/autonomy-settings.md`) scaffolded an `automerge` block this run, say so explicitly and flag it for review, then point at the fleet-side next steps: `cenci dispatch plan-refined on` (once `planning.autonomy` is `lean` on `origin/main`) and `cenci automerge on` (once the scaffolded block has been reviewed) — the same verbs named in the "Next step" block of `### Fleet Autonomy Controls` in `phases/fleet-enrollment.md`. If `sandbox.enabled` is `true`, mention the generated `.cenci/Dockerfile` and that `cenci sandbox build` (run from inside the repo, after the PR merges) builds the repo's own tailored image on top of the shared base. If `lazyboards.enabled` is `true`, list the generated `.lazyboards.yml` In Review actions with their keys, serve, and test commands (e.g., "`W` opens web-client's PR worktree, `S w` serves it with `ng serve`, `T w` runs its tests with `ng test --watch=false`") and point at `docs/orchestration.md` for the board recipe. Repeat the trust caveat here too, since this run's file ships as a PR: the generated shell actions and `cleanup` stay inert until `lazyboards trust` is run against the file's exact content, so trust must be re-granted after the PR merges if review changed a byte — and every teammate runs it once on their own clone. When the suggest-or-skip branch ran instead, report what happened (added actions, migrated to `keymaps:`, or "already complete — no changes"); on a delta run it only reports, so list the missing actions and say `/cenci:configure --all` offers to add them.

On a delta run, the final report also covers what was carried and what was refreshed. The final report repeats the carried-through summary, the Dockerfile report, and the lazyboards report. The carried-through summary names the carried question IDs and config keys and says `/cenci:configure --all` would re-open them; for a carried `planning`, `automerge` or `refinement` value it also states the existing value verbatim (terminal only, never the PR body). The Dockerfile report is the block diff (`git -C <worktree-path> diff -- .cenci/Dockerfile`) for a refreshed managed block, or "left untouched — no markers or malformed markers" with the `--all` pointer. The lazyboards report is the missing-actions list, "already covers all recommended actions", or the carried-but-missing-file notice from question 10. If the resolver failed and the run fell back to `full`, say so and why. If question 14 removed the `automerge` block(s) this run (the removal overlay was applied), say so explicitly and flag the PR's `## Review required — automerge policy removed` section. Report what was actually applied, never what was collected: when a question 13/14/15 overlay fell back, say its answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; when the ledger overlay fell back, say the ledger was not stamped so this run's questions will be asked again; when question 11's enroll failed, report the failure and that question 11 will be asked again only when `dispatchEnroll` is not already in the existing ledger, else to re-run `/cenci:configure --all`.
