---
name: setup
description: "Claude Code-only: install the cenci-sand launcher and build the sandbox container image. Codex users receive the launcher through the cenci installer."
compatibility: Requires Claude Code AskUserQuestion and CLAUDE_PLUGIN_ROOT.
argument-hint: [--build-only | --link-only]
user-invocable: true
disable-model-invocation: true
allowed-tools: Bash, Read, AskUserQuestion
---

## Task

Set up the `sandbox` plugin on this host so the user can run Claude Code inside an
isolated Docker/Podman container. Two steps:

1. **Symlink the `cenci-sand` launcher onto PATH** — the launcher ships with the
   plugin at `${CLAUDE_PLUGIN_ROOT}/cenci-sand`.
2. **Build the container image** (`agent-sandbox:latest`) via `cenci-sand --build`.

### Parse `$ARGUMENTS`

- `--link-only` — do step 1 only (symlink), skip the image build.
- `--build-only` — do step 2 only (build), skip the symlink.
- empty — do both.

### Preconditions

Run these checks first. If any fails, report it clearly and stop — do not continue.

- **Container runtime**: `command -v podman || command -v docker`. If neither is found,
  tell the user to install Docker or Podman and stop.
- **Launcher present**: verify `${CLAUDE_PLUGIN_ROOT}/cenci-sand` exists and is a file.

### Step 1 — Symlink the launcher

Skip this step when `--build-only` was passed.

1. Choose a PATH target directory. Prefer `~/.local/bin` (create it with `mkdir -p` if
   missing). Check it is on `$PATH`:
   ```bash
   case ":$PATH:" in *":$HOME/.local/bin:"*) echo on-path ;; *) echo not-on-path ;; esac
   ```
   If `~/.local/bin` is not on `$PATH`, warn the user they will need to add it (e.g.
   `export PATH="$HOME/.local/bin:$PATH"` in their shell profile) for `cenci-sand` to
   be found.

2. Create the symlink, resolving the launcher to an absolute path with `realpath` — do
   **not** use a `$(cd … && pwd)` substitution: combining `cd` with a write command in one
   compound trips Claude Code's built-in `cd-compound-write` guard and forces a manual
   prompt every run:
   ```bash
   ln -sf "$(realpath "${CLAUDE_PLUGIN_ROOT}")/cenci-sand" "$HOME/.local/bin/cenci-sand"
   ```
   - If a `cenci-sand` already exists at the target and is **not** a symlink, do not
     overwrite it — report it and ask the user (`AskUserQuestion`) whether to replace it.
   - If it is a symlink (even a stale one), `ln -sf` refreshes it; that is fine.

3. Confirm: `ls -l "$HOME/.local/bin/cenci-sand"` and `command -v cenci-sand`.

### Step 2 — Build the image

Skip this step when `--link-only` was passed.

Run the build through the launcher so the same runtime-detection logic is used:
```bash
cenci-sand --build
```
If `cenci-sand` is not yet on `$PATH` in this shell (freshly symlinked), invoke it by
absolute path instead: `"$HOME/.local/bin/cenci-sand" --build`.

The build can take several minutes on first run (it pulls the base image and installs
the SDKs). Report the outcome. On failure, surface the runtime's error output and stop.

### Done

Summarize what was done and point the user at usage:
- `cenci-sand` — launch Claude Code in the sandbox (full permissions inside the container)
- `cenci-sand xt` (or `cenci-sand --agent codex`) — launch Codex in the sandbox instead
- `cenci-sand ch`/`cs`/`co`/`cf` — launch Claude with the haiku/sonnet/opus/fable model
- `cenci-sand --shell` — open a shell for manual setup / troubleshooting
- `cenci-sand --build` — rebuild the image after changing the Dockerfile

See `${CLAUDE_PLUGIN_ROOT}/README.md` for auth, MCP, cenci, and Docker-in-Docker details.
