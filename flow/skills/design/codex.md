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
