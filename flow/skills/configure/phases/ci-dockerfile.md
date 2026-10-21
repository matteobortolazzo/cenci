# CI/CD and Dockerfile Generation

Read this file on every run, after `phases/guidance-settings.md` (see `SKILL.md` `### Step Map`). It holds steps 5d and 5e, which each decide internally whether they run.

5d. **Generate CI/CD pipeline** (from question 8, only if user selected Yes — on a delta run where question 8 is carried, `ci.yml` is not regenerated and this step is skipped):

   **GitHub Actions** — write `.github/workflows/ci.yml`:

   ```yaml
   name: CI
   on:
     push:
       branches: [main]
     pull_request:
       branches: [main]
   jobs:
     ci:
       runs-on: ubuntu-latest
       steps:
         - uses: actions/checkout@v4
         # Setup + cache for detected stack
         # Install deps → Lint → Build → Test
         # Upload coverage artifact
   ```

   Compose the `steps` array based on the detected stack from question 1:

   **Node-based stacks** (Angular, React, Next.js, Vue):
   - `actions/setup-node@v4` with `node-version` from `engines.node` or `"20"`, and `cache` set to the detected package manager (npm/yarn/pnpm)
   - Install: `npm ci` (or `yarn install --frozen-lockfile` / `pnpm install --frozen-lockfile`)
   - Lint step, Build step, Test step from the stack-to-CI mapping table (question 8 in `phases/runtime-ci-sandbox-questions.md`)
   - `actions/upload-artifact@v4` for coverage output

   **.NET stacks**:
   - `actions/setup-dotnet@v4` with `dotnet-version` extracted from stack token
   - `dotnet restore`
   - Lint, Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Go**:
   - `actions/setup-go@v5` with `go-version-file: 'go.mod'` (enables built-in caching)
   - Lint: `golangci/golangci-lint-action@v6`
   - Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Python**:
   - `actions/setup-python@v5` with `python-version` from `pyproject.toml` or `"3.12"`
   - `pip install -e ".[dev]"` (or `pip install -r requirements.txt`)
   - Lint, Test from the mapping table (no build step)
   - `actions/upload-artifact@v4` for coverage output

   **Rust**:
   - `dtolnay/rust-toolchain@stable` (or version from `rust-toolchain.toml`)
   - `Swatinem/rust-cache@v2`
   - Lint, Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Monorepo strategies**:
   - **Same stack family** (all projects share same stack type): Use a matrix job with `working-directory` per project
   - **Mixed stacks** (projects have different stack types): Generate separate named jobs per project (e.g., `ci-api`, `ci-web-client`)
   - **Path-based trigger filters**: Add `paths` filters under `push`/`pull_request` scoped to each project's directory, so CI only runs for changed projects

   After writing the file, create the parent directory if needed (`mkdir -p .github/workflows` for GitHub Actions).

5e. **Generate `.cenci/Dockerfile`** (from question 9, only if the effective `sandbox.enabled` is `true` — the user's Yes to question 9 this run, or on a delta run the carried existing `sandbox.enabled: true`):

   **`BASE_VERSION` is always `latest`** — there is no resolution step. `buildBase` (`watch/internal/sandbox/launcher/engine.go:337-349`) only ever tags the base image `cenci-sandbox-base:<12-hex content hash>` and `cenci-sandbox-base:latest`; no semver tag exists in any environment, so the ARG default can only ever be the `latest` alias. `cenci sandbox build` always overrides it at build time via `--build-arg BASE_VERSION=...` (`engine.go:372,392`), so the default is never load-bearing for that path — it only matters for a bare manual `docker build`, where `latest` is exactly the recipe `sandbox/README.md` documents. An empty default must never be written instead: Docker's `InvalidDefaultArgInFrom` lint check flags any `ARG` used in a `FROM` whose default resolves to an empty or invalid image reference, and it evaluates the file statically regardless of a build-time override — so the literal `latest` (never empty, never unresolved) is what step 5e always writes.

   **Generated file format** — always emit the ARG/FROM pair below, **never** a literal `FROM cenci-sandbox-base:<version>`. `cenci sandbox build` always passes `--build-arg BASE_VERSION=...` at build time; a literal `FROM` would silently drift from what's actually built:

   ```dockerfile
   # cenci:managed-begin
   ARG BASE_VERSION=latest
   FROM cenci-sandbox-base:${BASE_VERSION}

   SHELL ["/bin/bash", "-o", "pipefail", "-c"]

   # cenci:fragment-begin <fragment 1 name>
   <selected fragment 1 content, from sandbox/fragments/*.dockerfile>
   # cenci:fragment-end <fragment 1 name>

   # cenci:fragment-begin <fragment 2 name>
   <selected fragment 2 content>
   # cenci:fragment-end <fragment 2 name>
   ...
   # cenci:managed-end
   ```

   **Per-fragment markers**: wrap each fragment's content in a `# cenci:fragment-begin <name>` / `# cenci:fragment-end <name>` marker pair, placed immediately before and after that fragment's own content within the managed block (see the example above). `<name>` is the fragment file's basename without `.dockerfile` (e.g. `sandbox/fragments/docker.dockerfile` → `docker`). This lets `watch/internal/sandbox/launcher`'s fragment-drift detector (#1048) identify exactly which installed fragment a block of content came from; an already-generated block with no per-fragment markers still gets detected, via that detector's legacy banner-line fallback.

   **Fragment concatenation order** (when multiple fragments apply, e.g. a monorepo union): **dotnet → node → playwright → go → python → rust → docker → azure**, regardless of the order projects were discovered in. Node is mandatory; the remaining fragments are stack-selected (docker and azure are config-selected — see the Docker and Azure rules of the Stack-to-fragment mapping table under question 9 in `phases/runtime-ci-sandbox-questions.md`). Concatenate the selected `sandbox/fragments/*.dockerfile` file contents in that fixed order, each wrapped in its own per-fragment marker pair, applying the **.NET version substitution** from that mapping table to the dotnet fragment only — every other fragment is included verbatim. Deduplicate — each fragment appears at most once even when multiple monorepo projects map to the same fragment.

   **Merge-safe regeneration**: the whole block above (from `# cenci:managed-begin` through `# cenci:managed-end` inclusive) is the managed block.
   - **File doesn't exist**: create `.cenci/` (`mkdir -p .cenci`) and write the managed block as the full file content.
   - **Effective sandbox flags, per ID**: the fragment selection uses the effective `sandbox.enabled`, `sandbox.dind` and `sandbox.azure` values, decided per question ID. An ID in `delta.carried` reads the existing config value; an ID in `delta.ask` this run (its key was absent and it was never offered, e.g. a config with `sandbox.enabled: true` that predates questions 9b/9c asks `sandbox.dind` and `sandbox.azure`) uses the answer just collected, so a just-answered Yes is consumed by this same generation. A Dockerfile is generated or refreshed whenever the effective `sandbox.enabled` is `true`, whether it was carried or just answered. The refresh itself never asks a question.
   - **File exists with both markers present**: replace only the text from `# cenci:managed-begin` through `# cenci:managed-end` (inclusive) with the freshly generated block. Preserve everything before the begin marker and everything after the end marker exactly as-is — this is where a team can hand-append their own `RUN` steps across re-runs. On a delta run with valid `# cenci:managed-begin` / `# cenci:managed-end` markers, refresh the managed block silently — no Q9/Q9b/Q9c prompt — and leave every byte outside the markers unchanged. Report the block diff with `git -C <worktree-path> diff -- .cenci/Dockerfile`. Only an ID that is in `delta.ask` (key absent and never offered) puts a Q9/Q9b/Q9c question to the user on a delta run; the markers never cause one.
   - **File exists with no markers** (e.g. a hand-authored legacy `.cenci/Dockerfile`): do not silently overwrite it. On a delta run, a `.cenci/Dockerfile` with no markers or malformed markers is left untouched and reported — no prompt. On an `--all` or `full` run, a `.cenci/Dockerfile` with no markers or malformed markers gets the Overwrite/Skip/Show prompt. It reuses the conflict-check UX already used for CI/CD generation (question 8 / step 5d) — same three options, same behavior:
     "Found existing `.cenci/Dockerfile` without cenci's managed markers. What would you like to do?"
     Options: "Overwrite — wrap it in managed markers and replace with the generated block", "Skip — keep the existing file, still record `sandbox` in config.json", "Show existing — display the current file contents"
     - If Skip: still record `sandbox` in config.json, don't write the file.
     - If Show existing: read and display the file, then re-ask Overwrite/Skip.
   - **File exists with malformed markers** (exactly one of `# cenci:managed-begin` / `# cenci:managed-end` present, markers out of order, or duplicate marker pairs): do **not** attempt a partial text replace — a malformed marker pair cannot be trusted to safely bound the managed block, and a replacement span is never guessed. On a delta run it is left untouched and reported exactly like the "no markers" case above (no prompt); on an `--all` or `full` run it takes the same Overwrite/Skip/Show prompt — same prompt text, same three options, same behavior.

   **Monorepo**: fragments are the mandatory Node runtime fragment plus the deduplicated union described in the Stack-to-fragment mapping table under question 9 in `phases/runtime-ci-sandbox-questions.md`, concatenated in the dotnet → node → playwright → go → python → rust → docker → azure order above — one `.cenci/Dockerfile` for the whole repo, not one per project.

   **Committed, not ignored**: `.cenci/Dockerfile` is committed to the repo. Do **not** add `.cenci/` or `.cenci/Dockerfile` to `.gitignore` — the whole point is a team-shared, reviewed Dockerfile that the launcher's per-repo image selection (see `sandbox/README.md`) builds identically for every teammate.
