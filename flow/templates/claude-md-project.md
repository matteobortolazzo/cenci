# <project-name>

<stack> project within the <repo-name> monorepo.

## Stack
- <framework + version>
- Tests: <test-framework>

## Build & Test
```bash
<build-command>
<test-command>
```

## Conventions
- <project-specific rules populated during configure>

<!-- IF backend/API project -->
## Security
- Parameterized queries (or ORM) for all database access
- Input validation on all endpoints
- Authorization checks on all endpoints
<!-- END IF -->
<!-- IF frontend project -->
## Security
- Sanitize user input before rendering
<!-- END IF -->

<!-- IF pencil.enabled AND project has designPath -->
## Design
- Design spec: `<designPath>/DESIGN.md` — screens, components, tokens for this project
- Design file: `<designPath>/<name>.pen` — open in Pencil, read with Pencil MCP tools
- Read DESIGN.md before implementing any frontend feature in this project
<!-- END IF -->

## Reference Docs
Repo-level conventions live at `<repo-root>/docs/` (read on demand). Project-specific notes belong in this file.
