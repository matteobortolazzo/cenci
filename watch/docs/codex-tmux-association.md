# Codex tmux association

How the daemon decides which tmux window belongs to a Codex session, and how a user pins one by hand with `cenci bind` (#1529). Codex's shared app-server can keep the `TMUX_PANE` and `$TMUX` of the terminal that first started it, so a hook can name a pane that is stale or that belongs to another tmux server. See also `watch/plugin/codex/README.md` ("Shared app-server and stale tmux panes"), `watch/docs/tmux.md` (socket trust) and `watch/docs/hook-events.md` (ingest validation).

## Association states

`cenci status` and the daemon snapshot report one association per Codex session (`WindowState.association`, omitted when empty). `session_id` is present only on `unproven` entries, and only when the id has the session-id shape: the status hint needs it, and the broadcast socket is mounted into sandboxes, so `proven` and `bound` entries keep host session ids off it.

| State | Meaning |
|---|---|
| `bound` | A binding from `cenci bind` and a tracked window. Hook panes no longer move it. |
| `proven` | A tracked window from a legacy attachment (an event with no tmux socket) or from a hook pane whose server pid matched the live server and whose process ancestry proved a single-client Codex in that pane (see "The process-ancestry rule"). |
| `unproven` | A pane-carrying Codex event was refused: a missing pane (#1500), an empty or malformed server pid (an older notify binary sends none), a pid that does not match the live server, or no process-ancestry proof (#1530). The session stays listed, paneless. |

A Codex session that never reported a pane has no association and no hint. Claude sessions never carry an association.

An unproven session stays listed in `cenci status` and counted in `cenci widget-json`, follows its own Stop/SessionEnd and expires through the paneless session TTL. `cenci status` prints this line under it (the id is checked against the session-id pattern again before printing):

```text
hint: hooks were delivered but no tmux association is proven; run 'cenci bind <id>' in the session's tmux pane
```

The snapshot keys are additive. `cenci widget-json` prints its own `status.output` and never embeds `WindowState`, so its shape is unchanged.

## The server-pid rule

A pane is attached only when the live tmux server is the one the event came from. `notify` sends the second field of `$TMUX` as `tmux_server_pid`; the daemon compares it with `#{pid}` from the same `list-panes -F` listing that proves the pane exists, so there is no extra tmux call. A listed pane with no server pid counts as a mismatch.

- **Applies to** Codex events whose raw `tmux_socket` was non-empty before sanitizing and that carry a session id, and to bound sessions.
- **Exempt**: events with no socket at all (sandboxed sessions send `TMUX_PANE` without `$TMUX` and keep today's attach path) and events with no session id. Claude events are untouched.
- **Untrusted socket on an event**: it falls back to `""` (the default server) as before, and its pid is then compared against the default server's, so an equal pane id on another server never styles a window. A rejected bind socket never falls back.
- **Ingest**: `tmux_server_pid` must match `^[1-9][0-9]{0,9}$`; anything else is treated as absent, with a verbose log line.
- **Mismatch**: the session goes paneless without any tmux write, the refused (pane, socket) pair is remembered (#1500) and any binding is voided. A #1500 re-attach of that pair succeeds only when the pid matches and the event carries the ancestry proof below.
- **SessionEnd**: it never reaches the per-event check, so the daemon proves the pid before teardown. On a mismatch the window is forgotten without a restore or rename, a pending-close registered on that pane id is dropped without a kill, and the session is still deleted. A failed listing changes nothing beyond today's teardown. The clear/resume handoff follows the same rule.
- **No adoption of a bound pane**: a Codex hook pane already bound to another session is not adopted, so a second session never steals a bound window from its hook events.

## The process-ancestry rule

The server pid proves the pane is on the live server, not that the session runs in it: the shared app-server can keep a pane that is still alive. A hook pane is therefore attached as `proven` only when all three hold (#1530):

1. The hook's process ancestry includes the hook-supplied pane's `pane_pid`.
2. The event's server pid matches the live server (the server-pid rule above).
3. The nearest Codex ancestor of the hook is not the shared app-server.

When the ancestry holds but the nearest Codex ancestor is the shared app-server, or the proof is missing, the session stays `unproven` even while the supplied pane is alive.

- **Applies to** the same events as the server-pid rule: Codex events with a session id whose raw `tmux_socket` was non-empty before sanitizing. **Exempt**: events with no socket (sandboxed sessions, which have no `$TMUX` and see container pids that could never match a host `pane_pid`), pane-only events (no session id), and Claude events. `cenci notify` reads the ancestry only for `-agent codex` main-agent events (empty `agent_id`) that carry both `TMUX_PANE` and a `$TMUX` socket, after the `watch.excludeCwd` check.
- **Read bound**: one `ps -A -o pid= -o ppid= -o args=` call from `internal/procancestry`, under a 1s context plus a 500ms `WaitDelay`, so it fits the 3s `Interrupt`/`SessionEnd` hook timeouts. Stdout is capped (overflow is an error), stderr is discarded and the walk is capped at 32 entries (`procancestry.MaxDepth`); a repeated pid is a cycle error. Codex is identified as the first process above `cenci notify` whose argv[0] basename is exactly `codex`.
- **Wire keys**: `ancestry_pids` (string array, omitted when empty) and `ancestry_single_client` (bool, omitted when false). Only pids and one boolean cross IPC, never command lines. The daemon checks every pid against the server-pid pattern and rejects more than 32 entries at ingest, treating both keys as absent with a verbose log line that does not echo the values.
- **Fail closed**: an absent key, a malformed or too-deep chain, a failed or timed-out read or an older notify binary all mean unproven. `cenci notify` stays silent on a failed read, still delivers the event and exits 0, and writes no hook-health record. An event with no proof never detaches a session that is already `proven`: a paneless session becomes `unproven`, and an existing proven association is kept.
- **No recorded marker means always unproven**: the shared app-server is told apart from a client by a marker in the nearest Codex ancestor's argv. The production marker table is empty, because the native record's "Marker identifying the shared app-server" row is `not run`, and a guessed marker that misses the shared server would bring back the cross-association (P8). With an empty table the notify side never reports a single client, so every host Codex hook pane in tmux is unproven and `cenci bind` is the only way to attach it. The upstream gap: Codex hook input carries no client identity. Filling the marker row is a one-entry change to the table plus its tests.
- **Mismatch on a tracked window**: when `RequireAncestry` is set and the live listing shows a `pane_pid` that is empty or not in the stored chain, the tracked window is forgotten without a restore or rename and without any tmux write, as on a server-pid mismatch. This happens only for anomalous or forged events (a single-client claim whose chain does not contain the pane). A session that does not own the pane (a paneless adopter with a failed proof) fires no pending-close kill, which `TestDaemon_CodexSessionEndWithFailedProofTouchesNothing` covers. For the owning tracked session the daemon's SessionEnd `CheckPane` pre-check covers only the server pid, so a tracked window whose `pane_pid` left the stored chain is forgotten without a write but its pending-close kill still runs. This is accepted because a live pane's `pane_pid` does not change within a server lifetime (the pane-id stability assumption).
- **`agent_id` never rebinds**: for every Codex session with a session id, with or without a socket, an event with `agent_id` set never adopts or moves a pane, never sets `unproven` and never ends the parent. Claude is untouched.
- **A binding outranks a proof**: `cenci bind` clears the stored ancestry, a bound session is never moved by a hook pane, and a pane bound to another session is never adopted from an event. `cenci bind` itself carries no ancestry proof.
- **Nothing is inferred**: ownership is never derived from cwd, title, active window or a single matching pane.

## `cenci bind <session-id>`

Codex sessions only. The identity is the caller's own `$TMUX_PANE` and `$TMUX` (socket and server pid); run it in the pane that shows the session. It dials the default event socket and has no `--event-socket` flag. On the wire it is a `codex-bind` request (`ipc.BindRequest`) with a single ack-or-nack reply line.

| Exit | Meaning | Output |
|---|---|---|
| 0 | bound | stdout: `bound Codex session <id> to tmux pane <pane>` |
| 1 | refused, outside tmux, or no daemon | one stderr line (below) |
| 2 | usage error | stderr: `cenci bind: usage: cenci bind <session-id>` |

`-h`/`--help` prints usage to stdout and exits 0. Exit 1 lines:

- `cenci bind: not inside a tmux pane` (empty `$TMUX_PANE` or `$TMUX`; nothing is sent)
- `cenci bind: <reason>` (the daemon's refusal, relayed, see below)
- `cenci bind: the cenci daemon is not reachable` (nothing was sent; with no secure socket directory the line adds `(no secure socket directory)`)
- `cenci bind: the cenci daemon did not answer; the binding status is unknown`

### Bind reasons

| Reason | Cause |
|---|---|
| `invalid bind request` | Malformed session id, pane, socket or server pid |
| `bind requests are rate limited` | The bind token bucket is empty |
| `the daemon did not apply the bind in time; check cenci status` | The loop did not take or answer the bind within the daemon's budget |
| `unknown session` | The daemon does not track that session id |
| `not a Codex session` | The session is a Claude session |
| `tmux socket is not in the trusted socket directory` | The socket failed the trusted-directory check; no fallback to the default server |
| `pane is not listed on that tmux socket` | The live listing does not contain the pane |
| `tmux server pid does not match the live server` | `$TMUX`'s pid differs from the live server's `#{pid}` |
| `tmux pane listing failed` | The tmux listing errored, so nothing could be proven |
| `pane is tracked by a non-Codex session` | A Claude session holds that pane, or another pane of the same window (the frontend tracks windows); it is never detached |

### Limits and lifecycle

- The daemon gives a bind 10s (`bindBudget`), under the client's 15s deadline, so a slow daemon answers with the timeout reason instead of silence.
- Binds have their own rate limit: a burst of 5, then one per 30s.
- The bind is applied on the daemon's event loop. Success restyles the bound window at once.
- Bindings live in memory only and are dropped on daemon restart (like the pending-close registry). The next hook event of a session proves a pane again, or leaves it unproven.
- A bind detaches every Codex session holding the pane or another pane of its window. A holder with a session id goes paneless (still listed, `unproven`, not deleted); a pane-keyed holder (no session id) is removed, because it cannot exist without its pane. The window then carries the binding session's state.
- The restyle re-lists the panes. If the pane vanished or its server pid changed after the proof, the bind is refused late: the previous window was already restored and the holders detached, so they stay detached and the binding session is left paneless and `unproven`.
- Re-binding a session from another pane restores the previous window's options.
- A bound pane that a successful tmux listing proves gone removes the session through normal tracked-pane cleanup; a later event with the stale pane makes a paneless session. A listing failure keeps the binding.

## Risks and trust

- **Accepted risk: forged binds.** A container can send a bind that names any trusted host pane. The impact is limited to window styling and detaching a Codex session to paneless, the same reach forged hook events already have. Two side effects stay inside that reach: a pending-close registered on the bound pane fires on the binder's SessionEnd, and a Codex session detached by a bind becomes `unproven`, so its `session_id` appears in the snapshot on the broadcast socket that sandboxes can read. Keeping session ids off that socket altogether (a host-only query) is not part of this change. It is the same class as the accepted arm-path risk in `watch/docs/tmux.md`. `cenci bind` carries no process-ancestry proof of its own, so a forged bind stays an accepted risk (#1530 adds ancestry to hook events only, and a binding always outranks a proof). The bind rate limiter is shared by every caller (the same design as the arm and control limiters), so a container can drain it and delay a host user's bind until it refills.
- **Accepted risk: forged ancestry.** A process on the event socket, a container included, can send any `ancestry_pids` and `ancestry_single_client: true` with a forged event. The reach is the same as forged hook events and forged binds: window styling and detaching a session to paneless. It is the same class as the accepted arm-path risk in `watch/docs/tmux.md`.
- **Pane id reuse after a tmux restart** on a tracked window is caught only at the next Codex event, its SessionEnd included; the sweep does not check the pid.
- **Pending-close on a window released by a bind**: an entry registered on the old window is not moved to the new one. Sweep pruning still reclaims it.
- No shared app-server process is killed or restarted, and hook trust is never bypassed or changed.

## Native host record (P1, P22, P23)

These rows need a host with Codex and tmux; the implementation sandbox has neither. They are filled by a person running the steps below. Home paths, user and host names and session IDs are replaced with placeholders (`<home>`, `<user>`, `<host>`, `<session-id>`) before anything is pasted here. Nothing in this section is a claimed result until the row stops reading `not run`.

### Status

- P1 native reproduction: not run: native host session required (no Codex or tmux in the implementation sandbox)
- P22 native `cenci bind` verification: not run: native host session required (no Codex or tmux in the implementation sandbox)
- P23 native automatic association and two-client verification: not run: native host session required (needs a host with Codex and tmux)

### Versions

| Component | Version |
|---|---|
| Codex | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| cenci | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| tmux | not run: native host session required (no Codex or tmux in the implementation sandbox) |

### Hook-supplied identity versus pane B

The shared app-server is started from pane A, A is closed, and a thread runs from pane B through the installed host hook path.

| Field | Hook-supplied | Pane B (real) |
|---|---|---|
| Pane id | not run: native host session required (no Codex or tmux in the implementation sandbox) | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Socket | not run: native host session required (no Codex or tmux in the implementation sandbox) | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Server pid | not run: native host session required (no Codex or tmux in the implementation sandbox) | not run: native host session required (no Codex or tmux in the implementation sandbox) |

### Hook process ancestry

| Item | Value |
|---|---|
| Process names, hook to root | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Marker identifying the shared app-server | not run: native host session required (no Codex or tmux in the implementation sandbox) |

### P22 steps

| Step | Result |
|---|---|
| Shared app-server started from pane A | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Pane A closed | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Thread runs in pane B, `cenci status` shows it unproven with the hint | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| `cenci bind <session-id>` run from pane B | not run: native host session required (no Codex or tmux in the implementation sandbox) |
| Pane B's window shows `@cenci-symbol`/`@cenci-style` | not run: native host session required (no Codex or tmux in the implementation sandbox) |

### Unverified assumption

tmux's `#{pid}` is assumed to equal the second field of `$TMUX`. `TestListPanesReportsTheServerPID` checks it against a scratch server but skipped in the implementation sandbox because tmux is absent, so it is confirmed only once the native record above is filled. The same record confirms the assumption that tmux pane ids are not reused within one server lifetime. That assumption also covers the ancestry check: a live pane's `pane_pid` stays the same, so checking it on every event is enough.

The ancestry rule adds two more assumptions that the native record confirms. Codex is identified by an argv[0] basename of exactly `codex`, the native binary the npm wrapper launches; this has not been checked on a host. BSD/macOS `ps` may truncate the `args` column, which only matters once a shared-app-server marker exists, because the marker table is empty today.

## Deterministic fixture results

These cover the behavior on mock tmux sockets and run in `make test`; they are separate from the native status lines above and do not stand in for them.

| Behavior | Test |
|---|---|
| Bind styles the bound pane despite stale events (P2) | `TestDaemon_CodexBindStylesBoundPaneDespiteStaleEvents` |
| Equal pane ids on another socket never styled (P4) | `TestDaemon_CodexEqualPaneIDOnOtherSocketNeverStyled` |
| Server pid mismatch stays paneless (P5) | `TestDaemon_CodexServerPIDMismatchStaysPaneless`, `TestDaemon_CodexSessionEndUnderAnotherServerPIDWritesNothing` |
| Unproven session snapshot and lifecycle (P6) | `TestDaemon_CodexUnprovenSessionSnapshotAndLifecycle` |
| Rendered hint (P6) | `TestStatusSubcommand_HumanReadable_UnprovenCodexShowsBindHint` |
| Bind refusals, rebind, second bind, pane gone, listing failure (P12 to P18) | `TestBind_*` in `internal/daemon/codex_binding_test.go` |
| Bind round trip through the loop | `TestHandleBindRoundTripsThroughTheLoop` |
| CLI contract and exit codes (P3, P11) | `TestBindCmd_*` |
| Server pid from a real tmux server | `TestListPanesReportsTheServerPID` (skips without tmux) |
| Ancestry attaches the hook pane; the shared app-server, a missing `pane_pid` or a server pid mismatch stay unproven (P7) | `TestDaemon_CodexAncestryProofAttachesHookPane`, `TestDaemon_CodexSharedAppServerAncestryStaysUnprovenWhilePaneAlive`, `TestDaemon_CodexAncestryWithoutPanePIDStaysUnproven`, `TestDaemon_CodexAncestryNeedsServerPIDMatch` |
| SessionEnd and handoff with a failed proof touch nothing (P7) | `TestDaemon_CodexSessionEndWithFailedProofTouchesNothing` |
| Two clients on one app-server bind their own panes (P8) | `TestDaemon_CodexTwoClientsOnOneAppServerBindTheirOwnPanes` |
| Bound and proven lifecycles touch only their own window (P9) | `TestDaemon_CodexBoundAndProvenLifecycleTouchesOnlyOwnWindow` |
| `agent_id` events never rebind or end the parent (P9) | `TestDaemon_CodexSubagentEventNeverRebindsOrEndsParent` |
| The `agent_id` guard leaves Claude untouched: a Claude event with `agent_id` naming another live pane moves the session as the same event without `agent_id` does, never unproven; the same shape for Codex is refused | `TestDaemon_ClaudeAgentIDEventKeepsTodaysPaneBehavior` |
| No ancestry is unproven; a proven session is kept (P20) | `TestDaemon_CodexEventWithoutAncestryIsUnproven` |
| Ancestry deeper than the cap is unproven (P21) | `TestDaemon_CodexAncestryDeeperThanCapIsUnproven` |
| A tracked window is forgotten unrestored when the live `pane_pid` leaves the stored chain (P7) | `TestDaemon_CodexTrackedWindowForgottenWithoutRestoreOnAncestryMismatch` |
| Ownership never inferred from a pane title, window name or a single pane (P28); the cwd and the active window never reach the association path (`paneAdoptable`, `adoptEventPane`, the frontend's `OnEvent` and identity check): `ipc.HookEvent` carries no cwd, and `tmux.PaneInfo`, the listing that path reads, carries neither a current path nor an active flag, so no test can feed them to it (a pane's cwd is read only by the unrelated worktree-cleanup and window-restyle code) | `TestDaemon_CodexOwnershipNeverInferredFromTitleWindowNameOrSinglePane` |
| A binding outranks an ancestry proof | `TestDaemon_CodexBindingOutranksAncestryProof` |
| Malformed ancestry keys are treated as absent | `TestRawHookEventWithMalformedAncestryIsTreatedAsAbsent`, `TestEventReceiver_DecodesAncestryKeys` |
| Notify sends pids only, never command lines (P30) | `TestNotifyCodexSendsAncestryPIDsOnlyAndNoCommandLines` |
| A failed or timed-out read still delivers the event (P20) | `TestNotifyCodexAncestryReadFailureStillDelivers` |
| A failed read is silent and writes no hook-health failure record, only the delivery's own `last-ok` (P20) | `TestNotifyCodexAncestryReadFailureIsSilentAndWritesNoHookHealthRecord` |
| Ancestry deeper than the cap sends nothing (P21) | `TestNotifyCodexAncestryDeeperThanCapSendsNone` |
| Notify reads the ancestry only for Codex main-agent events in tmux | `TestNotifyReadsAncestryOnlyForCodexMainAgentEventsInTmux` |
| Bounded `ps` parser, walk, depth cap, cycle, nearest-Codex classifier, empty marker table | `TestReadWith*`, `TestProductionMarkerTableIsEmptyAndNeverReportsASingleClient`, `TestReadRunsTheRealPS` in `internal/procancestry` |
| `pane_pid` column of the pane listing | `TestParsePanesReadsPanePID`, `TestExecClientListPanesAsksForPanePID` |
