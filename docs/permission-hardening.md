# Permission hardening

Rules for writing and auditing permission/deny-list configurations that narrow access to security-critical tools and APIs.

## Rules

- When writing deny-list patterns for a CLI tool that uses pflag/Cobra option parsing (e.g., `gh`, `git`), enumerate all syntactic forms the parser accepts: long form (`--method M`), long-equals form (`--method=M`), short form (`-X M`), short-combined form (`-XM`), and short-equals form (`-X=M`). The equals-form shorthand (e.g., `-X=DELETE`) is particularly easy to miss in planning and causes real bypasses. When planning deny-list work, explicitly list all forms, test against the actual CLI parser, and verify the final patterns block all variants.

- When writing residual-gap or security prose after implementing a narrow permission hardening (e.g., denying specific HTTP methods on `gh api`), audit whether broader permission grants (like a blanket `Bash(tool:*)` allow) still permit the same action through other code paths that bypass the narrow fix. Document these gaps explicitly in prose — e.g., "native `gh` subcommands like `gh repo delete` and `gh secret set` remain allowed and are not blocked by the `gh api` method restrictions" — so readers understand what the fix actually achieves and what remains unguarded.
