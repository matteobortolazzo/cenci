# Autonomy Settings

Read this file when `planning`, `automerge`, `refinement`, `refinement.confirmBeforeApply` or `refinement.automerge` is asked this run (see `SKILL.md` `### Step Map`). It holds questions 13 to 17 with the marker-bounded `jq` programs that step 6 in `SKILL.md` applies.

### Autonomy Settings

**Repo questions 13–17 have no container guard**: unlike `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`,
`planning`, `automerge` and `refinement` are repo-committed keys that
land inside this run's PR, so an in-container run is fine. The questions below only
collect an answer; none writes anything itself — step 6 in `SKILL.md` performs the actual
write, overlaying whatever was collected here onto the merged config.

**Absent-only rule (delta runs).** On a delta run, questions 13, 14 and 15 are asked only when the key is absent and the ID is not in `configureOffered`; nothing is written on a skip. On a delta run, when `planning`, `automerge` or `refinement` is already present, report the
existing value verbatim, never re-prompt, never narrow, and never remove it.

**`--all` re-offers.** `--all` is the one way to change or remove a present value, and it
always shows the current value first. Under `--all`, questions 13, 14 and 15 are each re-offered even when `planning`, `automerge` or `refinement` is present, with the Keep option listed first and pre-selected per the keep-or-change rule in `SKILL.md`'s `### Question Defaults`. When the key is absent, `--all` asks the ordinary question below. Declining or keeping never writes anything but the `configureOffered` ledger stamp (step 6).

**Resolver-failure `full` runs do not re-offer.** A `full` run caused by a resolver failure (see `### Delta Resolution` in `phases/detection-and-delta.md`) is not an `--all` run: for a present `planning`, `automerge` or `refinement` key it behaves like a delta run — report the existing value verbatim and do NOT re-offer it, so a fallback can never reach the destructive "Remove the block(s)" option. Only a leading `--all` token re-offers a present value. An absent key is asked as the ordinary question.

**Presence detection**, read from `existingConfig` (already in hand from `### Existing Config
Detection` in `SKILL.md`):
- `planning` is present iff `existingConfig.planning` exists.
- `automerge` is present iff a top-level `automerge` key exists or any `projects[]` entry
  carries its own `automerge` key — monorepo automerge can be per-project only, so a
  top-level-only check would wrongly re-offer the question on a repo whose policy lives
  entirely in `projects[]`.
- `refinement` is present iff `existingConfig.refinement` exists.

13. **Plan approval autonomy** (asked via `AskUserQuestion` when `planning` is
    absent and, on a delta run, `planning` is in `delta.ask`; under `--all` it is also
    asked when `planning` is present, in the keep-or-change form below): "Should plan approval stay interactive, or switch to lean self-approval?
    `interactive` resolves repo-grounded choices, asks only consequential or unresolved
    questions through the question tool (up to 6, never a quota), and stops for plan review. `lean` lets a plan with no escalations continue straight into
    implementation in the same session, and also authorizes `cenci dispatch`'s unattended
    planning pickup and autonomous re-plan once pushed to `main` (see
    `docs/autonomous-loop.md`'s \"Let plans approve themselves\")."
    - Options: "Keep `interactive` (Recommended)", "Switch to `lean`"
    - Declining omits the `planning` key entirely — no `planning` key is written.
    - **`--all` with `planning` present** (per the keep-or-change rule in `SKILL.md`'s `### Question Defaults`): ask "`planning.autonomy` is currently
      `<current>`. Keep it or switch?" with the options "Keep current (Recommended)" and
      one switch option for the other value — "Switch to `interactive`" when the committed
      value is the exact string `lean`, "Switch to `lean`" otherwise (a value other than
      the exact string `lean` is treated as `interactive`, so it pre-selects Keep and
      offers the switch to `lean`). Keeping the current value writes nothing. Switching to `interactive` writes `planning.autonomy: "interactive"` explicitly, preserving sibling keys.
    - **Overlay program.** Every Q13 write — the switch above, and the ordinary
      "Switch to `lean`" answer when `planning` is absent — is this fixed program, applied
      to the candidate config with `--arg autonomy interactive|lean` and validated in
      step 6 (exit 0 and `jq -e 'type == "object"'`); it keeps every sibling key of `planning`:

<!-- cenci:configure-q13-switch:begin -->
```jq
.planning = ((.planning | if type == "object" then . else {} end) + {autonomy: $autonomy})
```
<!-- cenci:configure-q13-switch:end -->

14. **Automerge policy block** (asked via `AskUserQuestion` when `automerge` is
    absent and, on a delta run, `automerge` is in `delta.ask`; under `--all` it is also
    asked when `automerge` is present, in the keep-or-remove form below): "Scaffold a starter `automerge` policy block for this repo? This does not
    enable automerge by itself — it only seeds a deny-by-default policy that a human must
    still review, and the fleet-wide `automerge.enabled` switch (see
    `docs/autonomous-loop.md`) stays off until you flip it separately. This section never
    invents a risk policy field-by-field — it's a binary choice between no block and a
    conservative starter block."
    - Options: "Skip — no block (deny-by-default) (Recommended)", "Scaffold a starter
      block for hand review"
    - Declining omits the `automerge` key entirely — no `automerge` key is written.
    - On "Scaffold a starter block for hand review", seed `protectedPaths` from the union
      of three sources, deduplicated, additive only, never narrowing any of them: (a) the
      **full** mandatory baseline of category globs — the complete implement Sensitive-path
      backstop default pattern list (`flow/skills/implement/SKILL.md`'s built-in defaults,
      all 36 patterns, verbatim), plus the release/CI path; (b) any confirmed repo-shape
      paths (e.g. `.cenci/`); and (c) `existingConfig.security.sensitivePaths` when that key
      is present — the repo owner's own hand-declared sensitive-path patterns (see
      `security.sensitivePaths` elsewhere in `SKILL.md`), the one signal the built-in
      defaults can't cover by design. Set tight caps. The template below is the value
      written to the top-level `automerge` key, verbatim (with `protectedPaths` extended
      per repo shape and `existingConfig.security.sensitivePaths` when applicable):

<!-- cenci:automerge-scaffold:begin -->
```json
{
  "protectedPaths": [
    "*auth*",
    "*login*",
    "*logout*",
    "*session*",
    "*password*",
    "*passwd*",
    "*credential*",
    "*secret*",
    "*secrets*",
    "*token*",
    "*jwt*",
    "*apikey*",
    "*api_key*",
    "*.pem",
    "*.key",
    "*.env*",
    "*oauth*",
    "*sso*",
    "*saml*",
    "*openid*",
    "*permission*",
    "*acl*",
    "*rbac*",
    "*role*",
    "*crypto*",
    "*encrypt*",
    "*decrypt*",
    "*sign*",
    "*hash*",
    "*payment*",
    "*billing*",
    "*invoice*",
    "*checkout*",
    "*stripe*",
    "*migrat*",
    "*schema*",
    ".github/",
    ".cenci/"
  ],
  "maxChangedFiles": 10,
  "maxDiffLines": 400,
  "mergeMethod": "squash"
}
```
<!-- cenci:automerge-scaffold:end -->

      In a monorepo, this scaffold is always written as a single top-level block (never
      per-`projects[]` blocks) — a complete, valid, most-restrictive policy on its own
      that needs no per-project path derivation; the PR-body review flag (step 7 in `SKILL.md`) tells the
      human to consider splitting it into per-project blocks if that fits the repo better.

    - **`--all` with `automerge` present** (top-level or in any `projects[]` entry; per the keep-or-change rule in `SKILL.md`'s `### Question Defaults`): never
      offer the scaffold. Ask "An `automerge` policy block is already present. Keep it or
      remove it?" with the options "Keep current block(s) (Recommended)" and "Remove the block(s)".
      Keeping leaves every `automerge` block byte-identical — never removed and never narrowed. "Remove the block(s)" deletes the top-level `automerge` key and the `automerge` key of every `projects[]` entry. Nothing else changes. Removal is the one
      path that deletes a policy, so it always adds the `## Review required — automerge
      policy removed` section to the PR body (step 7). The removal is this fixed program,
      applied to the candidate config and validated in step 6 (exit 0 and `jq -e 'type == "object"'`):

<!-- cenci:configure-q14-remove:begin -->
```jq
del(.automerge) | if (.projects | type) == "array" then .projects |= map(if type == "object" then del(.automerge) else . end) else . end
```
<!-- cenci:configure-q14-remove:end -->

15. **Refinement autonomy** (asked via `AskUserQuestion` when `refinement` is absent and, on a delta run, `refinement` is in `delta.ask`; under `--all` it is also asked when `refinement` is present, in the keep-or-change form below): "Should `/cenci:refine` keep its full question interview, or switch to lean refinement?
    `interactive` asks only consequential or unresolved questions. `lean` asks one round of at most 3 questions and records grounded decisions as A1…An in the refinement summary (see
    `docs/autonomous-loop.md`'s \"Ask fewer questions at refinement\"). Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently."
    - Options: "Switch to `lean` (Recommended)", "Keep `interactive`"
    - Question 15 deliberately departs from question 13's recommend-interactive pattern: the owner wants lean refinement as the normal mode, so the offer recommends `lean`. "Switch to `lean` (Recommended)" applies only to the ordinary form, when `refinement` is absent; the runtime default is unchanged, because an absent `refinement` key still resolves to `interactive`.
    - Declining omits the `refinement` key entirely — no `refinement` key is written.
    - **`--all` with `refinement` present** (per the keep-or-change rule in `SKILL.md`'s `### Question Defaults`): ask "`refinement.autonomy` is currently
      `<current>`. Keep it or switch?" with the options "Keep current (Recommended)" and
      one switch option for the other value — "Switch to `interactive`" when the committed
      value is the exact string `lean`, "Switch to `lean`" otherwise (a value other than
      the exact string `lean` is treated as `interactive`, so it pre-selects Keep and
      offers the switch to `lean`). Keeping the current value writes nothing. Switching to `interactive` writes `refinement.autonomy: "interactive"` explicitly, preserving sibling keys.
    - **Overlay program.** Every Q15 write — the switch above, and the ordinary
      "Switch to `lean` (Recommended)" answer when `refinement` is absent — is this fixed program, applied
      to the candidate config with `--arg autonomy interactive|lean` and validated in
      step 6 (exit 0 and `jq -e 'type == "object"'`); it keeps every sibling key of `refinement`:

<!-- cenci:configure-q15-switch:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {autonomy: $autonomy})
```
<!-- cenci:configure-q15-switch:end -->

16. **Refinement confirmation** (independent ID `refinement.confirmBeforeApply`): ask via
    `AskUserQuestion` when this leaf is absent and, on a delta run, its ID is in `delta.ask`; `--all`
    re-offers it with the current value first. An existing `refinement` object or
    `refinement` ledger entry never suppresses this question. "After the questions are
    resolved, should refinement apply immediately or require a final confirmation?"
    Options: "Apply immediately (Recommended)" (`false`), "Require confirmation" (`true`).
    Store the boolean as `confirmBeforeApplyAnswer`, including explicit `false`.
    Missing resolves to `false`; malformed values resolve to `true` with a notice.
    A carried leaf is preserved verbatim, including false; on resolver-failure `full`
    runs, present leaves are carried and only `--all` re-offers them.

<!-- cenci:configure-confirm-before-apply:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {confirmBeforeApply: $confirmBeforeApply})
```
<!-- cenci:configure-confirm-before-apply:end -->

17. **Refinement merge grants** (independent ID `refinement.automerge`): ask via
    `AskUserQuestion` under the same leaf-level delta/`--all` rules as question 16.
    "May refinement grant hands-off merge to tickets that pass its eligibility checks?"
    Options: "Off (Recommended)" (`off`), "Grant for eligible tickets" (`eligible`).
    Store the enum as `refinementAutomergeAnswer`. Missing or malformed values resolve
    to `off` (malformed values produce a notice). This choice is independent of
    `planning.autonomy`, `refinement.autonomy`, and confirmation. It never changes the
    existing top-level or per-project `automerge` risk policy. Choosing off writes
    `refinement.automerge: "off"` explicitly and never removes that unrelated policy.
    `eligible` grants only when `refinementAutomerge` is `eligible`, the ticket's
    Automation verdict is `grant`, `browserRequired` is false, and no visual-check
    pattern matches. Evaluate each ticket independently, including split parents and
    every split child; a parent's grant never transfers to a child.
    Babysit still requires the fleet switch, repository risk policy, CI, and its other gates.

<!-- cenci:configure-refinement-automerge:begin -->
```jq
.refinement = ((.refinement | if type == "object" then . else {} end) + {automerge: $automerge})
```
<!-- cenci:configure-refinement-automerge:end -->
