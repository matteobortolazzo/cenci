# Host-managed login lifecycle for Claude Code and Codex (#1520)

Date: 2026-11-24

An evidence-tiered investigation of whether a login the user manages on the host can be
used by agents inside a sandbox, without the copy-once-then-diverge behavior documented in
[`sandbox/README.md`](../README.md#first-run-setup). It is a docs-and-fixture record: no
production credential handling changed.

**Closing this investigation does not mean shared login is done.** The verdict is in
[Recommendation](#recommendation): seamless shared login is currently unsupported.

| Field | Value |
|---|---|
| Date | 2026-11-24 |
| Claude Code version examined | 2.1.331, from `claude --version` run against the agent-CLI volume mounted at `/opt/cenci-agent` (Observed) |
| Codex version examined | Unresolved. No Codex agent-CLI volume was mounted in the examining session, so `codex --version` was not run and no version is guessed |
| Install method | The `cenci-agent-cli-<agent>` volume bootstrap: the launcher mounts the volume read-only at `/opt/cenci-agent` (`assembleVolumeMounts` in `watch/internal/sandbox/launcher/launch.go`). The mounted Claude volume's staging `package.json` pins `@anthropic-ai/claude-code` 2.1.331 |
| Container runtimes | Docker and Podman, from the launcher's runtime detection (Inference). `docker --version` and `podman --version` could not be run in the examining session, so runtime versions are Unresolved |
| Official sources | [Claude Code authentication](https://code.claude.com/docs/en/authentication), [Claude Code iam](https://code.claude.com/docs/en/iam) (read; same content as the authentication page), [Claude Code settings](https://code.claude.com/docs/en/settings), [Claude Code environment variables](https://code.claude.com/docs/en/env-vars), [Codex authentication](https://developers.openai.com/codex/auth) (308-redirects to `https://learn.chatgpt.com/docs/auth`), [Codex config reference](https://learn.chatgpt.com/codex/config-file/config-reference), [Codex CI/CD auth](https://learn.chatgpt.com/codex/auth/ci-cd-auth). Accessed 2026-11-24 |
| How it was produced | Read-only: `--version`, repository code reading and official documentation pages. No real-account runs, no token capture, no credential or keyring read |

Evidence limits that shape every section below:

- The Claude pages were read in full (authentication, settings, iam) or in part
  (`settings-reference`: the `apiKeyHelper` entry was not read; `env-vars`: first part
  verbatim). The Claude devcontainer, sandboxing and troubleshooting pages were not read.
- The Codex pages are official, but the text available for this record was a tool summary,
  not verified page wording. Every Codex statement below is tagged `[summary]` and must be
  re-checked against the live page before anyone relies on it. Codex statements are never
  quoted.
- No tokens, refresh tokens, account emails or authorization codes appear in this report.
  Every credential value in the harness is a visibly fake placeholder such as
  `fake-refresh-0001`.

## Observed

Code reading and `--version` output from the examining session. File and symbol are given;
line numbers were checked against this branch.

- **Claude Code CLI version.** `claude --version` printed `2.1.331 (Claude Code)`. The
  volume also holds the previous version `2.1.330` under `/opt/cenci-agent/versions/`, with
  `current` pointing at `2.1.331`.
- **Codex CLI version.** Not observed (no Codex volume was mounted in this session).
- **Agent CLI volume is read-only at workload.**
  `assembleVolumeMounts` mounts `AgentCLIVolumeName(agent)` at `/opt/cenci-agent:ro`
  (`launch.go` ~line 1137). It holds no credentials.
- **Home volume is writable and holds the live login.** The same function mounts the
  per-scope home volume at `/home/dev` read-write (`launch.go` ~line 1136). Seeded
  credentials land there.
- **Claude credential staging.** If the host's `~/.claude/.credentials.json` is a regular
  file, it is bind-mounted read-only at `/tmp/host-claude-creds/.credentials.json`
  (`assembleVolumeMounts`, `launch.go` ~lines 1188-1194; the comment there cites #259:
  rotating refresh tokens fork per copy). If the host has no such file, nothing is staged.
- **Reseed flag.** `ReseedCreds` (`launch.go` line 37) becomes `CENCI_SANDBOX_RESEED_CREDS=1`
  in the container environment (`launch.go` ~lines 1249-1253).
- **Codex credential staging.** `validateCredentials` bind-mounts the host's
  `~/.codex/auth.json` read-only at `/tmp/host-codex-creds/auth.json` when it is a regular
  file, and returns a hard error when there is neither that file nor `OPENAI_API_KEY`
  (`launch.go` ~lines 1273-1289). A host whose Codex login lives only in a credential
  store therefore stages nothing for Codex.
- **Seed-once.** `seed_credential` in `sandbox/lib/seed-auth.sh` copies the staged file only
  when the destination does not exist or `CENCI_SANDBOX_RESEED_CREDS` is `1`, then sets
  mode 600. The header comment of that file asserts rotation and independent chains; that
  assertion is an unverified claim (see [Unresolved](#unresolved)).
- **No host keyring in the container.** The README's GitHub CLI section states the
  container has no access to a host keyring and the image has no secret-service provider
  (`sandbox/README.md`, "GitHub CLI auth"). The launcher stages files, not keyrings.
- **Per-exec secret forwarding.** `forwardedEnvVarNames` in
  `watch/internal/sandbox/launcher/secretenv.go` lists `CONTEXT7_API_KEY`,
  `ANTHROPIC_API_KEY` (opencode only) and `OPENAI_API_KEY` (codex and opencode). Claude
  gets neither API key by default. `CLAUDE_CODE_OAUTH_TOKEN` is not in the built-in list.
  `resolveSecretEnvSet` merges the user's `sandboxEnv.forward` names, forwarded to every
  agent; `forwardNames` returns the names for one agent. Only names are stored, never
  values; the value is read from the host environment when the bare `-e NAME` token is
  emitted.
- **Harness result.** `bash sandbox/tests/auth-lifecycle.test.sh` passes (77 passed, 0
  failed at the time of writing). With the fake authority's single-use check disabled
  locally, the concurrent-refresh, reuse and stale-seed cases fail, so those assertions can
  fail. More local falsifications, on scratch copies and never committed, each made the
  suite fail: a no-op crash hook failed the token-spent assertions, removing the
  armed-probe proof failed the inherited-switch check, not planting a scan control
  failed its positive-control assertion, a no-op initial seed in the reseed case failed
  its stale-copy assertion, and an empty or unmatched home-variable scan input failed the
  home-variable assertions. This is the harness behaving as designed, not provider
  behavior.

## Upstream guarantees

This section holds only statements from the official pages listed in the header whose
wording was read verbatim, quoted, or explicitly marked as paraphrase. That is the Claude
block below. Codex statements are not here: their
wording was never verified, so they sit under
[Inference](#codex-statements-with-unverified-wording-re-check-live-page-slice-r1).

### Claude Code (verbatim, accessed 2026-11-24)

- Storage on Linux: "On Linux, credentials are stored in `~/.claude/.credentials.json` with
  file mode `0600`."
- Storage on macOS: "On macOS, credentials are stored in the encrypted macOS Keychain."
  When the Keychain rejects the write (for example a locked Keychain over SSH), the login
  falls back to `~/.claude/.credentials.json` (paraphrase).
- `CLAUDE_CONFIG_DIR` moves `.credentials.json` under that directory and keys the Keychain
  entry to it (paraphrase).
- `claude setup-token` is described as able to "generate a one-year OAuth token"; it "does
  not save the token anywhere" and the token "can only make model requests".
- `CLAUDE_CODE_OAUTH_TOKEN`: "Use this for CI pipelines and scripts where browser login
  isn't available." Bare mode does not read it (paraphrase).
- `apiKeyHelper`: "configure the `apiKeyHelper` setting to run a shell script that returns
  an API key." The re-run interval is controlled by `CLAUDE_CODE_API_KEY_HELPER_TTL_MS`,
  "Interval in milliseconds at which credentials should be refreshed (when using
  apiKeyHelper)". The `settings-reference` entry for `apiKeyHelper` was not read.
- Credential precedence as documented (paraphrase), highest first: cloud-provider variables,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, `apiKeyHelper`, `CLAUDE_CODE_OAUTH_TOKEN`,
  profile or federation credentials, then the `/login` subscription OAuth login.
- `ANTHROPIC_API_KEY`, when set, is used "instead of your Claude Pro, Max, ... subscription
  even if you are logged in."
- Lifetime: "Using Claude Code doesn't extend the login." Expiry surfaces as "Login reached
  its time limit" or "Login expired", with the warning "Your login expires in 3 days · run
  /login to renew". For claude.ai logins the docs say "your earlier sign-in expired and
  Claude Code can't refresh it".
- Account separation: "Each directory has its own settings, session history, and claude.ai
  login or API key." `/logout` ends a login (paraphrase).
- Claude's own sandboxed Bash "can read the rest of the filesystem, including credential
  files" by default; a `sandbox.credentials` setting can hide or mask them (paraphrase). This concerns
  Claude's host sandbox feature, which cenci keeps disabled inside the container
  (`sandbox/AGENTS.md`), so it is context, not a container guarantee.
- A dev container that "keeps your credentials in a volume" is mentioned in passing; the
  devcontainer page that would explain it was not read.

### Codex

Not listed here: no Codex wording was verified verbatim. See
[Inference](#codex-statements-with-unverified-wording-re-check-live-page-slice-r1), which
also records the `developers.openai.com/codex/auth` 308 redirect to
`learn.chatgpt.com/docs/auth`.

## Inference

Reasoning from the sections above. None of this is a provider statement.

### Codex statements with unverified wording (re-check live page; slice R1)

The Codex pages are official, but the text available for this record was a tool summary,
not verified page wording. Each statement is tagged `[summary]`, is not a provider
guarantee and is never quoted. Re-check every one against the live page (slice R1) before
anyone relies on it.

`https://developers.openai.com/codex/auth` returned HTTP 308 to
`https://learn.chatgpt.com/docs/auth` on 2026-11-24.

- `[summary]` Login commands: `codex login` (browser), `codex login --device-auth` (device
  code), and API-key login by piping the key to `codex login --with-api-key`.
- `[summary]` Credentials are cached either in plaintext `~/.codex/auth.json` or in the OS
  credential store. `CODEX_HOME` (default `~/.codex`) relocates the directory.
- `[summary]` The credential-store setting `cli_auth_credentials_store` takes `file`,
  `keyring`, `auto` or `ephemeral`; the config reference was not seen to state a default.
  `keyring` fails when no keyring is available; `ephemeral` keeps credentials in memory for
  one process.
- `[summary]` Headless guidance: copy `~/.codex/auth.json` to the headless machine (which
  may not apply when the OS credential store is used) and prefer device-code login first. A
  "Copy into a Docker container" section exists; its exact commands were not seen.
- `[summary]` The CLI and the extension share the same cached login. The CI/CD page says
  not to share one `auth.json` across concurrent jobs or multiple machines, and to use one
  `auth.json` per runner or per serialized workflow stream.
- `[summary]` Refresh runs automatically before expiry and after a 401; a session goes
  stale after about eight days in the open-source client (the summarizer's paraphrase).
- `[summary]` `codex logout` ends a login. `forced_chatgpt_workspace_id` and
  `forced_login_method` restrict the account or method.
- `[summary]` `auth.json` is to be treated like a password because it contains access
  tokens. The CI/CD page names an API key as the right way to authenticate automation.

### Reasoning

- **Same-user readability.** The container agent and any repository command it runs execute
  as the same `dev` user. A file mode of `0600` (guaranteed for Claude's file, set by
  `seed_credential` for the volume copy) does not separate two processes of one user, and
  environment variables are inherited by child processes. So any credential a native CLI
  reads from a file or from its environment is readable by any same-user process in the
  container. `watch/AGENTS.md` describes the launcher's own orphan scan reading in-container
  process environments for launch IDs, which is the same mechanism.
- **Staleness follows from rotation, if rotation holds.** If a provider rotates refresh
  tokens on refresh, a host refresh makes the volume copy stale and a volume refresh makes
  the host copy stale. That is the premise the seed-once design (#259) records. Whether
  either provider rotates, and what it does on reuse, is Unresolved.
- **Concurrent use of one lineage is unsafe if rotation plus reuse detection holds.** Two
  holders of one copied lineage would then race and one would be refused. The harness shows
  the consequence under that assumption only.
- **Credential-store modes may stage nothing.** On a macOS host Claude stores in the
  Keychain, and a Codex host may use `keyring` or `auto` (`[summary]`). The launcher stages a file only
  when it exists as a regular file (Observed), so those hosts may stage no login. Portability
  note only; the supported-platform contract is unchanged.
- **Container runtime detection.** `ContainerRuntime` prefers `podman` then `docker`;
  `ContainerRuntimePreferDocker` (dind) prefers `docker` then `podman`; `AvailableRuntimes`
  returns `docker` then `podman` (`watch/internal/sandbox/sandbox.go`). Credential staging
  uses plain read-only file bind mounts, so it does not depend on which runtime is chosen.
- **A single-file bind mount may not survive atomic replacement.** Replacing a bind-mounted
  single file by temp-file-and-rename generally fails across the mount boundary. Whether
  either CLI writes its credential file that way is Unresolved, so a "narrowly shared file"
  design may fail on first refresh.
- **Env-var credentials avoid the refresh race by construction, not by evidence.** A token
  passed through `CLAUDE_CODE_OAUTH_TOKEN` or `OPENAI_API_KEY` is not a copied file lineage.
  Whether those credentials involve any refresh lineage, and how they are revoked, is
  Unresolved.
- **#259.** The ticket text describes #259 (closed) as a regression from overwriting
  refreshed credentials on every start. That issue was not re-read for this report.

## Unresolved

Each item names what would settle it. Until then it is not a fact and no design may depend
on it.

1. **Refresh-token rotation and reuse detection, Claude Code.** Not stated in the pages
   read. Settles with: provider documentation, or an authorized manual run (matrix rows 1-2).
2. **Refresh-token rotation and reuse detection, Codex.** Not stated in the summary-derived
   text. Settles with: the live page read verbatim, or an authorized manual run.
3. **Concurrent use of one login by several sessions, Claude Code.** Not stated. Codex's
   advice against sharing one `auth.json` is `[summary]` and does not explain the failure
   mode.
4. **Claude `apiKeyHelper` re-run timing and failure behavior.** The `settings-reference`
   entry was not read; only the TTL environment variable and the failure messages were.
5. **Claude `CLAUDE_CODE_OAUTH_REFRESH_TOKEN`.** Appeared only in a tool summary. Not
   verified to exist; no design may rely on it.
6. **Claude and Codex device-style login inside a container.** The Claude pages read do not
   describe a device-code flow; Codex's `--device-auth` is `[summary]`. Whether either works
   in a container without a host browser was not exercised.
7. **Revocation of a `claude setup-token` token and of an API key,** and how that interacts
   with account switching. Not stated in the pages read.
8. **Codex Docker section and Podman behavior.** The Docker copy section's commands were not
   seen. Nothing was found about Podman for either CLI.
9. **Claude devcontainer, sandboxing and troubleshooting pages.** Not read.
10. **Codex CLI version and container runtime versions.** Not observed in the examining
    session.
11. **Whether either CLI writes its credential file in place or by rename,** and what a
    crash during refresh leaves behind. The harness models a truncated file; the real
    behavior is unobserved.
12. **The `sandbox/README.md` claim that a volume's copied credentials "become an
    independent login" that "stays logged in indefinitely".** The README alone holds that
    claim (`SECURITY.md` on main never made it). It depends on items 1-3 and stays as
    written, with a pointer here, because this report establishes no replacement.
    `SECURITY.md` gains only a new remaining-access note and a link to this report.
13. **macOS Keychain behavior for any matrix row.** Portability only; not exercised.

## Answers

Each answer cites a section or link. "Unresolved n" refers to the numbered list above.

### Q1. Which official mechanisms let each CLI use a host-managed login from a container?

- **Claude Code:**
  - File storage: `~/.claude/.credentials.json` on Linux, mode `0600`
    ([authentication](https://code.claude.com/docs/en/authentication); Upstream guarantees).
    This is the mechanism cenci uses today (Observed, `assembleVolumeMounts`).
  - Token for non-browser use: `claude setup-token` then `CLAUDE_CODE_OAUTH_TOKEN`; the token
    is not saved anywhere by the command and is limited to model requests.
  - Helper: `apiKeyHelper` runs a script that returns an API key (re-run timing is
    Unresolved 4).
  - Keychain: macOS only, with a file fallback; the container has no host keyring (Observed).
  - Device login and remote-execution facilities: not found in the pages read
    (Unresolved 6, 9).
- **Codex** (`[summary]`, [auth](https://learn.chatgpt.com/docs/auth)):
  - `codex login` (browser), `codex login --device-auth`, and API-key login.
  - File or credential-store caching via `cli_auth_credentials_store`
    (`file | keyring | auto | ephemeral`).
  - Copying `~/.codex/auth.json` to a headless machine; a Docker copy section exists
    (Unresolved 8).

### Q2. Can the host plus two sandbox sessions safely use one account and one cached lineage at once? Who refreshes, persists and revokes? What happens after logout, re-login, expiry, account switch, host restart, or a failed or partial refresh?

- **Claude Code:** Safe concurrent use of one lineage is not stated (Unresolved 1, 3).
  Rotation and reuse detection are not stated. The CLI process holding the login refreshes
  it ("Claude Code can't refresh it" implies the CLI attempts refresh); that the same process
  persists the file is Inference. Expiry, logout and per-directory account separation are
  documented (Upstream guarantees); server-side revocation is Unresolved 7. A host restart
  has no documented effect on file storage. A partial refresh is unobserved (Unresolved 11);
  the harness models a truncated file as `needs-relogin`, as a MODEL only
  ([Harness model and limits](#harness-model-and-limits)).
- **Codex** (`[summary]`): The CI/CD page advises one `auth.json` per runner or per serialized
  stream and not sharing it across concurrent jobs or machines. That is the only statement
  about concurrency and it argues against two live holders of one lineage. Refresh is
  automatic; `codex logout` ends a login; reuse detection is Unresolved 2.
- **Both:** under cenci today, the host and each home volume hold separate copies after the
  first refresh by either side (Inference, premise of #259). That cenci copies once means
  concurrent lineage sharing is avoided by design, not proven safe.

### Q3. Can credentials stay out of reach of arbitrary container commands, or does the native CLI need a readable token?

**Any credential the native CLI reads is readable by any same-user container process.** The
native CLI needs a readable credential in the container, as a file in `/home/dev` or as an
environment variable, and repository code running as `dev` can read the same thing. No
mechanism found hides it from same-user processes while the CLI still uses it.

- **Claude Code:** the volume file is mode `0600`, which does not separate same-user
  processes (Inference). `CLAUDE_CODE_OAUTH_TOKEN` or `apiKeyHelper` output would be read
  from the environment or a helper process the same user can inspect (Inference). The only
  documented hiding feature, `sandbox.credentials`, belongs to Claude's host sandbox, which
  is disabled inside the container (Upstream guarantees; `sandbox/AGENTS.md`).
- **Codex** (`[summary]`): `auth.json` is plaintext and to be treated like a password. The
  `ephemeral` store keeps credentials in memory for one process; that does not hide them
  from the same user's other processes (Inference) and requires a login per process.
- **What the boundary does guarantee today (Observed):** the host file is staged read-only,
  so container code cannot modify the host file; it can still read it. No host keyring or
  credential socket is exposed, and the credential reaches only the one scope's home volume
  (plus the staging mount below). Forwarded secret values never appear in the launcher's
  argv or stored config; inside the container they are ordinary readable environment
  variables, readable by same-user processes (for example through `/proc`). This is not a
  confidentiality guarantee.
- **Everything a container process can read (Observed, `launch.go` ~lines 1193 and 1280):**
  - the staged host files, `/tmp/host-claude-creds/.credentials.json` and
    `/tmp/host-codex-creds/auth.json`, bind-mounted read-only into every workload container
    and readable by any `dev` process for the container's whole lifetime, not only at seed
    time;
  - the home-volume copy under `/home/dev`;
  - the forwarded environment values (`sandboxEnv.forward` names and the built-in API-key
    names).
- **What reading the staged host file allows (Inference, not Observed):** read-only blocks
  modification, not reading or redeeming the token. If a provider rotates refresh tokens and
  detects reuse (the model this report's harness assumes, Unresolved 1-2), a process that
  reads the staged host file and redeems its refresh token could fork or revoke the host's
  login. Whether either provider behaves this way is not established.

### Q4. What works on the Linux hosts and runtimes supported today (Docker, Podman)? Record macOS keychain limits as portability findings only.

- **Claude Code:** file staging works on Linux with either runtime because it is a plain
  read-only bind mount (Observed; Inference on runtime independence). Runtime versions are
  Unresolved 10. On macOS the login is in the Keychain unless the file fallback applied, so
  the launcher may stage nothing (Inference). Portability finding only; the supported
  platform statements in `sandbox/README.md` are unchanged.
- **Codex** (`[summary]`): file staging works when `~/.codex/auth.json` exists. With
  `keyring` or `auto`, that file may not exist, and the launcher then requires
  `OPENAI_API_KEY` (Observed, `validateCredentials`). Podman is not mentioned in the pages
  seen (Unresolved 8).
- Every matrix row for either runtime is `unverified`.

### Q5. What is the smallest supported design that cuts repeated logins without letting repository code change the host account or configuration, or read arbitrary keychain entries?

- **Claude Code:** no design meeting the full bar was found. The nearest documented
  mechanism is a user-supplied `CLAUDE_CODE_OAUTH_TOKEN` through `sandboxEnv.forward`, which
  is a separate credential rather than the host login, with revocation, account semantics and
  subscription limits unresolved (Unresolved 7), and it is still readable in the container
  (Q3).
- **Codex** (`[summary]`): the nearest documented mechanism is API-key login, a different
  credential type, or an independent `--device-auth` login per sandbox. Neither is a host-
  managed login. No broker or helper was found.
- **Both:** the current seed-once plus `--reseed-creds` behavior stays the supported path.
  See [Recommendation](#recommendation).

## Comparison

Cells tag their basis: (O) Observed, (U) Upstream guarantee, (I) Inference, (?) Unresolved.
Codex cells are never (U): their wording is unverified, so they are tagged (I, `[summary]`)
or carry `[summary]` on a statement that rests on that summary.

| Option | Security boundary | Refresh ownership | Multi-session races | Revocation / account switching | Offline recovery | Portability |
|---|---|---|---|---|---|---|
| Seed-once / reseed (current) | Container code cannot modify the host file but can still read it: the staged host file is readable for the container's lifetime, as are the volume copy and forwarded env values (O); a reader could redeem the host's refresh token, which could fork or revoke the host login if rotation and reuse detection hold (I) | Each side refreshes its own copy after the first refresh (I, premise #259) | Avoided by copying once; two sandboxes of one scope share a volume copy, effects unverified (?) | Reseed copies the host login over the volume (O); a revoked host login is copied dead (I); no account switching beyond re-login | Works offline until expiry; a stale copy needs `--reseed-creds` or re-login (O, I) | Linux file staging with Docker or Podman (O, I); macOS Keychain host may stage nothing (I) |
| Narrowly shared credential files (one file mounted writable into several sandboxes) | Container can overwrite the host file; still readable by same-user code (I) | Whichever process refreshes first; the file is the single holder (I) | Unsafe if rotation holds; Codex docs advise against sharing one `auth.json` across concurrent jobs or machines `[summary]`; Claude not stated (?) | Host logout affects every sandbox at once (I); account switch needs `CLAUDE_CONFIG_DIR` (U) or `CODEX_HOME` (I, `[summary]`) separation | A crash mid-write could leave a truncated file (modeled only); in-place vs rename write unobserved (?) | Single-file bind mounts may break on rename (I); Keychain hosts have no file (I); Codex `keyring` hosts likewise (I, `[summary]`) |
| Independent per-sandbox login (`/login` in Claude, `codex login --device-auth` (I, `[summary]`)) | Credential lives only in that sandbox's home volume; same-user readable (I) | The sandbox's own CLI (I) | None between sandboxes if lineages are truly independent; unverified for either provider (?) | Revoke or switch per sandbox; user repeats login per volume (I) | Needs the provider reachable to log in; no host dependency (I) | Same on Linux, macOS and either runtime if the flow works headless (?) |
| Claude `claude setup-token` + `CLAUDE_CODE_OAUTH_TOKEN` | Env var readable by same-user code (I); token limited to model requests (U) | Not stated whether any refresh lineage exists; one-year token (U, ?) | Not stated (?) | Revocation not stated (?); precedence places it below `apiKeyHelper` and API keys (U) | One-year lifetime (U); recovery means generating a new token | Documented for CI and scripts (U); not read by bare mode (U) |
| Claude `apiKeyHelper` | A helper must run where the CLI runs, so inside the container it needs a readable source (I); a host-run helper is a host-exec socket, excluded | The helper's script; TTL via `CLAUDE_CODE_API_KEY_HELPER_TTL_MS` (U) | Helper-defined (?) | Helper-defined; an API key replaces subscription login (U) | Depends on the script (?) | Settings-level, same on all platforms (?); reference entry unread (?) |
| Codex API-key login (`--with-api-key`, `OPENAI_API_KEY`) | Key readable by same-user code (I); the launcher already forwards it per exec (O) | No refresh lineage; user rotates the key (I) | No lineage to race (I) | Revoke or rotate the key; separate billing credential, not a ChatGPT login `[summary]` | Works offline until the key is revoked (I) | Same everywhere (I); CI page names it for automation `[summary]` |
| Codex credential store `keyring` / `ephemeral` | `keyring`: no host keyring in the container (O); `ephemeral`: in memory per process `[summary]` | CLI (I, `[summary]`) | `ephemeral` has no shared state, so a login per process `[summary]` | Per process or per keyring (I, `[summary]`) | `keyring` fails when unavailable `[summary]` | The image lacks a secret-service provider (O); Codex `keyring` relying on one (I, `[summary]`) |
| Host credential broker or helper for a container | `none found` | `none found` | `none found` | `none found` | `none found` | `none found` |

`none found` for a broker: sources checked were the Claude authentication, settings, iam and
env-vars pages, the Codex authentication page, config reference and CI/CD page, and the
launcher code. The `apiKeyHelper` settings entry and the Claude devcontainer and sandboxing
pages were not read, so absence is not proof.

## Harness model and limits

[`sandbox/tests/auth-lifecycle.test.sh`](../tests/auth-lifecycle.test.sh) is a **MODEL of
assumed provider semantics, not observed provider behavior.** A fake authority issues
single-use rotating refresh tokens (`fake-refresh-0001` to `fake-refresh-0002` and on) and
treats a presented, already-used token as reuse that revokes the whole family. Whether
Claude Code or Codex do this is Unresolved 1-2. The suite shows only what cenci's seeding
code does if the model holds.

What is real: `seed_credential` from `sandbox/lib/seed-auth.sh`, run in a scrubbed `bash`.
Everything else (authority, holders, reader) is an in-test fixture. The suite never calls a
real CLI, network endpoint or keyring.

| Case | Modeled outcome asserted |
|---|---|
| Concurrent refresh by two holders of one copied lineage | Exactly one holder refreshes; the other gets `invalid_grant`; the family ends revoked |
| Reuse of a rotated refresh token | `invalid_grant`, then the successor token also fails (modeled family revocation) |
| Seed-once leaves a stale volume copy after a host refresh | Real `seed_credential` leaves the volume copy untouched; the stale copy gets `invalid_grant` |
| `CENCI_SANDBOX_RESEED_CREDS=1` recovery | Real `seed_credential` replaces the volume copy, mode `600`; the next refresh succeeds |
| Token spent, nothing persisted | A truncated, empty or missing credential reads as `needs-relogin`; authority state is unchanged. A test-only hook models "token spent, nothing persisted": the holder exits cooperatively after the authority redeemed and before the persist, leaving the old credential unchanged and a temp file behind; the authority has rotated, so the old token is spent but unsaved and presenting it gets `invalid_grant`. It does not prove atomic-persist or torn-write behavior |
| Sentinel secret never appears | A sentinel set in the suite's own environment never reaches fixtures, outputs or files; the scan is first shown to find a planted sentinel |
| Decoy home untouched | Decoy credential files under the fixture home are unchanged; the decoy-marker scan is first shown to find a planted marker; the suite never expands the caller's home directory |
| Temp root removed on exit | A child probe run of the suite exits 0, prints its own `mktemp -d` root and leaves through the normal exit trap; that root no longer exists afterwards. A child that inherits only the probe switch, without the parent's armed marker, runs the real cases instead of exiting 0 with none. A run with zero assertions never counts as a pass |

Limits:

- Single-use rotation with family revocation is an assumption. A provider with a grace
  window, no rotation or per-session lineages would make the first three rows differ.
- The credential file shape is `{"fakeRefreshToken":"..."}`, not either CLI's real format.
- It models neither timing nor network failure, token expiry, keyring storage, nor the real
  CLIs' write pattern (in place or rename).
- It cannot tell whether two sandboxes sharing a home volume interfere.
- A pass is not evidence that shared login would be safe. A modeled failure is not evidence
  that it would be unsafe.

## Manual verification matrix

No real-account run was made. Every row stays `unverified` until a human records a result
without token material.

| Case | What it checks | Result |
|---|---|---|
| Host + two concurrent sandboxes | Host and two sandboxes of one account stay logged in over a working session | unverified |
| Refresh race | Two holders of one copied lineage refresh at the same moment | unverified |
| Expired login | What each CLI shows and how recovery works after expiry | unverified |
| Revoked login | Behavior after all sessions are revoked server-side | unverified |
| Host re-authentication | Effect on sandbox copies after the host logs in again | unverified |
| Different accounts | Host account differs from a sandbox's account; no cross-contamination | unverified |
| Crash during refresh | CLI killed mid-refresh; state of the credential file afterwards | unverified |
| Unavailable keyring | Keyring-only host login (Claude on macOS, Codex `keyring`); what the launcher stages | unverified |
| Disconnected host | Sandbox behavior while the host is off or offline | unverified |

## Recommendation

Seamless shared login is currently unsupported

**Status: approved by the maintainer at the implement pipeline's Recommendation review gate (plan step 5), 2026-11-24.** No design is named, so the eight design subsections
(user opt-in, repository scope, credential exposure, host-side authority, redacted
diagnostics, revocation, migration, rollback) are not written. The decision rule requires
every element of a design to rest on a documented mechanism, all eight subsections to be
fillable, and no whole-home writable mount, host-exec socket, custom OAuth refresh service
or undocumented token endpoint.

Reasons:

1. **The central facts are undocumented or unverified.** Rotation, reuse detection and
   concurrent-lineage safety are not established for either CLI (Unresolved 1-3). A design
   that shares or re-seeds a lineage would rest on an unstated assumption. The only
   concurrency statement found, for Codex, advises against sharing one `auth.json`, and
   its wording is unverified (`[summary]`, slice R1), so it is not a guarantee.
2. **No documented host-side authority exists.** No broker or helper that lets a host
   process own refresh for a container was found (Comparison, last row). Creating one
   means a custom refresh service, a host-exec socket or a writable host credential mount,
   all excluded.
3. **The documented token mechanisms are different credentials, not the host login.**
   Claude's `setup-token` token and either CLI's API key are separate credentials. Their
   revocation, account semantics and subscription limits are Unresolved 7, so the
   revocation, host-side authority and migration subsections cannot be filled from
   documentation. There is no equivalent subscription-token path for Codex.
4. **A readable credential always remains in the container.** Whatever design is chosen,
   any credential the native CLI reads is readable by any same-user container process (Q3).
   No design can promise a boundary against repository code.
5. **Narrowly shared files may not work mechanically.** A single-file bind mount may not
   survive the CLI's atomic replacement, and the write pattern is unobserved (Unresolved 11).

The current behavior stays: seed-once, `--reseed-creds` and `cenci sandbox reseed-creds`
recovery. The `sandbox/README.md` claim about independent logins is unverified and points
here. `SECURITY.md` never made that claim; it gains a new remaining-access note plus a link
to this report.

## Implementation slices

No implementation slice is listed. Seamless shared login is unsupported, so this
investigation creates no follow-up implementation issues. The slices below are research
only and open nothing until a human decides to file them. Each could change the verdict;
none implements shared login.

| Slice | Depends on | Bounded acceptance criteria | Test plan |
|---|---|---|---|
| R1. Re-read the Codex authentication, config-reference and CI/CD pages verbatim | None | Every Codex `[summary]` statement here is confirmed, corrected or deleted with the page's wording; the Docker section and the default of `cli_auth_credentials_store` are recorded | Documentation read only; diff of this report; no credential involved |
| R2. Read the unread Claude pages (`apiKeyHelper` settings entry, devcontainer, sandboxing, troubleshooting) | None | Unresolved 4, 6, 7 and 9 are answered with a link or stay open with the reason | Documentation read only |
| R3. Human-run, explicitly authorized manual matrix on a disposable account | R1, R2 | Each of the nine rows is `passed`, `failed` or `unverified`, with no token, email or auth code in the record | Procedure in the matrix; results entered by the human; recorded outcome labels only |

## Related

- [`sandbox/README.md`](../README.md#login-lifecycle-and-limits): user-facing seed-once, reseed and limits.
- [`SECURITY.md`](../../SECURITY.md#credentials): the credential threat model.
- [`sandbox/docs/test-harness.md`](./test-harness.md): the environment-scrub and sentinel rules the harness follows.
