# Plan fidelity — implementation vs. stated intent

Conventions for cross-checking an implementation against every section of its plan or
ticket — not just the literal "Files to Modify" wording — before treating the plan as
fully satisfied.

## Rules

- When implementing a pattern or check that already exists elsewhere in the same file or codebase (e.g., a grep check, a validation guard, error-handling convention), audit existing examples first to match established conventions — do not implement solely from a plan description. This includes error-handling specifics like stderr redirection; implementing a similar check without those details is a silent failure that a reviewer is more likely to catch than an implementer.
- When implementing a feature from a plan with Assumptions or Risks sections, cross-check that your literal implementation aligns with those sections' stated intent, not just the Files to Modify wording. If different sections contradict (e.g., intent says "empty label is not a drift case" but Files to Modify lists a condition that treats any mismatch as drift), flag the contradiction for clarification before committing — do not resolve it by choosing the most literal section (#493).
- When implementing a child ticket within a parent epic that specifies 'Key design decisions' or similar intent-setting context, verify that documentation examples and placeholder values align with that parent epic's stated purpose — do not rely solely on generic plausibility (e.g., HTTP endpoints, standard test frameworks) that might contradict the parent context (e.g., offline-only checks) (#505).
- In fallback logic where ground truth is unrecoverable (lost context, inaccessible data, compacted state), distinguish sharply between 'what default label/value is safe to report' vs. 'can we assert that a specific claim actually occurred'. Never default to asserting a false-assuring claim (e.g., "security review done") when you cannot verify it — instead report the gap honestly (e.g., "status unknown, verify manually") (#525).
