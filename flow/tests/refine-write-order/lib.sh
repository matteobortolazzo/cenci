#!/usr/bin/env bash
# lib.sh — extractors + oracles backing flow/tests/refine-write-order.test.sh
# (ticket #878, "make proposal confirmation the first remote mutation").
#
# Sourced by the suite, never executed standalone. Follows the same idiom as
# flow/tests/parity/contract-lib.sh: pure `<name>_raw` extractors (safe
# inside `$(...)`, never call `fail()`) paired with `require_<name>` nameref
# wrappers that DO call `fail()`, but are invoked directly (never through
# `$(...)`) so the failure lands in the sourcing suite's own `failures`
# counter — see docs/shell-scripting-gotchas.md's "never call fail() from
# within $(...)" rule and flow/tests/read-helper-purity-contract.test.sh,
# which scans every *.sh library under flow/tests/ (this file included) for
# the violation.
#
# `drive_*`/`verify_*` naming mirrors contract-lib.sh:590-768 — a `drive_*`
# (here, `replay_through_stub`) executes a real artifact (the recording `gh`
# stub) and reports a small, classified result; a `verify_*` (here,
# `verify_refine_write_order`) is a deterministic oracle with no backing
# script, only a partial-order model derived from the AC.
set -uo pipefail

_REFINE_WRITE_ORDER_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || {
  echo "refine-write-order/lib.sh: failed to resolve its own directory." >&2
  return 1 2>/dev/null || exit 1
}
REFINE_WRITE_ORDER_FIXTURES="${_REFINE_WRITE_ORDER_LIB_DIR}/fixtures"

# ---------------------------------------------------------------------------
# _trim <string> — leading/trailing whitespace stripped. Pure, no fail().
# ---------------------------------------------------------------------------
_trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "${s}"
}

# ---------------------------------------------------------------------------
# _extract_gh_git_lines_from_text <text>
#
# Pure extraction, no fail(). Fence-aware (docs/shell-scripting-gotchas.md's
# "account for code fences" rule, mirroring
# flow/tests/plan-persist-sections-contract.test.sh's extract_persist_section
# toggle idiom): only lines inside a fenced ```...``` code block that begin
# with `gh ` or `git ` (after trimming) are considered real invocations —
# never a prose sentence that merely mentions "gh" or "git" outside a fence.
# Prints one full command line per line of output, in source order.
# ---------------------------------------------------------------------------
_extract_gh_git_lines_from_text() {
  local text="$1"
  awk '
    /^```/ { infence = !infence; next }
    infence {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (line ~ /^(gh|git)[[:space:]]/) print line
    }
  ' <<<"${text}"
}

# ---------------------------------------------------------------------------
# extract_pre_gate_gh_calls_raw <doc-path>
#
# Pure extractor for refine/SKILL.md. Prints every `gh`/`git` invocation
# line (fenced code only) found before the doc's real `## Confirmation
# Gate` heading. Anchored on the literal heading line `^## Confirmation
# Gate$` — SKILL.md forward-references the gate from prose earlier in the
# doc (e.g. "... at the `## Confirmation Gate` below ...", "... above
# ..."), including at least one forward reference whose "below" is soft-
# wrapped onto the next source line, so a phrase-containment scan with a
# same-line "above"/"below" exclusion is not reliable: it can exit ~60
# lines early, at the first forward reference, silently skipping most of
# `## Your Role` and `## Process` from the pre-gate scan. A standalone
# heading line is unambiguous — SKILL.md has exactly one line that is
# literally `## Confirmation Gate` with nothing else on it, and that is
# always the real gate — so this is fail-closed against forward references
# regardless of their wording or line-wrapping.
#
# codex.md has no markdown heading structure and no fenced code blocks at
# all, so this extractor does not apply to it; see
# extract_codex_pre_gate_gh_calls_raw below.
#
# Returns 1 (no output) if the file is unreadable — fail-closed, never a
# vacuous empty-prefix pass on a dangling path.
# ---------------------------------------------------------------------------
extract_pre_gate_gh_calls_raw() {
  local file="$1" content prefix
  content="$(cat "${file}" 2>/dev/null)" || return 1
  prefix="$(awk '
    /^## Confirmation Gate$/ { exit }
    { print }
  ' <<<"${content}")"
  _extract_gh_git_lines_from_text "${prefix}"
}

# require_extract_pre_gate_gh_calls <result-var> <doc-path> <label>
#
# Nameref wrapper. Must NOT be invoked via `$(...)` — its fail() call needs
# the sourcing suite's own shell, not a subshell's throwaway copy.
require_extract_pre_gate_gh_calls() {
  local -n _result="$1"
  local file="$2" label="$3" out
  if out="$(extract_pre_gate_gh_calls_raw "${file}")"; then
    _result="${out}"
    return 0
  fi
  fail "${label}: doc not found/unreadable: ${file}"
  _result=""
  return 1
}

# ---------------------------------------------------------------------------
# _extract_inline_gh_git_mentions_from_text <text>
#
# Pure extraction, no fail(). Recognizes inline single-backtick `gh ...` /
# `git ...` command mentions — codex.md documents every gh/git invocation
# this way (it has zero triple-backtick fenced code blocks), unlike
# SKILL.md's fenced-only style. The text is flattened (newlines -> spaces)
# before matching so a backtick span that happens to be soft-wrapped across
# two source lines (e.g. "`gh api\nuser --jq .login`") is still recognized
# as one mention. Prints one mention per output line, backticks stripped,
# in source order.
# ---------------------------------------------------------------------------
_extract_inline_gh_git_mentions_from_text() {
  local text="$1" flat
  flat="$(printf '%s' "${text}" | tr '\n' ' ')"
  grep -oE '`(gh|git)[^`]*`' <<<"${flat}" | tr -d '`'
}

# ---------------------------------------------------------------------------
# extract_codex_pre_gate_gh_calls_raw <doc-path>
#
# Pure extractor, codex.md-specific sibling of extract_pre_gate_gh_calls_raw
# above. codex.md documents every gh/git invocation as inline single-
# backtick prose with no fenced code blocks at all, so the fenced-only
# extractor always returns nothing on it — silently no-op-ing any caller
# that guards on its output being non-empty. This extractor instead scans
# BOTH fenced lines (defensively, in case a future edit adds one) and
# inline single-backtick `gh ...`/`git ...` mentions, bounded by codex.md's
# own Confirmation Gate marker: the literal bold-text phrase
# "**Confirmation Gate (apply mode, before any GitHub write)**:" — codex.md
# has no markdown heading structure at all, so there is no "## Confirmation
# Gate" line to anchor on there, unlike SKILL.md. Matched via a literal
# substring check (awk's index()), never a fuzzy/regex phrase match, so the
# marker can't misfire on an unrelated line that merely mentions
# "Confirmation Gate" in passing.
#
# Returns 1 (no output) if the file is unreadable — fail-closed, never a
# vacuous empty-prefix pass on a dangling path.
# ---------------------------------------------------------------------------
_CODEX_CONFIRMATION_GATE_MARKER='**Confirmation Gate (apply mode, before any GitHub write)**:'
extract_codex_pre_gate_gh_calls_raw() {
  local file="$1" content prefix fenced inline
  content="$(cat "${file}" 2>/dev/null)" || return 1
  prefix="$(awk -v marker="${_CODEX_CONFIRMATION_GATE_MARKER}" '
    index($0, marker) > 0 { exit }
    { print }
  ' <<<"${content}")"
  fenced="$(_extract_gh_git_lines_from_text "${prefix}")"
  inline="$(_extract_inline_gh_git_mentions_from_text "${prefix}")"
  printf '%s\n%s\n' "${fenced}" "${inline}" | sed '/^$/d'
}

# require_extract_codex_pre_gate_gh_calls <result-var> <doc-path> <label>
#
# Nameref wrapper. Must NOT be invoked via `$(...)` — see
# require_extract_pre_gate_gh_calls above.
require_extract_codex_pre_gate_gh_calls() {
  local -n _result="$1"
  local file="$2" label="$3" out
  if out="$(extract_codex_pre_gate_gh_calls_raw "${file}")"; then
    _result="${out}"
    return 0
  fi
  fail "${label}: doc not found/unreadable: ${file}"
  _result=""
  return 1
}

# ---------------------------------------------------------------------------
# extract_write_order_ops_raw <doc-path>
#
# Pure extractor. Looks for a `### Write order` section and, within it,
# every backtick-quoted op token (e.g. `` `claim` ``, `` `child-create:1` ``)
# in source order, one per output line. refine/SKILL.md and codex.md each
# carry this canonical section (#878) documenting the exact op sequence —
# claim -> Working -> parent body -> children+links -> Pass 2/design ->
# Refined/-Working -> ui:visual-check; this extractor is what turns that
# doc-committed section into the op-token stream `verify_refine_write_order`
# validates, so a future doc edit that reorders or drops an op is caught as
# a live regression rather than only at doc-review time.
# ---------------------------------------------------------------------------
extract_write_order_ops_raw() {
  local file="$1" content section
  content="$(cat "${file}" 2>/dev/null)" || return 1
  section="$(awk '
    /^### Write order$/ { on=1; next }
    on && /^```/ { infence = !infence; next }
    on && !infence && /^#/ { exit }
    on { print }
  ' <<<"${content}")"
  [[ -n "${section}" ]] || return 1
  printf '%s\n' "${section}" | grep -oE '`[a-z][a-z-]*(:[A-Za-z0-9]+)?`' | tr -d '`'
}

# require_extract_write_order_ops <result-var> <doc-path> <label>
require_extract_write_order_ops() {
  local -n _result="$1"
  local file="$2" label="$3" out
  if out="$(extract_write_order_ops_raw "${file}")" && [[ -n "${out}" ]]; then
    _result="${out}"
    return 0
  fi
  fail "${label}: no ### Write order section found in ${file} (expected once #878's green phase adds the canonical claim -> Working -> parent body -> children+links -> Pass 2/design -> Refined/-Working -> ui:visual-check order)"
  _result=""
  return 1
}

# ---------------------------------------------------------------------------
# classify_gh_call <command-line>
#
# Classifies a single `gh`/`git` invocation line as "read", "write", or
# "unknown". Fail-closed: any verb this function does not explicitly
# recognize as read-only is "unknown", never silently "read" — callers doing
# a zero-write assertion must treat "unknown" the same as "write".
#
# The `gh api` branch is itself fail-closed by inversion: a call is "read"
# ONLY when it has no method flag (`-X <verb>`/`-XVerb`/`--method <verb>`,
# any case, with or without a space), no field/input flag (`-f`, `-F`,
# `--field`, `--raw-field`, `--input` — gh defaults to POST when any of
# these are present, even with no explicit `-X`), and the endpoint is not
# `graphql` (a `graphql` call's query/mutation intent lives in its `-f
# query=...` payload text, which this function does not parse, so it is
# never assumed read-only). Everything else is "write" — never silently
# "read" just because the specific verb wasn't one of the ones this
# function was written against.
# ---------------------------------------------------------------------------
classify_gh_call() {
  local cmd
  cmd="$(_trim "$1")"
  case "${cmd}" in
    "gh issue view"*) echo read ;;
    "gh issue list"*) echo read ;;
    "gh api"*)
      if printf '%s' "${cmd}" | grep -qE '(^|[[:space:]])api[[:space:]]+graphql([[:space:]]|$)'; then
        echo write
      elif printf '%s' "${cmd}" | grep -qiE "(^|[[:space:]])(-x[[:space:]]*['\"]?[a-z]+|--method[[:space:]=]+['\"]?[a-z]+)"; then
        echo write
      elif printf '%s' "${cmd}" | grep -qE '(^|[[:space:]])(-f|-F|--field|--raw-field|--input)([[:space:]=]|$)'; then
        echo write
      else
        echo read
      fi
      ;;
    "gh issue edit"*) echo write ;;
    "gh issue create"*) echo write ;;
    "gh issue comment"*) echo write ;;
    "gh issue close"*) echo write ;;
    "gh issue reopen"*) echo write ;;
    "gh label create"*) echo write ;;
    "gh label delete"*) echo write ;;
    "git remote get-url"*) echo read ;;
    *) echo unknown ;;
  esac
}

# ---------------------------------------------------------------------------
# classify_drift <old-labels-space-separated> <new-labels-space-separated>
#
# Classifies the label-set diff between two parent snapshots as
# "authorization" | "cosmetic" | "none", per the ticket's authorization-
# sensitive label set (automerge:ok, Browser, ui:visual-check). A drift that
# touches ANY authorization-sensitive label is "authorization" even if it
# also touches purely cosmetic labels (area/priority/team/milestone/Design) —
# authorization-sensitivity always wins, never downgraded.
#
# `old`/`new` are space-joined label strings; the word-splitting loops below
# run with globbing disabled (`set -f`) for the duration of the split, so a
# label containing `*`/`?` can't be glob-expanded against files in the
# caller's cwd (a label containing a literal space would still misparse as
# two tokens — none of this function's current callers pass one, but
# glob-safety is cheap and this is a generic drift-classification utility).
# ---------------------------------------------------------------------------
classify_drift() {
  local old="$1" new="$2" t a
  local -a auth_sensitive=("automerge:ok" "Browser" "ui:visual-check")
  local -A oldset=() newset=()
  local changed=0 auth=0
  local _restore_glob=0
  case $- in *f*) ;; *) _restore_glob=1 ;; esac
  set -f
  for t in ${old}; do oldset["${t}"]=1; done
  for t in ${new}; do newset["${t}"]=1; done
  [[ "${_restore_glob}" -eq 1 ]] && set +f
  for t in "${!oldset[@]}"; do
    if [[ -z "${newset[${t}]:-}" ]]; then
      changed=1
      for a in "${auth_sensitive[@]}"; do [[ "${t}" == "${a}" ]] && auth=1; done
    fi
  done
  for t in "${!newset[@]}"; do
    if [[ -z "${oldset[${t}]:-}" ]]; then
      changed=1
      for a in "${auth_sensitive[@]}"; do [[ "${t}" == "${a}" ]] && auth=1; done
    fi
  done
  if [[ "${auth}" -eq 1 ]]; then
    echo authorization
  elif [[ "${changed}" -eq 1 ]]; then
    echo cosmetic
  else
    echo none
  fi
}

# ---------------------------------------------------------------------------
# verify_refine_write_order <space-separated-op-sequence>
#
# AC-derived partial-order oracle (no backing script). Validates a recorded
# call-sequence, expressed as an abstract op-token stream, against the
# expected write order: claim -> working -> parent-body ->
# (child-create:K immediately followed by its own child-link:K, for every
# child K, all before parent-exec-order) -> parent-exec-order -> refined ->
# visual-check. An empty sequence ("") is vacuously valid — that is the
# shape produced whenever zero writes occur at all (decline, an unreadable
# parent, or a post-confirm ownership conflict).
#
# On success: prints nothing, returns 0. On failure: prints a distinguishable
# one-line reason to stdout and returns 1 — mirrors
# check_synthetic_adapter's "...fail:out of order: ..." reason convention in
# flow/docs/adapter-contract.md.
# ---------------------------------------------------------------------------
verify_refine_write_order() {
  local seq="$1"
  local -a tokens
  local flat="${seq//$'\n'/ }"
  read -r -a tokens <<<"${flat}"
  local n=${#tokens[@]}
  local claim_i=-1 working_i=-1 body_i=-1 exec_i=-1 refined_i=-1 visual_i=-1
  local -A create_pos=() link_pos=()
  local -a child_positions=()
  local i t k

  for ((i = 0; i < n; i++)); do
    t="${tokens[i]}"
    case "${t}" in
      claim)
        [[ "${claim_i}" -ge 0 ]] && { echo "duplicate claim op"; return 1; }
        claim_i=${i}
        ;;
      working)
        [[ "${working_i}" -ge 0 ]] && { echo "duplicate working op"; return 1; }
        working_i=${i}
        ;;
      parent-body)
        [[ "${body_i}" -ge 0 ]] && { echo "duplicate parent-body op"; return 1; }
        body_i=${i}
        ;;
      parent-exec-order)
        [[ "${exec_i}" -ge 0 ]] && { echo "duplicate parent-exec-order op"; return 1; }
        exec_i=${i}
        ;;
      refined)
        [[ "${refined_i}" -ge 0 ]] && { echo "duplicate refined op"; return 1; }
        refined_i=${i}
        ;;
      visual-check)
        [[ "${visual_i}" -ge 0 ]] && { echo "duplicate visual-check op"; return 1; }
        visual_i=${i}
        ;;
      child-create:*)
        k="${t#child-create:}"
        [[ -n "${create_pos[${k}]:-}" ]] && { echo "duplicate child-create:${k} op"; return 1; }
        create_pos["${k}"]=${i}
        child_positions+=("${i}")
        if (( i + 1 >= n )) || [[ "${tokens[i+1]}" != "child-link:${k}" ]]; then
          echo "child-create:${k} not immediately followed by its own child-link:${k}"
          return 1
        fi
        ;;
      child-link:*)
        k="${t#child-link:}"
        [[ -n "${link_pos[${k}]:-}" ]] && { echo "duplicate child-link:${k} op"; return 1; }
        link_pos["${k}"]=${i}
        child_positions+=("${i}")
        ;;
      *)
        echo "unknown op token: ${t}"
        return 1
        ;;
    esac
  done

  if [[ "${claim_i}" -ge 0 && "${working_i}" -ge 0 && "${claim_i}" -ge "${working_i}" ]]; then
    echo "claim must precede working"
    return 1
  fi
  if [[ "${working_i}" -ge 0 && "${body_i}" -ge 0 && "${working_i}" -ge "${body_i}" ]]; then
    echo "working must precede parent-body"
    return 1
  fi

  # The ownership claim is the exclusive-ownership control the whole ticket
  # is about (#878) — a non-empty sequence with `claim` dropped entirely, or
  # present but not the first op, must be rejected, not silently accepted
  # because every pairwise check above only fires when both operands are
  # present.
  if [[ "${n}" -gt 0 && "${claim_i}" -ne 0 ]]; then
    echo "claim must be present and be the first op in a non-empty sequence"
    return 1
  fi

  local cp
  for cp in "${child_positions[@]:-}"; do
    [[ -z "${cp}" ]] && continue
    if [[ "${body_i}" -ge 0 && "${cp}" -le "${body_i}" ]]; then
      echo "child create/link ops must come after parent-body"
      return 1
    fi
    if [[ "${exec_i}" -ge 0 && "${cp}" -ge "${exec_i}" ]]; then
      echo "all child create/link ops must precede parent-exec-order"
      return 1
    fi
  done

  if [[ "${refined_i}" -ge 0 ]]; then
    if [[ "${body_i}" -ge 0 && "${refined_i}" -le "${body_i}" ]]; then
      echo "refined must come after parent-body"
      return 1
    fi
    if [[ "${exec_i}" -ge 0 && "${refined_i}" -le "${exec_i}" ]]; then
      echo "refined must come after parent-exec-order"
      return 1
    fi
    for cp in "${child_positions[@]:-}"; do
      [[ -z "${cp}" ]] && continue
      if [[ "${refined_i}" -le "${cp}" ]]; then
        echo "refined must come after every child create/link op"
        return 1
      fi
    done
  fi

  if [[ "${visual_i}" -ge 0 && "${refined_i}" -ge 0 && "${visual_i}" -le "${refined_i}" ]]; then
    echo "visual-check must come after refined"
    return 1
  fi

  return 0
}

# ---------------------------------------------------------------------------
# _substitute_placeholders <doc-command-line>
#
# Pure. Turns a doc-literal command line's angle-bracket placeholders into
# concrete values so it can actually be executed against the recording gh
# stub. Deliberately narrow: only the placeholders that appear in
# refine/SKILL.md's pre-gate span and the synthetic post-confirm re-fetch
# lines this suite drives.
# ---------------------------------------------------------------------------
_substitute_placeholders() {
  local line="$1"
  line="${line//<number>/100}"
  line="${line//<original-number>/100}"
  line="${line//<child-number>/101}"
  line="${line//<owner>\/<repo>/acme\/widgets}"
  line="${line//<D>/109}"
  line="${line//<token>/tok123}"
  printf '%s' "${line}"
}

# ---------------------------------------------------------------------------
# replay_through_stub <commands-newline-separated> <log-file> [view-fixture-path] [fail-pattern]
#
# `drive_*`-style helper (mirrors contract-lib.sh's drive_baseline_gate /
# drive_worktree_guard): actually EXECUTES a real doc-extracted (or
# suite-authored canonical) command sequence against fixtures/gh.stub.sh —
# copied into a fresh `mktemp -d`/bin/gh and chmod +x'd here at runtime
# (permission-adding only; the committed fixture carries no executable bit)
# — and captures what really happened, rather than only pattern-matching the
# doc text.
#
# <log-file> receives one line per invocation (the stub's "$*", i.e. `gh`'s
# argv without the `gh` token itself). <log-file>.stdout receives each
# invocation's captured stdout, one per replayed line. [view-fixture-path],
# when set, is handed to the stub as GH_STUB_VIEW_FIXTURE so a `gh issue
# view` call in the replayed sequence returns that canned JSON.
# [fail-pattern], when set, is a literal substring (GH_STUB_FAIL_PATTERN) —
# any invocation containing it is made to fail, simulating an unreadable
# fetch.
#
# Each extracted line is tokenized (`read -r -a argv`) and exec'd directly
# as `"${argv[@]}"` — never `bash -c "${line}"`. Only `gh` is a doc-literal
# command today, but `bash -c` on doc-extracted text is a real (if
# currently latent) code-execution surface: `git`, `rm`, `jq`,
# redirections, `&&`/`||` chains, and `$(...)` would all execute for real
# in this process's actual environment/cwd if a future doc edit put them in
# a fenced block within a replayed span. A line containing any shell
# metacharacter (`| ; & < > $ \` ( ) { }`) is refused outright — logged as
# an error to <log-file>.stderr and counted as a failure — rather than
# silently skipped or passed through to a shell. `git` is shimmed into the
# same `mktemp -d` bin alongside `gh` so a legitimately-replayed `git` call
# (tokenizable, no metacharacters) is still sandboxed instead of hitting
# the real repo.
#
# Returns 0 if every replayed line exited 0, 1 if any line failed or was
# refused as unreplayable (the harness still replays every line rather
# than aborting at the first failure, so the log/stdout capture is always
# complete).
# ---------------------------------------------------------------------------
replay_through_stub() {
  local commands="$1" logfile="$2" view_fixture="${3:-}" fail_pattern="${4:-}"
  local binroot line subst out overall=0 rc

  binroot="$(mktemp -d)" || return 1
  if ! cp "${REFINE_WRITE_ORDER_FIXTURES}/gh.stub.sh" "${binroot}/gh"; then
    rm -rf "${binroot}"
    return 1
  fi
  if ! cp "${REFINE_WRITE_ORDER_FIXTURES}/gh.stub.sh" "${binroot}/git"; then
    rm -rf "${binroot}"
    return 1
  fi
  chmod +x "${binroot}/gh" "${binroot}/git" || { rm -rf "${binroot}"; return 1; }

  : > "${logfile}" || { rm -rf "${binroot}"; return 1; }
  : > "${logfile}.stdout" || { rm -rf "${binroot}"; return 1; }
  : > "${logfile}.stderr" || { rm -rf "${binroot}"; return 1; }
  rm -f "${logfile}.counter"

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    subst="$(_substitute_placeholders "${line}")"
    if printf '%s' "${subst}" | grep -qE '[|;&<>$`(){}]'; then
      printf 'replay_through_stub: refusing to replay unreplayable-by-design line (shell metacharacter present): %s\n' "${subst}" >> "${logfile}.stderr"
      overall=1
      continue
    fi
    local -a argv=()
    read -r -a argv <<<"${subst}"
    if [[ "${#argv[@]}" -eq 0 ]]; then
      overall=1
      continue
    fi
    if out="$(PATH="${binroot}:${PATH}" GH_STUB_LOG="${logfile}" GH_STUB_VIEW_FIXTURE="${view_fixture}" GH_STUB_FAIL_PATTERN="${fail_pattern}" "${argv[@]}" 2>>"${logfile}.stderr")"; then
      rc=0
    else
      rc=1
      overall=1
    fi
    printf '%s\n' "${out}" >> "${logfile}.stdout"
    : "${rc}"
  done <<<"${commands}"

  rm -rf "${binroot}"
  return "${overall}"
}
