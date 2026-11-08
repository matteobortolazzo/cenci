#!/usr/bin/env bash
# runtime.test.sh -- native Codex runtime: checkpoint.mjs schema-v2 contract (#1233,
# B1-B8 and B11) plus the agent-install sequence. Every checkpoint case drives the
# real CLI inside a `git init` temp repo (the state root is the main checkout's
# .cenci/checkpoints) and compares bytes with `cmp`, so a rejected input is proven
# to leave the original file unchanged. Follows flow/docs/shell-scripting-gotchas.md:
# exact literals per case, never a generic marker.
set -uo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || { echo "runtime.test.sh: failed to resolve flow directory." >&2; exit 2; }
CKPT="$FLOW/codex/checkpoint.mjs"
T="$(mktemp -d)" || { echo "runtime.test.sh: mktemp failed." >&2; exit 2; }
trap 'rm -rf "$T"' EXIT
failures=0

DIGEST_A="9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
DIGEST_B="2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
REL=".cenci/checkpoints/implement-42.json"

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# run <cwd> <args...> -- runs the CLI from <cwd>; sets RC, writes $T/stdout and $T/stderr.
run() {
  local dir="$1"; shift
  ( cd "$dir" && node "$CKPT" "$@" ) >"$T/stdout" 2>"$T/stderr"
  RC=$?
}

# run_fault_mode <mode> <cwd> <args...> -- same, but with the test-only fault preload injecting
# <mode> (see fault.cjs below). A 10s timeout turns a hang into a failing exit code.
run_fault_mode() {
  local mode="$1" dir="$2"; shift 2
  ( cd "$dir" && CK_FAULT="$mode" timeout 10 node --require "$T/fault.cjs" "$CKPT" "$@" ) >"$T/stdout" 2>"$T/stderr"
  RC=$?
}
# run_fault <cwd> <args...> -- the rename fault.
run_fault() { run_fault_mode rename "$@"; }

expect_rc() { [[ "$RC" -eq "$2" ]] || fail "$1: expected exit $2, got $RC (stderr: $(cat "$T/stderr"))"; }
expect_no_stdout() { [[ ! -s "$T/stdout" ]] || fail "$1: stdout must be empty on this path (got: $(cat "$T/stdout"))"; }
expect_stderr() {
  local err
  err="$(cat "$T/stderr")"
  [[ "$err" == "checkpoint: "* ]] || fail "$1: stderr must start with 'checkpoint: ' (got: $err)"
  [[ "$err" == *"$2"* ]] || fail "$1: stderr must contain [$2] (got: $err)"
}
# The documented format is one `checkpoint: <file>: <problem>; <remedy>` line.
expect_single_remedy_line() {
  local err lines
  err="$(cat "$T/stderr")"
  lines="$(wc -l < "$T/stderr")"
  [[ "${lines//[[:space:]]/}" == "1" ]] || fail "$1: stderr must be exactly one line (got $lines lines: $err)"
  [[ "$err" == *"; "* ]] || fail "$1: stderr must carry a '; <remedy>' part (got: $err)"
}
expect_stderr_lacks() {
  local err
  err="$(cat "$T/stderr")"
  [[ "$err" != *"$2"* ]] || fail "$1: stderr must not contain [$2] (got: $err)"
}
expect_jq() { jq -e "$2" "$3" >/dev/null 2>&1 || fail "$1: jq assertion failed [$2] on $3: $(cat "$3" 2>/dev/null)"; }
expect_stdout_jq() { jq -e "$2" "$T/stdout" >/dev/null 2>&1 || fail "$1: stdout jq assertion failed [$2]: $(cat "$T/stdout")"; }

# reject <label> <file> <stderr-literal> <args...> -- the CLI must exit 2, print nothing
# on stdout, name the problem on stderr, and leave <file> byte-identical (or absent).
reject() {
  local label="$1" file="$2" literal="$3" existed=0; shift 3
  if [[ -f "$file" ]]; then
    existed=1
    cp "$file" "$T/snapshot" || { fail "$label: snapshot failed"; return; }
  fi
  run "$REPO" "$@"
  expect_rc "$label" 2
  expect_no_stdout "$label"
  expect_stderr "$label" "$literal"
  expect_single_remedy_line "$label"
  if [[ "$existed" -eq 1 ]]; then
    cmp -s "$T/snapshot" "$file" || fail "$label: file bytes changed after a rejected call"
  else
    { [[ ! -e "$file" && ! -L "$file" ]]; } || fail "$label: a file was created by a rejected call"
  fi
}

new_repo() {
  REPO="$(mktemp -d "$T/repo.XXXXXX")" || { echo "runtime.test.sh: mktemp failed." >&2; exit 2; }
  git init -q "$REPO" || { echo "runtime.test.sh: git init failed." >&2; exit 2; }
  CKDIR="$REPO/.cenci/checkpoints"
  CK="$CKDIR/implement-42.json"
}

# mutate <file> <jq-expr> -- rewrite a JSON fixture in place (the helper never wrote it).
mutate() {
  jq "$2" "$1" > "$T/mutated" && cp "$T/mutated" "$1" || fail "fixture mutation failed: $2"
}

# Test-only fault fixture for B7: a CommonJS preload that injects one fault, chosen by
# CK_FAULT. The production helper carries no fault hook.
#   rename          the atomic rename throws EIO
#   short-write     the state write reports fewer bytes than it was given
#   gitignore-write the .gitignore write fails after the file was created
#   lstat-plain     lstat of the checkpoint throws a non-errno error carrying secret text
#   lstat-errno     lstat of the checkpoint throws EACCES
#   stdout-epipe    the stdout write fails asynchronously with EPIPE
#   lstat-fifo      lstat of a FIFO checkpoint reports a regular file (the FIFO swapped in after the check)
cat > "$T/fault.cjs" <<'EOF'
const fs = require("node:fs");
const { syncBuiltinESMExports } = require("node:module");
const mode = process.env.CK_FAULT;
const errno = (message, code, syscall) => Object.assign(new Error(message), { code, syscall });
const isCheckpoint = (p) => typeof p === "string" && p.endsWith("implement-42.json");
if (mode === "rename") {
  fs.renameSync = () => { throw errno("injected rename failure", "EIO", "rename"); };
}
if (mode === "short-write") {
  const real = fs.writeSync;
  fs.writeSync = (fd, data, ...rest) => {
    if (typeof data === "string" && data.startsWith("{")) {
      real(fd, data.slice(0, 10));
      return 10;
    }
    return real(fd, data, ...rest);
  };
}
if (mode === "gitignore-write") {
  const real = fs.writeSync;
  fs.writeSync = (fd, data, ...rest) => {
    if (data === "*\n") {
      real(fd, "*");
      throw errno("injected gitignore write failure", "EIO", "write");
    }
    return real(fd, data, ...rest);
  };
}
if (mode === "lstat-plain" || mode === "lstat-errno") {
  const real = fs.lstatSync;
  fs.lstatSync = (p, ...rest) => {
    if (!isCheckpoint(p)) return real(p, ...rest);
    if (mode === "lstat-plain") throw new Error("secret-file-content-xyz");
    throw errno("injected", "EACCES", "lstat");
  };
}
if (mode === "lstat-fifo") {
  const real = fs.lstatSync;
  fs.lstatSync = (p, ...rest) => (isCheckpoint(p) ? real(require("node:path").join(require("node:path").dirname(p), ".gitignore")) : real(p, ...rest));
}
if (mode === "stdout-epipe") {
  process.stdout.write = () => {
    process.nextTick(() => process.stdout.emit("error", errno("write EPIPE", "EPIPE", "write")));
    return true;
  };
}
syncBuiltinESMExports();
EOF

# ===========================================================================
# B11 -- the original init/advance/block/clear + agent-install sequence, adjusted
# only to the v2 contract (git repo, identity on clear, schemaVersion 2).
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42 planned; expect_rc "B11 init" 0
run "$REPO" advance "$REL" implement 42 worktree; expect_rc "B11 advance" 0
expect_jq "B11 advance state" '.schemaVersion == 2 and .phase == "worktree" and .status == "running"' "$CK"
run "$REPO" block "$REL" implement 42 review; expect_rc "B11 block" 0
expect_jq "B11 block state" '.status == "needs-input"' "$CK"
AGENT_ROOT="$T/agent-root"
mkdir -p "$AGENT_ROOT" || fail "B11: mkdir agent root failed"
PLUGIN_ROOT="$FLOW" sh "$FLOW/codex/install-agents.sh" "$AGENT_ROOT" || fail "B11: install-agents.sh failed"
[[ "$(find "$AGENT_ROOT/.codex/agents" -name '*.toml' | wc -l)" -ge 5 ]] || fail "B11: fewer than 5 agent roles installed"
# Schema validation (#1040): each directory validated on its own, not
# combined -- .codex/agents/ is populated by copying the templates verbatim,
# so the same role `name`s legitimately appear in both; combining them would
# make every clean checkout report a false-positive duplicate.
bash "$FLOW/codex/validate-agent-roles.sh" "$FLOW/templates/codex/agent-roles" || fail "B11: template agent roles invalid"
REPO_ROOT="$(cd "$FLOW/.." && pwd)" || { echo "runtime.test.sh: failed to resolve repo root." >&2; exit 2; }
bash "$FLOW/codex/validate-agent-roles.sh" "$REPO_ROOT/.codex/agents" || fail "B11: repo agent roles invalid"
printf 'user-owned\n' > "$AGENT_ROOT/.codex/agents/planner.toml"
PLUGIN_ROOT="$FLOW" sh "$FLOW/codex/install-agents.sh" "$AGENT_ROOT" || fail "B11: second install-agents.sh failed"
grep -q 'user-owned' "$AGENT_ROOT/.codex/agents/planner.toml" || fail "B11: install-agents.sh overwrote a user-owned role"
run "$REPO" clear "$REL" implement 42; expect_rc "B11 clear" 0
{ [[ ! -e "$CK" ]]; } || fail "B11: clear left the checkpoint behind"

# ===========================================================================
# State root creation: first init creates .cenci/checkpoints (0700) with a
# .gitignore of `*`, so a checkpoint never shows up in `git status`.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "root init" 0
[[ "$(cat "$CKDIR/.gitignore" 2>/dev/null)" == "*" ]] || fail "root: .gitignore must contain exactly '*'"
[[ "$(ls -ld "$CKDIR")" == drwx------* ]] || fail "root: .cenci/checkpoints must be mode 0700 (got: $(ls -ld "$CKDIR"))"
[[ -z "$(git -C "$REPO" status --porcelain)" ]] || fail "root: checkpoint state must be git-ignored (status: $(git -C "$REPO" status --porcelain))"
expect_jq "root: fresh init state" '.schemaVersion == 2 and .phase == "planned" and .status == "planned" and .planPath == null and .planDigest == null and .worktree == null and .lastCompletedGate == null and .agentFallbacks == [] and .goal == null and (keys | length) == 12' "$CK"
expect_stdout_jq "root: init prints the state" '.workflow == "implement" and .target == "42"'

# ===========================================================================
# B1 -- rejection cases: exit 2, empty stdout, actionable stderr, bytes unchanged.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42 planned; expect_rc "B1 setup init" 0
run "$REPO" advance "$REL" implement 42 baseline --gate worktree; expect_rc "B1 setup advance" 0
cp "$CK" "$T/valid.json" || fail "B1: could not save the valid fixture"
restore() { cp "$T/valid.json" "$CK" || fail "B1: restore failed"; }

# unknown schema version
restore; mutate "$CK" '.schemaVersion = 3'
reject "B1 unknown schema: get" "$CK" "unknown schemaVersion" get "$REL"
reject "B1 unknown schema: advance" "$CK" "unknown schemaVersion" advance "$REL" implement 42 tests
reject "B1 unknown schema: init" "$CK" "unknown schemaVersion" init "$REL" implement 42
reject "B1 unknown schema: clear without --force" "$CK" "re-run with --force" clear "$REL" implement 42

# malformed JSON
restore; printf '{"schemaVersion": 2, "phase": ' > "$CK"
reject "B1 malformed: get" "$CK" "malformed JSON" get "$REL"
reject "B1 malformed: advance" "$CK" "malformed JSON" advance "$REL" implement 42 tests
reject "B1 malformed: init" "$CK" "malformed JSON" init "$REL" implement 42
reject "B1 malformed: block" "$CK" "malformed JSON" block "$REL" implement 42 tests
reject "B1 malformed: complete" "$CK" "malformed JSON" complete "$REL" implement 42 tests
expect_stderr_lacks "B1 malformed: no echoed file content" 'schemaVersion'

# wrong workflow / wrong target on the command line (path does not carry that identity)
restore
reject "B1 wrong workflow: advance" "$CK" "does not match workflow and target" advance "$REL" maintain 42 fix
reject "B1 wrong target: advance" "$CK" "does not match workflow and target" advance "$REL" implement 43 tests
reject "B1 wrong target: get" "$CK" "does not match workflow and target" get "$REL" implement 43
reject "B1 wrong target: block" "$CK" "does not match workflow and target" block "$REL" implement 43 tests
reject "B1 wrong target: complete" "$CK" "does not match workflow and target" complete "$REL" implement 43
# wrong identity recorded inside the file (the file name says 42, the content says 43 / maintain)
restore; mutate "$CK" '.target = "43"'
reject "B1 file records other target: advance" "$CK" "records a different workflow or target" advance "$REL" implement 42 tests
reject "B1 file records other target: get" "$CK" "records a different workflow or target" get "$REL"
restore; mutate "$CK" '.workflow = "maintain" | .phase = "fix"'
reject "B1 file records other workflow: advance" "$CK" "records a different workflow or target" advance "$REL" implement 42 tests

# invalid phase / status values
restore
reject "B1 invalid phase argument" "$CK" "is not a phase of workflow" advance "$REL" implement 42 nosuchphase
reject "B1 phase of another workflow" "$CK" "is not a phase of workflow" advance "$REL" implement 42 fix
reject "B1 unknown workflow" "$REPO/.cenci/checkpoints/nosuchworkflow-42.json" "unknown workflow" init "$REL" nosuchworkflow 42
restore; mutate "$CK" '.status = "bogus"'
reject "B1 invalid status in file: get" "$CK" "invalid field status" get "$REL"
reject "B1 invalid status in file: advance" "$CK" "invalid field status" advance "$REL" implement 42 tests
restore; mutate "$CK" '.phase = "nosuchphase"'
reject "B1 invalid phase in file: get" "$CK" "invalid field phase" get "$REL"
restore; mutate "$CK" '.extra = 1'
reject "B1 extra field in file" "$CK" "unexpected or missing fields" get "$REL"
restore; mutate "$CK" 'del(.goal)'
reject "B1 missing field in file" "$CK" "unexpected or missing fields" get "$REL"
restore; mutate "$CK" '.updatedAt = "yesterday"'
reject "B1 invalid updatedAt in file" "$CK" "invalid field updatedAt" get "$REL"

# illegal phase/status transitions
restore
reject "B1 advance to planned" "$CK" "cannot target the planned phase" advance "$REL" implement 42 planned
reject "B1 block to planned" "$CK" "cannot target the planned phase" block "$REL" implement 42 planned
reject "B1 advance to the gate phase" "$CK" "must come after the last completed gate" advance "$REL" implement 42 worktree
reject "B1 advance before the gate" "$CK" "must come after the last completed gate" block "$REL" implement 42 worktree
reject "B1 advance without a phase" "$CK" "usage:" advance "$REL" implement 42
run "$REPO" complete "$REL" implement 42 baseline; expect_rc "B1 complete (setup for terminal)" 0
expect_jq "B1 complete state" '.status == "complete" and .phase == "baseline"' "$CK"
reject "B1 advance after complete" "$CK" "checkpoint is complete (terminal)" advance "$REL" implement 42 tests
reject "B1 block after complete" "$CK" "checkpoint is complete (terminal)" block "$REL" implement 42 tests
reject "B1 complete after complete" "$CK" "checkpoint is complete (terminal)" complete "$REL" implement 42 tests

# usage errors
restore
reject "B1 unknown command" "$CK" "unknown command" frobnicate "$REL" implement 42
reject "B1 unknown flag" "$CK" "unknown flag" advance "$REL" implement 42 tests --nonsense x
reject "B1 misplaced flag: --force on advance" "$CK" "not valid for advance" advance "$REL" implement 42 tests --force
reject "B1 misplaced flag: metadata on get" "$CK" "not valid for get" get "$REL" --goal created
reject "B1 flag without value" "$CK" "requires a value" advance "$REL" implement 42 tests --worktree
reject "B1 duplicate flag" "$CK" "given more than once" advance "$REL" implement 42 tests --goal created --goal unavailable
reject "B1 clear without identity" "$CK" "usage:" clear "$REL"

# ===========================================================================
# B2 -- init is retry-safe: compatible state is a no-op, incompatible state exits 2.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42 planned; expect_rc "B2 first init" 0
cp "$T/stdout" "$T/init-1.out"; cp "$CK" "$T/init-1.file"
run "$REPO" init "$REL" implement 42 planned; expect_rc "B2 repeated init" 0
cmp -s "$T/stdout" "$T/init-1.out" || fail "B2: repeated init must print byte-identical state"
cmp -s "$CK" "$T/init-1.file" || fail "B2: repeated init must leave the file byte-identical"
run "$REPO" advance "$REL" implement 42 worktree --plan-path .plans/42-demo.md --plan-digest "$DIGEST_A" --worktree .worktrees/42-demo --gate worktree
expect_rc "B2 advance" 0
cp "$CK" "$T/advanced.file"
run "$REPO" init "$REL" implement 42 planned; expect_rc "B2 init after advance" 0
cmp -s "$CK" "$T/advanced.file" || fail "B2: init after progress must not reset the checkpoint"
expect_stdout_jq "B2 init after advance reports existing state" '.phase == "worktree" and .status == "running" and .lastCompletedGate == "worktree"'
run "$REPO" init "$REL" implement 42 planned --plan-path .plans/42-demo.md --plan-digest "$DIGEST_A"; expect_rc "B2 init with matching digest" 0
cmp -s "$CK" "$T/advanced.file" || fail "B2: init with a matching digest must not rewrite the file"
run "$REPO" init "$REL" implement 42 planned --worktree .worktrees/other --goal created; expect_rc "B2 init ignores metadata flags on a compatible file" 0
cmp -s "$CK" "$T/advanced.file" || fail "B2: init must ignore metadata flags on a compatible checkpoint"
# incompatible: different digest
reject "B2 init with a different digest" "$CK" "plan path or digest differs" init "$REL" implement 42 planned --plan-path .plans/42-demo.md --plan-digest "$DIGEST_B"
reject "B2 init with a different plan path" "$CK" "plan path or digest differs" init "$REL" implement 42 planned --plan-path .plans/other.md --plan-digest "$DIGEST_A"
# incompatible: file records another target identity
mutate "$CK" '.target = "43"'
reject "B2 init over a file that records another target" "$CK" "records a different workflow or target" init "$REL" implement 42 planned
# incompatible: existing state carries no digest but one is requested
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "B2 null-digest setup" 0
reject "B2 init with a digest over a null-digest checkpoint" "$CK" "records no plan digest" init "$REL" implement 42 planned --plan-path .plans/42-demo.md --plan-digest "$DIGEST_A"

# ===========================================================================
# B3 -- real CLI round-trip of the metadata fields.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42 planned; expect_rc "B3 init" 0
run "$REPO" advance "$REL" implement 42 baseline --worktree .worktrees/42-demo --plan-path .plans/42-demo.md --plan-digest "$DIGEST_A" --gate worktree --fallback planner:default --fallback reviewer:worker-a --goal created
expect_rc "B3 advance with metadata" 0
expect_stdout_jq "B3 advance prints the written state" '.planDigest == "'"$DIGEST_A"'" and .goal == "created"'
run "$REPO" get "$REL"; expect_rc "B3 get" 0
expect_stdout_jq "B3 round-trip" '.schemaVersion == 2
  and .workflow == "implement" and .target == "42" and .phase == "baseline" and .status == "running"
  and .planPath == ".plans/42-demo.md" and .planDigest == "'"$DIGEST_A"'"
  and .worktree == ".worktrees/42-demo" and .lastCompletedGate == "worktree"
  and .agentFallbacks == [{"role":"planner","worker":"default"},{"role":"reviewer","worker":"worker-a"}]
  and .goal == "created" and (keys | length) == 12 and (has("legacy") | not)'
[[ "$(jq -S . "$CK")" == "$(jq -S . "$T/stdout")" ]] || fail "B3: the file on disk must hold exactly the state that get prints"
expect_jq "B3 updatedAt is an ISO timestamp" '.updatedAt | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z$")' "$CK"
# fallbacks merge by role across calls; block preserves metadata
run "$REPO" advance "$REL" implement 42 tests --fallback planner:codex-worker --fallback implementer:worker-b; expect_rc "B3 merge fallbacks" 0
run "$REPO" block "$REL" implement 42 review; expect_rc "B3 block" 0
run "$REPO" get "$REL"; expect_rc "B3 get after block" 0
expect_stdout_jq "B3 merge by role, block preserves metadata" '.status == "needs-input" and .phase == "review"
  and .agentFallbacks == [{"role":"planner","worker":"codex-worker"},{"role":"reviewer","worker":"worker-a"},{"role":"implementer","worker":"worker-b"}]
  and .worktree == ".worktrees/42-demo" and .planPath == ".plans/42-demo.md" and .lastCompletedGate == "worktree" and .goal == "created"'
for goal in skipped-active unavailable; do
  run "$REPO" advance "$REL" implement 42 implement --goal "$goal"; expect_rc "B3 goal $goal" 0
  run "$REPO" get "$REL"
  expect_stdout_jq "B3 goal $goal round-trip" '.goal == "'"$goal"'"'
done
# invalid metadata is rejected and never written
cp "$CK" "$T/b3.file"
reject "B3 digest not hex" "$CK" "invalid --plan-digest" advance "$REL" implement 42 docs --plan-path .plans/x.md --plan-digest "ZZ$DIGEST_A"
reject "B3 digest wrong length" "$CK" "invalid --plan-digest" advance "$REL" implement 42 docs --plan-path .plans/x.md --plan-digest abc123
reject "B3 uppercase digest" "$CK" "invalid --plan-digest" advance "$REL" implement 42 docs --plan-path .plans/x.md --plan-digest "$(printf '%s' "$DIGEST_A" | tr 'a-f' 'A-F')"
reject "B3 plan path without digest" "$CK" "must be given together" advance "$REL" implement 42 docs --plan-path .plans/x.md
reject "B3 digest without plan path" "$CK" "must be given together" advance "$REL" implement 42 docs --plan-digest "$DIGEST_A"
reject "B3 absolute worktree" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree /abs/.worktrees/42
reject "B3 traversing worktree" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree ../escape
reject "B3 non-normalized worktree" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree .worktrees//42-demo
reject "B3 absolute plan path" "$CK" "invalid --plan-path" advance "$REL" implement 42 docs --plan-path /etc/passwd --plan-digest "$DIGEST_A"
reject "B3 traversing plan path" "$CK" "invalid --plan-path" advance "$REL" implement 42 docs --plan-path .plans/../x.md --plan-digest "$DIGEST_A"
reject "B3 invalid goal" "$CK" "invalid --goal" advance "$REL" implement 42 docs --goal done
reject "B3 fallback without colon" "$CK" "invalid --fallback" advance "$REL" implement 42 docs --fallback planner
reject "B3 fallback with uppercase role" "$CK" "invalid --fallback" advance "$REL" implement 42 docs --fallback Planner:worker
reject "B3 fallback with extra colon" "$CK" "invalid --fallback" advance "$REL" implement 42 docs --fallback planner:a:b
reject "B3 unknown gate" "$CK" "invalid --gate" advance "$REL" implement 42 docs --gate nosuchphase
reject "B3 worktree inside .git" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree .git/hooks
reject "B3 worktree that is .git" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree .git
reject "B3 plan path inside .git" "$CK" "invalid --plan-path" advance "$REL" implement 42 docs --plan-path .git/config --plan-digest "$DIGEST_A"
reject "B3 worktree with a trailing slash" "$CK" "invalid --worktree" advance "$REL" implement 42 docs --worktree .worktrees/x/
reject "B3 plan path with a trailing slash" "$CK" "invalid --plan-path" advance "$REL" implement 42 docs --plan-path .plans/x.md/ --plan-digest "$DIGEST_A"
cmp -s "$CK" "$T/b3.file" || fail "B3: rejected metadata changed the file"
run "$REPO" advance "$REL" implement 42 docs --worktree .github/work --plan-path .gitignore --plan-digest "$DIGEST_A"; expect_rc "B3 .git-prefixed names that are not .git stay valid" 0

# The frozen per-workflow phase tables: every workflow walks its full table in order.
walk_workflow() {
  local wf="$1" target="$2" phase file=".cenci/checkpoints/$1-$2.json"; shift 2
  run "$REPO" init "$file" "$wf" "$target"; expect_rc "tables $wf init" 0
  for phase in "$@"; do
    run "$REPO" advance "$file" "$wf" "$target" "$phase" --gate "$phase"; expect_rc "tables $wf advance $phase" 0
  done
  run "$REPO" complete "$file" "$wf" "$target"; expect_rc "tables $wf complete" 0
  run "$REPO" get "$file" "$wf" "$target"
  expect_stdout_jq "tables $wf final state" '.status == "complete" and .phase == "'"${!#}"'" and .lastCompletedGate == "'"${!#}"'"'
}
new_repo
walk_workflow implement 1 worktree baseline tests implement refactor review docs pr
walk_workflow maintain 2 worktree baseline fix review pr
walk_workflow refactor 3 worktree baseline refactor review pr
walk_workflow address-review 4 worktree fix verify push
walk_workflow configure 5 apply pr
walk_workflow refine 6 apply
walk_workflow review 7 review post
walk_workflow sync 8 sync
reject "tables: configure has no review phase" "$REPO/.cenci/checkpoints/configure-9.json" "is not a phase of workflow" advance ".cenci/checkpoints/configure-9.json" configure 9 review

# ===========================================================================
# B4 -- get / clear validation and missing-state behavior.
# ===========================================================================
new_repo
run "$REPO" get "$REL"; expect_rc "B4 get with no state root" 3; expect_no_stdout "B4 get with no state root"; expect_stderr "B4 get with no state root" "no checkpoint found"
run "$REPO" advance "$REL" implement 42 tests; expect_rc "B4 advance with no state root" 3; expect_no_stdout "B4 advance with no state root"
run "$REPO" block "$REL" implement 42 tests; expect_rc "B4 block with no state root" 3
run "$REPO" complete "$REL" implement 42; expect_rc "B4 complete with no state root" 3
run "$REPO" clear "$REL" implement 42; expect_rc "B4 clear with no state root" 0; expect_no_stdout "B4 clear with no state root"
{ [[ ! -e "$REPO/.cenci" ]]; } || fail "B4: get/clear/advance must never create the state root"
run "$REPO" init "$REL" implement 42; expect_rc "B4 init" 0
rm -f "$CK" || fail "B4: could not remove the checkpoint"
run "$REPO" get "$REL"; expect_rc "B4 get with the root but no file" 3; expect_no_stdout "B4 get with the root but no file"
run "$REPO" clear "$REL" implement 42; expect_rc "B4 clear with the root but no file" 0
# malformed file: refused without --force, removed with it
printf 'not json at all' > "$CK"
reject "B4 clear refuses a malformed file" "$CK" "re-run with --force" clear "$REL" implement 42
run "$REPO" clear "$REL" implement 42 --force; expect_rc "B4 clear --force malformed" 0
expect_stderr "B4 clear --force reports the removal" "removed unreadable checkpoint"
expect_no_stdout "B4 clear --force malformed"
{ [[ ! -e "$CK" ]]; } || fail "B4: clear --force must remove a malformed file"
printf '{"schemaVersion": 9}' > "$CK"
reject "B4 clear refuses an unknown-schema file" "$CK" "re-run with --force" clear "$REL" implement 42
run "$REPO" clear "$REL" implement 42 --force; expect_rc "B4 clear --force unknown schema" 0
{ [[ ! -e "$CK" ]]; } || fail "B4: clear --force must remove an unknown-schema file"
# identity checks on clear
run "$REPO" init "$REL" implement 42; expect_rc "B4 re-init" 0
reject "B4 clear with the wrong target" "$CK" "does not match workflow and target" clear "$REL" implement 43
reject "B4 clear with the wrong workflow" "$CK" "does not match workflow and target" clear "$REL" maintain 42
mutate "$CK" '.target = "43"'
reject "B4 clear over a file that records another target" "$CK" "records a different workflow or target" clear "$REL" implement 42 --force
# clear cannot remove that file (it fails on the same identity check), so the remedy must say to
# remove it by hand rather than point back at clear.
expect_stderr "B4 identity-mismatch remedy is a by-hand removal" "remove the file by hand if it is stale"
expect_stderr_lacks "B4 identity-mismatch remedy must not be circular" "checkpoint.mjs clear"
rm -f "$CK"; run "$REPO" init "$REL" implement 42; expect_rc "B4 init again" 0
run "$REPO" clear "$REL" implement 42; expect_rc "B4 clear a valid checkpoint" 0
{ [[ ! -e "$CK" ]]; } || fail "B4: clear must remove a valid checkpoint"
run "$REPO" init "$REL" implement 42; expect_rc "B4 init after clear" 0
# a complete checkpoint stays terminal until cleared
run "$REPO" complete "$REL" implement 42; expect_rc "B4 complete" 0
run "$REPO" clear "$REL" implement 42; expect_rc "B4 clear a complete checkpoint" 0
run "$REPO" init "$REL" implement 42; expect_rc "B4 init resets only through clear" 0
expect_stdout_jq "B4 fresh state after clear" '.status == "planned" and .phase == "planned"'

# ===========================================================================
# B5 -- containment: outside the root, '..', symlinked file, symlinked state dirs.
# ===========================================================================
new_repo
mkdir -p "$T/elsewhere" || fail "B5: mkdir failed"
reject "B5 absolute path outside the root: init" "$T/elsewhere/implement-42.json" "outside the checkpoint root" init "$T/elsewhere/implement-42.json" implement 42
reject "B5 absolute path outside the root: get" "$T/elsewhere/implement-42.json" "outside the checkpoint root" get "$T/elsewhere/implement-42.json"
reject "B5 relative path outside the root" "$REPO/implement-42.json" "outside the checkpoint root" init "implement-42.json" implement 42
reject "B5 path in .cenci but not in checkpoints" "$REPO/.cenci/implement-42.json" "outside the checkpoint root" init ".cenci/implement-42.json" implement 42
reject "B5 nested below the root" "$CKDIR/sub/implement-42.json" "outside the checkpoint root" init ".cenci/checkpoints/sub/implement-42.json" implement 42
reject "B5 '..' traversal that lands inside the root" "$CK" "'..' path segment" init ".cenci/checkpoints/../checkpoints/implement-42.json" implement 42
reject "B5 '..' traversal out of the root" "$T/elsewhere/implement-42.json" "'..' path segment" init ".cenci/checkpoints/../../../elsewhere/implement-42.json" implement 42
reject "B5 '..' in an absolute path" "$CK" "'..' path segment" get "$CKDIR/../checkpoints/implement-42.json"
reject "B5 '..' on clear" "$CK" "'..' path segment" clear ".cenci/checkpoints/../checkpoints/implement-42.json" implement 42
{ [[ ! -e "$REPO/.cenci" ]]; } || fail "B5: a rejected path must not create the state root"

# symlinked checkpoint file
run "$REPO" init "$REL" implement 42; expect_rc "B5 setup init" 0
rm -f "$CK" || fail "B5: could not remove the checkpoint"
printf '{"outside":"untouched"}\n' > "$T/outside.json"
ln -s "$T/outside.json" "$CK" || fail "B5: ln failed"
cp "$T/outside.json" "$T/outside.snapshot"
for cmd in "get $REL" "init $REL implement 42" "advance $REL implement 42 tests" "block $REL implement 42 tests" "complete $REL implement 42" "clear $REL implement 42" "clear $REL implement 42 --force"; do
  # shellcheck disable=SC2086
  reject "B5 symlinked file: ${cmd%% *}" "$CK" "not a regular file" $cmd
  cmp -s "$T/outside.json" "$T/outside.snapshot" || fail "B5 symlinked file: ${cmd%% *} modified the symlink target"
  [[ -L "$CK" ]] || fail "B5 symlinked file: ${cmd%% *} replaced or removed the symlink"
done
rm -f "$CK"

# symlinked .cenci/checkpoints pointing outside
new_repo
mkdir -p "$REPO/.cenci" "$T/outside-dir" || fail "B5: mkdir failed"
ln -s "$T/outside-dir" "$CKDIR" || fail "B5: ln failed"
reject "B5 symlinked checkpoints dir: init" "$T/outside-dir/implement-42.json" "not a plain directory" init "$REL" implement 42
reject "B5 symlinked checkpoints dir: get" "$T/outside-dir/implement-42.json" "not a plain directory" get "$REL"
reject "B5 symlinked checkpoints dir: clear" "$T/outside-dir/implement-42.json" "not a plain directory" clear "$REL" implement 42
[[ -z "$(ls -A "$T/outside-dir")" ]] || fail "B5: a symlinked checkpoints dir received files: $(ls -A "$T/outside-dir")"

# symlinked .cenci pointing outside
new_repo
mkdir -p "$T/outside-cenci/checkpoints" || fail "B5: mkdir failed"
ln -s "$T/outside-cenci" "$REPO/.cenci" || fail "B5: ln failed"
reject "B5 symlinked .cenci: init" "$T/outside-cenci/checkpoints/implement-42.json" "not a plain directory" init "$REL" implement 42
reject "B5 symlinked .cenci: get" "$T/outside-cenci/checkpoints/implement-42.json" "not a plain directory" get "$REL"
[[ -z "$(ls -A "$T/outside-cenci/checkpoints")" ]] || fail "B5: a symlinked .cenci received files"

# resolution: relative paths resolve against the main checkout, from a subdirectory or a worktree
new_repo
git -C "$REPO" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false commit -q --allow-empty -m init || fail "B5: seed commit failed"
mkdir -p "$REPO/sub/dir" || fail "B5: mkdir failed"
git -C "$REPO" worktree add -q "$REPO/.worktrees/42-demo" -b feature-42 || fail "B5: worktree add failed"
run "$REPO/sub/dir" init "$REL" implement 42; expect_rc "B5 init from a subdirectory" 0
{ [[ -f "$CK" ]]; } || fail "B5: init from a subdirectory must write to the main checkout root"
{ [[ ! -e "$REPO/sub/dir/.cenci" ]]; } || fail "B5: init from a subdirectory must not create .cenci there"
run "$REPO/.worktrees/42-demo" advance "$REL" implement 42 worktree --worktree .worktrees/42-demo --gate worktree; expect_rc "B5 advance from a worktree" 0
{ [[ ! -e "$REPO/.worktrees/42-demo/.cenci" ]]; } || fail "B5: a worktree must not get its own checkpoint root"
run "$REPO/.worktrees/42-demo" get "$CK"; expect_rc "B5 get from a worktree by absolute path" 0
expect_stdout_jq "B5 worktree view of the main checkpoint" '.phase == "worktree" and .worktree == ".worktrees/42-demo"'
# a bare directory outside any repository
mkdir -p "$T/norepo" || fail "B5: mkdir failed"
GIT_CEILING_DIRECTORIES="$T" run "$T/norepo" init "$REL" implement 42
expect_rc "B5 outside a git repository" 2; expect_no_stdout "B5 outside a git repository"; expect_stderr "B5 outside a git repository" "not inside a git repository"
# a bare repository has no main checkout
git init -q --bare "$T/bare.git" || fail "B5: git init --bare failed"
run "$T/bare.git" init "$REL" implement 42
expect_rc "B5 bare repository" 2; expect_no_stdout "B5 bare repository"; expect_stderr "B5 bare repository" "unsupported repository layout"

# ===========================================================================
# B6 -- legacy v1 checkpoints.
# ===========================================================================
new_repo
mkdir -p "$CKDIR" || fail "B6: mkdir failed"
write_v1() {
  cat > "$CK" <<EOF
{"schemaVersion":1,"workflow":"implement","target":"42","phase":"worktree","status":"running","planPath":null,"worktree":null,"lastCompletedGate":null,"agentFallbacks":[],"updatedAt":"2026-01-01T00:00:00.000Z"}
EOF
}
write_v1
run "$REPO" get "$REL"; expect_rc "B6 get v1" 0
expect_stdout_jq "B6 get marks v1 legacy" '.legacy == true and .schemaVersion == 1 and .phase == "worktree"'
reject "B6 advance rejects v1" "$CK" "legacy v1 checkpoint; run init to upgrade" advance "$REL" implement 42 baseline
reject "B6 block rejects v1" "$CK" "legacy v1 checkpoint; run init to upgrade" block "$REL" implement 42 baseline
reject "B6 complete rejects v1" "$CK" "legacy v1 checkpoint; run init to upgrade" complete "$REL" implement 42 baseline
reject "B6 init with another target over v1" "$CK" "does not match workflow and target" init "$REL" implement 43
run "$REPO" init "$REL" implement 42; expect_rc "B6 init upgrades v1" 0
expect_jq "B6 upgraded in place" '.schemaVersion == 2 and .phase == "worktree" and .status == "running" and .planDigest == null and .goal == null and .agentFallbacks == [] and (keys | length) == 12' "$CK"
run "$REPO" get "$REL"; expect_rc "B6 get after upgrade" 0
expect_stdout_jq "B6 upgraded state is no longer legacy" '(has("legacy") | not) and .schemaVersion == 2'
cp "$CK" "$T/upgraded.file"
run "$REPO" init "$REL" implement 42; expect_rc "B6 init after upgrade is a no-op" 0
cmp -s "$CK" "$T/upgraded.file" || fail "B6: init on the upgraded checkpoint must be a no-op"
run "$REPO" advance "$REL" implement 42 baseline; expect_rc "B6 advance works after upgrade" 0
# upgrade applies metadata flags and relativizes an absolute worktree inside the checkout
write_v1; mutate "$CK" '.worktree = "'"$REPO"'/.worktrees/42-demo" | .planPath = ".plans/42-demo.md"'
run "$REPO" init "$REL" implement 42 --plan-path .plans/42-demo.md --plan-digest "$DIGEST_A"; expect_rc "B6 upgrade with metadata" 0
expect_jq "B6 upgrade relativizes and applies flags" '.worktree == ".worktrees/42-demo" and .planPath == ".plans/42-demo.md" and .planDigest == "'"$DIGEST_A"'"' "$CK"
# non-upgradable v1 files are rejected, never silently mapped
write_v1; mutate "$CK" '.worktree = "/somewhere/else/.worktrees/42-demo"'
reject "B6 v1 absolute worktree outside the checkout" "$CK" "cannot be upgraded" init "$REL" implement 42
write_v1; mutate "$CK" '.phase = "flow"'
reject "B6 v1 unknown phase names clear" "$CK" "cannot be upgraded" init "$REL" implement 42
expect_stderr "B6 v1 unknown phase remedy" "remove it with: checkpoint.mjs clear <path> <workflow> <target>, then init again"
write_v1; mutate "$CK" '.status = "stalled"'
reject "B6 v1 unknown status" "$CK" "cannot be upgraded" init "$REL" implement 42
write_v1; mutate "$CK" '.lastCompletedGate = "flow"'
reject "B6 v1 unknown gate" "$CK" "cannot be upgraded" init "$REL" implement 42
write_v1; mutate "$CK" '.agentFallbacks = ["planner"]'
reject "B6 v1 string fallbacks" "$CK" "cannot be upgraded" init "$REL" implement 42
write_v1; mutate "$CK" '.workflow = 7'
reject "B6 v1 non-string identity" "$CK" "invalid field workflow" get "$REL"
# clear removes v1 (identity checked), including a non-upgradable one
write_v1; mutate "$CK" '.phase = "flow"'
run "$REPO" clear "$REL" implement 42; expect_rc "B6 clear removes v1" 0
{ [[ ! -e "$CK" ]]; } || fail "B6: clear must remove a legacy v1 checkpoint"
write_v1; mutate "$CK" '.target = "43"'
reject "B6 clear v1 with another recorded target" "$CK" "records a different workflow or target" clear "$REL" implement 42

# ===========================================================================
# B7 -- a failure before the atomic rename leaves the previous state readable.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "B7 setup init" 0
run "$REPO" advance "$REL" implement 42 worktree --worktree .worktrees/42-demo --gate worktree; expect_rc "B7 setup advance" 0
cp "$CK" "$T/b7.file"
run "$REPO" get "$REL"; cp "$T/stdout" "$T/b7.get"
for cmd in "advance $REL implement 42 baseline" "block $REL implement 42 baseline" "complete $REL implement 42 worktree"; do
  # shellcheck disable=SC2086
  run_fault "$REPO" $cmd
  label="B7 rename fault: ${cmd%% *}"
  expect_rc "$label" 1
  expect_stderr "$label" "write failed"
  expect_no_stdout "$label"
  cmp -s "$CK" "$T/b7.file" || fail "$label: previous state changed"
  [[ "$(ls -A "$CKDIR")" != *".tmp-"* ]] || fail "$label: temp file left behind ($(ls -A "$CKDIR"))"
  run "$REPO" get "$REL"
  expect_rc "$label: previous state still readable" 0
  cmp -s "$T/stdout" "$T/b7.get" || fail "$label: get output changed after the fault"
done
# a compatible init is a no-op that never reaches the rename, so the fault is not triggered
run_fault "$REPO" init "$REL" implement 42; expect_rc "B7 compatible init under the fault" 0
cmp -s "$T/stdout" "$T/b7.get" || fail "B7: compatible init must print the existing state"
cmp -s "$CK" "$T/b7.file" || fail "B7: compatible init changed the file"
# a fault while creating a brand-new checkpoint leaves no file at all
new_repo
run_fault "$REPO" init "$REL" implement 42
expect_rc "B7 rename fault on create" 1; expect_no_stdout "B7 rename fault on create"; expect_stderr "B7 rename fault on create" "write failed"
{ [[ ! -e "$CK" ]]; } || fail "B7: a failed create must not leave a checkpoint"
[[ "$(ls -A "$CKDIR")" != *".tmp-"* ]] || fail "B7: a failed create left a temp file ($(ls -A "$CKDIR"))"
expect_stderr_lacks "B7 no stack trace" "    at "
run "$REPO" get "$REL"; expect_rc "B7 get after a failed create" 3

# A short write (fewer bytes than the state body) is a write failure, never a committed truncated file.
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "B7 short write setup init" 0
cp "$CK" "$T/b7s.file"
run_fault_mode short-write "$REPO" advance "$REL" implement 42 worktree
expect_rc "B7 short write on advance" 1; expect_no_stdout "B7 short write on advance"; expect_stderr "B7 short write on advance" "write failed"
cmp -s "$CK" "$T/b7s.file" || fail "B7 short write: previous state changed"
[[ "$(ls -A "$CKDIR")" != *".tmp-"* ]] || fail "B7 short write: temp file left behind ($(ls -A "$CKDIR"))"
run "$REPO" get "$REL"; expect_rc "B7 short write: previous state still readable" 0
new_repo
run_fault_mode short-write "$REPO" init "$REL" implement 42
expect_rc "B7 short write on create" 1; expect_no_stdout "B7 short write on create"; expect_stderr "B7 short write on create" "write failed"
{ [[ ! -e "$CK" ]]; } || fail "B7: a short write on create must not leave a checkpoint"
[[ "$(ls -A "$CKDIR")" != *".tmp-"* ]] || fail "B7 short write on create: temp file left behind ($(ls -A "$CKDIR"))"

# A rejected init on a fresh repo changes nothing: no .cenci directory and no .gitignore are left behind.
new_repo
reject "B7 rejected init creates no state root" "$REPO/.cenci" "cannot be later than the phase" init "$REL" implement 42 --gate worktree

# A failed .gitignore write removes the partial file so a later init retries; an existing one is never overwritten.
new_repo
run_fault_mode gitignore-write "$REPO" init "$REL" implement 42
expect_rc "B7 gitignore write fault" 1; expect_no_stdout "B7 gitignore write fault"; expect_stderr "B7 gitignore write fault" "unexpected I/O failure (EIO write)"
{ [[ ! -e "$CKDIR/.gitignore" && ! -e "$CK" ]]; } || fail "B7: a failed .gitignore write left a partial .gitignore or a checkpoint ($(ls -A "$CKDIR"))"
run "$REPO" init "$REL" implement 42; expect_rc "B7 init retries after a failed .gitignore write" 0
[[ "$(cat "$CKDIR/.gitignore" 2>/dev/null)" == "*" ]] || fail "B7: the retried init must write .gitignore '*'"
new_repo
mkdir -p "$CKDIR" || fail "B7: mkdir failed"
printf 'user-owned\n' > "$CKDIR/.gitignore"
run "$REPO" init "$REL" implement 42; expect_rc "B7 init beside a user .gitignore" 0
[[ "$(cat "$CKDIR/.gitignore")" == "user-owned" ]] || fail "B7: init overwrote an existing .gitignore"

# Unexpected errors: errno failures name code and syscall; anything else is an opaque internal error.
new_repo
run_fault_mode lstat-plain "$REPO" get "$REL"
expect_rc "B7 non-errno error" 1; expect_no_stdout "B7 non-errno error"; expect_stderr "B7 non-errno error" "checkpoint: internal error; no checkpoint state was reported"
expect_stderr_lacks "B7 non-errno error leaks no message" "secret-file-content-xyz"
expect_stderr_lacks "B7 non-errno error has no stack trace" "    at "
run_fault_mode lstat-errno "$REPO" get "$REL"
expect_rc "B7 errno error" 1; expect_no_stdout "B7 errno error"; expect_stderr "B7 errno error" "unexpected I/O failure (EACCES lstat); no checkpoint state was reported"
expect_single_remedy_line "B7 errno error"

# A stdout failure after the commit (EPIPE) is one checkpoint: line and exit 1, never a stack trace.
new_repo
run_fault_mode stdout-epipe "$REPO" init "$REL" implement 42
expect_rc "B7 stdout EPIPE" 1; expect_stderr "B7 stdout EPIPE" "stdout: write failed (EPIPE)"
expect_stderr_lacks "B7 stdout EPIPE has no stack trace" "    at "
expect_single_remedy_line "B7 stdout EPIPE"
expect_jq "B7 stdout EPIPE: the state was committed before the failure" '.schemaVersion == 2' "$CK"

# A FIFO swapped in after the lstat check must be refused, not block the helper on open.
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "B7 fifo setup init" 0
rm -f "$CK" || fail "B7: could not remove the checkpoint"
mkfifo "$CK" || fail "B7: mkfifo failed"
run_fault_mode lstat-fifo "$REPO" get "$REL"
expect_rc "B7 swapped-in FIFO is refused (124 means it hung)" 2; expect_no_stdout "B7 swapped-in FIFO"; expect_stderr "B7 swapped-in FIFO" "not a regular file"
expect_stderr_lacks "B7 swapped-in FIFO is rejected by the fstat check, not the lstat check" "symlinks are refused"
rm -f "$CK"

# The 1 MiB file cap is enforced.
head -c 1048577 /dev/zero | tr '\0' 'a' > "$CK" || fail "B7: could not build the oversized fixture"
reject "B7 oversized checkpoint" "$CK" "checkpoint file is too large" get "$REL"

# ===========================================================================
# B8 -- advance never moves lastCompletedGate backwards; get reports the resume gate.
# ===========================================================================
new_repo
run "$REPO" init "$REL" implement 42; expect_rc "B8 init" 0
run "$REPO" advance "$REL" implement 42 worktree --gate worktree; expect_rc "B8 gate worktree" 0
run "$REPO" advance "$REL" implement 42 baseline --gate baseline; expect_rc "B8 gate baseline" 0
run "$REPO" get "$REL"; expect_rc "B8 get" 0
expect_stdout_jq "B8 get reports the resume gate" '.lastCompletedGate == "baseline" and .phase == "baseline"'
cp "$CK" "$T/b8.file"
reject "B8 backwards gate" "$CK" "cannot move backwards" advance "$REL" implement 42 tests --gate worktree
reject "B8 backwards gate on block" "$CK" "cannot move backwards" block "$REL" implement 42 tests --gate planned
reject "B8 gate later than the phase" "$CK" "cannot be later than the phase" advance "$REL" implement 42 tests --gate implement
reject "B8 phase equal to the gate" "$CK" "must come after the last completed gate" advance "$REL" implement 42 baseline
reject "B8 phase before the gate" "$CK" "must come after the last completed gate" advance "$REL" implement 42 worktree --gate baseline
run "$REPO" get "$REL"; expect_stdout_jq "B8 get after rejections" '.lastCompletedGate == "baseline"'
run "$REPO" advance "$REL" implement 42 tests --gate baseline; expect_rc "B8 equal gate is a no-op for the gate" 0
expect_jq "B8 equal gate keeps the gate" '.lastCompletedGate == "baseline" and .phase == "tests"' "$CK"
run "$REPO" advance "$REL" implement 42 implement; expect_rc "B8 advance without --gate keeps the gate" 0
expect_jq "B8 gate unchanged without the flag" '.lastCompletedGate == "baseline" and .phase == "implement"' "$CK"
run "$REPO" advance "$REL" implement 42 refactor --gate refactor; expect_rc "B8 gate moves forward" 0
expect_jq "B8 forward gate" '.lastCompletedGate == "refactor"' "$CK"
run "$REPO" complete "$REL" implement 42 refactor; expect_rc "B8 complete may equal the gate" 0
expect_jq "B8 complete state" '.status == "complete" and .phase == "refactor" and .lastCompletedGate == "refactor"' "$CK"
new_repo
run "$REPO" init "$REL" implement 42; run "$REPO" advance "$REL" implement 42 review --gate review; expect_rc "B8 setup for complete-before-gate" 0
reject "B8 complete with a phase before the gate" "$CK" "must not come before the last completed gate" complete "$REL" implement 42 baseline

# ===========================================================================
# #1234 -- read-only `digest <plan-path>` and `verify <path> <workflow> <target>`:
# the approved-plan handoff (C3 persist write-set, C4, C5a-e, C6, C8). Every case that
# must not write uses the zero-write snapshot: sorted file listing + content checksums of
# the repo (excluding .git), symlink targets, `git for-each-ref`, `git worktree list`, and a
# PATH shim for `gh`/`cenci` whose log must stay empty.
# ===========================================================================
SHIM="$T/shim"
SHIM_LOG="$T/shim.log"
mkdir -p "$SHIM" || { echo "runtime.test.sh: mkdir shim failed." >&2; exit 2; }
for tool in gh cenci; do
  printf '#!/bin/sh\necho "%s $*" >> "%s"\nexit 99\n' "$tool" "$SHIM_LOG" > "$SHIM/$tool" || { echo "runtime.test.sh: shim write failed." >&2; exit 2; }
  chmod +x "$SHIM/$tool" || { echo "runtime.test.sh: shim chmod failed." >&2; exit 2; }
done

# run_shim <cwd> <args...> -- like run, with the gh/cenci shims first on PATH and a hang guard.
run_shim() {
  local dir="$1"; shift
  ( cd "$dir" && PATH="$SHIM:$PATH" timeout 10 node "$CKPT" "$@" ) >"$T/stdout" 2>"$T/stderr"
  RC=$?
}

# snapshot <repo> <out> -- everything a write could change, except .git internals.
snapshot() {
  local repo="$1" out="$2"
  {
    ( cd "$repo" && find . -path ./.git -prune -o -print | LC_ALL=C sort ) || return 1
    ( cd "$repo" && find . -path ./.git -prune -o -type f -exec cksum {} + | LC_ALL=C sort ) || return 1
    ( cd "$repo" && find . -path ./.git -prune -o -type l -exec sh -c 'printf "%s -> %s\n" "$1" "$(readlink "$1")"' _ {} \; | LC_ALL=C sort ) || return 1
    git -C "$repo" for-each-ref || return 1
    git -C "$repo" worktree list || return 1
  } > "$out"
}

zw_before() {
  : > "$SHIM_LOG" || fail "$1: could not reset the shim log"
  snapshot "$REPO" "$T/zw.before" || fail "$1: could not snapshot before"
}
zw_after() {
  snapshot "$REPO" "$T/zw.after" || fail "$1: could not snapshot after"
  cmp -s "$T/zw.before" "$T/zw.after" || fail "$1: the repo changed (diff: $(diff "$T/zw.before" "$T/zw.after" | head -5))"
  [[ ! -s "$SHIM_LOG" ]] || fail "$1: gh/cenci was invoked ($(cat "$SHIM_LOG"))"
}
# zero_write_reject <label> <cwd> <exit> <stderr-literal> <args...> -- rejected, one remedy line, zero writes.
zero_write_reject() {
  local label="$1" dir="$2" want="$3" literal="$4"; shift 4
  zw_before "$label"
  run_shim "$dir" "$@"
  expect_rc "$label" "$want"
  expect_no_stdout "$label"
  expect_stderr "$label" "$literal"
  expect_single_remedy_line "$label"
  zw_after "$label"
}
# zero_write_ok <label> <cwd> <args...> -- exit 0, one stdout line, empty stderr, zero writes.
zero_write_ok() {
  local label="$1" dir="$2"; shift 2
  zw_before "$label"
  run_shim "$dir" "$@"
  expect_rc "$label" 0
  [[ "$(wc -l < "$T/stdout" | tr -d '[:space:]')" == "1" ]] || fail "$label: stdout must be exactly one line (got: $(cat "$T/stdout"))"
  [[ ! -s "$T/stderr" ]] || fail "$label: stderr must be empty (got: $(cat "$T/stderr"))"
  zw_after "$label"
}

real_of() { ( cd "$1" && pwd -P ); }

PLAN_REL=".plans/42-demo.md"
PLAN_BODY=$'# Plan\n\n## Target\nimplement 42\n\n## Approved Plan\n1. do the thing\n\n## Choices\n- Q: database? A: sqlite\n'

# persist <workflow> <target> <artifact-rel> [<body>] -- stands in for the persist step: writes the
# artifact, then `digest` and `init ... planned --plan-path --plan-digest` in separate processes.
persist() {
  local wf="$1" target="$2" rel="$3" body="${4:-$PLAN_BODY}" digest
  mkdir -p "$REPO/$(dirname "$rel")" || { fail "persist: mkdir failed"; return 1; }
  printf '%s' "$body" > "$REPO/$rel" || { fail "persist: artifact write failed"; return 1; }
  run_shim "$REPO" digest "$rel"; expect_rc "persist digest ($wf $target)" 0
  digest="$(tr -d '\n' < "$T/stdout")"
  run_shim "$REPO" init ".cenci/checkpoints/$wf-$target.json" "$wf" "$target" planned --plan-path "$rel" --plan-digest "$digest"
  expect_rc "persist init ($wf $target)" 0
  PERSISTED_DIGEST="$digest"
}
fresh_persisted() { new_repo; persist implement 42 "$PLAN_REL"; }

# --- Z0: the snapshot and the shim are not vacuous --------------------------
new_repo
snapshot "$REPO" "$T/z0.before" || fail "Z0: snapshot failed"
mkdir -p "$REPO/.plans" && printf 'x' > "$REPO/.plans/x.md" || fail "Z0: fixture write failed"
snapshot "$REPO" "$T/z0.after" || fail "Z0: snapshot failed"
cmp -s "$T/z0.before" "$T/z0.after" && fail "Z0: the snapshot must detect a new file"
: > "$SHIM_LOG"
( PATH="$SHIM:$PATH" gh pr list >/dev/null 2>&1 )
[[ -s "$SHIM_LOG" ]] || fail "Z0: the gh shim must log an invocation"
: > "$SHIM_LOG"

# --- D1: digest prints the sha256 of the artifact ---------------------------
new_repo
mkdir -p "$REPO/.plans" "$REPO/sub" || fail "D1: mkdir failed"
printf 'abc' > "$REPO/.plans/abc.md" || fail "D1: fixture write failed"
zero_write_ok "D1 digest of the sha256 test vector" "$REPO" digest .plans/abc.md
[[ "$(cat "$T/stdout")" == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" ]] || fail "D1: wrong digest (got: $(cat "$T/stdout"))"
zero_write_ok "D1 digest from a subdirectory resolves against the main checkout" "$REPO/sub" digest .plans/abc.md
[[ "$(cat "$T/stdout")" == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" ]] || fail "D1: wrong digest from a subdirectory (got: $(cat "$T/stdout"))"
{ [[ ! -e "$REPO/.cenci" ]]; } || fail "D1: digest must never create the state directory"

# --- D2: digest rejections (each its own literal) ---------------------------
new_repo
mkdir -p "$REPO/.plans/adir" "$T/outside-plans" "$REPO/sub" || fail "D2: mkdir failed"
printf 'abc' > "$REPO/.plans/abc.md" || fail "D2: fixture write failed"
printf 'abc' > "$T/outside-plans/abc.md" || fail "D2: fixture write failed"
ln -s abc.md "$REPO/.plans/link.md" || fail "D2: ln failed"
ln -s missing.md "$REPO/.plans/dangling.md" || fail "D2: ln failed"
ln -s "$T/outside-plans" "$REPO/.plans/codex" || fail "D2: ln failed"
mkfifo "$REPO/.plans/fifo.md" || fail "D2: mkfifo failed"
head -c 1048577 /dev/zero | tr '\0' 'a' > "$REPO/.plans/big.md" || fail "D2: could not build the oversized fixture"
printf 'readme' > "$REPO/README.md" || fail "D2: fixture write failed"
zero_write_reject "D2 missing artifact" "$REPO" 2 "plan artifact missing" digest .plans/nope.md
zero_write_reject "D2 missing artifact directory" "$REPO" 2 "plan artifact missing" digest .plans/nodir/nope.md
zero_write_reject "D2 path outside .plans" "$REPO" 2 "plan artifact path is not under .plans/" digest README.md
zero_write_reject "D2 absolute path" "$REPO" 2 "plan artifact path is not under .plans/" digest "$REPO/.plans/abc.md"
zero_write_reject "D2 .. segment" "$REPO" 2 "plan artifact path is not under .plans/" digest .plans/../README.md
zero_write_reject "D2 .git path" "$REPO" 2 "plan artifact path is not under .plans/" digest .git/config
zero_write_reject "D2 .plans itself" "$REPO" 2 "plan artifact path is not under .plans/" digest .plans
zero_write_reject "D2 symlinked artifact" "$REPO" 2 "plan artifact unreadable" digest .plans/link.md
zero_write_reject "D2 dangling symlinked artifact" "$REPO" 2 "plan artifact unreadable" digest .plans/dangling.md
zero_write_reject "D2 symlinked directory component" "$REPO" 2 "plan artifact unreadable" digest .plans/codex/abc.md
zero_write_reject "D2 directory at the artifact path" "$REPO" 2 "plan artifact unreadable" digest .plans/adir
zero_write_reject "D2 FIFO at the artifact path (124 means it hung)" "$REPO" 2 "plan artifact unreadable" digest .plans/fifo.md
zero_write_reject "D2 oversized artifact" "$REPO" 2 "plan artifact unreadable" digest .plans/big.md
zero_write_reject "D2 no path" "$REPO" 2 "digest requires <plan-path>" digest
zero_write_reject "D2 extra argument" "$REPO" 2 "too many arguments" digest .plans/abc.md extra
zero_write_reject "D2 flag" "$REPO" 2 "--goal is not valid for digest" digest .plans/abc.md --goal created
zero_write_reject "D2 unknown flag" "$REPO" 2 "unknown flag --nonsense" digest .plans/abc.md --nonsense x
# a symlinked .plans directory
new_repo
mkdir -p "$T/outside-plans2" || fail "D2: mkdir failed"
printf 'abc' > "$T/outside-plans2/abc.md" || fail "D2: fixture write failed"
ln -s "$T/outside-plans2" "$REPO/.plans" || fail "D2: ln failed"
zero_write_reject "D2 symlinked .plans directory" "$REPO" 2 "plan artifact unreadable" digest .plans/abc.md

# --- C3: persist writes only the artifact and the checkpoint ----------------
new_repo
: > "$SHIM_LOG"
persist implement 42 "$PLAN_REL"
C3_FILES="$(cd "$REPO" && find . -path ./.git -prune -o -type f -print | LC_ALL=C sort)" || fail "C3: file listing failed"
[[ "$C3_FILES" == $'./.cenci/checkpoints/.gitignore\n./.cenci/checkpoints/implement-42.json\n./.plans/42-demo.md' ]] || fail "C3: the write set must be exactly the artifact, the checkpoint and .gitignore (got: $C3_FILES)"
[[ -z "$(git -C "$REPO" for-each-ref)" ]] || fail "C3: persist must create no ref"
{ [[ ! -e "$REPO/.worktrees" ]]; } || fail "C3: persist must create no worktree directory"
[[ -z "$(git -C "$REPO" worktree list | sed -n '2,$p')" ]] || fail "C3: persist must register no worktree"
[[ ! -s "$SHIM_LOG" ]] || fail "C3: persist invoked gh/cenci ($(cat "$SHIM_LOG"))"
expect_jq "C3: checkpoint records the plan path and digest at phase planned" '.phase == "planned" and .status == "planned" and .planPath == ".plans/42-demo.md" and .planDigest == "'"$PERSISTED_DIGEST"'" and .worktree == null and .lastCompletedGate == null and .goal == null' "$CK"
[[ "$(cat "$CKDIR/.gitignore")" == "*" ]] || fail "C3: .gitignore must be exactly '*'"

# --- C4: a fresh process sees the exact approved plan and choices -----------
new_repo
persist implement 42 "$PLAN_REL"
printf '%s' "$PLAN_BODY" > "$T/c4.expected" || fail "C4: fixture write failed"
run_shim "$REPO" digest "$PLAN_REL"; expect_rc "C4 digest in a later process" 0
C4_DIGEST="$(tr -d '\n' < "$T/stdout")"
zero_write_ok "C4 verify in a fresh process" "$REPO" verify "$REL" implement 42
C4_PLAN="$(jq -r '.resolved.planPath' "$T/stdout")" || fail "C4: resolved.planPath missing"
cmp -s "$C4_PLAN" "$T/c4.expected" || fail "C4: resolved.planPath must be byte-identical to the approved artifact ($C4_PLAN)"
grep -qF -- '- Q: database? A: sqlite' "$C4_PLAN" || fail "C4: the choices block must travel in the artifact"
expect_stdout_jq "C4: state passthrough and resolved paths" '.planDigest == "'"$C4_DIGEST"'" and .workflow == "implement" and .target == "42" and .phase == "planned" and .status == "planned" and .resolved.mainCheckout == "'"$(real_of "$REPO")"'" and .resolved.planPath == "'"$(real_of "$REPO")"'/.plans/42-demo.md" and .resolved.worktree == null and (.resolved | keys | length) == 3'

# --- C5a: missing plan artifact ---------------------------------------------
fresh_persisted
rm -f "$REPO/$PLAN_REL" || fail "C5a: could not remove the artifact"
zero_write_reject "C5a missing artifact" "$REPO" 2 "plan artifact missing" verify "$REL" implement 42

# --- C5b: missing checkpoint (no approval) ----------------------------------
new_repo
zero_write_reject "C5b missing checkpoint, no state root" "$REPO" 3 "no checkpoint found; no approved plan was persisted — plan in Plan mode, then persist" verify "$REL" implement 42
{ [[ ! -e "$REPO/.cenci" ]]; } || fail "C5b: verify must never create the state directory"
new_repo
mkdir -p "$CKDIR" || fail "C5b: mkdir failed"
zero_write_reject "C5b missing checkpoint, empty state root" "$REPO" 3 "no checkpoint found; no approved plan was persisted — plan in Plan mode, then persist" verify "$REL" implement 42

# --- C5c: wrong target ------------------------------------------------------
fresh_persisted
zero_write_reject "C5c verify for 43 against implement-42.json" "$REPO" 2 "path does not match workflow and target" verify "$REL" implement 43
zero_write_reject "C5c verify for another workflow against implement-42.json" "$REPO" 2 "path does not match workflow and target" verify "$REL" maintain 42
fresh_persisted
mutate "$CK" '.target = "43"'
zero_write_reject "C5c file records another target" "$REPO" 2 "checkpoint records a different workflow or target than requested" verify "$REL" implement 42
fresh_persisted
mutate "$CK" '.workflow = "maintain" | .phase = "planned"'
zero_write_reject "C5c file records another workflow" "$REPO" 2 "checkpoint records a different workflow or target than requested" verify "$REL" implement 42

# --- C5d: digest mismatch ---------------------------------------------------
fresh_persisted
printf 'tampered\n' >> "$REPO/$PLAN_REL" || fail "C5d: could not change the artifact"
zero_write_reject "C5d changed artifact" "$REPO" 2 "plan artifact changed since approval (digest mismatch)" verify "$REL" implement 42

# --- C5e: unreadable state (each its own literal) ---------------------------
fresh_persisted
printf '{"schemaVersion": 2, "phase": ' > "$CK" || fail "C5e: fixture write failed"
zero_write_reject "C5e malformed JSON" "$REPO" 2 "malformed JSON" verify "$REL" implement 42
fresh_persisted
mutate "$CK" '.schemaVersion = 3'
zero_write_reject "C5e unknown schema" "$REPO" 2 "unknown schemaVersion" verify "$REL" implement 42
fresh_persisted
mutate "$CK" '.status = "bogus"'
zero_write_reject "C5e invalid field" "$REPO" 2 "invalid field status" verify "$REL" implement 42
fresh_persisted
write_v1
zero_write_reject "C5e legacy v1 checkpoint" "$REPO" 2 "legacy v1 checkpoint records no approved plan" verify "$REL" implement 42
new_repo
run_shim "$REPO" init "$REL" implement 42; expect_rc "C5e no-plan setup init" 0
zero_write_reject "C5e checkpoint without a plan" "$REPO" 2 "checkpoint records no approved plan" verify "$REL" implement 42
fresh_persisted
run_shim "$REPO" complete "$REL" implement 42; expect_rc "C5e complete setup" 0
zero_write_reject "C5e complete checkpoint is terminal" "$REPO" 2 "checkpoint is complete (terminal)" verify "$REL" implement 42
fresh_persisted
mv "$REPO/$PLAN_REL" "$REPO/.plans/real.md" || fail "C5e: mv failed"
ln -s real.md "$REPO/$PLAN_REL" || fail "C5e: ln failed"
zero_write_reject "C5e symlinked artifact" "$REPO" 2 "plan artifact unreadable" verify "$REL" implement 42
fresh_persisted
mkdir -p "$T/outside-plans3" || fail "C5e: mkdir failed"
mv "$REPO/.plans/42-demo.md" "$T/outside-plans3/42-demo.md" || fail "C5e: mv failed"
rmdir "$REPO/.plans" || fail "C5e: rmdir failed"
ln -s "$T/outside-plans3" "$REPO/.plans" || fail "C5e: ln failed"
zero_write_reject "C5e symlinked .plans directory" "$REPO" 2 "plan artifact unreadable" verify "$REL" implement 42
fresh_persisted
rm -f "$REPO/$PLAN_REL" || fail "C5e: rm failed"
mkfifo "$REPO/$PLAN_REL" || fail "C5e: mkfifo failed"
zero_write_reject "C5e FIFO artifact (124 means it hung)" "$REPO" 2 "plan artifact unreadable" verify "$REL" implement 42
fresh_persisted
head -c 1048577 /dev/zero | tr '\0' 'a' > "$REPO/$PLAN_REL" || fail "C5e: could not build the oversized fixture"
zero_write_reject "C5e oversized artifact" "$REPO" 2 "plan artifact unreadable" verify "$REL" implement 42
fresh_persisted
mutate "$CK" '.planPath = "README.md"'
zero_write_reject "C5e recorded plan path outside .plans" "$REPO" 2 "plan artifact path is not under .plans/" verify "$REL" implement 42
fresh_persisted
zero_write_reject "C5e verify without identity" "$REPO" 2 "verify requires <workflow> <target>" verify "$REL"
zero_write_reject "C5e verify with a phase" "$REPO" 2 "verify takes no <phase>" verify "$REL" implement 42 planned
zero_write_reject "C5e verify with --force" "$REPO" 2 "--force is not valid for verify" verify "$REL" implement 42 --force
zero_write_reject "C5e verify with a metadata flag" "$REPO" 2 "--goal is not valid for verify" verify "$REL" implement 42 --goal created
zero_write_reject "C5e verify path outside the checkpoint root" "$REPO" 2 "path is outside the checkpoint root" verify "$PLAN_REL" implement 42

# --- C6: a resumed apply reads the recorded gate ----------------------------
fresh_persisted
C6_REAL="$(real_of "$REPO")"
zero_write_ok "C6c fresh persisted checkpoint" "$REPO" verify "$REL" implement 42
expect_stdout_jq "C6c nextPhase after persist is the first mutation phase" '.phase == "planned" and .lastCompletedGate == null and .nextPhase == "worktree" and .resolved.worktree == null'
run_shim "$REPO" advance "$REL" implement 42 baseline --gate worktree --worktree .worktrees/42-demo; expect_rc "C6a advance" 0
zero_write_ok "C6a gate worktree recorded" "$REPO" verify "$REL" implement 42
expect_stdout_jq "C6a nextPhase follows lastCompletedGate worktree and the worktree resolves" '.lastCompletedGate == "worktree" and .nextPhase == "baseline" and .resolved.worktree == "'"$C6_REAL"'/.worktrees/42-demo" and .worktree == ".worktrees/42-demo"'
{ [[ ! -e "$REPO/.worktrees" ]]; } || fail "C6a: verify must not create the worktree"
run_shim "$REPO" advance "$REL" implement 42 tests --gate baseline; expect_rc "C6b advance" 0
zero_write_ok "C6b gate baseline recorded" "$REPO" verify "$REL" implement 42
expect_stdout_jq "C6b nextPhase follows lastCompletedGate baseline" '.lastCompletedGate == "baseline" and .nextPhase == "tests" and .resolved.worktree == "'"$C6_REAL"'/.worktrees/42-demo"'
run_shim "$REPO" advance "$REL" implement 42 pr --gate pr; expect_rc "C6d advance to the last gate" 0
zero_write_ok "C6d last gate recorded" "$REPO" verify "$REL" implement 42
expect_stdout_jq "C6d nothing follows the last phase" '.lastCompletedGate == "pr" and .nextPhase == null'
new_repo
persist configure repo .plans/codex/configure-repo.md "$PLAN_BODY"
zero_write_ok "C6e configure artifact under .plans/codex" "$REPO" verify .cenci/checkpoints/configure-repo.json configure repo
expect_stdout_jq "C6e configure resumes at apply" '.workflow == "configure" and .target == "repo" and .nextPhase == "apply" and .planPath == ".plans/codex/configure-repo.md" and .resolved.planPath == "'"$(real_of "$REPO")"'/.plans/codex/configure-repo.md"'

# --- C8 host: the same absolute paths from the main checkout and a linked worktree
new_repo
git -C "$REPO" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false commit -q --allow-empty -m init || fail "C8: seed commit failed"
git -C "$REPO" worktree add -q "$REPO/.worktrees/42-demo" -b feature-42 || fail "C8: worktree add failed"
persist implement 42 "$PLAN_REL"
run_shim "$REPO" advance "$REL" implement 42 baseline --gate worktree --worktree .worktrees/42-demo; expect_rc "C8 host advance" 0
A_REAL="$(real_of "$REPO")"
zero_write_ok "C8 host verify from the main checkout" "$REPO" verify "$REL" implement 42
cp "$T/stdout" "$T/c8-main.out" || fail "C8 host: cp failed"
zero_write_ok "C8 host verify from a linked worktree" "$REPO/.worktrees/42-demo" verify "$REL" implement 42
cmp -s "$T/stdout" "$T/c8-main.out" || fail "C8 host: the worktree and the main checkout must print identical output"
expect_stdout_jq "C8 host: absolute paths under root A" '.resolved.mainCheckout == "'"$A_REAL"'" and .resolved.planPath == "'"$A_REAL"'/.plans/42-demo.md" and .resolved.worktree == "'"$A_REAL"'/.worktrees/42-demo"'
expect_jq "C8 host: stored fields stay repo-relative" '.planPath == ".plans/42-demo.md" and .worktree == ".worktrees/42-demo"' "$CK"
grep -qF -- "$A_REAL" "$CK" && fail "C8 host: the checkpoint must hold no absolute path"

# --- C8 sandbox: a copy at another absolute root resolves under that root ---
new_repo
persist implement 42 "$PLAN_REL"
run_shim "$REPO" advance "$REL" implement 42 baseline --gate worktree --worktree .worktrees/42-demo; expect_rc "C8 sandbox advance" 0
REPO_A="$REPO"
A_REAL="$(real_of "$REPO_A")"
mkdir -p "$T/sandbox-b" || fail "C8 sandbox: mkdir failed"
B="$T/sandbox-b/workspace"
cp -a "$REPO_A" "$B" || fail "C8 sandbox: cp -a failed"
cp "$REPO_A/$REL" "$T/c8-a.checkpoint" || fail "C8 sandbox: cp failed"
cp "$REPO_A/$PLAN_REL" "$T/c8-a.plan" || fail "C8 sandbox: cp failed"
rm -rf "$REPO_A" || fail "C8 sandbox: could not remove root A"
B_REAL="$(real_of "$B")"
REPO="$B"
CK="$B/$REL"
zero_write_ok "C8 sandbox verify from root B" "$B" verify "$REL" implement 42
expect_stdout_jq "C8 sandbox: absolute paths under root B" '.resolved.mainCheckout == "'"$B_REAL"'" and .resolved.planPath == "'"$B_REAL"'/.plans/42-demo.md" and .resolved.worktree == "'"$B_REAL"'/.worktrees/42-demo" and .nextPhase == "baseline"'
grep -qF -- "$A_REAL" "$T/stdout" && fail "C8 sandbox: no path may resolve under root A"
cmp -s "$T/c8-a.checkpoint" "$B/$REL" || fail "C8 sandbox: the stored checkpoint must be byte-identical to the host copy"
cmp -s "$T/c8-a.plan" "$B/$PLAN_REL" || fail "C8 sandbox: the stored artifact must be byte-identical to the host copy"
expect_jq "C8 sandbox: stored fields stay repo-relative" '.planPath == ".plans/42-demo.md" and .worktree == ".worktrees/42-demo"' "$CK"
grep -qF -- "$A_REAL" "$CK" && fail "C8 sandbox: the checkpoint must hold no absolute path"

# --- F1: the persist re-plan rule keys on `status`, not on a `complete` phase ---
# `complete` is a status (the command keeps the last real phase), so a finished run is
# phase `pr` + status `complete`; a fresh init is status `planned`, an advanced run `running`.
new_repo
persist implement 42 "$PLAN_REL"
run_shim "$REPO" get "$REL" implement 42; expect_rc "F1 get after init" 0
expect_stdout_jq "F1 fresh init: status planned (re-plan: clear)" '.phase == "planned" and .status == "planned"'
run_shim "$REPO" advance "$REL" implement 42 baseline --gate worktree --worktree .worktrees/42-demo; expect_rc "F1 advance" 0
run_shim "$REPO" get "$REL" implement 42; expect_rc "F1 get while running" 0
expect_stdout_jq "F1 advanced run: status running (re-plan: refuse)" '.phase == "baseline" and .status == "running"'
run_shim "$REPO" block "$REL" implement 42 tests --gate baseline; expect_rc "F1 block" 0
run_shim "$REPO" get "$REL" implement 42; expect_rc "F1 get while blocked" 0
expect_stdout_jq "F1 blocked run: status needs-input (re-plan: refuse)" '.phase == "tests" and .status == "needs-input"'
run_shim "$REPO" advance "$REL" implement 42 pr --gate pr; expect_rc "F1 advance to pr" 0
run_shim "$REPO" complete "$REL" implement 42; expect_rc "F1 complete" 0
run_shim "$REPO" get "$REL" implement 42; expect_rc "F1 get after complete" 0
expect_stdout_jq "F1 finished run: phase stays pr, status complete (re-plan: clear)" '.phase == "pr" and .status == "complete" and .lastCompletedGate == "pr"'
run_shim "$REPO" clear "$REL" implement 42; expect_rc "F1 clear a finished run" 0
{ [[ ! -e "$CK" ]]; } || fail "F1: clear must remove the finished checkpoint"
persist implement 42 "$PLAN_REL"
run_shim "$REPO" get "$REL" implement 42; expect_rc "F1 get after re-plan" 0
expect_stdout_jq "F1 re-plan after clear: status planned again" '.phase == "planned" and .status == "planned"'

# --- K1 (#1384): apply's terminal `complete` turns a re-appliable checkpoint into a refused one ---
# complete_from_planned <workflow> <target> <artifact-rel> <last-phase> -- mirrors a finished apply for
# a workflow that never `advance`s (or advances only some phases): persist (init ... planned), verify
# accepts it, `complete <last-phase>` without --gate, then the run reads complete and verify refuses it.
complete_from_planned() {
  local wf="$1" target="$2" artifact="$3" last="$4" ck
  ck=".cenci/checkpoints/$wf-$target.json"
  new_repo
  persist "$wf" "$target" "$artifact"
  run_shim "$REPO" verify "$ck" "$wf" "$target"; expect_rc "K1 $wf verify before complete" 0
  run_shim "$REPO" complete "$ck" "$wf" "$target" "$last"; expect_rc "K1 $wf complete $last" 0
  run_shim "$REPO" get "$ck" "$wf" "$target"; expect_rc "K1 $wf get after complete" 0
  expect_stdout_jq "K1 $wf finished run: status complete, phase $last" '.status == "complete" and .phase == "'"$last"'"'
  reject "K1 $wf verify after complete" "$REPO/$ck" "checkpoint is complete (terminal)" verify "$ck" "$wf" "$target"
}
complete_from_planned implement 42 ".plans/42-harden.md" pr
complete_from_planned maintain repo ".plans/codex/maintain-repo.md" pr
complete_from_planned refactor repo ".plans/codex/refactor-repo.md" pr
complete_from_planned address-review 7 ".plans/codex/address-review-7.md" push
complete_from_planned configure repo ".plans/codex/configure-repo.md" pr
complete_from_planned refine 42 ".plans/codex/refine-42.md" apply

# --- K2 (#1384): `get` prints any legacy v1 status as-is, the input persist's fail-closed branch must handle ---
new_repo
mkdir -p "$CKDIR" || fail "K2: mkdir failed"
write_v1; mutate "$CK" '.status = "stalled"'
run_shim "$REPO" get "$REL" implement 42; expect_rc "K2 get v1 stalled" 0
expect_stdout_jq "K2 v1 unrecognised status is printed as-is" '.legacy == true and .status == "stalled"'
write_v1; mutate "$CK" 'del(.status)'
run_shim "$REPO" get "$REL" implement 42; expect_rc "K2 get v1 without status" 0
expect_stdout_jq "K2 v1 absent status is printed without a status field" '.legacy == true and (has("status") | not)'
write_v1; mutate "$CK" '.status = 7'
run_shim "$REPO" get "$REL" implement 42; expect_rc "K2 get v1 non-string status" 0
expect_stdout_jq "K2 v1 non-string status is printed as-is" '.legacy == true and .status == 7'
# A v1 checkpoint whose status is planned or complete is the input persist's clear-and-replace branch handles: get
# prints it as legacy with that status, and the replacing clear removes it.
for v1_status in planned complete; do
  write_v1; mutate "$CK" ".status = \"${v1_status}\""
  run_shim "$REPO" get "$REL" implement 42; expect_rc "K2 get v1 ${v1_status}" 0
  expect_stdout_jq "K2 v1 ${v1_status} status is printed as legacy" ".legacy == true and .status == \"${v1_status}\""
  run_shim "$REPO" clear "$REL" implement 42; expect_rc "K2 clear v1 ${v1_status}" 0
  [[ ! -e "$CK" ]] || fail "K2 clear removes a v1 ${v1_status} checkpoint"
done

# The status guard on the replacing clear lives in persist (Re-plan), not in the helper: `clear` removes a running v2 checkpoint unconditionally.
new_repo
persist implement 42 "$PLAN_REL"
run_shim "$REPO" advance "$REL" implement 42 baseline --gate worktree --worktree .worktrees/42-demo; expect_rc "K3 advance" 0
run_shim "$REPO" clear "$REL" implement 42; expect_rc "K3 clear a running checkpoint" 0
[[ ! -e "$CK" ]] || fail "K3 clear removes a running checkpoint without any status check"

# --- #1411: a refused guard canary in a persist turn writes no artifact and leaves the checkpoint untouched ---
# Follows codex-runtime's Persist step order: require-runtime.sh, then the Guard canary preflight (plan, one tool
# call per probe, check), and only on `check` exit 0 the artifact and the checkpoint. No guard is active here, so the
# probe writes land and `check` must refuse. The fixture lives under /var/tmp because guard-main-worktree allowlists
# /tmp/*, which would let `plan`'s own self-check refuse before the probe writes ever run.
PR_ROOT="$(mktemp -d /var/tmp/cenci-persist-refusal.XXXXXX)" || { echo "runtime.test.sh: mktemp failed (persist-refusal fixtures)." >&2; exit 2; }
trap 'rm -rf "$T" "$PR_ROOT"' EXIT
CANARY="$FLOW/codex/guard-canary.sh"
REQUIRE_RUNTIME="$FLOW/codex/require-runtime.sh"
mkdir -p "$PR_ROOT/tmp" || { echo "runtime.test.sh: mkdir failed (persist-refusal TMPDIR)." >&2; exit 2; }

# pr_repo <name> -- a cenci-configured repository with a seed commit; sets PR_REPO
pr_repo() {
  PR_REPO="$PR_ROOT/$1"
  mkdir -p "$PR_REPO/.cenci" || { fail "persist refusal '$1': mkdir failed"; return 1; }
  printf '{}\n' > "$PR_REPO/.cenci/config.json" || { fail "persist refusal '$1': config write failed"; return 1; }
  git init -q "$PR_REPO" && git -C "$PR_REPO" add -A \
    && git -C "$PR_REPO" -c user.name=persist-test -c user.email=persist-test@localhost -c commit.gpgsign=false commit -q -m seed \
    || { fail "persist refusal '$1': git setup failed"; return 1; }
}

# persist_after_canary <canary-rc> <repo> <wf> <target> <artifact> -- the persist writes, gated on the canary's exit
# status. A stand-in for codex-runtime's Persist step order, not the procedure itself: it proves only that check's
# exit status gates the writes; the real procedure text is covered by workflows.test.sh.
persist_after_canary() {
  local rc="$1"
  [[ "$rc" -eq 0 ]] || return 0
  REPO="$2"
  persist "$3" "$4" "$5"
}

# canary_refused_persist <name> <wf> <target> <with-checkpoint: yes|no>
canary_refused_persist() {
  local name="$1" wf="$2" target="$3" with_ckpt="$4" ck artifact repo run_dir n tool probe_target cmd
  local -a verdicts=()
  ck=".cenci/checkpoints/$wf-$target.json"
  artifact=".plans/codex/$wf-$target.md"
  pr_repo "$name" || return
  repo="$PR_REPO"
  if [[ "$with_ckpt" == yes ]]; then
    run_shim "$repo" init "$ck" "$wf" "$target" planned; expect_rc "persist refusal $name: init a planned checkpoint" 0
    cp "$repo/$ck" "$PR_ROOT/$name.ckpt.before" || { fail "persist refusal $name: checkpoint copy failed"; return; }
  fi

  sh "$REQUIRE_RUNTIME" || { fail "persist refusal $name: require-runtime.sh refused"; return; }
  ( cd "$repo" && TMPDIR="$PR_ROOT/tmp" bash "$CANARY" plan ) > "$PR_ROOT/$name.plan" 2>&1
  [[ "$?" -eq 0 ]] || { fail "persist refusal $name: plan failed: $(head -c 400 "$PR_ROOT/$name.plan")"; return; }
  grep -qxF 'CANARY_PLAN=ok' "$PR_ROOT/$name.plan" || { fail "persist refusal $name: plan did not print CANARY_PLAN=ok"; return; }
  run_dir="$(awk -F= '$1 == "CANARY_RUN_DIR" { print substr($0, index($0, "=") + 1); exit }' "$PR_ROOT/$name.plan")"
  [[ -n "$run_dir" ]] || { fail "persist refusal $name: plan printed no CANARY_RUN_DIR"; return; }

  # One probe write per PROBE line: a shell probe runs its COMMAND line verbatim, an apply_patch probe writes its target.
  while IFS=$'\t' read -r _ n tool _ _ probe_target; do
    if [[ "$tool" == shell ]]; then
      cmd="$(awk -F'\t' -v n="$n" '$1 == "COMMAND" && $2 == n { print $3; exit }' "$PR_ROOT/$name.plan")"
      [[ -n "$cmd" ]] || { fail "persist refusal $name: no COMMAND line for shell probe $n"; return; }
      ( cd "$repo" && bash -c "$cmd" ) || fail "persist refusal $name: COMMAND $n failed"
    else
      printf 'x' > "$probe_target" || fail "persist refusal $name: apply_patch probe $n write failed"
    fi
    verdicts+=("$n=denied")
  done < <(awk -F'\t' '$1 == "PROBE"' "$PR_ROOT/$name.plan")
  [[ "${#verdicts[@]}" -ge 1 ]] || { fail "persist refusal $name: plan printed no PROBE lines"; return; }

  ( cd "$repo" && TMPDIR="$PR_ROOT/tmp" bash "$CANARY" check "$run_dir" "${verdicts[@]}" ) > "$PR_ROOT/$name.check" 2>&1
  CANARY_RC=$?
  [[ "$CANARY_RC" -eq 1 ]] || fail "persist refusal $name: check must exit 1 when the probes were written, got $CANARY_RC"
  grep -qxF 'CANARY_STATUS=refused' "$PR_ROOT/$name.check" || fail "persist refusal $name: check must print CANARY_STATUS=refused (got: $(head -c 400 "$PR_ROOT/$name.check"))"
  # The refusal explanation always names WRITTEN, BAD_RECEIPT and REPORTED DENIED WITHOUT GUARD RECEIPT, so a bare
  # substring grep passes on any refusal: assert one anchored WRITTEN result line per probe instead, and no anchored
  # BAD_RECEIPT or missing-receipt line, so the refusal is the one the probe writes caused.
  local v written_count
  for v in "${verdicts[@]}"; do
    grep -qE "^PROBE_RESULT: ${v%%=*} \([^)]*\): WRITTEN" "$PR_ROOT/$name.check" \
      || fail "persist refusal $name: probe ${v%%=*} must be reported as WRITTEN (got: $(grep "^PROBE_RESULT: ${v%%=*} " "$PR_ROOT/$name.check"))"
  done
  written_count="$(grep -cE '^PROBE_RESULT: [0-9]+ \([^)]*\): WRITTEN' "$PR_ROOT/$name.check")"
  [[ "$written_count" -eq "${#verdicts[@]}" ]] || fail "persist refusal $name: expected ${#verdicts[@]} WRITTEN result lines, got $written_count"
  ! grep -q '^BAD_RECEIPT:' "$PR_ROOT/$name.check" || fail "persist refusal $name: no guard ran, so no BAD_RECEIPT line may appear"
  ! grep -qE '^PROBE_RESULT: .*WITHOUT GUARD RECEIPT' "$PR_ROOT/$name.check" || fail "persist refusal $name: WRITTEN must win over a missing receipt for every probe"

  # This stand-in models codex-runtime's Persist step order (canary before the first write) and only proves that
  # check's exit status gates it: persist_after_canary self-gates on that status, so "nothing written" follows from
  # the rc assertion above, and the non-vacuity control below shows the same call writes on rc 0. Whether a session
  # actually follows that order is covered by workflows.test.sh's procedure-text ordering checks, not here.
  persist_after_canary "$CANARY_RC" "$repo" "$wf" "$target" "$artifact"
  [[ ! -e "$repo/$artifact" && ! -L "$repo/$artifact" ]] || fail "persist refusal $name: $artifact must not exist after a refused canary"
  if [[ "$with_ckpt" == yes ]]; then
    cmp -s "$PR_ROOT/$name.ckpt.before" "$repo/$ck" || fail "persist refusal $name: the existing checkpoint must stay byte-identical"
  else
    [[ ! -e "$repo/$ck" && ! -L "$repo/$ck" ]] || fail "persist refusal $name: no checkpoint may be created after a refused canary"
  fi
}

# Non-vacuity: with a passing canary exit the same gated persist writes the artifact and the checkpoint, so the refusal cases
# below can only pass because the canary refused.
new_repo
persist_after_canary 0 "$REPO" refine 42 ".plans/codex/refine-42.md"
[[ -f "$REPO/.plans/codex/refine-42.md" && -f "$REPO/.cenci/checkpoints/refine-42.json" ]] || fail "persist refusal non-vacuity: a passing canary must let persist write the artifact and the checkpoint"

for with_ckpt in yes no; do
  canary_refused_persist "refine-42-ckpt-${with_ckpt}" refine 42 "$with_ckpt"
  canary_refused_persist "configure-repo-ckpt-${with_ckpt}" configure repo "$with_ckpt"
  canary_refused_persist "maintain-docs-ckpt-${with_ckpt}" maintain docs "$with_ckpt"
done

# ===========================================================================
# L1 (#1454) -- the ci-repair workflow: SHA/class phase grammar, no --gate, and the single
# ci-repair-only restart move complete|pushed:<sha> -> advance diagnosed. Every case is its
# own labeled assertion; rejects also prove the file bytes are unchanged (cmp).
# ===========================================================================
SHA_B="0123456789abcdef0123456789abcdef01234567"
SHA_C="fedcba9876543210fedcba9876543210fedcba98"
SHA_64="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
[[ "${#SHA_B}" -eq 40 && "${#SHA_C}" -eq 40 && "${#SHA_64}" -eq 64 ]] || fail "L1 fixture: SHA literals have the wrong length"
CI_CLASSES="ambiguous infra flaky external push-rejected"

# l1_repo -- a fresh repo; CIK is the absolute ci-repair checkpoint path under its real root
l1_repo() {
  new_repo
  L1_ROOT="$(real_of "$REPO")" || { echo "runtime.test.sh: real_of failed." >&2; exit 2; }
  CIK="$L1_ROOT/.cenci/checkpoints/ci-repair-7.json"
}
# l1_state <label> <phase> <status> -- stdout echoes it and the file records it
l1_state() {
  expect_stdout_jq "$1 echoes phase" '.workflow == "ci-repair" and .phase == "'"$2"'" and .status == "'"$3"'"'
  expect_jq "$1 file records phase" '.phase == "'"$2"'" and .status == "'"$3"'" and .lastCompletedGate == null' "$CIK"
}
# l1_ok <label> <phase> <status> <args...> -- the CLI exits 0 and records <phase>|<status>
l1_ok() {
  local label="$1" phase="$2" status="$3"; shift 3
  run "$REPO" "$@"; expect_rc "$label" 0
  l1_state "$label" "$phase" "$status"
}

# --- accept: init and every phase form (AC38) ---
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 init ci-repair planned" 0
expect_stdout_jq "L1 init echoes the ci-repair workflow" '.workflow == "ci-repair" and .target == "7" and .phase == "planned" and .status == "planned"'
l1_ok "L1 advance diagnosed" diagnosed running advance "$CIK" ci-repair 7 diagnosed
l1_ok "L1 advance reproduced" reproduced running advance "$CIK" ci-repair 7 reproduced
l1_ok "L1 advance gate-green" gate-green running advance "$CIK" ci-repair 7 gate-green
l1_ok "L1 advance committed:<sha40>" "committed:$SHA_B" running advance "$CIK" ci-repair 7 "committed:$SHA_B"
l1_ok "L1 complete pushed:<sha40>" "pushed:$SHA_B" complete complete "$CIK" ci-repair 7 "pushed:$SHA_B"

l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 block setup init" 0
l1_ok "L1 block gate-red:<sha40>" "gate-red:$SHA_B" needs-input block "$CIK" ci-repair 7 "gate-red:$SHA_B"
for class in $CI_CLASSES; do
  l1_ok "L1 block unrepaired:$class:<sha40>" "unrepaired:$class:$SHA_B" needs-input block "$CIK" ci-repair 7 "unrepaired:$class:$SHA_B"
done
l1_ok "L1 advance diagnosed after a needs-input block" diagnosed running advance "$CIK" ci-repair 7 diagnosed

l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 64-hex setup init" 0
l1_ok "L1 advance committed:<sha64>" "committed:$SHA_64" running advance "$CIK" ci-repair 7 "committed:$SHA_64"
l1_ok "L1 complete pushed:<sha64>" "pushed:$SHA_64" complete complete "$CIK" ci-repair 7 "pushed:$SHA_64"
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 64-hex block setup init" 0
l1_ok "L1 block gate-red:<sha64>" "gate-red:$SHA_64" needs-input block "$CIK" ci-repair 7 "gate-red:$SHA_64"
l1_ok "L1 block unrepaired:infra:<sha64>" "unrepaired:infra:$SHA_64" needs-input block "$CIK" ci-repair 7 "unrepaired:infra:$SHA_64"

# --- reject: malformed phases (AC39); one case per label, file bytes unchanged ---
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 reject setup init" 0
run "$REPO" advance "$CIK" ci-repair 7 diagnosed; expect_rc "L1 reject setup advance" 0
SHA_UPPER="$(printf '%s' "$SHA_B" | tr 'a-f' 'A-F')" || fail "L1 fixture: tr failed"
declare -a BAD_SHAS=(
  "non-hex|${SHA_B:0:39}g"
  "39-char|${SHA_B:0:39}"
  "41-char|${SHA_B}0"
  "7-char|${SHA_B:0:7}"
  "63-char|${SHA_64:0:63}"
  "65-char|${SHA_64}0"
  "uppercase|${SHA_UPPER}"
  "empty|"
)
PHRASE="is not a phase of workflow ci-repair"
for entry in "${BAD_SHAS[@]}"; do
  kind="${entry%%|*}"; bad="${entry#*|}"
  reject "L1 reject committed with $kind suffix" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 "committed:$bad"
  reject "L1 reject pushed with $kind suffix" "$CIK" "$PHRASE" complete "$CIK" ci-repair 7 "pushed:$bad"
  reject "L1 reject gate-red with $kind suffix" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "gate-red:$bad"
  reject "L1 reject unrepaired with $kind suffix" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:ambiguous:$bad"
done
reject "L1 reject suffix on diagnosed" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 "diagnosed:$SHA_B"
reject "L1 reject suffix on reproduced" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 "reproduced:$SHA_B"
reject "L1 reject suffix on gate-green" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 "gate-green:$SHA_B"
reject "L1 reject suffix on planned" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 "planned:$SHA_B"
reject "L1 reject bare committed" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 committed
reject "L1 reject bare pushed" "$CIK" "$PHRASE" complete "$CIK" ci-repair 7 pushed
reject "L1 reject bare gate-red" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 gate-red
reject "L1 reject bare unrepaired" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 unrepaired
reject "L1 reject empty phase argument" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 ""
reject "L1 reject unrepaired with a class outside the enum" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:bogus:$SHA_B"
# single-line format: the phase problem adds no ';' of its own, so the only separator is the remedy's
semicolons="$(grep -o '; ' "$T/stderr" | wc -l | tr -d '[:space:]')"
[[ "$semicolons" == "1" ]] || fail "L1 phase rejection must contain exactly one '; ' separator (got $semicolons: $(cat "$T/stderr"))"
reject "L1 reject unrepaired with an uppercase class" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:AMBIGUOUS:$SHA_B"
reject "L1 reject unrepaired with a sha and no class" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:$SHA_B"
reject "L1 reject unrepaired with a class and no sha" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:ambiguous"
reject "L1 reject unrepaired with a class and an empty sha" "$CIK" "$PHRASE" block "$CIK" ci-repair 7 "unrepaired:ambiguous:"
reject "L1 reject a phase of another workflow" "$CIK" "$PHRASE" advance "$CIK" ci-repair 7 baseline
reject "L1 reject init with a non-planned phase" "$CIK" "init only accepts the planned phase" init "$CIK" ci-repair 7 "pushed:$SHA_B"
# the same grammar guards a hand-edited file, and ci-repair has no gate to record
mutate "$CIK" '.phase = "pushed"'
reject "L1 file with a bare pushed phase" "$CIK" "invalid field phase" get "$CIK"
mutate "$CIK" '.phase = "pushed:'"${SHA_B:0:39}"'"'
reject "L1 file with a 39-char pushed suffix" "$CIK" "invalid field phase" get "$CIK"
mutate "$CIK" '.phase = "unrepaired:bogus:'"$SHA_B"'"'
reject "L1 file with an unknown unrepaired class" "$CIK" "invalid field phase" get "$CIK"
mutate "$CIK" '.phase = "diagnosed" | .lastCompletedGate = "diagnosed"'
reject "L1 file with a recorded gate" "$CIK" "invalid field lastCompletedGate" get "$CIK"

# --- reject: --gate is a usage error for ci-repair ---
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 gate setup init" 0
reject "L1 --gate on advance" "$CIK" "--gate is not valid for ci-repair" advance "$CIK" ci-repair 7 reproduced --gate diagnosed
reject "L1 --gate on block" "$CIK" "--gate is not valid for ci-repair" block "$CIK" ci-repair 7 "gate-red:$SHA_B" --gate diagnosed
reject "L1 --gate on complete" "$CIK" "--gate is not valid for ci-repair" complete "$CIK" ci-repair 7 "pushed:$SHA_B" --gate gate-green
reject "L1 --gate planned on advance" "$CIK" "--gate is not valid for ci-repair" advance "$CIK" ci-repair 7 diagnosed --gate planned
l1_repo
reject "L1 --gate on init creates nothing" "$CIK" "--gate is not valid for ci-repair" init "$CIK" ci-repair 7 planned --gate diagnosed

# --- restart: complete|pushed:<sha> -> advance diagnosed (AC40) and edge 62 (second cycle) ---
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned --worktree .worktrees/ci-7; expect_rc "L1 restart setup init" 0
run "$REPO" advance "$CIK" ci-repair 7 "committed:$SHA_B"; expect_rc "L1 restart setup advance" 0
run "$REPO" complete "$CIK" ci-repair 7 "pushed:$SHA_B"; expect_rc "L1 restart setup complete" 0
reject "L1 complete|pushed: advance reproduced is terminal" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 reproduced
reject "L1 complete|pushed: advance gate-green is terminal" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 gate-green
reject "L1 complete|pushed: advance committed is terminal" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 "committed:$SHA_C"
reject "L1 complete|pushed: advance pushed is terminal" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 "pushed:$SHA_C"
reject "L1 complete|pushed: block diagnosed is terminal" "$CIK" "checkpoint is complete (terminal)" block "$CIK" ci-repair 7 diagnosed
reject "L1 complete|pushed: block gate-red is terminal" "$CIK" "checkpoint is complete (terminal)" block "$CIK" ci-repair 7 "gate-red:$SHA_B"
reject "L1 complete|pushed: block unrepaired is terminal" "$CIK" "checkpoint is complete (terminal)" block "$CIK" ci-repair 7 "unrepaired:flaky:$SHA_B"
reject "L1 complete|pushed: complete pushed is terminal" "$CIK" "checkpoint is complete (terminal)" complete "$CIK" ci-repair 7 "pushed:$SHA_B"
reject "L1 complete|pushed: complete diagnosed is terminal" "$CIK" "checkpoint is complete (terminal)" complete "$CIK" ci-repair 7 diagnosed
l1_ok "L1 restart: advance diagnosed from complete|pushed" diagnosed running advance "$CIK" ci-repair 7 diagnosed
expect_jq "L1 restart keeps the file and its other fields" '.worktree == ".worktrees/ci-7" and .schemaVersion == 2 and .target == "7" and (keys | length) == 12' "$CIK"
run "$REPO" get "$CIK" ci-repair 7; expect_rc "L1 get after restart" 0
l1_state "L1 get after restart" diagnosed running
l1_ok "L1 restart cycle: advance reproduced" reproduced running advance "$CIK" ci-repair 7 reproduced
l1_ok "L1 restart cycle: advance gate-green" gate-green running advance "$CIK" ci-repair 7 gate-green
l1_ok "L1 restart cycle: advance committed:<C>" "committed:$SHA_C" running advance "$CIK" ci-repair 7 "committed:$SHA_C"
l1_ok "L1 restart cycle: complete pushed:<C>" "pushed:$SHA_C" complete complete "$CIK" ci-repair 7 "pushed:$SHA_C"
reject "L1 second cycle: advance reproduced is terminal" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 reproduced
reject "L1 second cycle: block diagnosed is terminal" "$CIK" "checkpoint is complete (terminal)" block "$CIK" ci-repair 7 diagnosed
reject "L1 second cycle: complete is terminal" "$CIK" "checkpoint is complete (terminal)" complete "$CIK" ci-repair 7 "pushed:$SHA_C"
l1_ok "L1 edge 62: restart again from complete|pushed:<C>" diagnosed running advance "$CIK" ci-repair 7 diagnosed

# the restart move never applies to another complete phase
for phase in diagnosed reproduced gate-green "committed:$SHA_B" "gate-red:$SHA_B" "unrepaired:ambiguous:$SHA_B"; do
  l1_repo
  run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 terminal setup init ($phase)" 0
  run "$REPO" complete "$CIK" ci-repair 7 "$phase"; expect_rc "L1 terminal setup complete ($phase)" 0
  reject "L1 restart refused from complete|$phase" "$CIK" "checkpoint is complete (terminal)" advance "$CIK" ci-repair 7 diagnosed
done

# edge 60 (helper side): a failed restart write leaves the complete|pushed checkpoint untouched
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 edge 60 setup init" 0
run "$REPO" complete "$CIK" ci-repair 7 "pushed:$SHA_B"; expect_rc "L1 edge 60 setup complete" 0
cp "$CIK" "$T/l1-complete.json" || fail "L1 edge 60: snapshot failed"
run_fault "$REPO" advance "$CIK" ci-repair 7 diagnosed
expect_rc "L1 edge 60 restart write failure exits 1" 1
expect_no_stdout "L1 edge 60 restart write failure"
expect_stderr "L1 edge 60 restart write failure names the problem" "write failed (EIO)"
cmp -s "$T/l1-complete.json" "$CIK" || fail "L1 edge 60: a failed restart advance must leave complete|pushed byte-identical"

# --- other workflows gain no restart and no SHA phases (AC41) ---
new_repo
run "$REPO" init "$REL" implement 42 planned; expect_rc "L1 implement setup init" 0
reject "L1 implement rejects a pushed:<sha> phase" "$CK" "is not a phase of workflow implement" advance "$REL" implement 42 "pushed:$SHA_B"
reject "L1 implement rejects a bare diagnosed phase" "$CK" "is not a phase of workflow implement" advance "$REL" implement 42 diagnosed
run "$REPO" complete "$REL" implement 42 pr; expect_rc "L1 implement setup complete" 0
reject "L1 implement has no restart move" "$CK" "checkpoint is complete (terminal)" advance "$REL" implement 42 tests
run "$REPO" init "$REL" implement 42; expect_rc "L1 implement init after complete is still a no-op" 0

# --- verify keeps refusing a plan-less or complete ci-repair checkpoint ---
l1_repo
run "$REPO" init "$CIK" ci-repair 7 planned; expect_rc "L1 verify setup init" 0
reject "L1 verify refuses a plan-less ci-repair checkpoint" "$CIK" "checkpoint records no approved plan" verify "$CIK" ci-repair 7
run "$REPO" complete "$CIK" ci-repair 7 "pushed:$SHA_B"; expect_rc "L1 verify setup complete" 0
reject "L1 verify refuses a complete ci-repair checkpoint" "$CIK" "checkpoint is complete (terminal)" verify "$CIK" ci-repair 7

# --- legacy v1 ci-repair files (edge 63) ---
l1_write_v1() {
  mkdir -p "$L1_ROOT/.cenci/checkpoints" || fail "L1: mkdir failed"
  cat > "$CIK" <<EOF
{"schemaVersion":1,"workflow":"ci-repair","target":"7","phase":"pushed:$SHA_B","status":"complete","planPath":null,"worktree":null,"lastCompletedGate":null,"agentFallbacks":[],"updatedAt":"2026-01-01T00:00:00.000Z"}
EOF
}
l1_repo
l1_write_v1
run "$REPO" get "$CIK"; expect_rc "L1 get v1 ci-repair" 0
expect_stdout_jq "L1 get marks the v1 ci-repair file legacy" '.legacy == true and .schemaVersion == 1 and .phase == "pushed:'"$SHA_B"'"'
reject "L1 v1 ci-repair: advance gives the legacy error" "$CIK" "legacy v1 checkpoint; run init to upgrade it" advance "$CIK" ci-repair 7 diagnosed
reject "L1 v1 ci-repair: block gives the legacy error" "$CIK" "legacy v1 checkpoint; run init to upgrade it" block "$CIK" ci-repair 7 "gate-red:$SHA_B"
reject "L1 v1 ci-repair: complete gives the legacy error" "$CIK" "legacy v1 checkpoint; run init to upgrade it" complete "$CIK" ci-repair 7 "pushed:$SHA_B"
run "$REPO" init "$CIK" ci-repair 7; expect_rc "L1 init upgrades an identity-valid v1 ci-repair file" 0
expect_jq "L1 v1 ci-repair upgraded in place" '.schemaVersion == 2 and .workflow == "ci-repair" and .phase == "pushed:'"$SHA_B"'" and .status == "complete" and .lastCompletedGate == null and (keys | length) == 12' "$CIK"
l1_ok "L1 restart works on the upgraded file" diagnosed running advance "$CIK" ci-repair 7 diagnosed
for bad in "pushed" "pushed:${SHA_B:0:39}" "unrepaired:bogus:$SHA_B" "diagnosed:$SHA_B"; do
  l1_repo
  l1_write_v1; mutate "$CIK" '.phase = "'"$bad"'"'
  reject "L1 v1 ci-repair with phase [$bad] cannot be upgraded" "$CIK" "cannot be upgraded" init "$CIK" ci-repair 7
done
l1_repo
l1_write_v1; mutate "$CIK" '.lastCompletedGate = "diagnosed"'
reject "L1 v1 ci-repair with a recorded gate cannot be upgraded" "$CIK" "cannot be upgraded" init "$CIK" ci-repair 7
# the literal target-less emulator shape is not identity-valid
l1_repo
l1_write_v1; mutate "$CIK" 'del(.target)'
reject "L1 target-less v1 ci-repair: init" "$CIK" "invalid field target" init "$CIK" ci-repair 7
reject "L1 target-less v1 ci-repair: get" "$CIK" "invalid field target" get "$CIK"
reject "L1 target-less v1 ci-repair: advance" "$CIK" "invalid field target" advance "$CIK" ci-repair 7 diagnosed

echo "runtime.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]] && echo "runtime.test.sh: passed"
[[ "$failures" -eq 0 ]]
