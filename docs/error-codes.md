# Error Codes

Cenci uses a stable, structured error-identifier scheme so failures can be
referenced, documented, and matched programmatically instead of relying on
ad hoc free-text messages.

For meaning, common causes, diagnostic commands, a recovery procedure, and
known platform-specific issues per code, see the
[failure atlas](failure-atlas.md). This page owns the naming convention and
a short one-line meaning per code; the atlas is the single home for the
fuller recovery content, so the two never carry duplicated, driftable text.

## Naming convention

```
CENCI-<AREA>-<SUBAREA>-<NNN>
```

- `AREA` — the broad component (e.g. `SANDBOX`, `DAEMON`, `CREDENTIALS`).
- `SUBAREA` — narrows it further (e.g. `START`, `SOCKET`, `CODEX`).
- `NNN` — a zero-padded three-digit sequence number **scoped per
  `AREA-SUBAREA` pair**: numbering restarts at `001` for each distinct
  `AREA-SUBAREA` combination, not globally.

Examples: `CENCI-SANDBOX-START-001`, `CENCI-DAEMON-SOCKET-002`,
`CENCI-CREDENTIALS-CODEX-003`.

Codes are declared as exported `Code` constants in
`watch/internal/errcode/errcode.go` and registered in that package's
data-driven `registry`, each mapping to an `Entry` (`Message`, `Causes`,
`Hints`) retrievable via `Lookup`. `init()` validates every registered code
against the format above and panics at load time on a malformed or
duplicate code, and cross-checks that every declared constant has a
registry entry — so a drift between the two fails fast instead of silently
degrading to an unregistered code.

Consumers should always use the exported `Code` constants rather than bare
string literals.

## Registry validation (for implementers)

When adding new `Code` constants to the `errcode` package:

1. Always declare the new `Code` constant in the `const` block.
2. Always add it to the `allCodes` slice — this powers the bidirectional validation check.
3. Always add its entry to the `registry` map with diagnostic content.

The `init()` function enforces both directions:
- **Forward**: every entry in `registry` is well-formed (matches `CENCI-<AREA>-<SUBAREA>-<NNN>` format).
- **Backward**: every constant in `allCodes` has a corresponding `registry` entry.

If you declare a new `Code` constant but forget to add it to `allCodes` or `registry`, the `init()` function will panic at load time with a clear error. This prevents a silent drift where an unused constant or orphaned registry entry goes undetected and causes Lookup to silently fail in the future. Always check that both directions are covered, not just the forward direction.

### Registry-only codes (reserved for future work)

When adding a code that is registry-only — registered in the constant and registry but not yet wired into any detection path or command — clearly mark it as such:

- In the registry entry comment, add a `Registry-only:` clause explaining why it's deferred and which future work will wire it (e.g., `Registry-only: reserved for wiring by ticket #XYZ`).
- In the Go doc comment above the `Code` constant, note that it is reserved for future use and is not currently attached by any command.

This prevents readers and future implementers from assuming the code is actively produced and detected in the current implementation. Example: `CENCI-SANDBOX-START-003` (readiness timeout) is registry-only for #572; only after the next ticket's work will `waitUntilReady` or `diagnose` attach it.

## Registered codes

| Code | Area / Subarea | Meaning |
|---|---|---|
| `CENCI-SANDBOX-START-001` | Sandbox / Start | The sandboxed agent CLI path is missing or not executable. |
| `CENCI-SANDBOX-START-002` | Sandbox / Start | The sandbox entrypoint failed during container startup (boot log, generic entrypoint-trap marker, container runtime logs, or fully generic fallback). |
| `CENCI-SANDBOX-START-003` | Sandbox / Start | The sandbox container did not signal readiness within the readiness-poll budget. Registry-only: registered (message, causes, hints) but not yet wired into any detection path — neither `waitUntilReady` nor `cenci diagnose` attaches it today (see below). Reserved for future use. |
| `CENCI-SANDBOX-SESSION-001` | Sandbox / Session | No container exists for the requested sandbox session (never launched, launched under a different scope, or already auto-removed by `--rm`). Attached by `cenci diagnose`. |
| `CENCI-DAEMON-CONN-001` | Daemon / Conn | The cenci daemon's event socket exists but did not answer a read-only dial. Attached by `cenci diagnose`. |
| `CENCI-DAEMON-SOCKET-001` | Daemon / Socket | The cenci daemon's event socket does not exist at all. Attached by `cenci diagnose`. |

`CENCI-SANDBOX-START-001` and `CENCI-SANDBOX-START-002` are attached by
`watch/internal/sandbox/launcher/launch.go`'s `startupFailureDetail` /
`waitUntilReady`, alongside the existing verbatim failure detail (e.g.
`container '<name>' failed during startup (status ..., exit ...)
[CENCI-SANDBOX-START-001]: <verbatim detail>`).

`CENCI-SANDBOX-START-003` (readiness timeout), `CENCI-SANDBOX-SESSION-001`
(session/container not found), `CENCI-DAEMON-CONN-001` (daemon unreachable),
and `CENCI-DAEMON-SOCKET-001` (event socket missing) were added for `cenci
diagnose <session>` (#572), a read-only report command in
`watch/internal/sandbox/launcher/diagnose.go` / `watch/diagnose_cmd.go`. Each
finding it reports is classified fatal/degraded/warning (see that file's
`severityForCode`). Of the three codes `diagnose` actually attaches
(`CENCI-SANDBOX-SESSION-001`, `CENCI-DAEMON-CONN-001`,
`CENCI-DAEMON-SOCKET-001`), none is the readiness-timeout code:
`CENCI-SANDBOX-START-003` is registry-only — `severityForCode` maps it (so a
future caller gets the right severity for free) but neither `waitUntilReady`
nor `diagnose` currently detects a stuck/never-ready container and attaches
it. `waitUntilReady`'s own `did not become ready within 60 seconds` message
still does not attach `CENCI-SANDBOX-START-003` itself — wiring a
readiness-timeout detection path (in `launch.go`, `diagnose.go`, or both) to
attach it is left to a follow-up.
