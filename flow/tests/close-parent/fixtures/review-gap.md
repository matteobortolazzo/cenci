## Acceptance Review

| # | Kind | Item | Status | Proof |
|---|---|---|---|---|
| 1 | acceptance criterion | `GET /items` returns 200 with a JSON array | covered | PR #510 — `tests/api.test.sh::returns 200` |
| 2 | edge case | An empty list renders the `No items yet` placeholder | missing | no merged PR touches the empty state |
| 3 | documentation item | README documents the `--format` flag | partial | PR #511 adds the flag; README still undocumented |

### Verdict
fail — 1 missing, 1 partial
