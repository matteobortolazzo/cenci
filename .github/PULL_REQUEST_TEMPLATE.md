## What & why

<!-- What does this PR change, and why? Link the ticket (e.g. Closes #123). -->

## Plugin(s) touched

<!-- cenci / cenci-watch / cenci-sandbox / repo-wide (docs, CI, etc.) -->

## Commit title reminder

The **PR title / squash-merge commit** drives the automated version bump for any plugin
whose paths it touches — use a [conventional commit](../CONTRIBUTING.md#conventional-commits-and-release-impact)
type:

- `feat: ...` → minor bump
- `feat!: ...` or a `BREAKING CHANGE:` footer → major bump
- `fix:`, `refactor:`, `test:`, `docs:`, `chore:` → patch bump

See the versioning table in [`AGENTS.md`](../AGENTS.md#versioning) for how bumps map to
each plugin's `paths:` filter.

## Test evidence

<!-- Commands run and their output/pass status (e.g. `make test`, `go test ./...`,
     shellcheck, manual smoke test). For docs/config-only changes, note the validation
     performed (link checks, YAML syntax, etc.). -->
