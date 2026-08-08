---
name: pencil-api
description: The current Pencil MCP tool surface, `execute` idiom catalog, transport table, and document-discipline rules. Read before any Pencil call, in either `cli-app` or `editor` mode.
user-invocable: false
---

# Pencil API

Single source of truth for the current Pencil MCP tool contract. Read this before any
Pencil call site — it documents the tool surface, the `execute` idiom catalog, how a
tool call is transported in each communication mode, and the document-discipline
rules every read/write batch depends on.

## MCP Surface

The design-consumed subset of the Pencil MCP surface is five tools:

| Tool | Parameters | Notes |
|---|---|---|
| `get_app_state` | `{ include_schema?: boolean }` | Returns session orientation: the active document's file path (or empty if no document is open) and other top-level editor state. Omit `include_schema` (or pass `include_schema: false`) for the cheap form — this is what a document-identity check uses, since it doesn't need the schema payload. Pass `include_schema: true` when the call also needs the full `.pen` schema plus `execute` function docs (e.g. component discovery). Use it to verify document identity before a read/write batch — not as a cheap connectivity probe (that's `execute`, see below). |
| `execute` | `{ input: '<Pencil-script>' }` | Runs a Pencil-script string against the active document and returns whatever the script `Print`s. This is the general-purpose read/write primitive — see the idiom catalog below for its common shapes. |
| `get_screenshot` | `nodeId` | Returns a screenshot of the given node inline (editor/MCP mode only — CLI mode uses `export_nodes` instead, since heredoc responses can't carry inline images). |
| `export_nodes` | `{ nodeIds: [...], outputDir: "<path>", format: "png" }` | Writes screenshots of the given nodes to disk at `outputDir`. The CLI-mode equivalent of `get_screenshot`. |
| `get_guidelines` | `{ topic }` or `{ category, name?, params? }` | Retrieves design guidance (design-system rules, style definitions, per-design-type topics). |

The MCP surface also carries `export_html` and `browser` tools; neither has a call
site in `design`, `implement`, or `verify-ui`, so they are out of scope for this
document.

## `execute` Idiom Catalog

`execute` takes a single Pencil-script string. These are the shapes this skill's
call sites use:

- **Top-level inventory** — list every top-level node without descending into
  children: `Get((n,c)=>{c.skipChildren();Print(n.id,n.name)})`
- **Reusable-component discovery** — list every reusable node: `Get(n=>n.reusable&&Print(n.id,n.name))`
- **Subtree read** — print a node and a bounded depth of its descendants: `Print(Get("<id>",{depth:3}))`
- **Layout check** — visit a subtree and print only nodes carrying a layout
  problem: `Get(<root>,(n,c)=>c.problems&&Print(n.name,c.problems))`
- **Read theme variables**: `Print(GetVariables())`
- **Write theme variables**: `SetVariables({...})` — always a merge onto the
  existing variable set. **Never** pass `replace: true`: a replace wipes every
  variable not present in the call's own payload, destroying the document's
  existing theming instead of extending it. There is no legitimate call site for
  `replace: true` in this codebase.
- **Insert a reusable component by reference**: `Insert("<parent>",{type:"ref",ref:"<Comp>",width:"fill_container"})`
- **Generate an image**: `Generate(nodeId,"ai"|"stock",…)`
- **Find empty canvas space** for placing a new screen without overlapping
  existing content: `FindEmptySpace`

**Validate document-derived values before interpolating them.** Node IDs and
component/screen names read back from the `.pen` document (e.g. via the
top-level inventory or reusable-discovery idioms above, or from an
`Insert("<parent>",{type:"ref",ref:"<Comp>",…})` call site) get interpolated
into the `execute` script strings above — and, in `cli-app` mode, further
into a quoted CLI heredoc. Per `docs/skill-authoring.md`, treat any such
value as semi-trusted: before substituting it into an idiom above, validate
it against one of two strict allowlist patterns, chosen by what the value is
and where it lands:

- **Node IDs** — including anywhere a node ID is interpolated into a
  filesystem path, such as the `design` skill's Step 4A `outputDir`/`Read`
  targets — use the narrower `^[A-Za-z0-9:._-]+$` (letters, digits, `:`,
  `.`, `_`, `-` — no `/`, no space, no quotes, no backslash, no newline, no
  `$`, no backtick, no `{`/`}`). A node ID is never legitimately
  path-separator-bearing, so excluding `/` here closes off path traversal
  through a document-derived ID (e.g. `../../home/user/private`) landing in
  a filesystem path.
- **Component/screen names** — use the wider `^[A-Za-z0-9:/._ -]+$` (adds
  `/` and space to the node-ID pattern). This pattern admits real Pencil
  component/screen names, which use `/` as a namespace separator (e.g.
  `Component/ExerciseCard`, `Screen/training-plan` — see the `design`
  skill's Phase 5 Step A), while still excluding every dangerous character.

If a value fails its pattern, reject it and abort the call site rather than
interpolating it; never silently strip or escape the offending characters.
Tell the user which name/ID failed validation and that they should rename it
in Pencil (avoiding quotes, backslashes, or newlines) before re-running.

Separately, also reject a value that is exactly the literal string `EOF`, or
that contains `EOF` as a standalone line — the char-class pattern above
doesn't catch this on its own, since `EOF` matches it, but in `cli-app` mode
a bare `EOF` line would terminate the quoted `<<'EOF' ... EOF` heredoc used
to transport the call (see the transport table below) early, letting the
rest of the value be interpreted as new shell/CLI input. Reject and abort the
call site the same way as a char-class failure — never strip or escape.

## Transport Table

Pencil tool calls are transported differently depending on `pencil.mode`
(`$PENCIL_MODE`), resolved from config:

| Mode | Transport |
|---|---|
| `editor` (legacy MCP fallback) | One `mcp__pencil__<tool>` call per invocation — e.g. `mcp__pencil__execute` with `{ input: '<Pencil-script>' }`. |
| `cli-app` (default for new installs) | A `pen interactive -a desktop` heredoc using the Bash tool, with one `tool_name({ key: value })` call per line inside the heredoc. Batch multiple independent calls into a single heredoc; split into separate heredocs at decision boundaries, where output must be read before choosing the next action. |

**`cli-app` mode specifics**:

- `save()` and `exit()` are shell-only commands inside the interactive session —
  they have no MCP equivalent and are never called from `editor` mode.
- Never pass `-i` in app mode (targeting the desktop app via `-a desktop`): `-i`
  injects a session-level `filePath`, but if it's passed while connected to the
  desktop app (app mode), the app silently ignores it and keeps operating on its
  own currently-open document — no error is raised, so a call intended for a
  different file silently targets whatever is already open instead.
- Headless mode (no desktop app, used by `implement`/`verify-ui` inside the
  sandbox) requires the `-o` output flag; documenting the full headless invocation
  contract is out of scope here and tracked for a future ticket (#958).

**Which `pen` is on PATH**: two different binaries can answer to the name `pen`,
and which one runs matters for every command above. The npm-distributed CLI
(`@pen.dev/cli`) provides the `interactive` subcommand this transport table
describes, including its own `--help` output. A desktop-app-installed symlink
named `pen` instead launches the GUI application on any argument it doesn't
recognize — including a bare `pen` with no arguments, or `pen --help`. Detecting
which one is on PATH (e.g. `configure`'s `pencil.mode` probe) must check for a
signal unique to the npm CLI's help output, not just a zero exit code, since the
desktop symlink's GUI-launch behavior can also exit 0.

**Verified against**: `@pen.dev/cli 0.3.2`, verified 2026-08-08.

## Document Discipline

Pencil operates on exactly one open document at a time — there is no tab/window
concept to disambiguate a target document from context.

- **No per-call `filePath` parameter exists on any of the 5 MCP tools** (see the
  MCP Surface table above) — no tool call can pass a path to target a specific
  document. The only `filePath`-like mechanism is the CLI's `-i` flag, which is
  session-level (applies to every request in the session, not a single call) and
  normally used in headless mode (see the transport table's `cli-app` mode
  specifics above — never passed in app mode). If `-i` is passed in app mode
  anyway, despite that rule, the app-held document takes precedence: the
  injected session `filePath` **silently falls back to whatever document is
  currently open** when the value doesn't match it — it does not error. A read
  or write aimed at the wrong document because of this fallback is
  indistinguishable from a stale node-ID error: both just return unexpected
  data. Always verify document identity with `get_app_state` before every
  read/write batch, rather than trusting a `filePath` value to have taken
  effect.
- **No tool in the current surface creates or opens a document.** Opening an
  existing `.pen` file or creating a new one is always a human action taken
  directly in the Pencil app — never something a skill can do on the user's
  behalf. A skill that needs a specific document open must ask the human to open
  or create it, then re-verify with `get_app_state`.
- **There is no MCP save.** Every edit made via `execute` exists only in the
  open editor's in-memory state until a human explicitly saves (Cmd/Ctrl+S) in
  the app. Any step that reads from disk afterward — `git add`, `git diff`, a
  fresh `Read` of the `.pen` file's committed bytes — captures a stale file if
  the human hasn't saved yet. Always prompt for a manual save before any file
  switch, copy, or commit that depends on the latest in-editor state.
