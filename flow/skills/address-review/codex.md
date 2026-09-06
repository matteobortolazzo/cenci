# Codex address-review procedure

Read `project-core`, `codex-runtime`, and `pr-comment-filter`. Fetch review threads and evaluate
actionability against `pr-comment-filter`'s Include/Exclude lists — in scope: unresolved threads
and resolved threads with new activity since resolution; excluded: bot comments, cenci's own
review replies (banner-prefix match), and resolved threads with no new activity — then
checkpoint the proposed fixes. Obtain approval in `/plan`; in a second normal-mode invocation
apply approved fixes, test, commit, push without force, reply, and re-request review. Every
reply — inline thread reply or general PR comment — opens with the blockquoted cenci
attribution banner and carries no `<!-- cenci-<kind> -->` marker, matching the Claude Code
procedure's "Posting Replies" template. Never resolve disputed feedback silently.
