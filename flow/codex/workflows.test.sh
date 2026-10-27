#!/usr/bin/env bash
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failures=0
fail(){ echo "FAIL: $1" >&2; failures=$((failures+1)); }

# The workflows whose Claude skill dispatches to a native Codex procedure. The dispatch
# line must name `<workflow>/codex.md` exactly, so a companion renamed or copied from a
# sibling cannot pass on the loose `Client dispatch.*Codex` shape alone.
for workflow in configure refine implement review address-review refactor sync maintain ci-repair babysit-attention; do
  skill="${ROOT}/skills/${workflow}/SKILL.md"
  procedure="${ROOT}/skills/${workflow}/codex.md"
  grep -q 'Client dispatch.*Codex' "$skill" || fail "$workflow dispatcher"
  grep -qF "In Codex, read \`codex-runtime\` and \`${workflow}/codex.md\`" "$skill" || fail "$workflow dispatcher does not name ${workflow}/codex.md"
  test -s "$procedure" || fail "$workflow Codex procedure"
done

# The two supervisor-dispatched skills keep the dispatch line as the first body line,
# directly after the frontmatter, so Codex stops before any Claude-only instruction.
for workflow in ci-repair babysit-attention; do
  first_body="$(awk 'c==2 && NF {print; exit} /^---$/ {c++}' "${ROOT}/skills/${workflow}/SKILL.md")"
  case "$first_body" in
    "> **Client dispatch**: In Codex, read \`codex-runtime\` and \`${workflow}/codex.md\`"*) ;;
    *) fail "$workflow dispatch line is not the first line after the frontmatter" ;;
  esac
done

# --- section-scoped contract helpers (see docs/shell-scripting-gotchas.md) ---
# normalize <file> -> the whole file, newlines folded so a phrase wrapped across source lines still matches
normalize(){ tr '\n' ' ' < "$1" | tr -s ' '; }
# section <file> <exact heading line> -> that section's body, up to the next heading of any level
section(){ awk -v h="$2" '$0==h {on=1; next} on && /^#+ / {exit} on {print}' "$1" | tr '\n' ' ' | tr -s ' '; }
# require_in <label> <text> <phrase>: fails on an empty phrase, an empty text or a missing phrase
require_in(){
  [[ -n "$3" ]] || { fail "$1: empty required phrase"; return; }
  [[ -n "$2" ]] || { fail "$1: scope is empty, so '$3' cannot be asserted"; return; }
  [[ "$2" == *"$3"* ]] || fail "$1: missing '$3'"
}
# forbid_in <label> <text> <phrase>: fails when the text is empty (vacuous) or contains the phrase
forbid_in(){
  [[ -n "$3" ]] || { fail "$1: empty forbidden phrase"; return; }
  [[ -n "$2" ]] || { fail "$1: scope is empty, so absence of '$3' proves nothing"; return; }
  [[ "$2" != *"$3"* ]] || fail "$1: must not contain '$3'"
}

# --- ci-repair native procedure: AC2 and the four AC5 behavior cases ---
repair="${ROOT}/skills/ci-repair/codex.md"
repair_all="$(normalize "$repair")"
for phrase in '`project-core`' '`codex-runtime`' '`shell-rules`' '`subagent-safety`' '`testing`' \
  'docs/model-escalation.md' 'domain-specialist' 'two failed attempts' \
  '`/plan`' '`cenci pipeline`' 'lifecycle label' '`gh pr create`' \
  'git push origin HEAD:refs/heads/<branch>' 'never a force flag' 'run-gate.sh' 'last-match' \
  'gate unset' '.cenci/checkpoints/ci-repair-<pr>.json' 'never `git add -A`' \
  'gh pr checks' '--json bucket,name,state' 'never inside the PR worktree'; do
  require_in "ci-repair/codex.md" "$repair_all" "$phrase"
done

# Root-agent writes: the opening section (under the H1, before the first `##`) states that only the
# root commits, pushes and writes the checkpoint, and that workers never push.
repair_intro="$(section "$repair" '# Codex ci-repair procedure')"
for phrase in 'The root agent alone commits, pushes and writes the checkpoint' 'workers never push'; do
  require_in "ci-repair intro" "$repair_intro" "$phrase"
done

# AC2 repair steps: reproduce locally first, then the smallest test-backed fix, then the gate.
repair_steps="$(section "$repair" '## Repair')"
for phrase in 'Reproduce the cause locally by running the failing command in the worktree' 'record `reproduced`' \
  'A cause that does not reproduce is classified, never guessed at' \
  'Make the smallest test-backed fix: a failing test first, then the code' \
  'Run the project'"'"'s local gate'; do
  require_in "ci-repair Repair" "$repair_steps" "$phrase"
done

gate="$(section "$repair" '### Gate failure')"
for phrase in 'no push' 'checkpoint.mjs block' 'gate-red:<head-sha>' 'GATE_LOG=' 'failing check names' 'returns control to the supervisor'; do
  require_in "ci-repair Gate failure" "$gate" "$phrase"
done

cause="$(section "$repair" '### Repair failure, ambiguous, infra, flaky or external cause')"
for phrase in 'no push' 'never marked repaired' 'checkpoint.mjs block' 'unrepaired:<class>:<head-sha>' \
  '`ambiguous`' '`infra`' '`flaky`' '`external`' '`push-rejected`' \
  'cross-repository' 'dirty' 'returns control to the supervisor'; do
  require_in "ci-repair Repair failure" "$cause" "$phrase"
done

pushed="$(section "$repair" '### Successful push')"
for phrase in 'without force' 'git push origin HEAD:refs/heads/<branch>' 'git ls-remote' 'checkpoint.mjs complete' 'pushed:<sha>' 'never `git add -A`'; do
  require_in "ci-repair Successful push" "$pushed" "$phrase"
done

restart="$(section "$repair" '### Restart')"
for phrase in '.cenci/checkpoints/ci-repair-<pr>.json' 'checkpoint.mjs get' 'last completed gate' 'no-op' 'recorded pushed SHA' 'current head' 'never duplicates a commit'; do
  require_in "ci-repair Restart" "$restart" "$phrase"
done
# Restart no-op honesty: the head still has failing checks, so the report names them and says no repair was
# attempted; and a recorded push already on the remote is a no-op even while `gh pr view` shows an older head.
for phrase in 'the head still has failing checks' 'names them' 'no repair was attempted' \
  'git ls-remote origin refs/heads/<branch>' 'git merge-base --is-ancestor' 'still shows an older head' 'no re-diagnosis'; do
  require_in "ci-repair Restart" "$restart" "$phrase"
done

# Inputs: owner/repo derivation from the origin URL and a failed `gh` read (finding: a failed read must
# stop as a read failure, never be reported as "no failing check").
inputs="$(section "$repair" '## Inputs')"
for phrase in 'strip one trailing `.git`' 'https://github.com/<owner>/<repo>' 'git@github.com:<owner>/<repo>' \
  'any other shape stops' \
  'could not read PR state' 'non-zero exit' 'empty output' 'not valid JSON' 'never reported as "no failing check"' \
  'exits 8 while checks are pending'; do
  require_in "ci-repair Inputs" "$inputs" "$phrase"
done

# Gate parse (finding: a nested `GATE_STATUS=green` printed by the repo's own fixtures, plus a kill or timeout,
# must never read as green) and the unset rule (skipped only when no gateCommand exists anywhere).
for phrase in 'exit status 0 AND the last non-empty line of the output is exactly `GATE_STATUS=green`' \
  'line-anchored' 'printed earlier by the repo'"'"'s own gate fixtures' 'killed or timed-out gate' \
  'any disagreement between the exit status and the status line' 'a non-zero exit is a gate failure' \
  'only when `.cenci/config.json` has no `gateCommand` at the top level and in no `projects[]` entry' \
  'jq -e' 'pass the project slug' 'omitted or mismatched slug' 'missing config' 'is a gate failure, not a skip' \
  'states why the gate was unset'; do
  require_in "ci-repair Repair gate" "$repair_steps" "$phrase"
done

# Checkpoint: literal helper invocations, positional order, init-before-block, missing vs unparseable,
# "checkpoint not recorded", a failed `complete` after a push, and the main-checkout root.
checkpoint="$(section "$repair" '## Checkpoint')"
for phrase in \
  'checkpoint.mjs" get <ckpt>' \
  'checkpoint.mjs" init <ckpt> ci-repair <pr> planned' \
  'checkpoint.mjs" advance <ckpt> ci-repair <pr> <phase>' \
  'checkpoint.mjs" block <ckpt> ci-repair <pr> <phase>' \
  'checkpoint.mjs" complete <ckpt> ci-repair <pr> <phase>' \
  'the phase is always the fifth argument' \
  'right after the input checks pass and before any `block`' \
  'advance`, `block` and `complete` throw when the file is missing' \
  'missing file is created with `init`' 'unparseable' 'never `init` over it' \
  'checkpoint not recorded' 'echoed `phase`' \
  'a failed `complete` after a successful push is reported plainly' 'never as a clean success' \
  'git rev-parse --path-format=absolute --git-common-dir' 'may itself be a linked worktree' 'never inside the PR worktree'; do
  require_in "ci-repair Checkpoint" "$checkpoint" "$phrase"
done

# The fork/external case: init, then block, before any worktree or write (no contradiction with the
# "block needs an existing file" rule).
worktree="$(section "$repair" '## Worktree')"
for phrase in 'initialize the checkpoint, then run `block`' 'before any worktree or write'; do
  require_in "ci-repair Worktree" "$worktree" "$phrase"
done

# --- babysit-attention (Claude and Codex): AC3 and AC4 ---
attention_skill="${ROOT}/skills/babysit-attention/SKILL.md"
attention_codex="${ROOT}/skills/babysit-attention/codex.md"
attention_skill_all="$(normalize "$attention_skill")"
attention_codex_all="$(normalize "$attention_codex")"

for pair in "SKILL.md:${attention_skill_all}" "codex.md:${attention_codex_all}"; do
  name="${pair%%:*}"
  text="${pair#*:}"
  require_in "babysit-attention/${name}" "$text" '"leave paused for manual repair"'
  require_in "babysit-attention/${name}" "$text" '"stop babysitting"'
  require_in "babysit-attention/${name}" "$text" 'cenci babysit stop <pr>'
  require_in "babysit-attention/${name}" "$text" 'not stopped'
  require_in "babysit-attention/${name}" "$text" 'non-zero exit'
  require_in "babysit-attention/${name}" "$text" 'never report success'
  require_in "babysit-attention/${name}" "$text" 'retry cap reached (exact count not readable from here)'
  require_in "babysit-attention/${name}" "$text" 'XDG_STATE_HOME'
  # The removed retry option: a non-empty file is required so the absence is not vacuous.
  forbid_in "babysit-attention/${name}" "$text" 'fresh budget'
done
require_in "babysit-attention/SKILL.md" "$attention_skill_all" '`AskUserQuestion`'

attention_summary="$(section "$attention_codex" '## Summary')"
for phrase in 'headRefOid' 'The summary carries the PR, the head SHA, the failing checks (or the merge-repair reason) and the attempt count' \
  'Name every check in the `fail` bucket' 'the summary names the merge-repair reason instead of failing checks'; do
  require_in "babysit-attention/codex.md Summary" "$attention_summary" "$phrase"
done
# owner/repo derivation and a failed `gh` read: a read failure stops, it is never summarized as "no failing check".
for phrase in 'strip one trailing `.git`' 'https://github.com/<owner>/<repo>' 'git@github.com:<owner>/<repo>' 'any other shape stops' \
  'could not read PR state' 'non-zero exit' 'empty output' 'not valid JSON' 'never reported as "no failing check"' \
  'exits 8 while checks are pending'; do
  require_in "babysit-attention/codex.md Summary" "$attention_summary" "$phrase"
done

attention_stop="$(section "$attention_codex" '## Stop choice')"
for phrase in 'cenci babysit stop <pr>' 'sandbox' 'not stopped' 'run on the host' 'never report success'; do
  require_in "babysit-attention/codex.md Stop choice" "$attention_stop" "$phrase"
done

attention_question="$(section "$attention_codex" '## Question')"
for phrase in 'exactly one' '"leave paused for manual repair"' '"stop babysitting"' 'headless `codex exec` run' \
  'prints the summary and the question and exits without choosing, mutating, pushing or stopping' \
  'Only a later user turn that names one option acts on it'; do
  require_in "babysit-attention/codex.md Question" "$attention_question" "$phrase"
done
forbid_in "babysit-attention/codex.md Question" "$attention_question" 'retry'

attention_never="$(section "$attention_codex" '## Never')"
for phrase in 'Never mutate GitHub, push, restart the supervisor or run a repair on this window'"'"'s own initiative' \
  'before or after the choice' \
  'no comment, no label, no commit and no `cenci babysit` call other than the stop above'; do
  require_in "babysit-attention/codex.md Never" "$attention_never" "$phrase"
done

runtime="${ROOT}/skills/codex-runtime/SKILL.md"
grep -q 'Require.*`/plan`\|must begin in `/plan`' "$runtime" || fail "Plan-mode contract"
grep -q '.cenci/checkpoints' "$runtime" || fail "checkpoint contract"
grep -q 'Arm a native Codex goal only after' "$runtime" || fail "goal arm contract"
grep -q 'Clear it before human input' "$runtime" || fail "goal clear contract"
grep -q 'built-in' "$runtime" && grep -q 'same bounded role prompt' "$runtime" || fail "agent fallback contract"
count="$(find "${ROOT}/templates/codex/agent-roles" -name '*.toml' | wc -l)"
test "$count" -ge 5 || fail "Codex agent templates"
echo "workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
