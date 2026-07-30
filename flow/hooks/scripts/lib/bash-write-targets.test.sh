#!/usr/bin/env bash
# Unit tests for flow/hooks/scripts/lib/bash-write-targets.sh (ticket #795).
#
# Classification: pure-parser unit suite. The tokenizer's failure modes (a
# `>` inside single vs double quotes, `2>&1` fd-dup vs `2>file`, `&>>`,
# `>|`, `tee -a -- f1 f2`, an escaped `\>`) are combinatorial and cannot be
# reasonably enumerated through the two guards' JSON-on-stdin entry point
# without a multiplicative blow-up of near-duplicate integration cases --
# hence a direct unit suite against the sourced lib functions.
#
# Mirrors the repo's existing shell-test harness idiom: plain bash, no
# framework, PASS/FAIL counters, non-zero exit on failure. The lib is
# `.`-sourced (not executed) since its functions must run in this test
# script's own shell.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_SH="${SCRIPT_DIR}/bash-write-targets.sh"

FAILURES=0
PASSES=0

fail() {
    echo "  FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

echo "bash-write-targets.test.sh"

if [[ ! -r "${LIB_SH}" ]]; then
    fail "lib not found or not readable: ${LIB_SH} (bash-write-targets.sh does not exist yet -- expected during red phase of #795)"
    echo
    echo "passed: ${PASSES}, failed: ${FAILURES}"
    exit 1
fi

# shellcheck source=/dev/null
. "${LIB_SH}"

for fn in bwt_has_write_candidate bwt_extract_targets bwt_zero_parse_suspicious bwt_has_delimited_tee bwt_is_exempt_device bwt_expand_safe_vars bwt_is_unresolved; do
    if ! command -v "${fn}" >/dev/null 2>&1; then
        fail "lib did not define expected function: ${fn}"
    fi
done

TEST_ROOT="$(mktemp -d /var/tmp/bash-write-targets-test.XXXXXX)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# ── Helpers ──────────────────────────────────────────────────────────

assert_true() {
    local label="$1"
    shift
    if "$@"; then
        pass
    else
        fail "${label}: expected success (exit 0), got $?"
    fi
}

assert_false() {
    local label="$1"
    shift
    if "$@"; then
        fail "${label}: expected failure (non-zero exit), got 0"
    else
        pass
    fi
}

assert_targets() {
    local label="$1" cmd="$2" expected="$3"
    local actual
    actual="$(bwt_extract_targets "${cmd}")"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: extract_targets(<${cmd}>) = <${actual}>, expected <${expected}>"
    fi
}

# assert_extract_exit <label> <cmd> <expected-exit> — like assert_targets, but
# asserts on bwt_extract_targets' own exit status rather than its output.
assert_extract_exit() {
    local label="$1" cmd="$2" expected="$3"
    local actual_out actual_exit
    actual_out="$(bwt_extract_targets "${cmd}")"
    actual_exit=$?
    if [[ "${actual_exit}" -eq "${expected}" ]]; then
        pass
    else
        fail "${label}: bwt_extract_targets exit=${actual_exit}, expected ${expected} (output=<${actual_out}>)"
    fi
    if [[ "${expected}" -ne 0 ]]; then
        if [[ -z "${actual_out}" ]]; then
            pass
        else
            fail "${label}: non-zero extraction emitted partial output <${actual_out}>"
        fi
    fi
}

assert_expand() {
    local label="$1" target="$2" expected="$3"
    local actual
    actual="$(bwt_expand_safe_vars "${target}")"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: expand_safe_vars(<${target}>) = <${actual}>, expected <${expected}>"
    fi
}

# expand_with_tmpdir <tmpdir-value-or-__unset__> <target> — runs
# bwt_expand_safe_vars with TMPDIR temporarily set (or unset), restoring the
# prior value afterward so cases don't leak state into each other.
expand_with_tmpdir() {
    local val="$1" target="$2" out
    local had_tmpdir=0 old_tmpdir=""
    if [[ -n "${TMPDIR+x}" ]]; then
        had_tmpdir=1
        old_tmpdir="${TMPDIR}"
    fi
    if [[ "${val}" == "__unset__" ]]; then
        unset TMPDIR
    else
        TMPDIR="${val}"
    fi
    out="$(bwt_expand_safe_vars "${target}")"
    if [[ "${had_tmpdir}" -eq 1 ]]; then
        TMPDIR="${old_tmpdir}"
    else
        unset TMPDIR
    fi
    printf '%s' "${out}"
}

assert_expand_with_tmpdir() {
    local label="$1" val="$2" target="$3" expected="$4"
    local actual
    actual="$(expand_with_tmpdir "${val}" "${target}")"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: expand_safe_vars(<${target}> with TMPDIR=<${val}>) = <${actual}>, expected <${expected}>"
    fi
}

# expand_with_var <name> <value-or-__unset__> <target> — like
# expand_with_tmpdir but generic over HOME/PWD.
expand_with_var() {
    local name="$1" val="$2" target="$3" out
    local had=0 old=""
    if [[ -n "${!name+x}" ]]; then
        had=1
        old="${!name}"
    fi
    if [[ "${val}" == "__unset__" ]]; then
        unset "${name}"
    else
        printf -v "${name}" '%s' "${val}"
    fi
    out="$(bwt_expand_safe_vars "${target}")"
    if [[ "${had}" -eq 1 ]]; then
        printf -v "${name}" '%s' "${old}"
    else
        unset "${name}"
    fi
    printf '%s' "${out}"
}

assert_expand_with_var() {
    local label="$1" name="$2" val="$3" target="$4" expected="$5"
    local actual
    actual="$(expand_with_var "${name}" "${val}" "${target}")"
    if [[ "${actual}" == "${expected}" ]]; then
        pass
    else
        fail "${label}: expand_safe_vars(<${target}> with ${name}=<${val}>) = <${actual}>, expected <${expected}>"
    fi
}

VALID_TMPDIR="${TEST_ROOT}/validtmp"
mkdir -p "${VALID_TMPDIR}"
NONEXISTENT_TMPDIR="${TEST_ROOT}/no-such-tmpdir"

# ── bwt_has_write_candidate: cheap early-exit predicate ─────────────
echo "case: bwt_has_write_candidate"
assert_false "no redirect, no tee -> not a candidate" bwt_has_write_candidate "git status"
assert_false "plain read command -> not a candidate" bwt_has_write_candidate "cat file.txt"
assert_true "unquoted > -> candidate" bwt_has_write_candidate "echo x > file"
assert_true "append >> -> candidate" bwt_has_write_candidate "echo x >> file"
assert_true "tee token -> candidate" bwt_has_write_candidate "foo | tee file"
assert_true "fd-prefixed redirect -> candidate" bwt_has_write_candidate "echo x 2> file"

# ── bwt_extract_targets: basic operators ────────────────────────────
echo "case: bwt_extract_targets basic operators"
assert_targets "> operator" "echo x > ${TEST_ROOT}/a.txt" "${TEST_ROOT}/a.txt"
assert_targets ">> operator" "echo x >> ${TEST_ROOT}/a.txt" "${TEST_ROOT}/a.txt"
assert_targets ">| operator (clobber)" "echo x >| ${TEST_ROOT}/a.txt" "${TEST_ROOT}/a.txt"
assert_targets "fd-prefixed 2>" "echo x 2> ${TEST_ROOT}/err.txt" "${TEST_ROOT}/err.txt"
assert_targets "fd-prefixed 2>>" "echo x 2>> ${TEST_ROOT}/err.txt" "${TEST_ROOT}/err.txt"
assert_targets "combined &>" "echo x &> ${TEST_ROOT}/both.txt" "${TEST_ROOT}/both.txt"
assert_targets "combined &>>" "echo x &>> ${TEST_ROOT}/both.txt" "${TEST_ROOT}/both.txt"
assert_targets "combined >&" "echo x >& ${TEST_ROOT}/both2.txt" "${TEST_ROOT}/both2.txt"

# ── bwt_extract_targets: fd-dup operands must be skipped, not targets ──
echo "case: bwt_extract_targets fd-dup operands are skipped"
assert_targets "2>&1 is a dup, not a path" "cmd 2>&1" ""
assert_targets "1>&2 is a dup, not a path" "cmd 1>&2" ""
assert_targets "dup then a real redirect target" "cmd 2>&1 > ${TEST_ROOT}/out.txt" "${TEST_ROOT}/out.txt"

# ── bwt_extract_targets: quote-state awareness ──────────────────────
echo "case: bwt_extract_targets quote-state awareness"
Q_SINGLE="echo '>' > ${TEST_ROOT}/quoted-single.txt"
assert_targets "quoted > inside single quotes is not an operator" "${Q_SINGLE}" "${TEST_ROOT}/quoted-single.txt"

Q_DOUBLE="echo \"a > b\" > ${TEST_ROOT}/quoted-double.txt"
assert_targets "quoted > inside double quotes is not an operator" "${Q_DOUBLE}" "${TEST_ROOT}/quoted-double.txt"

Q_ESCAPED="echo foo\\> bar > ${TEST_ROOT}/escaped.txt"
assert_targets "backslash-escaped > is not an operator" "${Q_ESCAPED}" "${TEST_ROOT}/escaped.txt"

# ── bwt_extract_targets: multiple redirects in one command ──────────
echo "case: bwt_extract_targets multiple redirects, only later one sensitive (both extracted)"
assert_targets "two redirects both extracted, in order" \
    "cmd > ${TEST_ROOT}/first.txt 2> ${TEST_ROOT}/second.txt" \
    "$(printf '%s\n%s' "${TEST_ROOT}/first.txt" "${TEST_ROOT}/second.txt")"

# ── bwt_extract_targets: tee operands ────────────────────────────────
echo "case: bwt_extract_targets tee operands"
assert_targets "plain tee" "foo | tee ${TEST_ROOT}/tee1.txt" "${TEST_ROOT}/tee1.txt"
assert_targets "tee -a (append flag)" "foo | tee -a ${TEST_ROOT}/tee2.txt" "${TEST_ROOT}/tee2.txt"
assert_targets "tee -a -- explicit end-of-options" "foo | tee -a -- ${TEST_ROOT}/tee3.txt" "${TEST_ROOT}/tee3.txt"
assert_targets "tee with multiple operands, all extracted, in order" \
    "foo | tee ${TEST_ROOT}/tee-a.txt ${TEST_ROOT}/tee-b.txt" \
    "$(printf '%s\n%s' "${TEST_ROOT}/tee-a.txt" "${TEST_ROOT}/tee-b.txt")"

# ── Fix 2 (security): a literal embedded newline is a statement separator ──
# `tee` landing as the first token on a new line (no leading whitespace)
# must still be detected -- a newline must reset tee-scan state exactly like
# `|`/`;`, not fall through to the default "append to word" branch.
echo "case: tee as the first token on a new line is detected (Fix 2 regression)"
NEWLINE_CMD="$(printf 'cd /workspace\ntee -a %s/authorized_keys' "${TEST_ROOT}")"
assert_targets "tee first token on new line is detected" "${NEWLINE_CMD}" "${TEST_ROOT}/authorized_keys"

# ── Fix 3 (tee detection): path-qualified tee invocations must be matched ──
# by basename ("${_bwt_w##*/}"), not only the bare unqualified word "tee".
echo "case: path-qualified tee invocations are detected (Fix 3 regression)"
assert_targets "/usr/bin/tee is detected" "foo | /usr/bin/tee ${TEST_ROOT}/qualified1.txt" "${TEST_ROOT}/qualified1.txt"
assert_targets "./tee is detected" "foo | ./tee ${TEST_ROOT}/qualified2.txt" "${TEST_ROOT}/qualified2.txt"

# ── Fix 5: bare &> is never an fd dup -- it always redirects to a literal
# file (confirmed against real bash: `echo hello &> 2` creates a file named
# "2"). Only the target-then-amp form (>&) keeps the fd-dup ambiguity.
echo "case: bare &> extracts its operand as a real target, never an fd dup (Fix 5 regression)"
assert_targets "cmd &> 2 extracts literal file named 2" "cmd &> 2" "2"
assert_targets "cmd 2>&1 (classic fd-dup form) still extracts nothing" "cmd 2>&1" ""

# ── Fix A (security, #795 round 2): backslash-newline is a true line
# continuation, not a literal newline appended into the word -- a following
# raw `print` must never fragment one logical target into two physical
# lines that individually dodge the sensitive-file blocklist.
echo "case: backslash-newline line continuation joins into one target, not two lines (Fix A regression)"
BSNL_CMD="$(printf 'cat foo > .e\\\nnv')"
assert_targets "cat foo > .e\\<newline>nv resolves to single-line .env" "${BSNL_CMD}" ".env"

BSNL_TEE_CMD="$(printf 'tee .e\\\nnv')"
assert_targets "tee .e\\<newline>nv (tee operand) resolves to single-line .env" "${BSNL_TEE_CMD}" ".env"

# ── Fix B (must-fix, #795 round 2): unquoted ( and ) are word/token
# boundaries, mirroring real POSIX shell grammar -- gluing them onto an
# adjacent word (no whitespace required) must not defeat detection.
echo "case: unquoted ( and ) act as word boundaries (Fix B regression)"
assert_targets "(tee /path) -- ( glued to tee still detects the tee invocation" \
    "(tee ${TEST_ROOT}/paren-tee.txt)" "${TEST_ROOT}/paren-tee.txt"
assert_targets "(echo hi>/repo/.env) -- trailing ) is not glued onto the target" \
    "(echo hi>${TEST_ROOT}/paren-target.env)" "${TEST_ROOT}/paren-target.env"

# Unquoted < is also a boundary (defense-in-depth completeness, same bypass
# class): a following word (e.g. tee) must not be glued onto it undetected.
# No target is extracted from `<` itself (input redirection is out of
# scope), but `tee` immediately after it must still be recognized as its own
# token in command position.
echo "case: unquoted < is a word boundary so tee<file doesn't merge tee into an unmatched word (Fix B regression)"
assert_targets "tee<file alone extracts nothing (no operand follows the boundary)" "tee<file" ""
assert_targets "(tee /path)<sensitive still detects the tee invocation before <" \
    "(tee ${TEST_ROOT}/paren-lt-tee.txt)<${TEST_ROOT}/ignored-input" "${TEST_ROOT}/paren-lt-tee.txt"

# ── Fix C reverted (#795 round 3): a prior round gated bare-"tee" detection
# on command position (`expect_cmd`) specifically so `grep tee /repo/.env`
# would NOT be treated as a tee invocation. Three independent reviewers
# proved that gate silently disabled detection for `sudo tee`, `{ tee; }`,
# `if...then tee...fi`, and similar ordinary constructs (command-position
# tracking for an open-ended set of shell reserved words and wrapper
# commands cannot be done safely/completely by lexical means alone), so it
# was reverted: tee detection now matches the bare word "tee" (by basename)
# anywhere in the command, unconditionally. This is the accepted,
# documented, safe-by-over-blocking tradeoff -- `grep tee /repo/.env` is
# once again treated as a possible tee invocation and /repo/.env IS
# extracted as a target, even though grep never actually writes to it.
echo "case: bare tee as a plain argument IS treated as a possible tee invocation (Fix C reverted, over-blocking by design)"
assert_targets "grep tee /repo/.env is (again) treated as a tee invocation (accepted over-blocking tradeoff)" \
    "grep tee ${TEST_ROOT}/false-positive.env" "${TEST_ROOT}/false-positive.env"

# Re-verify genuine tee invocations are still detected after reverting
# expect_cmd (the revert must not reintroduce Fix B's glued-metacharacter
# bypasses or regress any pre-existing detection).
echo "case: genuine tee invocations still detected after reverting expect_cmd (non-regression)"
assert_targets "(tee /path) still detected" "(tee ${TEST_ROOT}/expect-cmd-paren.txt)" "${TEST_ROOT}/expect-cmd-paren.txt"
assert_targets "tee -a <target> still detected" "tee -a ${TEST_ROOT}/expect-cmd-append.txt" "${TEST_ROOT}/expect-cmd-append.txt"
assert_targets "/usr/bin/tee <target> still detected" "foo | /usr/bin/tee ${TEST_ROOT}/expect-cmd-qualified1.txt" "${TEST_ROOT}/expect-cmd-qualified1.txt"
assert_targets "./tee <target> still detected" "foo | ./tee ${TEST_ROOT}/expect-cmd-qualified2.txt" "${TEST_ROOT}/expect-cmd-qualified2.txt"

# ── Round-3 regression: each previously-bypassed construct (all silently
# produced ZERO targets under expect_cmd -- verified concrete bypasses from
# three independent reviewers) must now be correctly detected.
echo "case: expect_cmd bypasses are fixed -- reserved-word and wrapper-command constructs (round 3 regression)"
assert_targets "if true; then tee /repo/.env; fi is detected" \
    "if true; then tee ${TEST_ROOT}/bypass-if.env; fi" "${TEST_ROOT}/bypass-if.env"
assert_targets "while tee /repo/.env; do break; done is detected" \
    "while tee ${TEST_ROOT}/bypass-while.env; do break; done" "${TEST_ROOT}/bypass-while.env"
assert_targets "{ tee /repo/.env; } is detected" \
    "{ tee ${TEST_ROOT}/bypass-brace.env; }" "${TEST_ROOT}/bypass-brace.env"
assert_targets "case x in x) tee /repo/.env;; esac is detected" \
    "case x in x) tee ${TEST_ROOT}/bypass-case.env;; esac" "${TEST_ROOT}/bypass-case.env"
assert_targets "sudo tee /repo/.env is detected" \
    "sudo tee ${TEST_ROOT}/bypass-sudo.env" "${TEST_ROOT}/bypass-sudo.env"
assert_targets "FOO=bar tee /repo/.env is detected" \
    "FOO=bar tee ${TEST_ROOT}/bypass-envassign.env" "${TEST_ROOT}/bypass-envassign.env"

# ── Fix 1 (must-fix, #795 round 4): the round-3 "unquoted { and } are word
# boundaries" treatment is reverted -- it was based on a flawed analogy to
# (/) and truncated legitimate literal-brace filenames. `{tee /path;}` (no
# surrounding whitespace) is not valid bash grammar in the first place (a
# real syntax error) -- there was never a real bypass through that construct
# to fix. Meanwhile real bash does NOT brace-expand a bare `{x}` with no
# comma/range inside, so a literal-brace path must be extracted IN FULL, not
# truncated at the first `{`.
echo "case: a literal-brace path is extracted in full, not truncated (Fix 1 regression)"
assert_targets "echo x > /repo/{decoy}.env extracts the full literal target" \
    "echo x > ${TEST_ROOT}/{decoy}.env" "${TEST_ROOT}/{decoy}.env"

# Legitimate `{ tee ...; }` command-grouping (correctly whitespace-separated,
# unlike the reverted glued form above) must still detect tee and its target
# correctly -- this works via ordinary space-triggered flushing now that {/}
# are no longer special-cased, with no boundary handling required.
echo "case: { tee /path; } (whitespace-separated grouping) still detects tee (Fix 1 non-regression)"
assert_targets "{ tee /repo/.env; } still detects the tee invocation" \
    "{ tee ${TEST_ROOT}/brace-grouping.env; }" "${TEST_ROOT}/brace-grouping.env"

# ── Fix 2 (high, #795 round 4): unquoted backtick is an unconditional shell
# metacharacter (always triggers command substitution regardless of adjacent
# whitespace, unlike {/}) and must act as a word boundary exactly like (/) --
# a glued `` `tee `` must not tokenize as a single 4-character literal word
# that never matches the basename "tee" check.
echo "case: backtick-wrapped tee invocation is detected (Fix 2 regression)"
assert_targets "\`tee /repo/.env\` (bare backtick-wrapped tee, no space) is detected" \
    "\`tee ${TEST_ROOT}/backtick-bare.env\`" "${TEST_ROOT}/backtick-bare.env"
assert_targets "cmd \`tee /repo/.env\` (backtick substitution as a later word) is detected" \
    "cmd \`tee ${TEST_ROOT}/backtick-later.env\`" "${TEST_ROOT}/backtick-later.env"

# A backtick inside single quotes is unaffected (still a literal, non-
# substituting character) -- the target must be extracted in full, backticks
# included, exactly as with any other quoted metacharacter.
echo "case: a quoted backtick in a target string is unaffected (Fix 2 non-regression)"
assert_targets "echo x > '/repo/\`weird\`.env' extracts the full literal target, backticks included" \
    "echo x > '${TEST_ROOT}/\`weird\`.env'" "${TEST_ROOT}/\`weird\`.env"

# ── #795 final round: a redirect between a command's operands does not end
# its argument list -- clearing tee state on `>`/`&>` silently dropped every
# tee operand after the redirect (`tee /tmp/decoy >/dev/null /repo/.env`
# extracted only decoy and /dev/null), a wrong-but-non-empty parse the
# empty-parse backstop can never catch. Only genuine command separators
# (`;`, `|`, newline, `(`, `)`, backtick, plain `&`) reset tee state.
echo "case: a redirect between tee operands does not clear tee state (mid-command redirect regression)"
assert_targets "tee decoy >/dev/null realtarget extracts all three, in order" \
    "tee ${TEST_ROOT}/decoy.txt >/dev/null ${TEST_ROOT}/real.env" \
    "$(printf '%s\n%s\n%s' "${TEST_ROOT}/decoy.txt" "/dev/null" "${TEST_ROOT}/real.env")"
assert_targets "tee f1 2>>log f2 keeps f2 as a tee operand" \
    "tee ${TEST_ROOT}/mid-f1.txt 2>>${TEST_ROOT}/mid-log.txt ${TEST_ROOT}/mid-f2.env" \
    "$(printf '%s\n%s\n%s' "${TEST_ROOT}/mid-f1.txt" "${TEST_ROOT}/mid-log.txt" "${TEST_ROOT}/mid-f2.env")"
assert_targets "tee f1 &>/dev/null f2 keeps f2 as a tee operand (&> sub-branch)" \
    "tee ${TEST_ROOT}/amp-f1.txt &>/dev/null ${TEST_ROOT}/amp-f2.env" \
    "$(printf '%s\n%s\n%s' "${TEST_ROOT}/amp-f1.txt" "/dev/null" "${TEST_ROOT}/amp-f2.env")"
assert_targets "plain job-control & still resets tee state (genuine separator)" \
    "tee ${TEST_ROOT}/amp-sep.txt & echo x" "${TEST_ROOT}/amp-sep.txt"

# ── #795 final round: leading-tilde handling. A plain unquoted ~ expands via
# bwt_expand_safe_vars (mirroring the \$HOME branch) so a literal
# ~/.claude/settings.json no longer mis-canonicalizes to an in-root relative
# path (which over-blocked the /cenci:configure pattern Q1a exists for). A
# QUOTED or escaped leading tilde is a literal filename character in real
# shells -- the tokenizer neutralizes it to ./~ so the expander can never
# tilde-expand it (expanding it would misclassify an in-root literal-~ write
# as out-of-root, a fail-open).
echo "case: bwt_expand_safe_vars ~ / ~/"
assert_expand_with_var "set absolute HOME: ~/x expands" HOME "${TEST_ROOT}/home" '~/x' "${TEST_ROOT}/home/x"
assert_expand_with_var "set absolute HOME: bare ~ expands" HOME "${TEST_ROOT}/home" '~' "${TEST_ROOT}/home"
assert_expand_with_var "unset HOME: ~/x is left unchanged" HOME "__unset__" '~/x' '~/x'
assert_expand_with_var "relative HOME: ~/x is left unchanged" HOME "relative-home" '~/x' '~/x'
assert_expand_with_var "~user is not expanded (out of the narrow ~/ form)" HOME "${TEST_ROOT}/home" '~user/x' '~user/x'
assert_expand_with_var "a ./~ literal is never tilde-expanded" HOME "${TEST_ROOT}/home" './~/x' './~/x'

echo "case: a quoted or escaped leading tilde is neutralized to ./~ (literal, never expanded)"
assert_targets "double-quoted tilde target is ./~-prefixed" \
    'echo x > "~/lit.txt"' './~/lit.txt'
assert_targets "single-quoted tilde target is ./~-prefixed" \
    "echo x > '~/lit.txt'" './~/lit.txt'
assert_targets "backslash-escaped tilde target is ./~-prefixed" \
    'echo x > \~/lit.txt' './~/lit.txt'
assert_targets "empty-quoted-string-then-tilde is ./~-prefixed (tilde not at word start after quote removal)" \
    "echo x > ''~/lit.txt" './~/lit.txt'
assert_targets "plain unquoted tilde target stays ~ (expandable)" \
    'echo x > ~/real.txt' '~/real.txt'

# ── #795 final round: bwt_zero_parse_suspicious, the empty-parse backstop
# trigger. Zero extracted targets is byte-identical for "genuinely writes
# nothing" and "unmodelled construct"; this predicate separates the two.
# The tee half requires a DELIMITED tee token (an alnum-embedded "tee" --
# sixteen, guarantee, committee -- can never invoke tee under any shell
# parse); the > half is a plain substring check.
echo "case: bwt_zero_parse_suspicious (empty-parse backstop trigger)"
assert_true "brace-expansion tee construct is suspicious" bwt_zero_parse_suspicious '{tee,cat} /repo/.env'
assert_true "any > (even quoted) is suspicious" bwt_zero_parse_suspicious 'echo "a > b"'
assert_true "backslash-prefixed \\tee is suspicious (delimited)" bwt_zero_parse_suspicious '\tee /repo/x'
assert_true "command-substitution tee is suspicious" bwt_zero_parse_suspicious 'x=$(tee /repo/x)'
assert_false "alnum-embedded tee (guarantee) is not suspicious" bwt_zero_parse_suspicious 'grep guarantee /workspace/x'
assert_false "alnum-embedded tee (sixteen) is not suspicious" bwt_zero_parse_suspicious 'echo sixteen items'
assert_false "no > and no tee at all is not suspicious" bwt_zero_parse_suspicious 'git status'

# ── bwt_has_delimited_tee (#810): extracted from bwt_zero_parse_suspicious's
# existing delimited-tee scan into its own named helper so
# guard-main-worktree.sh can call it directly from its zero-parse backstop
# (ticket #810's fix for regression #2's relative-target hole: a zero-parse
# command containing a delimited tee token must block unconditionally,
# since its target may be relative). Same delimited-match semantics as
# bwt_zero_parse_suspicious's tee half: a literal "tee" adjacent to a
# non-word character (comma, backslash, backtick, ...), never an
# alnum-embedded substring (guarantee, sixteen, committee).
echo "case: bwt_has_delimited_tee (new helper, #810)"
assert_true "brace-expansion tee construct has a delimited tee" bwt_has_delimited_tee '{tee,cat} f'
assert_true "backtick-wrapped tee has a delimited tee" bwt_has_delimited_tee '`tee f`'
assert_true "backslash-prefixed \\tee has a delimited tee" bwt_has_delimited_tee '\tee f'
assert_false "alnum-embedded tee (guarantee) has no delimited tee" bwt_has_delimited_tee 'grep guarantee /x'
assert_false "no tee at all has no delimited tee" bwt_has_delimited_tee 'git status'

# ── Ticket #810 fix 1: unquoted brace expansion in write-target position ──
# must fail closed with a new distinct exit code (6), joining 3/4/5 -- never
# silently emit a bogus literal target that bash would actually multiplex
# into several real write targets (Requirement A).
echo "case: unquoted brace expansion in write-target position fails closed with exit 6 (#810 Fix 1)"
assert_extract_exit "tee /tmp/x/{notes,.env} (comma form, tee operand) returns exit 6" \
    "tee /tmp/x/{notes,.env}" 6
assert_extract_exit "echo x > /repo/f{1,2}.txt (comma form, redirect target) returns exit 6" \
    "echo x > /repo/f{1,2}.txt" 6
assert_extract_exit "echo x > /repo/f{1..3} (range form, redirect target) returns exit 6" \
    "echo x > /repo/f{1..3}" 6
CONTINUED_BRACE_RANGE=$'tee .{e.\\\n.f}nv'
assert_extract_exit "a backslash-newline cannot hide a brace range that expands to .env" \
    "${CONTINUED_BRACE_RANGE}" 6
assert_extract_exit "tee -a /repo/{a,b} (comma form, tee operand after option) returns exit 6" \
    "tee -a /repo/{a,b}" 6

# Non-regressions: brace text a real shell does NOT expand (no unquoted
# comma/range inside an unquoted brace pair) must stay exit 0 and extract
# the FULL literal target, never truncate and never misfire the new exit 6.
echo "case: literal/quoted/escaped brace text is not brace expansion and stays exit 0 (#810 non-regression)"
assert_extract_exit "echo x > \$TEST_ROOT/{decoy}.env (no comma/range) returns exit 0" \
    "echo x > ${TEST_ROOT}/{decoy}.env" 0
assert_targets "echo x > \$TEST_ROOT/{decoy}.env (no comma/range) still extracts the full literal target" \
    "echo x > ${TEST_ROOT}/{decoy}.env" "${TEST_ROOT}/{decoy}.env"

SQ_BRACE_CMD="echo x > '${TEST_ROOT}/{a,b}.txt'"
assert_extract_exit "single-quoted {a,b} literal returns exit 0" "${SQ_BRACE_CMD}" 0
assert_targets "single-quoted {a,b} literal extracts the full literal target" \
    "${SQ_BRACE_CMD}" "${TEST_ROOT}/{a,b}.txt"

DQ_BRACE_CMD="echo x > \"${TEST_ROOT}/{a,b}.txt\""
assert_extract_exit "double-quoted {a,b} literal returns exit 0" "${DQ_BRACE_CMD}" 0
assert_targets "double-quoted {a,b} literal extracts the full literal target" \
    "${DQ_BRACE_CMD}" "${TEST_ROOT}/{a,b}.txt"

ESC_BRACE_CMD="echo x > ${TEST_ROOT}/\\{a,b\\}.txt"
assert_extract_exit "escaped \\{a,b\\} literal returns exit 0" "${ESC_BRACE_CMD}" 0
assert_targets "escaped \\{a,b\\} literal extracts the full literal target" \
    "${ESC_BRACE_CMD}" "${TEST_ROOT}/{a,b}.txt"

echo "case: { tee X; } bash grouping syntax still correctly detects target X (#810 non-regression)"
assert_extract_exit "{ tee X; } grouping returns exit 0" \
    "{ tee ${TEST_ROOT}/grouping-target.txt; }" 0
assert_targets "{ tee X; } grouping still detects the tee invocation and target" \
    "{ tee ${TEST_ROOT}/grouping-target.txt; }" "${TEST_ROOT}/grouping-target.txt"

# ── Ticket #810 fix 3: `>` inside a CLOSED [[ ... ]] / (( ... )) comparison
# context must never be tokenized as a redirect (Requirement C) -- but a
# real redirect immediately adjacent to such a construct must still be
# extracted, and an UNCLOSED [[ (anti-fail-open risk) must never suppress a
# real redirect either.
echo "case: > inside a closed [[ ... ]] / (( ... )) region is not a redirect (#810 Fix 3)"
assert_extract_exit "[[ z > a ]] returns exit 0" "[[ z > a ]]" 0
assert_targets "[[ z > a ]] emits no write target" "[[ z > a ]]" ""
assert_extract_exit "(( z > a )) returns exit 0" "(( z > a ))" 0
assert_targets "(( z > a )) emits no write target" "(( z > a ))" ""

echo "case: a real redirect adjacent to a [[ / (( region is still extracted (#810 Fix 3 non-regression)"
assert_targets "[[ -f x ]] > /repo/out still extracts /repo/out" \
    "[[ -f x ]] > ${TEST_ROOT}/region-adjacent-1.out" "${TEST_ROOT}/region-adjacent-1.out"
assert_targets "echo \$(( 1 > 2 )) > /repo/out still extracts /repo/out" \
    "echo \$(( 1 > 2 )) > ${TEST_ROOT}/region-adjacent-2.out" "${TEST_ROOT}/region-adjacent-2.out"

echo "case: an unclosed [[ never suppresses a real redirect (anti-fail-open, #810 Fix 3)"
UNCLOSED_1="echo '[[' ; cat x > ${TEST_ROOT}/unclosed-1.out"
assert_targets "echo '[[' ; cat x > /repo/out still extracts /repo/out (quoted [[ earlier in the command)" \
    "${UNCLOSED_1}" "${TEST_ROOT}/unclosed-1.out"
UNCLOSED_2="echo [[ > ${TEST_ROOT}/unclosed-2.out"
assert_targets "echo [[ > /repo/out (unclosed [[, never closed) still extracts /repo/out" \
    "${UNCLOSED_2}" "${TEST_ROOT}/unclosed-2.out"

# ── Ticket #810 stabilization review, Bug 1 [CRITICAL]: a sticky `curregion`
# flag leaked from a closed (( ... )) / $(( ... )) region into the NEXT
# word's state, since flush() reset curregion only AFTER its
# `if (!wordhas) return` early exit -- a (( ... )) region always ends by
# dispatching an empty-word flush(), so curregion stayed 1 and disabled
# tee-ARMING for a following `tee` word that was never actually inside any
# region. Confirmed against real bash: both commands below genuinely write
# to their target.
echo "case: a closed (( ... )) region does not leak curregion into the next word, disabling tee detection (Bug 1 regression)"
assert_targets "(( 1 )) ; tee AGENTS.md > /dev/null -- tee still arms after a closed (( )) region" \
    "(( 1 )) ; tee ${TEST_ROOT}/bug1-parens.md > /dev/null" \
    "$(printf '%s\n%s' "${TEST_ROOT}/bug1-parens.md" "/dev/null")"
assert_targets "echo \$((1+1)) | tee AGENTS.md > /dev/null -- tee still arms after a closed \$(( )) region" \
    "echo \$((1+1)) | tee ${TEST_ROOT}/bug1-dollar-parens.md > /dev/null" \
    "$(printf '%s\n%s' "${TEST_ROOT}/bug1-dollar-parens.md" "/dev/null")"

# ── Ticket #810 stabilization review, Bug 2 [HIGH]: mark_regions() had no
# token-boundary check (so it could pair a "[[" glued mid-word, which real
# bash never treats as the reserved conditional token, with a later "]]")
# and no balance/nesting check (so it could pair a mismatched/malformed
# "((" ... "))"-shaped span that bash's real parser does NOT treat as one
# arithmetic construct). Both let a real redirect get wrongly suppressed.
echo "case: a glued [[ (not its own token) is never treated as a region opener (Bug 2 Part A regression)"
assert_targets "echo x[[ z > AGENTS.md ]] b -- glued [[ does not suppress the real redirect" \
    "echo x[[ z > ${TEST_ROOT}/bug2-glued.md ]] b" "${TEST_ROOT}/bug2-glued.md"

echo "case: [[ with no space after it is never treated as a region opener (Bug 2 Part A regression)"
assert_targets "[[z > AGENTS.md ]] -- [[ with no trailing space does not suppress the real redirect" \
    "[[z > ${TEST_ROOT}/bug2-nospace.md ]]" "${TEST_ROOT}/bug2-nospace.md"

echo "case: a mismatched/malformed ((...)) -shaped span is never treated as one closed region (Bug 2 Part B regression)"
assert_targets "((printf x > AGENTS.md); (true)) -- malformed (( )) does not suppress the real redirect" \
    "((printf x > ${TEST_ROOT}/bug2-malformed.md); (true))" "${TEST_ROOT}/bug2-malformed.md"

# ── Also fix separately (code-review-flagged test gap) ───────────────
# The case immediately above never actually reaches find_close(): "((printf"
# has no whitespace after the "((" opener, so is_opener_followed_by_ws()
# already rejects it before find_close() ever runs -- its outcome is
# correct but does not exercise what its label claims. This case adds a
# "((" opener WITH a trailing space (so it genuinely reaches find_close())
# whose interior is still malformed/mismatched, proving find_close()'s own
# balance/nesting-abandon logic (not just the earlier whitespace-boundary
# check) correctly leaves the real redirect unsuppressed.
echo "case: a ((-with-trailing-space, still-malformed span genuinely reaches find_close()'s abandon path (test-gap fix)"
assert_targets "(( printf x > AGENTS.md); (true)) -- malformed (( )) with a real space after (( still does not suppress the real redirect" \
    "(( printf x > ${TEST_ROOT}/bug2-malformed-spaced.md); (true))" "${TEST_ROOT}/bug2-malformed-spaced.md"

# ── Ticket #810 stabilization review (second cycle), Bug 3 [CRITICAL]:
# is_opener_boundary/find_close accepted spans bash does not actually treat
# as "[[ ]]"/"(( ))", allowing three fail-open bypass vectors.

# Vector 1: `<`/`>` immediately followed by another `(` is bash PROCESS
# SUBSTITUTION, not a real "((" arithmetic-construct start. Confirmed
# exploit: `echo x >(( : > /repo/.env ))` really runs the subshell
# `( : > /repo/.env )` (truncating /repo/.env) under real bash.
echo "case: process substitution glued to (( is never treated as a real (( opener (Bug 3 Vector 1 regression)"
assert_targets "echo x >(( : > /repo/.env )) -- the real write inside the process-substitution subshell is extracted, not suppressed" \
    "echo x >(( : > ${TEST_ROOT}/bug3-procsub.env ))" "$(printf ':\n%s' "${TEST_ROOT}/bug3-procsub.env")"

# Vector 2: "[[" is a bash reserved word recognized only in genuine COMMAND
# POSITION -- not merely "preceded by whitespace". Confirmed exploit:
# `echo x [[ z > AGENTS.md ]] b` is, in real bash, just `echo` with a
# genuine redirect to AGENTS.md.
echo "case: [[ preceded by a plain word (not command position) is never treated as a region opener (Bug 3 Vector 2 regression)"
assert_targets "echo x [[ z > AGENTS.md ]] b -- [[ preceded by the plain word \"x\" is not command position, real redirect extracted" \
    "echo x [[ z > ${TEST_ROOT}/bug3-notcmdpos.md ]] b" "${TEST_ROOT}/bug3-notcmdpos.md"

# Vector 3: bash terminates a "[[ ... ]]" conditional at the FIRST standalone
# "]]" token, it does NOT balance nested "["/"]" characters the way
# find_close's depth-counting genuinely models "(( ))" nesting. Confirmed
# exploit: `[[ [[ ]] ; echo x > AGENTS.md ]]` is valid bash where
# `[[ [[ ]]` is itself a complete (if degenerate) conditional and
# `echo x > AGENTS.md` afterward really executes the write.
echo "case: the first standalone ]] closes [[, not the last ]] in the text (Bug 3 Vector 3 regression)"
assert_targets "[[ [[ ]] ; echo x > AGENTS.md ]] -- the real redirect between the two conditionals is extracted, not suppressed" \
    "[[ [[ ]] ; echo x > ${TEST_ROOT}/bug3-firstclose.md ]]" "${TEST_ROOT}/bug3-firstclose.md"

# Non-regression: a real redirect AFTER a genuine [[ ... ]] region is still
# extracted, and the POSIX character class [[:alpha:]]'s glued "]]" is
# never mistaken for the standalone terminator (only the final, whitespace-
# preceded " ]]" is).
echo "case: [[ \$x =~ [[:alpha:]] ]] > /repo/out still extracts the real redirect after the region (Bug 3 Vector 3 non-regression)"
assert_targets "[[ \$x =~ [[:alpha:]] ]] > /repo/out extracts /repo/out, glued POSIX-class ]] is not the terminator" \
    "[[ \$x =~ [[:alpha:]] ]] > ${TEST_ROOT}/bug3-posixclass.out" "${TEST_ROOT}/bug3-posixclass.out"

echo "case: the bare [[ \$x =~ [[:alpha:]] ]] alone still emits zero targets (Bug 3 Vector 3 non-regression)"
assert_targets "bare [[ \$x =~ [[:alpha:]] ]] with no trailing redirect emits zero targets" \
    '[[ $x =~ [[:alpha:]] ]]' ""

# ── Ticket #810 round-3 stabilization review, Finding 1 [CRITICAL]:
# is_bracket_command_position() previously accepted "[[" preceded by a `)`,
# `{`, `}`, or bare reserved word (`if`/`then`/`do`/...) as "command
# position" without recursively checking THAT token's own position -- an
# open-ended, unbounded amount of shell-grammar replication this ticket is
# explicitly scoped to avoid. That accepted set is now dropped entirely: a
# "[[" (or "((") is only ever a real region opener when the character
# immediately preceding it (after skipping whitespace) is start-of-string,
# `;`, `&`, `|`, or a newline -- nothing else. Confirmed exploits below, all
# real bash writes that a wider accepted set previously suppressed.
echo "case: [[ preceded by a closing command-substitution ) is not command position -- the real redirect is extracted (Finding 1 regression)"
assert_targets "echo \$(date) [[ x > /repo/.env ]] > /tmp/ok -- both targets extracted, not suppressed" \
    "echo \$(date) [[ x > ${TEST_ROOT}/finding1-paren.env ]] > ${TEST_ROOT}/finding1-paren.ok" \
    "$(printf '%s\n%s' "${TEST_ROOT}/finding1-paren.env" "${TEST_ROOT}/finding1-paren.ok")"

echo "case: [[ preceded by the bare reserved word \"do\" (used as a plain argument here) is not command position -- the real redirect is extracted (Finding 1 regression)"
assert_targets "echo do [[ x > /repo/.env ]] > /tmp/ok -- both targets extracted, not suppressed" \
    "echo do [[ x > ${TEST_ROOT}/finding1-do.env ]] > ${TEST_ROOT}/finding1-do.ok" \
    "$(printf '%s\n%s' "${TEST_ROOT}/finding1-do.env" "${TEST_ROOT}/finding1-do.ok")"

echo "case: [[ preceded by a bare } (used as a plain argument here) is not command position -- the real redirect is extracted (Finding 1 regression)"
assert_targets "echo } [[ x > /repo/.env ]] > /tmp/ok -- both targets extracted, not suppressed" \
    "echo } [[ x > ${TEST_ROOT}/finding1-brace.env ]] > ${TEST_ROOT}/finding1-brace.ok" \
    "$(printf '%s\n%s' "${TEST_ROOT}/finding1-brace.env" "${TEST_ROOT}/finding1-brace.ok")"

echo "case: [[ preceded by a bare ! (used as a plain argument here) is not command position -- the real redirect is extracted (Finding 1 regression)"
assert_targets "echo ! [[ x > /repo/.env ]] > /tmp/ok -- both targets extracted, not suppressed" \
    "echo ! [[ x > ${TEST_ROOT}/finding1-bang.env ]] > ${TEST_ROOT}/finding1-bang.ok" \
    "$(printf '%s\n%s' "${TEST_ROOT}/finding1-bang.env" "${TEST_ROOT}/finding1-bang.ok")"

# Non-regressions (Finding 1 design directive): bare [[ / (( at genuine
# simple statement-start positions (start-of-string, or after `;`/`|`) must
# still resolve to zero targets -- the minimal, non-recursible allowlist
# still accepts these positions, it only stops accepting the wider,
# recursive ones above.
echo "case: bare [[ z > a ]] and (( z > a )) at start-of-string still resolve to zero targets (Finding 1 non-regression)"
assert_targets "bare [[ z > a ]] emits no write target" "[[ z > a ]]" ""
assert_targets "bare (( z > a )) emits no write target" "(( z > a ))" ""

echo "case: [[ z > a ]] preceded by ; still resolves to zero targets (Finding 1 non-regression)"
assert_targets "true ; [[ z > a ]] emits no write target" "true ; [[ z > a ]]" ""

echo "case: [[ z > a ]] preceded by | still resolves to zero targets (Finding 1 non-regression)"
assert_targets "true | [[ z > a ]] emits no write target" "true | [[ z > a ]]" ""

# ── Ticket #810 round-3 stabilization review, Finding 2 [HIGH]:
# find_close_bracket()'s post-"]]" delimiter set previously omitted `>`/`<`,
# even though real bash terminates the "]]" word at ANY unquoted
# metacharacter, including a glued redirect. A "]]" immediately glued to a
# redirect (`]]>`) was therefore wrongly rejected as the real closer, and the
# scan continued past it to a LATER standalone "]]", extending the
# suppressed region over the real redirect in between.
echo "case: a ]] immediately glued to a redirect (]]>) is recognized as the real closer, not skipped past (Finding 2 regression)"
assert_targets "[[ a ]]>/repo/out ; echo ]] > /tmp/ok -- the real redirect right after ]] is extracted, not suppressed" \
    "[[ a ]]>${TEST_ROOT}/finding2.out ; echo ]] > ${TEST_ROOT}/finding2.ok" \
    "$(printf '%s\n%s' "${TEST_ROOT}/finding2.out" "${TEST_ROOT}/finding2.ok")"

# ── Ticket #810 round-4 security review [CRITICAL]: is_opener_boundary()'s
# minimal accepted-preceding-char set ({start-of-string, ;, &, |, newline})
# was determined by a stateless backward substr() re-scan of the raw command
# text that (a) accepted the & / | TAIL of the compound redirect operators
# >&, <&, >| as if they were a bare separator/pipe, even though bash parses
# what follows one of those as an ordinary WORD, not a reserved [[ / ((
# opener, and (b) had no quote/escape awareness, so a backslash-escaped or
# quoted ;/&/| was wrongly accepted as a valid boundary. Both are fixed by
# moving boundary determination into mark_regions()'s own quote/escape-aware
# forward scan (lastsig/beforesig), consulted via is_boundary_char().
# ── round-5 review, Medium finding: the two cases below previously inserted
# a filename BETWEEN the >|/>& operator and [[ (e.g. "echo hi >| <path>
# [[ ..."), so `lastsig` at the [[ boundary check was that filename's last
# character, not `|`/`&` -- the beforesig == ">" branch of is_boundary_char
# was never actually exercised (these would pass identically against the
# pre-round-4 code too). Fixed to direct adjacency, matching the real PoC
# shape: `echo hi >| [[ x > <path> ]]` truncates/dups to a file literally
# NAMED "[[" (real bash: `>|`/`>&` redirect to whatever WORD follows), so
# both the literal word "[[" and the real `> <path>` redirect inside must be
# extracted as separate targets.
echo "case: >| does not make the following [[ a real opener -- both the literal [[ redirect target and the real redirect inside are extracted (round-4 regression, round-5 fixed adjacency)"
assert_targets "echo hi >| [[ x > /repo/.env ]] -- >| truncates to a file literally named [[, the redirect inside is real" \
    "echo hi >| [[ x > ${TEST_ROOT}/round4-pipe.env ]]" \
    "$(printf '%s\n%s' '[[' "${TEST_ROOT}/round4-pipe.env")"

echo "case: >& does not make the following [[ a real opener -- both the literal [[ redirect target and the real redirect inside are extracted (round-4 regression, round-5 fixed adjacency)"
assert_targets "echo hi >& [[ x > /repo/.env ]] -- >& redirects to a file literally named [[, the redirect inside is real" \
    "echo hi >& [[ x > ${TEST_ROOT}/round4-amp.env ]]" \
    "$(printf '%s\n%s' '[[' "${TEST_ROOT}/round4-amp.env")"

echo "case: <& does not make the following [[ a real opener -- the real redirect inside is extracted (round-4 regression)"
assert_targets "cat 0<& [[ x > /repo/.env ]] -- <& is an input-fd-dup operator, the redirect inside [[ ]] is still real" \
    "cat 0<& [[ x > ${TEST_ROOT}/round4-inamp.env ]]" \
    "${TEST_ROOT}/round4-inamp.env"

echo "case: a backslash-escaped ; does not count as a valid boundary -- the real redirect inside [[ ]] is extracted (round-4 regression)"
assert_targets "echo \; [[ x > /repo/.env ]] -- the escaped ; is a literal argument character, not a separator" \
    "echo \\; [[ x > ${TEST_ROOT}/round4-escsemi.env ]]" \
    "${TEST_ROOT}/round4-escsemi.env"

# Non-regression: every REAL separator/pipe form must still suppress the
# region exactly as before -- widening rejection of >&/<&/>| tails must not
# narrow acceptance of genuine && / || / |& / ;& / bare ; / bare & / bare |
# / start-of-string / newline boundaries.
echo "case: real separators still correctly suppress a following [[ / (( region (round-4 non-regression)"
assert_targets "true && [[ z > a ]] emits no write target" "true && [[ z > a ]]" ""
assert_targets "false || (( z > a )) emits no write target" "false || (( z > a ))" ""
assert_targets "true |& [[ z > a ]] emits no write target" "true |& [[ z > a ]]" ""
assert_targets "true ;& [[ z > a ]] emits no write target (degenerate but a real bash token pair)" "true ;& [[ z > a ]]" ""
assert_targets "true ; [[ z > a ]] emits no write target (bare unescaped ;)" "true ; [[ z > a ]]" ""
assert_targets "true & [[ z > a ]] emits no write target (bare unescaped &)" "true & [[ z > a ]]" ""
assert_targets "true | [[ z > a ]] emits no write target (bare unescaped |)" "true | [[ z > a ]]" ""
assert_targets "[[ z > a ]] at start-of-string emits no write target" "[[ z > a ]]" ""
NEWLINE_BOUNDARY_CMD="$(printf 'true\n[[ z > a ]]')"
assert_targets "[[ z > a ]] preceded by a newline emits no write target" "${NEWLINE_BOUNDARY_CMD}" ""

# ── Ticket #810 round-5 security review [CRITICAL]: region suppression
# swallows real writes hidden inside command/process substitution nested in
# [[ ]] / (( )). Bash performs command substitution (backticks, $(...)) and
# process substitution (<(...), >(...)) INSIDE both [[ ... ]] and (( ... )),
# so a redirect or `tee` invocation nested inside one of those is a real,
# executing write -- but mark_regions() previously suppressed every
# character position between a recognized opener and its matched closer
# unconditionally, including the substitution's own contents, so the real
# write inside was never extracted and never blocked. Fixed by re-scanning
# the region's INTERIOR for any unquoted substitution marker (backtick, `$(`,
# `<(`, `>(`) before committing to suppress it -- a region whose interior
# contains one is not suppressed at all.
echo "case: a real write hidden inside \$( ) nested in [[ ]] is extracted, not suppressed (round-5 regression)"
assert_targets "echo ok > .ok ; [[ \$(printf x > .env) ]] -- both the benign and the nested-substitution write are extracted" \
    "echo ok > ${TEST_ROOT}/round5-nested1.ok ; [[ \$(printf x > ${TEST_ROOT}/round5-nested1.env) ]]" \
    "$(printf '%s\n%s' "${TEST_ROOT}/round5-nested1.ok" "${TEST_ROOT}/round5-nested1.env")"

echo "case: a real write hidden inside \$( ) nested in (( )) is extracted, not suppressed (round-5 regression)"
assert_targets "echo ok > .ok ; (( \$(printf x > .env; echo 1) > 0 )) -- both the benign and the nested-substitution write are extracted" \
    "echo ok > ${TEST_ROOT}/round5-nested2.ok ; (( \$(printf x > ${TEST_ROOT}/round5-nested2.env; echo 1) > 0 ))" \
    "$(printf '%s\n%s\n%s' "${TEST_ROOT}/round5-nested2.ok" "${TEST_ROOT}/round5-nested2.env" "0")"

echo "case: a tee operand hidden inside \$( ) nested in [[ ]] arms tee and is extracted (round-5 regression -- exercises the !wregion tee-arming suppression, not just >)"
assert_targets "echo ok > .ok ; [[ \$(echo p | tee .env) ]] -- the tee operand nested inside the substitution is extracted" \
    "echo ok > ${TEST_ROOT}/round5-nested3.ok ; [[ \$(echo p | tee ${TEST_ROOT}/round5-nested3.env) ]]" \
    "$(printf '%s\n%s' "${TEST_ROOT}/round5-nested3.ok" "${TEST_ROOT}/round5-nested3.env")"

echo "case: a backtick-wrapped write nested in [[ ]] is extracted, not suppressed (round-5 regression)"
assert_targets "[[ \`printf x > .env\` ]] -- the backtick-substitution write is extracted" \
    "[[ \`printf x > ${TEST_ROOT}/round5-backtick.env\` ]]" \
    "${TEST_ROOT}/round5-backtick.env"

echo "case: a write nested inside process substitution <( ) inside [[ ]] is extracted, not suppressed (round-5 regression)"
assert_targets "[[ -e <(printf x > .env) ]] -- the process-substitution write is extracted" \
    "[[ -e <(printf x > ${TEST_ROOT}/round5-procsub.env) ]]" \
    "${TEST_ROOT}/round5-procsub.env"

echo "case: backslash-newline removal is honored before unquoted command/process-substitution marker recognition"
CONTINUED_UNQUOTED_CMDSUB="$(printf '[[ $\\\n(printf x > %s) ]]' "${TEST_ROOT}/round5-continued-cmdsub.env")"
assert_targets "a continued \$( marker still exposes its nested write" \
    "${CONTINUED_UNQUOTED_CMDSUB}" \
    "${TEST_ROOT}/round5-continued-cmdsub.env"
CONTINUED_UNQUOTED_PROCSUB="$(printf '[[ -e <\\\n(printf x > %s) ]]' "${TEST_ROOT}/round5-continued-procsub.env")"
assert_targets "a continued <( marker still exposes its nested write" \
    "${CONTINUED_UNQUOTED_PROCSUB}" \
    "${TEST_ROOT}/round5-continued-procsub.env"

# Non-regression: every prior legitimate-region case with NO substitution
# inside must still correctly suppress its comparison operator.
echo "case: legitimate comparison regions with no nested substitution still correctly suppress (round-5 non-regression)"
assert_targets "[[ z > a ]] emits no write target" "[[ z > a ]]" ""
assert_targets "(( z > a )) emits no write target" "(( z > a ))" ""
assert_targets "(( 1 > 0 )) emits no write target" "(( 1 > 0 ))" ""
assert_targets "[[ \$x =~ [[:alpha:]] ]] emits no write target" '[[ $x =~ [[:alpha:]] ]]' ""

# ── Ticket #810 round-6 stabilization [HIGH]: has_nested_substitution() did
# not recognize bash 5.3+'s function substitution forms `${ cmd; }` /
# `${| cmd; }`, which execute a real command from inside a [[ ]]/(( ))
# construct just like `$(...)`. A region containing one was previously
# suppressed as an ordinary comparison, so a real write nested inside was
# never extracted and never blocked. Fixed by additionally recognizing `${`
# followed by whitespace or `|` (which distinguishes a funsub from an
# ordinary parameter expansion like `${var}`/`${x:-default}`, neither of
# which executes arbitrary commands) as a substitution marker.
echo "case: a real write hidden inside \${ cmd; } funsub nested in [[ ]] is extracted, not suppressed (round-6 regression)"
assert_targets "echo ok > .ok ; [[ -n \${ printf x > .env; } ]] -- both the decoy and the nested-funsub write are extracted" \
    "echo ok > ${TEST_ROOT}/round6-funsub1.ok ; [[ -n \${ printf x > ${TEST_ROOT}/round6-funsub1.env; } ]]" \
    "$(printf '%s\n%s' "${TEST_ROOT}/round6-funsub1.ok" "${TEST_ROOT}/round6-funsub1.env")"

echo "case: backslash-newline removal is honored around an unquoted function-substitution marker"
CONTINUED_UNQUOTED_FUNSUB="$(printf '[[ -n $\\\n{\\\n printf x > %s; } ]]' "${TEST_ROOT}/round6-continued-funsub.env")"
assert_targets "continued \${ plus continued post-brace whitespace still exposes the nested write" \
    "${CONTINUED_UNQUOTED_FUNSUB}" \
    "${TEST_ROOT}/round6-continued-funsub.env"

echo "case: legitimate parameter expansion \${x} / \${y} (no space/pipe after \${) still correctly suppresses (round-6 non-regression)"
assert_targets "[[ \${x} > \${y} ]] emits no write target" '[[ ${x} > ${y} ]]' ""

echo "case: a real write hidden inside \${| cmd; } value-substitution funsub nested in [[ ]] is extracted, not suppressed (round-6 regression)"
assert_targets "echo ok > .ok ; [[ -n \${| printf x > .env; } ]] -- both the decoy and the nested-funsub write are extracted" \
    "echo ok > ${TEST_ROOT}/round6-funsub-pipe.ok ; [[ -n \${| printf x > ${TEST_ROOT}/round6-funsub-pipe.env; } ]]" \
    "$(printf '%s\n%s' "${TEST_ROOT}/round6-funsub-pipe.ok" "${TEST_ROOT}/round6-funsub-pipe.env")"

echo "case: a real write hidden inside \${ cmd; } funsub nested in (( )) is extracted, not suppressed (round-6 regression)"
assert_targets "(( \${ printf 1 > .env; } > 0 )) -- both the nested-funsub write and the region's own unsuppressed > 0 are extracted" \
    "(( \${ printf 1 > ${TEST_ROOT}/round6-funsub-arith.env; } > 0 ))" \
    "$(printf '%s\n%s' "${TEST_ROOT}/round6-funsub-arith.env" "0")"

# ── Ticket #810 stabilization: a command/process/function substitution
# marker found INSIDE a double-quoted span within a [[ ]]/(( )) region's
# interior is treated as a deliberately blunt, maximally conservative
# unsupported construct: bash lets the substitution escape the enclosing
# double quote for its own real syntax, but this tokenizer's own
# double-quote handling has no safe way to resume precise parsing through
# that mid-scan. Earlier attempts at a precise resume-parsing mechanism
# (tracking the substitution's real close position and forcing quote state
# back to double-quoted afterward) repeatedly introduced new bugs across
# several review rounds, so rather than continue chasing them, the WHOLE
# extraction now fails closed unconditionally with exit code 7 whenever such
# a marker is found -- blocking the whole command rather than attempting to
# precisely resolve what is inside it. The same marker in UNQUOTED text is
# unaffected: it is already correctly handled by the tokenizer's ordinary
# unquoted dispatch (see the round-5/6 cases above), with no special
# mechanism needed.
echo "case: a real write hidden inside a double-quoted \$( ) command substitution nested in [[ ]] fails closed (exit 7)"
assert_extract_exit "[[ -n \"\$(printf x > .env)\" ]] -- the double-quoted \$( ) substitution fails the whole extraction closed" \
    "[[ -n \"\$(printf x > ${TEST_ROOT}/round7-dq-cmdsub.env)\" ]]" \
    7

echo "case: a backslash-newline cannot hide a double-quoted \$( ) command substitution nested in [[ ]]"
DQ_CONTINUED_CMDSUB=$'[[ -n "$\\\n(printf x > .env)" ]]'
assert_extract_exit "Bash removes backslash-newline before recognizing the double-quoted command substitution, so extraction fails closed" \
    "${DQ_CONTINUED_CMDSUB}" \
    7

echo "case: a real write hidden inside a double-quoted \$( ) nested in (( )) fails closed (exit 7)"
assert_extract_exit "(( \"\$(printf x > .env; echo 1)\" > 0 )) -- the double-quoted substitution fails the whole extraction closed" \
    "(( \"\$(printf x > ${TEST_ROOT}/round7-dq-arith.env; echo 1)\" > 0 ))" \
    7

echo "case: a double-quoted backtick-wrapped write nested in [[ ]] fails closed (exit 7)"
assert_extract_exit "[[ -n \"\`printf x > .env\`\" ]] -- the double-quoted backtick-substitution fails the whole extraction closed" \
    "[[ -n \"\`printf x > ${TEST_ROOT}/round7-dq-backtick.env\`\" ]]" \
    7

echo "case: a double-quoted \${ cmd; } funsub write nested in [[ ]] fails closed (exit 7)"
assert_extract_exit "[[ -n \"\${ printf x > .env; }\" ]] -- the double-quoted funsub fails the whole extraction closed" \
    "[[ -n \"\${ printf x > ${TEST_ROOT}/round7-dq-funsub.env; }\" ]]" \
    7

echo "case: a doubly-nested double-quoted command substitution nested in [[ ]] fails closed (exit 7)"
assert_extract_exit "[[ -n \"\$(printf '%s' \"\$(echo y)\" > .env)\" ]] -- the outer double-quoted \$( ) fails the whole extraction closed regardless of its own nested content" \
    "[[ -n \"\$(printf '%s' \"\$(echo y)\" > ${TEST_ROOT}/round7-dq-doublenest.env)\" ]]" \
    7

echo "case: a single-quoted nested-substitution-shaped string remains fully inert -- the region is still correctly suppressed as an ordinary comparison (non-regression)"
assert_targets "[[ '\$(printf x > /repo/.env)' > /repo/other ]] emits no write target -- single quotes suppress everything, including command substitution" \
    "[[ '\$(printf x > /repo/.env)' > /repo/other ]]" \
    ""

echo "case: a plain double-quoted comparison with no nested substitution still correctly suppresses (non-regression)"
assert_targets "[[ \"a\" > \"b\" ]] emits no write target" \
    "[[ \"a\" > \"b\" ]]" \
    ""

# ── Fix D (should-fix, #795 round 2): an oversized command must fail closed
# with a distinct exit code (4), not be confused with "awk not found" (3).
echo "case: bwt_extract_targets fails closed with a distinct exit code when the command exceeds the length threshold (Fix D regression)"
LONG_CMD_OVER_THRESHOLD="$(printf '%*s' 100005 '' | tr ' ' 'a')"
assert_extract_exit "command over the length threshold returns exit 4" "${LONG_CMD_OVER_THRESHOLD}" 4

LONG_CMD_UNDER_THRESHOLD="echo $(printf '%*s' 99000 '' | tr ' ' 'a') > ${TEST_ROOT}/under-threshold.txt"
assert_extract_exit "command comfortably under the length threshold is unaffected" "${LONG_CMD_UNDER_THRESHOLD}" 0
assert_targets "command comfortably under the length threshold still extracts its target" \
    "${LONG_CMD_UNDER_THRESHOLD}" "${TEST_ROOT}/under-threshold.txt"

# ── bwt_is_exempt_device ─────────────────────────────────────────────
echo "case: bwt_is_exempt_device"
assert_true "/dev/null is exempt" bwt_is_exempt_device "/dev/null"
assert_true "/dev/stdout is exempt" bwt_is_exempt_device "/dev/stdout"
assert_true "/dev/stderr is exempt" bwt_is_exempt_device "/dev/stderr"
assert_true "/dev/fd/0 is exempt" bwt_is_exempt_device "/dev/fd/0"
assert_true "/dev/fd/99 is exempt" bwt_is_exempt_device "/dev/fd/99"
assert_false "/dev/random is not exempt" bwt_is_exempt_device "/dev/random"
assert_false "a sensitive path is not exempt" bwt_is_exempt_device "${TEST_ROOT}/.env"

# ── Fix 1 (security): /dev/fd/../ path traversal must NOT be exempt ────
# A raw prefix-glob match against the un-canonicalized target let a
# traversal payload masquerade as an exempt /dev/fd/N device, bypassing
# every downstream check. Only a purely-numeric fd suffix is exempt.
echo "case: /dev/fd/../ traversal payloads are not exempt (Fix 1 regression)"
assert_false "/dev/fd/../../../etc/passwd is not exempt" bwt_is_exempt_device "/dev/fd/../../../etc/passwd"
assert_false "/dev/fd/../.. is not exempt" bwt_is_exempt_device "/dev/fd/../.."
assert_false "/dev/fd/0extra (non-numeric suffix) is not exempt" bwt_is_exempt_device "/dev/fd/0extra"
assert_false "/dev/fd/ (empty suffix) is not exempt" bwt_is_exempt_device "/dev/fd/"

# ── bwt_expand_safe_vars: literal path unaffected ────────────────────
echo "case: bwt_expand_safe_vars leaves a plain literal path unchanged"
assert_expand "literal path" "${TEST_ROOT}/plain.txt" "${TEST_ROOT}/plain.txt"

# ── bwt_expand_safe_vars: \${TMPDIR:-/tmp} ───────────────────────────
echo "case: bwt_expand_safe_vars \${TMPDIR:-/tmp}"
assert_expand_with_tmpdir "unset TMPDIR falls back to /tmp" "__unset__" \
    '${TMPDIR:-/tmp}/cenci/x.md' "/tmp/cenci/x.md"
assert_expand_with_tmpdir "valid TMPDIR is used" "${VALID_TMPDIR}" \
    '${TMPDIR:-/tmp}/cenci/x.md' "${VALID_TMPDIR}/cenci/x.md"
assert_expand_with_tmpdir "TMPDIR set but non-existent dir is unresolved (left unexpanded)" "${NONEXISTENT_TMPDIR}" \
    '${TMPDIR:-/tmp}/cenci/x.md' '${TMPDIR:-/tmp}/cenci/x.md'
assert_expand_with_tmpdir "TMPDIR set but relative is unresolved (left unexpanded)" "relative-tmp" \
    '${TMPDIR:-/tmp}/cenci/x.md' '${TMPDIR:-/tmp}/cenci/x.md'

# ── bwt_expand_safe_vars: \$TMPDIR / \${TMPDIR} (no fallback) ────────
echo "case: bwt_expand_safe_vars \$TMPDIR / \${TMPDIR}"
assert_expand_with_tmpdir "unset TMPDIR: \$TMPDIR is unresolved (left unexpanded)" "__unset__" \
    '$TMPDIR/x' '$TMPDIR/x'
assert_expand_with_tmpdir "valid TMPDIR: \$TMPDIR expands" "${VALID_TMPDIR}" \
    '$TMPDIR/x' "${VALID_TMPDIR}/x"
assert_expand_with_tmpdir "valid TMPDIR: \${TMPDIR} expands" "${VALID_TMPDIR}" \
    '${TMPDIR}/x' "${VALID_TMPDIR}/x"
assert_expand_with_tmpdir "non-existent TMPDIR: \$TMPDIR is unresolved" "${NONEXISTENT_TMPDIR}" \
    '$TMPDIR/x' '$TMPDIR/x'

# ── bwt_expand_safe_vars: \$HOME / \${HOME} ──────────────────────────
echo "case: bwt_expand_safe_vars \$HOME / \${HOME}"
assert_expand_with_var "unset HOME: \$HOME is unresolved" HOME "__unset__" '$HOME/x' '$HOME/x'
assert_expand_with_var "set absolute HOME: \$HOME expands" HOME "${TEST_ROOT}/home" '$HOME/x' "${TEST_ROOT}/home/x"
assert_expand_with_var "set absolute HOME: \${HOME} expands" HOME "${TEST_ROOT}/home" '${HOME}/x' "${TEST_ROOT}/home/x"

# ── bwt_expand_safe_vars: \$PWD / \${PWD} ────────────────────────────
echo "case: bwt_expand_safe_vars \$PWD / \${PWD}"
assert_expand_with_var "unset PWD: \$PWD is unresolved" PWD "__unset__" '$PWD/x' '$PWD/x'
assert_expand_with_var "set absolute PWD: \$PWD expands" PWD "${TEST_ROOT}/cwd" '$PWD/x' "${TEST_ROOT}/cwd/x"
assert_expand_with_var "set absolute PWD: \${PWD} expands" PWD "${TEST_ROOT}/cwd" '${PWD}/x' "${TEST_ROOT}/cwd/x"

# ── bwt_expand_safe_vars: unrecognized \$VAR is left unresolved ─────
echo "case: bwt_expand_safe_vars leaves an unrecognized \$VAR unresolved"
assert_expand "unknown var untouched" '$FOO/bar' '$FOO/bar'

# ── bwt_is_unresolved ─────────────────────────────────────────────────
echo "case: bwt_is_unresolved"
assert_false "a clean absolute path is resolved" bwt_is_unresolved "${TEST_ROOT}/clean.txt"
assert_true "a dollar-sign target is unresolved" bwt_is_unresolved '$FOO/bar'
assert_true "a command-substitution target is unresolved" bwt_is_unresolved '$(mktemp)'
assert_true "a backtick target is unresolved" bwt_is_unresolved '`mktemp`'
assert_true "a parenthesized target is unresolved" bwt_is_unresolved 'a(b)c'
assert_true "an empty target is unresolved" bwt_is_unresolved ""
NEWLINE_TARGET="$(printf 'a\nb')"
assert_true "a target containing a newline is unresolved" bwt_is_unresolved "${NEWLINE_TARGET}"

# ── Fix 4 (performance): extraction must be near-linear, not quadratic ──
# The prior character-at-a-time `${_bwt_rest#?}` shell scan measured ~33.5s
# on a 5000-char command. A single-pass awk state machine should complete a
# comparably-sized command in well under a second; a generous 2s threshold
# avoids flakiness while still proving the quadratic blowup is gone.
echo "case: extraction on a large synthetic command completes well under 1s (Fix 4 regression)"
PERF_CMD="echo"
PERF_WORD_COUNT=700
for ((PERF_I = 0; PERF_I < PERF_WORD_COUNT; PERF_I++)); do
    PERF_CMD="${PERF_CMD} aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
done
PERF_TARGET="${TEST_ROOT}/perf-target.txt"
PERF_CMD="${PERF_CMD} > ${PERF_TARGET}"
echo "  (synthetic command length: ${#PERF_CMD} chars)"

PERF_START_NS="$(date +%s%N)"
PERF_ACTUAL="$(bwt_extract_targets "${PERF_CMD}")"
PERF_END_NS="$(date +%s%N)"
PERF_ELAPSED_MS=$(( (PERF_END_NS - PERF_START_NS) / 1000000 ))
echo "  (elapsed: ${PERF_ELAPSED_MS}ms)"

if [[ "${PERF_ACTUAL}" == "${PERF_TARGET}" ]]; then
    pass
else
    fail "large command extraction: got <${PERF_ACTUAL}>, expected <${PERF_TARGET}>"
fi

if [[ "${PERF_ELAPSED_MS}" -lt 2000 ]]; then
    pass
else
    fail "large command extraction took ${PERF_ELAPSED_MS}ms, expected well under 2000ms (near-linear, not quadratic)"
fi

# ── awk-missing fail-closed (Fix 4) ─────────────────────────────────────
# bwt_extract_targets must fail closed (non-zero exit, no output) when awk
# is unavailable, mirroring the lib-missing fail-closed convention already
# used by both guard scripts for a missing bash-write-targets.sh itself.
echo "case: bwt_extract_targets fails closed when awk is unavailable"
AWK_MISSING_BIN="${TEST_ROOT}/bin-no-awk"
mkdir -p "${AWK_MISSING_BIN}"
NO_AWK_OUT_FILE="${TEST_ROOT}/bwt-no-awk-out.txt"
NO_AWK_EXIT=0
PATH="${AWK_MISSING_BIN}" bwt_extract_targets "echo x > ${TEST_ROOT}/a.txt" >"${NO_AWK_OUT_FILE}" 2>/dev/null || NO_AWK_EXIT=$?
NO_AWK_OUT="$(cat "${NO_AWK_OUT_FILE}")"
if [[ "${NO_AWK_EXIT}" -ne 0 ]]; then
    pass
else
    fail "bwt_extract_targets with no awk on PATH: expected non-zero exit, got 0"
fi
if [[ -z "${NO_AWK_OUT}" ]]; then
    pass
else
    fail "bwt_extract_targets with no awk on PATH: expected no output, got <${NO_AWK_OUT}>"
fi

# ── wc-missing fail-closed (Fix 3, #795 round 3) ────────────────────────
# The length pre-check depends on `wc`; a missing `wc` must fail closed with
# its own distinct exit code (5), never silently collapse the computed
# length to 0 (which would be misread as "under threshold" and let the
# command through to awk unchecked). PATH is curated to include awk (so the
# awk-missing check at the top of the function passes) but deliberately
# excludes wc, isolating this specific check.
echo "case: bwt_extract_targets fails closed with a distinct exit code when wc is unavailable"
WC_MISSING_BIN="${TEST_ROOT}/bin-no-wc"
mkdir -p "${WC_MISSING_BIN}"
REAL_AWK="$(command -v awk)" || {
    echo "test setup: awk not found on PATH" >&2
    exit 1
}
ln -s "${REAL_AWK}" "${WC_MISSING_BIN}/awk"
NO_WC_OUT_FILE="${TEST_ROOT}/bwt-no-wc-out.txt"
NO_WC_EXIT=0
PATH="${WC_MISSING_BIN}" bwt_extract_targets "echo x > ${TEST_ROOT}/a.txt" >"${NO_WC_OUT_FILE}" 2>/dev/null || NO_WC_EXIT=$?
NO_WC_OUT="$(cat "${NO_WC_OUT_FILE}")"
if [[ "${NO_WC_EXIT}" -eq 5 ]]; then
    pass
else
    fail "bwt_extract_targets with no wc on PATH: expected exit 5, got ${NO_WC_EXIT}"
fi
if [[ -z "${NO_WC_OUT}" ]]; then
    pass
else
    fail "bwt_extract_targets with no wc on PATH: expected no output, got <${NO_WC_OUT}>"
fi

# ── Summary ──────────────────────────────────────────────────────────
echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
