# Codex review procedure

Before accepting reviews or acting on findings, apply the security and silent-failure
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate a bounded
second opinion to `domain-specialist` with the existing reviews and source paths;
validate its evidence using the rules below and retain all required normal reviews.

Read `project-core`, `codex-runtime`, and `subagent-safety`. Gather the diff once, then use
the generated code/security reviewer adapters (or equivalent built-in workers) for bounded
parallel review. Validate every finding against the diff and report only actionable issues;
do not mutate code unless explicitly requested. A worker's finding is a claim, not evidence — per
Anthropic's Opus 5.5 guidance for subagent fan-out, when a subagent reports back, check its evidence before you accept it:
before presenting any Critical/High/Must Fix finding (or, when a fix is explicitly requested,
before applying one), read the cited file and line yourself, and accept it only once that read
confirms the finding holds against the actual code. A finding that does not hold is not reported
as critical or fixed; list it as a one-line `Considered and discarded` entry (what was claimed,
why it does not hold) so the rejection stays visible. Lower-tier findings are reported as-is.

In PR mode, grade acceptance before code: resolve the PR's ticket (`Fixes`/`Closes`/`Resolves`
`#<n>`, else `Related to #<n>`; none → note `Acceptance: n/a` and review code only), fetch its
body and locate its design record (`.plans/<n>-*.md` or `.plans/done/<n>-*.md`), then delegate
the `acceptance-reviewer` role with the ticket body, the design record and the PR — never the
diff alone — and persist its `## Acceptance Review` table. Score it with
`"${PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" <table> [--ui]` (`--ui` for a UI
ticket: gallery and secret-gist screenshots in en-US and it-IT are required evidence, and a
diff-only review of a UI ticket fails). A fail (any partial or missing item, or missing UI
evidence) returns the PR to implementation before code review starts: report
`NEEDS_WORK (acceptance)` with the blocking rows and do not run the code reviewers. Only a pass
continues into the code review above; the report embeds the table under `### Acceptance`.
