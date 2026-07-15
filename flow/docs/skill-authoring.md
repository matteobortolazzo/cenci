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
- When using the temp-file + read-back pattern, guard against empty reads with
  `[ -n "$VAR" ]` between the `cat` and the external command (e.g.,
  `TITLE=$(cat /tmp/claude/title.txt) && [ -n "$TITLE" ] && gh issue create --title "$TITLE" …`).
  A silent write failure (zero-length file or unwritten temp file) will produce
  an empty string that succeeds with `cat` but creates a malformed external
  command; the guard provides fail-fast detection.
- When the same ticket/target could plausibly be operated on by concurrent skill
  runs, scope temp-file paths with a per-run random token (generated once via
  `mktemp -u`, carried forward as literal text, never re-derived from shell state
  such as `$$`, which does not persist across separate Bash tool calls) so
  concurrent runs cannot clobber each other's staged files. This aligns with the
  CLAUDE.md rule that already mandates scoping shared temp files "by worktree
  path, run ID, or session UUID." Pair the token with a per-run success-marker
  file that a mechanical cleanup gate checks (e.g. `cat marker.ok`) before
  deleting anything — narrative-only cleanup justifications ("reaching this step
  means everything succeeded") are not an actual check.
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
- When skill instructions direct the executing agent to make a decision over a specific artifact (e.g., "based on the context-gatherer digest," "reading the bundle's summary"), verify what that artifact actually contains before assuming it has the fidelity your logic requires. A digest or paraphrase (e.g., the ~40-line context-gatherer summary) is not a substitute for verbatim source text. If a decision gate requires fidelity beyond what the artifact provides (e.g., checking for exact phrasing in a ticket body), explicitly direct the agent to read the source artifact or fetch the original data — do not rely on assumptions about what an abbreviated form captures.
- When authoring a general/summary statement that will be referenced by name in multiple concrete implementation steps (e.g., "shared write-failure protocol: every write in this section must verify by re-fetch"), verify that the statement's claimed scope actually matches what each concrete step implements. Scope mismatch creates an internal contradiction — e.g., a blanket claim "all writes must X" but some steps only implement "Y as proof of success" — that could lead a future agent to follow the summary literally and patch steps incorrectly. Either narrow the summary's scope to match reality (e.g., "most writes must X; ticket creation verifies by return URL") or broaden implementations to match the claim.
- When a load-bearing value (e.g., a scoped identifier for temp-file paths or directory segments) is populated through multiple code paths or entry points, apply the same validation check at every path that sets the value, not just the primary/first-written one. For example, if a slug is both freshly-generated in one path and read from a saved plan file in another, validate it at both points — an unvalidated entry point (e.g., a hand-edited plan file) carries the same risk as any other external source.
- When validating a scoped path-segment identifier with a character-class allowlist (e.g., `^[A-Za-z0-9._-]+$` to reject `/` and shell metacharacters), trace how the value is consumed downstream. The regex alone may still permit directory-traversal patterns like `.` or `..` when the value is used as a standalone path segment (e.g., `attachments/<scope>/`). Explicitly reject dot-only values (`.`, `..`) and patterns containing `..` in addition to the regex check — do not assume the character-class alone is sufficient.
- When adding a new `cat`-as-boolean-gate pattern to a skill (e.g., a cleanup marker file check, a success/completion gate), inspect the skill document for existing analogous gates. If precedent exists with explicit exit-code-semantics documentation (e.g., "if `cat` fails (non-zero exit, `No such file or directory`), the marker is absent; if it exits 0, the marker is present"), match that documentation style in the new gate — do not leave exit-code interpretation implicit. Consistency within a document prevents future readers from inferring different semantics for structurally identical patterns and avoids creating ambiguity about what a gate actually checks.
- When a skill derives part of a filename or path from a value only available in a command's *response* (e.g., sniffing a file extension from an HTTP Content-Type header, extracting a version from a server response), never design a single-step template that computes the full destination path upfront and then runs the command. The destination path must be known before the command executes (e.g., for `curl -o <path>`), but the response-derived value is only available after. Break the circular dependency by staging: write to a provisional/temporary name first (e.g., `attachment-<n>.partial`), capture the response value (e.g., Content-Type), derive the final filename, then rename to the final path. Document both the staging step and the rename, not just the download.
- When splitting a single-step procedure into multiple sequential steps (e.g., download → parse response → rename), each new intermediate step introduced must have its own explicit failure-handling instruction — do not assume that a single failure-handling sentence written for the original step automatically covers new steps inserted before or after it. For example, a procedure that grows from "download <url> to <file>" into "download to <provisional>; capture Content-Type; map to extension; mv to <final>" requires documented failure branches for the download (e.g., HTTP errors), the capture (e.g., missing Content-Type header), the mapping (e.g., unmapped MIME type), and the rename (e.g., permission denied, target exists) — not a single blanket "if download fails, stop." Reviewers should specifically check for this coverage when a procedure is decomposed.
