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

## UI Conventions

- Component library: `<path-to-component-library>`
- Component catalog: `<storybook-like-app-path>` — run with `<catalog-command>`
- Reuse an existing component before authoring a new one. Browse the catalog first;
  only add a component when nothing there covers the need, and put it in the library
  rather than beside the feature that needed it.
- <project-specific UI rules populated during configure>
<!-- END IF -->

## Reference Docs

Repository conventions live in `<repo-root>/docs/` and are read on demand.
