#!/usr/bin/env bash
# Tests for check-sensitive-files.sh. Mirrors the harness style of
# guard-main-worktree.test.sh: plain bash, no framework, PASS/FAIL counters,
# non-zero exit on failure. Each case drives the hook script with
# PreToolUse-style JSON on stdin. Unlike guard-main-worktree.sh, this hook
# does not consult cwd/git state — it only reads tool_input.file_path (or
# filePath) from stdin, so no per-case cwd/repo setup is needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check-sensitive-files.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

# run_check <json> — runs the hook with <json> on stdin.
# Sets CHECK_EXIT and CHECK_STDERR.
run_check() {
    local json="$1"
    CHECK_STDERR="$(echo "${json}" | sh "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

# run_check_with_path <path_override> <json> — like run_check, but overrides
# PATH with a curated bin dir before invoking the hook. Used to simulate a
# PATH missing specific external tools (fail-closed tests).
run_check_with_path() {
    local path_override="$1" json="$2"
    CHECK_STDERR="$(echo "${json}" | PATH="${path_override}" sh "${CHECK_SH}" 2>&1 >/dev/null)"
    CHECK_EXIT=$?
}

assert_exit() {
    local label="$1" expected="$2"
    if [[ "${CHECK_EXIT}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: exit ${CHECK_EXIT}, expected ${expected}"
    fi
}

assert_blocked_stderr() {
    local label="$1"
    if [[ "${CHECK_STDERR}" == *BLOCKED* ]]; then
        pass
    else
        fail "${label}: stderr should contain BLOCKED"
    fi
}

# make_curated_bin <dir> <tool>... — creates <dir> containing symlinks to the
# real binaries for each named tool (resolved from the current PATH) and
# nothing else. Used with run_check_with_path to simulate a PATH that is
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

echo "check-sensitive-files.test.sh"

TEST_ROOT="$(mktemp -d /var/tmp/check-sensitive-files-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# ── Regression: env files (category 1 of today's blocklist) ─────────
echo "case: .env-family paths are blocked (regression)"
for p in "${TEST_ROOT}/.env" "${TEST_ROOT}/config/.env" "${TEST_ROOT}/app.env.local"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "env file (${p})" 2
    assert_blocked_stderr "env file (${p})"
done

# ── Regression: credentials/secrets/keys (category 2) ────────────────
echo "case: credentials/secrets/key paths are blocked (regression)"
for p in "${TEST_ROOT}/credentials.json" "${TEST_ROOT}/app-secrets.yaml" "${TEST_ROOT}/server.pem" "${TEST_ROOT}/app.key"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "credential/key file (${p})" 2
    assert_blocked_stderr "credential/key file (${p})"
done

# ── Regression: SSH/keystore files (category 3) ──────────────────────
echo "case: SSH/keystore paths are blocked (regression)"
for p in "${TEST_ROOT}/.ssh/id_rsa" "${TEST_ROOT}/app.keystore" "${TEST_ROOT}/app.jks"; do
    run_check "{\"tool_input\":{\"file_path\":\"${p}\"}}"
    assert_exit "ssh/keystore file (${p})" 2
    assert_blocked_stderr "ssh/keystore file (${p})"
done

# ── Non-sensitive absolute path → allowed ────────────────────────────
echo "case: non-sensitive absolute path is allowed"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "non-sensitive path" 0

# ── Empty/missing file_path → no-op ──────────────────────────────────
echo "case: missing file_path is a no-op"
run_check "{\"tool_input\":{}}"
assert_exit "missing file_path" 0

echo "case: empty file_path is a no-op"
run_check "{\"tool_input\":{\"file_path\":\"\"}}"
assert_exit "empty file_path" 0

# ── Symlink hardening: benign name → sensitive canonical target ─────
# notes.txt (benign raw name) symlinks to a real, existing .env file. Only
# a canonicalizing implementation resolves the symlink and blocks via the
# canonical target; today's raw-name grep/sed sees only the literal string
# "notes.txt" and allows it. The target must be a real existing file — a
# broken symlink would fall into an ancestor-walk branch instead of having
# its final component dereferenced.
echo "case: symlink with benign name resolving to a sensitive canonical target is blocked"
mkdir -p "${TEST_ROOT}/symlinks"
touch "${TEST_ROOT}/symlinks/real.env"
ln -s "${TEST_ROOT}/symlinks/real.env" "${TEST_ROOT}/symlinks/notes.txt"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/symlinks/notes.txt\"}}"
assert_exit "symlink benign-name -> sensitive target" 2
assert_blocked_stderr "symlink benign-name -> sensitive target"

# ── Symlink hardening: sensitive name → benign canonical target ─────
# .env (sensitive raw name) symlinks to a real, existing benign file. Today's
# raw-name match already blocks this via the literal ".env" name; a hardened
# canonicalizing implementation must preserve this via a union of raw-name
# and canonical-name checks (canonicalization must not replace raw matching).
echo "case: symlink with sensitive name resolving to a benign canonical target is still blocked"
touch "${TEST_ROOT}/symlinks/benign.txt"
ln -s "${TEST_ROOT}/symlinks/benign.txt" "${TEST_ROOT}/symlinks/.env"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/symlinks/.env\"}}"
assert_exit "symlink sensitive-name -> benign target" 2
assert_blocked_stderr "symlink sensitive-name -> benign target"

# ── ..-collapse hardening ────────────────────────────────────────────
# The raw path's final literal component is "..", which matches no blocklist
# pattern; only after lexically collapsing ".." does the path resolve to a
# real, existing ".env" directory. Today's script has no normalization step,
# so it allows this write; a hardened implementation must collapse ".."
# (and/or canonicalize) before matching. NOTE: a simpler "*/.env"-suffixed
# ..-substring like "/a/b/../.env" is NOT usable here — its raw string still
# ends in the literal characters ".env", so today's suffix-glob already
# matches it without any normalization (verified empirically); it would not
# exercise this requirement.
echo "case: a ..-collapsed path resolving to a sensitive filename is blocked"
mkdir -p "${TEST_ROOT}/dotdot/.env"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/dotdot/.env/sub/..\"}}"
assert_exit "dot-dot collapse to sensitive path" 2
assert_blocked_stderr "dot-dot collapse to sensitive path"

# ── jq missing from PATH must fail closed ────────────────────────────
# Today's script never calls jq (it uses grep/sed), so a curated PATH
# lacking jq still runs the raw-text extraction successfully and allows this
# non-sensitive path. A hardened implementation depends on jq for field-
# scoped extraction and must fail closed (block) when jq is unavailable
# rather than silently falling through to an allow.
echo "case: jq missing from PATH fails closed"
JQ_MISSING_BIN="${TEST_ROOT}/bin-no-jq"
make_curated_bin "${JQ_MISSING_BIN}" sh cat grep sed realpath readlink
run_check_with_path "${JQ_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "jq missing fails closed" 2
assert_blocked_stderr "jq missing fails closed"

# ── Malformed JSON on stdin must fail closed ─────────────────────────
# Deliberately truncated/invalid JSON (unterminated string, no closing
# braces). Today's grep pattern requires a closing quote to match at all, so
# it simply fails to extract anything and allows the write (exit 0). A
# hardened implementation must treat a JSON parse failure as fail-closed.
echo "case: malformed JSON on stdin fails closed"
run_check '{"tool_input":{"file_path": "oops'
assert_exit "malformed JSON fails closed" 2
assert_blocked_stderr "malformed JSON fails closed"

# ── Relative file_path must fail closed ──────────────────────────────
echo "case: relative file_path fails closed"
run_check "{\"tool_input\":{\"file_path\":\"src/foo.txt\"}}"
assert_exit "relative file_path fails closed" 2
assert_blocked_stderr "relative file_path fails closed"

# ── Resolver (realpath/readlink) missing from PATH must fail closed ──
echo "case: path resolver (realpath/readlink) missing from PATH fails closed"
RESOLVER_MISSING_BIN="${TEST_ROOT}/bin-no-resolver"
make_curated_bin "${RESOLVER_MISSING_BIN}" sh cat grep sed jq
run_check_with_path "${RESOLVER_MISSING_BIN}" "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/src/foo.txt\"}}"
assert_exit "resolver missing fails closed" 2
assert_blocked_stderr "resolver missing fails closed"

# ── file_path present-but-empty must fall back to filePath (jq `//` bug) ──
# jq's `//` alternative operator only falls back on null/false, NOT on an
# empty string. A payload with BOTH keys present, file_path set to "" and
# filePath set to a genuinely sensitive path, previously extracted "" (the
# `//` never tried filePath because "" is not null) and hit the empty-path
# early `exit 0` -- silently ALLOWING the write. The extraction now checks
# emptiness explicitly so a present-but-empty file_path correctly falls
# through to filePath.
echo "case: empty file_path falls back to filePath (jq // does not treat \"\" as fallback trigger)"
run_check "{\"tool_input\":{\"file_path\":\"\",\"filePath\":\"${TEST_ROOT}/fallback/secrets/prod.env\"}}"
assert_exit "empty file_path falls back to filePath" 2
assert_blocked_stderr "empty file_path falls back to filePath"

# ── Genuine symlink loop: resolve_path is never reached (investigated) ──
# Investigated via `sh -x` trace: a two-node ELOOP cycle (a -> b, b -> a)
# makes BOTH `[ -e path ]` and `[ -d path ]` report false for any path that
# requires traversing the cycle -- the exact same signal this script already
# uses to decide "does not exist yet, walk up to the nearest real ancestor."
# So the parent-anchored ancestor walk backs off past the loop node(s) and
# calls resolve_path only on the nearest REAL, non-looping ancestor (which
# always exists -- the walk is guaranteed to terminate at "/"); the
# unresolved loop segment is carried forward only as a literal TAIL string
# (lexical_collapse never touches the filesystem). resolve_path is therefore
# never actually invoked on the unresolvable component for a pure ELOOP
# input -- confirmed empirically, not just by reading the code. Neither the
# "-e-true-but-resolve-fails" branch nor the "ancestor-walk resolve failure"
# branch is reachable this way: both would require resolve_path to run on a
# string that -e/-d already evaluated, and ELOOP makes all three (-e, -d,
# resolve_path) fail identically on that string, so it never gets there.
# Net effect: a bare symlink loop with a benign literal name degrades to
# raw-name-only matching and is ALLOWED (exit 0), not blocked. This is inert
# in practice -- an actual Write/Edit through a genuine symlink loop
# independently fails at the OS level (ELOOP) regardless of this hook's
# verdict, so no exploitable gap results. This test locks in that documented,
# verified behavior rather than a fabricated fail-closed assertion the
# current parent-anchored design cannot actually produce for this input.
echo "case: a genuine symlink loop (benign literal name) is allowed -- resolve_path is never reached"
mkdir -p "${TEST_ROOT}/loop"
ln -s b "${TEST_ROOT}/loop/a"
ln -s a "${TEST_ROOT}/loop/b"
run_check "{\"tool_input\":{\"file_path\":\"${TEST_ROOT}/loop/a\"}}"
assert_exit "symlink loop (benign literal name)" 0

# ── Lookalike file_path substring elsewhere in the JSON must be ignored ──
# The true tool_input.file_path is genuine; a sibling field (old_string, as
# in an Edit tool call) contains a lookalike '"file_path": "..."' substring
# pointing at the opposite classification. Field-scoped extraction (jq) must
# key off the real tool_input.file_path, not any "file_path"-shaped text
# found anywhere in the raw JSON. Built via jq -n --arg (never hand-escaped)
# to guarantee valid nesting/escaping.
#
# NOTE (empirically verified): because valid JSON escapes every quote inside
# a string value as \", the decoy text embedded in old_string never forms an
# unescaped `"file_path"` substring — grep's exact quote-adjacent pattern
# does not match it. So today's raw grep/sed extraction already returns the
# correct (true-field) result for this specific construction; it is not
# fooled by an *escaped* decoy. This matches the same construction already
# present in guard-main-worktree.test.sh, and was confirmed by running this
# exact JSON through the pre-hardening guard-main-worktree.sh (git rev
# d45e6fc^), which also correctly keyed off the true field.
#
# To actually reproduce today's field-scoping gap, the decoy must be a
# second *unescaped*, syntactically real "file_path" JSON key elsewhere in
# the tree (not embedded inside an escaped string value) — grep's head -1
# then picks whichever "file_path" key occurs first in the raw text,
# regardless of nesting/schema. That sub-case is included below and does
# fail today.
echo "case: lookalike file_path substring in old_string is ignored (true field sensitive)"
LOOKALIKE_JSON=$(jq -n --arg fp "${TEST_ROOT}/lookalike/.env" --arg os '"file_path": "'"${TEST_ROOT}"'/lookalike/benign.txt"' '{tool_input:{old_string:$os,file_path:$fp}}')
run_check "${LOOKALIKE_JSON}"
assert_exit "lookalike substring ignored (true field sensitive)" 2
assert_blocked_stderr "lookalike substring ignored (true field sensitive)"

echo "case: lookalike file_path substring in old_string is ignored (true field benign)"
LOOKALIKE_JSON_BENIGN=$(jq -n --arg fp "${TEST_ROOT}/lookalike/benign2.txt" --arg os '"file_path": "'"${TEST_ROOT}"'/lookalike/.env"' '{tool_input:{old_string:$os,file_path:$fp}}')
run_check "${LOOKALIKE_JSON_BENIGN}"
assert_exit "lookalike substring ignored (true field benign)" 0

echo "case: a genuine (unescaped) second file_path key elsewhere in the tree is ignored"
NESTED_LOOKALIKE_JSON=$(jq -n --arg decoy "${TEST_ROOT}/lookalike/decoy-benign.txt" --arg fp "${TEST_ROOT}/lookalike/real-sensitive.env" '{tool_input:{decoy:{file_path:$decoy},file_path:$fp}}')
run_check "${NESTED_LOOKALIKE_JSON}"
assert_exit "nested second file_path key ignored" 2
assert_blocked_stderr "nested second file_path key ignored"

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
