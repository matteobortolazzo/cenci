# Local implementation timings

Both implement clients use this procedure during apply, including compact mode,
parallel lanes, acceptance-fix rounds, and resumed work. OpenCode remains paused;
no adapter or enablement change is required. Measurements are advisory: they never
replace evidence, change a gate, suppress an error, authorize a retry, or waive a
required verification. Do not time individual reads, searches, or tool calls.

## Initialize

After selecting/creating the feature worktree and before its baseline verification,
create a fresh timing directory once. Planning-only runs do not initialize timing.
Resolve `<plugin-root>` from this installed skill (Claude's `CLAUDE_PLUGIN_ROOT`,
Codex's resolved `PLUGIN_ROOT`); substitute that absolute path in all four calls.
Use an existing absolute temporary parent for `<temp-parent>` (`TMPDIR`, or `/tmp`).

```bash
node "<plugin-root>/skills/implement/scripts/timing.mjs" init "<temp-parent>"
```

Check exit 0 and a nonempty absolute directory path before using stdout as
`<timing-dir>`. Keep that literal path in the orchestrator's run context and each
delegation brief; never reconstruct it, use a fixed filename, or mix it with the
review `RUN_DIR`. The directory is private and unique per invocation; the helper
starts no background process and sends no telemetry. Records contain only run/span
IDs, static labels, phase, timestamps and outcome, never commands, output, ticket
text, environment values, credentials, or file contents.

## Record boundaries

Immediately before each activity below, call `start` with the phase and static
label from the table, checking exit 0 before retaining the returned span ID:

```bash
node "<plugin-root>/skills/implement/scripts/timing.mjs" start "<timing-dir>" implementation lane-1
```

| Execution point in both clients | Phase | Label and owner |
| --- | --- | --- |
| Test/implement/refactor delegation (Phases 3–5), compact delegation, each parallel lane or fix delegation | `implementation` | Orchestrator: `test-red`, `implement-green`, `refactor`, `compact`, `lane-1`, `acceptance-fix`, or `review-fix` |
| Acceptance reviewer, each configured code/security/silent-failure reviewer (Phases 6–7), including another review round | `review` | Orchestrator: `acceptance`, `code-review`, `security-review`, or `silent-failure-review` |
| Baseline gate, scoped lane check group, build/test/lint group, lane verification barrier, and required post-fix/post-rebase reruns | `verification` | The agent actually running that group: stable labels such as `flow-health-gate`, `lane-1-checks`, or `full-checks-flow` |

Immediately after the activity finishes, close its own span, using `completed`,
`failed`, or `stopped` according to the actual result:

```bash
node "<plugin-root>/skills/implement/scripts/timing.mjs" end "<timing-dir>" "<span-id>" completed
```

At most two timer calls per substantive activity or verification group. Group a
build/test/lint sequence in one span; do not wrap each command separately. Record
every attempted group, including failures, with a fresh span ID. Reuse the same
verification label for the same project and check group on reruns so repeat counts
are meaningful: the same health gate run at baseline, after fixes, and after rebase
uses `flow-health-gate` all three times, never a phase-specific label. Different
commands/scopes need different labels. Labels must match
`^[a-z][a-z0-9-]{0,63}$`; derive them from roles/project slugs, never command text.
These are group invocation counts, not counts of tests or individual commands.

Only the span's owner closes it. Pass the directory and verification instructions
to implementers; they own their nested verification spans, while the orchestrator
owns delegation spans. Include the absolute installed helper path in those briefs;
the implementer's narrow timing exception permits only its `start`/`end` calls.
Concurrent lanes use independent IDs and files; never share
one span ID between workers. Do not count the barrier twice as both an orchestrator
verification span and the worker's identical verification span. Existing child
command results remain authoritative: inspect their exit status independently of
timing calls, then follow the existing gate/retry/stop rule even if timing fails.

## Report and recovery

After all workers return, and before the final response or any orderly stop, read:

```bash
node "<plugin-root>/skills/implement/scripts/timing.mjs" summary "<timing-dir>"
```

In the final session summary report the artifact path/run ID, measured elapsed
seconds since initialization, accumulated seconds for implementation/review/
verification, and verification group invocation/repeat counts (with repeated
labels). Keep it to one short line unless investigating a bottleneck. These
accumulated values overlap across concurrent workers and nested verification;
never sum them as wall time or claim a measured speedup without a comparable run.
Elapsed time includes orchestration and pauses after initialization, excludes
planning, and ends at this summary snapshot. Unrecorded phases are unmeasured,
not zero. Incomplete spans have unknown duration/outcome and must be reported.

All measurements are scoped to this session on the same host and boot, using its
monotonic clock. A fresh/resumed session always initializes a new directory; it
never closes old unfinished spans, backfills timestamps, or merges runs. Retain
the local artifact for inspection; it is not a checkpoint or PR attachment.
If init/start/end/summary fails (including missing Node, invalid labels, missing or
corrupt files), or would need fresh client permission, report `timing: unavailable`
once, continue the original workflow,
and stop issuing new timer calls for this run. Any already started worker may
finish its own records, but unavailable/incomplete data is never presented as
complete. Do not retry work or ask for approval solely to recover timing.
