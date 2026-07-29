# Adapter behavioral-parity contract

Ticket #524 (child of #517). This is the property table
`flow/tests/parity/parity.test.sh` checks: eight safety-critical properties
every implement-pipeline client adapter (currently Claude Code and Codex;
OpenCode is out of scope until #517's OpenCode child slice) must exercise,
via deterministic control points rather than live LLM runs.

Two properties (`red-before-green`, `push-policy`) have no backing script —
they are checked against a harness-defined deterministic model
(`verify_red_before_green`, `verify_push_policy` in `contract-lib.sh`) plus
procedure-text anchors in the real docs. `gate-result-integrity` and
`verification-locality` are each **partially** script-backed and partially
procedure-text-only — see their Notes below for the honest split.

## Ordering, not just presence

`check_synthetic_adapter` (the fixture-driven checker behind the
`good-adapter`/`bad-*` self-tests in `parity.test.sh`) checks two things per
property, not one: **presence** (its anchor text exists in the doc at all)
and **order** (the anchor's byte offset is strictly greater than the
previous property's, in this table's row order). A fixture that swaps two
sections wholesale — every anchor still present, none of them missing —
still fails, with a distinguishable `"...fail:out of order: ..."` reason
instead of the presence-check's reason text. `fixtures/bad-wrong-order/`
and its self-test in `parity.test.sh` exist specifically to prove this: it
asserts the ordering violation is caught on the correct property, with
every other property still reporting `pass` (the breakage stays isolated,
same discipline as every other `bad-*` fixture).

**Real Claude/Codex adapters — narrow pairwise ordering checks, not this
table's row order.** `check_claude_adapter` and `check_codex_adapter` do
**not** carry the same cross-property strict-order assertion
`check_synthetic_adapter` does (i.e. "every property's anchor in this
table's row order") — that would be wrong here, because the real docs
correctly document a different, narrower order than the table's row order.
Verified directly against the real, committed docs: `phase-2-worktree.md`
correctly orders Create Worktree (`worktree-isolation`) before Baseline Gate
Check's Invoke (`baseline-gate`) before its Interpret section
(`gate-result-integrity`) — the opposite of this table's `baseline-gate`
(row 1) → `worktree-isolation` (row 2) order, because you must create the
worktree before you can run a gate inside it. Symmetrically, `codex.md`'s
single paragraph correctly states `/plan`'s "Stop before mutations"
(`planning-immutability`) before apply mode's "create the worktree"
(`worktree-isolation`) — again the opposite of this table's row order,
because planning-mode text precedes apply-mode text in the same sentence.
Enforcing this table's row order against the real adapters would require
either reordering those read-only, currently-correct pipeline docs (a
functional regression: the worktree genuinely must exist before the gate
runs inside it) or fabricating a bespoke "true" per-adapter order with no
principled basis beyond making the current text pass — the same
test-quality anti-pattern this harness exists to prevent elsewhere.

Instead, each real-adapter checker's `worktree-isolation` property carries
its own narrow, already-true-today pairwise ordering assertion, via the
shared `_markers_strictly_increasing` helper in `contract-lib.sh`: Claude's
asserts `git worktree add .worktrees/` (worktree creation) precedes
`hooks/scripts/run-gate.sh` (baseline-gate invocation) precedes the
`` `GATE_STATUS=green` or `GATE_STATUS=unset` → this target passes. ``
anchor (gate-result-integrity's interpretation); Codex's asserts
`Stop before mutations` (planning-mode) precedes `create the worktree`
(apply-mode). A future silent reordering of either real doc — e.g. running
the gate before the worktree exists, trusting `GATE_STATUS` before the gate
has run, or creating the worktree before planning mode ends — now fails
`worktree-isolation` with a distinguishable
`"...fail:out of order in <doc>: ..."` reason, instead of passing
vacuously the way a presence-only check would. `parity.test.sh` proves this
ordering check itself actually rejects a reordering (not just passing on
the real, already-correctly-ordered docs) by feeding
`_markers_strictly_increasing` a deliberately-reordered COPY of each
adapter's anchor text, never the real files. The table's row order remains
a stable ID/reporting order for `parity.test.sh`'s `for prop in ...` loops,
not a claimed execution order shared by every adapter; each adapter's
actual required sequencing is documented per-row below and enforced either
by the presence anchors themselves or, for this one ordering-sensitive
pair, by the pairwise offset assertion described here.

| Property | Deterministic enforcement point | Required control-point invocation | Adapters |
|---|---|---|---|
| `baseline-gate` | Script: `hooks/scripts/run-gate.sh` | Claude: `skills/implement/phases/phase-2-worktree.md`'s Baseline Gate Check invokes `run-gate.sh` before Phase 3 and hard-stops on a red/unrunnable gate. Codex: `skills/implement/codex.md` invokes `run-gate.sh` after creating the worktree and before test-first implementation, and hard-stops (`checkpoint.mjs block`, goal-clear, worktree/branch retained) on a red/unrunnable gate — #517's "Codex implement gate parity" child slice (#555). | Claude, Codex |
| `worktree-isolation` | Script: `hooks/scripts/guard-main-worktree.sh` (wired via each adapter's own `hooks.json` under both a `Write\|Edit` matcher and a `Bash` matcher) | Writes must land only under `.worktrees/<id>-<desc>/`; the guard blocks `Write`/`Edit` calls targeting the main worktree, and (#795) also blocks Bash redirection (`>`, `>>`, `>|`, fd-prefixed/combined forms) and `tee`/`tee -a` write targets under the same allowlist. An out-of-root absolute Bash redirect target (e.g. `~/.claude/settings.json`) is still allowed through outright (unchanged). A relative Bash write target, though, is never resolved against the hook's own process cwd (#810): the command text can `cd` before writing, making that cwd untrustworthy evidence. Instead the target is lexically collapsed and checked against a small relative allowlist (`.plans/*`, `.worktrees/*`, `.claude/plans/*`, `designs/*`, `*.pen`, `DESIGN.md`); anything else is rejected — except when the resolved root is itself a feature worktree with no `cd`/`pushd`-shaped text anywhere in the command, an availability narrowing so an ordinary relative write inside a feature worktree (e.g. `git diff > diff.txt`) isn't over-blocked, while a `cd <main-root> && write <relative>` pattern still is. Coverage boundary (#795/#810): the tokenizer is a precision layer, not a completeness proof — `cp`/`mv`/`dd of=`/`sed -i`/`truncate` are out of detection scope by settled refinement decision, and heredoc bodies/arbitrary metacharacter gluing still degrade to the guard's empty-parse backstop (a raw-text scan for the repo root, with allowlisted-subtree mentions neutralized) and block — a false positive the agent can rephrase around, never a silent permit. Two constructs are now precisely modelled instead of falling to that backstop: an unquoted brace expansion in write-target position (e.g. `tee /tmp/x/{a,b}`) is detected directly by the tokenizer and fails the whole call closed with a dedicated exit code, distinct from the generic unmodelled-construct case; and `>` inside a well-formed, boundary-delimited `[[ ... ]]`/`(( ... ))` construct is recognized as comparison/arithmetic rather than a redirect — a real redirect immediately adjacent to such a construct, or a command/process substitution nested inside it in UNQUOTED text, is still correctly extracted, while the same kind of substitution found INSIDE a double-quoted string within the construct's interior is instead treated as an unsupported construct and fails closed with its own dedicated exit code. Accepted residuals are named in `hooks/scripts/lib/bash-write-targets.sh`'s "Threat model and accepted residuals" header block. Claude: `phase-2-worktree.md`'s Create Worktree step (`git worktree add .worktrees/...`) plus `hooks/hooks.json` wiring. Codex: `codex.md`'s "create the worktree" step plus `codex/hooks.json` wiring (the same guard script, wired independently per adapter). | Claude, Codex |
| `red-before-green` | No backing script — harness-defined event-sequence model (`verify_red_before_green`) | Tests must be written and observed failing (red) before any implementation is reported green; procedure text must never document marking work green with no prior observed failure. Claude anchors: `phase-3-test-red.md` ("Tests should fail.") + `phase-4-implement-green.md` ("make failing tests pass."). Codex anchor: `codex.md`'s "implement test-first" step. | Claude, Codex |
| `gate-result-integrity` | Partially script-backed: `hooks/scripts/run-gate.sh`'s `GATE_STATUS=<green\|red\|unset>` output, interpreted by `verify_gate_interpretation`'s deterministic model. Partially procedure-text-only: the doc must never describe a red/unrunnable gate result as a pass. | A `GATE_STATUS=red` line, or a non-zero exit with no `GATE_STATUS=` line at all, must always resolve to "fail" — never silently to "pass". Claude anchor: `phase-2-worktree.md`'s Interpret section (`GATE_STATUS=red` → fails, `GATE_STATUS=green`/`unset` → passes). Codex anchor: `codex.md`'s baseline-gate step (`GATE_STATUS=green`/`unset` → proceed, `GATE_STATUS=red`/no-`GATE_STATUS=`-line → stop), same root cause as `baseline-gate`, closed by #555. | Claude, Codex |
| `planning-immutability` | Script: `hooks/scripts/guard-main-worktree.sh` (planning-session scenario — the guard has no separate "planning mode"; it blocks any main-worktree source write, which is exactly what a planning session must never make) | A planning session may only write to `.plans/` (or `.claude/plans/`, temp paths) — never a source file directly, and never before a worktree exists. Claude anchor: `phase-1-plan.md` ("Never begin Phase 2 in a session that created a new plan"). Codex anchor: `codex.md`'s `/plan` step ("Stop before mutations"). | Claude, Codex |
| `sensitive-file-refusal` | Script: `hooks/scripts/check-sensitive-files.sh` (wired via each adapter's own `hooks.json` under both a `Write\|Edit` matcher and a `Bash` matcher) | Writes to env files, credentials/secrets/keys, and SSH/keystore files are blocked regardless of cenci configuration, whether via `Write`/`Edit` or (#795) via a Bash redirection/`tee` write target. Coverage boundary (#795/#810): same tokenizer as `worktree-isolation`'s Bash arm — `cp`/`mv`/`dd of=`/`sed -i`/`truncate` out of scope by settled decision, and the same brace-expansion and comparison/arithmetic-context handling (each failing closed with its own dedicated exit code rather than the generic unmodelled-construct path). An unmodelled write-shaped construct (heredoc bodies, arbitrary metacharacter gluing) still degrades to this guard's own empty-parse backstop — a raw-text scan for its sensitive markers (`check_sensitive_raw`, kept in sync with the blocklist by the marker-sync contract test) — and blocks rather than silently allowing. Unlike `worktree-isolation`, this guard does NOT apply the relative-cwd-distrust policy: its decision axis is filename-marker matching, not path containment, so a stale-cwd relative target doesn't change whether the target matches a sensitive marker. Both adapters wire the same script into their respective `hooks.json`. | Claude, Codex |
| `verification-locality` | Partially script-backed: `codex/checkpoint.mjs` records the workflow `target` at `init` time (Codex side); partially procedure-text-only: build/test verification must run inside the assigned worktree, not elsewhere. | Claude: `guard-main-worktree.sh` backs the write half (any stray write outside the worktree is blocked); `agents/implementer.md`'s Working Directory section ("target it explicitly on every command") anchors the run-commands-against-the-worktree half. Codex: `checkpoint.mjs`'s recorded `target` is compared against the claimed verification path by `verify_worktree_match`; `codex-runtime/SKILL.md` documents the checkpoint storing `worktree` identity alongside phase/status. | Claude, Codex |
| `push-policy` | No backing script — harness-defined command-string model (`verify_push_policy`) | Never force-push or bypass a mandatory approval/review/security gate. A plain `git push` or `git push --force-with-lease ...` is fine; a bare `--force`/`-f`/`--no-verify` is not. Claude anchors: `phase-9-pr.md` (uses `--force-with-lease`, never a bare force/no-verify push) + `phase-6-7-review.md` ("These quality gates are mandatory."). Codex anchor: `codex.md`'s literal sentence "Never force-push or bypass security/design/approval gates." | Claude, Codex |
