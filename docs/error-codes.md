# Error Codes

Cenci uses a stable, structured error-identifier scheme so failures can be
referenced, documented, and matched programmatically instead of relying on
ad hoc free-text messages.

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

## Registered codes

| Code | Area / Subarea | Meaning |
|---|---|---|
| `CENCI-SANDBOX-START-001` | Sandbox / Start | The sandboxed agent CLI path is missing or not executable. |
| `CENCI-SANDBOX-START-002` | Sandbox / Start | The sandbox entrypoint failed during container startup (boot log, generic entrypoint-trap marker, container runtime logs, or fully generic fallback). |

These two codes are attached by `watch/internal/sandbox/launcher/launch.go`'s
`startupFailureDetail` / `waitUntilReady`, alongside the existing verbatim
failure detail (e.g. `container '<name>' failed during startup (status ...,
exit ...) [CENCI-SANDBOX-START-001]: <verbatim detail>`).

The `did not become ready within 60 seconds` readiness-timeout path does not
yet attach a code — that classification is deferred to a follow-up ticket
(#572, the diagnose command work) rather than this foundation ticket.
