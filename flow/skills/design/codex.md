# Codex design procedure

Read `project-core` and `codex-runtime`. This workflow is optional: require configured
Pencil CLI/MCP dependencies. In `/plan`, gather UX decisions and approve the design scope;
in normal mode update the `.pen` file and DESIGN.md, visually verify, checkpoint results,
and perform the existing design-ticket label transition.

**Sandbox guard (host-only)**: `$cenci:design` is host-only — the Pencil desktop app it
drives is never reachable inside the cenci sandbox. Before any Pencil probe or
auto-launch, detect an in-container session the same two-step way as
`configure/scripts/detect-project.sh`: check `CENCI_SANDBOX` (prints `1` → in
container), then fall back to `/.dockerenv` (present → in container). If either
matches, stop immediately and report: the Pencil desktop app is not reachable from
inside the cenci sandbox — exit the container and re-run `$cenci:design` on the host.
In-sandbox sessions only get design access through headless reads via
`$cenci:implement` and `verify-ui`, never through this workflow.

**Command surface (least privilege)**: this workflow's own procedure performs no remote fetches of its own — neither a `curl` grant nor a web-fetch capability, and the procedure invokes neither. (Attachment downloads via the `attachments` reference skill may still use `curl` when the user selects an attachment; that call falls outside this narrowed grant and will prompt for approval — an accepted tradeoff, not a regression.) Its `gh` surface is limited to exactly five `gh issue` verbs — `view`, `edit`, `comment`, `list`, `close` — and no other verb (`create`/`transfer`/`develop` are not granted), plus `gh label create …` and `gh api user --jq …` (via `ticket-ownership`); its `git` surface is limited to `git remote get-url`, `git add`, `git commit`, and `git rev-parse`. The only temp-dir primitive is a standalone `mktemp -d` call (never an assignment wrapper or `$(...)` command substitution — the printed path is carried forward as literal text, never shell state). Stage and commit the design artifacts as **two standalone commands** — never a `git add … && git commit …` compound — since approval rules in both clients evaluate each segment of a compound independently (see `shell-rules`).
