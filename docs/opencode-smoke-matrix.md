# OpenCode smoke matrix

> **Manual, maintainer-run checklist — not automated, not CI-gated.** Nothing in this
> repository executes the steps below: they require a real OpenCode CLI, real
> Anthropic/OpenAI credentials, and a real GitHub issue/PR. Running them is an explicit
> maintainer follow-up action, performed outside cenci's automated test/lint/CI
> pipelines. This document is the checklist itself, not a record that the run has
> already happened — see [Results log](#results-log) below for that.

This is the manual verification companion to
[flow's OpenCode support](../flow/docs/opencode.md) and
[cenci-watch's OpenCode adapter](../watch/README.md#dispatching-workflows-cenci-run).
Automated tests cover installer detection/doctor/setup and the OpenCode plugin's event
mapping in isolation; they cannot exercise a live OpenCode CLI reasoning over a real
ticket end to end. Run this checklist by hand after any change that touches OpenCode
support (the installer's OpenCode wiring, `flow/opencode/install-skills.sh`,
`sandbox/lib/opencode-config.sh`, `watch/plugin/opencode`, or a pinned `OPENCODE_MIN_VERSION`
bump) and whenever OpenCode itself ships a new minimum-supported release.

## Prerequisites

- [ ] Claude Code or Codex is already installed and working with cenci (OpenCode is an
      additive agent, never a standalone install — see
      [Getting started's OpenCode section](getting-started.md#opencode-support-additional-opt-in-agent)).
- [ ] `install.sh` (or `cenci update`) has been run and the OpenCode opt-in prompt
      (`OpenCode detected — link cenci's skills and register its plugin?`) was accepted.
- [ ] `cenci doctor` reports `OpenCode <version> supports cenci integration`,
      `OpenCode skills linked`, and `OpenCode plugin registered` — all `✓`, no `✗`/warning.
- [ ] A `config.json` (`$XDG_CONFIG_HOME/cenci/config.json`, or `--config`) defines an
      `opencode` agent entry with an `implement` workflow, per
      [cenci-watch's configuration reference](../watch/README.md) (OpenCode has no
      built-in template — `cenci run implement <ticket> --agent opencode` errors without
      one).
- [ ] `gh auth login` has been run; the GitHub repo used for the smoke ticket is
      reachable.
- [ ] Provider credentials are ready for **both** passes below:
  - Anthropic: `opencode auth login` (Anthropic) creating
    `~/.local/share/opencode/auth.json`, or `ANTHROPIC_API_KEY` exported.
  - OpenAI: `opencode auth login` (OpenAI), or `OPENAI_API_KEY` exported.
  - Only one provider's credential needs to be active for a given pass; swap between
    passes rather than trying to satisfy both simultaneously.

## Running a pass

Repeat this section once per provider (see [Two passes](#two-passes-anthropic-and-openai)).
Use a real GitHub issue small enough to implement end to end (a good candidate: an
already-triaged, well-scoped bug fix or small feature, similar in size to what the
[ticket-sizing guide](../flow/docs/ticket-sizing.md) considers a single ticket).

1. **Pick up the ticket.**

   ```bash
   cenci run implement <ticket-number> --agent opencode
   ```

   - [ ] **Verify:** a detached tmux window named `<ticket-number>-implement` is
         created; `cenci status` shows it as running.

2. **Plan produced.**

   - [ ] **Verify:** OpenCode, guided by the repo's `AGENTS.md`/`CLAUDE.md` conventions
         and cenci's linked portable skills, works out an approach before making
         changes (inspect the session transcript/pane).

3. **Code implemented.**

   - [ ] **Verify:** the working tree under the session's worktree/branch has real,
         relevant changes addressing the ticket — not a no-op or unrelated diff.

4. **Tests run.**

   - [ ] **Verify:** the project's test command was actually invoked during the run and
         its result (pass/fail) is visible in the transcript, per the
         [`testing`](../flow/skills/testing/SKILL.md) skill's test-first expectations
         (portable and linked for OpenCode).

5. **PR opened.**

   - [ ] **Verify:** a pull request exists on GitHub referencing the ticket, with a
         description and a diff that matches what was implemented in step 3.

6. **Live status reflects the run.**

   - [ ] **Verify:** `cenci status` (or the tmux/menu-bar/desktop surface in use) showed
         the window transition through running → done (or running → needs-input, if
         OpenCode asked a permission question) — confirming the
         `watch/plugin/opencode` hooks fired.

7. **Event mapping spot-checks (`watch/plugin/opencode/plugin.ts`).**

   Confirm each OpenCode event maps to the expected daemon `hook_event_name`
   (inspect the session transcript / `cenci status` transitions):

   - [ ] **`session.status`** — `busy` → `PostToolUse` (window shows running);
         `idle` → `Stop` (window shows done).
   - [ ] **`session.error`** → `StopFailure`, but only for an already-tracked
         session; an untracked `session.error` must NOT flip an unrelated window.
   - [ ] **`message.updated`** (role=`user`) → `UserPromptSubmit`, reported once;
         re-submitting the same message id does NOT double-report (dedup).
   - [ ] **`permission.asked`** → `PermissionRequest` (window shows needs-input).

**Pass** means every checkbox above is checked for that provider, with no cenci-side
crash, hang, or silently-swallowed error. A step that fails for a reason specific to
the ticket content (e.g. a genuinely broken test in the target repo) is not a cenci
failure — note it and move on; a step that fails because of missing/incorrect
integration (auth not staged, plugin not firing, workflow template missing) **is** a
cenci failure and should block release until fixed.

## Two passes: Anthropic and OpenAI

Run the [pass above](#running-a-pass) twice, end to end, once per provider — do not
skip either even if the other already passed recently:

1. **Anthropic pass** — provider credential is Anthropic (`opencode auth login`
   Anthropic sign-in, or `ANTHROPIC_API_KEY`).
2. **OpenAI pass** — provider credential is OpenAI (`opencode auth login` OpenAI
   sign-in, or `OPENAI_API_KEY`).

Use a different ticket for each pass so a flaky or ambiguous ticket doesn't mask a
provider-specific problem.

## Results log

This is a living checklist — append a row after every maintainer-run pass, do not
overwrite prior entries. `cenci version` and `opencode --version` values are read
directly from the maintainer's machine at run time.

| Date | cenci version | OpenCode version | Provider | Ticket | Pass/Fail | Notes |
|---|---|---|---|---|---|---|
| _(fill in)_ | | | | | | |
