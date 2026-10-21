# Runtime, CI and Sandbox Questions

Read this file on every run, after `phases/mcp-lsp-ui-questions.md` (see `SKILL.md` `### Step Map`). It holds questions 7 to 9c — auto-compact, the subagent pin, the CI/CD pipeline with its Stack-to-CI mapping table, and the sandbox Dockerfile, nested Docker and Azure CLI questions with the Stack-to-fragment mapping table. Ask each question only when its ID is in `delta.ask`, as `SKILL.md` `### Question Defaults` describes; the mapping tables are read on every run because steps 5d and 5e use them.

7. **Disable auto-compact**: "Do you want to disable Claude Code's auto-compact feature?
    Auto-compact compresses conversation history as the context window fills,
    which can lose important context during long sessions. (Recommended: Yes — disable it)"
   - Default: Yes
   - If Yes: set `"autoCompactEnabled": false` in `~/.claude/settings.json` using jq (see step 5c for the exact jq/mv sequence, and its Write-then-mv variant when the file doesn't exist yet)
   - If No: remove the `autoCompactEnabled` key from `~/.claude/settings.json` (if present)
   - Either way, also remove any `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` key from the `env` object in `~/.claude/settings.json`. Earlier versions of this skill set it to `"1"` believing that meant manual-only compaction; Claude Code interprets it as "compact once 1% of the context window is used", so any leftover value causes constant compaction and must be purged.

7b. **Pin subagents to 200K context**: "Do you want to pin cenci's subagents to a 200K-context
    model? cenci delegates reviews to subagents, and on 1M-context sessions that delegation can be
    gated — every subagent inherits the session's 1M flag but not its extra-usage entitlement, so
    reviews fail with 'Usage credits required for 1M context' (Claude Code bug #51060). Pinning
    subagents to Sonnet 200K keeps reviews working while your main session keeps its 1M context.
    (Recommended: Yes if you run a 1M-context session; No otherwise — see the tiering caveat below)"
   - Default: Yes when the session plausibly runs a 1M model; No otherwise
   - If Yes: merge `{"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}` into `~/.claude/settings.json` using jq (see step 5c-bis for the exact jq/mv sequence, and its Write-then-mv variant when the file doesn't exist yet). This runs all `Task` subagents on Sonnet 200K regardless of the main session model. (Pin Sonnet, not Opus — Opus is auto-upgraded to 1M on Max/Team/Enterprise plans and would re-trigger the gate.)
   - If No: remove the `CLAUDE_CODE_SUBAGENT_MODEL` key from the `env` object in `~/.claude/settings.json` (if present)
   - **Tiering caveat (state regardless of answer)**: the pin overrides every agent's `model:` frontmatter — cenci's model tiering (opus refiner/planner/security-reviewer, haiku context-gatherer/structure-analyzer/lessons-collector) is flattened onto the pinned model while it is set. On a standard 200K session the 1M gate never fires, so answer No there to keep the tiering active.
   - **Caveat (state regardless of answer)**: this only affects **new** sessions — restart after configuring. If subagent reviews still fail with the 1M gate even after pinning (the pin didn't strip `[1m]`), run `/model sonnet` for the current session, which always yields 200K.

8. **CI/CD pipeline**: "Do you want to generate a CI/CD pipeline?"
   - Options: "Yes — generate a CI workflow", "No — skip"
   - Default: No
   - Platform: GitHub Actions

   **Conflict check**: If the user selects Yes, scan for existing CI configuration:
   - Use the `Glob` tool with patterns `.github/workflows/*.yml` and `.github/workflows/*.yaml`

   If any files are found, present them and ask:
   - **Single file**: "Found existing CI configuration at `<path>`. What would you like to do?"
   - **Multiple files**: "Found existing CI workflows:\n  - `<path1>`\n  - `<path2>`\n  ...\nWhat would you like to do?"

   Options: "Overwrite — generate `ci.yml` (existing files are not deleted)", "Skip — keep existing files", "Show existing — display the current file contents"
   - If Skip: still record `cicd` in config.json, don't write the file
   - If Show existing: read and display each file, then re-ask Overwrite/Skip

   **Stack-to-CI mapping**: Use the detected stack from question 1 to select the appropriate lint, build, and test commands:

   | Stack | Lint | Build | Test |
   |---|---|---|---|
   | `dotnet*` | `dotnet format --verify-no-changes` | `dotnet build --no-restore` | `dotnet test --no-build --collect:"XPlat Code Coverage"` |
   | `go` | `golangci-lint run ./...` | `go build ./...` | `go test ./... -coverprofile=coverage.out` |
   | `python` | `ruff check .` | *(none)* | `pytest --cov=. --cov-report=xml` |
   | `rust` | `cargo clippy -- -D warnings` | `cargo build` | `cargo test` |
   | `angular*` | `ng lint` | `ng build --configuration production` | `ng test --watch=false --code-coverage` |
   | `react` / `next` | `npx eslint .` | `npm run build` | `npm test -- --coverage --watchAll=false` |
   | `vue` | `npx eslint .` | `npm run build` | `npm run test:unit -- --coverage` |

   **Package manager detection** (for Node-based stacks): use `detection.packageManager`. Manual fallback:
   - `pnpm-lock.yaml` → pnpm
   - `yarn.lock` → yarn
   - `package-lock.json` → npm
   - .NET → NuGet cache on `**/*.csproj`
   - Go → built-in cache in `actions/setup-go@v5`

   **Version pinning**:
   - Node: `engines.node` from `package.json`, fallback `"20"`
   - .NET: extract from stack token (`dotnet10` → `"10.x"`)
   - Go: first line of `go.mod`
   - Python: `python-requires` from `pyproject.toml`, fallback `"3.12"`
   - Rust: `rust-toolchain.toml` or `"stable"`

9. **Sandbox Dockerfile**: "Generate a sandbox Dockerfile for this repo? (Tailors the sandbox image to your stack; committed so your team shares it.)"
   - Options: "Yes — generate `.cenci/Dockerfile`", "No — skip"
   - Default: Yes

   **Mandatory agent runtime**: Always include `node.dockerfile`, regardless of the detected project stack. Claude Code and Codex are npm-distributed launchers installed by the isolated shared-volume updater, so generated images need Node.js but must not bake either agent CLI.

   **Stack-to-fragment mapping**: In addition to the mandatory Node runtime fragment, use the detected stack from question 1 (or, for monorepos, the union of every `projects[].stack.framework` value) to select which `sandbox/fragments/*.dockerfile` blocks to include:

   | Detected stack | Fragment |
   |---|---|
   | `dotnet*` | .NET SDK block (version from the token) |
   | `angular*` / `react` / `next` / `vue` / `node` | Node block |
   | `angular*` / `react` / `next` / `vue` | Playwright block (Chromium, for `verify-ui` screenshot capture) |
   | `go` | Go block |
   | `python` | Python + uv block |
   | `rust` | Rust block |
   | *(none — config-selected)* | Docker block, when `sandbox.dind: true` (question 9b) |
   | *(none — config-selected)* | Azure block, when `sandbox.azure: true` (question 9c) |

   Playwright is scoped to the frontend-framework tokens, not plain `node` — a Node-based
   backend/API project gets the Node block without paying Chromium's image-size cost for a
   visual-verification step it will never run.

   **Docker fragment** (config-selected, not stack-token-selected): when the config being
   written enables nested Docker (`sandbox.dind: true`, question 9b), additionally include
   `sandbox/fragments/docker.dockerfile` — it installs the Docker CLI, the `docker-ce`
   engine and `containerd.io` so `entrypoint.sh` can start an inner daemon under
   `sysbox-runc`. This block used to live in `Dockerfile.base` and so shipped in every
   image; since #831 it is selected only for repos that actually run nested Docker.
   **A `dind: true` repo whose `.cenci/Dockerfile` omits this fragment builds an image
   with no `dockerd`** — the sandbox still boots and stays usable, but nested Docker is
   unavailable and `~/.cenci-dockerd-startup-error` explains why. So when Q9 is answered
   No (no `.cenci/Dockerfile` generated) and Q9b is answered Yes, tell the user their repo
   will use the shared `cenci-sandbox:latest` monolith, which carries the Docker block
   already — no action needed.

   **Azure fragment** (config-selected, not stack-token-selected): when the config being
   written enables the Azure CLI (`sandbox.azure: true`, question 9c), additionally
   include `sandbox/fragments/azure.dockerfile` — it installs `az` from Microsoft's apt
   repo so an agent can check real command syntax with `az <group> <cmd> --help` instead
   of guessing it. There is no stack token for this: an Azure repo can be written in any
   language, so the opt-in is the only signal. Unlike the Docker fragment there is **no**
   monolith fallback — `cenci-sandbox:latest` carries no `az` either — so a repo that
   answers Yes to Q9c but No to Q9 gets no Azure CLI at all; tell the user Q9 must also be
   Yes for the CLI to reach their sandbox.

   **Monorepo**: take the union of all `projects[].stack.framework` values, deduplicated — e.g. a repo with a Go API project and a React web client project selects both the Go and Node fragments. Node is still emitted only once because the mandatory runtime set and stack-selected set are deduplicated.

   A stack token that matches no row above (e.g. `markdown-shell`, `docker-shell`) contributes no additional project fragment. This is not an error — the generated Dockerfile still contains the mandatory Node runtime fragment.

   **.NET version substitution** (the only row with a version-from-token adjustment): `sandbox/fragments/dotnet.dockerfile` ships with `ARG DOTNET_SDK_VERSION=10.0.100` as its own default. When including this fragment, replace that default's version with `<major>.0.100`, where `<major>` is extracted from the stack token using the same extraction as the CI mapping's version-pinning table above (`dotnet10` → `10`) — e.g. a `dotnet8` stack writes `ARG DOTNET_SDK_VERSION=8.0.100`. **Monorepo tie-break**: when multiple projects map to the dotnet fragment with different major versions (e.g. one project on `dotnet8`, another on `dotnet10`), use the **highest** major version found across all matching projects. If no major version can be extracted from the token, leave the fragment's own default (`10.0.100`) unmodified — and add an inline comment immediately after the `ARG DOTNET_SDK_VERSION` line noting the version could not be auto-detected from the stack token and the fragment's default was used instead, e.g. `# .NET version could not be auto-detected from the stack token — using fragment default. See sandbox/README.md to pin manually.` The other fragments (node, playwright, go, python, rust, docker, azure) are included verbatim with their own `ARG` defaults unmodified — every fragment `ARG` (including `DOTNET_SDK_VERSION` and `BASE_VERSION`) remains overridable at build time via `--build-arg`, so an unmodified default is never a hard lock-in.

   > **Sync obligation**: `sandbox/fragments/*.dockerfile` is the source of truth for these blocks; the mapping table above mirrors their content and existence, not their byte contents (generation reads the fragment files directly — see step 5e). If a fragment is added, removed, or renamed, this table needs a matching manual update. Low risk in practice — both live in the same monorepo and are maintained together — but currently unenforced by tooling.

   > **Trust / security note**: `.cenci/Dockerfile` is committed to the repo, so it is reviewed like any other file in the PR that adds or changes it. It only runs `docker build` steps assembled from `sandbox/fragments/*.dockerfile` — no arbitrary runtime hooks execute during configure or during the build it produces.

9b. **Nested Docker (dind)**: "Does this repo need Docker inside the sandbox — Testcontainers, `docker build`/`docker run` in tests, or a Docker SDK client?"
   - Options: "Yes — enable nested Docker (`sandbox.dind`)", "No — skip"
   - Default: Yes when `dindDetected` is `true` (a Testcontainers/Docker-SDK trigger was found by Dependency Detection in `phases/mcp-lsp-ui-questions.md`), otherwise No
   - This question is independent of question 9 (Sandbox Dockerfile) — ask it regardless of how Q9 was answered, and record its answer separately (see the `sandbox.dind` schema note in `SKILL.md`).
   - If Yes: inform the user that nested Docker requires the host to have Docker (not Podman) with the `sysbox-runc` container runtime registered — `cenci doctor` reports this — and point at `sandbox/README.md#nested-docker-sysbox` for host install instructions per distro.
   - If Yes **and** question 9 generated a `.cenci/Dockerfile`: that Dockerfile must include `sandbox/fragments/docker.dockerfile` (see the mapping table's Docker rule under question 9). Because Q9b is asked after Q9, generate the Dockerfile only once this answer is known — or regenerate it here — so a `dind: true` repo never ends up with an image that has no `dockerd`. Tell the user to re-run `/cenci:configure` and then `cenci sandbox build` to pick the fragment up.

9c. **Azure CLI**: "Does this repo need the Azure CLI (`az`) inside the sandbox — Azure resources, Bicep/ARM templates, or Azure DevOps?"
   - Options: "Yes — install the Azure CLI (`sandbox.azure`)", "No — skip"
   - Default: pre-select from `existingConfig.sandbox.azure`; when the field is absent, default to No. There is **no** dependency scan behind this one: an Azure repo can be written in any language and Azure SDK packages appear in plenty of repos that never touch `az`, so a detected dependency would be a poor proxy for wanting a ~500MB CLI in the image. Ask plainly instead.
   - Like Q9b, this is independent of question 9 — ask it regardless of how Q9 was answered, and record its answer separately (see the `sandbox.azure` schema note in `SKILL.md`).
   - If Yes **and** question 9 generated a `.cenci/Dockerfile`: that Dockerfile must include `sandbox/fragments/azure.dockerfile` (see the mapping table's Azure rule under question 9). Q9c is asked after Q9, so generate the Dockerfile only once this answer is known — or regenerate it here.
   - If Yes **and** question 9 was answered No: unlike dind, the shared `cenci-sandbox:latest` monolith carries **no** `az`, so there is no fallback. Tell the user their sandbox will not have the Azure CLI until they also opt into a per-repo Dockerfile (Q9).
   - If Yes: also tell the user that `cenci open` will stage their host login (`~/.azure`'s `azureProfile.json`, `msal_token_cache.json`, `service_principal_entries.json`) read-only into the sandbox, seeded once into its home volume — so `az account show` works on first open, and a later `az login` inside the container is never overwritten by the host copy. If they have no host login, `az login` inside the sandbox works and persists in the home volume.
