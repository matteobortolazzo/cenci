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
- When prose claims a generated artifact is "drawn from," "based on," or "sourced from" another location (e.g., a starter config "seeded from the implement skill's backstop default pattern list"), verify complete fidelity by exhaustively comparing the artifact against the source — not by sampling or spot-checking. Test assertions claiming full coverage must validate every element in the claimed source set, not a representative sample. A narrowed scaffold that samples from the source yet claims comprehensive derivation is a silent correctness gap, only detectable by independent review counting all elements. During PR review, verify that both the generated artifact and its test suite fully reflect the claimed source.
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
- **Canonical: `gh api ... --input` for title/body-carrying writes.** Route every write
  that sets a title (issue/PR create, or an edit that also changes the title) through
  `gh api <endpoint> -X <METHOD> --input <json-file>`, where `<json-file>` is a JSON
  payload mechanically composed with `jq -n --rawfile` from raw title/body files a file
  tool (e.g. `Write`) authored as plain text — never hand-interpolated shell text and
  never a hand-escaped JSON literal. This has zero shell interpolation: the raw
  title/body files are the only externally-sourced input, and `jq` cannot let
  `--rawfile` content influence the payload's *structure* — see the `shell-rules`
  skill's canonical snippet for the exact three-step procedure.
- **Retired: temp-file + read-back for a body-only `gh api -f` write.** `address-review`'s
  Posting Replies previously sent inline PR reply text through
  `gh api ... -f body="$REPLY"` via a temp-file + read-back (`value=$(cat
  /tmp/claude/value.txt)` then `--flag "$value"`). #773 migrated it onto the same
  `jq -n --rawfile` + `gh api ... --input` pattern as the canonical bullet above —
  `gh api` accepts `--input` for a body-only payload the same as a title-carrying one, so
  this CLI-specific fallback is no longer needed for that site. No flow skill currently
  needs a genuinely `--input`-less body flow; a plain body-only write with no title
  should prefer `--body-file` directly (`gh pr comment ... --body-file`, `gh issue edit
  ... --body-file`) over hand-composing JSON. If a future CLI surface truly has neither
  `--input` nor a body-file flag, only then fall back to the temp-file + read-back
  pattern above, and guard against empty reads with `[ -n "$VAR" ]` between the `cat` and
  the external command — a silent write failure (zero-length or unwritten temp file)
  produces an empty string that succeeds with `cat` but creates a malformed external
  command.
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
- Skill sections with sequential file writes must show exact command templates (with
  session-uuid and run-ID scoping) in code blocks, document verify-before-continuing
  or recovery handling for every file write and external call, and use consistent
  verification patterns across all steps in the section. Don't rely on prose
  descriptions alone for critical paths — enforce consistency by example.
- When writing a contract test for a skill file, never weaken the skill's authoritative
  prose or documented patterns (e.g. a "Convention" section's code example) to satisfy
  the test's search logic. Instead, scope and refine the test's predicates — e.g., if a
  test needs to find a guarded invocation of `pencil interactive -a desktop` but the
  file also legitimately mentions that pattern in an earlier Convention section, scope
  the test's grep to search only from the guard's phase heading onward (using `tail -n
  +<line_number>` plus line-offset math), not by deleting `-a desktop` from the earlier
  section. Weakening prose to dodge naive whole-file matching creates a silent
  regression: an agent following the skill literally will miss the stripped-out `-a
  desktop` flag and silently fail. Self-referential edits (modifying both the skill
  file and its own contract test in the same change) deserve extra scrutiny in review
  because a test-logic regression and a prose regression can be hard to tell apart at a
  glance.
- When a skill's gate or authorization logic defines multiple boolean signals or decision
  points, ensure each signal's prose description is distinct and unambiguous — e.g.,
  "per-child frontend status" (childIsFrontend) vs. "design-only child from a split"
  are distinct roles and must not be described as equivalent or interchangeable. Contract
  tests that check for gate-step text presence cannot detect semantic confusion between
  signals. Reviewers must verify that each gate step wires the *intended* signal, not a
  similarly-named alternative, especially in security-relevant gates where signal-wiring
  errors can silently allow or withhold access incorrectly. This pattern parallels the
  #824 rule ("validate then use") but applies to signal *selection* rather than
  validate-vs-hardcode: the gate may be syntactically complete yet wire the wrong boolean
  signal (#848).
- When editing multiple skill/procedure documentation files that describe the same behavior from different levels of abstraction (e.g., a high-level session-shape summary in SKILL.md and a detailed authoritative procedure in phase-1-plan.md), cross-check them for factual consistency before committing. Specifically verify that each outcome or branch described in the summary actually matches what the authoritative procedure specifies for that case — e.g., if the summary states "records nothing," verify the procedure doesn't document persistence steps, state transitions, or downstream calls for that path. Summary-vs-procedure inconsistencies can only be caught by cross-file comparison, not by testing or reviewing each file independently, and will silently mislead future readers and implementers.
- In guard clauses and early-exit branches, use explicit directional language (e.g., "skip to section [name]" or "continue directly to the next section") instead of ambiguous terms like "fall through." When the same document uses "fall through" elsewhere to mean "proceed normally" (the opposite), reusing it in a guard context risks an agent or reviewer misreading the guard's semantics, especially in security-relevant gates. Contract tests should pin the corrected wording to prevent silent regression (#928).
