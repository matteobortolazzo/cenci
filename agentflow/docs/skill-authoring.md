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
