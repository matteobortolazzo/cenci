# Native Claude measurements: the producer (#1451)

Owner doc for the producer side of the `native-measurement` envelope that the daemon
ingests (#1450, `internal/measurement`, `internal/daemon/measurement.go`). It records
the producer contract and the evidence gathered so far. The user-facing option is in
`watch/README.md` (`#### Native measurements in Claude Code (nativeMeasurements)`).

Status: the contract and the API facts below are verified. Real host-mode and
sandbox-mode samples and the authentication-mode coverage are **not yet captured**;
until they are, nothing here may be used to let native quota control dispatch (see
`Before native quota may control dispatch`).

## Producer contract

- **Option**: `nativeMeasurements` in the plugin's `userConfig` (boolean, default
  `false`), independent of `statusCommand`. `hooks/register.js` registers its
  `session.measure` hook only when the option is exactly `true`; otherwise it reads no
  usage and starts no process. `hooks.json` keeps exactly one module entry.
- **Read**: on each `session.measure` event the hook awaits `$.session.usage()`,
  `$.session.id()` and `$.session.cwd()` (all async; the event input
  `{context, rateLimits, cost?, changed}` carries neither a session id nor a cwd). A
  rejection, `null`, an array or any other non-object from `usage()` delivers nothing.
  The hook always continues with `next(e)` and never waits for the delivery.
- **Allowlist**: the mod builds a snake_case body with only `session_id`,
  `measured_at` (`new Date().toISOString()` when `usage()` was read, kept while a
  sample waits in the queue), `context{tokens,window,percent}` and
  `rate_limits[{kind,percent_used,resets_at}]`. `cost`, `startedAt`, `context.breakdown`
  and every other field stay in the mod process. Absent figures stay absent (no
  fabricated zeros). `resetsAt` is an ISO 8601 string in the declarations, so strings
  pass through and any other value is forwarded raw for the daemon to mark `invalid`.
- **Transport**: stdin only. `$.process.run` accepts `{ cwd, env, stdin, timeoutMs }`,
  so the mod runs `['/bin/sh', '<root>/hooks/measure.sh']` with `cwd` the plugin root,
  `timeoutMs: 5000` and the payload `{"cwd": <abs path>, "measurement": {...}}` on
  stdin. There is no argv transport.
- **Wrapper**: `hooks/measure.sh` resolves the binary exactly like `status.sh` (plugin
  `bin/cenci`, then the shared resolver) and `exec`s
  `cenci notify -agent claude -measurement`, passing stdin through. With no binary it
  prints `cenci binary not found` to stderr and exits 127; it writes no hook-health
  record. The mod ignores the result.
- **notify mode**: reads at most 4096 bytes (the receiver's line cap), accepts only
  `-agent claude`, drops a cwd matched by `watch.excludeCwd` (the cwd is only compared,
  never forwarded), rebuilds the body from the same allowlist (nested keys included),
  sets the constant `source` `session.measure`, checks the result with
  `measurement.Parse` and drops it on a whole-sample error (bad session id, bad
  `measured_at`, malformed). A bad single field is forwarded as sent so the daemon names
  it in `invalid`. It sends through `ipc.SendNativeMeasurement`, never `DeliverEvent`.
  Every outcome is silent with exit 0 and writes no hook-health record.
- **No autostart**: with no daemon listening the sample is dropped; no daemon, PID file
  or socket is created.
- **Coalescing**: per session id, one delivery is in flight and at most one newer sample
  waits behind it; the newest queued sample replaces the one before it. A timeout
  rejection (`aborted: still running after 5000ms`), a start failure or a non-zero exit
  frees the slot and the queued sample is still sent. No failure toasts.
- **What the mod never touches**: no Node socket, no `process.spawn` (it has no
  timeout), no `http.fetch`, no model, prompt or session-send entry point.

## Verified in this build (Claude Code 2.1.319)

Checked on 2.1.319 from the mods type declarations and the shipped usage builder in
the installed executable. These are API facts, not live samples.

| Question | Finding |
|---|---|
| Minimum version | v2.1.319 is the version verified; `plugin.json` says "Needs Claude Code v2.1.319 or later". `statusCommand` alone needs v2.1.287. An earlier version that has the same API was not tested. |
| Event | `session.measure` input is `{context, rateLimits, cost?, changed}`; it has no session id and no cwd. |
| Usage | `$.session.usage()` answers `{startedAt, context, rateLimits, cost?}` (async). |
| `context` | `{tokens?, window, percent?}`. With no input tokens yet only `window` is present. With no session bound the context is answered empty. A `breakdown` is added only when asked for, which this mod never does. |
| `rateLimits[].kind` | `five_hour`, `seven_day`, and `spend_limit` (the last only when the provider is a gateway). The daemon's table maps the first two; `spend_limit` is a spend cap, not a time window, and stays `unknown:spend_limit`. |
| `rateLimits[].percentUsed` | A number; the builder derives it from the window utilization. |
| `rateLimits[].resetsAt` | An ISO 8601 string (the builder calls `toISOString()` on the window's reset time). No unit conversion is needed. |
| `cost` | `{ usd: number }`; the unit is US dollars. It is not sent (P46). It is optional, so it may be absent. Whether a plan login reports a meaningful value is not verified. |
| `$.session.id()` / `$.session.cwd()` | Async accessors. Whether `$.session.id()` always equals the hook `session_id` the daemon keys on is not yet confirmed live (listed below). |
| Process API | `$.process.run(argv, { cwd, env, stdin, timeoutMs })`; a timeout rejects with `aborted: still running after <N>ms`. |
| Harness | `claude-code/testing` can raise `session.measure`, stub `session.usage`, `session.id`, `session.cwd` and `process.run`, and a `process.run` stub can stay pending (used by the coalescing tests). |
| Version accessor | None used; `claude_version` is not sent. |

Commands run in the sandbox with the installed executable (`claude` is not on `PATH`;
the executable is the one the sandbox ships):

- `claude plugin test watch/plugin`: 67 pass, 0 fail across 2 files (`register.test.ts`
  and `measure.test.ts`).
- `claude plugin validate watch/plugin`: validation passed. Its only warning is the
  missing plugin icon, which concerns the plugin directory and not this change.

## Host-mode and sandbox-mode samples

Not yet captured. They need a live session with `nativeMeasurements: true` and a
throwaway probe mod (never committed) that logs the raw `usage()`. Record only redacted
values: no session ids, account or organization ids, emails, tokens or prompts.

| Run | Claude Code version | Auth mode | Daemon received the sample | Sample (redacted) |
|---|---|---|---|---|
| Host | Not yet captured | Not yet captured | Not yet captured | Not yet captured |
| Sandbox (`cn`) | Not yet captured | Not yet captured | Not yet captured | Not yet captured |

Field availability by mode:

| Field | Host | Sandbox |
|---|---|---|
| `context.tokens` | Not yet captured | Not yet captured |
| `context.window` | Not yet captured | Not yet captured |
| `context.percent` | Not yet captured | Not yet captured |
| `rate_limits[]` (which kinds) | Not yet captured | Not yet captured |
| `rate_limits[].resets_at` | Not yet captured | Not yet captured |
| `cost` units and presence | Not yet captured | Not yet captured |

Authentication modes tested: none yet (for example `/login` plan and API key).

## Before native quota may control dispatch

All of these are still required. Until they are done a native sample is display-only.

1. A real host-mode sample and a real sandbox-mode sample, redacted, recorded above,
   with the Claude Code version of each run.
2. Confirmation that a sample reaches the running daemon in both modes (the session's
   `native_measurement` appears in the state snapshot), which also proves
   `$.session.id()` equals the hook `session_id`.
3. Each authentication mode that will be supported (`/login` plan, API key, any
   gateway), with which of `rateLimits` and `cost` it reports. A mode that was not
   tested stays unsupported.
4. The observed `resetsAt` value against the documented ISO 8601 string, in each mode.
5. The `cost` units for a follow-up that may send it (the declarations say USD; the
   observed value for each mode is not yet recorded).
6. The behavior when a session has no input tokens yet and when `usage()` reports no
   rate limits (an empty array), so a dispatch decision never reads an empty window as
   headroom.
7. Whether the earliest Claude Code version with this API is older than v2.1.319.

## Redaction checklist

Before committing any sample: remove session ids, account and organization ids,
emails, tokens, prompts and tool output; keep only field names, numbers, kinds and
timestamps rounded to a window that identifies no account. Then grep this file for
session-id-shaped (UUID) and token-shaped (`sk-`, long base64) strings.
