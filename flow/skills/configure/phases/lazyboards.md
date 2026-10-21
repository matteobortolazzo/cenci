# Lazyboards Board Config

Read this file when `lazyboards` is asked this run or `.lazyboards.yml` exists (see `SKILL.md` `### Step Map`). It holds question 10 with its key-assignment rules, the suggest-or-skip analyzer for an existing file, and step 5f, which generates `.lazyboards.yml`.

### Board Config (lazyboards)

**This section is read whenever `lazyboards` is asked or `.lazyboards.yml` exists** — there is no install/opt-in
gate. The router (`SKILL.md`'s `### Step Map`) has already checked whether a board config exists with its own `test -f` Bash call; branch on that result:

- **No `.lazyboards.yml` at the repo root** → ask **question 10** below (offer to
  generate one) when its ID `lazyboards` is in `delta.ask` (always on an `--all` or
  `full` run). On "No", omit `lazyboards` from config.json (same pattern as
  `cicd`/`sandbox`). A declined question 10 is recorded in `configureOffered` and is not re-asked on a delta run.
  If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it.
- **`.lazyboards.yml` already exists** → **skip question 10** and jump to
  **Existing config: suggest or skip** below (analyze the file against the
  recommended action set and suggest what's missing, or skip quietly with a small log).
  On a delta run with an existing `.lazyboards.yml`, the suggest-or-skip analyzer reports new board actions in the end-of-run summary and never prompts; `--all` restores the Apply/Overwrite/Keep/Show prompt.

10. **Board config** (no existing `.lazyboards.yml`): present this Yes/No
    confirmation via `AskUserQuestion` — a pre-existing
    `lazyboards.enabled: true` recorded from a prior run, or user context requesting a
    narrower focus or a skip, never authorizes silently regenerating
    `.lazyboards.yml` without a question: "Generate a per-repo
    `.lazyboards.yml` for the lazyboards board? (Wires Refine/Implement keybindings onto
    the New/Refined/Planned columns, Edit-plan and View-plan keybindings on Planned that open
    the ticket's saved plan in `$EDITOR` / a pager, plus In Review actions that open a
    PR's registered worktree in a tmux window (`W`) and, per project, serve or test it
    with a mnemonic key sequence — reviewing a PR becomes a couple of keypresses on
    the card.)"
   - Options: "Yes — generate `.lazyboards.yml`", "No — skip"

   **If Yes — detect runnable projects and their serve commands.** For the single
   project (or each monorepo project confirmed in Step 2a), derive a serve command
   from the first matching rule:

   | Rule (first match wins) | Serve command |
   |---|---|
   | `package.json` has a `dev` script | `npm run dev` |
   | `package.json` has a `start` script | `npm run start` |
   | Angular project (`@angular/core`) without either script | `ng serve` |
   | `.csproj` with `Microsoft.NET.Sdk.Web` | `dotnet run` |
   | Go module containing a `main` package | `go run .` |
   | Anything else (libraries, tooling, markdown/shell) | *not runnable — no action generated* |

   Only the **runner invocation** above is ever embedded in the generated file
   (`npm run dev`, `ng serve`, …) — never the script *contents* from `package.json`,
   which are semi-trusted external values (see `docs/skill-authoring.md`).

   **Also derive a test command** for each project (first match wins), so the In
   Review column can offer a one-keypress "run the PR's tests in its worktree"
   action alongside serve. Reuse the project's stored `testCommand` from
   `existingConfig.projects[]` when present before falling back to this table:

   | Rule (first match wins) | Test command |
   |---|---|
   | `package.json` has a `test` script | `npm test` |
   | Angular project (`@angular/core`) | `ng test --watch=false` |
   | `.csproj` or `.sln` present | `dotnet test` |
   | Go module | `go test ./...` |
   | Anything else (no detectable tests) | *not testable — no test action generated* |

   As with serve, only the **runner invocation** is embedded — never the raw `test`
   script *contents* from `package.json`.

   **Key assignment**: any key, of any case, can carry a binding — there is no
   reserved namespace — and lazyboards supports multi-key sequences, not just
   single keys, so per-project serve/test actions never have to compete for a
   scarce pool. A sequence is written in
   **canonical space-separated form and quoted** — `"S b"`, not `Sb`, which would
   parse as a single two-character key rather than a two-key sequence.

   **`W` is reserved board-wide for Open worktree** — a single action, always emitted
   on `In Review`, that opens the PR's registered worktree in a tmux window with a
   plain shell and runs no command (see the generated example below). `W` is never
   assigned to serve, test, or any other action.

   **Keys lazyboards claims by default.** A user binding wins over a built-in
   default, so binding one of these silently replaces the built-in command rather
   than erroring. Never assign any of them:
   `?` `q` `n` `e` `c` `o` `r` `p` `x` `d` `P` `m` `A` `G` `D` `s` `f` `a` `/`
   `j` `k` `l` `h` `tab` `shift+tab` `1`–`9` `ctrl+c`, plus the `g` prefix (`g a`,
   `g r`). Of the keys cenci generates, none collides with lazyboards' own bindings —
   `R I E V W C X S T` are all free.

   **Never bind or unbind `ctrl+c`.** It always force-quits, ahead of any table
   lookup; a `ctrl+c` token anywhere in any key or sequence — even as an explicit
   `~` unbind — is a load-time config error that stops the whole config from
   loading.

   **Prefix constraint**: a bound key that is a strict, whitespace-boundary prefix
   of another bound key in the same effective table is a **load-time config error**
   — lazyboards refuses to load the file at all, rather than the shorter key merely
   losing at dispatch time. The effective table is the fully resolved namespace:
   built-in defaults **plus** the user's `keymaps.<mode>` table **plus** that
   column's `keymaps.columns.<name>` overlay. So the built-in defaults are a prefix
   source too — a `d`-prefixed sequence (`"d x"`) is invalid because the default
   `d` (`card.delete`) is already bound, and the `g` prefix is unavailable in the
   other direction because `g a`/`g r` already exist. Before assigning the `S`/`T`
   mnemonic prefixes below, check the whole resolved namespace — the default keys
   above, `keymaps.normal`/`keymaps.detail`, and the target column's own overlay,
   including any user-added binding such as a manually bound `S: Sync` — for a
   standalone key sharing the leading letter. If one is claimed, pick a different
   leading letter for that project's serve/test group instead (e.g. `R` for "Run")
   and call out the substitution explicitly in the AskUserQuestion mapping prompt
   below so the user can approve or rename it.

   Assign **serve** keys as `S` followed by a project-specific mnemonic letter:
   pick whichever second letter best identifies the project for its type — e.g.
   `"S b"` for a backend/API project, `"S f"` for a frontend project, or the first
   letter of the project slug when neither fits (e.g. `"S w"` for `web-client`,
   `"S a"` for `admin`). For a single-project repo, plain `S` is enough — there's
   nothing to disambiguate. On a mnemonic collision between two projects, fall back
   to the next letter of that project's slug.

   Assign **test** keys the same way: plain `T` for a single testable project, or
   `T` + mnemonic (`"T b"`, `"T f"`, …) for multiple, following the same mnemonic
   rule as serve.

   Never use `C` or `X` (the generated file's own `keymaps.normal`/`keymaps.detail`
   tables claim them for the Claude/Codex board-level launch actions), never use `E`
   or `V` (the Planned column's Edit-plan and View-plan actions claim them), never
   reuse a key or key sequence already assigned
   to a serve action, never repurpose `W` for anything but Open worktree, and never
   assign a leading letter already bound as a standalone key anywhere in the
   resolved namespace per the prefix constraint above.

   Present the proposed mapping with AskUserQuestion before generating, e.g.:
   "Proposed In Review actions: `W` → open PR worktree, `S b` → api serve
   (`dotnet run`), `S f` → web-client serve (`ng serve`), `T f` → web-client tests
   (`ng test --watch=false`). Generate these?" Options: "Yes — use this mapping
   (Recommended)", "Change keys or drop projects" (then re-ask with the user's
   adjustments; enforce the reserved-key exclusions above).

### Step 5f — Generate .lazyboards.yml

5f. **Generate `.lazyboards.yml`** (when question 10 was asked and answered Yes
   in this session — the no-existing-file path). When a file already existed, the *Board
   Config* branch instead runs the **Existing config: suggest or skip** sub-step at
   the end of this section; the generation format below is what both paths write:

   Write `.lazyboards.yml` at the repo root with the confirmed key mapping. Two
   lazyboards behaviors to honor:

   - A local `columns:` list **replaces** the global column list entirely — it
     **never merges**. Because the generated file must stand alone, it never relies
     on inheritance from a global config cenci no longer creates: every column the
     board should show is declared here. `Implemented` is dropped from the generated
     file entirely — it is a label in the ticket lifecycle, not a board column.
   - **Every key binding goes in the `keymaps:` namespace.** Per-column bindings
     live under `keymaps.columns.<name>` (matched case-insensitively); board-level
     bindings go in **both** `keymaps.normal` and `keymaps.detail`, since
     `keymaps.normal` alone would silently kill them while the detail panel is
     focused. `columns:` itself is the board layout — a bare name list.

   The full generated file is the worked example in `references/lazyboards-example.md`;
   read it before writing or rewriting the file. The rules below govern every action it
   holds.

   - **`W` (Open worktree) is always emitted on `In Review`, for every repo, whether
     or not any project is runnable or testable.** It opens the PR's registered
     worktree in a tmux window with a plain shell and runs no command — it never
     carries a project path or a serve/test command, even in a monorepo. `W` is
     never reused for serve, test, or any other action. `command:` is omitted
     deliberately: on an action that sets `window:`, lazyboards treats "open a
     window on this directory" as complete and runs the default shell there.
   - `Planned` gets a local `I` (Implement) action too, so a ticket that already
     passed planning can still be manually re-dispatched straight from the board, plus
     local `E` (Edit plan, opens in `${EDITOR:-vi}`) and `V` (View plan, opens in
     `${PAGER:-less}`) actions on the ticket's persisted plan file. Both carry
     `terminal: true`, so the editor/pager gets lazyboards' own terminal and the
     board returns when it exits — the same shape lazyboards' built-in `e` (edit
     card) uses, and one that needs no multiplexer. Plan files are
     `.plans/<number>-<slug>.md`; the slug isn't derivable from the number, so both
     actions glob `.plans/{number}-*.md` (the same resolution the implement skill
     uses) and no-op when no plan file is present (e.g. already consumed in Phase 9).
     `{number}` is a validated integer, safe to interpolate.
   - `Implemented` is **never** re-emitted as a generated column in
     `.lazyboards.yml` — only `New`, `Refined`, `Planned`, and `In Review` appear.
   - `C` and `X` are board-level Claude/Codex launch actions, emitted under
     **`keymaps.normal` and `keymaps.detail`** — never under
     `keymaps.columns.<name>` (they are board-level, not per-column). lazyboards
     merges `keymaps:` tables and scalar fields across a global and a local config
     file, with **local keys winning** (and the merged user config always winning
     over the built-in defaults), so the generated file is self-contained and a
     stray `~/.config/lazyboards/config.yml` cannot conflict with it. `cleanup` is
     a top-level scalar for the same reason: it is always emitted here, outside
     `keymaps:`; delete the line to opt out.
   - One `In Review` **serve** action per runnable project, using the confirmed key
     (`S` alone for a single runnable project, or the `S` + mnemonic sequence —
     `"S b"`, `"S f"`, … — for multiple, per the Key assignment rules above) and
     serve command; action name `Serve <slug> worktree`. One `In Review` **test**
     action per testable project, using the confirmed test key (`T` alone, or the
     `T` + mnemonic sequence for multiple) and test command; action name
     `Test <slug> worktree`. Sequence keys are written in canonical
     **space-separated** form and quoted (`"S w"`), never concatenated (`Sw`).
   - **Never hand-write a `tmux new-window` invocation.** lazyboards assembles it
     from four `type: shell` fields and escapes the window name, the working
     directory, and the command as single shell tokens, so a worktree path or
     project subpath containing a space, a quote, `;`, or `$(…)` cannot break out
     of the command line:

     | Field | Meaning |
     |-------|---------|
     | `window:` | Window name. Its presence is what makes the action open a window. Template-expanded. |
     | `cwd:` | Working directory. Template-expanded. Applies to **every** shell action — windowed, `terminal: true`, and plain buffered alike. |
     | `focus:` | `true` switches to the new window; omitted leaves it detached (what the old `-d` flag did). |
     | `terminal: true` | Hands the command lazyboards' own terminal: full output and input, board restored on exit. |

     All four are modifiers on `type: shell` and are a **load-time config error**
     on a `type: url` action, as are the two contradictory combinations:
     `window:` together with `terminal:`, and `focus:` without a `window:`.
   - **Pick the run mode by what the command is**, never by how fast it is:
     a **serve** action keeps running alongside the board, so it takes
     `window: "pr-{pr_number}"` (detached, no `focus:`); a **test** action is
     watched to completion and then hands the board back, so it takes
     `terminal: true`. `W` takes `window:` with no `command:`. All three set
     `cwd:` — only the key, the run mode, and the command differ.
   - **The project path is a plain `cwd:` segment, in a monorepo too.**
     **Single project**: `cwd: "{pr_worktree}"`. **Monorepo**: append the project
     path directly — `cwd: "{pr_worktree}/apps/web-client"`. No nested POSIX
     quoting, no `'\''` escaping, and never a `cd <path> && ` prefix in the
     command: `cwd:` is expanded and then escaped as one token, so quoting it by
     hand would double-escape it.
   - `{pr_worktree}` resolves the PR branch's registered Git worktree at action
     time, so the file stays machine-independent — never embed absolute paths.
     `{number}` (used in `command: "cenci run refine {number}"` /
     `implement {number}`) is a validated GitHub issue/PR
     integer, not free text, so it is safe to interpolate without additional
     escaping. `window:` and `cwd:` are template-expanded like `url:`/`command:`,
     so they count for scope inference and the scope variable restrictions too — a
     `{pr_*}` placeholder in either one requires `scope: pr` on that action, and a
     board-scope action may not reference one at all.
   - A `window:` action needs a tmux session: outside tmux lazyboards reports
     `Not inside tmux` in the status bar instead of running anything. A
     `terminal: true` action has no such dependency and works over a plain SSH
     session — which is why the `E`/`V` plan actions above use it.
   - **Zero runnable projects**: `W` (Open worktree) is emitted regardless — it
     doesn't depend on any project being runnable or testable. If no project in the
     repo has a detected serve or test command, `In Review` still carries just `W`:
     ```yaml
     keymaps:
       columns:
         "In Review":
           W:
             name: Open worktree
             type: shell
             scope: pr
             window: "pr-{pr_number}"
             cwd: "{pr_worktree}"
     ```
   - **Trust (run after writing the file).** A repo-local `.lazyboards.yml` is
     attacker-controlled, so lazyboards silently strips every `type: shell` binding
     **and** the `cleanup:` line until the file's exact content is trusted — and the
     generated file is entirely shell actions plus `cleanup`, so it is completely
     inert until then. After the write, ask via `AskUserQuestion`: "Run `lazyboards
     trust` now so the generated shell actions are honored?" Options: "Yes — run
     `lazyboards trust` (Recommended)", "No — I'll run it myself". On Yes, run it as
     its **own** Bash call from the repo root:
     ```bash
     lazyboards trust
     ```
     **Either way, always print the caveat**: trust is keyed to the file's exact
     content, not its path, so it must be re-granted after the configure PR merges
     if review changed a single byte, and again after any later regeneration of the
     file. A teammate cloning the repo has to run `lazyboards trust` once themselves.
     (Running `lazyboards trust` inside the worktree trusts the worktree copy's
     content; the same content at the repo root is trusted too, since the trust store
     is keyed by content hash, not path.)
   - **Existing config: suggest or skip** (the branch taken from *Board Config*
     above when `.lazyboards.yml` already exists — question 10 is **not** asked and
     the file is **not** blindly overwritten). This file-exists conflict check fires
     **unconditionally** whenever `.lazyboards.yml` already exists at the repo root,
     regardless of any prior `lazyboards.enabled` state recorded in
     `.cenci/config.json` — a stale or missing flag never causes this branch to be
     skipped, since the check is driven by the file's presence on disk, not the
     recorded flag:
     1. Read the existing file and derive the **recommended action set** this repo
        would generate above: Refine/Implement on `New`/`Refined`/`Planned`,
        Edit-plan (`E`) and View-plan (`V`) actions on `Planned`, an unconditional `W` (Open worktree) on `In Review`, and
        per runnable/testable project a serve (`S`/`"S b"`/`"S f"`/…) and test
        (`T`/`"T b"`/`"T f"`/…) In Review action. Outside the per-column tables it also includes the board-level `C`/`X` launch actions and `cleanup: "cenci close {number}"`.
     2. Compute the **delta** = recommended actions absent from the existing file,
        read from its `keymaps.normal`/`keymaps.detail`/`keymaps.columns.<name>`
        tables. A concatenated sequence key (`Sw`) and its canonical form (`"S w"`)
        are the same key, not two.
        Match by column + action intent (name/command) — or, for the column-less
        board-level `C`/`X`/`cleanup` actions, by intent alone — **not** by raw key,
        so a user's custom key binding is respected rather than flagged as
        "missing". Before assigning a key to any delta action, apply the
        prefix constraint from the key-assignment step above: if the resolved
        namespace already binds a standalone key (built-in default, or user-added
        such as a custom `S: Sync` board-level action) that would prefix-collide
        with the proposed `S`/`T` key, pick a different leading letter for that
        action instead.
        - **Hand-written tmux (any action, add or carry-over)**: an existing
          action whose `command:` starts with `tmux new-window` matches by intent
          — it is not "missing" — but counts toward the delta as an **update** to
          the `window:`/`cwd:`/`focus:` form, since the hand-written invocation
          puts the escaping of the worktree path and window name on the config
          file. Translate it: `-n <name>` → `window:`, `-c <dir>` → `cwd:`
          (dropping any nested `'…'` quoting and any `cd <dir> && ` prefix in the
          command), absence of `-d` → `focus: true`, and the remaining shell
          command → `command:`. Anything the four fields cannot express (a split,
          an existing window, `send-keys`, shell logic wrapped around the command)
          stays a plain `command:` and is **not** flagged. These two bullets are
          the only command-content comparisons in the delta; every other action is
          matched by intent alone, never diff-compared.
     3. **Delta non-empty** → on a `delta` run, do **not** prompt: leave the file
        untouched and list the missing actions (with the key each would use) in the
        end-of-run lazyboards report, saying `/cenci:configure --all` offers to add
        them. On an `--all` or `full` run, present the concrete additions via `AskUserQuestion`,
        e.g. "`.lazyboards.yml` is missing a PR-worktree test action: `T` → run tests
        (`dotnet test`) in the PR worktree. Add it?" — or, when a leading letter had
        to be substituted for the reason above, surface it explicitly, e.g.
        "`.lazyboards.yml`'s custom `S` (Sync) action would block `"S w"` (serve
        watch) — proposing `"R w"` instead. Add it?" Options: "Apply suggested
        additions (Recommended)", "Overwrite fully — regenerate from scratch", "Keep
        as-is — no changes", "Show existing — display the current file". **Apply** and
        **Overwrite** both rewrite the whole file — a local `columns:` list *replaces*
        the global list (it never merges, so a partial in-place patch is impossible):
        merge the user's existing custom actions with the missing recommended ones,
        including the board-level `C`/`X` bindings and the `cleanup:` scalar, and
        carry the existing top-level `provider:`, `repo:`, and `project:` identity
        lines (if present) through unchanged — they are project-identity keys
        lazyboards only reads from the local file. **Keep as-is** leaves the file
        untouched. **Both rewrite paths always emit `keymaps:` form** — including
        for the user's own custom actions carried over, whose concatenated sequence
        keys (`Rw`) are rewritten space-separated (`"R w"`) and whose translatable
        hand-written `tmux new-window` commands are rewritten into
        `window:`/`cwd:`/`focus:` per the delta rule above.
     4. **Delta empty** → do **not** prompt. Emit a small log line
        (`.lazyboards.yml already covers all recommended actions — no changes.`) and
        move on.

     In every case above, record `lazyboards.enabled: true` in config.json, since a
     working board config exists. Whenever this branch **rewrote** the file (case 3),
     run the trust sub-step above as well — a rewrite changes the file's bytes, so
     any trust previously granted for it is void.
   - **Committed, not ignored**: `.lazyboards.yml` is committed (same reasoning as
     `.cenci/Dockerfile` — team-shared and reviewed; `{pr_worktree}` keeps it
     portable). Do **not** add it to `.gitignore`.
