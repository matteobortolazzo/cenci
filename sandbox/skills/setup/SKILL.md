---
name: setup
description: "Claude Code-only: verify the cenci launcher and build the sandbox container image. Codex users receive the launcher through the cenci installer."
compatibility: Requires Claude Code AskUserQuestion and CLAUDE_PLUGIN_ROOT.
argument-hint: [--build-only | --check-only]
user-invocable: true
disable-model-invocation: true
allowed-tools: Bash, Read, AskUserQuestion
---

## Task

Set up the `sandbox` plugin on this host so the user can run Claude Code inside an
isolated Docker/Podman container. The launcher is the `cenci` binary itself
(`cenci open` / its `cn` alias, `cenci sandbox <verb>`) — no plugin symlink is
installed by this skill. Two steps:

1. **Verify the launcher** — `cenci` resolves on PATH.
2. **Build the container image** (`cenci-sandbox:latest`) via `cenci sandbox build`.

### Parse `$ARGUMENTS`

- `--check-only` — do step 1 only (verify), skip the image build.
- `--build-only` — do step 2 only (build), skip the verification.
- empty — do both.

### Preconditions

Run these checks first. If any fails, report it clearly and stop — do not continue.

- **Container runtime**: `command -v podman || command -v docker`. If neither is found,
  tell the user to install Docker or Podman and stop.

### Step 1 — Verify the launcher

Skip this step when `--build-only` was passed.

1. Check the binary and its alias:
   ```bash
   command -v cenci
   command -v cn
   ```
   - If `cenci` is missing, the plugin's first-session bootstrap has not run yet or
     `~/.local/bin` is not on `$PATH`. Check with:
     ```bash
     case ":$PATH:" in *":$HOME/.local/bin:"*) echo on-path ;; *) echo not-on-path ;; esac
     ```
     If it is not on `$PATH`, warn the user to add it (e.g.
     `export PATH="$HOME/.local/bin:$PATH"` in their shell profile). If it is on
     `$PATH` but `cenci` still does not resolve, tell the user to re-run the cenci
     installer (`curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash`
     — resolves to the latest release tag by default; `CENCI_REF=main` for
     bleeding-edge main)
     and stop.
   - A missing `cn` alone is cosmetic — mention re-running the installer to create it,
     but continue.

2. Confirm the launcher answers: `cenci version`.

### Step 2 — Build the image

Skip this step when `--check-only` was passed.

Run the build through the launcher so the same runtime-detection and asset-resolution
logic is used:
```bash
cenci sandbox build
```

The build can take several minutes on first run (it pulls the base image and installs
the SDKs). Report the outcome. On failure, surface the runtime's error output and stop.

### Done

Summarize what was done and point the user at usage:
- `cn` (alias for `cenci open`) — launch Claude Code in the sandbox (full permissions inside the container)
- `cn xt` (or `cenci open --agent codex`) — launch Codex in the sandbox instead
- `cn ch`/`cs`/`co`/`cf` — launch Claude with the haiku/sonnet/opus/fable model
- `cenci open --shell` — open a shell for manual setup / troubleshooting
- `cenci sandbox build` — rebuild the image after changing the Dockerfile

See `${CLAUDE_PLUGIN_ROOT}/README.md` for auth, MCP, cenci, and Docker-in-Docker details.
