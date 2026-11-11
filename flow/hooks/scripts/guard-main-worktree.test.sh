#!/usr/bin/env bash
# Tests for guard-main-worktree.sh. Follows the repo's shell-test precedent
# (flow/hooks/scripts/check-sensitive-files.test.sh, sandbox/tests/*.test.sh):
# plain bash, no framework, PASS/FAIL counters, non-zero exit on failure.
# Each case runs in a fresh directory under one mktemp root and drives the
# hook script with PreToolUse-style JSON on stdin.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD_SH="${SCRIPT_DIR}/guard-main-worktree.sh"
# Interpreter used to invoke the hook. Defaults to sh (the shebang's
# interpreter); the summary block re-execs this suite once per additional
# available shell so bash-only bugs cannot hide behind whichever shell
# /bin/sh happens to be on this host.
HOOK_SHELL="${HOOK_SHELL:-sh}"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# run_guard <cwd> <json> — runs the hook from <cwd> with <json> on stdin.
# Sets GUARD_EXIT and GUARD_STDERR.
run_guard() {
    local cwd="$1" json="$2"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${GUARD_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${GUARD_EXIT}, expected ${expected}"
    fi
}

# run_guard_with_path <cwd> <path_override> <json> — like run_guard, but
# overrides PATH with a curated bin dir before invoking the hook. Used to
# simulate a PATH missing specific external tools (fail-closed tests).
# Sets GUARD_EXIT and GUARD_STDERR.
run_guard_with_path() {
    local cwd="$1" path_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | PATH="${path_override}" "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# run_guard_with_tmpdir <cwd> <tmpdir_override> <json> — like run_guard, but
# sets TMPDIR to <tmpdir_override> for a single invocation. Used to drive the
# #749 TMPDIR-widening cases below. Sets GUARD_EXIT and GUARD_STDERR.
run_guard_with_tmpdir() {
    local cwd="$1" tmpdir_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | TMPDIR="${tmpdir_override}" "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# make_curated_bin <dir> <tool>... — creates <dir> containing symlinks to the
# real binaries for each named tool (resolved from the current PATH) and
# nothing else. Used with run_guard_with_path to simulate a PATH that is
# missing specific tools.
make_curated_bin() {
    local dir="$1"
    shift
    mkdir -p "${dir}"
    local tool real
    for tool in "$@"; do
        real="$(command -v "${tool}")" || {
            echo "make_curated_bin: '${tool}' not found on PATH" >&2
            exit 1
        }
        ln -s "${real}" "${dir}/${tool}"
    done
}

# make_mktemp_fail_bin <dir> <rm_log> <tool>... — like make_curated_bin, but
# additionally plants a fake `mktemp` that always fails (exit 1, no output,
# simulating e.g. TMPDIR exhaustion) and a fake `rm` that appends its argv to
# <rm_log> (one invocation per line) before delegating to the real `rm` (its
# path captured at shim-creation time via `command -v rm`, never hardcoded).
# Used to prove the mktemp-failure fallback (#550): the fallback must never
# assign /dev/null to a path later passed to rm, and must not drop jq's own
# diagnostic.
make_mktemp_fail_bin() {
    local dir="$1" rm_log="$2"
    shift 2
    mkdir -p "${dir}"
    cat > "${dir}/mktemp" <<'SCRIPT'
#!/bin/sh
exit 1
SCRIPT
    chmod +x "${dir}/mktemp"
    local real_rm
    real_rm="$(command -v rm)" || {
        echo "make_mktemp_fail_bin: 'rm' not found on PATH" >&2
        exit 1
    }
    cat > "${dir}/rm" <<SCRIPT
#!/bin/sh
printf '%s\n' "\$*" >> '${rm_log}'
exec '${real_rm}' "\$@"
SCRIPT
    chmod +x "${dir}/rm"
    local tool real
    for tool in "$@"; do
        real="$(command -v "${tool}")" || {
            echo "make_mktemp_fail_bin: '${tool}' not found on PATH" >&2
            exit 1
        }
        ln -s "${real}" "${dir}/${tool}"
    done
}

echo "guard-main-worktree.test.sh"

# Not under /tmp: the guard allowlists /tmp/* paths, which would make every
# case exit 0 regardless of the config gate. /var/tmp is not allowlisted.
TEST_ROOT="$(mktemp -d /var/tmp/guard-main-worktree-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# Normalize TMPDIR for the whole suite (#749): TEST_ROOT deliberately lives
# under /var/tmp specifically to dodge the /tmp/* arm, and run-checks.sh:137
# runs every suite with the ambient environment -- an inherited
# TMPDIR=/var/tmp (or any other custom value) would silently widen the new
# TMPDIR allowlist and turn every pre-existing "should still be blocked" case
# into a false pass. Each #749 case below sets TMPDIR explicitly for its own
# single invocation via run_guard_with_tmpdir; every other case must run with
# TMPDIR genuinely unset.
unset TMPDIR
# #1212: the capture tap is opt-in per invocation; an inherited value would make
# every case write evidence files.
unset CENCI_CODEX_HOOK_CAPTURE_DIR

make_git_repo() {
    local dir="$1"
    mkdir -p "${dir}"
    git -C "${dir}" init -q
}

# ── Case 1: unconfigured git repo → guard is a no-op (the bug fix) ──
echo "case: unconfigured git repo allows source writes"
REPO="${TEST_ROOT}/unconfigured"
make_git_repo "${REPO}"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "unconfigured repo" 0

# ── Case 2: non-git directory, no config → no-op ────────────────────
echo "case: non-git directory allows source writes"
DIR="${TEST_ROOT}/plain-dir"
mkdir -p "${DIR}"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir" 0

# ── Case 3: configured repo → source writes blocked ─────────────────
echo "case: configured repo blocks source writes"
REPO="${TEST_ROOT}/configured"
make_git_repo "${REPO}"
mkdir -p "${REPO}/.cenci"
touch "${REPO}/.cenci/config.json"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "configured repo" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "configured repo: stderr should contain BLOCKED"
fi

# ── Case 4: configured repo, .worktrees/ path → allowlisted ─────────
echo "case: configured repo allows .worktrees/ writes"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/1-feat/src/foo.txt\"}}"
assert_exit "worktree path" 0

# ── Case 5: configured repo, cwd in a subdirectory → still blocked ──
echo "case: configured repo blocks from a subdirectory cwd"
mkdir -p "${REPO}/src/nested"
run_guard "${REPO}/src/nested" "{\"tool_input\":{\"file_path\":\"${REPO}/src/foo.txt\"}}"
assert_exit "subdirectory cwd" 2

# ── Case 6: missing file_path → no-op (existing behavior) ───────────
echo "case: missing file_path is a no-op"
run_guard "${REPO}" "{\"tool_input\":{}}"
assert_exit "missing file_path" 0

# ── Case 7: configured repo, .cenci/ path → blocked (configure now ships
# its own writes through a feature worktree + PR, so no main-worktree
# carve-out is needed for it) ────────────────────────────────────────
echo "case: configured repo blocks .cenci/ writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.cenci/Dockerfile\"}}"
assert_exit ".cenci path" 2

echo "case: configured repo blocks AGENTS.md writes outside a worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/AGENTS.md\"}}"
assert_exit "AGENTS.md path" 2

echo "case: configured repo allows .cenci/ writes inside a feature worktree"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.worktrees/configure-init/.cenci/Dockerfile\"}}"
assert_exit ".cenci path inside worktree" 0

echo "case: legacy-only configured repo remains protected"
LEGACY_REPO="${TEST_ROOT}/legacy-configured"
make_git_repo "${LEGACY_REPO}"
mkdir -p "${LEGACY_REPO}/.claude"
touch "${LEGACY_REPO}/.claude/config.json"
run_guard "${LEGACY_REPO}" "{\"tool_input\":{\"file_path\":\"${LEGACY_REPO}/src/foo.txt\"}}"
assert_exit "legacy configured repo" 2

# ── Case 8: non-git dir with its own .claude/config.json → no-op ────
# Pre-fix, ROOT=$(pwd) fallback would let this directory's own config.json
# satisfy the gate and incorrectly enforce (#371).
echo "case: non-git directory with .claude/config.json is still a no-op"
DIR="${TEST_ROOT}/non-git-with-config"
mkdir -p "${DIR}/.claude"
touch "${DIR}/.claude/config.json"
run_guard "${DIR}" "{\"tool_input\":{\"file_path\":\"${DIR}/src/foo.txt\"}}"
assert_exit "non-git dir with config.json" 0

# ── Case 9: configured repo, native Plan Mode storage under the sandbox
# container's home (/home/dev/.claude/plans/) → allowlisted (#431). The
# hook only ever sees an absolute file_path, never a real $HOME, so this
# exercises the container-home shape of that path.
echo "case: configured repo allows .claude/plans/ writes under container home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"/home/dev/.claude/plans/plan.md\"}}"
assert_exit "container home .claude/plans path" 0

# ── Case 10: configured repo, native Plan Mode storage under an arbitrary
# host $HOME (not /home/dev) → allowlisted (#431). Proves the match is
# portable across $HOME values, not hardcoded to the container's home.
echo "case: configured repo allows .claude/plans/ writes under an arbitrary host home"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/home-host/.claude/plans/plan.md\"}}"
assert_exit "arbitrary host home .claude/plans path" 0

# ── Case 11: configured repo, source path adjacent to .claude but not under
# plans/ → still blocked (#431). Guards against an over-broad glob that
# would allow any .claude*-prefixed path.
echo "case: configured repo blocks source paths that merely resemble .claude/plans/"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/src/.claude-notes.md\"}}"
assert_exit "adjacent-to-.claude source path" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "adjacent-to-.claude source path: stderr should contain BLOCKED"
fi

# ── Hardening cases (#440): normalize file_path and use real JSON parsing ──
# Run against a dedicated repo so these cases don't depend on directory
# state left behind by earlier cases (e.g. case 5 creates $REPO/src/nested).
HARDEN_REPO="${TEST_ROOT}/hardening"
make_git_repo "${HARDEN_REPO}"
mkdir -p "${HARDEN_REPO}/.cenci"
touch "${HARDEN_REPO}/.cenci/config.json"

# Case: a "../.." substring makes the raw path contain /.worktrees/ (an
# allowlisted substring) but it must be normalized (.. collapsed) before the
# allowlist check — it canonicalizes to $HARDEN_REPO/src/evil.txt, outside
# any worktree. $HARDEN_REPO/src must not exist so this exercises
# normalization of a path whose target does not exist yet.
echo "case: ..-substring path is normalized before the allowlist match and blocked"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/x/../../src/evil.txt\"}}"
assert_exit "dot-dot substring bypass" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "dot-dot substring bypass: stderr should contain BLOCKED"
fi

# Case: a symlink planted under an allowlisted directory (.worktrees/) that
# resolves OUTSIDE the repo is now allowed outright (#1072 flips this from a
# block to an allow -- an out-of-repo canonical target, however it's reached,
# is out of this guard's scope; see the SCOPE_ROOT header comment). The
# canonicalize-before-decide intent this case originally pinned (a lexical
# allowlist-substring match is not enough; the target must be resolved first)
# is preserved by the in-repo twin immediately below, whose resolved target
# lands back inside the repo and must still block.
echo "case: symlink under .worktrees/ resolving outside the repo is allowed (#1072, flipped from blocked)"
mkdir -p "${TEST_ROOT}/outside"
mkdir -p "${HARDEN_REPO}/.worktrees"
ln -s "${TEST_ROOT}/outside" "${HARDEN_REPO}/.worktrees/link"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link/evil.txt\"}}"
assert_exit "symlink escape via .worktrees/ to outside the repo allowed (#1072)" 0

# In-repo twin (#1072): the same symlink shape, but resolving to somewhere
# ELSE inside the repo (not under .worktrees/) -- preserves the original
# case's canonicalize-before-decide intent: the literal ".worktrees/" path
# substring is not enough, the resolved target still decides, and a resolved
# in-repo, non-allowlisted target must still block.
echo "case: symlink under .worktrees/ resolving to a non-allowlisted in-repo path is blocked (#1072 twin)"
mkdir -p "${HARDEN_REPO}/protected-dir/existingsub"
ln -s "${HARDEN_REPO}/protected-dir" "${HARDEN_REPO}/.worktrees/link-inrepo"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link-inrepo/evil.txt\"}}"
assert_exit "symlink escape via .worktrees/ to an in-repo path blocked (#1072 twin)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink escape via .worktrees/ to an in-repo path blocked: stderr should contain BLOCKED"
fi

# Case: a symlinked allowlisted directory whose escape target has an existing
# ancestor several levels below the symlink itself, but the immediate tail
# (newdir/) does not exist yet, resolving OUTSIDE the repo, is now allowed
# (#1072, flipped from blocked) -- must still resolve the symlink by walking
# up to the nearest existing ancestor (existingsub/), not just the immediate
# parent (the ancestor-walk fix, #440), before making the now out-of-repo
# allow decision. The in-repo twin immediately below preserves the original
# multi-level-missing-tail intent.
echo "case: symlink escape with a multi-level missing tail resolving outside the repo is allowed (#1072, flipped from blocked)"
mkdir -p "${TEST_ROOT}/outside/existingsub"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link/existingsub/newdir/newfile.txt\"}}"
assert_exit "symlink escape with multi-level missing tail, outside repo, allowed (#1072)" 0

# In-repo twin (#1072): same multi-level-missing-tail shape, but the symlink
# resolves to an in-repo, non-allowlisted directory (the link-inrepo symlink
# and its existingsub/ ancestor were created by the twin above) -- preserves
# the original case's intent that the ancestor walk must find existingsub/,
# not just the immediate parent, before the (still in-repo) block decision.
echo "case: symlink escape with a multi-level missing tail resolving to an in-repo path is blocked (#1072 twin)"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/link-inrepo/existingsub/newdir/newfile.txt\"}}"
assert_exit "symlink escape with multi-level missing tail, in-repo, blocked (#1072 twin)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink escape with multi-level missing tail, in-repo, blocked: stderr should contain BLOCKED"
fi

# Case: jq missing from PATH must fail closed (block), not silently fall
# through to an allow. The curated PATH still has git/cat, so the config
# gate passes through to the jq check rather than short-circuiting at exit 0.
echo "case: jq missing from PATH fails closed"
JQ_MISSING_BIN="${TEST_ROOT}/bin-no-jq"
make_curated_bin "${JQ_MISSING_BIN}" sh git cat realpath
run_guard_with_path "${HARDEN_REPO}" "${JQ_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "jq missing fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "jq missing fails closed: stderr should contain BLOCKED"
fi

# Case: a relative file_path (no leading /) must fail closed rather than be
# silently normalized relative to some directory and potentially match an
# allowlisted pattern by coincidence.
echo "case: relative file_path fails closed"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"src/foo.txt\"}}"
assert_exit "relative file_path fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative file_path fails closed: stderr should contain BLOCKED"
fi

# Case: the true tool_input.file_path is a blocked source path, but an
# unrelated field (old_string, as in an Edit tool call) contains a lookalike
# '"file_path": "..."' substring pointing at an allowlisted path. The hook
# must key off the real tool_input.file_path field via jq, not an incidental
# substring found anywhere in the raw JSON. Built via jq -n --arg (never
# hand-escaped strings) to guarantee valid nesting/escaping.
echo "case: lookalike file_path substring elsewhere in the JSON is ignored"
LOOKALIKE_JSON=$(jq -n --arg fp "${HARDEN_REPO}/src/evil.txt" --arg os '"file_path": "'"${HARDEN_REPO}"'/.worktrees/safe/x.txt"' '{tool_input:{file_path:$fp,old_string:$os}}')
run_guard "${HARDEN_REPO}" "${LOOKALIKE_JSON}"
assert_exit "lookalike file_path substring ignored" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "lookalike file_path substring ignored: stderr should contain BLOCKED"
fi

# Case: both realpath and readlink missing from PATH must also fail closed
# (the resolver step, not just the jq step).
echo "case: path resolver (realpath/readlink) missing from PATH fails closed"
RESOLVER_MISSING_BIN="${TEST_ROOT}/bin-no-resolver"
make_curated_bin "${RESOLVER_MISSING_BIN}" sh git cat jq
run_guard_with_path "${HARDEN_REPO}" "${RESOLVER_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "resolver missing fails closed" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "resolver missing fails closed: stderr should contain BLOCKED"
fi

# Case: a symlink loop leaf directly under the .worktrees/ allowlist must
# still be blocked -- an unresolvable ELOOP node is a fail-closed signal
# that must win over the allowlist substring match, not be walked past.
echo "case: symlink loop leaf under .worktrees/ allowlist is blocked"
mkdir -p "${HARDEN_REPO}/.worktrees/loop"
ln -s b "${HARDEN_REPO}/.worktrees/loop/a"
ln -s a "${HARDEN_REPO}/.worktrees/loop/b"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/loop/a\"}}"
assert_exit "symlink loop leaf under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink loop leaf under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a symlink loop as an intermediate ancestor (not the leaf) under the
# .worktrees/ allowlist, with a not-yet-existing tail below it, must also be
# blocked -- proves the fix isn't leaf-only.
echo "case: symlink loop as an intermediate ancestor under .worktrees/ allowlist is blocked"
mkdir -p "${HARDEN_REPO}/.worktrees/loop2"
ln -s b "${HARDEN_REPO}/.worktrees/loop2/a"
ln -s a "${HARDEN_REPO}/.worktrees/loop2/b"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/loop2/a/newsub/new.txt\"}}"
assert_exit "symlink loop intermediate under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "symlink loop intermediate under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a dangling symlink leaf under the .worktrees/ allowlist (real
# symlink node, unresolvable target) must be blocked.
echo "case: dangling symlink leaf under .worktrees/ allowlist is blocked"
ln -s "${TEST_ROOT}/nonexistent-target-worktree" "${HARDEN_REPO}/.worktrees/dangling-link"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"${HARDEN_REPO}/.worktrees/dangling-link\"}}"
assert_exit "dangling symlink leaf under .worktrees/" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "dangling symlink leaf under .worktrees/: stderr should contain BLOCKED"
fi

# Case: a present-but-empty file_path must fall back to filePath (jq `//`
# bug). jq's `//` alternative operator only falls back on null/false, NOT on
# an empty string. A payload with BOTH keys present, file_path set to "" and
# filePath set to a genuine main-worktree source path, previously extracted
# "" (the `//` never tried filePath because "" is not null) and hit the
# empty-path early `exit 0` -- silently ALLOWING the write. The extraction
# must check emptiness explicitly so a present-but-empty file_path correctly
# falls through to filePath.
echo "case: empty file_path falls back to filePath (jq // does not treat \"\" as fallback trigger)"
run_guard "${HARDEN_REPO}" "{\"tool_input\":{\"file_path\":\"\",\"filePath\":\"${HARDEN_REPO}/src/foo.txt\"}}"
assert_exit "empty file_path falls back to filePath" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "empty file_path falls back to filePath: stderr should contain BLOCKED"
fi

# ── mktemp-failure fallback (#550) ──────────────────────────────────
# The mktemp-failure fallback (`JQ_ERR=$(mktemp 2>/dev/null) || JQ_ERR=/dev/null`)
# must never assign /dev/null to a path later handed to `rm` (as root that
# unlinks the /dev/null device node), and must not silently drop jq's own
# parse-error text. PATH is curated so mktemp always fails and rm is shimmed
# to log its argv (then delegate to the real rm), so the test can observe
# both what gets unlinked and what stderr actually says. Malformed JSON on
# stdin (rather than a config file, since this hook reads JSON from stdin)
# triggers the jq parse failure.
echo "case: mktemp failure preserves jq's diagnostic and never rm's /dev/null"
MKTEMP_FAIL_BIN="${TEST_ROOT}/bin-mktemp-fail-guard"
RM_LOG_GUARD="${TEST_ROOT}/rm-log-guard.txt"
: > "${RM_LOG_GUARD}"
make_mktemp_fail_bin "${MKTEMP_FAIL_BIN}" "${RM_LOG_GUARD}" sh git cat jq realpath
run_guard_with_path "${HARDEN_REPO}" "${MKTEMP_FAIL_BIN}" '{"tool_input":'
assert_exit "mktemp failure fallback (exit code unchanged)" 2
if [[ "${GUARD_STDERR}" == *"guard-main-worktree.sh: warning: mktemp failed; jq errors are written directly to stderr below"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain the mktemp-failure warning line"
fi
if [[ "${GUARD_STDERR}" == *"parse error"* ]]; then
    pass
else
    fail "mktemp failure fallback: stderr should contain jq's own parse-error diagnostic, not an empty detail"
fi
RM_LOG_GUARD_CONTENT="$(cat "${RM_LOG_GUARD}" 2>/dev/null)"
if [[ "${RM_LOG_GUARD_CONTENT}" != *"/dev/null"* ]]; then
    pass
else
    fail "mktemp failure fallback: rm must never be invoked with /dev/null (log: ${RM_LOG_GUARD_CONTENT})"
fi

# ── TMPDIR widening (#749) ──────────────────────────────────────────
# guard-main-worktree.sh's TMPDIR_ALLOW block additionally admits paths under
# a canonicalized $TMPDIR, so the repo-wide `${TMPDIR:-/tmp}/cenci/` body-file
# convention (shell-rules) stops being blocked. TMPDIR_REPO is a dedicated
# repo (not reused from earlier cases) so these cases are order-independent.
# CUSTOM_TMP is a *sibling* of TMPDIR_REPO under TEST_ROOT (never nested
# inside it), so it is not an ancestor of TMPDIR_REPO -- proving the
# ancestor-of-$ROOT guard does not gut this legitimate use.
TMPDIR_REPO="${TEST_ROOT}/tmpdir-widening"
make_git_repo "${TMPDIR_REPO}"
mkdir -p "${TMPDIR_REPO}/.cenci"
touch "${TMPDIR_REPO}/.cenci/config.json"
CUSTOM_TMP="${TEST_ROOT}/custom-tmp"
mkdir -p "${CUSTOM_TMP}"

echo "case: TMPDIR widening allows the \${TMPDIR}/cenci/ body-file convention"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "TMPDIR widening: cenci/ body file" 0

echo "case: TMPDIR widening covers every skill's \${TMPDIR}/... convention, not just /cenci/"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/claude/cenci-749-diff.patch\"}}"
assert_exit "TMPDIR widening: claude/ convention coverage" 0

echo "case: TMPDIR widening canonicalizes a symlinked TMPDIR before matching"
SYMLINK_TMP="${TEST_ROOT}/custom-tmp-link"
ln -s "${CUSTOM_TMP}" "${SYMLINK_TMP}"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${SYMLINK_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/x.md\"}}"
assert_exit "TMPDIR widening: symlinked TMPDIR resolves to the real path" 0

echo "case: TMPDIR widening does not gut the guard for the repo it protects"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR widening: repo source write still blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "TMPDIR widening: repo source write should contain BLOCKED"
fi

# Negative controls: each of these must still block the main-worktree source
# write. The equals/ancestor pair are the discriminating ones -- without the
# ancestor-of-$ROOT check they would flip to exit 0.
echo "case: TMPDIR unset skips the widening entirely (source write blocked)"
run_guard "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR unset: source write blocked" 2

# #1072 flips this from a block to an allow: CUSTOM_TMP is a sibling of
# TMPDIR_REPO under TEST_ROOT (never nested inside it, see the comment
# above), so this path canonicalizes OUTSIDE the repo regardless of whether
# TMPDIR widening ever ran -- it is allowed outright under SCOPE_ROOT,
# independent of TMPDIR being set or unset. This case no longer discriminates
# "TMPDIR unset genuinely skips widening"; the in-repo twin immediately below
# preserves that original non-vacuity intent instead.
echo "case: TMPDIR unset, out-of-repo custom-tmp path is allowed regardless (#1072, flipped from blocked)"
run_guard "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "TMPDIR unset: out-of-repo custom-tmp path allowed (#1072)" 0

# In-repo twin (#1072): an in-repo, custom-tmp-shaped path (mirrors the
# ${TMPDIR}/cenci/... convention shape, but lives inside TMPDIR_REPO) with
# TMPDIR unset -- preserves the original case's intent that without TMPDIR
# set, the widening genuinely does not fire and a would-be-allowed shape
# stays blocked.
echo "case: TMPDIR unset, in-repo custom-tmp-shaped path stays blocked (#1072 twin, preserves original non-vacuity intent)"
mkdir -p "${TMPDIR_REPO}/custom-tmp-shaped/cenci"
run_guard "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/custom-tmp-shaped/cenci/design-comment-749.md\"}}"
assert_exit "TMPDIR unset: in-repo custom-tmp-shaped path still blocked (#1072 twin)" 2

echo "case: relative TMPDIR skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "relative-tmp" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "relative TMPDIR: source write blocked" 2

echo "case: TMPDIR equal to \$ROOT skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TMPDIR_REPO}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR == ROOT: source write blocked" 2

echo "case: TMPDIR an ancestor of \$ROOT skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TEST_ROOT}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR ancestor of ROOT: source write blocked" 2

# TMPDIR nested INSIDE $ROOT (a descendant, e.g. $ROOT/tmp) is the reverse of
# the ancestor case above: without a symmetric rejection, TMPDIR_ALLOW would
# be set to $ROOT/tmp and the guard would allowlist every write under that
# main-worktree subtree -- defeating the guard's purpose. A write under
# $ROOT/tmp itself (not just some other $ROOT/src path) is the discriminating
# assertion here: it is the exact path the containment bug would wrongly let
# through.
echo "case: TMPDIR a descendant of \$ROOT (inside the repo) skips the widening"
INSIDE_TMP="${TMPDIR_REPO}/tmp"
mkdir -p "${INSIDE_TMP}"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${INSIDE_TMP}" "{\"tool_input\":{\"file_path\":\"${INSIDE_TMP}/x\"}}"
assert_exit "TMPDIR descendant of ROOT: write under \$ROOT/tmp blocked" 2

echo "case: TMPDIR pointing at a non-existent directory skips the widening"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${TEST_ROOT}/no-such-dir" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/src/foo.txt\"}}"
assert_exit "TMPDIR non-existent dir: source write blocked" 2

# ── Deferred item 4 cross-file pin (#749) ────────────────────────────
# The *.pen | */DESIGN.md | */designs/* allowlist arm was removed entirely
# along with the rest of the design-stage removal -- this deliberately
# TIGHTENS the guard, so a main-worktree write under designs/ is now blocked
# like any other source path rather than allowlisted. The
# ${TMPDIR:-/tmp}/cenci/design-comment-<number>.md body-file convention is
# unaffected: it stays allowed on its own merits via the general TMPDIR
# widening below, not via any design-specific arm.
echo "case: designs/DESIGN.md Write target is now blocked (design-stage allowlist arm removed)"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${TMPDIR_REPO}/designs/DESIGN.md\"}}"
assert_exit "designs/DESIGN.md write blocked" 2

echo "case: cross-file pin -- design's \${TMPDIR}/cenci/design-comment-<n>.md Write target"
run_guard_with_tmpdir "${TMPDIR_REPO}" "${CUSTOM_TMP}" "{\"tool_input\":{\"file_path\":\"${CUSTOM_TMP}/cenci/design-comment-749.md\"}}"
assert_exit "cross-file pin: cenci/design-comment-749.md" 0

# ── Bash mode (#795): tool_input.command redirection/tee targets ────
# guard-main-worktree.sh must also inspect Bash tool_input.command for
# `>`/`>>`/`>|`/tee write targets, extracted via the shared
# flow/hooks/scripts/lib/bash-write-targets.sh lib, behind the same
# .cenci/config.json / .claude/config.json gate. Historically (#795) the Bash
# arm was deliberately MORE permissive than the Write|Edit arm here: an
# out-of-repo-root target (e.g. $HOME/.claude/settings.json) was allowed
# (Q1a) while the Write|Edit arm blocked every out-of-repo-root target. As of
# #1072 the Write|Edit arm gains the SAME out-of-repo-root allow (scoped via
# SCOPE_ROOT, not the raw repo root -- see the SCOPE_ROOT header comment), so
# this is no longer an asymmetry between the two arms; both now share the
# out-of-repo-scope allow policy, just measured against SCOPE_ROOT rather
# than the plain repo root.
BASH_REPO="${TEST_ROOT}/bash-mode"
make_git_repo "${BASH_REPO}"
mkdir -p "${BASH_REPO}/.cenci"
touch "${BASH_REPO}/.cenci/config.json"

echo "case: Bash command with >> to a main-worktree source file is blocked"
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash >> main-worktree source blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash >> main-worktree source blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command redirecting into .worktrees/ is allowed"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/.worktrees/1-x/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > .worktrees/ allowed" 0

echo "case: Bash command redirecting to \${TMPDIR:-/tmp}/cenci/... is allowed under a custom TMPDIR"
CUSTOM_TMP_BASH="${TEST_ROOT}/bash-custom-tmp"
mkdir -p "${CUSTOM_TMP_BASH}"
JSON=$(jq -n --arg cmd 'echo x > "${TMPDIR:-/tmp}/cenci/x.md"' '{tool_input:{command:$cmd}}')
run_guard_with_tmpdir "${BASH_REPO}" "${CUSTOM_TMP_BASH}" "${JSON}"
assert_exit "bash custom TMPDIR cenci/ allowed" 0

echo "case: Bash command redirecting stderr to /dev/null is allowed (exempt device)"
JSON=$(jq -n --arg cmd "cmd 2>/dev/null" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash 2>/dev/null allowed" 0

echo "case: Bash command redirecting through an unresolved \$OUT variable is blocked"
JSON=$(jq -n --arg cmd 'cmd > "$OUT"' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > \"\$OUT\" blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash > \"\$OUT\" blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command redirecting through \$(mktemp) is blocked"
JSON=$(jq -n --arg cmd 'cmd > $(mktemp)' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash > \$(mktemp) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash > \$(mktemp) blocked: stderr should contain BLOCKED"
fi

# Pins Q1a: the Bash arm's repo-scope pre-check allows a target that resolves
# outside SCOPE_ROOT (as of #1072; previously RESOLVED_ROOT), even though it
# is a redirect near a genuinely sensitive file -- this is the resolution
# that lets /cenci:configure's documented `jq ... ~/.claude/settings.json >
# ~/.claude/settings.json.tmp` pattern through. The WRITE target here is
# deliberately the intermediate `.tmp` file (matching that documented
# pattern exactly), not the bare `settings.json` itself -- as of #1072 Fix 3
# the bare `~/.claude/settings.json` shape is now denylisted on BOTH arms
# regardless of out-of-repo-scope (see the dedicated self-protection
# denylist cases below), so this case's target was narrowed to the shape
# that must legitimately stay allowed. As of #1072 the Write|Edit arm allows
# the same shape of out-of-repo-scope target too (see the SCOPE_ROOT header
# comment), so this is no longer "more permissive than the Write|Edit arm"
# -- both arms now share the same out-of-repo-scope allow policy.
echo "case: Bash command redirecting to \$HOME/.claude/settings.json.tmp (out-of-root) is allowed (Q1a)"
JSON=$(jq -n --arg cmd 'echo x > "$HOME/.claude/settings.json.tmp"' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash \$HOME/.claude/settings.json.tmp out-of-root allowed (Q1a)" 0

# ── #1072 Fix 3: self-protection denylist for out-of-repo Bash write
# targets ── mirrors the Write|Edit-arm denylist cases below (the AC 1/2/5
# section) -- a Bash redirect to a session-security-sensitive out-of-repo
# path must be blocked outright, not allowed via the same out-of-repo-scope
# `continue` that lets $HOME/.claude/settings.json.tmp through above. Literal
# absolute paths (not $HOME expansion) so these cases don't depend on HOME
# being overridden.
BASH_SENSITIVE_HOME="${TEST_ROOT}/bash-sensitive-home"
mkdir -p "${BASH_SENSITIVE_HOME}/.claude/plugins/cenci-flow/hooks/scripts" "${BASH_SENSITIVE_HOME}/.ssh"

echo "case: Bash redirect to ~/.claude/settings.json (out-of-root) is blocked (#1072 Fix 3)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_SENSITIVE_HOME}/.claude/settings.json" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash settings.json out-of-root blocked (#1072 Fix 3)" 2
if [[ "${GUARD_STDERR}" == *"session-security-sensitive"* ]]; then
    pass
else
    fail "bash settings.json out-of-root blocked: stderr should contain the self-protection wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    fail "bash settings.json out-of-root blocked: stderr must not use the main-worktree wording, got: ${GUARD_STDERR}"
else
    pass
fi

echo "case: Bash redirect under ~/.claude/plugins/ (out-of-root) is blocked (#1072 Fix 3)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_SENSITIVE_HOME}/.claude/plugins/cenci-flow/hooks/scripts/guard-main-worktree.sh" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash .claude/plugins/ out-of-root blocked (#1072 Fix 3)" 2

echo "case: Bash redirect under ~/.ssh/ (out-of-root) is blocked (#1072 Fix 3)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_SENSITIVE_HOME}/.ssh/authorized_keys" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash .ssh/ out-of-root blocked (#1072 Fix 3)" 2

# ── Regression: an IN-SCOPE Bash write target shaped like a denylist entry
# must be completely unaffected by the self-protection denylist (post-#1072-
# Fix-3 HIGH regression fix). Mirrors the Write|Edit-arm regression case
# above. `.claude/settings.json` is not itself allowlisted by
# bash_target_allowed (only `.worktrees/`, `.plans/`, `.claude/plans/`, temp
# paths, and design artifacts are), so the CORRECT pre-existing-logic outcome
# for this in-repo, non-worktree Bash write target is exit 2 with the
# ordinary main-worktree BLOCKED wording -- proving the outcome is unchanged
# from before the self-protection feature existed.
echo "case: in-repo Bash redirect to <repo>/.claude/settings.json is blocked by the ordinary allowlist/block logic, NOT the self-protection denylist (#1072 regression fix)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/.claude/settings.json" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "in-repo bash .claude/settings.json blocked by ordinary logic (#1072 regression fix)" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "in-repo bash .claude/settings.json blocked: stderr should use the ordinary main-worktree wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"session-security-sensitive"* ]]; then
    fail "in-repo bash .claude/settings.json blocked: stderr must NOT use the self-protection wording (the denylist must not run for an in-scope target), got: ${GUARD_STDERR}"
else
    pass
fi

echo "case: in-repo Bash redirect to <repo>/.worktrees/configure-init/.claude/settings.json (feature-worktree shape) is allowed, NOT blocked by the self-protection denylist (#1072 regression fix)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/.worktrees/configure-init/.claude/settings.json" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "in-repo bash feature-worktree .claude/settings.json allowed (#1072 regression fix)" 0

# The #749 descendant-of-$ROOT rejection must still apply in Bash mode: a
# TMPDIR pointed inside the repo must not allowlist writes under it.
echo "case: Bash command with TMPDIR=\${BASH_REPO}/tmp (descendant of ROOT) is still blocked"
mkdir -p "${BASH_REPO}/tmp"
JSON=$(jq -n --arg cmd "echo x > ${BASH_REPO}/tmp/x" '{tool_input:{command:$cmd}}')
run_guard_with_tmpdir "${BASH_REPO}" "${BASH_REPO}/tmp" "${JSON}"
assert_exit "bash TMPDIR descendant of ROOT still blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash TMPDIR descendant of ROOT still blocked: stderr should contain BLOCKED"
fi

echo "case: unconfigured repo is a no-op even for an in-root Bash redirect"
BASH_UNCONFIGURED_REPO="${TEST_ROOT}/bash-unconfigured"
make_git_repo "${BASH_UNCONFIGURED_REPO}"
JSON=$(jq -n --arg cmd "echo x > ${BASH_UNCONFIGURED_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_UNCONFIGURED_REPO}" "${JSON}"
assert_exit "bash unconfigured repo no-op" 0

echo "case: Bash command with no redirect and no tee is allowed"
JSON=$(jq -n --arg cmd "git status" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash no-redirect allowed" 0

# ── Fix F (silent-failure-hunter, #795 round 2): end-to-end integration
# coverage for the awk-missing fail-closed path. bash-write-targets.test.sh
# unit-tests bwt_extract_targets directly with awk removed from PATH, but
# nothing previously proved the caller-side block (exit 2, BLOCKED message)
# actually fires through this hook's real JSON-on-stdin entry point.
echo "case: Bash command inspection fails closed when awk is unavailable (Fix F integration)"
AWK_MISSING_BIN="${TEST_ROOT}/bin-no-awk"
make_curated_bin "${AWK_MISSING_BIN}" sh git cat jq mktemp rm realpath
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard_with_path "${BASH_REPO}" "${AWK_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when awk missing" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash command inspection blocked when awk missing: stderr should contain BLOCKED"
fi

# ── Should-fix (#795 round 3): end-to-end integration coverage for the
# command-too-long fail-closed path. bash-write-targets.test.sh unit-tests
# bwt_extract_targets's length pre-check directly, but nothing previously
# proved the caller-side block (exit 2, the specific "too long to inspect"
# BLOCKED message) actually fires through this hook's real JSON-on-stdin
# entry point. Mirrors the awk-missing integration case above.
echo "case: Bash command inspection fails closed when the command is too long to inspect (length pre-check integration)"
LONG_BASH_CMD="echo $(printf '%*s' 100005 '' | tr ' ' 'a') >> ${BASH_REPO}/src/too-long.c"
JSON=$(jq -n --arg cmd "${LONG_BASH_CMD}" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash command inspection blocked when command too long" 2
if [[ "${GUARD_STDERR}" == *"too long to inspect"* ]]; then
    pass
else
    fail "bash command inspection blocked when command too long: stderr should contain the 'too long to inspect' message, got: ${GUARD_STDERR}"
fi

# ── Should-fix (#795 round 4): end-to-end integration coverage for the
# wc-missing fail-closed path. bash-write-targets.test.sh unit-tests
# bwt_extract_targets's length pre-check's wc dependency directly, but
# nothing previously proved the caller-side block (exit 2, the specific "wc
# was not found on PATH" BLOCKED message) actually fires through this hook's
# real JSON-on-stdin entry point. Mirrors the awk-missing integration case
# above; PATH is curated with awk present but wc absent, isolating this
# specific dependency.
echo "case: Bash command inspection fails closed when wc is unavailable (wc-missing integration)"
WC_MISSING_BIN="${TEST_ROOT}/bin-no-wc"
make_curated_bin "${WC_MISSING_BIN}" sh git cat jq mktemp rm realpath awk
JSON=$(jq -n --arg cmd "echo x >> ${BASH_REPO}/src/no-wc.c" '{tool_input:{command:$cmd}}')
run_guard_with_path "${BASH_REPO}" "${WC_MISSING_BIN}" "${JSON}"
assert_exit "bash command inspection blocked when wc missing" 2
if [[ "${GUARD_STDERR}" == *"wc was not found on PATH"* ]]; then
    pass
else
    fail "bash command inspection blocked when wc missing: stderr should contain the 'wc was not found on PATH' message, got: ${GUARD_STDERR}"
fi

# ── #795 final round: empty-parse backstop + tilde expansion ─────────
# Zero extracted targets from a command that still looks like it might
# write (any `>`, or a delimited tee token) triggers a raw-text scan for
# the repo root; a root mention blocks, no mention allows. Mentions of the
# always-allowlisted subtrees (.worktrees/, .plans/, .claude/plans) are
# neutralized first so the pipeline's own `git -C <root>/.worktrees/<id>
# commit -m "a -> b"` shape never trips the backstop.

# run_guard_with_home <cwd> <home_override> <json> — like run_guard, but
# sets HOME for a single invocation. Used by the tilde-expansion cases so
# the out-of-root verdict is deterministic regardless of the ambient HOME.
run_guard_with_home() {
    local cwd="$1" home_override="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | HOME="${home_override}" "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

echo "case: brace-expansion tee construct mentioning the root is blocked (empty-parse backstop)"
JSON=$(jq -n --arg cmd "{tee,cat} ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop brace tee in-root blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "backstop brace tee in-root blocked: stderr should contain BLOCKED"
fi

# {tee,cat} /var/tmp/unrelated-elsewhere.txt brace-expands (in COMMAND
# position, which the exit-6 unsupported-construct detection surface does
# not model -- that surface is scoped to write-TARGET position only; #808
# owns the command-position per-verb decision) to the literal words `tee cat
# /var/tmp/unrelated-elsewhere.txt` -- a bash invocation of `tee` with
# operand "cat" (a RELATIVE, in-root write target -- a file literally named
# "cat"), not merely an out-of-root absolute write as the old rationale
# assumed. The tokenizer still sees a zero-target parse here (its one
# "word" is the whole "{tee,cat}" string, whose basename never equals
# "tee"), so under the new design (#810 Requirement A) this now blocks via
# the unconditional delimited-tee zero-parse branch ("a delimited tee token
# in the raw text blocks unconditionally; its target may be relative to the
# main worktree") -- not the old "no root mention -> allow" backstop path
# this case originally exercised (flipped expectation, was exit 0).
echo "case: {tee,cat} /var/tmp/unrelated-elsewhere.txt is a relative in-root write (of a file named \"cat\"), not an out-of-root target, and is blocked (#810, flipped from exit 0)"
JSON=$(jq -n --arg cmd "{tee,cat} /var/tmp/unrelated-elsewhere.txt" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace-tee relative 'cat' target blocked (flipped from exit 0)" 2

echo "case: quoted -> plus a feature-worktree path mention is allowed (allowlisted-subtree neutralization)"
JSON=$(jq -n --arg cmd "git -C ${BASH_REPO}/.worktrees/1-x commit -m \"a -> b\"" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop worktree-path mention neutralized" 0

echo "case: quoted -> plus a .plans path mention is allowed (allowlisted-subtree neutralization)"
JSON=$(jq -n --arg cmd "ls ${BASH_REPO}/.plans \"a > b\"" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backstop .plans-path mention neutralized" 0

echo "case: alnum-embedded tee with an absolute in-root path is allowed (delimited-tee trigger)"
JSON=$(jq -n --arg cmd "grep guarantee ${BASH_REPO}/docs/notes.md" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "embedded tee in-root grep allowed" 0

# Tilde expansion (#795 final round): a plain unquoted ~/... target expands
# via HOME (mirroring \$HOME) and lands out-of-root -> allowed (Q1a), fixing
# the over-block that broke /cenci:configure's documented
# `... > ~/.claude/settings.json.tmp` pattern. A QUOTED leading tilde is a
# literal in-root filename character in real shells and must stay blocked
# (the tokenizer neutralizes it to ./~ so it is never expanded).
echo "case: unquoted ~ redirect expands via HOME and is allowed out-of-root (Q1a tilde expansion)"
TILDE_HOME="${TEST_ROOT}/tilde-home"
mkdir -p "${TILDE_HOME}"
JSON=$(jq -n --arg cmd 'echo x > ~/.claude/settings.json.tmp' '{tool_input:{command:$cmd}}')
run_guard_with_home "${BASH_REPO}" "${TILDE_HOME}" "${JSON}"
assert_exit "unquoted tilde out-of-root allowed" 0

# Under the new design (#810), this quoted-tilde case still discriminates
# tilde NON-expansion (a genuinely-expanded ~ would be absolute and
# out-of-root -> allowed, Q1a) -- but it now blocks via the new
# non-allowlisted relative-target policy (the neutralized "./~/notes.txt"
# target lexically collapses to the relative form "~/notes.txt", which
# matches no allowlist arm) rather than the old cwd-trusting canonicalize
# + repo-root-scope resolution logic.
echo "case: quoted ~ redirect is a literal in-root path and stays blocked (no tilde expansion for quoted tilde)"
JSON=$(jq -n --arg cmd 'echo x > "~/notes.txt"' '{tool_input:{command:$cmd}}')
run_guard_with_home "${BASH_REPO}" "${TILDE_HOME}" "${JSON}"
assert_exit "quoted tilde literal in-root blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "quoted tilde literal in-root blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810: stabilize Bash write guards for shell expansion, cwd
# changes, and comparison contexts (closes three #795 regressions) ─────────

# AC 2 / the ticket's fourth pinned case: {tee,cat} AGENTS.md brace-expands
# its command word to `tee cat AGENTS.md` -- a RELATIVE target with no
# root-string anywhere in the raw command text for the OLD backstop to
# find. Requirement A: "absence of a literal repo-root string is not
# sufficient evidence a write is out-of-root" -- a zero-parse command
# containing a delimited tee token must block unconditionally instead.
echo "case: {tee,cat} AGENTS.md (relative brace-expansion tee target) blocks from the configured main worktree (#810 AC 2)"
JSON=$(jq -n --arg cmd '{tee,cat} AGENTS.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace-tee relative AGENTS.md target blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "brace-tee relative AGENTS.md target blocked: stderr should contain BLOCKED"
fi

# AC 3 / Requirement B: a relative write target must not be resolved solely
# against the hook process's cwd -- the command text itself can `cd` before
# the write executes. Repro: the hook cwd is a FEATURE worktree (created via
# a real `git worktree add`), but the Bash command first `cd`s to the MAIN
# worktree root and then writes a relative target. Today's guard resolves
# "flow/should-not-write" against the feature-worktree cwd (whose own path
# already contains "/.worktrees/") and wrongly allowlists it.
WORKTREE_REPRO_REPO="${TEST_ROOT}/worktree-cwd-repro"
make_git_repo "${WORKTREE_REPRO_REPO}"
mkdir -p "${WORKTREE_REPRO_REPO}/.cenci"
touch "${WORKTREE_REPRO_REPO}/.cenci/config.json"
git -C "${WORKTREE_REPRO_REPO}" config user.email "test@example.com"
git -C "${WORKTREE_REPRO_REPO}" config user.name "Test"
git -C "${WORKTREE_REPRO_REPO}" add -A
git -C "${WORKTREE_REPRO_REPO}" commit -q -m "init"
FEATURE_WORKTREE_REPRO="${WORKTREE_REPRO_REPO}/.worktrees/1-x"
git -C "${WORKTREE_REPRO_REPO}" worktree add -q -b feat-1-x "${FEATURE_WORKTREE_REPRO}" >/dev/null

echo "case: relative Bash write target after a cd to the main worktree is blocked from a feature-worktree cwd (stale-cwd regression, #810 AC 3)"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative target after cd to main worktree blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "relative target after cd to main worktree blocked: stderr should name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

echo "case: shell token composition cannot hide cd before a parsed relative redirect"
JSON=$(jq -n --arg cmd "c\\d ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "escaped cd before parsed relative target blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "escaped cd before parsed relative target blocked: stderr should name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

echo "case: feature-worktree cwd cannot bypass zero-parse relative tee protection"
JSON=$(jq -n --arg cmd "c\\d ${WORKTREE_REPRO_REPO} && {tee,cat} AGENTS.md" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "escaped cd before zero-parse relative tee target blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "escaped cd before zero-parse relative tee target blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

echo "case: control -- same command shape with an absolute .worktrees/-scoped target is allowed"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > ${FEATURE_WORKTREE_REPRO}/flow/should-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "control: absolute .worktrees-scoped target allowed" 0

# Conservative policy: the hook cannot prove that the Bash command preserves
# its starting cwd. Even when the hook starts inside a feature worktree,
# non-allowlisted relative targets must use an absolute worktree path.
echo "case: an ordinary relative-write command is blocked from a feature-worktree cwd"
JSON=$(jq -n --arg cmd 'pytest 2> errors.log' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "plain relative write, feature-worktree cwd blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "plain relative write, feature-worktree cwd blocked: stderr should name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

echo "case: another ordinary relative-write shape is blocked from a feature-worktree cwd"
JSON=$(jq -n --arg cmd 'git diff > diff.txt' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "git diff > diff.txt, feature-worktree cwd blocked" 2

# Re-run the original regression alongside the conservative cases: their
# identical feature-worktree hook cwd must not change the decision.
echo "case: non-regression -- relative target after a cd to the main worktree stays blocked"
JSON=$(jq -n --arg cmd "cd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative target after cd to main worktree still blocked" 2
if [[ "${GUARD_STDERR,,}" == *"cwd"* ]]; then
    pass
else
    fail "relative target after cd to main worktree: stderr should still name the cwd uncertainty, got: ${GUARD_STDERR}"
fi

# An upward-traversing relative target is one concrete demonstration of why
# relative resolution against the hook cwd is unsafe.
echo "case: relative Bash write target traversing upward with .. is blocked even with no cd (Finding 3A regression)"
JSON=$(jq -n --arg cmd 'printf x > ../../AGENTS.md' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative .. traversal with no cd blocked (Finding 3A)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative .. traversal with no cd blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

# A non-traversing relative target follows the same policy because shell
# syntax can change cwd before the redirect.
echo "case: non-regression -- an ordinary non-traversing relative write stays blocked from a feature-worktree cwd"
JSON=$(jq -n --arg cmd 'pytest 2> errors.log' '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "non-traversing relative write, feature-worktree cwd blocked" 2

# A different directory-changing builtin must yield the same conservative
# decision; the policy does not depend on recognizing a particular verb.
echo "case: relative Bash write target after a pushd to the main worktree is blocked from a feature-worktree cwd (Finding 3B regression)"
JSON=$(jq -n --arg cmd "pushd ${WORKTREE_REPRO_REPO} && printf x > flow/should-not-write" '{tool_input:{command:$cmd}}')
run_guard "${FEATURE_WORKTREE_REPRO}" "${JSON}"
assert_exit "relative target after pushd to main worktree blocked (Finding 3B)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "relative target after pushd to main worktree blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

# Relative allowlist shapes remain untrusted; the pipeline's own plan-persist
# step now supplies the absolute repo-root path.
echo "case: relative .plans/-shaped Bash write target is blocked"
JSON=$(jq -n --arg cmd 'cat /tmp/x >> .plans/1-foo.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "relative .plans/ write target blocked" 2

echo "case: absolute repo-root .plans target remains allowed"
JSON=$(jq -n --arg cmd "cat /tmp/x >> ${BASH_REPO}/.plans/1-foo.md" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "absolute .plans/ write target allowed" 0

echo "case: implement plan persistence uses the absolute repo-root .plans path"
PHASE_1_PLAN="${SCRIPT_DIR}/../../skills/implement/phases/phase-1-plan.md"
if grep -qF '>> "<repo-root>/.plans/<filename>"' "${PHASE_1_PLAN}"; then
    pass
else
    fail "phase-1-plan.md should append context through the quoted absolute <repo-root>/.plans path"
fi
if grep -qF '>> <repo-root>/.plans/<filename>' "${PHASE_1_PLAN}"; then
    fail "phase-1-plan.md must quote its absolute .plans target for repo roots containing spaces"
else
    pass
fi
if grep -qF '>> .plans/<filename>' "${PHASE_1_PLAN}"; then
    fail "phase-1-plan.md must not append context through a cwd-relative .plans path"
else
    pass
fi

echo "case: a relative allowlist shape is not treated as repo-root-relative from a subdirectory cwd"
mkdir -p "${BASH_REPO}/src/nested"
JSON=$(jq -n --arg cmd 'printf x > .plans/pwn' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}/src/nested" "${JSON}"
assert_exit "relative .plans/ target from subdirectory blocked" 2

echo "case: a relative .plans symlink cannot escape to a protected source file"
mkdir -p "${BASH_REPO}/.plans"
ln -s ../AGENTS.md "${BASH_REPO}/.plans/link.md"
JSON=$(jq -n --arg cmd 'printf x > .plans/link.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "relative .plans/ symlink escape blocked" 2

echo "case: a relative DESIGN.md symlink cannot escape to a protected source file"
ln -s AGENTS.md "${BASH_REPO}/DESIGN.md"
JSON=$(jq -n --arg cmd 'printf x > DESIGN.md' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "relative DESIGN.md symlink escape blocked" 2

# AC 4 / Requirement C: `>` inside a CLOSED [[ ... ]] / (( ... )) comparison
# context is not a redirect at all -- must never be classified as a write
# target.
echo "case: [[ z > a ]] and (( z > a )) are comparison contexts, not redirects, and are allowed (#810 AC 4)"
JSON=$(jq -n --arg cmd '[[ z > a ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash [[ z > a ]] allowed" 0

JSON=$(jq -n --arg cmd '(( z > a ))' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash (( z > a )) allowed" 0

# AC 5: a real redirect immediately after a [[ ... ]] region must still be
# extracted and checked.
echo "case: a real redirect immediately after [[ ... ]] is still extracted and blocked (#810 AC 5)"
JSON=$(jq -n --arg cmd "[[ -f x ]] > ${BASH_REPO}/src/foo.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "bash [[ -f x ]] > src/foo.c (adjacent real redirect) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "bash [[ -f x ]] > src/foo.c (adjacent real redirect) blocked: stderr should contain BLOCKED"
fi

# AC 1: unquoted brace expansion in write-target position must fail closed
# (new bwt_extract_targets exit code 6), never emit a bogus literal target
# -- and the guard must name the unsupported construct.
echo "case: tee <root>/src/{a,b}.c (unquoted brace expansion in tee operand position) is blocked, naming the unsupported construct (#810 AC 1)"
JSON=$(jq -n --arg cmd "tee ${BASH_REPO}/src/{a,b}.c" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "brace expansion tee target blocked" 2
if [[ "${GUARD_STDERR,,}" == *"brace"* ]]; then
    pass
else
    fail "brace expansion tee target blocked: stderr should name the unsupported brace-expansion construct, got: ${GUARD_STDERR}"
fi

# ── Ticket #810 stabilization review, Bug 1 [CRITICAL] (e2e): a sticky
# `curregion` flag left over from a closed (( ... )) region disabled
# tee-ARMING for the very next word, silently permitting a real write.
# Confirmed against real bash: `(( 1 )) ; tee AGENTS.md > /dev/null`
# genuinely writes to AGENTS.md.
echo "case: Bash command with a closed (( )) region before tee still blocks the tee write (Bug 1 e2e regression)"
JSON=$(jq -n --arg cmd '(( 1 )) ; tee AGENTS.md > /dev/null' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "(( 1 )) ; tee AGENTS.md > /dev/null blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "(( 1 )) ; tee AGENTS.md > /dev/null blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization review, Bug 2 [HIGH] (e2e): mark_regions() had
# no token-boundary check, so a "[[" glued mid-word (never the reserved
# conditional token in real bash) could still suppress a real, adjacent
# redirect. Confirmed against real bash: `echo x[[ z > AGENTS.md ]] b`
# genuinely writes to AGENTS.md.
echo "case: Bash command with a glued [[ (not its own token) still blocks the real redirect (Bug 2 e2e regression)"
JSON=$(jq -n --arg cmd 'echo x[[ z > AGENTS.md ]] b' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x[[ z > AGENTS.md ]] b blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x[[ z > AGENTS.md ]] b blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization review (second cycle), Bug 3 [CRITICAL] (e2e):
# is_opener_boundary/find_close accepted spans bash does not actually treat
# as "[[ ]]"/"(( ))", allowing three fail-open bypass vectors.

# Vector 1: process substitution glued to (( is not a real (( opener.
# Confirmed against real bash: `echo x >(( : > /repo/.env ))` genuinely
# writes to /repo/.env (the subshell `( : > /repo/.env )`).
echo "case: Bash command with process substitution glued to (( still blocks the real write (Bug 3 Vector 1 e2e regression)"
JSON=$(jq -n --arg cmd "echo x >(( : > ${BASH_REPO}/src/bug3-procsub.env ))" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x >(( : > src/bug3-procsub.env )) blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x >(( : > src/bug3-procsub.env )) blocked: stderr should contain BLOCKED"
fi

# Vector 2: "[[" preceded by a plain word (not command position) is not a
# real conditional opener. Confirmed against real bash:
# `echo x [[ z > AGENTS.md ]] b` genuinely writes to AGENTS.md.
echo "case: Bash command with [[ preceded by a plain word (not command position) still blocks the real redirect (Bug 3 Vector 2 e2e regression)"
JSON=$(jq -n --arg cmd "echo x [[ z > ${BASH_REPO}/src/bug3-notcmdpos.md ]] b" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo x [[ z > src/bug3-notcmdpos.md ]] b blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo x [[ z > src/bug3-notcmdpos.md ]] b blocked: stderr should contain BLOCKED"
fi

# Vector 3: bash terminates "[[ ... ]]" at the FIRST standalone "]]", not
# the last one in the text. Confirmed against real bash:
# `[[ [[ ]] ; echo x > AGENTS.md ]]` genuinely writes to AGENTS.md.
echo "case: Bash command where the first standalone ]] closes [[ still blocks the real redirect in between (Bug 3 Vector 3 e2e regression)"
JSON=$(jq -n --arg cmd "[[ [[ ]] ; echo x > ${BASH_REPO}/src/bug3-firstclose.md ]]" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ [[ ]] ; echo x > src/bug3-firstclose.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ [[ ]] ; echo x > src/bug3-firstclose.md ]] blocked: stderr should contain BLOCKED"
fi

# Non-regression: a real redirect after a genuine [[ ... ]] region (with a
# glued POSIX character class inside it) is still extracted and blocked.
echo "case: [[ \$x =~ [[:alpha:]] ]] followed by a real redirect still blocks (Bug 3 Vector 3 e2e non-regression)"
JSON=$(jq -n --arg cmd "[[ \$x =~ [[:alpha:]] ]] > ${BASH_REPO}/src/bug3-posixclass.out" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ \$x =~ [[:alpha:]] ]] > src/bug3-posixclass.out blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ \$x =~ [[:alpha:]] ]] > src/bug3-posixclass.out blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-4 security review [CRITICAL] (e2e): is_opener_boundary()
# previously accepted the &/| TAIL of the compound redirect operators >&/<&/
# >| as if it were a bare separator/pipe, wrongly opening a suppressed [[ ]]
# region and hiding a real redirect inside it. Confirmed against real bash:
# `echo hi >| [[ x > AGENTS.md ]]` truncate-redirects to a file literally
# named "[[", and the `[[ x > AGENTS.md ]]` that follows is NOT the reserved
# conditional token in that (redirect-target) position -- `> AGENTS.md`
# genuinely writes to AGENTS.md.
echo "case: Bash command with >| immediately before [[ still blocks the real redirect inside (round-4 e2e regression)"
JSON=$(jq -n --arg cmd 'echo hi >| [[ x > AGENTS.md ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo hi >| [[ x > AGENTS.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo hi >| [[ x > AGENTS.md ]] blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command with >& immediately before [[ still blocks the real redirect inside (round-4 e2e regression)"
JSON=$(jq -n --arg cmd 'echo hi >& [[ x > AGENTS.md ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo hi >& [[ x > AGENTS.md ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo hi >& [[ x > AGENTS.md ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-5 security review [CRITICAL] (e2e): a real write
# hidden inside command/process substitution nested in [[ ]] / (( )) was
# previously swallowed by mark_regions()'s region suppression -- the benign
# earlier target made the parse non-empty, so the zero-parse backstop never
# ran, and the real write inside the suppressed [[ ]] span was never
# extracted. Confirmed against real bash: both commands below genuinely
# write to AGENTS.md in the configured main worktree.
echo "case: Bash command with a real write hidden inside \$( ) nested in [[ ]] still blocks (round-5 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ $(printf x > AGENTS.md) ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ \$(printf x > AGENTS.md) ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ \$(printf x > AGENTS.md) ]] blocked: stderr should contain BLOCKED"
fi

echo "case: Bash command with a tee operand hidden inside \$( ) nested in [[ ]] still blocks (round-5 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ $(echo p | tee AGENTS.md) ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ \$(echo p | tee AGENTS.md) ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ \$(echo p | tee AGENTS.md) ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 round-6 stabilization [HIGH] (e2e): bash 5.3+'s function
# substitution forms `${ cmd; }` / `${| cmd; }` execute a real command from
# inside a [[ ]]/(( )) construct just like `$(...)`, but were not recognized
# as a substitution marker -- the region was suppressed as an ordinary
# comparison, so the real write nested inside was never extracted and never
# blocked. Confirmed against real bash: this command genuinely writes to
# AGENTS.md in the configured main worktree.
echo "case: Bash command with a real write hidden inside \${ cmd; } funsub nested in [[ ]] still blocks (round-6 e2e regression)"
JSON=$(jq -n --arg cmd 'echo ok > /tmp/ok ; [[ -n ${ printf x > AGENTS.md; } ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "echo ok > /tmp/ok ; [[ -n \${ printf x > AGENTS.md; } ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "echo ok > /tmp/ok ; [[ -n \${ printf x > AGENTS.md; } ]] blocked: stderr should contain BLOCKED"
fi

# ── Ticket #810 stabilization (e2e): a command/process/function substitution
# marker found INSIDE a double-quoted span within a [[ ]]/(( )) region's
# interior is a deliberately blunt, maximally conservative unsupported
# construct: this tokenizer has no safe way to resume precise parsing through
# such a marker's real syntax once wrapped in an outer double-quoted string,
# so the whole command fails closed unconditionally (bwt_extract_targets
# exit 7) rather than attempting to precisely resolve what write is inside
# it. Confirmed against real bash: this command genuinely writes to
# AGENTS.md in the configured main worktree, but this guard now blocks it
# purely because the construct is unsupported for extraction -- not because
# it precisely resolved AGENTS.md as the target.
echo "case: Bash command with a real write hidden inside a double-quoted \$( ) command substitution nested in [[ ]] fails closed (exit 7 e2e)"
JSON=$(jq -n --arg cmd '[[ -n "$(printf x > AGENTS.md)" ]]' '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"cannot be safely resolved"* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should name the unsupported double-quoted-nested-substitution construct, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *'[[ -n "$(printf x > AGENTS.md)" ]]'* ]]; then
    pass
else
    fail "[[ -n \"\$(printf x > AGENTS.md)\" ]] blocked: stderr should name the offending command, got: ${GUARD_STDERR}"
fi

echo "case: a backslash-newline cannot hide a double-quoted \$( ) command substitution from the main-worktree guard"
BASH_CMD=$'[[ -n "$\\\n(printf x > AGENTS.md)" ]]'
JSON=$(jq -n --arg cmd "${BASH_CMD}" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "backslash-newline-spliced double-quoted command substitution blocked" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* && "${GUARD_STDERR}" == *"cannot be safely resolved"* ]]; then
    pass
else
    fail "backslash-newline-spliced double-quoted command substitution: stderr should identify the unsupported construct, got: ${GUARD_STDERR}"
fi

# ── Ticket #1036: Codex apply_patch envelope recognition ─────────────
# On the Codex path, apply_patch reaches PreToolUse hooks as a Bash-matcher
# tool call whose tool_input.command is the raw patch text (optionally
# wrapped in a shell `apply_patch <<'EOF' ... EOF` heredoc invocation).
# Neither the shared tokenizer nor this guard previously modelled that
# grammar: arrow operators/redirects inside the diff BODY tripped the
# relative-target rejection (false positive), and a patch body with no
# arrow-shaped characters at all extracted zero targets and silently passed
# through unchecked (silent bypass). bwt_is_apply_patch_payload /
# bwt_apply_patch_targets (flow/hooks/scripts/lib/bash-write-targets.sh)
# recognize the bare apply_patch envelope shape and this guard branches onto
# them BEFORE the tokenizer ever sees the diff body.
APPLY_PATCH_REPO="${TEST_ROOT}/apply-patch"
make_git_repo "${APPLY_PATCH_REPO}"
mkdir -p "${APPLY_PATCH_REPO}/.cenci"
touch "${APPLY_PATCH_REPO}/.cenci/config.json"

# #1212: the payload shape Codex really sends for a direct apply_patch call
# carries tool_name "apply_patch" and the session cwd next to
# tool_input.command. The relative-target resolution below is gated on that
# shape (the guard joins a relative target to cwd only when it can show the
# payload cwd is the directory the write resolves against), so the #1036 /
# #1045 envelope fixtures send it. Fixtures that deliberately model the
# shell-prefixed / Bash-routed fall-back keep the bare {tool_input} shape.
AP_SHAPE='{tool_name:"apply_patch",cwd:$cwd,tool_input:{command:$cmd}}'

# (a) all declared targets under <root>/.worktrees/<id>-<desc>/, body
# containing =>, List<Foo>, >>, "committee" -> exit 0. Proves arrow
# operators in the diff BODY never trip the relative-target rejection once
# the envelope is recognized (#1036 AC 1).
echo "case: apply_patch envelope with all targets under .worktrees/ is allowed despite arrow operators in the diff body (#1036 AC 1)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: .worktrees/1036-x/src/committee.go
@@
-func old() map[string]List<Foo> { return nil }
+func new() map[string]List<Foo> {
+    x := a => b
+    y >> z
+    return committee
+}
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope, all targets in .worktrees/, arrow-heavy body allowed" 0

# (b) *** Update File: AGENTS.md (relative), >-free body, cwd = repo root ->
# exit 2 with the existing feature-worktree guidance text (#1036 AC 2).
echo "case: apply_patch envelope declaring a relative main-worktree target is blocked with the existing feature-worktree guidance (#1036 AC 2)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: AGENTS.md
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope relative AGENTS.md target blocked" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree, not a feature worktree"* ]]; then
    pass
else
    fail "apply_patch envelope relative AGENTS.md target blocked: stderr should contain the existing feature-worktree guidance text, got: ${GUARD_STDERR}"
fi

# (c) absolute <root>/AGENTS.md -> exit 2.
echo "case: apply_patch envelope declaring an absolute main-worktree target is blocked"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: ${APPLY_PATCH_REPO}/AGENTS.md
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope absolute AGENTS.md target blocked" 2

# (d) relative .worktrees/<id>-x/f.txt from repo-root cwd -> exit 0 (proves
# the cwd base actually resolves).
echo "case: apply_patch envelope declaring a relative .worktrees/ target resolves against cwd and is allowed"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Add File: .worktrees/1036-x/f.txt
+new
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope relative .worktrees/ target allowed" 0

# (e) canonicalize-then-decide: relative .worktrees/<id>-x/../../AGENTS.md ->
# exit 2. Lexical trust would allowlist this via */.worktrees/*;
# canonicalization must resolve it to <root>/AGENTS.md first. Load-bearing --
# must not be omitted.
echo "case: apply_patch envelope target escaping .worktrees/ via .. is blocked -- canonicalize-then-decide, not lexical trust (#1036, load-bearing)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: .worktrees/1036-x/../../AGENTS.md
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope .worktrees/../../ escape blocked" 2

# (f) *** Update File: in the worktree + *** Move to: into the main worktree
# -> exit 2 (both Move targets checked).
echo "case: apply_patch envelope's Move to destination escaping to the main worktree is blocked even though the source is in-worktree"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: .worktrees/1036-x/old.txt
*** Move to: AGENTS.md
@@
-old
+new
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope Move to main-worktree destination blocked" 2

# (g) malformed bare envelope (missing *** End Patch) -> exit 2 with the
# exit-8 message; and unquoted <<EOF -> exit 2 with the exit-8 message.
echo "case: malformed bare apply_patch envelope (missing *** End Patch) is blocked with a dedicated message naming the construct"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: AGENTS.md
@@
-old
+new" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "malformed apply_patch envelope (missing End Patch) blocked" 2
if [[ "${GUARD_STDERR,,}" == *"apply_patch"* ]]; then
    pass
else
    fail "malformed apply_patch envelope (missing End Patch) blocked: stderr should name the apply_patch construct, got: ${GUARD_STDERR}"
fi

echo "case: apply_patch heredoc wrapper with an unquoted delimiter (<<EOF) is blocked with a dedicated message naming the construct"
JSON=$(jq -n --arg cmd "apply_patch <<EOF
*** Begin Patch
*** Update File: AGENTS.md
*** End Patch
EOF" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch unquoted heredoc delimiter blocked" 2
if [[ "${GUARD_STDERR,,}" == *"apply_patch"* ]]; then
    pass
else
    fail "apply_patch unquoted heredoc delimiter blocked: stderr should name the apply_patch construct, got: ${GUARD_STDERR}"
fi

# (g2) #1036 Fix 6: a malformed envelope whose diff BODY carries secret-
# shaped material must never have that body echoed into the exit-8 BLOCKED
# message -- only the first (sentinel) line may appear.
echo "case: malformed apply_patch envelope whose body contains secret-shaped material does not leak that body into the BLOCKED message (#1036 Fix 6)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Add File: AGENTS.md
+API_KEY=sk-supersecretvalue12345
@@ missing end sentinel" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "malformed apply_patch envelope with secret-shaped body blocked" 2
if [[ "${GUARD_STDERR}" == *"sk-supersecretvalue12345"* ]]; then
    fail "malformed apply_patch envelope with secret-shaped body blocked: BLOCKED message leaked the diff body's secret-shaped content, got: ${GUARD_STDERR}"
else
    pass
fi
if [[ "${GUARD_STDERR,,}" == *"apply_patch"* && "${GUARD_STDERR}" == *"*** Begin Patch"* ]]; then
    pass
else
    fail "malformed apply_patch envelope with secret-shaped body blocked: stderr should still name the apply_patch construct via its first line, got: ${GUARD_STDERR}"
fi

# (g3) #1045 review, Fix 9: bwt_is_unresolved models SHELL-EXPANSION residue,
# which an apply_patch declared path cannot have -- only a non-expanding
# delimiter is ever accepted, so the body is never expanded and '$', a
# backtick, and '(' in a declared path are ordinary filename characters.
# Running that test on them rejected a legitimate write to a real file with
# "Use a literal absolute path", which it already was. The decision must come
# from the path itself, exactly as for any other declared target.
echo "case: an apply_patch declared path containing '(' resolves normally -- inside a feature worktree it is allowed, not rejected as unresolvable (#1045 Fix 9)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Add File: .worktrees/1036-x/reports/summary (1).csv
+col
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch declared path with '(' inside .worktrees/ allowed" 0

# The SAME shape outside the feature worktree must still block -- on the
# main-worktree decision, the real one, not on a spurious unresolvable-target
# rejection. And per #1036 Fix 6 / Fix A, that message must still never carry
# the diff body, which may itself hold secret material.
echo "case: the same '('-containing declared path targeting the main worktree is blocked on the main-worktree decision, without leaking the diff body's secret-shaped content (#1045 Fix 9, #1036 Fix A)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Add File: reports/summary (1).csv
+API_KEY=sk-supersecretvalue12345
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch declared path with '(' in the main worktree blocked" 2
if [[ "${GUARD_STDERR}" == *"sk-supersecretvalue12345"* ]]; then
    fail "apply_patch declared path with '(' in the main worktree blocked: BLOCKED message leaked the diff body's secret-shaped content, got: ${GUARD_STDERR}"
else
    pass
fi
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* && "${GUARD_STDERR}" == *"reports/summary (1).csv"* ]]; then
    pass
else
    fail "apply_patch declared path with '(' in the main worktree blocked: stderr should be the main-worktree message naming the resolved target, got: ${GUARD_STDERR}"
fi

# (g4) #1045 review, Fix 7: bwt_is_apply_patch_payload gates the parser, so a
# shape it rejects never reaches bwt_apply_patch_targets at all. The
# column-0-only sentinel test rejected exactly the shape #1036's Fix 1 exists
# to handle: a `<<-'EOF'` body written the natural way, with the sentinel
# tab-indented along with everything else. The command then fell through to
# bwt_has_write_candidate, which is false for an arrow-free diff body, and the
# hook exited 0 -- a full guard bypass. Driven end-to-end here (not at the
# parser, which handled this shape correctly all along).
echo "case: a <<-'EOF' envelope whose sentinel and directives are tab-indented is recognized end-to-end and blocked on its main-worktree target (#1045 Fix 7)"
AP_TAB=$'\t'
JSON=$(jq -n --arg cmd "apply_patch <<-'EOF'
${AP_TAB}*** Begin Patch
${AP_TAB}*** Update File: AGENTS.md
${AP_TAB}*** End Patch
${AP_TAB}EOF" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "tab-indented <<-'EOF' envelope targeting AGENTS.md blocked" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "tab-indented <<-'EOF' envelope targeting AGENTS.md blocked: stderr should be the main-worktree message, got: ${GUARD_STDERR}"
fi

echo "case: the same tab-indented <<-'EOF' envelope targeting a feature worktree is allowed (recognition is not a blanket over-block) (#1045 Fix 7)"
JSON=$(jq -n --arg cmd "apply_patch <<-'EOF'
${AP_TAB}*** Begin Patch
${AP_TAB}*** Add File: .worktrees/1036-x/tabbed.txt
${AP_TAB}+x
${AP_TAB}*** End Patch
${AP_TAB}EOF" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "tab-indented <<-'EOF' envelope targeting .worktrees/ allowed" 0

# (g5) #1045 review, Fix 7: the same permissive-bucket bypass, reached through
# delimiter spellings bash accepts for a bare, non-expanding heredoc.
echo "case: every non-expanding heredoc delimiter spelling is recognized end-to-end and blocked on its main-worktree target (#1045 Fix 7)"
for spelling in "apply_patch << 'EOF'" "apply_patch <<\\EOF" "apply_patch<<'EOF'"; do
    JSON=$(jq -n --arg cmd "${spelling}
*** Begin Patch
*** Update File: AGENTS.md
*** End Patch
EOF" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
    run_guard "${APPLY_PATCH_REPO}" "${JSON}"
    assert_exit "delimiter spelling <${spelling}> targeting AGENTS.md blocked" 2
done

echo "case: an apply_patch here-STRING invocation, whose payload this parser cannot see, fails closed rather than falling through to the tokenizer (#1045 Fix 7)"
JSON=$(jq -n --arg cmd 'apply_patch <<<"$patch"' --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch here-string invocation blocked" 2

# (g6) #1045 review, Fix 8: `*** End of File` is part of apply_patch's chunk
# grammar, emitted whenever a hunk runs to the end of the file. Extracting it
# as a declared path made this guard join it to cwd, canonicalize it inside
# the repo root, and hard-block -- so EVERY patch whose last chunk reaches EOF
# was rejected, with a nonsensical message naming "*** End of File" as a write
# target.
echo "case: an envelope whose chunk ends with the *** End of File marker is allowed when its real target is a feature worktree (#1045 Fix 8)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: .worktrees/1036-x/src/app.py
@@
-old
+new
*** End of File
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "envelope with *** End of File marker and a .worktrees/ target allowed" 0
if [[ "${GUARD_STDERR}" == *"End of File"* ]]; then
    fail "envelope with *** End of File marker: the marker must never be reported as a write target, got: ${GUARD_STDERR}"
else
    pass
fi

# (h) fall-back proof: cd /elsewhere && apply_patch <<'EOF' ... with a
# =>-bearing body -> exit 2 via the EXISTING relative-target/zero-parse
# message, explicitly asserted as NOT the apply_patch exit-8 message.
echo "case: shell-prefixed apply_patch invocation with arrows in the body falls back to the existing tokenizer path (not the apply_patch exit-8 message)"
JSON=$(jq -n --arg cmd "cd /elsewhere && apply_patch <<'EOF'
*** Begin Patch
*** Update File: foo.go
@@
-old := old
+new := old => new
*** End Patch
EOF" '{tool_input:{command:$cmd}}')
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "shell-prefixed apply_patch with arrow body falls back and blocks" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "shell-prefixed apply_patch with arrow body falls back and blocks: stderr should contain BLOCKED"
fi
# The command text itself literally contains the word "apply_patch" (it's
# part of the raw command being echoed into the BLOCKED message), so the
# discriminator here is NOT "does stderr mention apply_patch" -- it's which
# message fired. This must be one of the two PRE-EXISTING relative-target /
# zero-parse messages (never the new apply_patch-specific exit-8 wording),
# proving BWT_APPLY_PATCH=0 fell through to the unchanged tokenizer path.
if [[ "${GUARD_STDERR}" == *"cannot verify the relative Bash write target"* || "${GUARD_STDERR}" == *"unmodelled shell construct"* ]]; then
    pass
else
    fail "shell-prefixed apply_patch with arrow body falls back and blocks: stderr should be one of the existing relative-target/zero-parse messages, got: ${GUARD_STDERR}"
fi

# (i) documented residual, labelled as such: the same shell-prefixed shape
# with a >-free body declaring AGENTS.md -> exit 0, unchanged from today's
# behavior. Pinned so it cannot change silently.
echo "case: DOCUMENTED RESIDUAL -- shell-prefixed apply_patch invocation with an arrow-free body is not recognized as a write candidate at all and is silently allowed (#1036, pinned, unchanged from today)"
JSON=$(jq -n --arg cmd "cd /elsewhere && apply_patch <<'EOF'
*** Begin Patch
*** Update File: AGENTS.md
*** End Patch
EOF" '{tool_input:{command:$cmd}}')
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "DOCUMENTED RESIDUAL: shell-prefixed apply_patch, arrow-free body, allowed unchanged" 0

# (j) out-of-root absolute declared target -> exit 0 (Q1a asymmetry
# preserved).
echo "case: apply_patch envelope declaring an out-of-repo-root absolute target is allowed (Q1a asymmetry preserved)"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Add File: ${TEST_ROOT}/apply-patch-outside/f.txt
+new
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope out-of-root absolute target allowed (Q1a)" 0

# (k) relative declared target whose ancestor is a dangling symlink -> exit 2
# (canonicalization failure blocks).
echo "case: apply_patch envelope declaring a relative target under a dangling symlink ancestor is blocked (canonicalization failure)"
ln -s "${TEST_ROOT}/apply-patch-nonexistent-target" "${APPLY_PATCH_REPO}/dangling-link"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: dangling-link/sub.txt
*** End Patch" --arg cwd "${APPLY_PATCH_REPO}" "${AP_SHAPE}")
run_guard "${APPLY_PATCH_REPO}" "${JSON}"
assert_exit "apply_patch envelope relative target under dangling symlink blocked" 2

# (l) unconfigured repo -> exit 0 (config gate unchanged).
echo "case: apply_patch envelope in an unconfigured repo is a no-op (config gate unchanged)"
APPLY_PATCH_UNCONFIGURED_REPO="${TEST_ROOT}/apply-patch-unconfigured"
make_git_repo "${APPLY_PATCH_UNCONFIGURED_REPO}"
JSON=$(jq -n --arg cmd "*** Begin Patch
*** Update File: AGENTS.md
@@
-old
+new => decoy
*** End Patch" '{tool_input:{command:$cmd}}')
run_guard "${APPLY_PATCH_UNCONFIGURED_REPO}" "${JSON}"
assert_exit "apply_patch envelope unconfigured repo no-op" 0

# ── Ticket #1212: relative apply_patch targets need a determinable cwd ─────
# #1036 resolved a relative apply_patch target against THIS hook process's
# cwd. Codex's exec_command PreToolUse payload carries only {command} -- the
# per-call `workdir` is dropped -- so for a Bash-routed `apply_patch <<'EOF'`
# the directory the write really resolves against can differ from the hook's
# cwd: a worktree session whose exec_command runs with workdir = the repo
# root writes a relative AGENTS.md into the MAIN worktree while the guard
# joined it to the worktree and allowed it. The guard now joins a relative
# target only when tool_name is "apply_patch" and the payload cwd is an
# absolute, newline-free path whose canonical form equals the canonical form
# of the hook's own cwd. Anything else fails closed (over-block, never allow).
RAP_REPO="${TEST_ROOT}/relative-apply-patch"
RAP_WT="${RAP_REPO}/.worktrees/1212-wt"
RAP_OTHER="${TEST_ROOT}/relative-apply-patch-other"
RAP_SECRET='API_KEY=sk-supersecretvalue12345'
make_git_repo "${RAP_REPO}"
mkdir -p "${RAP_REPO}/.cenci" "${RAP_WT}" "${RAP_OTHER}"
touch "${RAP_REPO}/.cenci/config.json"

# ap_env <add|update|delete|move> <dir-prefix> -- an apply_patch envelope whose
# targets live under <dir-prefix> (empty, or a relative path ending in "/").
# Bodies carry secret-shaped text so a message that echoes the diff is caught.
ap_env() {
    local kind="$1" d="$2"
    case "${kind}" in
        add) printf '*** Begin Patch\n*** Add File: %snew.txt\n+%s\n*** End Patch' "${d}" "${RAP_SECRET}" ;;
        update) printf '*** Begin Patch\n*** Update File: %sAGENTS.md\n@@\n-old\n+%s\n*** End Patch' "${d}" "${RAP_SECRET}" ;;
        delete) printf '*** Begin Patch\n*** Delete File: %sold.txt\n*** End Patch' "${d}" ;;
        move) printf '*** Begin Patch\n*** Update File: %sa.txt\n*** Move to: %sb.txt\n@@\n-old\n+%s\n*** End Patch' "${d}" "${d}" "${RAP_SECRET}" ;;
    esac
}

# ap_heredoc <env> -- the same envelope wrapped the way a Bash-routed call
# carries it.
ap_heredoc() {
    printf "apply_patch <<'EOF'\n%s\nEOF" "$1"
}

# ap_payload <tool_name|-> <cwd|-> <command> -- a PreToolUse payload; "-"
# omits that field entirely.
ap_payload() {
    jq -n --arg t "$1" --arg c "$2" --arg cmd "$3" \
        '{tool_input: {command: $cmd}}
         + (if $t == "-" then {} else {tool_name: $t} end)
         + (if $c == "-" then {} else {cwd: $c} end)'
}

# assert_undeterminable <label> -- exit 2, the dedicated message, and no diff
# body echoed into it.
assert_undeterminable() {
    local label="$1"
    assert_exit "${label}" 2
    if [[ "${GUARD_STDERR}" == *BLOCKED* \
        && "${GUARD_STDERR}" == *"effective working directory cannot be determined"* \
        && "${GUARD_STDERR}" == *"use an absolute feature-worktree path"* ]]; then
        pass
    else
        fail "${label}: stderr should be the undeterminable-cwd BLOCKED message, got: ${GUARD_STDERR}"
    fi
    if [[ "${GUARD_STDERR}" == *"${RAP_SECRET}"* ]]; then
        fail "${label}: BLOCKED message leaked the diff body, got: ${GUARD_STDERR}"
    else
        pass
    fi
}

for kind in add update delete move; do
    # Worktree session, cwd == hook pwd, relative targets: legitimate, allowed.
    echo "case: relative ${kind} from a worktree session whose payload cwd is the hook cwd is allowed (#1212)"
    run_guard "${RAP_WT}" "$(ap_payload apply_patch "${RAP_WT}" "$(ap_env "${kind}" '')")"
    assert_exit "relative ${kind} in a worktree session allowed" 0

    # Root session, cwd == hook pwd, relative target resolving to the main worktree.
    echo "case: relative ${kind} from a main-worktree session is blocked on the main-worktree decision (#1212)"
    run_guard "${RAP_REPO}" "$(ap_payload apply_patch "${RAP_REPO}" "$(ap_env "${kind}" '')")"
    assert_exit "relative ${kind} resolving to the main worktree blocked" 2
    if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
        pass
    else
        fail "relative ${kind} resolving to the main worktree blocked: stderr should be the main-worktree message, got: ${GUARD_STDERR}"
    fi

    # The same in-worktree relative target from the root is allowed today
    # (repo-root cwd + .worktrees/ prefix); it must still be, with the real shape.
    echo "case: relative ${kind} under .worktrees/ from a main-worktree session with the real payload shape is allowed (#1212, #1036 preserved)"
    run_guard "${RAP_REPO}" "$(ap_payload apply_patch "${RAP_REPO}" "$(ap_env "${kind}" '.worktrees/1212-wt/')")"
    assert_exit "relative ${kind} under .worktrees/ with the real shape allowed" 0

    # Missing tool_name: cannot show this is a direct apply_patch call.
    echo "case: relative ${kind} with no tool_name fails closed (#1212)"
    run_guard "${RAP_REPO}" "$(ap_payload - "${RAP_REPO}" "$(ap_env "${kind}" '.worktrees/1212-wt/')")"
    assert_undeterminable "relative ${kind} without tool_name"

    # Bash-routed heredoc: exec_command's workdir is invisible to the hook.
    echo "case: relative ${kind} inside a Bash-routed apply_patch heredoc fails closed (#1212, the worktree-session / root-workdir bypass shape)"
    run_guard "${RAP_WT}" "$(ap_payload Bash "${RAP_WT}" "$(ap_heredoc "$(ap_env "${kind}" '')")")"
    assert_undeterminable "relative ${kind} in a tool_name=Bash heredoc"

    # Payload cwd differs from the hook's cwd (both directions).
    echo "case: relative ${kind} whose payload cwd differs from the hook cwd fails closed (#1212)"
    run_guard "${RAP_WT}" "$(ap_payload apply_patch "${RAP_REPO}" "$(ap_env "${kind}" '')")"
    assert_undeterminable "relative ${kind} with payload cwd != hook cwd (worktree hook, root payload)"
    run_guard "${RAP_REPO}" "$(ap_payload apply_patch "${RAP_WT}" "$(ap_env "${kind}" '.worktrees/1212-wt/')")"
    assert_undeterminable "relative ${kind} with payload cwd != hook cwd (root hook, worktree payload)"

    # A cwd that is not a clean absolute path.
    echo "case: relative ${kind} with a newline-bearing, relative or missing payload cwd fails closed (#1212)"
    run_guard "${RAP_WT}" "$(ap_payload apply_patch "${RAP_WT}"$'\n'"/elsewhere" "$(ap_env "${kind}" '')")"
    assert_undeterminable "relative ${kind} with an embedded-newline cwd"
    run_guard "${RAP_WT}" "$(ap_payload apply_patch "${RAP_WT}"$'\n' "$(ap_env "${kind}" '')")"
    assert_undeterminable "relative ${kind} with a trailing-newline cwd"
    run_guard "${RAP_REPO}" "$(ap_payload apply_patch "." "$(ap_env "${kind}" '.worktrees/1212-wt/')")"
    assert_undeterminable "relative ${kind} with a relative cwd"
    run_guard "${RAP_REPO}" "$(ap_payload apply_patch - "$(ap_env "${kind}" '.worktrees/1212-wt/')")"
    assert_undeterminable "relative ${kind} with no cwd"
done

# A symlinked spelling of the same directory is the SAME directory: the
# comparison is canonical(cwd) == canonical(pwd), not a string compare, so
# this must not over-block.
echo "case: relative add whose payload cwd is a symlink to the hook cwd is allowed (canonical comparison, #1212)"
ln -s "${RAP_WT}" "${TEST_ROOT}/relative-apply-patch-link"
run_guard "${RAP_WT}" "$(ap_payload apply_patch "${TEST_ROOT}/relative-apply-patch-link" "$(ap_env add '')")"
assert_exit "payload cwd symlinked to hook cwd allowed" 0

# Absolute targets never needed a cwd: the new predicate must not touch them.
echo "case: absolute targets are decided exactly as before even when the payload shape is incomplete (#1212)"
run_guard "${RAP_WT}" "$(ap_payload - - "$(ap_env add "${RAP_WT}/")")"
assert_exit "absolute worktree target with no tool_name/cwd allowed" 0
run_guard "${RAP_WT}" "$(ap_payload - - "$(ap_env update "${RAP_REPO}/")")"
assert_exit "absolute main-worktree target with no tool_name/cwd blocked" 2

# Bash relative targets get no cwd-join exception, whatever the payload says
# (#810): the redirect's effective directory is unknowable from the hook.
echo "case: a plain relative Bash redirect stays rejected even with tool_name=Bash and cwd == hook cwd (#810 preserved)"
run_guard "${RAP_WT}" "$(ap_payload Bash "${RAP_WT}" 'printf x > notes.txt')"
assert_exit "relative Bash redirect still rejected" 2
if [[ "${GUARD_STDERR}" == *"cannot verify the relative Bash write target"* ]]; then
    pass
else
    fail "relative Bash redirect still rejected: stderr should be the #810 relative-target message, got: ${GUARD_STDERR}"
fi

# ── Ticket #1212: env-gated capture tap ────────────────────────────────
# CENCI_CODEX_HOOK_CAPTURE_DIR lets the native runner record the payload cwd
# and the hook process cwd. It is write-only evidence plumbing inside a
# security guard: gated on an absolute, existing, non-symlink directory, and it
# must never change a decision or the stderr a caller sees.

# cap_entries <dir> -- number of filesystem entries below <dir>. A missing
# directory yields the non-numeric "missing-dir" (never 0), so an assertion
# of "nothing was written there" cannot pass vacuously on a wrong path.
cap_entries() {
    local listing
    [[ -d "$1" ]] || { echo "missing-dir"; return 0; }
    listing="$(find "$1" -mindepth 1)" || { echo "find-failed"; return 0; }
    if [[ -z "${listing}" ]]; then echo 0; else printf '%s\n' "${listing}" | wc -l | tr -d '[:space:]'; fi
}

# cap_lines <dir> -- total lines across every regular file below <dir>
# ("missing-dir" / "find-failed" when it cannot be counted).
cap_lines() {
    local content
    [[ -d "$1" ]] || { echo "missing-dir"; return 0; }
    content="$(find "$1" -type f -exec cat {} +)" || { echo "find-failed"; return 0; }
    if [[ -z "${content}" ]]; then echo 0; else printf '%s\n' "${content}" | wc -l | tr -d '[:space:]'; fi
}

# cap_tree <dir> -- sorted listing of every entry below <dir> outside .git, so
# a before/after comparison proves the tap wrote nothing into the cwd.
cap_tree() {
    find "$1" -mindepth 1 -not -path '*/.git' -not -path '*/.git/*' | LC_ALL=C sort
}

# run_guard_capture <cwd> <capture-dir-value> <json> -- run_guard with the tap
# variable set to <capture-dir-value> for this one invocation.
run_guard_capture() {
    local cwd="$1" capdir="$2" json="$3"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | CENCI_CODEX_HOOK_CAPTURE_DIR="${capdir}" "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# Payloads: an allowed absolute envelope, a main-worktree denial, an
# undeterminable-cwd denial (no tool_name), and a Write|Edit file_path call.
TAP_ALLOW="$(ap_payload apply_patch "${RAP_OTHER}" "$(ap_env add "${RAP_WT}/")")"
TAP_DENY_MAIN="$(ap_payload apply_patch "${RAP_OTHER}" "$(ap_env update "${RAP_REPO}/")")"
TAP_DENY_CWD="$(ap_payload - "${RAP_OTHER}" "$(ap_env add '.worktrees/1212-wt/')")"
TAP_WRITE="$(jq -n --arg c "${RAP_OTHER}" --arg f "${RAP_WT}/x.txt" '{tool_name: "Write", cwd: $c, tool_input: {file_path: $f}}')"

echo "case: capture tap unset or empty writes nothing into the cwd or a stray directory (#1212)"
TAP_DIR="${TEST_ROOT}/tap-unset"
mkdir -p "${TAP_DIR}"
chmod 700 "${TAP_DIR}"
TAP_BEFORE="$(cap_tree "${RAP_REPO}")"
run_guard "${RAP_REPO}" "${TAP_ALLOW}"
assert_exit "tap unset, allowed call" 0
run_guard_capture "${RAP_REPO}" "" "${TAP_ALLOW}"
assert_exit "tap empty, allowed call" 0
if [[ "$(cap_tree "${RAP_REPO}")" == "${TAP_BEFORE}" && "$(cap_entries "${TAP_DIR}")" == 0 ]]; then
    pass
else
    fail "tap unset/empty: the cwd listing changed or the unrelated directory received entries (found $(cap_entries "${TAP_DIR}"))"
fi
if [[ "$(cap_entries "${TEST_ROOT}/tap-does-not-exist")" == "missing-dir" ]]; then
    pass
else
    fail "cap_entries must report a nonexistent directory as missing-dir, never 0"
fi

for tap_case in "allow|${TAP_ALLOW}|0" "deny-main|${TAP_DENY_MAIN}|2" "deny-cwd|${TAP_DENY_CWD}|2" "write|${TAP_WRITE}|0"; do
    tap_name="${tap_case%%|*}"
    tap_rest="${tap_case#*|}"
    tap_json="${tap_rest%|*}"
    tap_expect="${tap_rest##*|}"
    echo "case: capture tap set to a valid directory writes exactly one line (${tap_name}), before the decision (#1212)"
    TAP_DIR="${TEST_ROOT}/tap-set-${tap_name}"
    mkdir -p "${TAP_DIR}"
    chmod 700 "${TAP_DIR}"
    run_guard_capture "${RAP_REPO}" "${TAP_DIR}" "${tap_json}"
    assert_exit "tap set, ${tap_name} call keeps its decision" "${tap_expect}"
    if [[ "$(cap_lines "${TAP_DIR}")" == 1 ]]; then
        pass
    else
        fail "tap set (${tap_name}): expected exactly one captured line, found $(cap_lines "${TAP_DIR}")"
    fi
    # Each call owns its file (mktemp, O_EXCL): a second call adds a second
    # file rather than appending to the first.
    run_guard_capture "${RAP_REPO}" "${TAP_DIR}" "${tap_json}"
    if [[ "$(cap_entries "${TAP_DIR}")" == 2 && "$(cap_lines "${TAP_DIR}")" == 2 ]]; then
        pass
    else
        fail "tap set (${tap_name}): two calls must leave two per-call files of one line each, found $(cap_entries "${TAP_DIR}") entries / $(cap_lines "${TAP_DIR}") lines"
    fi
    rm -f "${TAP_DIR}"/guard-main-worktree.*
    run_guard_capture "${RAP_REPO}" "${TAP_DIR}" "${tap_json}"
    # The line records both directories (payload cwd and hook cwd) and never
    # the patch body.
    TAP_CONTENT="$(find "${TAP_DIR}" -type f -exec cat {} +)"
    if [[ "${TAP_CONTENT}" == *"${RAP_OTHER}"* && "${TAP_CONTENT}" == *"${RAP_REPO}"* ]]; then
        pass
    else
        fail "tap set (${tap_name}): the captured line should carry the payload cwd (${RAP_OTHER}) and the hook cwd (${RAP_REPO}), got: ${TAP_CONTENT}"
    fi
    if [[ "${TAP_CONTENT}" == *"${RAP_SECRET}"* ]]; then
        fail "tap set (${tap_name}): the captured line leaked the diff body"
    else
        pass
    fi
done

echo "case: capture tap set to a symlinked, relative or missing directory writes nothing (#1212)"
TAP_REAL="${TEST_ROOT}/tap-real"
mkdir -p "${TAP_REAL}"
ln -s "${TAP_REAL}" "${TEST_ROOT}/tap-link"
run_guard_capture "${RAP_REPO}" "${TEST_ROOT}/tap-link" "${TAP_ALLOW}"
assert_exit "tap via symlinked directory keeps the decision" 0
if [[ "$(cap_entries "${TAP_REAL}")" == 0 ]]; then
    pass
else
    fail "tap via symlinked directory: the symlink target must stay empty, found $(cap_entries "${TAP_REAL}") entries"
fi
mkdir -p "${RAP_REPO}/tap-relative"
run_guard_capture "${RAP_REPO}" "tap-relative" "${TAP_ALLOW}"
assert_exit "tap via relative directory keeps the decision" 0
if [[ "$(cap_entries "${RAP_REPO}/tap-relative")" == 0 ]]; then
    pass
else
    fail "tap via relative directory: expected no capture entries, found $(cap_entries "${RAP_REPO}/tap-relative")"
fi
run_guard_capture "${RAP_REPO}" "${TEST_ROOT}/tap-missing" "${TAP_ALLOW}"
assert_exit "tap via missing directory keeps the decision" 0
if [[ -e "${TEST_ROOT}/tap-missing" ]]; then
    fail "tap via missing directory: the guard must not create the directory"
else
    pass
fi

echo "case: capture tap set to a group/world-writable directory writes nothing (#1212)"
for tap_mode in 770 707 777; do
    TAP_WW="${TEST_ROOT}/tap-writable-${tap_mode}"
    mkdir -p "${TAP_WW}"
    chmod "${tap_mode}" "${TAP_WW}"
    run_guard_capture "${RAP_REPO}" "${TAP_WW}" "${TAP_ALLOW}"
    assert_exit "tap via mode-${tap_mode} directory keeps the decision" 0
    if [[ "$(cap_entries "${TAP_WW}")" == 0 ]]; then
        pass
    else
        fail "tap via mode-${tap_mode} directory: expected no capture entries, found $(cap_entries "${TAP_WW}")"
    fi
done

echo "case: capture tap never writes through a pre-planted symlink at the legacy shared file name (#1212)"
TAP_PLANT="${TEST_ROOT}/tap-planted"
TAP_VICTIM="${TEST_ROOT}/tap-victim.txt"
mkdir -p "${TAP_PLANT}"
chmod 700 "${TAP_PLANT}"
printf 'untouched\n' > "${TAP_VICTIM}"
ln -s "${TAP_VICTIM}" "${TAP_PLANT}/guard-main-worktree.log"
run_guard_capture "${RAP_REPO}" "${TAP_PLANT}" "${TAP_ALLOW}"
assert_exit "tap with a planted symlink keeps the decision" 0
if [[ "$(cat "${TAP_VICTIM}")" == untouched && "$(cap_entries "${TAP_PLANT}")" == 2 ]]; then
    pass
else
    fail "tap with a planted symlink: the victim file must stay untouched and the call must get its own new file (victim: $(cat "${TAP_VICTIM}"), entries: $(cap_entries "${TAP_PLANT}"))"
fi

echo "case: the exit code and stderr are identical with and without the capture tap (#1212)"
TAP_DIR="${TEST_ROOT}/tap-parity"
mkdir -p "${TAP_DIR}"
chmod 700 "${TAP_DIR}"
for parity_json in "${TAP_ALLOW}" "${TAP_DENY_MAIN}" "${TAP_DENY_CWD}" "${TAP_WRITE}"; do
    run_guard "${RAP_REPO}" "${parity_json}"
    PARITY_EXIT="${GUARD_EXIT}"
    PARITY_STDERR="${GUARD_STDERR}"
    run_guard_capture "${RAP_REPO}" "${TAP_DIR}" "${parity_json}"
    if [[ "${GUARD_EXIT}" -eq "${PARITY_EXIT}" && "${GUARD_STDERR}" == "${PARITY_STDERR}" ]]; then
        pass
    else
        fail "tap parity: exit ${GUARD_EXIT} vs ${PARITY_EXIT} without the tap; stderr differs: '${GUARD_STDERR}' vs '${PARITY_STDERR}'"
    fi
done

# ── Ticket #1072: Write|Edit targets that canonicalize outside the repo ────
# The Bash arm already allows absolute targets that canonicalize outside the
# resolved repo root (#795 Q1a, #810). The Write|Edit arm gains the same
# allow, gated on a shared SCOPE_ROOT derived from `git rev-parse
# --git-common-dir` (its parent, adopted only when it is a strict ancestor of
# the resolved repo root -- a real linked worktree; otherwise SCOPE_ROOT
# falls back to the resolved repo root). A dedicated repo (OOR_REPO) keeps
# this section's fixtures order-independent from earlier cases.
OOR_REPO="${TEST_ROOT}/out-of-repo"
make_git_repo "${OOR_REPO}"
mkdir -p "${OOR_REPO}/.cenci"
touch "${OOR_REPO}/.cenci/config.json"

# AC 1: a ~/.claude/projects/<slug>/memory/<file>.md-shaped absolute path
# entirely outside the repo -- the reported case (#1072) -- must be allowed.
# The target file itself exists, exercising the resolve_path("$FILE_PATH")
# branch (not the not-yet-existing ancestor-walk branch).
echo "case: out-of-repo ~/.claude/projects/<slug>/memory/<file>.md path is allowed (#1072 AC 1, the reported case)"
OOR_HOME="${TEST_ROOT}/oor-home"
mkdir -p "${OOR_HOME}/.claude/projects/-workspace/memory"
touch "${OOR_HOME}/.claude/projects/-workspace/memory/notes.md"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/projects/-workspace/memory/notes.md\"}}"
assert_exit "out-of-repo memory path allowed (#1072 AC 1)" 0

# AC 1: same shape but the tail does not exist yet (multi-level), exercising
# the parent-anchored ancestor-walk canonicalization on the allow path.
echo "case: out-of-repo ~/.claude/projects/... path with a not-yet-existing multi-level tail is allowed (#1072 AC 1)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/projects/-workspace/memory/newdir/newsub/new.md\"}}"
assert_exit "out-of-repo memory path, multi-level missing tail, allowed (#1072 AC 1)" 0

# AC 2: a symlink planted OUTSIDE the repo but pointing AT <repo>/src must
# still be blocked -- the repo-scope decision is made on the CANONICAL form,
# never the lexical (pre-symlink-resolution) path.
echo "case: symlink planted outside the repo pointing at <repo>/src is blocked (#1072 AC 2)"
mkdir -p "${OOR_REPO}/src"
ln -s "${OOR_REPO}/src" "${TEST_ROOT}/oor-symlink-into-repo"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/oor-symlink-into-repo/evil.txt\"}}"
assert_exit "symlink outside repo resolving into <repo>/src blocked (#1072 AC 2)" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "symlink outside repo resolving into <repo>/src blocked: stderr should contain the main-worktree block text, got: ${GUARD_STDERR}"
fi

# AC 2: a ".." sequence that lexically starts outside the repo must still be
# collapsed BEFORE the repo-scope decision -- it lands back inside the repo
# here, so it must still block.
echo "case: <out-of-repo-dir>/../<repo-basename>/src/evil.txt collapses into the repo and is blocked (#1072 AC 2)"
mkdir -p "${TEST_ROOT}/oor-dotdot-base"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/oor-dotdot-base/../${OOR_REPO##*/}/src/evil.txt\"}}"
assert_exit "dot-dot collapse back into the repo blocked (#1072 AC 2)" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "dot-dot collapse back into the repo blocked: stderr should contain the main-worktree block text, got: ${GUARD_STDERR}"
fi

# AC 5 / message accuracy: an out-of-repo path whose ancestor is a dangling
# symlink must still fail closed via the existing unresolvable-symlink
# message, never be mislabeled as a main-worktree block.
echo "case: out-of-repo path under a dangling-symlink ancestor is blocked without the main-worktree message (#1072 AC 5)"
mkdir -p "${TEST_ROOT}/ac5-outside"
ln -s "${TEST_ROOT}/ac5-nonexistent-target" "${TEST_ROOT}/ac5-outside/dangling-link"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/ac5-outside/dangling-link/sub.txt\"}}"
assert_exit "out-of-repo dangling-symlink ancestor blocked (#1072 AC 5)" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    fail "out-of-repo dangling-symlink ancestor blocked: stderr must not mislabel this as a main-worktree block, got: ${GUARD_STDERR}"
else
    pass
fi

# ── #1072 Fix 3: self-protection denylist for out-of-repo Write|Edit
# targets ── the out-of-repo allow above (AC 1/2/5) deliberately lets a
# session write to ANY out-of-repo absolute path -- but a handful of paths
# are session-security-sensitive regardless of repo scope and must stay
# blocked even though they are outside SCOPE_ROOT: hook config
# (~/.claude/settings*.json), this hook's own plugin script
# (~/.claude/plugins/**), and SSH config/keys (~/.ssh/**). Each case asserts
# the BLOCKED message uses the new self-protection wording, never the
# main-worktree wording (these are not main-worktree targets).
echo "case: out-of-repo Write to ~/.claude/settings.json is blocked (#1072 Fix 3)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/settings.json\"}}"
assert_exit "out-of-repo settings.json blocked (#1072 Fix 3)" 2
if [[ "${GUARD_STDERR}" == *"session-security-sensitive"* ]]; then
    pass
else
    fail "out-of-repo settings.json blocked: stderr should contain the self-protection wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    fail "out-of-repo settings.json blocked: stderr must not use the main-worktree wording, got: ${GUARD_STDERR}"
else
    pass
fi

echo "case: out-of-repo Write to ~/.claude/settings.local.json is blocked (#1072 Fix 3)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/settings.local.json\"}}"
assert_exit "out-of-repo settings.local.json blocked (#1072 Fix 3)" 2

echo "case: out-of-repo Write under ~/.claude/plugins/ is blocked (#1072 Fix 3)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/plugins/cenci-flow/hooks/scripts/guard-main-worktree.sh\"}}"
assert_exit "out-of-repo .claude/plugins/ blocked (#1072 Fix 3)" 2

echo "case: out-of-repo Write under ~/.ssh/ is blocked (#1072 Fix 3)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.ssh/authorized_keys\"}}"
assert_exit "out-of-repo .ssh/ blocked (#1072 Fix 3)" 2

# Negative control: an out-of-repo path that merely resembles the denylist
# shapes (settings.json.tmp, the documented /cenci:configure intermediate
# file) must NOT be denylisted -- only the exact shapes matter.
echo "case: out-of-repo Write to ~/.claude/settings.json.tmp is still allowed (#1072 Fix 3 negative control)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude/settings.json.tmp\"}}"
assert_exit "out-of-repo settings.json.tmp allowed (#1072 Fix 3 negative control)" 0

# ── Regression: an IN-SCOPE path shaped like a denylist entry must be
# completely unaffected by the self-protection denylist (post-#1072-Fix-3
# HIGH regression fix). An earlier revision ran scope_self_protection_denied
# BEFORE scope_precheck, so this in-repo path -- exactly what /cenci:configure
# writes inside its own feature worktree -- was wrongly hard-blocked with the
# self-protection message instead of falling through to the ordinary
# allowlist/block logic. `.claude/settings.json` is not itself allowlisted
# below (only `.claude/plans/` is -- see Cases 9-11 above), so the CORRECT
# pre-existing-logic outcome for this in-repo, non-worktree path is exit 2
# with the ordinary main-worktree BLOCKED wording -- proving the outcome is
# unchanged from before the self-protection feature existed, not that this
# path is newly allowed.
echo "case: in-repo Write to <repo>/.claude/settings.json is blocked by the ordinary allowlist/block logic, NOT the self-protection denylist (#1072 regression fix)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_REPO}/.claude/settings.json\"}}"
assert_exit "in-repo .claude/settings.json blocked by ordinary logic (#1072 regression fix)" 2
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "in-repo .claude/settings.json blocked: stderr should use the ordinary main-worktree wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"session-security-sensitive"* ]]; then
    fail "in-repo .claude/settings.json blocked: stderr must NOT use the self-protection wording (the denylist must not run for an in-scope path), got: ${GUARD_STDERR}"
else
    pass
fi

echo "case: in-repo Write to <repo>/.worktrees/configure-init/.claude/settings.json (feature-worktree shape, /cenci:configure's real flow) is allowed, NOT blocked by the self-protection denylist (#1072 regression fix)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_REPO}/.worktrees/configure-init/.claude/settings.json\"}}"
assert_exit "in-repo feature-worktree .claude/settings.json allowed (#1072 regression fix)" 0

# AC 3 non-regression: a Write|Edit .plans/ path was not previously pinned by
# a dedicated case (only the Bash-mode arm has one) -- add it here so the
# SCOPE_ROOT refactor cannot silently regress it. .worktrees/ (Case 4),
# .claude/plans/ (Cases 9-10), designs/DESIGN.md now blocked (the cross-file
# pin above), the TMPDIR-widening allow cases, and in-repo source writes
# staying blocked (Case 3) are already pinned elsewhere in this suite.
echo "case: configured repo still allows .plans/ Write|Edit targets (#1072 AC 3 non-regression)"
run_guard "${REPO}" "{\"tool_input\":{\"file_path\":\"${REPO}/.plans/1-foo.md\"}}"
assert_exit "configured repo allows .plans/ Write|Edit target (#1072 AC 3)" 0

# ── Ticket #1072, Q1: SCOPE_ROOT derivation from a real linked worktree ────
# git rev-parse --show-toplevel from a cwd inside a FEATURE worktree returns
# the feature worktree, not the main worktree -- a literal "outside ROOT"
# check would wrongly allow main-worktree writes from a feature-worktree
# session. SCOPE_ROOT must be derived from `git rev-parse --git-common-dir`'s
# parent instead. -c user.email/-c user.name on the commit itself (not a
# persisted git config write) because CI runners may have no global git
# identity.
echo "── #1072 Q1: real linked worktree ──"
Q1_REPO="${TEST_ROOT}/q1-linked-worktree"
make_git_repo "${Q1_REPO}"
mkdir -p "${Q1_REPO}/.cenci" "${Q1_REPO}/src"
touch "${Q1_REPO}/.cenci/config.json"
echo "package app" > "${Q1_REPO}/src/app.go"
git -C "${Q1_REPO}" add -A
git -C "${Q1_REPO}" -c user.email="test@example.com" -c user.name="Test" commit -q -m "init"
Q1_FEATURE_WORKTREE="${Q1_REPO}/.worktrees/42-x"
git -C "${Q1_REPO}" worktree add -q -b feat-42-x "${Q1_FEATURE_WORKTREE}" >/dev/null
mkdir -p "${Q1_FEATURE_WORKTREE}/src"

echo "case: Write to the main worktree's src/app.go from a feature-worktree cwd is blocked (#1072 Q1 regression)"
run_guard "${Q1_FEATURE_WORKTREE}" "{\"tool_input\":{\"file_path\":\"${Q1_REPO}/src/app.go\"}}"
assert_exit "main-worktree Write from feature-worktree cwd blocked (#1072 Q1)" 2

echo "case: Write to the feature worktree's own src/app.go from that same cwd is allowed (#1072 Q1)"
run_guard "${Q1_FEATURE_WORKTREE}" "{\"tool_input\":{\"file_path\":\"${Q1_FEATURE_WORKTREE}/src/app.go\"}}"
assert_exit "feature-worktree Write allowed (#1072 Q1)" 0

# The load-bearing regression: today the Bash arm's pre-check tests
# containment against RESOLVED_ROOT (= the FEATURE worktree from this cwd),
# so a Bash redirect into the MAIN worktree's src/app.go resolves "outside
# the repo root" from that vantage point and is wrongly allowed outright
# (Q1a) -- exactly the hole #1072 closes by repointing the Bash arm's
# pre-check at SCOPE_ROOT too.
echo "case: Bash redirect to the main worktree's src/app.go from a feature-worktree cwd is blocked (#1072 Q1, Bash-arm tightening)"
JSON=$(jq -n --arg cmd "echo x > ${Q1_REPO}/src/app.go" '{tool_input:{command:$cmd}}')
run_guard "${Q1_FEATURE_WORKTREE}" "${JSON}"
assert_exit "bash redirect to main worktree from feature-worktree cwd blocked (#1072 Q1)" 2

# ── #1072 Fix 1: the empty-parse zero-target backstop must also scan for a
# MAIN worktree mention (SCOPE_ROOT), not just $ROOT/$RESOLVED_ROOT (the
# feature worktree from this cwd) -- an unparseable/unmodeled Bash write
# whose raw text names the main worktree path, but never mentions the
# feature worktree path at all, previously escaped this backstop entirely.
# A quoted `>` (inside the eval argument) produces zero extracted targets
# and trips bwt_zero_parse_suspicious, exactly like the existing
# allowlisted-subtree-neutralization cases above -- but this raw text
# mentions ONLY Q1_REPO (the main worktree / SCOPE_ROOT), never
# Q1_FEATURE_WORKTREE ($ROOT/$RESOLVED_ROOT from this cwd), isolating the
# fix: pre-#1072 Fix 1 code would not have matched at all here.
#
# #1084 updated this case's COMMAND, never its assertion. It previously used
# a bare `echo "a -> b, notes at <root>/README"`, which #1084 now proves
# inert (no path-write syntax, no re-parse verb) and deliberately allows --
# that command's suspicion was the false positive #1084 exists to remove.
# The `eval` wrapper restores a genuinely non-inert command with the same
# shape (quoted `>`, zero extracted targets, main-worktree-only mention), so
# this case still isolates exactly what it was written to prove: SCOPE_ROOT
# is among the roots the raw-text backstop scan matches against.
echo "case: zero-parse Bash command whose raw text mentions ONLY the main worktree path (not the feature worktree) is blocked (#1072 Fix 1)"
JSON=$(jq -n --arg cmd "eval \"echo a > ${Q1_REPO}/README\"" '{tool_input:{command:$cmd}}')
run_guard "${Q1_FEATURE_WORKTREE}" "${JSON}"
assert_exit "zero-parse backstop catches main-worktree-only mention from feature-worktree cwd (#1072 Fix 1)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "zero-parse backstop catches main-worktree-only mention: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi
# Message accuracy: the BLOCKED text must name the root that ACTUALLY matched
# (the main worktree / SCOPE_ROOT here), not $RESOLVED_ROOT -- which in this
# feature-worktree session is Q1_FEATURE_WORKTREE, a string the raw command
# text never contains, and which would send the agent chasing a path that
# isn't there.
if [[ "${GUARD_STDERR}" == *"(${Q1_REPO})"* ]]; then
    pass
else
    fail "zero-parse backstop catches main-worktree-only mention: stderr should name the matched main-worktree root (${Q1_REPO}), got: ${GUARD_STDERR}"
fi

# ── #1072 Fix 2: TMPDIR-widening containment checks measured against
# SCOPE_ROOT, not RESOLVED_ROOT ── in a feature-worktree session, a TMPDIR
# pointing inside the MAIN worktree (but outside the feature worktree) must
# not become TMPDIR_ALLOW -- that would silently re-open main-worktree
# containment via the TMPDIR-widening allowlist path on both arms.
echo "── #1072 Fix 2: TMPDIR pointing inside the main worktree from a feature-worktree session ──"
Q1_TMPDIR_INSIDE_MAIN="${Q1_REPO}/tmp"
mkdir -p "${Q1_TMPDIR_INSIDE_MAIN}"

echo "case: Write to a TMPDIR path inside the main worktree (outside the feature worktree) is blocked, not widened (#1072 Fix 2)"
run_guard_with_tmpdir "${Q1_FEATURE_WORKTREE}" "${Q1_TMPDIR_INSIDE_MAIN}" "{\"tool_input\":{\"file_path\":\"${Q1_TMPDIR_INSIDE_MAIN}/x.txt\"}}"
assert_exit "TMPDIR inside main worktree not widened, Write blocked (#1072 Fix 2)" 2
if [[ "${GUARD_STDERR}" == *BLOCKED* ]]; then
    pass
else
    fail "TMPDIR inside main worktree not widened: stderr should contain BLOCKED, got: ${GUARD_STDERR}"
fi

echo "case: Bash redirect to a TMPDIR path inside the main worktree (outside the feature worktree) is blocked, not widened (#1072 Fix 2)"
JSON=$(jq -n --arg cmd "echo x > ${Q1_TMPDIR_INSIDE_MAIN}/y.txt" '{tool_input:{command:$cmd}}')
run_guard_with_tmpdir "${Q1_FEATURE_WORKTREE}" "${Q1_TMPDIR_INSIDE_MAIN}" "${JSON}"
assert_exit "bash TMPDIR inside main worktree not widened, blocked (#1072 Fix 2)" 2

# ── Ticket #1072, Q1 fallback: a main-worktree session (no linked worktree)
# -- common-dir's parent equals ROOT, so SCOPE_ROOT falls back to ROOT.
# Asserted explicitly rather than relying on AC1/AC3 to imply it. ──
echo "── #1072 Q1 fallback: main-worktree session ──"
Q1_FALLBACK_REPO="${TEST_ROOT}/q1-fallback"
make_git_repo "${Q1_FALLBACK_REPO}"
mkdir -p "${Q1_FALLBACK_REPO}/.cenci"
touch "${Q1_FALLBACK_REPO}/.cenci/config.json"

echo "case: in-repo source write is blocked in a main-worktree session (#1072 Q1 fallback)"
run_guard "${Q1_FALLBACK_REPO}" "{\"tool_input\":{\"file_path\":\"${Q1_FALLBACK_REPO}/src/foo.txt\"}}"
assert_exit "main-worktree session in-repo write blocked (#1072 Q1 fallback)" 2

echo "case: out-of-repo write is allowed in a main-worktree session (#1072 Q1 fallback)"
run_guard "${Q1_FALLBACK_REPO}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/q1-fallback-outside/foo.txt\"}}"
assert_exit "main-worktree session out-of-repo write allowed (#1072 Q1 fallback)" 0

# ── Ticket #1072, Q1 fail-closed: git rev-parse --git-common-dir itself
# fails -- SCOPE_ROOT_OK must be set false, and the two decision sites must
# fail closed (exit 2, "could not be classified", never mislabeled as
# "targets the main worktree"). The shim passes every OTHER git subcommand
# through by exec (so the show-toplevel/config-gate calls still succeed) and
# is kept #!/bin/sh so it survives the dash/sh re-execs the suite performs.
echo "── #1072 Q1 fail-closed: git rev-parse --git-common-dir fails ──"
make_git_common_dir_fail_bin() {
    local dir="$1"
    shift
    mkdir -p "${dir}"
    local real_git
    real_git="$(command -v git)" || {
        echo "make_git_common_dir_fail_bin: 'git' not found on PATH" >&2
        exit 1
    }
    cat > "${dir}/git" <<GITSCRIPT
#!/bin/sh
if [ "\$1" = "rev-parse" ] && [ "\$2" = "--git-common-dir" ]; then
    exit 1
fi
exec '${real_git}' "\$@"
GITSCRIPT
    chmod +x "${dir}/git"
    local tool real
    for tool in "$@"; do
        real="$(command -v "${tool}")" || {
            echo "make_git_common_dir_fail_bin: '${tool}' not found on PATH" >&2
            exit 1
        }
        ln -s "${real}" "${dir}/${tool}"
    done
}
GIT_CDFAIL_BIN="${TEST_ROOT}/bin-git-common-dir-fail"
make_git_common_dir_fail_bin "${GIT_CDFAIL_BIN}" sh cat jq realpath mktemp rm

echo "case: git rev-parse --git-common-dir failure fails closed on an out-of-repo Write target (#1072 Q1 fail-closed)"
run_guard_with_path "${OOR_REPO}" "${GIT_CDFAIL_BIN}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/q1-failclosed-outside/foo.txt\"}}"
assert_exit "git-common-dir failure fails closed (#1072 Q1)" 2
if [[ "${GUARD_STDERR}" == *"could not be classified"* ]]; then
    pass
else
    fail "git-common-dir failure fails closed: stderr should contain the could-not-classify wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    fail "git-common-dir failure fails closed: stderr must never say 'targets the main worktree' for an unclassifiable target, got: ${GUARD_STDERR}"
else
    pass
fi

# #1072 Fix 4: the symmetric Bash-arm fail-closed case -- there was
# previously no equivalent to the Write|Edit-arm case immediately above for
# the Bash arm. Reuses the same git-failing shim, but with awk/wc also
# present (a fresh bin dir, GIT_CDFAIL_BASH_BIN) since the Bash arm's target
# extraction (bwt_extract_targets) needs both, unlike the Write|Edit arm.
echo "case: git rev-parse --git-common-dir failure fails closed on an out-of-repo Bash redirect target (#1072 Q1 fail-closed, Bash arm)"
GIT_CDFAIL_BASH_BIN="${TEST_ROOT}/bin-git-common-dir-fail-bash"
make_git_common_dir_fail_bin "${GIT_CDFAIL_BASH_BIN}" sh cat jq realpath mktemp rm awk wc
JSON=$(jq -n --arg cmd "echo x > ${TEST_ROOT}/q1-bash-failclosed-outside/foo.txt" '{tool_input:{command:$cmd}}')
run_guard_with_path "${OOR_REPO}" "${GIT_CDFAIL_BASH_BIN}" "${JSON}"
assert_exit "git-common-dir failure fails closed on Bash arm (#1072 Q1, Fix 4)" 2
if [[ "${GUARD_STDERR}" == *"could not be classified"* ]]; then
    pass
else
    fail "git-common-dir failure fails closed on Bash arm: stderr should contain the could-not-classify wording, got: ${GUARD_STDERR}"
fi
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    fail "git-common-dir failure fails closed on Bash arm: stderr must never say 'targets the main worktree' for an unclassifiable target, got: ${GUARD_STDERR}"
else
    pass
fi

# ── #1072 review: SCOPE_ROOT must not adopt an arbitrary ancestor ─────
# A BARE repository holding its worktrees inside itself reports a common dir
# of `<base>/proj.git` (the bare repo itself), whose parent `<base>` IS a
# strict ancestor of the worktree's resolved root. Adopting it would make
# SCOPE_ROOT the directory CONTAINING the repo -- for a real bare repo at
# ~/proj.git that is the user's whole home directory, putting every
# out-of-repo path (including the ~/.claude/projects/<slug>/memory/... writes
# #1072 exists to allow) under the main-worktree allowlist. The `.git`
# basename test in the derivation keeps this layout on the documented
# fall-back-to-$ROOT residual instead.
echo "── #1072 review: bare-repo common dir must not widen SCOPE_ROOT ──"
BARE_BASE="${TEST_ROOT}/bare-scope"
mkdir -p "${BARE_BASE}"
git init -q --bare "${BARE_BASE}/proj.git"
git -C "${BARE_BASE}/proj.git" worktree add -q wt1 >/dev/null 2>&1
BARE_WT="${BARE_BASE}/proj.git/wt1"
mkdir -p "${BARE_WT}/.cenci" "${BARE_BASE}/sibling"
touch "${BARE_WT}/.cenci/config.json"

echo "case: a sibling of a bare repo is NOT treated as in-repo (SCOPE_ROOT must not become the bare repo's parent)"
run_guard "${BARE_WT}" "{\"tool_input\":{\"file_path\":\"${BARE_BASE}/sibling/note.md\"}}"
assert_exit "bare-repo sibling path allowed, not swallowed by SCOPE_ROOT" 0

echo "case: the bare repo's own worktree still enforces the allowlist (guard is not disabled by the fallback)"
run_guard "${BARE_WT}" "{\"tool_input\":{\"file_path\":\"${BARE_WT}/src/app.go\"}}"
assert_exit "bare-repo worktree in-repo source write blocked" 2

# ── #1072 review: ~/.claude.json belongs on the self-protection denylist ──
# It is the user-scope Claude Code config holding mcpServers/enabledPlugins --
# the same "writable here means arbitrary command execution" risk as
# ~/.claude/settings.json's hook definitions, which the denylist already
# covers.
echo "case: out-of-repo Write to ~/.claude.json is blocked (self-protection denylist)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude.json\"}}"
assert_exit "out-of-repo ~/.claude.json blocked" 2
if [[ "${GUARD_STDERR}" == *"session-security-sensitive"* ]]; then
    pass
else
    fail "out-of-repo ~/.claude.json blocked: stderr should contain the self-protection wording, got: ${GUARD_STDERR}"
fi

echo "case: out-of-repo Bash redirect to ~/.claude.json is blocked (self-protection denylist)"
JSON=$(jq -n --arg cmd "echo x > ${BASH_SENSITIVE_HOME}/.claude.json" '{tool_input:{command:$cmd}}')
run_guard "${BASH_REPO}" "${JSON}"
assert_exit "out-of-repo bash ~/.claude.json blocked" 2

echo "case: out-of-repo Write to ~/.claude.json.tmp is still allowed (negative control)"
run_guard "${OOR_REPO}" "{\"tool_input\":{\"file_path\":\"${OOR_HOME}/.claude.json.tmp\"}}"
assert_exit "out-of-repo ~/.claude.json.tmp allowed" 0

# ── Ticket #1074: widened self-protection denylist ─────────────────────────
# Closes the three gaps #1073 left open in scope_self_protection_denied():
#   1. the zero-parse backstop never consulted the denylist,
#   2. a symlinked ~/.claude, ~/.ssh or ~/.codex hid the canonical path from
#      the text match (and a symlinked dotfile hid ~/.bashrc, ~/.gitconfig),
#   3. ~/.codex/, ~/.gitconfig, shell startup files and an out-of-repo .git
#      hooks/config directory were not covered.
# It does NOT model cp/mv/dd/sed -i/truncate; /cenci:configure's mv-based
# settings.json bootstrap depends on that, so its exact commands are pinned
# below ("configure pins"). Every case drives the hook through its real
# JSON-on-stdin entry point and asserts only the exit code and the stderr
# substrings `session-security-sensitive` / `outside the repository`.
echo "── #1074: widened self-protection denylist ──"

DL_OTHER_REPO="${TEST_ROOT}/oor-other-repo"
make_git_repo "${DL_OTHER_REPO}"

# dl_write_json <abs-path> / dl_bash_json <command> — PreToolUse JSON builders.
dl_write_json() { jq -n --arg p "$1" '{tool_input:{file_path:$p}}'; }
dl_bash_json() { jq -n --arg c "$1" '{tool_input:{command:$c}}'; }

# assert_stderr_has / assert_stderr_lacks <label> <substring> — literal match
# against the last run's GUARD_STDERR.
assert_stderr_has() {
    if [[ "${GUARD_STDERR}" == *"$2"* ]]; then
        pass
    else
        fail "$1: stderr should contain '$2', got: ${GUARD_STDERR}"
    fi
}
assert_stderr_lacks() {
    if [[ "${GUARD_STDERR}" == *"$2"* ]]; then
        fail "$1: stderr must not contain '$2', got: ${GUARD_STDERR}"
    else
        pass
    fi
}

# assert_sensitive_block <label> — the self-protection verdict: exit 2 with
# the `session-security-sensitive` wording.
assert_sensitive_block() {
    assert_exit "$1" 2
    assert_stderr_has "$1" "session-security-sensitive"
}

# run_guard_with_home_unset <cwd> <json> — like run_guard_with_home, but with
# HOME removed from the environment entirely.
run_guard_with_home_unset() {
    local cwd="$1" json="$2"
    GUARD_STDERR="$(cd "${cwd}" && echo "${json}" | env -u HOME "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
    GUARD_EXIT=$?
}

# AC 1 + AC 3: each of the 13 new shapes, written out-of-repo through
# Write|Edit, is blocked with the self-protection wording AND the "outside the
# repository" wording.
for dl_target in \
    "${OOR_HOME}/.codex/config.toml" \
    "${OOR_HOME}/.gitconfig" \
    "${OOR_HOME}/.config/git/config" \
    "${OOR_HOME}/.bashrc" \
    "${OOR_HOME}/.bash_profile" \
    "${OOR_HOME}/.bash_login" \
    "${OOR_HOME}/.profile" \
    "${OOR_HOME}/.zshrc" \
    "${OOR_HOME}/.zshenv" \
    "${OOR_HOME}/.zprofile" \
    "${OOR_HOME}/.zlogin" \
    "${DL_OTHER_REPO}/.git/hooks/pre-commit" \
    "${DL_OTHER_REPO}/.git/config"; do
    dl_label="${dl_target#"${TEST_ROOT}"/}"
    echo "case: out-of-repo Write|Edit to ${dl_label} is blocked (#1074 AC 1)"
    run_guard "${OOR_REPO}" "$(dl_write_json "${dl_target}")"
    assert_sensitive_block "Write|Edit ${dl_label} (#1074 AC 1)"
    assert_stderr_has "Write|Edit ${dl_label} (#1074 AC 3)" "outside the repository"
done

# AC 3: the category list now names git config and shell startup files.
echo "case: the self-protection message names git config and shell startup files (#1074 AC 3)"
run_guard "${OOR_REPO}" "$(dl_write_json "${OOR_HOME}/.gitconfig")"
assert_stderr_has "message categories: git config (#1074 AC 3)" "git config"
assert_stderr_has "message categories: shell startup file (#1074 AC 3)" "shell startup file"

# AC 2: the five required paths on the Bash arm too (redirect form). $HOME is
# left literal in the command, so the guard itself expands it from the HOME
# the case sets.
for dl_rel in .codex/config.toml .gitconfig .bashrc .zshrc; do
    echo "case: out-of-repo Bash redirect to \$HOME/${dl_rel} is blocked (#1074 AC 2)"
    run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo x > \$HOME/${dl_rel}")"
    assert_sensitive_block "Bash redirect \$HOME/${dl_rel} (#1074 AC 2)"
    assert_stderr_has "Bash redirect \$HOME/${dl_rel} (#1074 AC 3)" "outside the repository"
done
echo "case: out-of-repo Bash redirect to <other-repo>/.git/hooks/pre-commit is blocked (#1074 AC 2)"
run_guard "${OOR_REPO}" "$(dl_bash_json "echo x > ${DL_OTHER_REPO}/.git/hooks/pre-commit")"
assert_sensitive_block "Bash redirect other-repo .git/hooks/pre-commit (#1074 AC 2)"
assert_stderr_has "Bash redirect other-repo .git/hooks/pre-commit (#1074 AC 3)" "outside the repository"

# AC 4: symlinked config directories. HOME/.claude, HOME/.ssh and HOME/.codex
# are symlinks into HOME/dotfiles/<dir>; the canonical target no longer
# contains /.claude/ (etc.), so a write to the link, a write straight to the
# real directory, and a Bash redirect through $HOME must all still be blocked.
DL_SYM_HOME="${TEST_ROOT}/sym-home"
mkdir -p "${DL_SYM_HOME}/dotfiles/claude" "${DL_SYM_HOME}/dotfiles/ssh" "${DL_SYM_HOME}/dotfiles/codex" \
    || fail "fixture: mkdir symlinked-config-directory home"
echo '{}' > "${DL_SYM_HOME}/dotfiles/claude/settings.json" || fail "fixture: write dotfiles/claude/settings.json"
touch "${DL_SYM_HOME}/dotfiles/ssh/authorized_keys" "${DL_SYM_HOME}/dotfiles/codex/config.toml" \
    || fail "fixture: touch dotfiles ssh/codex files"
ln -s "${DL_SYM_HOME}/dotfiles/claude" "${DL_SYM_HOME}/.claude" || fail "fixture: ln -s ~/.claude"
ln -s "${DL_SYM_HOME}/dotfiles/ssh" "${DL_SYM_HOME}/.ssh" || fail "fixture: ln -s ~/.ssh"
ln -s "${DL_SYM_HOME}/dotfiles/codex" "${DL_SYM_HOME}/.codex" || fail "fixture: ln -s ~/.codex"
for dl_sym in "claude|.claude|settings.json" "ssh|.ssh|authorized_keys" "codex|.codex|config.toml"; do
    dl_real="${dl_sym%%|*}"
    dl_rest="${dl_sym#*|}"
    dl_link="${dl_rest%%|*}"
    dl_file="${dl_rest#*|}"

    echo "case: symlinked ~/${dl_link}: Write to the link path is blocked (#1074 AC 4)"
    run_guard_with_home "${OOR_REPO}" "${DL_SYM_HOME}" "$(dl_write_json "${DL_SYM_HOME}/${dl_link}/${dl_file}")"
    assert_sensitive_block "symlinked ${dl_link}: Write to the link (#1074 AC 4)"

    echo "case: symlinked ~/${dl_link}: Write to the real dotfiles/${dl_real} directory is blocked (#1074 AC 4)"
    run_guard_with_home "${OOR_REPO}" "${DL_SYM_HOME}" "$(dl_write_json "${DL_SYM_HOME}/dotfiles/${dl_real}/${dl_file}")"
    assert_sensitive_block "symlinked ${dl_link}: Write to the real directory (#1074 AC 4)"

    echo "case: symlinked ~/${dl_link}: Bash redirect through \$HOME/${dl_link} is blocked (#1074 AC 4)"
    run_guard_with_home "${OOR_REPO}" "${DL_SYM_HOME}" "$(dl_bash_json "echo x > \$HOME/${dl_link}/${dl_file}")"
    assert_sensitive_block "symlinked ${dl_link}: Bash redirect (#1074 AC 4)"
done

# Q&A 1 (a): a symlinked dotfile (~/.bashrc -> real-dotfiles/bashrc). The
# canonical target is real-dotfiles/bashrc, which matches no shape, so the
# as-written path (pre-canonical) must be matched too -- on both arms.
DL_RC_HOME="${TEST_ROOT}/sym-rc-home"
mkdir -p "${DL_RC_HOME}/real-dotfiles" || fail "fixture: mkdir symlinked-dotfile home"
touch "${DL_RC_HOME}/real-dotfiles/bashrc" "${DL_RC_HOME}/real-dotfiles/gitconfig" \
    || fail "fixture: touch real-dotfiles"
ln -s "${DL_RC_HOME}/real-dotfiles/bashrc" "${DL_RC_HOME}/.bashrc" || fail "fixture: ln -s ~/.bashrc"
ln -s "${DL_RC_HOME}/real-dotfiles/gitconfig" "${DL_RC_HOME}/.gitconfig" || fail "fixture: ln -s ~/.gitconfig"
for dl_rc in .bashrc .gitconfig; do
    echo "case: symlinked dotfile ~/${dl_rc}: Bash redirect is blocked via the as-written path (#1074 Q&A 1a)"
    run_guard_with_home "${OOR_REPO}" "${DL_RC_HOME}" "$(dl_bash_json "echo x > \$HOME/${dl_rc}")"
    assert_sensitive_block "symlinked dotfile ${dl_rc}: Bash redirect (#1074 Q&A 1a)"

    echo "case: symlinked dotfile ~/${dl_rc}: Write|Edit is blocked via the as-written path (#1074 Q&A 1a)"
    run_guard_with_home "${OOR_REPO}" "${DL_RC_HOME}" "$(dl_write_json "${DL_RC_HOME}/${dl_rc}")"
    assert_sensitive_block "symlinked dotfile ${dl_rc}: Write|Edit (#1074 Q&A 1a)"
done

# Review-fix B: overlapping anchors. HOME/.codex is a symlink to the REAL
# directory HOME/.claude, so the canonical target <home>/.claude/config.toml
# rewrites to `.claude/config.toml` through the .claude anchor (no shape) but to
# `.codex/config.toml` through the .codex anchor (a shape). Every anchor must be
# tried, not only the first one that contains the target.
DL_OVL_HOME="${TEST_ROOT}/overlap-home"
mkdir -p "${DL_OVL_HOME}/.claude" || fail "fixture: mkdir overlapping-anchor home"
touch "${DL_OVL_HOME}/.claude/config.toml" || fail "fixture: touch overlapping-anchor config.toml"
ln -s "${DL_OVL_HOME}/.claude" "${DL_OVL_HOME}/.codex" || fail "fixture: ln -s ~/.codex -> ~/.claude"
echo "case: ~/.codex -> ~/.claude: Write to the real .claude/config.toml is blocked via the later anchor (#1074 review B)"
run_guard_with_home "${OOR_REPO}" "${DL_OVL_HOME}" "$(dl_write_json "${DL_OVL_HOME}/.claude/config.toml")"
assert_sensitive_block "overlapping anchors: Write to the real directory (#1074 review B)"
echo "case: ~/.codex -> ~/.claude: Bash redirect to \$HOME/.claude/config.toml is blocked via the later anchor (#1074 review B)"
run_guard_with_home "${OOR_REPO}" "${DL_OVL_HOME}" "$(dl_bash_json 'echo x > $HOME/.claude/config.toml')"
assert_sensitive_block "overlapping anchors: Bash redirect to the real directory (#1074 review B)"

# AC 5: zero-parse commands. The tokenizer extracts no target from these (the
# `>` is inside quotes), so only the raw-text backstop runs -- and it names no
# repo root, so before #1074 they exited 0. The new home-anchored scan blocks
# them.
echo "case: eval \"echo x > \$HOME/.claude/settings.json\" is blocked by the zero-parse home scan (#1074 AC 5)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'eval "echo x > $HOME/.claude/settings.json"')"
assert_sensitive_block "zero-parse eval \$HOME/.claude/settings.json (#1074 AC 5)"
assert_stderr_has "zero-parse eval: names the matched fragment (#1074 Q&A 8)" ".claude/settings"
assert_stderr_has "zero-parse eval: keeps the #1084 no-invented-redirect guidance (#1074 Q&A 8)" "NEVER add a redirect"

echo "case: sh -c 'echo x >> ~/.bashrc' is blocked by the zero-parse home scan (#1074 AC 5)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "sh -c 'echo x >> ~/.bashrc'")"
assert_sensitive_block "zero-parse sh -c ~/.bashrc (#1074 AC 5)"

# AC 5: every denylisted suffix is recognised after a home anchor.
for dl_suffix in \
    .claude/settings.json .claude.json .claude/plugins/x/hook.sh .ssh/authorized_keys \
    .codex/config.toml .gitconfig .config/git/config \
    .bashrc .bash_profile .bash_login .profile .zshrc .zshenv .zprofile .zlogin; do
    echo "case: zero-parse eval naming \$HOME/${dl_suffix} is blocked (#1074 AC 5)"
    run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "eval \"echo x > \$HOME/${dl_suffix}\"")"
    assert_sensitive_block "zero-parse eval \$HOME/${dl_suffix} (#1074 AC 5)"
    assert_stderr_has "zero-parse eval \$HOME/${dl_suffix}: blocked by the home scan, not a per-target block (#1074 AC 5)" "anchored at home"
done

# AC 5: every home anchor spelling is recognised, including the quoted-home
# spellings (Q&A 1b) and the literal HOME value.
for dl_anchor in '~/' '$HOME/' '${HOME}/' '"$HOME"/' '"${HOME}"/' "${OOR_HOME}/"; do
    echo "case: zero-parse sh -c naming ${dl_anchor}.bashrc is blocked (#1074 AC 5 / Q&A 1b)"
    run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "sh -c 'echo x >> ${dl_anchor}.bashrc'")"
    assert_sensitive_block "zero-parse anchor ${dl_anchor}.bashrc (#1074 AC 5 / Q&A 1b)"
    assert_stderr_has "zero-parse anchor ${dl_anchor}.bashrc: blocked by the home scan, not a per-target block (#1074 AC 5 / Q&A 1b)" "anchored at home"
done

# Control: a home-anchored mention of a NON-denylisted file keeps exiting 0, so
# the scan is not simply blocking every zero-parse command.
echo "case: zero-parse eval naming \$HOME/notes.txt (not denylisted) is still allowed (#1074 control)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'eval "echo x > $HOME/notes.txt"')"
assert_exit "zero-parse non-denylisted home mention allowed (#1074 control)" 0

# Review-fix A: ONE parseable redirect makes the target list non-empty, which
# skips the zero-parse arm -- so a re-parsed home-anchored write hidden behind
# it (eval / sh -c) must still be caught by the post-loop home scan.
echo "case: a parsed redirect plus eval \"echo x >> ~/.bashrc\" is blocked by the home scan (#1074 review A)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'echo ok > /tmp/x; eval "echo x >> ~/.bashrc"')"
assert_sensitive_block "partial parse: eval ~/.bashrc after a parsed redirect (#1074 review A)"
assert_stderr_has "partial parse: eval ~/.bashrc names the home-anchored scan (#1074 review A)" "anchored at home"

echo "case: a parsed redirect plus sh -c with \$HOME/.claude/settings.json is blocked by the home scan (#1074 review A)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json ": > /tmp/x && sh -c 'echo x > \$HOME/.claude/settings.json'")"
assert_sensitive_block "partial parse: sh -c \$HOME/.claude/settings.json after a parsed redirect (#1074 review A)"
assert_stderr_has "partial parse: sh -c \$HOME/.claude/settings.json names the home-anchored scan (#1074 review A)" "anchored at home"

# `env` is masked out of the post-loop verb check (it is a data word in the
# configure jq pins), but a real re-parse verb behind it still counts.
echo "case: a parsed redirect plus env sh -c with ~/.bashrc is still blocked (#1074 review A)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; env sh -c 'echo x >> ~/.bashrc'")"
assert_sensitive_block "partial parse: env sh -c ~/.bashrc after a parsed redirect (#1074 review A)"
assert_stderr_has "partial parse: env sh -c ~/.bashrc names the home-anchored scan (#1074 review A)" "anchored at home"

# Negative control: the post-loop scan needs a re-parse verb, so a plain parsed
# redirect that merely MENTIONS a denylisted home path keeps exiting 0.
echo "case: grep PATH ~/.bashrc > /tmp/out (parsed redirect, no re-parse verb) is still allowed (#1074 review A control)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'grep PATH ~/.bashrc > /tmp/out')"
assert_exit "parsed redirect mentioning ~/.bashrc without a re-parse verb allowed (#1074 review A control)" 0
assert_stderr_lacks "parsed redirect mentioning ~/.bashrc: no self-protection wording (#1074 review A control)" "session-security-sensitive"

# Review-fix E: a brace-built tee (`{tee,-a,~/.bashrc}`) is not a re-parse verb,
# so with a parseable decoy redirect in front it slipped past the verb-only
# trigger. A delimited `tee` token now also triggers the post-loop home scan.
echo "case: a parsed redirect decoy plus '| {tee,-a,~/.bashrc}' is blocked by the home scan (#1074 review E)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'echo ok > /tmp/x; echo evil | {tee,-a,~/.bashrc}')"
assert_sensitive_block "partial parse: decoy redirect + brace tee ~/.bashrc (#1074 review E)"
assert_stderr_has "partial parse: decoy redirect + brace tee names the home-anchored scan (#1074 review E)" "anchored at home"

echo "case: a parsed redirect decoy plus '| {tee,-a,\$HOME/.claude/settings.json}' is blocked by the home scan (#1074 review E)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'echo ok > /tmp/x; echo evil | {tee,-a,$HOME/.claude/settings.json}')"
assert_sensitive_block "partial parse: decoy redirect + brace tee \$HOME/.claude/settings.json (#1074 review E)"

# Pinned over-block: the delimited-tee trigger is a raw-text signal, so a
# parsed redirect plus a tee that only READS a home-anchored sensitive file is
# blocked too.
echo "case: 'cat ~/.bashrc | tee /tmp/out; echo a > /tmp/b' is blocked (#1074 pinned fail-closed)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'cat ~/.bashrc | tee /tmp/out; echo a > /tmp/b')"
assert_sensitive_block "parsed redirect + delimited tee reading ~/.bashrc blocked (#1074 pinned fail-closed)"
assert_stderr_has "parsed redirect + delimited tee reading ~/.bashrc: home scan (#1074 pinned fail-closed)" "anchored at home"

# Review-fix F: `env -S` / `--split-string` re-parses its argument text, so the
# `env` mask must not apply to it (the five configure pins, whose `.env` /
# `"env"` words are data, stay exit 0 -- they are pinned below).
echo "case: a parsed redirect decoy plus env -S 'tee -a \${HOME}/.bashrc' is blocked (#1074 review F)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; echo evil | env -S 'tee -a \${HOME}/.bashrc'")"
assert_sensitive_block "partial parse: decoy redirect + env -S tee \${HOME}/.bashrc (#1074 review F)"

# The tee above is also caught by the delimited-tee trigger, so these variants
# carry no tee: only the unmasked `env` verb can make them block.
echo "case: a parsed redirect decoy plus env -S 'dd of=\${HOME}/.bashrc' is blocked (#1074 review F)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; env -S 'dd of=\${HOME}/.bashrc'")"
assert_sensitive_block "partial parse: decoy redirect + env -S dd (#1074 review F)"

echo "case: a parsed redirect decoy plus env -iS 'dd of=~/.bashrc' is blocked (#1074 review F)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; env -iS 'dd of=~/.bashrc'")"
assert_sensitive_block "partial parse: decoy redirect + env -iS dd (#1074 review F)"

echo "case: a parsed redirect decoy plus env -i -S 'dd of=~/.bashrc' is blocked (#1074 review F)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; env -i -S 'dd of=~/.bashrc'")"
assert_sensitive_block "partial parse: decoy redirect + env -i -S dd (#1074 review F)"

echo "case: a parsed redirect decoy plus env --split-string 'dd of=~/.bashrc' is blocked (#1074 review F)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "echo ok > /tmp/x; env --split-string 'dd of=~/.bashrc'")"
assert_sensitive_block "partial parse: decoy redirect + env --split-string dd (#1074 review F)"

# Review-fix G: the partial-parse arm DID extract targets, so its first line
# must not claim it could not.
echo "case: the partial-parse home-scan message words its first line accurately (#1074 review G)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'echo ok > /tmp/x; echo evil | {tee,-a,~/.bashrc}')"
assert_stderr_has "partial-parse message: names the unmodelled text (#1074 review G)" "contains unmodelled shell text (a re-parsing verb or brace-built tee) alongside parsed redirects"
assert_stderr_lacks "partial-parse message: does not claim targets were not extracted (#1074 review G)" "could not extract"
assert_stderr_has "partial-parse message: names the matched fragment (#1074 review G)" "anchored at home (~/.bashrc)"
assert_stderr_has "partial-parse message: keeps the no-redirect guidance (#1074 review G)" "NEVER add a redirect just to satisfy this message"

# Q&A 2: the home scan runs AFTER the repo-root scan. A zero-parse command that
# names BOTH the main worktree root and a home-anchored denylisted path is
# blocked by the root scan, with its existing message (not the home-scan block's
# wording).
echo "case: zero-parse command naming the repo root AND \$HOME/.bashrc keeps the root-scan message (#1074 Q&A 2)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "eval \"echo x > ${OOR_REPO}/notes.txt; echo y >> \$HOME/.bashrc\"")"
assert_exit "zero-parse root + home mention blocked (#1074 Q&A 2)" 2
assert_stderr_has "zero-parse root + home mention: root-scan message is emitted (#1074 Q&A 2)" "the raw command text mentions the main worktree root (${OOR_REPO})"
assert_stderr_has "zero-parse root + home mention: root-scan rewrite guidance is emitted (#1074 Q&A 2)" "targeting the feature worktree (.worktrees/<id>-<desc>/) or a temp path."
assert_stderr_lacks "zero-parse root + home mention: not the home-scan wording (#1074 Q&A 2)" "anchored at home"
assert_stderr_lacks "zero-parse root + home mention: not the self-protection wording (#1074 Q&A 2)" "session-security-sensitive"

# Q&A 8: the zero-parse home-scan block names the matched fragment and keeps the
# #1084 agent-safe guidance: a no-write command is rewritten (never given an
# invented redirect), and the agent is told not to route around the block.
echo "case: zero-parse home-scan message keeps the #1084 guidance (#1074 Q&A 8)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "sh -c 'echo x >> ~/.bashrc'")"
assert_sensitive_block "zero-parse home-scan message (#1074 Q&A 8)"
assert_stderr_has "zero-parse home-scan message: names the matched fragment (#1074 Q&A 8)" "(~/.bashrc)"
assert_stderr_has "zero-parse home-scan message: no-write rewrite clause (#1074 Q&A 8)" "If the command performs NO write"
assert_stderr_has "zero-parse home-scan message: rewrite without the re-parse text (#1074 Q&A 8)" "rewrite it without that text"
assert_stderr_has "zero-parse home-scan message: never add a redirect (#1074 Q&A 8)" "NEVER add a redirect just to satisfy this message"
assert_stderr_has "zero-parse home-scan message: never route around with git checkout/stash (#1074 Q&A 8)" "never route around it with git checkout/stash/apply"
assert_stderr_has "zero-parse home-scan message: never route around with cp/mv (#1074 Q&A 8)" "cp/mv"
assert_stderr_has "zero-parse home-scan message: never route around by copying files (#1074 Q&A 8)" "by copying files"

# Review-fix G: byte-identity of the whole zero-parse message. The expected text
# is built from the known command and fragment and compared whole (the capture
# strips the final newline on both sides), so any drift in either line fails.
echo "case: the zero-parse home-scan message is byte-identical (#1074 review G)"
DL_EXACT_CMD="sh -c 'echo x >> ~/.bashrc'"
DL_EXACT_LINE1="BLOCKED: guard-main-worktree.sh could not extract this Bash command's write targets (unmodelled shell construct), and the raw command text names a session-security-sensitive path outside the repository (hook/MCP config, plugin script, git config or hooks, shell startup file, or SSH config/keys) anchored at home (~/.bashrc): ${DL_EXACT_CMD}"
DL_EXACT_LINE2="A write to such a path is refused regardless of the out-of-repo-scope allow policy. If the command performs NO write -- its write-shaped text comes from a re-parsing verb (eval, sh -c, xargs) or a shell-composed construct -- rewrite it without that text, or read files with your client's file-read tooling instead of a shell pipeline. NEVER add a redirect just to satisfy this message, and never route around it with git checkout/stash/apply, cp/mv, or by copying files."
DL_EXACT_EXPECTED="${DL_EXACT_LINE1}"$'\n'"${DL_EXACT_LINE2}"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "${DL_EXACT_CMD}")"
assert_exit "zero-parse home-scan exact stderr: blocked (#1074 review G)" 2
if [[ "${GUARD_STDERR}" == "${DL_EXACT_EXPECTED}" ]]; then
    pass
else
    fail "zero-parse home-scan exact stderr: expected:
${DL_EXACT_EXPECTED}
got:
${GUARD_STDERR}"
fi

# AC 6: /cenci:configure's settings.json bootstrap. The four jq commands and
# the mv command below are copied VERBATIM from
# flow/skills/configure/phases/guidance-settings.md; each must exit 0 under the
# Bash arm. The flow depends on the guard NOT modelling `mv` targets; each case
# also greps the doc for the exact command, so drift on either side fails a
# named test here.
DL_CONF_DOC="${SCRIPT_DIR}/../../skills/configure/phases/guidance-settings.md"
DL_CONF_HOME="${TEST_ROOT}/conf-home"
mkdir -p "${DL_CONF_HOME}/.claude" || fail "fixture: mkdir configure home"
echo '{}' > "${DL_CONF_HOME}/.claude/settings.json" || fail "fixture: write configure settings.json"

# guidance-settings.md step 5c (disable auto-compact), jq call.
DL_CONF_JQ_COMPACT_OFF="jq '.autoCompactEnabled = false | del(.env.CLAUDE_AUTOCOMPACT_PCT_OVERRIDE) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp"
# guidance-settings.md step 5c undo (re-enable auto-compact), jq call.
DL_CONF_JQ_COMPACT_ON="jq 'del(.autoCompactEnabled) | del(.env.CLAUDE_AUTOCOMPACT_PCT_OVERRIDE) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp"
# guidance-settings.md subagent-model step (set), jq call.
DL_CONF_JQ_SUBAGENT_SET="jq '. * {\"env\": {\"CLAUDE_CODE_SUBAGENT_MODEL\": \"claude-sonnet-5\"}}' ~/.claude/settings.json > ~/.claude/settings.json.tmp"
# guidance-settings.md subagent-model step (undo), jq call.
DL_CONF_JQ_SUBAGENT_DEL="jq 'del(.env.CLAUDE_CODE_SUBAGENT_MODEL) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp"
# guidance-settings.md, every one of the four steps: the mv into place.
DL_CONF_MV="mv ~/.claude/settings.json.tmp ~/.claude/settings.json"

# dl_configure_pin <label> <command> <home> — the guard allows <command> (exit
# 0, no self-protection wording) and guidance-settings.md still contains it.
dl_configure_pin() {
    local label="$1" cmd="$2" home="$3"
    run_guard_with_home "${OOR_REPO}" "${home}" "$(dl_bash_json "${cmd}")"
    assert_exit "${label}" 0
    assert_stderr_lacks "${label}" "session-security-sensitive"
    if grep -qF -- "${cmd}" "${DL_CONF_DOC}"; then
        pass
    else
        fail "${label}: command not found verbatim in guidance-settings.md (drift)"
    fi
}

echo "case: configure pin (guidance-settings.md): jq disabling auto-compact exits 0 (#1074 AC 6)"
dl_configure_pin "configure pin: jq autoCompactEnabled=false (guidance-settings.md)" "${DL_CONF_JQ_COMPACT_OFF}" "${DL_CONF_HOME}"
echo "case: configure pin (guidance-settings.md): jq re-enabling auto-compact exits 0 (#1074 AC 6)"
dl_configure_pin "configure pin: jq del(.autoCompactEnabled) (guidance-settings.md)" "${DL_CONF_JQ_COMPACT_ON}" "${DL_CONF_HOME}"
echo "case: configure pin (guidance-settings.md): jq setting the subagent model exits 0 (#1074 AC 6)"
dl_configure_pin "configure pin: jq set CLAUDE_CODE_SUBAGENT_MODEL (guidance-settings.md)" "${DL_CONF_JQ_SUBAGENT_SET}" "${DL_CONF_HOME}"
echo "case: configure pin (guidance-settings.md): jq removing the subagent model exits 0 (#1074 AC 6)"
dl_configure_pin "configure pin: jq del CLAUDE_CODE_SUBAGENT_MODEL (guidance-settings.md)" "${DL_CONF_JQ_SUBAGENT_DEL}" "${DL_CONF_HOME}"
echo "case: configure pin (guidance-settings.md): mv settings.json.tmp into place exits 0 (#1074 AC 6)"
dl_configure_pin "configure pin: mv settings.json.tmp settings.json (guidance-settings.md)" "${DL_CONF_MV}" "${DL_CONF_HOME}"
# Same pin, under a HOME whose .claude is a symlink (the anchor rewrite must
# not turn settings.json.tmp into a denylisted target).
echo "case: configure pin (guidance-settings.md) still exits 0 when ~/.claude is a symlink (#1074 AC 6)"
dl_configure_pin "configure pin under symlinked ~/.claude: jq autoCompactEnabled=false (guidance-settings.md)" "${DL_CONF_JQ_COMPACT_OFF}" "${DL_SYM_HOME}"

# Edge cases ──────────────────────────────────────────────────────────────

# An in-repo write never reaches the denylist: .codex is blocked by the
# ordinary logic in the main worktree and allowed under .worktrees/<id>/.
echo "case: in-repo Write to <repo>/.codex/agents/planner.toml is blocked by the ordinary logic (#1074 edge)"
run_guard "${OOR_REPO}" "$(dl_write_json "${OOR_REPO}/.codex/agents/planner.toml")"
assert_exit "in-repo .codex/agents/planner.toml blocked (#1074 edge)" 2
assert_stderr_has "in-repo .codex/agents/planner.toml: ordinary wording (#1074 edge)" "targets the main worktree"
assert_stderr_lacks "in-repo .codex/agents/planner.toml: no self-protection wording (#1074 edge)" "session-security-sensitive"

echo "case: in-repo Bash redirect to <repo>/.codex/agents/planner.toml is blocked by the ordinary logic (#1074 edge)"
run_guard "${OOR_REPO}" "$(dl_bash_json "echo x > ${OOR_REPO}/.codex/agents/planner.toml")"
assert_exit "in-repo bash .codex/agents/planner.toml blocked (#1074 edge)" 2
assert_stderr_has "in-repo bash .codex/agents/planner.toml: ordinary wording (#1074 edge)" "targets the main worktree"
assert_stderr_lacks "in-repo bash .codex/agents/planner.toml: no self-protection wording (#1074 edge)" "session-security-sensitive"

echo "case: in-repo Write to <repo>/.worktrees/configure-init/.codex/agents/planner.toml is allowed (#1074 edge)"
run_guard "${OOR_REPO}" "$(dl_write_json "${OOR_REPO}/.worktrees/configure-init/.codex/agents/planner.toml")"
assert_exit "in-repo feature-worktree .codex/agents/planner.toml allowed (#1074 edge)" 0

echo "case: in-repo Write to <repo>/.git/config is blocked by the ordinary logic (#1074 edge)"
run_guard "${OOR_REPO}" "$(dl_write_json "${OOR_REPO}/.git/config")"
assert_exit "in-repo .git/config blocked (#1074 edge)" 2
assert_stderr_has "in-repo .git/config: ordinary wording (#1074 edge)" "targets the main worktree"
assert_stderr_lacks "in-repo .git/config: no self-protection wording (#1074 edge)" "session-security-sensitive"

# A zero-parse mention of a feature-worktree .claude/settings.json is not
# anchored at home -- even when the repo lives directly under HOME, because
# <HOME>/<subdir>/.../.claude/settings is not <HOME>/.claude/settings.
echo "case: zero-parse mention of <repo>/.worktrees/x/.claude/settings.json is allowed, HOME = the repo's parent (#1074 edge)"
run_guard_with_home "${OOR_REPO}" "${TEST_ROOT}" "$(dl_bash_json "eval \"echo x > ${OOR_REPO}/.worktrees/x/.claude/settings.json\"")"
assert_exit "zero-parse feature-worktree .claude/settings.json mention allowed (#1074 edge)" 0

# HOME unset, empty, relative or `/`: the literal-$HOME form and the anchor
# rewrite are skipped, the `~/` and `$HOME/` raw forms and the lexical shapes
# still apply, and the guard never treats every path as denylisted.
# dl_run_odd_home <cwd> <json> — run the guard with the HOME value in $dl_home.
dl_run_odd_home() {
    case "${dl_home}" in
        "<unset>") run_guard_with_home_unset "$1" "$2" ;;
        "<empty>") run_guard_with_home "$1" "" "$2" ;;
        *) run_guard_with_home "$1" "${dl_home}" "$2" ;;
    esac
}
for dl_home in "<empty>" "relative/home" "/" "<unset>"; do
    echo "case: HOME=${dl_home}: zero-parse '~/.bashrc' mention is still blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_bash_json 'eval "echo x >> ~/.bashrc"')"
    assert_sensitive_block "HOME=${dl_home}: zero-parse ~/.bashrc (#1074 edge)"

    echo "case: HOME=${dl_home}: zero-parse '\$HOME/.bashrc' mention is still blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_bash_json 'eval "echo x >> $HOME/.bashrc"')"
    assert_sensitive_block "HOME=${dl_home}: zero-parse \$HOME/.bashrc (#1074 edge)"

    # The braced raw form: the single quotes keep the literal text `${HOME}/`
    # reaching the guard, which must match it without expanding HOME.
    echo "case: HOME=${dl_home}: zero-parse '\${HOME}/.bashrc' mention is still blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_bash_json 'sh -c '"'"'echo x >> ${HOME}/.bashrc'"'"'')"
    assert_sensitive_block "HOME=${dl_home}: zero-parse \${HOME}/.bashrc via sh -c (#1074 edge)"
    assert_stderr_has "HOME=${dl_home}: zero-parse \${HOME}/.bashrc names the matched fragment (#1074 edge)" '${HOME}/.bashrc'

    echo "case: HOME=${dl_home}: zero-parse eval '\${HOME}/.claude/settings.json' mention is still blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_bash_json 'eval "echo x > ${HOME}/.claude/settings.json"')"
    assert_sensitive_block "HOME=${dl_home}: zero-parse \${HOME}/.claude/settings.json via eval (#1074 edge)"
    assert_stderr_has "HOME=${dl_home}: zero-parse \${HOME}/.claude/settings.json names the matched fragment (#1074 edge)" '${HOME}/.claude/settings'

    echo "case: HOME=${dl_home}: an unrelated zero-parse '/tmp/.../.codex/x' mention is NOT blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_bash_json 'eval "echo x > /tmp/oor-zp-1074/.codex/x"')"
    assert_exit "HOME=${dl_home}: unrelated .codex mention allowed (#1074 edge)" 0

    echo "case: HOME=${dl_home}: the lexical Write to <home>/.gitconfig is still blocked (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_write_json "${OOR_HOME}/.gitconfig")"
    assert_sensitive_block "HOME=${dl_home}: lexical Write .gitconfig (#1074 edge)"

    echo "case: HOME=${dl_home}: an ordinary out-of-repo Write is NOT denylisted (#1074 edge)"
    dl_run_odd_home "${OOR_REPO}" "$(dl_write_json "${OOR_HOME}/.claude/projects/-workspace/memory/notes.md")"
    assert_exit "HOME=${dl_home}: ordinary out-of-repo Write allowed (#1074 edge)" 0
done

# A dangling .claude symlink: the anchor rewrite is skipped, and the existing
# unresolvable-symlink block (not the self-protection wording) still applies.
DL_DANG_HOME="${TEST_ROOT}/dangling-home"
mkdir -p "${DL_DANG_HOME}" || fail "fixture: mkdir dangling-symlink home"
ln -s "${DL_DANG_HOME}/missing-dotfiles" "${DL_DANG_HOME}/.claude" || fail "fixture: ln -s dangling ~/.claude"
echo "case: dangling ~/.claude symlink: Write is blocked by the existing unresolvable-symlink message (#1074 edge)"
run_guard_with_home "${OOR_REPO}" "${DL_DANG_HOME}" "$(dl_write_json "${DL_DANG_HOME}/.claude/settings.json")"
assert_exit "dangling .claude: Write blocked (#1074 edge)" 2
assert_stderr_has "dangling .claude: Write uses the unresolvable-symlink message (#1074 edge)" "does not resolve"
assert_stderr_lacks "dangling .claude: Write is not mislabeled self-protection (#1074 edge)" "session-security-sensitive"
echo "case: dangling ~/.claude symlink: Bash redirect is blocked by the existing unresolvable-symlink message (#1074 edge)"
run_guard_with_home "${OOR_REPO}" "${DL_DANG_HOME}" "$(dl_bash_json 'echo x > $HOME/.claude/settings.json')"
assert_exit "dangling .claude: Bash redirect blocked (#1074 edge)" 2
assert_stderr_has "dangling .claude: Bash uses the unresolvable-symlink message (#1074 edge)" "unresolvable symlink"
assert_stderr_lacks "dangling .claude: Bash is not mislabeled self-protection (#1074 edge)" "session-security-sensitive"

# A config directory symlinked to `/`. The guard skips an anchor whose
# canonical form is "/" as defence in depth: a "/" anchor would only produce the
# degenerate pattern `//*`, which no canonical path matches, so deleting that
# skip is not observable from here. This block pins the behaviour that matters
# -- "no over-block": an ordinary out-of-repo write elsewhere stays allowed,
# while a lexical-shape write is still blocked.
DL_ROOTLINK_HOME="${TEST_ROOT}/rootlink-home"
DL_ROOTLINK_OTHER="${TEST_ROOT}/rootlink-other"
mkdir -p "${DL_ROOTLINK_HOME}" "${DL_ROOTLINK_OTHER}" || fail "fixture: mkdir ~/.codex -> / homes"
ln -s / "${DL_ROOTLINK_HOME}/.codex" || fail "fixture: ln -s / ~/.codex"
echo "case: ~/.codex -> / : an ordinary out-of-repo Write elsewhere is NOT denylisted (#1074 Q&A 4)"
run_guard_with_home "${OOR_REPO}" "${DL_ROOTLINK_HOME}" "$(dl_write_json "${DL_ROOTLINK_OTHER}/notes.md")"
assert_exit "~/.codex -> /: ordinary out-of-repo Write allowed (#1074 Q&A 4)" 0
assert_stderr_lacks "~/.codex -> /: ordinary Write not mislabeled self-protection (#1074 Q&A 4)" "session-security-sensitive"

echo "case: ~/.codex -> / : an ordinary out-of-repo Bash redirect elsewhere is NOT denylisted (#1074 Q&A 4)"
run_guard_with_home "${OOR_REPO}" "${DL_ROOTLINK_HOME}" "$(dl_bash_json "echo x > ${DL_ROOTLINK_OTHER}/notes.md")"
assert_exit "~/.codex -> /: ordinary out-of-repo Bash redirect allowed (#1074 Q&A 4)" 0
assert_stderr_lacks "~/.codex -> /: ordinary Bash redirect not mislabeled self-protection (#1074 Q&A 4)" "session-security-sensitive"

echo "case: ~/.codex -> / : the lexical Write to <home>/.gitconfig is still blocked (#1074 Q&A 4)"
run_guard_with_home "${OOR_REPO}" "${DL_ROOTLINK_HOME}" "$(dl_write_json "${DL_ROOTLINK_HOME}/.gitconfig")"
assert_sensitive_block "~/.codex -> /: lexical Write .gitconfig (#1074 Q&A 4)"

echo "case: ~/.codex -> / : the lexical Bash redirect to \$HOME/.gitconfig is still blocked (#1074 Q&A 4)"
run_guard_with_home "${OOR_REPO}" "${DL_ROOTLINK_HOME}" "$(dl_bash_json 'echo x > $HOME/.gitconfig')"
assert_sensitive_block "~/.codex -> /: lexical Bash redirect .gitconfig (#1074 Q&A 4)"

# Accepted fail-closed false positive: the prefix match on .claude/settings
# also catches settings.json.tmp in a zero-parse (eval) command.
echo "case: zero-parse eval naming ~/.claude/settings.json.tmp is blocked (#1074 pinned false positive)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'eval "echo x > ~/.claude/settings.json.tmp"')"
assert_sensitive_block "zero-parse eval ~/.claude/settings.json.tmp (#1074 pinned false positive)"

# The plain configure mv is allowed (pinned above); the same mv with an
# fd-dup lands in the suspicious backstop and is blocked on purpose.
echo "case: 'mv ~/.claude/settings.json.tmp ~/.claude/settings.json 2>&1' is blocked (#1074 pinned fail-closed)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json "${DL_CONF_MV} 2>&1")"
assert_sensitive_block "mv with 2>&1 blocked (#1074 pinned fail-closed)"

# Over-block, pinned: ANY non-inert zero-parse command that names a home-anchored
# denylisted path is blocked even when it only reads. `ssh` inside `~/.ssh/`
# counts as a re-parse verb, so this read-only command is not provably inert.
echo "case: read-only 'ssh-add -l -F ~/.ssh/config 2>&1' is blocked (#1074 pinned fail-closed)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'ssh-add -l -F ~/.ssh/config 2>&1')"
assert_sensitive_block "read-only ssh-add naming ~/.ssh/config with 2>&1 blocked (#1074 pinned fail-closed)"
assert_stderr_has "read-only ssh-add: blocked by the home scan (#1074 pinned fail-closed)" "anchored at home"

# A provably inert read that mentions ~/.bashrc skips the backstop entirely.
echo "case: 'grep -n PATH ~/.bashrc 2>&1' (provably inert) is still allowed (#1074 edge)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json 'grep -n PATH ~/.bashrc 2>&1')"
assert_exit "inert grep of ~/.bashrc with 2>&1 allowed (#1074 edge)" 0

# The unconditional #810 delimited-tee block runs first, with its own message.
echo "case: '{tee,cat} ~/.bashrc' keeps the existing tee message (#1074 edge)"
run_guard_with_home "${OOR_REPO}" "${OOR_HOME}" "$(dl_bash_json '{tee,cat} ~/.bashrc')"
assert_exit "brace tee ~/.bashrc blocked (#1074 edge)" 2
assert_stderr_has "brace tee ~/.bashrc: existing tee message (#1074 edge)" "tee invocation"
assert_stderr_lacks "brace tee ~/.bashrc: not the new self-protection wording (#1074 edge)" "session-security-sensitive"

# ── #1084: the zero-parse backstop must not block PROVABLY INERT commands ──
# The backstop's suspicion test (bwt_zero_parse_suspicious) is a quoting-blind
# `*'>'*` substring match, so a read-only command whose only `>` characters
# live inside quotes -- a PCRE lookaround, an HTML tag, an `2>&1` fd-dup --
# reached the raw-text root-mention scan and was blocked despite writing
# nothing at all. Agents, not humans, choose the command shape, so this
# recurred constantly and its remediation text ("rewrite using a plain,
# directly-parseable redirect") actively misdirects an agent that never
# intended a write. bwt_zero_parse_inert now proves such a command inert and
# the guard skips the backstop for it; every case below that is NOT provably
# inert must keep blocking exactly as before.
INERT_REPO="${TEST_ROOT}/inert-backstop"
make_git_repo "${INERT_REPO}"
mkdir -p "${INERT_REPO}/.cenci" "${INERT_REPO}/src"
touch "${INERT_REPO}/.cenci/config.json"

echo "── #1084: provably-inert read-only commands are allowed ──"

# The reported case: PCRE lookbehind/lookahead put `>` and `<` inside single
# quotes, and the `cd` prefix names the repo root, so the root-mention scan hit.
echo "case: quoted-regex grep naming the repo root is allowed (#1084 reported case)"
JSON=$(jq -n --arg cmd "cd ${INERT_REPO}/src && grep -oP '(?<=>)[^<>{}]{25,}(?=<)' index.njk | grep -v '^\\s*\$' | head -60" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "quoted-regex grep naming repo root allowed (#1084)" 0

# `2>&1` sets mode="fddup", emits nothing, and leaves zero targets -- an
# fd-dup writes to an already-open fd, never to a path.
echo "case: 2>&1 on a read naming the repo root is allowed (#1084)"
JSON=$(jq -n --arg cmd "grep -r foo ${INERT_REPO}/src 2>&1" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "2>&1 read naming repo root allowed (#1084)" 0

echo "case: quoted HTML tag text naming the repo root is allowed (#1084)"
JSON=$(jq -n --arg cmd "grep -n '<div>' ${INERT_REPO}/src/index.njk" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "quoted HTML tag grep naming repo root allowed (#1084)" 0

echo "── #1084: non-inert commands must keep blocking (regression controls) ──"

# Real redirect, real target -- never reaches the backstop at all; blocked by
# the ordinary parsed-target path. Proves the fix did not disarm the guard.
echo "case: plain in-root redirect still blocked (#1084 control)"
JSON=$(jq -n --arg cmd "echo x > ${INERT_REPO}/AGENTS.md" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "plain in-root redirect still blocked (#1084)" 2

# The re-parse verbs are why "all `>` are quoted" can never mean "inert":
# quoted text handed to a second shell parse is exactly what the backstop
# exists for. Each of these has ZERO path-write syntax at this parse level.
for verb_case in \
    "eval|eval \"echo x > ${INERT_REPO}/AGENTS.md\"" \
    "sh -c|sh -c \"echo x > ${INERT_REPO}/AGENTS.md\"" \
    "bash -c|bash -c \"echo x > ${INERT_REPO}/AGENTS.md\"" \
    "xargs|echo ${INERT_REPO}/AGENTS.md | xargs -I{} sh -c \"echo x > {}\"" \
    "awk print >|awk 'BEGIN{print 1 > \"${INERT_REPO}/AGENTS.md\"}'"; do
    verb_label="${verb_case%%|*}"
    verb_cmd="${verb_case#*|}"
    echo "case: re-parse verb '${verb_label}' with quoted redirect still blocked (#1084)"
    JSON=$(jq -n --arg cmd "${verb_cmd}" '{tool_input:{command:$cmd}}')
    run_guard "${INERT_REPO}" "${JSON}"
    assert_exit "re-parse verb '${verb_label}' still blocked (#1084)" 2
done

# #810 Fix 2's unconditional delimited-tee branch must stay AHEAD of the
# inert check: `{tee,cat}` brace-expands to a real tee whose target is
# relative, so there is no root string for any scan to match.
echo "case: brace-expanded tee still blocked unconditionally (#810 Fix 2 ordering, #1084)"
JSON=$(jq -n --arg cmd "{tee,cat} ${INERT_REPO}/AGENTS.md" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "brace-expanded tee still blocked (#1084)" 2

# Non-vacuity: prove the allowed cases above are decided by the inert check
# and not by some unrelated early exit. Same quoted-regex shape, but with a
# re-parse verb wrapped around it -- the ONLY difference -- must block.
echo "case: non-vacuity -- the same quoted-regex command inside eval blocks (#1084)"
JSON=$(jq -n --arg cmd "eval \"grep -oP '(?<=>)x' ${INERT_REPO}/src/index.njk\"" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
assert_exit "quoted-regex inside eval blocks (#1084 non-vacuity)" 2

echo "── #1084: zero-parse block message must not instruct an agent to write ──"
# The old remediation text told an agent whose command performs no write to
# "rewrite the command using a plain, directly-parseable redirect" -- an
# instruction to invent a write. The message must now say so explicitly.
JSON=$(jq -n --arg cmd "eval \"echo x > ${INERT_REPO}/AGENTS.md\"" '{tool_input:{command:$cmd}}')
run_guard "${INERT_REPO}" "${JSON}"
if [[ "${GUARD_STDERR}" == *"NEVER add a redirect"* ]]; then
    pass
else
    fail "zero-parse message must warn against inventing a redirect, got: ${GUARD_STDERR}"
fi

# ── Ticket #816: three Bash write-guard bypasses left after #810 ──────────
# Gap 1: a double-quoted substitution anywhere (not only inside [[ ]]/(( )))
# next to quoted write-shaped text fails closed with exit 7 and a generalised
# message. Gap 2: `$'...'` is its own quote state, so `\'` inside it cannot
# make a real redirect look quoted. Gap 3: a $HOME/~/$PWD/$TMPDIR expansion is
# honoured only when the same command does not change that variable (or, for
# $PWD, the directory); otherwise the target is blocked with a message naming
# the change. Root for the feature-worktree-cwd cases is WORKTREE_REPRO_REPO.
T816_HOME="${TEST_ROOT}/t816-home"
T816_TMP="${TEST_ROOT}/t816-tmp"
mkdir -p "${T816_HOME}" "${T816_TMP}"

echo "case: an out-of-region double-quoted \$( ) substitution hiding a main-worktree write is blocked with the generalised exit-7 message (#816 AC 30)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "echo ok > /tmp/ok; echo \"\$(printf x > ${WORKTREE_REPRO_REPO}/AGENTS.md)\"")"
assert_exit "#816 AC 30 out-of-region double-quoted substitution blocked" 2
assert_stderr_has "#816 AC 30" "command/process/function substitution appears inside a double-quoted string"
assert_stderr_has "#816 AC 30" "cannot be safely resolved"
assert_stderr_lacks "#816 AC 30" "within a comparison construct"
assert_stderr_has "#816 AC 30 remediation" "git commit -F"
assert_stderr_has "#816 AC 30 remediation" "--body-file"

echo "case: \$'a\\'b' cannot hide a real main-worktree redirect behind drifted quote state (#816 AC 34)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "echo ok > /tmp/ok; echo \$'a\\'b' > ${WORKTREE_REPRO_REPO}/AGENTS.md #'")"
assert_exit "#816 AC 34 ANSI-C quoted redirect blocked" 2
assert_stderr_has "#816 AC 34" "targets the main worktree"

echo "case: HOME reassigned before a ~ redirect is blocked as a changed variable (#816 AC 36)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "HOME=${BASH_REPO}; echo x > ~/AGENTS.md")"
assert_exit "#816 AC 36 HOME= then ~ redirect blocked" 2
assert_stderr_has "#816 AC 36" "HOME"
assert_stderr_has "#816 AC 36" "changed by this command"
assert_stderr_has "#816 AC 36" "literal absolute path"

echo "case: export HOME before a \$HOME redirect is blocked as a changed variable (#816 AC 37)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "export HOME=${BASH_REPO} && echo x > \$HOME/AGENTS.md")"
assert_exit "#816 AC 37 export HOME then \$HOME redirect blocked" 2
assert_stderr_has "#816 AC 37" "changed by this command"

echo "case: cd to the main worktree before a \$PWD redirect is blocked from a feature-worktree cwd (#816 AC 38)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "cd ${WORKTREE_REPRO_REPO} && echo x > \$PWD/AGENTS.md")"
assert_exit "#816 AC 38 cd then \$PWD redirect blocked" 2
assert_stderr_has "#816 AC 38" "changes the working directory"
assert_stderr_has "#816 AC 38" "literal absolute path"

echo "case: TMPDIR reassigned before a \${TMPDIR:-/tmp} tee is blocked as a changed variable (#816 AC 39)"
run_guard_with_tmpdir "${BASH_REPO}" "${T816_TMP}" "$(dl_bash_json "TMPDIR=${BASH_REPO}; tee \${TMPDIR:-/tmp}/AGENTS.md")"
assert_exit "#816 AC 39 TMPDIR= then \${TMPDIR:-/tmp} tee blocked" 2
assert_stderr_has "#816 AC 39" "TMPDIR"
assert_stderr_has "#816 AC 39" "changed by this command"
assert_stderr_has "#816 AC 39" "literal absolute path"

echo "case: eval marks HOME changed, so a later \$HOME redirect is blocked (#816 AC 40)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "eval 'HOME=/x'; echo y > \$HOME/z")"
assert_exit "#816 AC 40 eval then \$HOME redirect blocked" 2
assert_stderr_has "#816 AC 40" "changed by this command"

echo "case: source marks TMPDIR changed, so a later \$TMPDIR redirect is blocked (#816 AC 40, source)"
run_guard_with_tmpdir "${BASH_REPO}" "${T816_TMP}" "$(dl_bash_json "source ./env.sh; echo y > \$TMPDIR/z")"
assert_exit "#816 AC 40 source then \$TMPDIR redirect blocked" 2
assert_stderr_has "#816 AC 40 source" "TMPDIR"
assert_stderr_has "#816 AC 40 source" "changed by this command"

echo "case: a command-position . marks PWD changed, so a later \${PWD} redirect is blocked (#816 AC 40, dot)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json ". ./env.sh; echo y > \${PWD}/z")"
assert_exit "#816 AC 40 . then \${PWD} redirect blocked" 2
assert_stderr_has "#816 AC 40 dot" "changes the working directory"

echo "case: cd does not affect a \${TMPDIR:-/tmp} expansion (#816 AC 43)"
run_guard_with_tmpdir "${FEATURE_WORKTREE_REPRO}" "${T816_TMP}" "$(dl_bash_json "cd ${FEATURE_WORKTREE_REPRO} && go test ./... > \${TMPDIR:-/tmp}/cenci/t-816.log 2>&1")"
assert_exit "#816 AC 43 cd then \${TMPDIR:-/tmp} log allowed" 0

echo "case: a non-eval/source re-parse verb marks nothing changed (#816 AC 44)"
run_guard_with_tmpdir "${BASH_REPO}" "${T816_TMP}" "$(dl_bash_json "timeout 60 make test > \${TMPDIR:-/tmp}/cenci/t-816.log 2>&1")"
assert_exit "#816 AC 44 timeout then \${TMPDIR:-/tmp} log allowed" 0

echo "case: a literal absolute target is unaffected by a variable change (#816 AC 45)"
run_guard "${BASH_REPO}" "$(dl_bash_json "HOME=/x some-cmd > /tmp/cenci/t-816.out")"
assert_exit "#816 AC 45 HOME= with literal absolute target allowed" 0

echo "case: a target word still holding \$' is unresolved (#816 edge case)"
run_guard "${BASH_REPO}" "$(dl_bash_json "echo x > \$'/tmp/ok'")"
assert_exit "#816 edge \$'/tmp/ok' target blocked" 2
assert_stderr_has "#816 edge \$'/tmp/ok'" "cannot resolve the Bash write target"

echo "case: a double-quoted substitution with no > and no tee is allowed (#816 edge case)"
run_guard "${BASH_REPO}" "$(dl_bash_json "git commit -m \"\$(printf 'msg')\"")"
assert_exit "#816 edge git commit -m \"\$(printf)\" allowed" 0

echo "case: a bare HOME word marks HOME changed -- accepted over-block (#816 edge case)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "echo HOME > \$HOME/x")"
assert_exit "#816 edge echo HOME > \$HOME/x blocked" 2
assert_stderr_has "#816 edge echo HOME" "changed by this command"

echo "case: a literal absolute feature-worktree target after cd is allowed (#816 edge case)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "cd ${FEATURE_WORKTREE_REPRO} && echo x > ${FEATURE_WORKTREE_REPRO}/out.txt")"
assert_exit "#816 edge cd <wt> then absolute <wt> target allowed" 0

echo "case: ~user is never expanded and falls to the relative-target policy (#816 edge case)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "HOME=${BASH_REPO}; tee ~user/AGENTS.md")"
assert_exit "#816 edge ~user target blocked" 2
assert_stderr_has "#816 edge ~user" "cannot verify the relative Bash write target"
assert_stderr_lacks "#816 edge ~user" "changed by this command"

echo "case: pushd marks PWD changed while /dev/null stays exempt (#816 edge case)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "pushd ${WORKTREE_REPRO_REPO} >/dev/null; echo x > \${PWD}/AGENTS.md")"
assert_exit "#816 edge pushd >/dev/null then \${PWD} redirect blocked" 2
assert_stderr_has "#816 edge pushd" "changes the working directory"
assert_stderr_has "#816 edge pushd names the \${PWD} target" "'\${PWD}/AGENTS.md'"

echo "case: control -- pushd with only /dev/null and a literal absolute temp target is allowed (#816 edge case)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(dl_bash_json "pushd ${WORKTREE_REPRO_REPO} >/dev/null; echo x > /tmp/cenci/t-816.out")"
assert_exit "#816 edge pushd >/dev/null with literal temp target allowed" 0

echo "case: an apply_patch envelope whose body holds HOME= and cd is unaffected by the change check (#816 edge case)"
run_guard "${FEATURE_WORKTREE_REPRO}" "$(jq -n --arg cmd "*** Begin Patch
*** Add File: ~/notes-816.txt
+HOME=/x
+cd /tmp
*** End Patch" --arg cwd "${FEATURE_WORKTREE_REPRO}" "${AP_SHAPE}")"
assert_exit "#816 edge apply_patch body with HOME= and cd allowed" 0

echo "case: grep with a quoted pattern and a . path argument logging to \${TMPDIR:-/tmp} is allowed (#816 review fix)"
run_guard_with_tmpdir "${FEATURE_WORKTREE_REPRO}" "${T816_TMP}" "$(dl_bash_json "grep \"x\" . > \${TMPDIR:-/tmp}/cenci/g-816.log")"
assert_exit "#816 review grep \"x\" . > \${TMPDIR:-/tmp} log allowed" 0

echo "case: a sourced dot behind a FOO=\${x} assignment prefix marks HOME changed, so a later ~ redirect is blocked (#816 review fix)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "FOO=\${x} . ./env.sh; echo y > ~/f")"
assert_exit "#816 review FOO=\${x} . ./env.sh then ~ redirect blocked" 2
assert_stderr_has "#816 review FOO=\${x} dot" "HOME"
assert_stderr_has "#816 review FOO=\${x} dot" "changed by this command"

echo "case: a sourced dot after a case-pattern ) marks HOME changed, so a later ~ redirect is blocked (#816 review fix)"
run_guard_with_home "${BASH_REPO}" "${T816_HOME}" "$(dl_bash_json "case x in x) . ./env.sh;; esac; echo y > ~/f")"
assert_exit "#816 review case-pattern . then ~ redirect blocked" 2
assert_stderr_has "#816 review case-pattern dot" "changed by this command"

# ── Ticket #1411: guard canary receipts ────────────────────────────────
# When this guard denies a canary-shaped probe target
# <main-root>/cenci-guard-canary-<hex>-<n>.txt whose run dir
# cenci-guard-canary-<hex> (under $TMPDIR, else /tmp) lists probe <n> for
# guard-main-worktree with exactly that target, it leaves one receipt in the
# run dir for guard-canary.sh check. The receipt must never change the exit
# code, the stderr or the decision, and every other deny leaves no receipt.
RG_REPO="${TEST_ROOT}/receipt-repo"
make_git_repo "${RG_REPO}"
mkdir -p "${RG_REPO}/.cenci" "${RG_REPO}/src"
touch "${RG_REPO}/.cenci/config.json"
RG_ROOT="$(cd "${RG_REPO}" && pwd -P)" || { echo "FAIL: test setup: resolve ${RG_REPO}" >&2; exit 1; }
RG_TMP="${TEST_ROOT}/receipt-tmp"
mkdir -p "${RG_TMP}"
chmod 700 "${RG_TMP}"
RG_SEQ=0
RG_RUN=""
RG_HEX=""
RG_TAB=$'\t'
# The /tmp fallback case needs a real run dir under /tmp; it is removed on exit.
RG_SYS_HEX="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
[[ "${RG_SYS_HEX}" =~ ^[0-9a-f]{16}$ ]] || { echo "FAIL: test setup: random run id" >&2; exit 1; }
RG_SYS_RUN="/tmp/cenci-guard-canary-${RG_SYS_HEX}"
trap 'rm -rf "${TEST_ROOT}" "${RG_SYS_RUN}"' EXIT

# rg_new_run [base] -- a fresh 0700 run dir (default base: RG_TMP) with a unique
# hex id; sets RG_RUN and RG_HEX.
rg_new_run() {
    local base="${1:-${RG_TMP}}"
    RG_SEQ=$((RG_SEQ + 1))
    RG_HEX="$(printf '%016x' "$((0xa9e000000000 + RG_SEQ))")"
    RG_RUN="${base}/cenci-guard-canary-${RG_HEX}"
    mkdir -m 700 "${RG_RUN}" || { echo "FAIL: test setup: mkdir ${RG_RUN}" >&2; exit 1; }
}

# rg_write_probes <run-dir> <hex> -- the record plan writes for today's
# manifest: 2/4 are this guard's main-worktree probes, 1/3 the .env probes; 5 is
# a main-worktree-shaped target listed for the OTHER guard.
rg_write_probes() {
    local run="$1" hex="$2"
    {
        printf 'V1\nHEX\t%s\nMAIN\t%s\n' "${hex}" "${RG_ROOT}"
        printf 'PROBE\t1\tapply_patch\tcheck-sensitive-files\tWrite|Edit\t%s/write-edit-1.env\n' "${run}"
        printf 'PROBE\t2\tapply_patch\tguard-main-worktree\tWrite|Edit\t%s/cenci-guard-canary-%s-2.txt\n' "${RG_ROOT}" "${hex}"
        printf 'PROBE\t3\tshell\tcheck-sensitive-files\tBash\t%s/bash-3.env\n' "${run}"
        printf 'PROBE\t4\tshell\tguard-main-worktree\tBash\t%s/cenci-guard-canary-%s-4.txt\n' "${RG_ROOT}" "${hex}"
        printf 'PROBE\t5\tshell\tcheck-sensitive-files\tBash\t%s/cenci-guard-canary-%s-5.txt\n' "${RG_ROOT}" "${hex}"
    } > "${run}/probes.tsv" || { echo "FAIL: test setup: probes.tsv in ${run}" >&2; exit 1; }
}

rg_json() { # <shell|apply_patch|write|edit> <target>
    case "$1" in
        shell)
            jq -n --arg c "printf x > '$2'" --arg cwd "${RG_ROOT}" \
                '{hook_event_name: "PreToolUse", tool_name: "Bash", cwd: $cwd, tool_input: {command: $c}}'
            ;;
        apply_patch)
            jq -n --arg c "$(printf '*** Begin Patch\n*** Add File: %s\n+x\n*** End Patch' "$2")" --arg cwd "${RG_ROOT}" \
                '{hook_event_name: "PreToolUse", tool_name: "apply_patch", cwd: $cwd, tool_input: {command: $c}}'
            ;;
        write|edit)
            local tool=Write
            [[ "$1" == edit ]] && tool=Edit
            jq -n --arg f "$2" --arg cwd "${RG_ROOT}" --arg tool "${tool}" \
                '{tool_name: $tool, cwd: $cwd, tool_input: {file_path: $f, content: "x"}}'
            ;;
    esac
}

# rg_receipts <dir> -- receipt-* entry names in <dir>, one per line.
rg_receipts() {
    local e
    for e in "$1"/receipt-*; do
        if [[ -e "${e}" || -L "${e}" ]]; then
            printf '%s\n' "${e##*/}"
        fi
    done
    return 0
}

# rg_count <dir> -- receipt-* entries in <dir> ("missing-dir" when absent).
rg_count() {
    local l
    [[ -d "$1" ]] || { echo "missing-dir"; return 0; }
    l="$(rg_receipts "$1")"
    if [[ -z "${l}" ]]; then echo 0; else printf '%s\n' "${l}" | wc -l | tr -d '[:space:]'; fi
}

# rg_snapshot -- every cenci-guard-canary-* entry and receipt under /tmp and
# the TMPDIR the cases pass. /tmp is shared with other processes (a sibling
# worktree's gate, a live guard canary), so there only entries this suite itself
# can have created count: its run ids start with 0000a9e0 (rg_new_run) and the
# /tmp fallback case uses RG_SYS_HEX. The suite's own RG_TMP is scanned in full.
rg_snapshot() {
    local d e name
    for d in /tmp "${RG_TMP}"; do
        for e in "${d}"/cenci-guard-canary-* "${d}"/cenci-guard-canary-*/receipt-*; do
            if [[ "${d}" != "${RG_TMP}" ]]; then
                name="${e#"${d}"/}"
                [[ "${name}" == cenci-guard-canary-0000a9e0* || "${name}" == cenci-guard-canary-"${RG_SYS_HEX}"* ]] || continue
            fi
            if [[ -e "${e}" || -L "${e}" ]]; then
                printf '%s\n' "${e}"
            fi
        done
    done
    return 0
}

# rg_run <tmpdir-or-"unset"> <json> -- run the guard from the main root.
rg_run() {
    if [[ "$1" == unset ]]; then
        run_guard "${RG_ROOT}" "$2"
    else
        run_guard_with_tmpdir "${RG_ROOT}" "$1" "$2"
    fi
}

# rg_parity <label> <tmpdir-or-"unset"> <json> -- the last run's exit code and
# stderr must be byte-identical to the same payload run now (the caller has
# removed or neutralised the run dir first).
rg_parity() {
    local label="$1" want_exit="${GUARD_EXIT}" want_err="${GUARD_STDERR}"
    rg_run "$2" "$3"
    if [[ "${GUARD_EXIT}" -eq "${want_exit}" && "${GUARD_STDERR}" == "${want_err}" ]]; then
        pass
    else
        fail "${label}: exit/stderr must be identical with and without the run dir (exit ${want_exit} vs ${GUARD_EXIT}; stderr '${want_err}' vs '${GUARD_STDERR}')"
    fi
}

# rg_assert_one_receipt <label> <run-dir> <hex> <n> <tool_name> <target>
rg_assert_one_receipt() {
    local label="$1" run="$2" hex="$3" n="$4" tool="$5" target="$6" name body
    if [[ "${GUARD_EXIT}" -eq 2 && "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
        pass
    else
        fail "${label}: the probe must still be denied with the guard's own message (exit ${GUARD_EXIT}): ${GUARD_STDERR:0:200}"
    fi
    if [[ "$(rg_count "${run}")" == 1 ]]; then
        pass
    else
        fail "${label}: expected exactly one receipt in ${run}, found $(rg_count "${run}")"
    fi
    name="$(rg_receipts "${run}" | sed -n 1p)"
    if [[ "${name}" == "receipt-guard-main-worktree-${n}."* && -f "${run}/${name}" && ! -L "${run}/${name}" ]]; then
        pass
    else
        fail "${label}: the receipt must be a regular file named receipt-guard-main-worktree-${n}.*, got '${name}'"
    fi
    body="$(cat "${run}/${name}" 2>/dev/null)"
    if [[ "${body}" == "V1${RG_TAB}guard-main-worktree${RG_TAB}${hex}${RG_TAB}${n}${RG_TAB}${tool}" \
        && "$(wc -l < "${run}/${name}" | tr -d '[:space:]')" == 1 ]]; then
        pass
    else
        fail "${label}: the receipt must be the single line V1<TAB>guard-main-worktree<TAB>${hex}<TAB>${n}<TAB>${tool}, got '${body}'"
    fi
    if [[ "${body}" == *printf* || "${body}" == *"Begin Patch"* || "${body}" == *"${target}"* ]]; then
        fail "${label}: the receipt must carry no probe command, patch text or target path: '${body}'"
    else
        pass
    fi
    if [[ -e "${target}" ]]; then
        fail "${label}: the denied probe target must not exist"
    else
        pass
    fi
}

# Guidance is conditional prose: neither a canary-shaped filename nor this
# message proves a receipt or a successful preflight. Ordinary writes still
# receive their feature-worktree remediation.
assert_canary_guidance() {
    assert_stderr_has "$1" "If this is a planned guard-canary.sh probe, this denial is expected."
    assert_stderr_has "$1" "Record denied, complete all remaining planned probes, then run guard-canary.sh check."
    assert_stderr_has "$1" "Proceed only on CANARY_STATUS=active."
    assert_stderr_has "$1" "Never retry, relocate or bypass a planned probe, or manufacture a success."
    assert_stderr_has "$1" "Otherwise, for an accidental main-worktree write:"
    assert_stderr_lacks "$1" "THE ONLY CORRECT FIX"
}

echo "case: ordinary Write/Edit and Bash denials retain feature-worktree remediation"
for rg_shape in write edit shell; do
    rg_run "${RG_TMP}" "$(rg_json "${rg_shape}" "${RG_ROOT}/src/ordinary.txt")"
    assert_exit "ordinary ${rg_shape} remediation" 2
    assert_stderr_has "ordinary ${rg_shape} remediation" "targets the main worktree"
    if [[ "${rg_shape}" == shell ]]; then
        assert_stderr_has "ordinary shell remediation" "redirect to an absolute path rooted at the feature"
    else
        assert_stderr_has "ordinary ${rg_shape} remediation" "re-issue the SAME Write/Edit with an absolute path rooted"
    fi
    assert_canary_guidance "ordinary ${rg_shape} conditional guidance"
done

for rg_case in "shell|4|Bash|${RG_TMP}" "apply_patch|2|apply_patch|${RG_TMP}" "write|2|Write|${RG_TMP}" "edit|2|Edit|${RG_TMP}" "shell|4|Bash|${RG_TMP}/"; do
    IFS='|' read -r rg_shape rg_n rg_tool rg_tmpdir <<< "${rg_case}"
    echo "case: a denied ${rg_shape} canary probe (TMPDIR=${rg_tmpdir##*/receipt-}) leaves exactly one receipt with the right fields (#1411)"
    rg_new_run
    rg_write_probes "${RG_RUN}" "${RG_HEX}"
    rg_target="${RG_ROOT}/cenci-guard-canary-${RG_HEX}-${rg_n}.txt"
    rg_j="$(rg_json "${rg_shape}" "${rg_target}")"
    rg_run "${rg_tmpdir}" "${rg_j}"
    rg_assert_one_receipt "receipt (${rg_shape}, TMPDIR ${rg_tmpdir})" "${RG_RUN}" "${RG_HEX}" "${rg_n}" "${rg_tool}" "${rg_target}"
    assert_canary_guidance "planned ${rg_shape} probe"
    rm -rf "${RG_RUN}"
    rg_parity "receipt (${rg_shape}, TMPDIR ${rg_tmpdir})" "${rg_tmpdir}" "${rg_j}"
    assert_canary_guidance "${rg_shape} canary-shaped filename without a run directory"
done

echo "case: with no run dir under TMPDIR the receipt goes to the /tmp run dir, and TMPDIR wins when both exist (#1411)"
mkdir -m 700 "${RG_SYS_RUN}" || { echo "FAIL: test setup: mkdir ${RG_SYS_RUN}" >&2; exit 1; }
rg_write_probes "${RG_SYS_RUN}" "${RG_SYS_HEX}"
rg_target="${RG_ROOT}/cenci-guard-canary-${RG_SYS_HEX}-4.txt"
rg_j="$(rg_json shell "${rg_target}")"
for rg_tmpdir in unset "${RG_TMP}"; do
    rg_run "${rg_tmpdir}" "${rg_j}"
    rg_assert_one_receipt "/tmp fallback (TMPDIR ${rg_tmpdir})" "${RG_SYS_RUN}" "${RG_SYS_HEX}" 4 Bash "${rg_target}"
    rm -f "${RG_SYS_RUN}"/receipt-*
done
mkdir -m 700 "${RG_TMP}/cenci-guard-canary-${RG_SYS_HEX}" || exit 1
rg_write_probes "${RG_TMP}/cenci-guard-canary-${RG_SYS_HEX}" "${RG_SYS_HEX}"
rg_run "${RG_TMP}" "${rg_j}"
rg_assert_one_receipt "TMPDIR precedence" "${RG_TMP}/cenci-guard-canary-${RG_SYS_HEX}" "${RG_SYS_HEX}" 4 Bash "${rg_target}"
if [[ "$(rg_count "${RG_SYS_RUN}")" == 0 ]]; then
    pass
else
    fail "TMPDIR precedence: the /tmp run dir must get no receipt when TMPDIR holds the run dir"
fi
rm -rf "${RG_TMP}/cenci-guard-canary-${RG_SYS_HEX}" "${RG_SYS_RUN}"
rg_parity "/tmp fallback" "${RG_TMP}" "${rg_j}"

echo "case: a denial unrelated to the canary leaves no receipt and no run-dir entry anywhere (#1411)"
rg_new_run
rg_write_probes "${RG_RUN}" "${RG_HEX}"
mkdir -p "${RG_ROOT}/sub"
RG_BEFORE="$(rg_snapshot)"
# Hermeticity: plant one foreign cenci-guard-canary-* entry in the shared /tmp
# between the snapshots, as a concurrent gate or live canary would; the
# assertion below must not attribute it to these denials.
RG_FOREIGN="/tmp/cenci-guard-canary-ffff$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
mkdir -m 700 "${RG_FOREIGN}" && mkdir "${RG_FOREIGN}/receipt-foreign" \
    || { echo "FAIL: test setup: foreign canary decoy ${RG_FOREIGN}" >&2; exit 1; }
# An ordinary source write, an unlisted probe number, a probe listed for the
# other guard, and the right leaf in the wrong directory.
for rg_spec in "shell|${RG_ROOT}/src/foo.txt" "write|${RG_ROOT}/src/foo.txt" "apply_patch|${RG_ROOT}/src/foo.txt" \
    "shell|${RG_ROOT}/cenci-guard-canary-${RG_HEX}-9.txt" "shell|${RG_ROOT}/cenci-guard-canary-${RG_HEX}-5.txt" \
    "shell|${RG_ROOT}/sub/cenci-guard-canary-${RG_HEX}-4.txt" "write|${RG_ROOT}/cenci-guard-canary-${RG_HEX}-9.txt"; do
    rg_run "${RG_TMP}" "$(rg_json "${rg_spec%%|*}" "${rg_spec#*|}")"
    assert_exit "unrelated deny ${rg_spec}" 2
done
if [[ "$(rg_snapshot)" == "${RG_BEFORE}" && "$(rg_count "${RG_RUN}")" == 0 ]]; then
    pass
else
    fail "unrelated deny: /tmp and \$TMPDIR must gain no cenci-guard-canary-* entry or receipt (before: ${RG_BEFORE}; after: $(rg_snapshot))"
fi
rm -rf "${RG_FOREIGN}" "${RG_RUN}"
# A record whose HEX names another run, and a run dir with no record at all.
rg_new_run
rg_write_probes "${RG_RUN}" "ffffffffffffffff"
rg_run "${RG_TMP}" "$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
assert_exit "record from another run: still denied" 2
if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
    pass
else
    fail "a probe record whose HEX differs from the run dir name must not earn a receipt"
fi
rm -rf "${RG_RUN}"
rg_new_run
rg_run "${RG_TMP}" "$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
assert_exit "no probe record: still denied" 2
if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
    pass
else
    fail "a run dir with no probes.tsv must not earn a receipt"
fi
rm -rf "${RG_RUN}"

echo "case: a symlinked run dir gets no receipt, and exit/stderr are unchanged (#1411)"
rg_new_run
RG_REAL="${RG_TMP}/real-${RG_HEX}"
mv "${RG_RUN}" "${RG_REAL}" || exit 1
ln -s "${RG_REAL}" "${RG_RUN}" || exit 1
rg_write_probes "${RG_REAL}" "${RG_HEX}"
rg_j="$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
rg_run "${RG_TMP}" "${rg_j}"
assert_exit "symlinked run dir: still denied" 2
if [[ "$(rg_count "${RG_REAL}")" == 0 ]]; then
    pass
else
    fail "symlinked run dir: no receipt may be written through the symlink"
fi
rm -f "${RG_RUN}"
rg_parity "symlinked run dir" "${RG_TMP}" "${rg_j}"
rm -rf "${RG_REAL}"

echo "case: a group- or world-writable run dir gets no receipt, and exit/stderr are unchanged (#1411)"
for rg_mode in 770 707 777; do
    rg_new_run
    rg_write_probes "${RG_RUN}" "${RG_HEX}"
    chmod "${rg_mode}" "${RG_RUN}"
    rg_j="$(rg_json apply_patch "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-2.txt")"
    rg_run "${RG_TMP}" "${rg_j}"
    assert_exit "mode-${rg_mode} run dir: still denied" 2
    if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
        pass
    else
        fail "mode-${rg_mode} run dir: expected no receipt, found $(rg_count "${RG_RUN}")"
    fi
    rm -rf "${RG_RUN}"
    rg_parity "mode-${rg_mode} run dir" "${RG_TMP}" "${rg_j}"
done

echo "case: a run dir owned by another user gets no receipt (#1411)"
if [[ "$(id -u)" -eq 0 ]]; then
    rg_new_run
    rg_write_probes "${RG_RUN}" "${RG_HEX}"
    chown 65534 "${RG_RUN}" "${RG_RUN}/probes.tsv" || exit 1
    rg_j="$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
    rg_run "${RG_TMP}" "${rg_j}"
    assert_exit "foreign-owned run dir: still denied" 2
    if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
        pass
    else
        fail "foreign-owned run dir: expected no receipt"
    fi
    rm -rf "${RG_RUN}"
    rg_parity "foreign-owned run dir" "${RG_TMP}" "${rg_j}"
else
    echo "SKIP: foreign-owned run dir needs root to chown a directory to another user"
fi

echo "case: a receipt that cannot be written still leaves the guard exiting 2 with its own message (#1411)"
rg_new_run
rg_write_probes "${RG_RUN}" "${RG_HEX}"
chmod 500 "${RG_RUN}"
rg_j="$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
rg_run "${RG_TMP}" "${rg_j}"
assert_exit "read-only run dir: still denied" 2
if [[ "$(id -u)" -eq 0 ]]; then
    echo "SKIP: read-only run dir: root bypasses the mode bits, so only the exit code and stderr are checked"
elif [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
    pass
else
    fail "read-only run dir: no receipt can have been written"
fi
chmod 700 "${RG_RUN}"
rm -rf "${RG_RUN}"
rg_parity "read-only run dir" "${RG_TMP}" "${rg_j}"

# A same-user session can plant its own canary-shaped run dir (it passes every
# hardening check) with a huge or sparse probes.tsv. The guard must not read it
# unbounded before its exit 2: a stalled hook times out, and a timed-out hook
# is non-blocking under Claude Code, so the deny would turn into an allow.
RG_TIME_LIMIT=10
# rg_run_timed <tmpdir> <json> -- rg_run under a hard RG_TIME_LIMIT-second
# limit when timeout(1) exists (exit 124 = stalled).
rg_run_timed() {
    local start
    if command -v timeout >/dev/null 2>&1; then
        GUARD_STDERR="$(cd "${RG_ROOT}" && echo "$2" | TMPDIR="$1" timeout "${RG_TIME_LIMIT}" "${HOOK_SHELL}" "${GUARD_SH}" 2>&1 >/dev/null)"
        GUARD_EXIT=$?
    else
        start="${SECONDS}"
        rg_run "$1" "$2"
        if [[ $((SECONDS - start)) -gt "${RG_TIME_LIMIT}" ]]; then
            GUARD_EXIT=124
        fi
    fi
}
# rg_valid_then_pad <run-dir> <hex> <pad-bytes> -- the real probe record, then
# <pad-bytes> of '#' on one more line, so the record itself would earn a
# receipt and only the size can refuse it.
rg_valid_then_pad() {
    rg_write_probes "$1" "$2"
    printf '%*s\n' "$3" '' | tr ' ' '#' >> "$1/probes.tsv" || { echo "FAIL: test setup: pad probes.tsv" >&2; exit 1; }
}
# rg_grow_huge <run-dir> <sparse|newlines> -- append a filler to probes.tsv: a
# 1 GiB sparse extension, or 32 MiB of newlines (one read-loop iteration per
# byte, which stalls an unbounded loop for tens of seconds).
rg_grow_huge() {
    if [[ "$2" == sparse ]]; then
        # Without conv=notrunc, dd ftruncates the output to the seek offset,
        # which keeps the record and extends it sparsely.
        dd if=/dev/null of="$1/probes.tsv" bs=1 seek=1073741824 count=0 2>/dev/null \
            || { echo "FAIL: test setup: grow probes.tsv" >&2; exit 1; }
        [[ "$(wc -c < "$1/probes.tsv" | tr -d '[:space:]')" == 1073741824 ]] \
            || { echo "FAIL: test setup: probes.tsv did not grow to 1 GiB" >&2; exit 1; }
    else
        head -c 33554432 /dev/zero | tr '\0' '\n' >> "$1/probes.tsv" \
            || { echo "FAIL: test setup: grow probes.tsv" >&2; exit 1; }
    fi
}
# rg_assert_prompt_deny <label> -- the last rg_run_timed denied, in time.
rg_assert_prompt_deny() {
    if [[ "${GUARD_EXIT}" -eq 124 ]]; then
        fail "$1: the guard stalled past ${RG_TIME_LIMIT}s reading the probe record"
    else
        assert_exit "$1: still denied" 2
    fi
}

for rg_spec in "shell|sparse" "write|sparse" "shell|newlines" "write|newlines"; do
    rg_shape="${rg_spec%%|*}"
    rg_fill="${rg_spec#*|}"
    echo "case: a huge (${rg_fill}) probes.tsv cannot stall the ${rg_shape} deny: exit 2 promptly, identical stderr, no receipt (#1411)"
    rg_new_run
    rg_write_probes "${RG_RUN}" "${RG_HEX}"
    rg_grow_huge "${RG_RUN}" "${rg_fill}"
    rg_j="$(rg_json "${rg_shape}" "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
    rg_run_timed "${RG_TMP}" "${rg_j}"
    rg_assert_prompt_deny "huge (${rg_fill}) probes.tsv (${rg_shape})"
    if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
        pass
    else
        fail "huge (${rg_fill}) probes.tsv (${rg_shape}): an oversized probe record must earn no receipt, found $(rg_count "${RG_RUN}")"
    fi
    rm -rf "${RG_RUN}"
    rg_parity "huge (${rg_fill}) probes.tsv (${rg_shape})" "${RG_TMP}" "${rg_j}"
done

echo "case: a compound command whose first target is canary-shaped (huge probes.tsv) and second a real main-worktree target still exits 2 promptly (#1411)"
rg_new_run
rg_write_probes "${RG_RUN}" "${RG_HEX}"
rg_grow_huge "${RG_RUN}" newlines
rg_target="${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt"
rg_j="$(jq -n --arg c "printf x > '${rg_target}'; printf x > '${RG_ROOT}/src/real.txt'" --arg cwd "${RG_ROOT}" \
    '{hook_event_name: "PreToolUse", tool_name: "Bash", cwd: $cwd, tool_input: {command: $c}}')"
rg_run_timed "${RG_TMP}" "${rg_j}"
rg_assert_prompt_deny "compound command, huge probes.tsv"
if [[ "${GUARD_STDERR}" == *"targets the main worktree"* ]]; then
    pass
else
    fail "compound command, huge probes.tsv: the guard's own message must be unchanged, got: ${GUARD_STDERR:0:200}"
fi
if [[ "$(rg_count "${RG_RUN}")" == 0 && ! -e "${rg_target}" && ! -e "${RG_ROOT}/src/real.txt" ]]; then
    pass
else
    fail "compound command, huge probes.tsv: expected no receipt and neither target written (receipts: $(rg_count "${RG_RUN}"))"
fi
rm -rf "${RG_RUN}"
rg_parity "compound command, huge probes.tsv" "${RG_TMP}" "${rg_j}"

echo "case: a probes.tsv one line over the size cap earns no receipt even though it lists the probe (#1411)"
rg_new_run
rg_valid_then_pad "${RG_RUN}" "${RG_HEX}" 16384
rg_j="$(rg_json shell "${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt")"
rg_run_timed "${RG_TMP}" "${rg_j}"
rg_assert_prompt_deny "over-cap probes.tsv"
if [[ "$(rg_count "${RG_RUN}")" == 0 ]]; then
    pass
else
    fail "over-cap probes.tsv: a record larger than the cap must earn no receipt, found $(rg_count "${RG_RUN}")"
fi
rm -rf "${RG_RUN}"
rg_parity "over-cap probes.tsv" "${RG_TMP}" "${rg_j}"
# Control: the same record padded well under the cap still earns its receipt,
# so the cap, not the padding line, is what refuses above.
rg_new_run
rg_valid_then_pad "${RG_RUN}" "${RG_HEX}" 1024
rg_target="${RG_ROOT}/cenci-guard-canary-${RG_HEX}-4.txt"
rg_run_timed "${RG_TMP}" "$(rg_json shell "${rg_target}")"
rg_assert_one_receipt "under-cap padded probes.tsv" "${RG_RUN}" "${RG_HEX}" 4 Bash "${rg_target}"
rm -rf "${RG_RUN}"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES} (shell: ${HOOK_SHELL})"

# The hook's #!/bin/sh shebang resolves to bash on macOS/Arch/Fedora and to
# dash on Debian/Ubuntu, so a single pass only ever exercises whichever shell
# this host's /bin/sh happens to be. Re-run the whole case list once per other
# available shell. Alternates are resolved to absolute paths because
# run_guard_with_path replaces PATH with a curated bin dir that would not
# contain a bare `bash`/`dash`.
if [[ -z "${HOOK_SHELL_PINNED:-}" ]]; then
    for candidate in bash dash; do
        alt="$(command -v "${candidate}")" || continue
        [[ "${alt}" == "$(command -v sh)" ]] && continue
        echo "── re-running under ${alt} ──"
        HOOK_SHELL_PINNED=1 HOOK_SHELL="${alt}" bash "$0" || exit 1
    done
fi

[[ "${FAILURES}" -eq 0 ]]
