# Codex babysit-attention procedure

Read `project-core`, `codex-runtime`, `shell-rules` and `subagent-safety`. The persistent babysit supervisor
opened this session because automated progress on one PR is paused: a CI or merge repair retry cap was reached,
or progress is ambiguous. The launch reason is a label only; it never decides what this procedure offers. This
procedure never enters `/plan` and never writes a checkpoint of its own.

## Summary

The launch argument is `<pr-number> <reason>`. Take only the PR number, validated against `^[1-9][0-9]*$`; an
invalid number stops the run with no write. Derive `owner/repo` from `git remote get-url origin`: strip one
trailing `.git`, accept only the https form `https://github.com/<owner>/<repo>` or the ssh forms
`git@github.com:<owner>/<repo>` and `ssh://git@github.com/<owner>/<repo>`, reduce it to `<owner>/<repo>` and
validate that against `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`. A URL of any other shape stops the run with no write, as do a
failed `git remote get-url` and a value that fails the regex. Build the summary from read-only calls:
`gh pr view <pr> --repo <owner>/<repo> --json headRefName,headRefOid,state,mergeable` and
`gh pr checks <pr> --repo <owner>/<repo> --json bucket,name,state`. A failed read stops the run with no write and
the report "could not read PR state": a `gh pr view` with a non-zero exit, and either command with empty output or
output that is not valid JSON, is a read failure and is never reported as "no failing check". `gh pr checks` exits 8
while checks are pending and exits non-zero while checks fail, yet still writes valid JSON, so for it a non-zero
exit is a read failure only when the JSON does not decode. Name every check in the `fail` bucket and
every `pending` one; never derive CI status from check conclusions. When the reason is a merge repair cap, the
summary names the merge-repair reason instead of failing checks.

The summary carries the PR, the head SHA, the failing checks (or the merge-repair reason) and the attempt count.
Read the attempt count by running `cenci babysit status <pr>` once; it prints one JSON line and works on the
host and inside a sandbox. Use `ciFixAttempts` when the reason is a CI retry cap and `conflictFixAttempts`
when it is a merge repair retry cap. When the command exits non-zero, or its output does not decode as JSON,
the summary says "retry cap reached (exact count not readable from here)" and never guesses a number.

## Question

Ask exactly one blocking question through the client's available user-input mechanism, with exactly two
options: "leave paused for manual repair" and "stop babysitting". Then stop. This procedure never detects whether
the session is interactive: a headless `codex exec` run prints the summary and the question and exits without
choosing, mutating, pushing or stopping. Only a later user turn that names one option acts on it.

"leave paused for manual repair" changes nothing: report that the PR stays paused and the supervisor keeps
polling, and that merge conflicts are repaired by a human in a normal session. "stop babysitting" continues at
`## Stop choice`.

## Stop choice

Run `cenci babysit stop <pr>` once; inside a sandbox it is forwarded to the host supervisor. Exit 0 with
`Stopped babysitting PR #<pr> on the host.` means the supervisor was stopped: report that. A non-zero exit means
the supervisor was not stopped, whether the host refused (`not stopped: <reason>`) or did not answer in time
(`stop status unknown`): say "not stopped" plainly, print `cenci babysit stop <pr>` as the exact command to
run on the host, and never report success. Do not retry the command and do not kill any process by other means.

## Never

Never mutate GitHub, push, restart the supervisor or run a repair on this window's own initiative, before or after
the choice: no comment, no label, no commit and no `cenci babysit` call other than the stop above and `cenci babysit status <pr>`.
