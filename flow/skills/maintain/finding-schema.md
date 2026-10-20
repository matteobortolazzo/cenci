# Maintenance Finding Reference

The shared finding format every `/cenci:maintain` analyzer reports in. The maintain procedure (`SKILL.md` on Claude Code, `codex.md` on Codex) puts this file's absolute path in every maintainer's launch prompt, and each maintainer reads it before analysis. A maintainer's own file supplies what is role-specific: its audit heading, category name(s), ID prefix, field meanings, no-findings sentence and analysis steps.

## Finding Fields

Report every finding with exactly these fields. A maintainer's role-specific meaning of a field wins over the generic meaning below.

- **ID** — a short stable identifier made of the maintainer's prefix and a two-digit counter, e.g. `STR-01`
- **Category** — one of the category names the maintainer owns
- **Severity** — Critical | High | Medium | Low
- **Location** — file path (and section/line where applicable)
- **Evidence** — the concrete `Read`/`Grep` result supporting the finding, redacted per the rule below
- **Proposed change** — the specific edit that would resolve it
- **Repair confidence** — High | Medium | Low — how mechanically safe an automated apply would be
- **Required tests** — the test(s) that must pass after the repair, or "manual verification" if none apply

## Redaction

If a scanned file's `Read`/`Grep` result would reproduce a credential-like value (API key, token, password) or PII in Evidence, redact the sensitive value and quote only the surrounding context — never paste the value itself into a finding.

Precedence: a maintainer's role-specific redaction restriction adds to this base rule and never relaxes it. Where both apply, the stricter one wins.

## Output Template

Replace `<Audit heading>` with the maintainer's audit heading and `<PREFIX>` with its ID prefix.

```markdown
## <Audit heading>

### Findings

#### [<PREFIX>-01][HIGH] <title>
- **Category**: <category>
- **Location**: `path/to/file`
- **Evidence**: <quoted Read/Grep result>
- **Proposed change**: <specific edit>
- **Repair confidence**: High | Medium | Low
- **Required tests**: <test path or "manual verification">

### Recommendations
- <improvements that don't rise to individual findings>
```

When the audit has no findings, keep the heading and report only the maintainer's own no-findings sentence under `### Findings`:

```markdown
## <Audit heading>

### Findings
<the maintainer's no-findings sentence>
```
