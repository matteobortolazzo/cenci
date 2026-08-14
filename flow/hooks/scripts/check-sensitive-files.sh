#!/bin/sh
# PreToolUse hook: warn before writing to sensitive files (env files,
# credentials/secrets/keys, SSH/keystore files). Runs unconditionally in
# every repo — unlike guard-main-worktree.sh, this hook does not gate on
# cenci configuration.
#
# Hardening: the file path is extracted via jq's field-scoped
# '.tool_input.file_path' with a fallback to '.tool_input.filePath' against
# properly parsed JSON (not a raw-text grep/sed), so a lookalike
# "file_path"-shaped substring in an unrelated field (e.g. old_string) can no
# longer be mistaken for the real path. The fallback is driven by an explicit
# emptiness check (not jq's bare '//', which only falls back on null/false,
# not on an empty string) so a present-but-empty file_path correctly falls
# through to filePath instead of short-circuiting to the empty-path allow
# below. Missing jq, a JSON parse failure, a non-absolute file_path, or the
# absence of both realpath and readlink -f each fail closed (block) rather
# than silently allowing the write.
#
# The extracted path is canonicalized (symlinks resolved, "."/".." segments
# lexically collapsed) using the same parent-anchored resolver as
# guard-main-worktree.sh. The blocklist case statement is then run against
# BOTH the raw file_path and the canonicalized path — a match on either
# blocks (union, not canonical-only). This is deliberate: canonical-only
# matching would let a symlink literally named e.g. ".env" that resolves to
# a benign target slip through, regressing today's raw-name behavior; raw-
# only matching would miss a benign-looking name that resolves to a
# sensitive canonical target. The union preserves today's raw-name blocking
# while adding canonical-path protection, with no regressions either way.
#
# Second input mode (#795): when tool_input.file_path/filePath are both
# absent but tool_input.command is non-empty (a Bash tool call), the same
# blocklist is applied to every `>`/`>>`/`>|`/`tee` write target found in the
# command, extracted via the shared lib/bash-write-targets.sh tokenizer.
# This mode runs unconditionally in every repo too, matching the
# Write|Edit arm's unconditional behavior (ticket #795, Q2). The tokenizer
# is a precision layer, not a completeness proof: when it extracts ZERO
# targets from a command that still looks like it might write, the
# empty-parse backstop scans the raw command text for this guard's markers
# (check_sensitive_raw) and blocks on a hit — an unmodelled shell construct
# costs a false positive, never a silent permit. See the lib header's
# "Threat model and accepted residuals" block for the coverage boundary. A
# recognized, strictly-shaped apply_patch envelope (#1036, recognized via
# bwt_is_apply_patch_payload/bwt_apply_patch_targets) branches onto its own
# declared Add/Update/Delete/Move targets before the tokenizer ever sees the
# diff body, and suppresses this backstop's raw-text marker scan in that
# case — see the Bash arm below.

command -v jq >/dev/null 2>&1 || {
  echo "BLOCKED: jq is required by check-sensitive-files.sh but was not found on PATH." >&2
  exit 2
}

INPUT=$(cat)

# mktemp-failure fallback (#550): if a temp file can be created, jq's stderr
# is captured there (fd 9 points at it) so the normal-path message keeps its
# exact inline-detail format. If mktemp fails (e.g. TMPDIR exhaustion), fd 9
# is instead dup'd straight to the script's own stderr — /dev/null is never
# assigned to JQ_ERR, so it can never be handed to `rm` (which as root would
# unlink the /dev/null device node), and jq's diagnostic is never dropped.
if JQ_ERR=$(mktemp 2>/dev/null); then
  exec 9>"$JQ_ERR"
else
  JQ_ERR=""
  exec 9>&2
  echo "check-sensitive-files.sh: warning: mktemp failed; jq errors are written directly to stderr below" >&2
fi

# jq_err_detail — the jq diagnostic text to interpolate into a BLOCKED
# message: the captured temp-file content when one exists, else a pointer to
# the raw jq stderr already printed above (fd 9 passthrough case).
jq_err_detail() {
  if [ -n "$JQ_ERR" ]; then
    cat "$JQ_ERR" 2>/dev/null
  else
    echo "(see the jq error printed on stderr above)"
  fi
}

# jq_err_cleanup — close fd 9 and remove the temp file, only when one exists.
# Always returns 0 on success: an `if` whose final command is unconditional,
# not a `&&`-chain whose truth value would become the function's return code
# (which would make the no-temp-file case return 1 even though cleanup
# succeeded — harmless today since no caller uses `set -e`, but latent).
jq_err_cleanup() {
  exec 9>&-
  if [ -n "$JQ_ERR" ]; then
    rm -f "$JQ_ERR"
  fi
}

# Dual-mode extraction (#795): a single jq call extracts EITHER the
# Write|Edit file_path/filePath field OR, when neither is present, the Bash
# tool_input.command string. Two logical values are printed, one per line:
# line 1 is the resolved file_path/filePath (or empty string), and every
# remaining line (2 onward, rejoined with newlines) is tool_input.command (or
# empty) — rejoining rather than taking a single line 2 means an embedded
# literal newline inside a multi-line Bash command is never truncated.
# Explicit emptiness checks throughout (never bare '//', which only falls
# back on null/false, not on an empty string — flow/docs/shell-scripting-
# gotchas.md rule 2).
if ! JQ_OUT=$(printf '%s' "$INPUT" | jq -r '
    (if (.tool_input.file_path // "") != "" then
       .tool_input.file_path
     elif (.tool_input.filePath // "") != "" then
       .tool_input.filePath
     else
       ""
     end),
    (.tool_input.command // "")
  ' 2>&9); then
  echo "BLOCKED: check-sensitive-files.sh could not parse the tool call's JSON input: $(jq_err_detail)" >&2
  jq_err_cleanup
  exit 2
fi
jq_err_cleanup

# Split JQ_OUT's two logical values without depending on sed (kept out of
# this hook's tool dependency footprint): `read` (a shell builtin) peels off
# line 1, then `cat` passes the remainder straight through, preserving any
# embedded newlines in a multi-line Bash command.
FILE_PATH=$(printf '%s\n' "$JQ_OUT" | { IFS= read -r JQ_LINE1; printf '%s' "$JQ_LINE1"; })
TOOL_COMMAND=$(printf '%s\n' "$JQ_OUT" | { IFS= read -r JQ_DISCARD; cat; })

[ -z "$FILE_PATH" ] && [ -z "$TOOL_COMMAND" ] && exit 0

# Detect a path resolver able to canonicalize an *existing* path. Prefer
# realpath; fall back to GNU readlink -f (probed via a known-existing path so
# BSD/other readlink implementations that lack -f are rejected here, not at
# use time). Neither available → fail closed.
if command -v realpath >/dev/null 2>&1; then
  resolve_path() { realpath "$1"; }
elif readlink -f / >/dev/null 2>&1; then
  resolve_path() { readlink -f "$1"; }
else
  echo "BLOCKED: check-sensitive-files.sh requires realpath or 'readlink -f' to canonicalize paths, neither was found on PATH." >&2
  exit 2
fi

# Lexically collapse "." and ".." segments in an absolute path, without
# touching the filesystem. Used both as the sole normalization for
# not-yet-existing paths and as a final cleanup pass after a parent-anchored
# resolve (whose re-appended tail may itself contain traversal segments).
lexical_collapse() {
  path="$1"
  # Split on '/', rebuild collapsing '.' and '..' against the accumulated stack.
  # Disable globbing for the unquoted split so path segments containing glob
  # metacharacters (*, ?, [) are never pathname-expanded.
  old_ifs=$IFS
  IFS='/'
  set -f
  # shellcheck disable=SC2086 # intentional IFS='/' word-splitting to
  # enumerate path segments; globbing is disabled via `set -f` above.
  set -- $path
  set +f
  IFS=$old_ifs
  result=""
  for seg in "$@"; do
    case "$seg" in
      "" | ".") ;;
      "..")
        result="${result%/*}"
        ;;
      *)
        result="$result/$seg"
        ;;
    esac
  done
  [ -z "$result" ] && result="/"
  printf '%s\n' "$result"
}

# canonicalize_path <abs-path> — parent-anchored canonicalization shared by
# both arms below: prints the canonical form (symlinks resolved via the
# nearest existing ancestor, "."/".." collapsed) to stdout and returns 0.
# Only ever feeds *existing* paths to the resolver, sidestepping
# realpath/readlink -f divergence on non-existent trailing components. When
# the path itself does not exist, walks up toward / to find the nearest
# *existing* ancestor — not just the immediate parent — so a symlink several
# levels above a not-yet-existing tail still gets resolved ("/" always
# exists, so the walk terminates). Returns 1 (prints nothing) when a path
# component is a symlink node ([ -L ], no dereference) that does not resolve
# to an existing target ([ -e ] false — dangling target or ELOOP cycle), or
# when resolve_path itself fails; callers must fail closed (block) on that.
# Mirrors guard-main-worktree.sh's canonicalize_target.
canonicalize_path() {
  _cp_path="$1"
  if [ -e "$_cp_path" ]; then
    resolve_path "$_cp_path" 2>/dev/null || return 1
    return 0
  fi
  _cp_ancestor="$_cp_path"
  _cp_tail=""
  while [ -n "$_cp_ancestor" ] && [ ! -d "$_cp_ancestor" ]; do
    if [ -L "$_cp_ancestor" ] && [ ! -e "$_cp_ancestor" ]; then
      return 1
    fi
    _cp_seg="${_cp_ancestor##*/}"
    if [ -z "$_cp_tail" ]; then
      _cp_tail="$_cp_seg"
    else
      _cp_tail="$_cp_seg/$_cp_tail"
    fi
    _cp_ancestor="${_cp_ancestor%/*}"
  done
  [ -z "$_cp_ancestor" ] && _cp_ancestor="/"
  _cp_resolved_ancestor=$(resolve_path "$_cp_ancestor" 2>/dev/null) || return 1
  lexical_collapse "$_cp_resolved_ancestor/$_cp_tail"
}

# Check against sensitive file patterns. Run against BOTH the raw path and
# the canonicalized path — block (exit 2) if either matches (see header
# comment for the union-matching rationale). Shared by both arms below.
check_sensitive() {
  case "$1" in
    *.env|*.env.*|*/.env|*/.env.*)
      echo "BLOCKED: Refusing to write to environment file: $1" >&2
      echo "Environment files may contain secrets. Edit manually if needed." >&2
      exit 2
      ;;
    *credentials*|*secrets*|*secret.*|*.pem|*.key|*.pfx|*.p12)
      echo "BLOCKED: Refusing to write to sensitive file: $1" >&2
      echo "This file may contain credentials or keys. Edit manually if needed." >&2
      exit 2
      ;;
    *id_rsa*|*id_ed25519*|*id_ecdsa*|*.keystore|*.jks)
      echo "BLOCKED: Refusing to write to key file: $1" >&2
      echo "This file contains cryptographic keys. Edit manually if needed." >&2
      exit 2
      ;;
  esac
}

# check_sensitive_raw <raw-command> — the empty-parse backstop's marker scan
# (#795): applied to a whole Bash command string, not a single path, when the
# tokenizer saw a write candidate but extracted ZERO targets (the observable
# proxy for "we did not understand this command" — e.g. brace expansion like
# `{tee,cat} /repo/.env`). Blocks (exit 2) if the raw text mentions any
# sensitive marker anywhere.
#
# Deliberately SUBSTRING-form markers, not check_sensitive's whole-path
# globs: those are anchored for path endings — `*.env` matches a string
# *ending* in .env, so it would hit `{tee,cat} /repo/.env` but miss
# `tee /repo/.env && echo ok`. Each marker below is the substring core of a
# check_sensitive pattern; the two lists MUST stay in sync — every
# check_sensitive pattern needs a covering marker here, pinned by the
# marker-sync contract test in check-sensitive-files.test.sh.
#
# Over-blocking is the accepted cost: a command the tokenizer cannot model
# that merely *mentions* a marker (e.g. `-> .env` inside a quoted string) is
# blocked. The alternative — zero targets silently means allow — is the
# fail-open path #795's refinement forbids ("neither guard may gain a
# fail-open path"); a false positive here can be rephrased around, a silent
# permit cannot.
check_sensitive_raw() {
  case "$1" in
    *.env*)
      _csr_marker=".env"
      ;;
    *credentials*|*secrets*|*secret.*|*.pem*|*.key*|*.pfx*|*.p12*)
      _csr_marker="credentials/secrets/key-file"
      ;;
    *id_rsa*|*id_ed25519*|*id_ecdsa*|*.keystore*|*.jks*)
      _csr_marker="cryptographic-key-file"
      ;;
    *)
      return 0
      ;;
  esac
  {
    echo "BLOCKED: check-sensitive-files.sh could not extract this Bash command's write targets (unmodelled shell construct), and the raw command text mentions a sensitive-file marker (${_csr_marker}): $1"
    echo "Rewrite the command using a plain, directly-parseable redirect (>, >>) or tee form — or edit the file manually if needed."
  } >&2
  exit 2
}

if [ -n "$FILE_PATH" ]; then
  case "$FILE_PATH" in
    /*) ;;
    *)
      echo "BLOCKED: check-sensitive-files.sh received a non-absolute file_path: $FILE_PATH" >&2
      exit 2
      ;;
  esac

  # Parent-anchored canonicalization via the shared canonicalize_path
  # (see its definition above) — fails closed on an unresolvable path or an
  # ancestor component that is a dangling/looping symlink.
  if ! CANONICAL_PATH=$(canonicalize_path "$FILE_PATH"); then
    echo "BLOCKED: check-sensitive-files.sh refusing to resolve $FILE_PATH: the path could not be canonicalized (a component is a symlink that does not resolve — dangling target or symlink loop — or the resolver failed)." >&2
    exit 2
  fi

  check_sensitive "$FILE_PATH"
  check_sensitive "$CANONICAL_PATH"

  exit 0
elif [ -n "$TOOL_COMMAND" ]; then
  # Bash arm (#795): tool_input.command redirection/tee write targets are
  # extracted via the shared lib and checked against the same blocklist as
  # the Write|Edit arm above. Runs unconditionally (Q2) -- no config gate.
  LIB_DIR="${0%/*}"
  [ "$LIB_DIR" = "$0" ] && LIB_DIR="."
  BWT_LIB="$LIB_DIR/lib/bash-write-targets.sh"
  if [ ! -r "$BWT_LIB" ]; then
    echo "BLOCKED: check-sensitive-files.sh could not find its required helper library ($BWT_LIB) to inspect this Bash command's write targets." >&2
    exit 2
  fi
  # shellcheck source=./lib/bash-write-targets.sh
  . "$BWT_LIB"

  # apply_patch envelope recognition (#1036): branches BEFORE the ordinary
  # bwt_has_write_candidate early-exit so the shell tokenizer never sees the
  # diff body. See the header's "Second input mode" paragraph.
  BWT_APPLY_PATCH=0
  if bwt_is_apply_patch_payload "$TOOL_COMMAND"; then
    BWT_AP_TARGETS=$(bwt_apply_patch_targets "$TOOL_COMMAND")
    BWT_AP_STATUS=$?
    case "$BWT_AP_STATUS" in
      0)
        BWT_APPLY_PATCH=1
        BWT_TARGETS="$BWT_AP_TARGETS"
        ;;
      9)
        # Not the strict shape (shell-composed) -- fall through unchanged to
        # the existing bwt_has_write_candidate/bwt_extract_targets path below.
        BWT_APPLY_PATCH=0
        ;;
      3)
        echo "BLOCKED: check-sensitive-files.sh requires awk (via lib/bash-write-targets.sh) to inspect this apply_patch envelope's write targets, but awk was not found on PATH." >&2
        exit 2
        ;;
      8)
        # #1036 Fix 6: never interpolate the full $TOOL_COMMAND here -- for a
        # recognized apply_patch envelope it IS the diff body, which may
        # itself carry the exact secret material this hook exists to refuse
        # (e.g. .env contents, an API key), and echoing it into a BLOCKED
        # stderr message would surface it into hook transcripts/logs,
        # defeating the whole point of the block. Name only the first line
        # (the sentinel/wrapper line, never body content) via
        # bwt_ap_first_line.
        BWT_AP_FIRST_LINE=$(bwt_ap_first_line "$TOOL_COMMAND")
        {
          echo "BLOCKED: check-sensitive-files.sh recognized this Bash command as an apply_patch envelope (starting: $BWT_AP_FIRST_LINE), but it does not parse."
          echo "Only two shapes are accepted: bare '*** Begin Patch' ... '*** End Patch' text, or that same text wrapped in an apply_patch <<'DELIM' / apply_patch <<-\"DELIM\" heredoc with a quoted delimiter."
        } >&2
        exit 2
        ;;
      *)
        echo "BLOCKED: check-sensitive-files.sh could not classify this Bash command's apply_patch envelope status (unexpected internal state)." >&2
        exit 2
        ;;
    esac
  fi

  if [ "$BWT_APPLY_PATCH" -eq 0 ]; then
    bwt_has_write_candidate "$TOOL_COMMAND" || exit 0

    # bwt_extract_targets fails closed (non-zero, no output) when awk is
    # missing from PATH, when the command is too long to safely inspect (#795
    # round 2, Fix D), or when wc is missing from PATH (#795 round 3, needed by
    # the length pre-check) -- an empty result in any of these cases must never
    # be treated as "no write targets found" (which would silently allow the
    # command). These failure modes get distinct exit codes (3, 4, 5, 6, 7) from
    # bwt_extract_targets so the message here is accurate rather than
    # misreporting one failure as another.
    BWT_TARGETS=$(bwt_extract_targets "$TOOL_COMMAND")
    BWT_EXTRACT_STATUS=$?
    if [ "$BWT_EXTRACT_STATUS" -ne 0 ]; then
      if [ "$BWT_EXTRACT_STATUS" -eq 4 ]; then
        echo "BLOCKED: check-sensitive-files.sh: command too long to inspect for write targets (bwt_extract_targets)." >&2
      elif [ "$BWT_EXTRACT_STATUS" -eq 5 ]; then
        echo "BLOCKED: check-sensitive-files.sh requires wc (via lib/bash-write-targets.sh) to inspect this Bash command's write targets, but wc was not found on PATH." >&2
      elif [ "$BWT_EXTRACT_STATUS" -eq 6 ]; then
        # #810 Fix 1: an unquoted brace expansion ({a,b}, {1..3}) in
        # write-target position is not supported for direct extraction --
        # handing back the raw un-expanded literal would misreport what bash
        # actually writes to. Unconditional: this blocks regardless of whether
        # the raw command also happens to mention a sensitive marker.
        echo "BLOCKED: check-sensitive-files.sh could not inspect this Bash command's write targets: it contains an unquoted brace expansion (e.g. {a,b} or {1..3}) in write-target position, which is not supported for direct extraction: $TOOL_COMMAND" >&2
        echo "Rewrite the command without brace expansion (e.g. one command per target) or edit the file manually if needed." >&2
      elif [ "$BWT_EXTRACT_STATUS" -eq 7 ]; then
        # #810 stabilization: a command/process/function substitution appears
        # inside a double-quoted string within a comparison construct
        # (`[[ ]]`/`(( ))`), which cannot be safely resolved -- this tokenizer
        # has no safe way to resume precise parsing through the real syntax of
        # such a substitution once it is wrapped in an outer double-quoted
        # string, so the whole command fails closed unconditionally, regardless
        # of whether the raw command also happens to mention a sensitive
        # marker.
        echo "BLOCKED: check-sensitive-files.sh could not inspect this Bash command's write targets: a command/process/function substitution appears inside a double-quoted string within a comparison construct, which cannot be safely resolved: $TOOL_COMMAND" >&2
        echo "Rewrite the command without a double-quoted nested substitution inside [[ ]] / (( )) (e.g. hoist it to its own statement first) or edit the file manually if needed." >&2
      else
        echo "BLOCKED: check-sensitive-files.sh requires awk (via lib/bash-write-targets.sh) to inspect this Bash command's write targets, but awk was not found on PATH." >&2
      fi
      exit 2
    fi
  fi
  # Empty-parse backstop (#795): zero extracted targets is byte-identical
  # for "this command genuinely writes nothing" and "the tokenizer met a
  # construct it does not model" (brace expansion, heredoc bodies, arbitrary
  # metacharacter gluing). When the command still looks like it might write
  # (bwt_zero_parse_suspicious: any `>`, or a delimited `tee` token), scan
  # the RAW command text for this guard's sensitive markers and block on a
  # hit — an unmodelled construct costs a false positive, never a silent
  # permit. Without this, `{tee,cat} /repo/.env` extracted nothing and was
  # silently allowed (#795 review rounds 1-5, the non-converging root cause).
  #
  # Gated on BWT_APPLY_PATCH=0 (#1036): once a bare apply_patch envelope was
  # successfully recognized and parsed, BWT_TARGETS already holds its real
  # declared targets (never empty on that success path), so this backstop
  # must never re-scan the diff BODY text for sensitive-marker-shaped noise
  # that is not a real write.
  if [ "$BWT_APPLY_PATCH" -eq 0 ] && [ -z "$BWT_TARGETS" ]; then
    if bwt_zero_parse_suspicious "$TOOL_COMMAND"; then
      check_sensitive_raw "$TOOL_COMMAND"
    fi
    exit 0
  fi

  # Relative-target cwd resolution (root AGENTS.md, #1036 review Fix 5):
  # computed once, unconditionally, used by every relative BWT_TARGET below
  # (not just apply_patch ones -- this is the same join point regardless of
  # source). Checked explicitly -- never unchecked command substitution on
  # this security-critical path; an unchecked `$(pwd)` failure would
  # silently collapse to a root-relative path and undermine hardening.
  # Mirrors guard-main-worktree.sh's BWT_CWD pattern (introduced for the
  # same apply_patch relative-target join in this same ticket).
  if ! BWT_CWD=$(pwd) || [ -z "$BWT_CWD" ]; then
    echo "BLOCKED: check-sensitive-files.sh could not determine its own working directory to resolve a relative Bash write target." >&2
    exit 2
  fi

  while IFS= read -r BWT_TARGET; do
    [ -z "$BWT_TARGET" ] && continue

    bwt_is_exempt_device "$BWT_TARGET" && continue

    # #1036: an apply_patch declared path is the literal, verbatim payload
    # text -- under a quoted heredoc delimiter (the only form recognized) the
    # body is never shell-expanded, so expanding $HOME/$TMPDIR/etc. here
    # would misreport what the tool actually writes to.
    if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
      BWT_EXPANDED="$BWT_TARGET"
    else
      BWT_EXPANDED=$(bwt_expand_safe_vars "$BWT_TARGET")
    fi

    if bwt_is_unresolved "$BWT_EXPANDED"; then
      # #1036 second review, Fix A: this per-target loop runs for BOTH the
      # apply_patch and ordinary-tokenizer sources of BWT_TARGETS, but only
      # the tokenizer's $TOOL_COMMAND is an ordinary shell command safe to
      # echo in full -- for a recognized apply_patch envelope (BWT_APPLY_PATCH
      # =1) $TOOL_COMMAND IS the diff body and may itself carry the exact
      # secret material (e.g. a .env file's contents) this hook exists to
      # refuse (the same leak vector Fix 6 closed for the exit-8 parse-failure
      # message, but reachable here too: an ordinary declared filename
      # containing '$', a backtick, or '(' -- e.g. `reports/summary (1).csv`
      # -- reaches this branch on the SUCCESS path, no malformed envelope
      # required). Name only the first line via bwt_ap_first_line in that
      # case; the ordinary tokenizer path (BWT_APPLY_PATCH=0) is unaffected
      # and keeps the full $TOOL_COMMAND, which was never the leak vector and
      # remains useful context there.
      if [ "$BWT_APPLY_PATCH" -eq 1 ]; then
        BWT_MSG_CMD=$(bwt_ap_first_line "$TOOL_COMMAND")
      else
        BWT_MSG_CMD="$TOOL_COMMAND"
      fi
      {
        echo "BLOCKED: check-sensitive-files.sh cannot resolve the Bash write target '$BWT_EXPANDED' in: $BWT_MSG_CMD"
        echo "Use a literal absolute path, or one of \${TMPDIR:-/tmp}, \$TMPDIR, \$HOME, \$PWD."
      } >&2
      exit 2
    fi

    case "$BWT_EXPANDED" in
      /*) BWT_ABS="$BWT_EXPANDED" ;;
      *) BWT_ABS="$BWT_CWD/$BWT_EXPANDED" ;;
    esac

    BWT_COLLAPSED=$(lexical_collapse "$BWT_ABS")

    # Symlink resolution (#795 final round): the two lexical forms above
    # never touch the filesystem, so `>> notes.txt` where notes.txt symlinks
    # to .env matched nothing. Canonicalize via the same parent-anchored
    # canonicalize_path the Write|Edit arm uses, and check the union of all
    # three forms (raw + collapsed + canonical) — same union rationale as
    # the header comment. Fails closed on an unresolvable component.
    if ! BWT_CANON=$(canonicalize_path "$BWT_ABS"); then
      echo "BLOCKED: check-sensitive-files.sh refusing to resolve Bash write target $BWT_ABS: the path could not be canonicalized (a component is a symlink that does not resolve — dangling target or symlink loop — or the resolver failed)." >&2
      exit 2
    fi

    check_sensitive "$BWT_ABS"
    check_sensitive "$BWT_COLLAPSED"
    check_sensitive "$BWT_CANON"
  done <<BWT_TARGETS_EOF
$BWT_TARGETS
BWT_TARGETS_EOF

  exit 0
else
  exit 0
fi
