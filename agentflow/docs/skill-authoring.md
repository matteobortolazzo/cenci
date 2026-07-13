# Skill authoring — generated-file safety

Conventions for markdown skills that generate or regenerate files committed to a
user's repo (Dockerfiles, CI YAML, config JSON, etc.), especially when values come
from external or semi-trusted sources.

## Rules

- When a skill resolves a value from an external or semi-trusted file (e.g.
  `~/.claude/plugins/installed_plugins.json`, a forked repo's `plugin.json`) and embeds
  it into a generated artifact that will later be built or executed (a Dockerfile
  `ARG`, CI YAML, a shell script), validate the value against a strict format pattern
  (e.g. `^[0-9]+\.[0-9]+\.[0-9]+$` for a semver) before writing it anywhere. An
  unvalidated string can inject content (embedded newlines, `#` comments, directives)
  or spoof a structural marker used for safe regeneration. If validation fails, treat
  the value as unresolved and fall through to the documented fallback — never write
  the raw value.
- Marker-based "merge-safe" regeneration (e.g. `# tool:managed-begin` /
  `# tool:managed-end`) must define behavior for every marker state, not just "both
  present" and "no markers present." Explicitly handle malformed states (one marker
  only, out-of-order, duplicate pairs) by routing through the same manual
  Overwrite/Skip/Show conflict prompt used for "no markers" — never guess a
  replacement span. A malformed marker pair can itself be the result of
  attacker-controlled or corrupted content smuggled in via an unvalidated upstream
  value.
- When resolving an ambiguous choice among multiple candidates read from a shared
  file (e.g. several plugin entries matching a lookup key), never silently pick "the
  first match." Use a deterministic tie-break rule (e.g. highest semver) and surface
  which candidate was chosen in the user-facing completion summary — not only as an
  inline comment in the generated file.
- Fallback/unresolved-value paths must be surfaced in the chat-level completion
  summary the user actually reads, not only as an inline comment in the generated
  file. A user who never opens the generated file should still learn that a value
  went unresolved and where to fix it manually.
- When documenting shell command templates in a skill (e.g., `gh issue create
  --title "Followup: <PR title>"`), all free-text values substituted from user
  input or external sources must be routed through the temp-file + read-back
  pattern (`value=$(cat /tmp/claude/value.txt)` then `--flag "$value"`), never
  inline interpolation. Direct interpolation allows shell injection: a PR title
  containing `$(…)` or backticks will be executed as code. This applies to all
  interpolation contexts — not only message bodies, but also flags like `--title`.
- Every documented external-mutation command flow (gh issue/pr create, edit,
  comment, label create, etc.) must include an explicit documented failure branch.
  Never leave the executing agent to proceed with unparsed identifiers, missing
  output, or fabricated values (e.g., a non-existent `#<n>` reference posted to a
  real GitHub comment). Failure branches must surface the error, stop the flow, or
  route to manual intervention — not silently skip the output. A user following
  documented steps should never unknowingly create stale or broken references.
- Do not copy or propagate the phrasing "suppresses only the 'already exists'
  error" when documenting error suppression (e.g., `2>/dev/null || true` on
  `gh label create`). This phrasing is inaccurate — those operators suppress *all*
  failures, not just one. Instead, state plainly that all errors are suppressed
  and document where silent failures will surface as missing-resource errors in
  downstream commands (e.g., "the label will not be found on the next step").
