# Release hygiene — repo-settings runbook (Part 2 of #150)

This is the maintainer runbook for **Part 2** of
[#150](https://github.com/matteobortolazzo/agent-stack/issues/150) — the repo-settings
half that can't be expressed as a file in a PR. Part 1 (this PR) ships the docs/config
half: `SECURITY.md`, `CONTRIBUTING.md`, issue/PR templates, and `.github/dependabot.yml`.
Run Part 2 manually, once, after Part 1 merges.

Commands below use `gh api` and assume you're authenticated as the repo owner. Replace
`{owner}/{repo}` with `matteobortolazzo/agent-stack` where a concrete example is useful;
elsewhere the placeholder form is used since these commands are otherwise generic.

## 1. Repo topics

```bash
gh api -X PUT repos/matteobortolazzo/agent-stack/topics \
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
gh api -X POST repos/matteobortolazzo/agent-stack/rulesets \
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

## 5. Mandatory post-ruleset verification

**Not optional.** A misconfigured bypass actor silently blocks every future automated
release commit — the version-bump workflows push directly to `main`, and if the
ruleset's PR-required rule applies to them, that push fails.

After creating the ruleset:

1. Merge (or wait for) the next PR that touches a plugin path (`agentflow/**`,
   `agentwatch/**`, or `dev-sandbox/**`).
2. Open the Actions tab and find the resulting `*-version-bump.yml` run (e.g.
   `agentflow — Version Bump`).
3. Confirm the run **succeeds end-to-end** — specifically the `git push` step that lands
   the `chore(release): <plugin>/vX.Y.Z` commit on `main`, and (for agentwatch) the
   follow-on `agentwatch-release.yml` dispatch.
4. If the push fails with a branch-protection error, the bypass actor is misconfigured —
   fix the ruleset's `bypass_actors` entry immediately, before any other plugin-touching
   PR merges.

Only consider Part 2 complete once this verification step has passed on a real merge.
