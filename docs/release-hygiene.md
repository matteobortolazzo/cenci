# Release hygiene — repo-settings runbook (Part 2 of #150)

This is the maintainer runbook for **Part 2** of
[#150](https://github.com/matteobortolazzo/cenci/issues/150) — the repo-settings
half that can't be expressed as a file in a PR. Part 1 (this PR) ships the docs/config
half: `SECURITY.md`, `CONTRIBUTING.md`, issue/PR templates, and `.github/dependabot.yml`.
Run Part 2 manually, once, after Part 1 merges.

Commands below use `gh api` and assume you're authenticated as the repo owner. Replace
`{owner}/{repo}` with `matteobortolazzo/cenci` where a concrete example is useful;
elsewhere the placeholder form is used since these commands are otherwise generic.

## 1. Repo topics

```bash
gh api -X PUT repos/matteobortolazzo/cenci/topics \
  -H "Accept: application/vnd.github+json" \
  -f names[]=claude-code \
  -f names[]=codex \
  -f names[]=ai-agents \
  -f names[]=docker \
  -f names[]=podman \
  -f names[]=tmux \
  -f names[]=developer-tools \
  -f names[]=claude-plugins
```

## 2. Auto-merge and delete-branch-on-merge

`allow_auto_merge` is a hard prerequisite for #149's `deps-bump.yml` auto-merge flow —
sequence this before that flow goes live.

```bash
gh api -X PATCH repos/{owner}/{repo} \
  -f allow_auto_merge=true \
  -f delete_branch_on_merge=true
```

## 3. Private vulnerability reporting

Enables the reporting channel that `SECURITY.md` references as "preferred, once
enabled."

```bash
gh api -X PUT repos/{owner}/{repo}/private-vulnerability-reporting \
  -H "Accept: application/vnd.github+json"
```

After this succeeds, update `SECURITY.md`'s reporting section to name GitHub private
vulnerability reporting as the preferred channel (email remains a valid fallback).

## 4. Branch protection ruleset on `main`

Require PRs (0 required approvals — solo maintainer), block force pushes and branch
deletion. **This must bypass the GitHub Actions app**, or every automated
version-bump/release commit (a direct push to `main`) and the deps-bump auto-merge break
silently.

> **MUST VERIFY before applying**: the GitHub Actions app's integration id below (`15368`)
> is the id used at the time this runbook was written. Confirm it's still correct for
> this installation before creating the ruleset — a wrong id creates a ruleset that
> *looks* like it has a bypass but doesn't, and the first version-bump push after
> merging will fail with no obvious warning until you check the Actions log (step 5).
> Look it up with:
> ```bash
> gh api repos/{owner}/{repo}/installations 2>/dev/null || true
> # or, from an existing successful bot commit/run, cross-check the actor:
> gh api repos/{owner}/{repo}/actions/runs --jq '.workflow_runs[0].actor'
> ```

```bash
gh api -X POST repos/matteobortolazzo/cenci/rulesets \
  -H "Accept: application/vnd.github+json" \
  -f name="main" \
  -f target="branch" \
  -f enforcement="active" \
  -f 'conditions[ref_name][include][]=~DEFAULT_BRANCH' \
  -f 'conditions[ref_name][exclude][]=' \
  -f 'rules[][type]=pull_request' \
  -F 'rules[][parameters][required_approving_review_count]=0' \
  -f 'rules[][type]=deletion' \
  -f 'rules[][type]=non_fast_forward' \
  -f 'bypass_actors[][actor_id]=15368' \
  -f 'bypass_actors[][actor_type]=Integration' \
  -f 'bypass_actors[][bypass_mode]=always'
```

(Adjust the JSON shape as needed — `gh api` field flattening for nested arrays can be
finicky; a raw JSON body via `--input -` is a reasonable alternative if the flattened
form above is rejected. Verify the created ruleset's `bypass_actors` in the response
before moving on.)

> **CODEOWNERS interaction:** [`.github/CODEOWNERS`](../.github/CODEOWNERS) assigns
> `@matteobortolazzo` as owner of the least-privilege gate script
> (`check-workflow-permissions.sh`) and its test. That file is
> **informational-only** until this ruleset adds a `require_code_owner_review`
> rule — GitHub does not let a PR author approve their own PR, so enabling it
> today would block the sole collaborator's own merges to those two files.
> Add `require_code_owner_review` here only once a second collaborator exists.
> CODEOWNERS path drift (entries pointing at files that no longer exist) is
> caught on every push/PR by the `check-codeowners` job in `workflow-lint.yml`.

## 5. Mandatory post-ruleset verification

**Not optional.** A misconfigured bypass actor silently blocks every future automated
release commit — the version-bump workflows push directly to `main`, and if the
ruleset's PR-required rule applies to them, that push fails.

After creating the ruleset:

1. Merge (or wait for) the next PR that touches a plugin path (`flow/**`,
   `watch/**`, or `sandbox/**`).
2. Open the Actions tab and find the resulting `*-version-bump.yml` run (e.g.
   `flow — Version Bump`).
3. Confirm the run **succeeds end-to-end** — specifically the `git push` step that lands
   the `chore(release): <plugin>/vX.Y.Z` commit on `main`, and (for watch) the
   follow-on `watch-release.yml` dispatch.
4. If the push fails with a branch-protection error, the bypass actor is misconfigured —
   fix the ruleset's `bypass_actors` entry immediately, before any other plugin-touching
   PR merges.

Only consider Part 2 complete once this verification step has passed on a real merge.

## 6. Mandatory post-merge verification of reusable version-bump permissions (#193)

**Not optional.** #193 removed the elevating `permissions:` block from the reusable
`plugin-version-bump.yml` workflow (it now inherits whatever permissions the caller job
grants) and reverted `flow-version-bump.yml` / `sandbox-version-bump.yml` to
`contents: write` only, dropping the `actions: write` stopgap. This is only fully proven
on a real plugin-touching push — a misconfigured caller permission would silently break
the next release the same way it did before #189/#190/#192.

After this fix merges to `main`:

1. Wait for the next real push that touches a plugin path (`flow/**`,
   `watch/**`, or `sandbox/**`).
2. Open the Actions tab and find the resulting `*-version-bump.yml` run.
3. Confirm the run **succeeds end-to-end** with no `startup_failure` — specifically:
   - `flow` / `cenci-sandbox`: the run starts and the `git push` step lands the
     `chore(release): <plugin>/vX.Y.Z` commit on `main`, under a `contents: write`-only
     token.
   - `watch`: the run succeeds under `contents: write` + `actions: write`, tags the
     release, and dispatches `watch-release.yml` via `gh workflow run`.
4. Confirm the release-commit skip guard still works: on the push that follows the
   `chore(release): <plugin>/v...` commit, the version-bump workflow's HEAD check exits
   early (no recursive bump).
5. If any run fails validation before a job starts (`startup_failure`), a caller is
   missing a required permission — check it against `.github/scripts/check-workflow-permissions.sh`'s
   rules and fix the caller's `permissions:` block.

Only consider #193 complete once this verification step has passed on a real merge for
at least one plugin.

## 7. Mandatory post-merge verification: build provenance (#593)

**Not optional.** #593 added an `actions/attest-build-provenance` step to
`watch-release.yml` (gated by the new `id-token: write` / `attestations: write`
permissions), publishing a SLSA build provenance attestation covering all release
tarballs. This can only be proven end-to-end on a real release — the workflow needs
a genuine OIDC token minted on a tag/dispatch run, which isn't available inside a PR.

After the next `watch` release lands:

1. Download one of the release tarballs, e.g.
   `gh release download watch/v<ver> -p 'cenci_<ver>_linux_amd64.tar.gz'`.
2. Run `gh attestation verify cenci_<ver>_linux_amd64.tar.gz --owner matteobortolazzo`
   (or `--repo matteobortolazzo/cenci` for a tighter, repo-scoped check).
3. Confirm the command reports a verified provenance attestation tying the tarball to
   the `watch-release.yml` run and commit that built it.
4. If verification fails, check that the `Attest build provenance for release
   tarballs` step actually ran and succeeded in that release's workflow run, and that
   the workflow's `permissions:` block still grants `id-token: write` and
   `attestations: write`.

Only consider #593 complete once this verification step has passed on a real release.

## 8. Mandatory post-merge verification: installer release trust (#626, #736)

**Not optional, but no longer the only guard.** #626 hardened `install.sh`'s
release-pin path to require `cosign verify-blob` against a `watch-release.yml`-signed
`install.sh.bundle` before executing any release-pinned installer bytes. AC8 of #626
asked for this to be proven end-to-end on a real release — the workflow needs a
genuine OIDC token minted on a tag/dispatch run, which isn't available inside a PR —
but that manual check evidently never ran: every automated release is actually
published via `workflow_dispatch --ref main` (from `plugin-version-bump.yml`), whose
Fulcio certificate binds `refs/heads/main`, not the release tag. The
`--certificate-identity-regexp` pinned by #626 only ever matched the tag-push identity
(`refs/tags/watch/v<version>`), so `install.sh`'s own verification — and this runbook's
manual check, had anyone run it against an automated release — would have failed
closed on every automated `watch` release since #626 landed. #736 fixed the regexp to
accept both canonical identities (tag push and dispatch-from-main) and, since a manual
runbook step is easy to forget or skip, also added an automated
"Verify the published installer signature" step to `watch-release.yml` itself: it
re-downloads the just-published `install.sh`/`install.sh.bundle` and runs the same
`cosign verify-blob` check in CI, failing the release job if it doesn't verify. That
step is now the primary guard against this class of regression; the manual steps
below are a backstop for validating the released artifact by hand, not the only check.

After the next `watch` release lands:

1. Confirm the release workflow run's own "Verify the published installer signature"
   step passed — that's the automated version of everything below, and it runs on
   every release, not just when someone remembers this runbook.
2. Download the published installer asset and its bundle, e.g.:
   ```bash
   curl -fsSL -o install.sh https://github.com/matteobortolazzo/cenci/releases/download/watch/v<ver>/install.sh
   curl -fsSL -o install.sh.bundle https://github.com/matteobortolazzo/cenci/releases/download/watch/v<ver>/install.sh.bundle
   ```
3. Verify with `cosign` against the pinned identity, the same command
   `verify_installer_asset()` in `install.sh` runs internally:
   ```bash
   cosign verify-blob --bundle install.sh.bundle \
     --certificate-identity-regexp '^https://github\.com/matteobortolazzo/cenci/\.github/workflows/watch-release\.yml@refs/(heads/main|tags/watch/v[0-9]+\.[0-9]+\.[0-9]+)$' \
     --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
     install.sh
   ```
4. Confirm `cosign` reports a verified signature tying `install.sh` to the
   `watch-release.yml` run and whichever trigger (tag push or dispatch-from-main)
   built it.
5. Run the verified script (`bash install.sh`) and confirm it executes normally —
   proving sign → publish → download → verify → execute end to end.
6. If verification fails, check that the "sign install.sh" step actually ran and
   uploaded both `install.sh` and `install.sh.bundle` as release assets in that
   release's workflow run, and that the workflow's `permissions:` block still grants
   `id-token: write`.

Only consider #626's AC8 complete once this verification step has passed on a real
release.
