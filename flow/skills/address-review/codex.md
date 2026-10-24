# Codex address-review procedure

Before fixing in-scope feedback, apply the security, silent-failure, and diagnosis
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate a
bounded second opinion to `domain-specialist` using the cited sources, feedback,
and any failed attempts. Validate its evidence; preserve actionability filtering,
approved scope, existing stop rules, and the prohibition on extra review cycles.

Read `project-core`, `codex-runtime`, and `pr-comment-filter`. Fetch review threads and evaluate
actionability against `pr-comment-filter`'s Include/Exclude lists — in scope: unresolved threads
and resolved threads with new activity since resolution; excluded: bot comments, cenci's own
review replies (banner-prefix match), and resolved threads with no new activity — then
checkpoint the proposed fixes. Obtain approval in `/plan`. In the second normal-mode invocation,
first run the guard canary preflight, `bash "${PLUGIN_ROOT}/codex/guard-canary.sh" plan`, as
`codex-runtime` describes, and on any refusal stop with nothing changed, reporting the helper's text and any
`LEFTOVER:` path. Only after `CANARY_STATUS=active`,
apply approved fixes, test, commit, push without force, reply, and re-request review. Every
reply — inline thread reply or general PR comment — opens with the blockquoted cenci
attribution banner and carries no `<!-- cenci-<kind> -->` marker, matching the Claude Code
procedure's "Posting Replies" template. Never resolve disputed feedback silently.
