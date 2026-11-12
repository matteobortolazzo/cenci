# Codex tmux association

How the daemon decides which tmux window belongs to a Codex session, and how a user pins one by hand with `cenci bind` (#1529). Codex's shared app-server can keep the `TMUX_PANE` and `$TMUX` of the terminal that first started it, so a hook can name a pane that is stale or that belongs to another tmux server. See also `watch/plugin/codex/README.md` ("Shared app-server and stale tmux panes"), `watch/docs/tmux.md` (socket trust) and `watch/docs/hook-events.md` (ingest validation).

## Association states

`cenci status` and the daemon snapshot report one association per Codex session (`WindowState.association`, omitted when empty). `session_id` is present only on `unproven` entries, and only when the id has the session-id shape: the status hint needs it, and the broadcast socket is mounted into sandboxes, so `proven` and `bound` entries keep host session ids off it.

| State | Meaning |
|---|---|
| `bound` | A binding from `cenci bind` and a tracked window. Hook panes no longer move it. |
| `proven` | A tracked window from a legacy attachment (an event with no tmux socket) or from a hook pane whose server pid matched the live server. |
| `unproven` | A pane-carrying Codex event was refused: a missing pane (#1500), an empty or malformed server pid (an older notify binary sends none), or a pid that does not match the live server. The session stays listed, paneless. |

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
- **Mismatch**: the session goes paneless without any tmux write, the refused (pane, socket) pair is remembered (#1500) and any binding is voided. A #1500 re-attach of that pair succeeds only when the pid matches.
- **SessionEnd**: it never reaches the per-event check, so the daemon proves the pid before teardown. On a mismatch the window is forgotten without a restore or rename, a pending-close registered on that pane id is dropped without a kill, and the session is still deleted. A failed listing changes nothing beyond today's teardown. The clear/resume handoff follows the same rule.
- **No adoption of a bound pane**: a Codex hook pane already bound to another session is not adopted, so a second session never steals a bound window from its hook events.

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

- **Accepted risk: forged binds.** A container can send a bind that names any trusted host pane. The impact is limited to window styling and detaching a Codex session to paneless, the same reach forged hook events already have. Two side effects stay inside that reach: a pending-close registered on the bound pane fires on the binder's SessionEnd, and a Codex session detached by a bind becomes `unproven`, so its `session_id` appears in the snapshot on the broadcast socket that sandboxes can read. Keeping session ids off that socket altogether (a host-only query) is not part of this change. It is the same class as the accepted arm-path risk in `watch/docs/tmux.md`. Proving that the caller belongs to the pane is the process-ancestry work in #1530. The bind rate limiter is shared by every caller (the same design as the arm and control limiters), so a container can drain it and delay a host user's bind until it refills.
- **Pane id reuse after a tmux restart** on a tracked window is caught only at the next Codex event, its SessionEnd included; the sweep does not check the pid.
- **Pending-close on a window released by a bind**: an entry registered on the old window is not moved to the new one. Sweep pruning still reclaims it.
- No shared app-server process is killed or restarted, and hook trust is never bypassed or changed.

## Native host record (P1, P22)

These rows need a host with Codex and tmux; the implementation sandbox has neither. They are filled by a person running the steps below. Home paths, user and host names and session IDs are replaced with placeholders (`<home>`, `<user>`, `<host>`, `<session-id>`) before anything is pasted here. Nothing in this section is a claimed result until the row stops reading `not run`.

### Status

- P1 native reproduction: not run: native host session required (no Codex or tmux in the implementation sandbox)
- P22 native `cenci bind` verification: not run: native host session required (no Codex or tmux in the implementation sandbox)

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

tmux's `#{pid}` is assumed to equal the second field of `$TMUX`. `TestListPanesReportsTheServerPID` checks it against a scratch server but skipped in the implementation sandbox because tmux is absent, so it is confirmed only once the native record above is filled. The same record confirms the assumption that tmux pane ids are not reused within one server lifetime.

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
