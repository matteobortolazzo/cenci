# Skill authoring — generated-file safety

Conventions for markdown skills that generate or regenerate files committed to a
user's repo (Dockerfiles, CI YAML, config JSON, etc.), especially when values come
from external or semi-trusted sources.

## Rules

- Validate any externally- or semi-trusted-sourced value (e.g. from
  `~/.claude/plugins/installed_plugins.json`, a forked repo's `plugin.json`) against a
  strict format pattern (e.g. `^[0-9]+\.[0-9]+\.[0-9]+$` for a semver) before embedding
  it in a generated artifact that will be built or executed (a Dockerfile `ARG`, CI
  YAML, a shell script). If validation fails, treat the value as unresolved and fall
  through to the documented fallback — never write the raw value.
- Marker-based "merge-safe" regeneration (`# tool:managed-begin` / `# tool:managed-end`)
  must define behavior for malformed marker states too (one marker only, out-of-order,
  duplicate pairs), not just "both present" and "no markers" — route them through the
  same manual Overwrite/Skip/Show conflict prompt used for "no markers," never guess a
  replacement span.
- When resolving an ambiguous choice among multiple candidates read from a shared file
  (e.g. several plugin entries matching a lookup key), never silently pick the first
  match — use a deterministic tie-break rule (e.g. highest semver) and surface which
  candidate was chosen in the user-facing completion summary, not only as an inline
  comment in the generated file.
- Surface fallback/unresolved-value paths in the chat-level completion summary the user
  actually reads, not only as an inline comment in the generated file — a user who never
  opens the generated file should still learn that a value went unresolved and where to
  fix it manually.
- Route all free-text values substituted from user input or external sources into a
  documented shell command template (e.g. `gh issue create --title "Followup: <PR
  title>"`) through the temp-file + read-back pattern (`value=$(cat
  /tmp/claude/value.txt)` then `--flag "$value"`), never inline interpolation — direct
  interpolation allows shell injection via `$(...)` or backticks, in any interpolation
  context, not just message bodies.
- When using the temp-file + read-back pattern, guard against empty reads with
  `[ -n "$VAR" ]` between the `cat` and the external command — a silent write failure
  (zero-length or unwritten temp file) produces an empty string that succeeds with `cat`
  but creates a malformed external command.
- When the same ticket/target could plausibly be operated on by concurrent skill runs,
  scope temp-file paths with a per-run random token (from `mktemp -u`, carried forward
  as literal text, never re-derived from `$$`, which doesn't persist across separate
  Bash tool calls) so concurrent runs can't clobber each other's staged files. Pair the
  token with a per-run success-marker file that a mechanical cleanup gate checks (`cat
  marker.ok`) before deleting anything — narrative-only cleanup justifications
  ("reaching this step means everything succeeded") are not an actual check.
- Every documented external-mutation command flow (`gh issue/pr create`, `edit`,
  `comment`, `label create`, etc.) must include an explicit documented failure branch
  that surfaces the error, stops the flow, or routes to manual intervention — never
  leave the executing agent to proceed with unparsed identifiers, missing output, or
  fabricated values (e.g. a non-existent `#<n>` reference posted to a real GitHub
  comment).
- Don't describe error suppression like `2>/dev/null || true` on `gh label create` as
  "suppresses only the 'already exists' error" — those operators suppress *all*
  failures. State plainly that all errors are suppressed and document where silent
  failures will surface downstream (e.g. "the label will not be found on the next
  step").
- Before directing an agent to decide from a specific artifact (e.g. "based on the
  context-gatherer digest"), verify the artifact actually has the fidelity the decision
  requires — a digest or paraphrase (e.g. the ~40-line context-gatherer summary) is not
  a substitute for verbatim source text. If a decision gate needs fidelity beyond what
  the artifact provides, direct the agent to read the source artifact instead.
- When a summary statement is referenced by name in multiple concrete steps (e.g.
  "shared write-failure protocol: every write in this section must verify by
  re-fetch"), verify its claimed scope actually matches what each step implements —
  narrow the summary or broaden the steps so a scope mismatch doesn't lead a future
  agent to apply it incorrectly.
- When a load-bearing value (e.g. a scoped identifier for temp-file paths) is populated
  through multiple code paths or entry points, apply the same validation at every path
  that sets it, not just the primary one — a hand-edited saved plan file carries the
  same risk as any other external source.
- When validating a scoped path-segment identifier with a character-class allowlist
  (e.g. `^[A-Za-z0-9._-]+$`), the regex alone can still permit directory-traversal
  patterns like `.` or `..` when the value is used as a standalone path segment —
  explicitly reject dot-only values and patterns containing `..` in addition to the
  regex check.
- When adding a new `cat`-as-boolean-gate pattern (e.g. a cleanup marker file check),
  match the exit-code-semantics documentation style of existing analogous gates in the
  document — don't leave interpretation implicit when precedent already spells it out.
- When a filename or path segment is only derivable from a command's response (e.g.
  sniffing a file extension from a Content-Type header), never compute the full
  destination path upfront and then run the command — the response-derived value is
  only available after. Stage to a provisional name first (e.g.
  `attachment-<n>.partial`), capture the response value, derive the final filename,
  then rename to the final path; document both the staging step and the rename.
- When splitting a single-step procedure into multiple sequential steps (e.g. download
  → parse response → rename), give each new intermediate step its own explicit
  failure-handling instruction — a single blanket sentence written for the original
  step doesn't automatically cover steps inserted before or after it.
