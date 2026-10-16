# Open parent blockers for split children

Shared by Claude's `SKILL.md` and `codex.md`. Run only when creating split
children. This copies the parent's native `blockedBy` relationships; the parent
itself is a tracking issue, never an inherited blocker of its own children.

Before rendering the confirmation manifest, fetch the parent's blockers once for
the whole split. Use this run's existing validated `<number>` and `<token>` for
scratch paths. Run each command separately and inspect its result:

```bash
gh issue view <number> --repo <owner>/<repo> --json blockedBy > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-blockers.json || rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-blockers.json
```

Validate the complete connection, positive integer issue numbers, known states,
and same-repository URLs before consuming any number. `totalCount` must match
the returned nodes: a truncated list is an error, never an empty blocker set.
The selector returns a sorted, deduplicated JSON array containing only OPEN
issues; it returns `[]` successfully for no blockers or all-closed blockers.

```bash
jq -e -c '.blockedBy | if (.nodes | type) != "array" then error("invalid blocker nodes") elif .totalCount != (.nodes | length) then error("incomplete blocker connection") elif all(.nodes[]; (.number | type) == "number" and .number > 0 and .number == (.number | floor) and (.state == "OPEN" or .state == "CLOSED") and .url == ("https://github.com/" + $repo + "/issues/" + (.number | tostring))) then [.nodes[] | select(.state == "OPEN") | .number] | unique else error("invalid blocker identity or state") end' --arg repo <owner>/<repo> ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-blockers.json
```

If the file is absent, the fetch failed, or the selector fails, retry once from
the fetch; on another failure, STOP with zero writes and report the error.
Never treat unreadable, malformed, incomplete, or unknown-state data as `[]`.
Use the selector's actual output as the inherited blocker set in every child's
manifest; omit closed parent blockers from both body lines and native writes.
The response includes state, so no per-blocker state lookup is needed.

After confirmation, repeat the fetch and selector before the first write,
alongside manifest revalidation. Compare the selected open set with the gate's
snapshot. If it changed, STOP with zero writes and request a fresh refinement
confirmation through the client's available user-input mechanism; report the
changed blockers. Do not silently apply a dependency graph the user did not see.
Reuse the validated set for every child in this pass, including the first child;
do not fetch the parent again for each child. This is a split-time snapshot, not
ongoing synchronization of future parent changes.

At each child's blockers step, read its current native `blockedBy` connection
before editing:

```bash
gh issue view <child-number> --repo <owner>/<repo> --json blockedBy
```

Validate completeness and identity as above, but retain all
existing numbers regardless of OPEN/CLOSED state when comparing link presence
(the open-only selector is for parent inheritance, not child link verification).
Add only missing expected links; already-linked blockers are a no-op on retry.
Preserve unrelated existing links. After editing, re-fetch and verify every
expected blocker is present (command success alone is insufficient). For an
empty expected set, skip the blockers step entirely.

On any child read, edit, or verification failure, retry once, then STOP before
the next child or Pass 2. Report existing children and missing blocker links;
retain the same checkpoint and body files so recovery repairs the same children
without duplicate creates or duplicate links. A resumed apply repeats parent
revalidation and the child link-presence check, never trusts a prior success.
On successful completion, or on a declined confirmation with zero GitHub writes,
remove the scratch file by its explicit path (retain it on write failure):

```bash
rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-blockers.json
```
