#!/usr/bin/env bash
# require-runtime.test.sh -- #1233 B9: the Codex normal-mode prerequisite gate.
# Drives the real flow/codex/require-runtime.sh under a controlled PATH that holds
# only mock gh/git (which record every call) and, per case, a fake `node`.
# Also pins the codex-runtime SKILL.md ordering contract (the require-runtime step
# precedes every checkpoint step) and that install.sh's doctor threshold equals
# the script's. Follows flow/docs/shell-scripting-gotchas.md: exact literals per
# case, and every doc-sentence assertion is paired with a behavior case that
# exercises the mechanism it describes.
set -uo pipefail
FLOW="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || { echo "require-runtime.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "$FLOW/.." && pwd)" || { echo "require-runtime.test.sh: failed to resolve repo root." >&2; exit 2; }
SCRIPT="$FLOW/codex/require-runtime.sh"
SKILL="$FLOW/skills/codex-runtime/SKILL.md"
INSTALL_SH="$REPO_ROOT/install.sh"
T="$(mktemp -d)" || { echo "require-runtime.test.sh: mktemp failed." >&2; exit 2; }
trap 'rm -rf "$T"' EXIT
SH_BIN="$(command -v sh)" || { echo "require-runtime.test.sh: no sh on PATH." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# run_case <name> <node-mode> -- builds a PATH of only mock gh/git (plus a fake node
# unless <node-mode> is "none"), runs the script from an empty working directory,
# and sets RC, $T/<name>/stdout, $T/<name>/stderr, $T/<name>/calls.
#   none            no node on PATH
#   print:<text>    node prints <text> for any invocation, exit 0
#   exit1           node prints nothing and exits 1
run_case() {
  local name="$1" mode="$2" dir="$T/$1"
  mkdir -p "$dir/bin" "$dir/work" || { echo "require-runtime.test.sh: mkdir failed." >&2; exit 2; }
  : > "$dir/calls"
  local tool
  for tool in gh git; do
    printf '#!/bin/sh\nprintf "%%s %%s\\n" "%s" "$*" >> "%s"\nexit 0\n' "$tool" "$dir/calls" > "$dir/bin/$tool"
    chmod +x "$dir/bin/$tool"
  done
  case "$mode" in
    none) ;;
    exit1) printf '#!/bin/sh\nexit 1\n' > "$dir/bin/node"; chmod +x "$dir/bin/node" ;;
    print:*)
      # The fake node reads its output from a file so no case text is ever shell-evaluated by the fixture.
      printf '%s\n' "${mode#print:}" > "$dir/node-out"
      printf '#!/bin/sh\nIFS= read -r line < "%s"\nprintf '"'"'%%s\\n'"'"' "$line"\n' "$dir/node-out" > "$dir/bin/node"
      chmod +x "$dir/bin/node" ;;
    *) echo "require-runtime.test.sh: bad node mode $mode" >&2; exit 2 ;;
  esac
  ( cd "$dir/work" && env -i HOME="$dir" PATH="$dir/bin" "$SH_BIN" "$SCRIPT" ) >"$dir/stdout" 2>"$dir/stderr"
  RC=$?
  CASE_DIR="$dir"
}

message() {
  printf 'cenci: Codex workflow runtime unavailable — Node.js 18+ not found on PATH (found: %s). Install Node.js 18 or newer, then rerun; no checkpoint, workflow, or GitHub state was changed.' "$1"
}

# expect_rejected <label> <found-literal>
expect_rejected() {
  local label="$1" found="$2" err
  [[ "$RC" -eq 2 ]] || fail "$label: expected exit 2, got $RC"
  err="$(cat "$CASE_DIR/stderr")"
  [[ "$err" == "$(message "$found")" ]] || fail "$label: stderr mismatch (got: $err)"
  [[ ! -s "$CASE_DIR/stdout" ]] || fail "$label: stdout must be empty (got: $(cat "$CASE_DIR/stdout"))"
  [[ ! -s "$CASE_DIR/calls" ]] || fail "$label: no gh/git call may happen (got: $(cat "$CASE_DIR/calls"))"
  { [[ ! -e "$CASE_DIR/work/.cenci" ]]; } || fail "$label: .cenci/checkpoints must not be created"
  [[ -z "$(ls -A "$CASE_DIR/work")" ]] || fail "$label: the working directory must stay empty (got: $(ls -A "$CASE_DIR/work"))"
}

# expect_accepted <label>
expect_accepted() {
  local label="$1"
  [[ "$RC" -eq 0 ]] || fail "$label: expected exit 0, got $RC (stderr: $(cat "$CASE_DIR/stderr"))"
  [[ ! -s "$CASE_DIR/stdout" ]] || fail "$label: must be silent on stdout (got: $(cat "$CASE_DIR/stdout"))"
  [[ ! -s "$CASE_DIR/stderr" ]] || fail "$label: must be silent on stderr (got: $(cat "$CASE_DIR/stderr"))"
  [[ ! -s "$CASE_DIR/calls" ]] || fail "$label: the gate itself must not call gh/git (got: $(cat "$CASE_DIR/calls"))"
}

[[ -f "$SCRIPT" ]] || fail "flow/codex/require-runtime.sh does not exist"

# ===========================================================================
# B9 -- Node absent from PATH: actionable message, no state or GitHub mutation.
# ===========================================================================
run_case no-node none
expect_rejected "B9 no node on PATH" "none"

# unsupported and unparseable versions are rejected, naming what was found
run_case node-16 "print:v16.20.0";  expect_rejected "B9 node v16.20.0" "16.20.0"
run_case node-17 "print:v17.9.1";   expect_rejected "B9 node v17.9.1" "17.9.1"
run_case node-garbage "print:banana"; expect_rejected "B9 garbage version output" "unparseable"
run_case node-empty "print:";        expect_rejected "B9 empty version output" "unparseable"
run_case node-bare-v "print:v";      expect_rejected "B9 bare v" "unparseable"
run_case node-nan "print:vNaN.1.2"; expect_rejected "B9 non-numeric major" "unparseable"
run_case node-two-part "print:v20.1"; expect_rejected "B9 two-part version" "unparseable"
run_case node-exit1 exit1;           expect_rejected "B9 node --version failing" "unparseable"
# Injection payloads use shell builtins only (the controlled PATH holds no `touch`), so a
# naive evaluation would really create `pwned` in the working directory.
INJECT_SUBST='v16.0.0$(: > pwned)'
INJECT_BACKTICK='v16.0.0`echo x > pwned`'
INJECT_ACCEPTED='v20.0.0-$(: > pwned)'
run_case node-injection "print:$INJECT_SUBST"; expect_rejected "B9 version text is never evaluated" "unparseable"
{ [[ ! -e "$T/node-injection/work/pwned" ]]; } || fail "B9: version text was evaluated by the shell"
run_case node-injection-backtick "print:$INJECT_BACKTICK"; expect_rejected "B9 backtick version text is never evaluated" "unparseable"
{ [[ ! -e "$T/node-injection-backtick/work/pwned" ]]; } || fail "B9: backtick version text was evaluated by the shell"
# An accepted version suffix is not evaluated either.
run_case node-injection-suffix "print:$INJECT_ACCEPTED"; expect_accepted "B9 accepted suffix is not evaluated"
{ [[ ! -e "$T/node-injection-suffix/work/pwned" ]]; } || fail "B9: an accepted version suffix was evaluated by the shell"
# Positive control: a naive shell that evaluates these payloads DOES create the file, so the
# absence assertions above can actually fail.
for payload in "$INJECT_SUBST" "$INJECT_BACKTICK" "$INJECT_ACCEPTED"; do
  control="$(mktemp -d "$T/control.XXXXXX")" || { echo "require-runtime.test.sh: mktemp failed." >&2; exit 2; }
  ( cd "$control" && eval "naive=\"$payload\"" ) >/dev/null 2>&1
  [[ -e "$control/pwned" ]] || fail "positive control: evaluating [$payload] naively must create pwned"
done

# supported versions pass silently
run_case node-18 "print:v18.0.0";   expect_accepted "B9 node v18.0.0 (the minimum)"
run_case node-20 "print:v20.11.0";  expect_accepted "B9 node v20.11.0"
run_case node-24 "print:v24.1.0";   expect_accepted "B9 node v24.1.0"
run_case node-prerelease "print:v22.0.0-nightly20240101"; expect_accepted "B9 pre-release tag on a supported major"

# ===========================================================================
# SKILL.md ordering contract: the require-runtime step precedes every checkpoint
# step and states "before any checkpoint, workflow, or GitHub mutation".
# Pure predicate (no fail() inside), so it can be checked against injected copies.
# ===========================================================================
REQ_CMD='sh "${PLUGIN_ROOT}/codex/require-runtime.sh"'
REQ_PHRASE='before any checkpoint, workflow, or GitHub mutation'
REQ_STOP='stop and report'
CKPT_CMD='codex/checkpoint.mjs'

# skill_order_ok <file> -- 0 only when the first line naming the require-runtime
# command also states the phrase and the stop rule, and precedes the first line
# naming the checkpoint helper.
skill_order_ok() {
  local file="$1" req_line ckpt_line line
  [[ -f "$file" ]] || return 1
  req_line="$(grep -nF -- "$REQ_CMD" "$file" | head -1 | cut -d: -f1)"
  ckpt_line="$(grep -nF -- "$CKPT_CMD" "$file" | head -1 | cut -d: -f1)"
  [[ -n "$req_line" && -n "$ckpt_line" ]] || return 1
  line="$(sed -n "${req_line}p" "$file")"
  [[ "$line" == *"$REQ_PHRASE"* ]] || return 1
  [[ "$line" == *"$REQ_STOP"* ]] || return 1
  [[ "$req_line" -lt "$ckpt_line" ]]
}

skill_order_ok "$SKILL" || fail "SKILL.md: the require-runtime bullet must come before the checkpoint bullet and state '$REQ_PHRASE' and '$REQ_STOP' on its own line"
grep -qF -- 'plan path, worktree, last completed gate, and status' "$SKILL" || fail "SKILL.md must keep the literal 'plan path, worktree, last completed gate, and status'"
grep -qF -- '.cenci/checkpoints' "$SKILL" || fail "SKILL.md must keep the literal '.cenci/checkpoints'"

# non-vacuity: each way of breaking the contract must be caught
if [[ -f "$SKILL" ]]; then
  # (a) checkpoint bullet moved ahead of the require-runtime bullet
  {
    echo "- Persist with node \"\${PLUGIN_ROOT}/codex/checkpoint.mjs\" first."
    cat "$SKILL"
  } > "$T/skill-reordered.md"
  skill_order_ok "$T/skill-reordered.md" && fail "non-vacuity: a checkpoint line ahead of the require-runtime line was not detected"
  # (b) the 'before any ... mutation' phrase removed
  sed "s/$REQ_PHRASE/before something/" "$SKILL" > "$T/skill-nophrase.md"
  skill_order_ok "$T/skill-nophrase.md" && fail "non-vacuity: a missing '$REQ_PHRASE' was not detected"
  # (c) the stop rule removed
  sed "s/$REQ_STOP/continue/" "$SKILL" > "$T/skill-nostop.md"
  skill_order_ok "$T/skill-nostop.md" && fail "non-vacuity: a missing '$REQ_STOP' was not detected"
  # (d) the require-runtime command removed
  grep -vF -- "$REQ_CMD" "$SKILL" > "$T/skill-nocmd.md"
  skill_order_ok "$T/skill-nocmd.md" && fail "non-vacuity: a missing require-runtime command was not detected"
fi

# ===========================================================================
# install.sh's doctor threshold equals the script's (B10 stays in sync).
# ===========================================================================
script_min="$(sed -n 's/^MIN_NODE_MAJOR=\([0-9][0-9]*\)$/\1/p' "$SCRIPT" 2>/dev/null)"
install_min="$(sed -n 's/^[[:space:]]*local node_min_major=\([0-9][0-9]*\)$/\1/p' "$INSTALL_SH" 2>/dev/null)"
if [[ -z "$script_min" ]]; then fail "require-runtime.sh must define MIN_NODE_MAJOR=<n> on its own line"; fi
if [[ -z "$install_min" ]]; then fail "install.sh doctor_codex_support must define 'local node_min_major=<n>' on its own line"; fi
if [[ -n "$script_min" && -n "$install_min" ]]; then
  [[ "$script_min" == "$install_min" ]] || fail "install.sh node_min_major ($install_min) must equal require-runtime.sh MIN_NODE_MAJOR ($script_min)"
  [[ "$script_min" == "18" ]] || fail "the Node threshold must be 18 (got $script_min)"
fi

echo "require-runtime.test.sh: failures=${failures}"
[[ "$failures" -eq 0 ]] && echo "require-runtime.test.sh: passed"
[[ "$failures" -eq 0 ]]
