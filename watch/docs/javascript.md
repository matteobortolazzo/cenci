# JavaScript / JXA Frontend Patterns

Conventions and gotchas specific to JavaScript-for-Automation (JXA) and JavaScript frontends in cenci plugins.

## Rules

- **`new Date(string)` does not throw on unparseable input — validate with `isNaN(date.getTime())`**. In JavaScript, `new Date("unparseable")` silently creates an `Invalid Date` object. Calling `.getHours()`, `.getMinutes()`, or other date methods on `Invalid Date` returns `NaN`, rendering misleading UI (e.g., "last run NaN:NaN" with no error indication). Always check `isNaN(date.getTime())` after parsing and fall back to rendering the raw input string on failure, keeping malformed data visible. This mirrors the Go pattern in `status.go` where `time.Parse` errors are caught and the raw RFC3339 string is rendered as fallback. See `plugin/macos/cenci.5s.sh` lines 174–182 for the correct pattern.
