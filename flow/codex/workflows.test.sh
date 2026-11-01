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
  # The attempt count comes from `cenci babysit status <pr>`, never from a state-directory read.
  require_in "babysit-attention/${name}" "$text" 'cenci babysit status <pr>'
  require_in "babysit-attention/${name}" "$text" 'ciFixAttempts'
  require_in "babysit-attention/${name}" "$text" 'conflictFixAttempts'
  require_in "babysit-attention/${name}" "$text" 'does not decode as JSON'
  require_in "babysit-attention/${name}" "$text" 'stop status unknown'
  forbid_in "babysit-attention/${name}" "$text" 'cenci/babysit/'
  # The removed retry option: a non-empty file is required so the absence is not vacuous.
  forbid_in "babysit-attention/${name}" "$text" 'fresh budget'
done
require_in "babysit-attention/SKILL.md" "$attention_skill_all" '`AskUserQuestion`'

# The babysit skill reports the forwarded stop's three outcomes and verifies through `status`.
babysit_all="$(normalize "${ROOT}/skills/babysit/SKILL.md")"
for phrase in 'cenci babysit status <pr>' 'Stopped babysitting PR #<pr> on the host.' 'not stopped: <reason>' 'stop status unknown'; do
  require_in "babysit/SKILL.md" "$babysit_all" "$phrase"
done
forbid_in "babysit/SKILL.md" "$babysit_all" 'there is no `cenci babysit status` subcommand'

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
# The attempt count: read from `cenci babysit status <pr>`, by reason, with the fallback phrase.
for phrase in 'cenci babysit status <pr>' '`ciFixAttempts`' '`conflictFixAttempts`' 'exits non-zero' 'does not decode as JSON' \
  'retry cap reached (exact count not readable from here)'; do
  require_in "babysit-attention/codex.md Summary" "$attention_summary" "$phrase"
done
forbid_in "babysit-attention/codex.md Summary" "$attention_summary" 'cenci/babysit/'

attention_stop="$(section "$attention_codex" '## Stop choice')"
for phrase in 'cenci babysit stop <pr>' 'sandbox' 'not stopped' 'stop status unknown' 'run on the host' 'never report success'; do
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
  'no comment, no label, no commit and no `cenci babysit` call other than the stop above and `cenci babysit status <pr>`'; do
  require_in "babysit-attention/codex.md Never" "$attention_never" "$phrase"
done

runtime="${ROOT}/skills/codex-runtime/SKILL.md"
grep -q '.cenci/checkpoints' "$runtime" || fail "checkpoint contract"
grep -qx '## Goal lifecycle' "$runtime" || fail "goal lifecycle contract"
grep -q 'built-in' "$runtime" && grep -q 'same bounded role prompt' "$runtime" || fail "agent fallback contract"

# --- Approved-plan handoff (#1234): C2, C3, C5, C6, C7 and C9 contract text ---
# order_in <label> <text> <phrase>...: each phrase must occur, in the given order, in a non-empty text
order_in(){
  local label="$1" text="$2" phrase rest
  shift 2
  [[ -n "$text" ]] || { fail "$label: scope is empty, so order cannot be asserted"; return; }
  rest="$text"
  for phrase in "$@"; do
    [[ -n "$phrase" ]] || { fail "$label: empty phrase"; return; }
    [[ "$rest" == *"$phrase"* ]] || { fail "$label: '$phrase' missing or out of order"; return; }
    rest="${rest#*"$phrase"}"
  done
}
# handoff_artifact <workflow> -> the artifact path literal the companion and docs/codex.md both name
handoff_artifact(){
  case "$1" in
    configure) echo '.plans/codex/configure-repo.md' ;;
    refine) echo '.plans/codex/refine-<ticket>.md' ;;
    implement) echo '.plans/<ticket>-<slug>.md' ;;
    address-review) echo '.plans/codex/address-review-<pr>.md' ;;
    refactor) echo '.plans/codex/refactor-repo.md' ;;
    maintain) echo '.plans/codex/maintain-<mode>[-<scope>].md' ;;
  esac
}
handoff_workflows='configure refine implement address-review refactor maintain'

runtime_all="$(normalize "$runtime")"
handoff_intro="$(section "$runtime" '## Approved-plan handoff')"
handoff_self_check="$(section "$runtime" '### Plan-mode self-check')"
handoff_boundaries="$(section "$runtime" '### Write boundaries')"
handoff_persist="$(section "$runtime" '### Persist step')"
handoff_apply="$(section "$runtime" '### Apply')"
handoff_replan="$(section "$runtime" '### Re-plan')"

# Retired wording: the three bullets the handoff section replaces must be gone (the old line-204 check pinned the first).
for phrase in 'must begin in `/plan`' 'second normal-mode invocation' 'Refinement is an exception' 'passing the approved plan or `.plans/<file>` explicitly'; do
  forbid_in "codex-runtime retired wording" "$runtime_all" "$phrase"
done

# C7 scope of the contract: Codex-only, with the reason Claude Code's SKILL.md procedures stay unchanged (skill-authoring.md).
for phrase in 'Codex-only' 'Claude Code has no collaboration-mode switch' '`phases/phase-1-plan.md` owns its approval' \
  '.plans/codex/<workflow>-<target>.md' '.plans/<ticket>-<slug>.md' 'configure and refactor use `repo`' '`<mode>-<scope>`' 'task slug'; do
  require_in "codex-runtime handoff intro" "$handoff_intro" "$phrase"
done

# CLI contract: verify and digest are documented next to the other subcommands.
for phrase in 'verify <path> <workflow> <target> | digest <plan-path>' 'checkpoint.mjs" verify <path> <workflow> <target>' \
  'checkpoint.mjs" digest <plan-path>' 'nextPhase'; do
  require_in "codex-runtime CLI contract" "$runtime_all" "$phrase"
done

# C2: the Plan-mode self-check. Outside Plan mode: no tool call, one refusal line, and no side effect.
for phrase in '<collaboration_mode>' 'no tool call' 'cannot be read' \
  'cenci: $cenci:<wf> plans only in Plan mode — type /plan and resend: /plan $cenci:<wf> <target>' \
  'leave Plan mode and resend'; do
  require_in "C2 plan-mode self-check" "$handoff_self_check" "$phrase"
done
for phrase in 'Planning in Plan mode makes no project or GitHub mutation' '`.plans`' '`cenci pipeline`'; do
  require_in "C2 plan-mode writes" "$handoff_boundaries" "$phrase"
done
# C3/C4/Q5: the self-check scopes itself to the planning request. persist and apply run in Default mode, are not refused by it,
# and keep their own rules: refused only while Plan mode is still active, otherwise persist -> Persist step, apply -> require-runtime.sh -> verify.
for phrase in 'applies to the planning request only' 'neither `persist` nor `apply`' \
  '`persist` and `apply` run in Default mode and are not refused by this check' \
  'while Plan mode is still active is refused with no tool call' 'leave Plan mode and resend' \
  'in Default mode `persist` proceeds to the Persist step' '`apply` proceeds to `require-runtime.sh`, then `verify`'; do
  require_in "Q5 persist/apply exempt from planning self-check" "$handoff_self_check" "$phrase"
done

# C2/C7: every planning companion opens (first paragraph after the H1) with the self-check and names the one handoff.
for wf in $handoff_workflows; do
  companion="${ROOT}/skills/${wf}/codex.md"
  opening="$(awk 'NF && !/^#/ {print; exit}' "$companion")"
  require_in "C2 ${wf}/codex.md opening paragraph" "$opening" 'Plan-mode self-check'
  require_in "C2 ${wf}/codex.md opening paragraph" "$opening" '`codex-runtime`'"'"'s **Approved-plan handoff**'
  # Q5/#979: the self-check is scoped to the planning request, and the opening only references the handoff (never restates it)
  require_in "Q5 ${wf}/codex.md opening paragraph" "$opening" 'Planning-request Plan-mode self-check first'
  for phrase in 'checkpoint.mjs verify' 'require-runtime.sh' '`persist` writes only' 'Planning runs only in Plan mode'; do
    forbid_in "#979 ${wf}/codex.md opening paragraph restates the handoff" "$opening" "$phrase"
  done
  # C7: the artifact path and the retired two-invocation wording
  require_in "C7 ${wf}/codex.md artifact" "$(normalize "$companion")" "$(handoff_artifact "$wf")"
  forbid_in "C7 ${wf}/codex.md" "$(normalize "$companion")" 'second normal-mode invocation'
done
forbid_in "C2 address-review/codex.md" "$(normalize "${ROOT}/skills/address-review/codex.md")" 'checkpoint the proposed fixes'
# C7: configure no longer keeps the approved plan in the conversation, and no `apply` takes a checkpoint id.
configure_all="$(normalize "${ROOT}/skills/configure/codex.md")"
forbid_in "C7 configure/codex.md" "$configure_all" 'the approved plan remains in the prior conversation'
forbid_in "C7 configure/codex.md" "$configure_all" 'apply <checkpoint-id>'
require_in "C7 configure/codex.md" "$configure_all" 'cenci run configure apply repo --agent codex'
require_in "C7 configure/codex.md" "$configure_all" 'travel in the approved-plan artifact'
forbid_in "C7 configure/codex.md" "$configure_all" 'plan/apply checkpoint'
forbid_in "C7 fleet-enrollment.md" "$(normalize "${ROOT}/skills/configure/phases/fleet-enrollment.md")" 'carry answers through the checkpoint'
# Q1 (#1234): refine joins the handoff; the #1316 one-invocation procedure is gone.
refine_all="$(normalize "${ROOT}/skills/refine/codex.md")"
forbid_in "C7 refine/codex.md" "$refine_all" 'do not require a separate `/plan`'
forbid_in "C7 refine/codex.md" "$refine_all" 'continues through preparation and apply in the same run'
require_in "C7 refine/codex.md" "$refine_all" 'Explicit Plan mode remains read-only'
require_in "C7 refine/codex.md" "$refine_all" '`apply` re-fetches'
require_in "C7 refine/codex.md" "$refine_all" '`refinementConfirmBeforeApply`'
# implement keeps the pinned ordering anchor and takes its first-vs-saved apply from the checkpoint phase.
implement_all="$(normalize "${ROOT}/skills/implement/codex.md")"
order_in "C7 implement/codex.md anchors" "$implement_all" 'Stop before mutations' 'create the worktree'
require_in "C7 implement/codex.md" "$implement_all" 'whose `phase` is `planned` is a new-plan apply'
forbid_in "C7 implement/codex.md" "$implement_all" 'in the same session as an approved `/plan` result'

# C3: the persist step writes only the artifact and the checkpoint, prints the apply command, and claims nothing.
for phrase in 'writes only the plan artifact and the checkpoint' '## Target' '## Approved Plan' '## Choices' \
  'checkpoint.mjs" digest' 'init <ckpt> <wf> <target> planned --plan-path' '--plan-digest' \
  'cenci run <wf> apply <target> --agent codex' '$cenci:<wf> apply <target>' '$cenci:<wf> persist <target>' \
  'never apply mutations'; do
  require_in "C3 persist-only contract" "$handoff_persist" "$phrase"
done
for phrase in 'claim' 'label' 'worktree' '`gh`' 'cenci pipeline' 'GitHub'; do
  forbid_in "C3 persist section" "$handoff_persist" "$phrase"
done
require_in "C3 write boundaries" "$handoff_boundaries" 'The persist step makes no claim, label, worktree, `cenci pipeline` or GitHub write'

# C5: apply order is require-runtime.sh, then checkpoint.mjs verify, then the first mutation; a failed verify stops before any write.
order_in "C5 apply stops on verify" "$handoff_apply" 'require-runtime.sh' 'checkpoint.mjs" verify <ckpt> <wf> <target>' 'a non-zero `verify` stops before any write' \
  "the workflow's mutations"
require_in "C5 apply stops on verify" "$handoff_apply" 'reports its one stderr line'
require_in "C5 apply stops on verify" "$handoff_apply" '`resolved.planPath`'
require_in "C5 apply stops on verify" "$handoff_apply" 'plan in Plan mode, then persist'

# C6: a resumed apply starts at nextPhase and never repeats a completed gate; first-vs-saved comes from the checkpoint phase.
for phrase in 'never recreate a recorded worktree, re-apply a label or reopen a PR' '`nextPhase`' '`lastCompletedGate`' \
  'whose `phase` is `planned` is a new-plan apply' 'any later phase is a saved-plan apply' \
  "advances the checkpoint to a phase before that phase's first write"; do
  require_in "C6 resume skips completed gates" "$handoff_apply" "$phrase"
done

# Q&A 9: persist over an existing checkpoint of the same identity.
for phrase in '`clear`' 'in flight' 'resume `apply` command' 'explicit `checkpoint.mjs clear`'; do
  require_in "re-plan rule" "$handoff_replan" "$phrase"
done
# F1: the rule keys on `status` (`complete` is a status, never a phase), here and in persist step 1 and the docs.
for phrase in 'whose `status` is `planned` or `complete`' 'whose `status` is `running` or `needs-input`'; do
  require_in "F1 re-plan rule keys on status" "$handoff_replan" "$phrase"
done
forbid_in "F1 re-plan rule keys on status" "$handoff_replan" '`phase` is `planned` or `complete`'
for phrase in '`status` `planned` or `complete` means run `clear`' '`status` `running` or `needs-input` is a run in flight'; do
  require_in "F1 persist step 1 keys on status" "$handoff_persist" "$phrase"
done
forbid_in "F1 persist step 1 keys on status" "$handoff_persist" 'a `phase` of `planned` or `complete`'
forbid_in "F1 persist step 1 keys on status" "$handoff_persist" 'any other `phase` is a run in flight'
# F2: the user-run clear remedy is named wherever the only exit may be discarding a run.
clear_remedy='checkpoint.mjs" clear <ckpt> <wf> <target>'
require_in "F2 persist in-flight refusal names clear" "$handoff_persist" "$clear_remedy"
require_in "F2 persist in-flight refusal names clear" "$handoff_persist" 'if that `apply` stops on `verify`'
require_in "F2 apply stop names clear" "$handoff_apply" "$clear_remedy"
require_in "F2 apply stop names clear" "$handoff_apply" 'user-run'
require_in "F2 re-plan names clear" "$handoff_replan" 'checkpoint.mjs clear <ckpt> <wf> <target>'

# C9: flow/docs/codex.md documents the copy-pasteable plan -> persist -> apply commands and every capability error.
codex_docs="${ROOT}/docs/codex.md"
codex_docs_all="$(normalize "$codex_docs")"
docs_handoff="$(section "$codex_docs" '## Plan, persist and apply')"
for phrase in 'cenci run implement 42 --agent codex' '/plan $cenci:implement 42' '$cenci:implement persist 42' \
  'cenci run implement apply 42 --agent codex'; do
  require_in "C9 docs commands" "$docs_handoff" "$phrase"
done
order_in "C9 docs command order" "$docs_handoff" 'cenci run implement 42 --agent codex' '/plan $cenci:implement 42' '$cenci:implement persist 42' \
  'cenci run implement apply 42 --agent codex'
for phrase in \
  'no checkpoint found; no approved plan was persisted — plan in Plan mode, then persist' \
  'path does not match workflow and target; the file name must be <wf>-<target>.json' \
  'checkpoint records a different workflow or target than requested; it is never reset silently; clear cannot remove it either, so remove the file by hand if it is stale' \
  'legacy v1 checkpoint records no approved plan; plan in Plan mode, then persist' \
  'checkpoint is complete (terminal); plan in Plan mode, then persist a new plan' \
  'checkpoint records no approved plan; plan in Plan mode, then persist' \
  'plan artifact path is not under .plans/; use a repo-relative path such as .plans/<name>.md' \
  'plan artifact missing; plan in Plan mode, then persist' \
  'plan artifact unreadable; plan in Plan mode, then persist again to rewrite it' \
  'plan artifact changed since approval (digest mismatch); plan in Plan mode, then persist again' \
  'cenci: Codex workflow runtime unavailable — Node.js 18+ not found on PATH (found: ' \
  'Install Node.js 18 or newer, then rerun; no checkpoint, workflow, or GitHub state was changed.' \
  'Collaboration modes are disabled' 'Plan mode unavailable' \
  "cenci: \$cenci:<wf> plans only in Plan mode — type /plan and resend: /plan \$cenci:<wf> <target>"; do
  require_in "C9 docs errors" "$docs_handoff" "$phrase"
done
# The docs literals are the helper's literals, not a paraphrase.
for phrase in 'no checkpoint found' 'no approved plan was persisted — plan in Plan mode, then persist' 'plan artifact changed since approval (digest mismatch)'; do
  require_in "C9 docs literals match checkpoint.mjs" "$(normalize "${ROOT}/codex/checkpoint.mjs")" "$phrase"
done
require_in "C9 docs literals match require-runtime.sh" "$(normalize "${ROOT}/codex/require-runtime.sh")" 'Codex workflow runtime unavailable — Node.js %s+ not found on PATH (found: %s). Install Node.js %s or newer, then rerun; no checkpoint, workflow, or GitHub state was changed.'
# Every workflow's artifact and apply command appear in the docs table, and its companion names the same artifact.
for wf in $handoff_workflows; do
  require_in "C9 docs table ${wf}" "$docs_handoff" "$(handoff_artifact "$wf")"
  require_in "C9 docs table ${wf}" "$docs_handoff" "cenci run ${wf} apply "
done
require_in "C9 docs unattended" "$docs_handoff" 'unattended'
# The retired two-invocation and one-invocation-refine wording is gone, and the checkpoint no longer routes.
forbid_in "C9 docs retired wording" "$codex_docs_all" 'A second normal-mode invocation persists plans'
forbid_in "C9 docs retired wording" "$codex_docs_all" 'Refinement can complete in one normal-mode invocation'
require_in "C9 docs checkpoint routing" "$codex_docs_all" 'the checkpoint selects first-apply vs saved-plan apply only'

# Review fixes (#1234): the in-flight checkpoint check precedes every persist write, and persist requires a Plan-mode approval in this session.
no_approved_plan='cenci: no approved plan in this session — type /plan and resend: /plan $cenci:<wf> <target>'
order_in "R1 persist checks for a checkpoint before writing" "$handoff_persist" 'checkpoint.mjs" get <ckpt> <wf> <target>' 'Write the artifact' \
  'checkpoint.mjs" digest' 'checkpoint.mjs" clear <ckpt> <wf> <target>' 'init <ckpt> <wf> <target> planned --plan-path'
for phrase in 'before writing anything' 'a run in flight' 'resume `apply` command' 'run `clear`' 'stop and report its stderr line'; do
  require_in "R1 persist in-flight refusal" "$handoff_persist" "$phrase"
done
require_in "R1 re-plan ordering" "$handoff_replan" 'before the artifact is written'
forbid_in "R1 re-plan ordering" "$handoff_replan" 'the artifact is overwritten'

# R2: the exact refusal literal when this session has no Plan-mode-approved plan, in the contract and in the docs.
require_in "R2 persist approval check" "$handoff_persist" "$no_approved_plan"
require_in "R2 persist approval check" "$handoff_persist" 'no Plan-mode-approved plan for `<wf> <target>`'
order_in "R2 approval check precedes persist writes" "$handoff_persist" "$no_approved_plan" 'require-runtime.sh' 'Write the artifact'
require_in "R2 docs approval refusal" "$docs_handoff" "$no_approved_plan"

# R4: implement's artifact is the planner's plan file; the Target/Approved Plan wrapper is for the other five workflows.
# The shape is stated once, in codex-runtime's Persist step (#979 restatement rule); implement/codex.md only references it.
for phrase in 'for implement the artifact is the planner'"'"'s plan file' 'YAML front matter (`---` as the first line)' \
  '`## Ticket Details`' '`## Implementation Plan`' '`## Architectural Context`' 'appending a `## Choices` heading after them' \
  'other five workflows only'; do
  require_in "R4 persist implement artifact shape" "$handoff_persist" "$phrase"
done
for phrase in 'YAML front matter' 'appending a `## Choices` heading' 'other five workflows only' '`## Target`/`## Approved Plan` wrapper'; do
  forbid_in "R4 implement/codex.md does not restate the artifact shape" "$implement_all" "$phrase"
done
require_in "R4 implement/codex.md references the shape" "$implement_all" 'The artifact shape is defined only in `codex-runtime`'"'"'s Persist step'
require_in "R4 implement/codex.md three-heading hard stop" "$implement_all" 'A missing heading is a hard stop: stop before the checkpoint/label mutations'

# R3: a saved-plan apply registers only the plan that verify checked, and stops with zero writes otherwise.
for phrase in 'must equal the checkpoint'"'"'s verified `resolved.planPath`' 'artifacts[0]' \
  'stop with zero writes and tell the user to re-plan' 'never register an unverified plan'; do
  require_in "R3 saved-plan apply registers the verified plan" "$implement_all" "$phrase"
done
# F4: the comparison is an exact string match of a real path against the absolute resolved.planPath.
forbid_in "F4 identity comparison" "$implement_all" 'compared repo-relative'
for phrase in 'take the real path of `plan-check`'"'"'s `artifacts[0]`' 'require it to equal `resolved.planPath` as an exact string' 'Any difference means stop with zero writes'; do
  require_in "F4 identity comparison is an exact real-path string match" "$implement_all" "$phrase"
done
require_in "F2 implement identity-mismatch stop names clear" "$implement_all" 'checkpoint.mjs" clear <ckpt> <wf> <target>'
# F5: "Plan-mode-approved" is defined once, in the Persist step.
require_in "F5 Plan-mode-approved defined" "$handoff_persist" 'a plan produced in Plan mode in this session for `<wf> <target>`, followed by the user'"'"'s `persist` or implement-the-plan turn'
# F6: nextPhase null just runs complete.
require_in "F6 nextPhase null" "$handoff_apply" 'run no workflow mutation, run `complete`, and report that the run is already finished'
forbid_in "F6 nextPhase null" "$handoff_apply" 'advance or `complete` the checkpoint'
# F1/F2 docs: status wording and the clear remedy.
require_in "F1 docs re-plan keys on status" "$docs_handoff" 'checkpoint whose `status` is `planned`'
require_in "F1 docs re-plan keys on status" "$docs_handoff" 'or `complete`'
forbid_in "F1 docs re-plan keys on status" "$docs_handoff" 'checkpoint that is `planned` (approved, never applied) or `complete`'
require_in "F2 docs in-flight refusal names clear" "$docs_handoff" 'checkpoint.mjs" clear <ckpt> <wf> <target>'
require_in "F2 docs in-flight refusal names clear" "$docs_handoff" 'if that `apply` stops on `verify`'
order_in "R3 equality check precedes every pipeline write" "$implement_all" 'Resume routing (apply, ticket mode).' \
  'must equal the checkpoint'"'"'s verified `resolved.planPath`' 'Pipeline lifecycle (ticket mode).'

# R5/R6: apply defines nextPhase null and stops on a failed checkpoint write.
for phrase in '`nextPhase` `null` means every phase is done' 'run no workflow mutation' 'report that the run is already finished' \
  'A failure of any checkpoint write (`advance`, `block`, `complete`) stops apply before the next mutation and is reported'; do
  require_in "R5/R6 apply nextPhase and checkpoint-write failures" "$handoff_apply" "$phrase"
done

# --- Persist lifecycle hardening (#1384) ---
# Completion rule: one shared sentence and table in Apply, the last phase named explicitly and no --gate.
require_in "#1384 apply completion command" "$handoff_apply" 'node "${PLUGIN_ROOT}/codex/checkpoint.mjs" complete <ckpt> <wf> <target> <last-phase>'
require_in "#1384 apply completion rule" "$handoff_apply" "reaches its successful end"
forbid_in "#1384 apply completion without gate" "$handoff_apply" '<last-phase> --gate'
# The completion paragraph itself says `complete` runs without --gate; with that one sanctioned mention removed, no `--gate` may remain in it.
completion_para="${handoff_apply#*"When a workflow's apply reaches"}"
completion_para="${completion_para%%"The last phase and the success point"*}"
require_in "#1384 apply completion paragraph" "$completion_para" '(without `--gate`)'
forbid_in "#1384 apply completion paragraph passes no --gate" "${completion_para//'(without `--gate`)'/}" '--gate'
require_in "#1384 apply completion table header" "$handoff_apply" '| Workflow | Last phase | Success point |'
# Each success point is tied to its own row (workflow, last phase and success point in one cell run).
for row in \
  '| `implement` | `pr` | after `gh pr create` succeeds and the babysit hand-off has been attempted |' \
  '| `maintain` | `pr` | after the PR is open and its mergeability is confirmed not CONFLICTING (an unresolved UNKNOWN runs no `complete`) |' \
  '| `refactor` | `pr` | after the approved tickets are created |' \
  '| `address-review` | `push` | after the push, the replies and the review re-request |' \
  '| `configure` | `pr` | after the PR is opened |' \
  '| `refine` | `apply` | after the GitHub writes are verified as persisted |'; do
  require_in "#1384 apply completion table row" "$handoff_apply" "$row"
done
require_in "#1384 apply maintain completes only on a confirmed mergeability" "$handoff_apply" 'confirmed not CONFLICTING'
# A stopped run never completes.
for phrase in 'a stopped, blocked, declined or failed apply never runs `complete`' 'a `verify` failure' 'a `block`' \
  'Decline' 'pre-write STOP' 'lean-contract STOP' 'PR stop' 'unknown-stage stop' "maintain's unresolved UNKNOWN mergeability"; do
  require_in "#1384 apply never completes on stop" "$handoff_apply" "$phrase"
done
# A failed `complete` after the final mutation: reported with the deliverable, never undone, user-run clear remedy.
for phrase in 'PR URL, ticket numbers or pushed SHA' 'together with the `complete` failure' 'never as a clean success' 'never undone'; do
  require_in "#1384 apply failed complete reporting" "$handoff_apply" "$phrase"
done
require_in "#1384 apply failed complete names clear" "$handoff_apply" 'checkpoint.mjs" clear <ckpt> <wf> <target>'

# Persist: step 1 decides only, the artifact check precedes the write, and the replacing clear follows the digest.
require_in "#1384 persist step 1 decides only" "$handoff_persist" 'decides only and writes nothing'
forbid_in "#1384 persist step 1 no early clear" "$handoff_persist" 'run `clear` for the identity (see Re-plan below), then continue'
require_in "#1384 persist artifact check" "$handoff_persist" 'workflow-specific artifact check'
require_in "#1384 persist artifact check leaves checkpoint" "$handoff_persist" 'byte-identical'
order_in "#1384 persist artifact check precedes the artifact write" "$handoff_persist" 'workflow-specific artifact check' 'Write the artifact'
# Any status other than planned/complete is a run in flight, in Persist step 1 and in Re-plan.
other_status='an unrecognised value, an absent field, or a non-string value on a legacy v1 checkpoint'
require_in "#1384 persist other status is in flight" "$handoff_persist" "$other_status"
require_in "#1384 persist other status gets the same refusal" "$handoff_persist" 'is a run in flight too, with the same refusal'
require_in "#1384 re-plan other status is in flight" "$handoff_replan" "$other_status"
require_in "#1384 re-plan other status is tied to a status check" "$handoff_replan" "or any other \`status\` (${other_status})"
# The second get guards the conditional clear against a concurrent apply.
require_in "#1384 persist second get before clear" "$handoff_persist" 'runs `get` again'
require_in "#1384 persist second get stop" "$handoff_persist" 'stops without `clear` or `init`'
require_in "#1384 persist second get fails on any non-zero exit" "$handoff_persist" 'any non-zero exit, exit 3 included'
require_in "#1384 persist second get stop reports the rewritten artifact" "$handoff_persist" 'the artifact was already rewritten but the checkpoint was not changed'
order_in "#1384 persist second get sits between the digest and init" "$handoff_persist" 'checkpoint.mjs" digest' 'runs `get` again' \
  'checkpoint.mjs" clear <ckpt> <wf> <target>' 'init <ckpt> <wf> <target> planned --plan-path'
# The in-flight guard covers a run step 1 sees; a run starting apply later is caught by the second get, and the status guard is persist's.
require_in "#1384 re-plan in-flight guard scope" "$handoff_replan" 'the status guard on `clear` lives in persist, not in `checkpoint.mjs clear`'
# Re-plan wording: keyed on status, check before the write, clear just before init.
forbid_in "#1384 re-plan wording" "$handoff_replan" 'is cleared (`clear`) first'
forbid_in "#1384 re-plan keys on status" "$handoff_replan" 'follows its phase'
require_in "#1384 re-plan wording" "$handoff_replan" 'follows its `status`'
require_in "#1384 re-plan wording" "$handoff_replan" 'the `clear` runs just before `init`'
# A legacy v1 checkpoint with status planned or complete takes the same clear-and-replace path as a v2 one.
legacy_replace='a legacy v1 checkpoint whose `status` is `planned` or `complete` is cleared and replaced like a v2 one'
require_in "#1384 persist legacy v1 planned/complete is replaced" "$handoff_persist" "$legacy_replace"
require_in "#1384 re-plan legacy v1 planned/complete is replaced" "$handoff_replan" "$legacy_replace"
# implement's line 69 and the user doc mirror the same ordering.
require_in "#1384 implement missing heading wording" "$implement_all" 'stops the persist step with no artifact write and leaves any existing checkpoint unchanged'
forbid_in "#1384 implement missing heading wording" "$implement_all" 'stops and writes neither'
for phrase in 'marks a finished run `complete`' 'only after the new artifact is written and digested' \
  'unrecognised legacy v1 status' 'refused as in flight'; do
  require_in "#1384 docs persist lifecycle" "$docs_handoff" "$phrase"
done
# The "Planning again over a checkpoint" paragraph states all three facts itself, not just step 3.
docs_replan="${docs_handoff#*"Planning again over a checkpoint"}"
docs_replan="${docs_replan%%"**Unattended launches.**"*}"
for phrase in 'marks a finished run `complete`' 'the replacing `clear` comes only after the new artifact is written and digested' \
  'unrecognised legacy v1 status' 'refused as in flight'; do
  require_in "#1384 docs re-plan paragraph" "$docs_replan" "$phrase"
done
for phrase in 'cleared first' 'is cleared (`clear`) first' 'follows its phase'; do
  forbid_in "#1384 docs re-plan paragraph retired wording" "$docs_replan" "$phrase"
done

# --- Native goal lifecycle (#1235): D1-D6 ---
# One shared lifecycle in codex-runtime; implement and maintain only reference its steps (#979: no restated rule).
# goal_row <file> <case> -> the `### Goal cases` table row whose first cell is exactly <case>
goal_row(){ awk -v c="| $2 |" '$0=="### Goal cases" {on=1; next} on && /^#+ / {exit} on && index($0, c)==1 {print; exit}' "$1" | tr -s ' '; }

[[ "$(grep -cx '## Goal lifecycle' "$runtime")" -eq 1 ]] || fail "D5 codex-runtime must carry exactly one '## Goal lifecycle' section"
goal_create="$(section "$runtime" '### Create step')"
goal_stop="$(section "$runtime" '### Stop step')"
goal_complete="$(section "$runtime" '### Complete step')"
goal_cases_text="$(section "$runtime" '### Goal cases')"

# D1: authorization and the fixed objective.
for phrase in 'Only a human-issued `apply` may create a goal' \
  'Planning, `persist`, `ci-repair` and `babysit-attention` never create a goal' \
  'Complete approved plan <path> through an open PR' 'repo-relative `planPath` that `verify` returns' \
  'Never set a `token_budget`' 'Babysit through merge is never part of the objective' 'Claude Code `SKILL.md` procedures stay unchanged'; do
  require_in "D1 goal authorization and objective" "$(section "$runtime" '## Goal lifecycle')" "$phrase"
done
order_in "D1 create step: runs after verify and right before the first advance" "$goal_create" \
  'after `verify` passes' 'right before the first checkpoint `advance`'
order_in "D1/D2 create step: the existing-goal check precedes the create call" "$goal_create" 'get_goal({})' 'create_goal({objective: "Complete approved plan <path> through an open PR"})'
# D2: an unfinished goal is left alone; absent tools take the checkpoint-only path; the outcome is recorded.
for phrase in 'status other than `complete`' 'left unchanged' '`skipped-active`' '`unavailable`' '`created`' 'checkpoint-only recovery' \
  '--goal <created|skipped-active|unavailable>' 'checkpoint state alone'; do
  require_in "D2 create step" "$goal_create" "$phrase"
done
# Review fix: `unavailable` only for tools absent before any call; an error from an invoked goal tool is a stop.
for phrase in 'absent from the client'"'"'s tool list before any goal call is made' \
  'any error from an invoked goal tool (`get_goal`, `create_goal` or `update_goal`) is a stop' 'never downgraded to `unavailable`'; do
  require_in "D2 create step unavailable means absent tools only" "$goal_create" "$phrase"
done
require_in "D2 goal cases tool-error row: an invoked goal tool's error is a stop" "$(goal_row "$runtime" 'tool error')" 'an error from an invoked goal tool'
# Review fix: a resume adopts this run's own goal as `created` when the checkpoint recorded none (crash between create_goal and the first advance).
for phrase in 'the checkpoint records `created`, or records no goal' 'unfinished goal whose objective equals this run'"'"'s objective' \
  'a goal with a different objective is `skipped-active`'; do
  require_in "D2 create step resume adopts this run's goal" "$goal_create" "$phrase"
done
# D3: every stop leaves the goal as it is and keeps the unfinished state in the checkpoint.
for phrase in 'checkpoint.mjs block' 'question' 'red or unrunnable baseline' 'native-tool error' 'budget or usage limit' '`Context: low`' \
  'left exactly as it is' 'never marks the objective complete' 'never invents a clear or pause operation' \
  'exposes' 'conditions of that client'; do
  require_in "D3 stop step" "$goal_stop" "$phrase"
done
# D4: complete only for a created goal, only after the PR exists, before babysit.
for phrase in 'only when the checkpoint'"'"'s `goal` is `created`' 'a successful `gh pr create`' 'before `cenci babysit` is armed' \
  'A failed `gh pr create` is a stop' 'update_goal({status:"complete"})'; do
  require_in "D4 complete step" "$goal_complete" "$phrase"
done
# D5: the retired arm/clear wording is gone from the contract and every restatement site (case-insensitive).
for phrase in 'Arm a native Codex goal only after' 'Clear it before human input' 'persist the checkpoint, clear the active goal'; do
  forbid_in "D5 codex-runtime retired goal wording" "$runtime_all" "$phrase"
done
for swept in "$runtime" "${ROOT}"/skills/*/codex.md "${ROOT}/docs/codex.md" "${ROOT}/docs/adapter-contract.md"; do
  swept_text="$(normalize "$swept" | tr '[:upper:]' '[:lower:]')"
  for phrase in 'clear the goal' 'clear the active goal' 'clear it before' 'goal-clear' 'clears/pauses' 'arm the native goal'; do
    forbid_in "D5 clear-the-goal sweep ${swept#"${ROOT}/"}" "$swept_text" "$phrase"
  done
done
# D5: implement and maintain reference each step and never restate the lifecycle.
maintain_all="$(normalize "${ROOT}/skills/maintain/codex.md")"
for pair in "implement:${implement_all}" "maintain:${maintain_all}"; do
  wf="${pair%%:*}"
  text="${pair#*:}"
  for phrase in '**Goal lifecycle** create step' '**Goal lifecycle** stop step' '**Goal lifecycle** complete step'; do
    require_in "D5 ${wf}/codex.md references the shared lifecycle" "$text" "$phrase"
  done
  for phrase in 'get_goal' 'create_goal' 'update_goal' 'Complete approved plan' 'token_budget'; do
    forbid_in "D5 ${wf}/codex.md restates the lifecycle (#979)" "$text" "$phrase"
  done
done
order_in "D1/D3/D4 implement goal steps" "$implement_all" 'once `verify` has passed' '**Goal lifecycle** create step' 'run-gate.sh' \
  '**Goal lifecycle** stop step' 'gh pr create' '**Goal lifecycle** complete step' 'cenci pipeline artifact <id> --pr' 'cenci babysit <pr> --agent codex --interval'
order_in "D1/D4 maintain goal steps" "$maintain_all" 'Verify `apply` read the approved apply plan' '**Goal lifecycle** create step' 'to `worktree`' \
  'open one PR' '**Goal lifecycle** stop step' '**Goal lifecycle** complete step'
require_in "D4 implement babysit is outside the objective" "$implement_all" 'outside the goal objective'
require_in "D4 maintain completes after Gate C" "$maintain_all" 'Run the **Goal lifecycle** complete step only after the PR exists and its mergeability is confirmed not `CONFLICTING`'
# Review fix: an UNKNOWN mergeability that stays unresolved is not "confirmed not CONFLICTING": no complete step.
require_in "D4 maintain unresolved UNKNOWN runs no complete step" "$maintain_all" 'An `UNKNOWN` mergeability that stays unresolved after the re-queries is not confirmed: run no complete step, leave the goal as it is, and report the PR as open with the mergeability-unverified note'
# Review fix: implement's `Context: low` split signal names the block command and the shared stop step (referenced, not restated).
require_in "D3 implement Context: low split signal stops through the shared stop step" "$implement_all" 'stops the run: run `checkpoint.mjs block` per the **Goal lifecycle** stop step'

# D6: nine goal cases. Each case has two halves: the checkpoint.mjs command sequence the case runs in a scratch repo
# (real helper, real state), and the case's row in the Goal cases table naming the goal call it makes. The goal tools
# themselves exist only in native Codex, so their calls are proven by the native runner, not here.
for goal_case in 'existing goal' 'no goal' 'no tools' 'ambiguity' 'red baseline' 'tool error' 'budget exhaustion' 'PR success' 'PR failure'; do
  [[ -n "$(goal_row "$runtime" "$goal_case")" ]] || fail "D6 Goal cases table has no row for '${goal_case}'"
done
goal_root="$(mktemp -d /var/tmp/cenci-goal-fixtures.XXXXXX)" || { echo "workflows.test.sh: mktemp failed (goal fixtures)." >&2; exit 2; }
trap 'rm -rf "$goal_root"' EXIT
GOAL_CKPT_JS="${ROOT}/codex/checkpoint.mjs"
GOAL_REL=".cenci/checkpoints/implement-42-demo.json"
GOAL_REPO=""
goal_ckpt(){ ( cd "$GOAL_REPO" && node "$GOAL_CKPT_JS" "$@" ) 2>"${goal_root}/stderr"; }
# goal_repo <case>: a scratch repo whose checkpoint is an approved, planned implement run; sets GOAL_REPO
goal_repo(){
  local digest
  GOAL_REPO="${goal_root}/$1"
  mkdir -p "${GOAL_REPO}/.plans" && git init -q "$GOAL_REPO" || { fail "D6 fixture '$1': repo setup failed"; return 1; }
  printf '%s\n' '## Ticket Details' 'goal fixture' > "${GOAL_REPO}/.plans/42-demo.md"
  digest="$(goal_ckpt digest .plans/42-demo.md)" || { fail "D6 fixture '$1': digest failed: $(cat "${goal_root}/stderr")"; return 1; }
  goal_ckpt init "$GOAL_REL" implement 42-demo planned --plan-path .plans/42-demo.md --plan-digest "$digest" >/dev/null \
    || { fail "D6 fixture '$1': init failed: $(cat "${goal_root}/stderr")"; return 1; }
}
# goal_step <label> <args...>: one checkpoint.mjs call that must succeed
goal_step(){
  local label="$1"; shift
  goal_ckpt "$@" >/dev/null || fail "D6 ${label}: 'checkpoint.mjs $1' failed: $(cat "${goal_root}/stderr")"
}
# goal_expect <label> <goal> <status> <phase> <gate>: the whole recorded state, so a drifted goal or a silent complete fails
goal_expect(){
  local actual
  actual="$(jq -r '[(.goal // "none"), .status, .phase, (.lastCompletedGate // "none")] | join(" ")' "${GOAL_REPO}/${GOAL_REL}" 2>/dev/null)"
  [[ "$actual" == "$2 $3 $4 $5" ]] || fail "D6 $1: expected state [$2 $3 $4 $5], got [${actual}]"
}
# goal_resume <label> <nextPhase>: an unfinished run must still pass verify and resume at <nextPhase>
goal_resume(){
  local out next
  out="$(goal_ckpt verify "$GOAL_REL" implement 42-demo)" || { fail "D6 $1: verify rejected the unfinished run: $(cat "${goal_root}/stderr")"; return; }
  next="$(printf '%s' "$out" | jq -r '.nextPhase' 2>/dev/null)"
  [[ "$next" == "$2" ]] || fail "D6 $1: verify nextPhase expected [$2], got [${next}]"
}
# goal_row_has <case> <phrase...>: the table row names the expected goal call and recorded outcome
goal_row_has(){
  local row phrase goal_case="$1"; shift
  row="$(goal_row "$runtime" "$goal_case")"
  for phrase in "$@"; do require_in "D6 Goal cases row '${goal_case}'" "$row" "$phrase"; done
}
goal_row_lacks_complete(){ forbid_in "D6 Goal cases row '$1' must make no completing call" "$(goal_row "$runtime" "$1")" 'update_goal'; }

# Shared run prefix: the create step's outcome rides on the first advance; the worktree/baseline advances then keep it.
goal_start(){ goal_repo "$1" && goal_step "$1 first advance" advance "$GOAL_REL" implement 42-demo worktree --goal "$2" \
  && goal_step "$1 baseline advance" advance "$GOAL_REL" implement 42-demo baseline --worktree .worktrees/42-demo --gate worktree; }

# existing goal: left unchanged, skipped-active recorded, the run continues and ends without any goal outcome change.
if goal_start existing-goal skipped-active; then
  goal_expect "existing goal after baseline" skipped-active running baseline worktree
  goal_step "existing goal pr advance" advance "$GOAL_REL" implement 42-demo pr --gate docs
  goal_step "existing goal complete" complete "$GOAL_REL" implement 42-demo pr --gate pr
  goal_expect "existing goal finished on checkpoint state alone" skipped-active complete pr pr
fi
goal_row_has 'existing goal' 'no goal call' '`skipped-active`' 'left unchanged'
goal_row_lacks_complete 'existing goal'
# no goal: created and recorded; the run is still running.
if goal_start no-goal created; then
  goal_expect "no goal after baseline" created running baseline worktree
fi
goal_row_has 'no goal' '`create_goal`' '`created`' 'Complete approved plan <path> through an open PR'
# no tools: the checkpoint-only path records unavailable and the run still finishes.
if goal_start no-tools unavailable; then
  goal_expect "no tools after baseline" unavailable running baseline worktree
  goal_step "no tools pr advance" advance "$GOAL_REL" implement 42-demo pr --gate docs
  goal_step "no tools complete" complete "$GOAL_REL" implement 42-demo pr --gate pr
  goal_expect "no tools finished on checkpoint state alone" unavailable complete pr pr
fi
goal_row_has 'no tools' 'no goal call' '`unavailable`' 'checkpoint-only recovery'
goal_row_lacks_complete 'no tools'
# ambiguity: blocked mid-run, goal as recorded, resumable, never complete.
if goal_start ambiguity created; then
  goal_step "ambiguity tests advance" advance "$GOAL_REL" implement 42-demo tests --gate baseline
  goal_step "ambiguity block" block "$GOAL_REL" implement 42-demo tests
  goal_expect "ambiguity" created needs-input tests baseline
  goal_resume "ambiguity" tests
fi
goal_row_has 'ambiguity' 'no goal call' '`needs-input`' 'left unchanged'
goal_row_lacks_complete 'ambiguity'
# red baseline: the goal already exists (created before the gate), the block keeps the worktree gate.
if goal_start red-baseline created; then
  goal_step "red baseline block" block "$GOAL_REL" implement 42-demo baseline
  goal_expect "red baseline" created needs-input baseline worktree
  goal_resume "red baseline" baseline
fi
goal_row_has 'red baseline' 'no goal call' '`needs-input`' 'left unchanged'
goal_row_lacks_complete 'red baseline'
# tool error: at create time no outcome is recorded (goal stays null); at complete time the created goal is kept.
if goal_repo tool-error-create; then
  goal_step "tool error at create block" block "$GOAL_REL" implement 42-demo worktree
  goal_expect "tool error at create" none needs-input worktree none
  goal_resume "tool error at create" worktree
fi
if goal_start tool-error-complete created; then
  goal_step "tool error at complete pr advance" advance "$GOAL_REL" implement 42-demo pr --gate docs
  goal_step "tool error at complete block" block "$GOAL_REL" implement 42-demo pr
  goal_expect "tool error at complete" created needs-input pr docs
  goal_resume "tool error at complete" pr
fi
goal_row_has 'tool error' 'no goal call' '`needs-input`' 'never downgraded to `unavailable`'
goal_row_lacks_complete 'tool error'
# budget exhaustion: blocked in implement, the unfinished state resumes at implement.
if goal_start budget-exhaustion created; then
  goal_step "budget exhaustion implement advance" advance "$GOAL_REL" implement 42-demo implement --gate tests
  goal_step "budget exhaustion block" block "$GOAL_REL" implement 42-demo implement
  goal_expect "budget exhaustion" created needs-input implement tests
  goal_resume "budget exhaustion" implement
fi
goal_row_has 'budget exhaustion' 'no goal call' '`needs-input`' 'left unchanged' '`Context: low`'
goal_row_lacks_complete 'budget exhaustion'
# PR success: the created goal completes only once the PR exists; the checkpoint is terminal and verify refuses to re-apply it.
if goal_start pr-success created; then
  goal_step "PR success pr advance" advance "$GOAL_REL" implement 42-demo pr --gate docs
  goal_step "PR success complete" complete "$GOAL_REL" implement 42-demo pr --gate pr
  goal_expect "PR success" created complete pr pr
  if goal_ckpt verify "$GOAL_REL" implement 42-demo >/dev/null; then
    fail "D6 PR success: verify must refuse a finished run"
  else
    [[ "$(cat "${goal_root}/stderr")" == *'checkpoint is complete (terminal)'* ]] || fail "D6 PR success: verify refusal must say the checkpoint is complete (terminal)"
  fi
fi
goal_row_has 'PR success' '`update_goal({status:"complete"})`' '`created`' 'only when' '`complete`'
# PR failure: a failed gh pr create blocks at pr, never completes, and resumes at pr.
if goal_start pr-failure created; then
  goal_step "PR failure pr advance" advance "$GOAL_REL" implement 42-demo pr --gate docs
  goal_step "PR failure block" block "$GOAL_REL" implement 42-demo pr
  goal_expect "PR failure" created needs-input pr docs
  goal_resume "PR failure" pr
fi
goal_row_has 'PR failure' 'no goal call' '`needs-input`' 'left unchanged'
goal_row_lacks_complete 'PR failure'

# D8: the docs describe the lifecycle and link the native evidence.
goal_evidence_link='codex-native-evidence.md#native-workflows-plan-mode-handoff-and-goal-lifecycle'
for phrase in '**Goal lifecycle**' "$goal_evidence_link" 'leaves it unchanged' \
  'no cenci automation (dispatch, babysit, ci-repair, babysit-attention) issues `apply`, so only a human-started `apply`, attended or not, creates a goal'; do
  require_in "D8 docs/codex.md goal lifecycle" "$codex_docs_all" "$phrase"
done
forbid_in "D8 docs/codex.md retired unattended-launch goal wording" "$codex_docs_all" 'An unattended launch never creates a goal'
require_in "D1 codex-runtime only a human-started apply creates a goal" "$(section "$runtime" '## Goal lifecycle')" \
  'No cenci automation (dispatch, babysit, `ci-repair`, `babysit-attention`) issues `apply`, so only a human-started `apply`, attended or not, creates a goal'
forbid_in "D1 codex-runtime retired unattended-launch goal wording" "$(section "$runtime" '## Goal lifecycle')" 'neither does any unattended launch'
forbid_in "D8 docs/codex.md retired goal wording" "$codex_docs_all" 'a native goal armed only after plan approval'
forbid_in "D8 docs/codex.md evidence pointer" "$codex_docs_all" 'tracked in #1235'
# The evidence section stays "pending native run" until a maintainer commits one, so no sentence may claim it is recorded there.
forbid_in "D8 docs/codex.md evidence not yet recorded" "$codex_docs_all" "recorded in [\`codex-native-evidence.md\`](${goal_evidence_link})"
require_in "D8 docs/codex.md evidence pending wording" "$codex_docs_all" "tracked in [\`codex-native-evidence.md\`](${goal_evidence_link}), pending the maintainer's native run"
require_in "D8 adapter-contract baseline-gate" "$(normalize "${ROOT}/docs/adapter-contract.md")" 'goal left unchanged (`codex-runtime` Goal lifecycle stop step)'

# Final review fixes: every restatement of the existing-goal rule carries the resumed-apply exception (#979).
goal_resume_exception='except this run'\''s own goal on a resumed `apply` (Create step 2)'
require_in "D2 codex-runtime existing goal row carries the resumed-apply exception" "$(goal_row "$runtime" 'existing goal')" "$goal_resume_exception"
require_in "D2 docs/codex.md existing goal bullet carries the resumed-apply exception" "$codex_docs_all" "$goal_resume_exception"
# docs/codex.md Complete bullet matches maintain: an unresolved UNKNOWN runs no complete step.
require_in "D4 docs/codex.md complete needs a confirmed mergeability" "$codex_docs_all" 'a mergeability check confirmed not `CONFLICTING` (an unresolved `UNKNOWN` runs no complete step)'
forbid_in "D4 docs/codex.md retired complete mergeability wording" "$codex_docs_all" 'a mergeability check that is not `CONFLICTING`'
# maintain: the unresolved-UNKNOWN checkpoint disposition (reference to the Goal lifecycle, not a restatement).
require_in "D4 maintain unresolved UNKNOWN checkpoint disposition" "$maintain_all" \
  'the checkpoint is left at `pr` with status `running` and the goal unchanged, so a later `apply` re-queries mergeability and runs the complete step if it is then not `CONFLICTING`'
# Native evidence judging rules for the create-then-complete row.
evidence_all="$(normalize "${ROOT}/docs/codex-native-evidence.md")"
for phrase in 'the objective must equal `Complete approved plan <path> through an open PR` exactly' \
  'any `token_budget` is a `FAIL`' \
  'only `update_goal` with `status: "complete"` counts as completion' \
  'an unreadable tool-call list (a jq or extraction failure, or a zero-call result with no rollout and no parsed event) is `inconclusive`, never ok'; do
  require_in "D7 evidence goal create-then-complete judging rule" "$evidence_all" "$phrase"
done
# docs/codex.md Create bullet: automation-launch wording, not the "unattended ... attended or not" contradiction.
require_in "D1 docs/codex.md create bullet names cenci automation" "$codex_docs_all" 'A launch started by cenci automation cannot create one: no cenci automation'
forbid_in "D1 docs/codex.md retired unattended-launch create wording" "$codex_docs_all" 'An unattended launch cannot create one'

count="$(find "${ROOT}/templates/codex/agent-roles" -name '*.toml' | wc -l)"
test "$count" -ge 5 || fail "Codex agent templates"
echo "workflows.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]]
