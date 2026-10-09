# Codex review procedure

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
