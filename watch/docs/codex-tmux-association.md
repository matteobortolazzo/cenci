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

On Linux, when the report includes an unproven Codex session, it also suggests
`codex --no-daemon` once for future interactive tmux launches. This lets the
hook inherit the pane of the client that started it while the shared server
continues to serve its existing sessions.

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
- **The marker table is per platform; no recorded marker means always unproven**: Codex hook input carries no client identity, so the shared app-server is told apart from a client by a marker in the nearest Codex ancestor's argv. The table (`sharedServerMarkersByOS`, keyed by `runtime.GOOS`) holds `app-server` for Linux only, the marker the native record below found in every shared-app-server observation and in no single-client one, read whole from Linux `ps`. On Linux a single-client Codex in a pane (`codex exec`, or a TUI running in-process) is proven automatically; a thread on the shared app-server, a default TUI's path, stays unproven and needs `cenci bind`. A client whose own argv contains `app-server` (a prompt naming it) is also unproven, the safe direction. Every other platform, macOS included, has no native run, so its table is empty: the notify side never reports a single client there, and `cenci bind` is the only way to attach a host Codex hook pane. A guessed or truncated marker that misses the shared server would bring back the cross-association (P8), which is why a platform gets an entry only after a native run reads the marker whole.
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

- **Accepted risk: forged binds.** A container can send a bind that names any trusted host pane. The impact is limited to window styling and detaching a Codex session to paneless, the same reach forged hook events already have. Two side effects stay inside that reach: a pending-close registered on the bound pane fires on the binder's SessionEnd, and a Codex session detached by a bind becomes `unproven`, so its `session_id` appears in the snapshot on the broadcast socket that sandboxes can read. Keeping session ids off that socket altogether (a host-only query) is not part of this change. It is the same class as forged hook events and as the forwarded `babysit stop`/`babysit status` requests that stay identity-blind (#1390), recorded in the remaining gaps of `watch/docs/tmux.md`; the arm request is no longer in that class, since its launch-ID binding (#1135) holds a container to its own pane. `cenci bind` carries no process-ancestry proof of its own, so a forged bind stays an accepted risk (#1530 adds ancestry to hook events only, and a binding always outranks a proof). The bind rate limiter is shared by every caller (the same design as the arm and control limiters), so a container can drain it and delay a host user's bind until it refills.
- **Accepted risk: forged ancestry.** A process on the event socket, a container included, can send any `ancestry_pids` and `ancestry_single_client: true` with a forged event. The reach is the same as forged hook events and forged binds: window styling and detaching a session to paneless. It is the same class as the forwarded `babysit stop`/`babysit status` requests that stay identity-blind (#1390), recorded in the remaining gaps of `watch/docs/tmux.md`.
- **Pane id reuse after a tmux restart** on a tracked window is caught only at the next Codex event, its SessionEnd included; the sweep does not check the pid.
- **Pending-close on a window released by a bind**: an entry registered on the old window is not moved to the new one. Sweep pruning still reclaims it.
- No shared app-server process is killed or restarted, and hook trust is never bypassed or changed.

## Native host record (P1, P22, P23)

These rows are filled from output captured on a host with Codex and tmux, either by an agent running there or pasted by a person; the implementation sandbox has neither. A row is never filled by inference, and a step that was not run reads `not run: <reason>`. Home paths, user and host names and session IDs are replaced with placeholders (`<home>`, `<user>`, `<host>`, `<session-id>`) before anything is written here, and the tmux socket directory's uid with `<uid>`. Argv evidence keeps argv[0] (its basename, or a sanitized path) plus the subcommand and flag-name tokens; every other value is `<arg>`.

### Commands

Run everything on a dedicated scratch tmux server, so no existing server or pane is touched, and start Codex only from a folder Codex already trusts. A `-L` socket sits in the same per-uid socket directory as the default server, so the hooks are the installed ones. The record below ran on a server named `cenci-1550`, so its socket reads `/tmp/tmux-<uid>/cenci-1550`.

```sh
codex --version; cenci version; tmux -V; uname -sr; ps --version  # versions
tmux -L cenci-native new-session -d -s repro -c "$PWD"
tmux -L cenci-native new-window -d -n paneA -c "$PWD" -P -F '#{pane_id}'   # pane A
```

Hook capture (Linux, `/proc`; run it in its own pane and stop it with Ctrl-C). A hook lives for milliseconds, so it scans `/proc` directly instead of polling with `pgrep`. For each `cenci notify -agent codex` it prints the hook's `TMUX`/`TMUX_PANE` and its `ps -A -o pid= -o ppid= -o args=` rows up to pid 1:

```sh
python3 -I -c '
import os, subprocess, sys, time
seen = set()
while True:
    for d in os.listdir("/proc"):
        if not d.isdigit() or d in seen:
            continue
        try:
            argv = open(f"/proc/{d}/cmdline", "rb").read().split(b"\0")
            if b"notify" not in argv or b"codex" not in argv:
                continue
            env = open(f"/proc/{d}/environ", "rb").read().split(b"\0")
            rows = subprocess.run(["ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "args="], capture_output=True, text=True).stdout
        except OSError:
            continue
        seen.add(d)
        procs = {r.split(None, 2)[0]: r.split(None, 2) for r in rows.splitlines() if r.strip()}
        print("==", time.strftime("%T"), "hook pid", d)
        print(*[e.decode() for e in env if e.startswith((b"TMUX=", b"TMUX_PANE="))], sep="\n")
        p = d
        while p in procs:
            print(" ".join(procs[p]))
            p = procs[p][1]
        sys.stdout.flush()
    time.sleep(0.005)
' > hooks.txt
```

P1 and P22 (`<A>` and `<B>` are the pane ids printed by `new-window`; stop the shared app-server first with `codex app-server daemon stop` so pane A starts it):

```sh
tmux -L cenci-native send-keys -t <A> 'codex "Reply with the single word ok and nothing else. Do not run any tools."' Enter
pgrep -af 'codex app-server --listen'                          # the app-server pane A started
tr '\0' '\n' < /proc/<app-server-pid>/environ | grep '^TMUX'   # the pane it keeps
tmux -L cenci-native send-keys -t <A> /quit Enter              # quit the TUI; the app-server stays
tmux -L cenci-native kill-pane -t <A>
tmux -L cenci-native new-window -d -n paneB -c "$PWD" -P -F '#{pane_id}'   # pane B
tmux -L cenci-native send-keys -t <B> 'echo "TMUX=$TMUX TMUX_PANE=$TMUX_PANE pid=$(tmux display -p "#{pid}")"' Enter
tmux -L cenci-native send-keys -t <B> 'codex "Run the shell command sleep 90 exactly once, then reply with the single word ok."' Enter
cenci status                                                   # unproven, with the hint
```

Bind while the turn is still running: the sweep's Codex exit backstop untracks a done session whose pane no longer runs `codex`, so a bind after the turn ends can be undone by the next sweep. Suspend the TUI, bind, read the window options and resume in one line:

```sh
tmux -L cenci-native send-keys -t <B> C-z
tmux -L cenci-native send-keys -t <B> 'cenci bind <session-id>; echo exit=$?; tmux show-options -w | grep cenci; fg' Enter
tmux -L cenci-native capture-pane -p -a -t <B>                 # the shell output, saved while the TUI is back
```

P23: a single-client `codex exec "<prompt>"` in a pane, then two default TUIs in two new panes with prompts that start differently (`Client C: ...`, `Client D: ...`), so `cenci status` names which session id runs in which pane. Bind each from its own pane as above. Pane-id reuse: create and kill panes on the scratch server and list their ids. Server pid: `cd watch && go test ./internal/tmux -run TestListPanesReportsTheServerPID -v`. Clean up with `tmux -L cenci-native kill-server` only.

### Status

- P1 native reproduction: run on Linux (2026-11-19); results below.
- P22 native `cenci bind` verification: run on Linux (2026-11-19); results below.
- P23 native automatic association and two-client verification: run on Linux (2026-11-19) with the hook binary swapped for a build of this branch (the marker), then restored; results below.
- macOS: not run: no macOS host.

### Versions

| Component | Version |
|---|---|
| Codex | `codex-cli 0.167.2` (standalone, a static ELF at `<home>/.codex/packages/standalone/current/bin/codex`); the managed app-server daemon `0.170.0-x86_64-unknown-linux-musl` |
| cenci | `cenci 2.61.1` (daemon and Codex hook binary). `cenci` on `PATH` linked to an older 2.58.0 binary, so `cenci status` and `cenci bind` ran the 2.61.1 binary by its full path. P23's hook binary was a `dev` build of this branch, restored afterwards to the same sha256 |
| tmux | `tmux 3.8` |
| Host | `Linux 7.3.1`, `ps from procps-ng 4.0.7` |

### Hook-supplied identity versus pane B

The shared app-server is started from pane A, A is closed, and a thread runs from pane B through the installed host hook path. Pane B's values come from `echo "TMUX=$TMUX TMUX_PANE=$TMUX_PANE pid=$(tmux display -p '#{pid}')"` in pane B; the hook's from the capture above.

| Field | Hook-supplied | Pane B (real) |
|---|---|---|
| Pane id | `%2` (pane A, already killed) | `%3` |
| Socket | `/tmp/tmux-<uid>/cenci-1550` | `/tmp/tmux-<uid>/cenci-1550` |
| Server pid | `3322126` | `3322126` (`#{pid}` and `$TMUX`'s second field) |

An earlier run on the same host showed the cross-server case: a thread started from pane `%0` of the scratch server delivered hooks with `TMUX=/tmp/tmux-<uid>/default,17432,3` and `TMUX_PANE=%153`, the pane and the default server of the terminal that had first started a still-running app-server.

### Hook process ancestry

| Item | Value |
|---|---|
| Process names, hook to root | P1 pane B: `cenci` ← `zsh` ← `codex` ← `systemd` (pid 1). While pane A's TUI was still running, its threads' hooks ran under `cenci` ← `zsh` ← `codex` (the app-server) ← `codex` (pane A's TUI) ← `-zsh` (pane A's `pane_pid`) ← `tmux` ← `systemd` |
| Marker identifying the shared app-server | `app-server` (Linux only) |

The marker is in the nearest Codex ancestor's argv in every shared-app-server observation and absent from every single-client one. Linux `ps` printed every chain row whole: across 54 captured hooks, each `args` value `ps` listed equalled that process's `/proc/<pid>/cmdline`, the longest at 328 characters (in 5 captures the short-lived hook rows had already exited before the `ps` snapshot, so they were missing, never different). The second shared observation matters most: the app-server pane A's TUI auto-starts is the TUI's child, so its hook chain contains pane A's `pane_pid`, and only the marker keeps that thread from being proven in pane A.

| Observation | Kind | Nearest Codex ancestor's argv |
|---|---|---|
| Thread on a pre-existing managed app-server (hook pane `%153`, default server) | shared | `codex app-server --listen <arg> --analytics-default-enabled -c <arg> -c <arg> -c <arg> -c <arg> --managed-daemon` (its parent: `codex app-server daemon pid-update-loop`) |
| Pane A's TUI auto-starts the app-server (hook pane `%2`) | shared | `codex app-server --listen <arg> --analytics-default-enabled -c <arg> -c <arg> -c <arg> -c <arg> --managed-daemon` (its parent: `codex <arg>`, pane A's TUI) |
| P1 pane B after pane A closed (hook pane `%2`) | shared | the same argv, its parent now pid 1 |
| P23 clients C and D (hook pane `%2`) | shared | the same argv, its parent pid 1 |
| `codex exec` in a pane (hook pane `%1`, four runs) | single client | `codex exec <arg>` (its parent: `-zsh`, the pane's `pane_pid`) |
| TUI with the daemon auto-start off (hook pane `%1`) | single client | `codex -c <arg> <arg>` (its parent: `-zsh`, the pane's `pane_pid`) |

### P22 steps

| Step | Result |
|---|---|
| Shared app-server started from pane A | `codex <arg>` in pane `%2` auto-started `codex app-server --listen <arg> ... --managed-daemon` as its child. The app-server's environment: `TMUX=/tmp/tmux-<uid>/cenci-1550,3322126,0`, `TMUX_PANE=%2` |
| Pane A closed | `/quit` ended the TUI; the app-server kept running, reparented to pid 1. `tmux kill-pane -t %2`; `list-panes -a` then listed `%0 %1` |
| Thread runs in pane B, `cenci status` shows it unproven with the hint | Hooks carried `TMUX_PANE=%2`. `cenci status`: `(no session) - Run the shell command sleep... (codex) (running)` and `hint: hooks were delivered but no tmux association is proven; run 'cenci bind <session-id>' in the session's tmux pane` |
| `cenci bind <session-id>` run from pane B | With the TUI suspended (Ctrl-Z) during the running turn: `bound Codex session <session-id> to tmux pane %3`, `exit=0`. `cenci status` then: `repro:3 - paneB (codex) (running)`; the snapshot: pane `%3`, association `bound` |
| Pane B's window shows `@cenci-symbol`/`@cenci-style` | `tmux show-options -w -t %3` while running: `@cenci-style fg=blue,dim`, `@cenci-symbol ▶`; after the turn: `@cenci-style fg=green,dim`, `@cenci-symbol ✓`, and `cenci status`: `repro:3 - paneB (codex) (done)` |

### P23 results

| Check | Result |
|---|---|
| Automatic association | `codex exec "Run the shell command sleep 20 exactly once, then reply with the single word ok."` in pane `%1`, no bind: the snapshot showed pane `%1`, socket `/tmp/tmux-<uid>/cenci-1550`, status `running`, association `proven`; `tmux show-options -w -t %1`: `@cenci-style fg=blue,dim`, `@cenci-symbol ▶`. Its hooks' nearest Codex ancestor was `codex exec <arg>` |
| Two clients | TUIs `Client C` in pane `%7` and `Client D` in pane `%8`, both on the shared app-server (hooks carried `TMUX_PANE=%2`, nearest Codex ancestor `codex app-server ...`): both listed `(no session) ... (codex) (running)` with the hint, association `unproven`. `cenci bind` from `%7`: `bound Codex session <session-id> to tmux pane %7`, `exit=0`; from `%8`: `bound Codex session <session-id> to tmux pane %8`, `exit=0`. The snapshot then: `Client C` on pane `%7`, `Client D` on pane `%8`, both `bound` |

The two-client prompts must start differently. A first attempt with one identical prompt in both panes was abandoned before binding, because nothing tied either session id to its pane; ordering by the time-sortable session id would have bound them crosswise, since `Client D`'s id sorted before `Client C`'s.

### Assumptions

| Assumption | Outcome | Evidence |
|---|---|---|
| tmux's `#{pid}` equals the second field of `$TMUX` | confirmed | `go test ./internal/tmux -run TestListPanesReportsTheServerPID -v`: `--- PASS: TestListPanesReportsTheServerPID (0.07s)`. In pane B, `$TMUX` was `/tmp/tmux-<uid>/cenci-1550,3322126,0` and `tmux display -p '#{pid}'` printed `3322126` |
| Pane ids are not reused within one server lifetime (so a live pane's `pane_pid` stays the same) | confirmed | On the scratch server panes were created in the order `%0 %1 %2 %3 %4 %5 %6 %7 %8 %9`; `%2` was killed before `%3` was created, and `%4`, `%5` and `%6` were each created and killed in turn. After those kills, `list-panes -a` listed `%0 %1 %3`, and the next panes were `%7` and `%8`, never a killed id |
| Codex's argv[0] basename is exactly `codex` | confirmed | Every Codex row in the ancestry evidence above: the standalone CLI runs as `codex`, the app-server as `<home>/.codex/packages/app-server-daemon/releases/<arg>/bin/codex`. No npm/Node install was observed on this host |
| `ps` prints `args` untruncated | confirmed on Linux (procps-ng 4.0.7); not run: no macOS host | Every listed chain row's `args` equalled `/proc/<pid>/cmdline`. BSD/macOS `ps` may truncate the column, so the marker table has no macOS entry |

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
| Bounded `ps` parser, walk, depth cap, cycle, nearest-Codex classifier | `TestReadWith*`, `TestReadRunsTheRealPS` in `internal/procancestry` |
| The marker table holds `app-server` for Linux only; the recorded native chains (three shared-server, two single-client) classify as unproven and proven on Linux and all unproven on any other platform | `TestProductionMarkerTableHoldsAppServerForLinuxOnly`, `TestProductionMarkerTableClassifiesTheRecordedNativeChains`, `TestReadWithDefaultsToTheRunningPlatformsMarkers` in `internal/procancestry` |
| `pane_pid` column of the pane listing | `TestParsePanesReadsPanePID`, `TestExecClientListPanesAsksForPanePID` |
