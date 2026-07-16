# <project-name>

<stack> project within the <repo-name> monorepo.

## Stack

- <framework + version>
- Tests: <test-framework>

## Build and Test

```bash
<build-command>
<test-command>
```

## Conventions

- <project-specific rules populated during configure>

<!-- IF backend/API project -->
## Security

- Use parameterized queries or an ORM for database access.
- Validate input and enforce authorization at every endpoint.
<!-- END IF -->

<!-- IF frontend project -->
## Security

- Sanitize untrusted input before rendering.
<!-- END IF -->

## Reference Docs

Repository conventions live in `<repo-root>/docs/` and are read on demand.
