## Acceptance Review

| # | Kind | Item | Status | Proof |
|---|---|---|---|---|
| 1 | acceptance criterion | `GET /items` returns 200 with a JSON array | covered | PR #510 — `tests/api.test.sh::returns 200` |
| 2 | edge case | An empty list renders the `No items yet` placeholder | covered | PR #511 — `ui/list.spec.ts::empty state` |
| 3 | documentation item | README documents the `--format` flag | covered | PR #511 — `README.md#format` |

### Verdict
pass — 3 covered
