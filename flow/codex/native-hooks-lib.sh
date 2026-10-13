#!/usr/bin/env bash
# native-hooks-lib.sh -- pure helpers for the opt-in native Codex runner
# (native-hooks.test.sh, #1212). Sourced, never executed. Pinned by
# native-hooks.gate.test.sh, which runs in CI without a Codex binary.
#
#   cnh_parse_version <text>
#       stdout: X.Y.Z extracted from `codex --version` output ("codex-cli
#       0.144.1" or a bare "0.144.1"). Non-zero and empty stdout when no
#       X.Y.Z is present.
#   cnh_version_ge <X.Y.Z> <min X.Y.Z>
#       exit 0 when the first is >= the second (numeric per component, not
#       lexicographic); non-zero when lower OR when either is not X.Y.Z
#       (fail closed: an unreadable version is "not run", never "supported").
#   cnh_sanitize <home> <user> <host> <fixture-root>
#       stdin -> stdout. Replaces, literally (never as a regex): <home> ->
#       <HOME>, <user> -> <USER>, <host> -> <HOST>, <fixture-root> ->
#       <FIXTURE>. Home and fixture are replaced wherever they occur; user and
#       host only as whole tokens -- bounded by a string edge or ANY character
#       other than [A-Za-z0-9] (so `box.lan`, `dev`, [dev] and `dev.` are
#       redacted; `-` and `_` count as edges) -- so user `root` never turns
#       `rootless` into `<USER>less`. The user match is case-sensitive; the
#       host match is case-insensitive and covers both the full name and its
#       short form (the part before the first dot). Any UUID (session/thread
#       ids) -> <UUID>; token-shaped
#       strings (sk-..., ghp_/gho_/github_pat_..., xox?-..., AKIA..., JWTs,
#       "Bearer <value>") -> <TOKEN>. An EMPTY replacement source is skipped
#       (an empty home must never redact the whole stream). Git commit SHAs
#       are NOT redacted (the evidence record needs the plugin commit).
#       Idempotent.
#   cnh_classify_verdict <expect: deny|allow> <attempted: yes|no> \
#                        <target: written|absent> <hook-block: yes|no>
#       stdout: one of denied | allowed | inconclusive | hooks-inactive
#   cnh_is_sandbox [marker-file...]
#       exit 0 only when CENCI_SANDBOX is exactly "1" AND at least one marker
#       is a regular file. With no arguments the markers are the hard-coded
#       CNH_DEFAULT_CONTAINER_MARKERS below. There is deliberately NO
#       environment override: a host run must never be made to look like a
#       sandbox run (that adds --dangerously-bypass-hook-trust and
#       --dangerously-bypass-approvals-and-sandbox) by anything in its
#       environment. Tests pass explicit marker arguments, or run a copy of the
#       runner whose constant names their own marker file.
#   cnh_trust_bypass_flag [marker-file...]
#       stdout: --dangerously-bypass-hook-trust when cnh_is_sandbox holds,
#       otherwise nothing. Always exit 0.

CNH_DEFAULT_CONTAINER_MARKERS="/.dockerenv /run/.containerenv"

cnh_parse_version() {
    local re='([0-9]+\.[0-9]+\.[0-9]+)'
    if [[ "$1" =~ ${re} ]]; then
        printf '%s\n' "${BASH_REMATCH[1]}"
        return 0
    fi
    return 1
}

cnh_version_ge() {
    local re='^([0-9]+)\.([0-9]+)\.([0-9]+)$'
    local -a have want
    [[ "$1" =~ ${re} ]] || return 1
    have=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}")
    [[ "$2" =~ ${re} ]] || return 1
    want=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}")
    local i
    for i in 0 1 2; do
        if ((10#${have[i]} > 10#${want[i]})); then
            return 0
        elif ((10#${have[i]} < 10#${want[i]})); then
            return 1
        fi
    done
    return 0
}

# cnh__is_token_edge <char> -- 0 when <char> (the single character next to a
# candidate user/host name, or empty at a string edge) delimits a whole token:
# anything that is not an ASCII letter or digit. Non-ASCII characters count as
# edges, which can only over-redact, never leak.
cnh__is_token_edge() {
    local LC_ALL=C
    [[ -z "$1" || "$1" != [A-Za-z0-9] ]]
}

# cnh__glob_for <needle> <nocase: 0|1> -- echo a glob pattern matching <needle>
# literally; with nocase=1 every ASCII letter matches either case. Every other
# character is backslash-escaped, so no needle character acts as a wildcard.
cnh__glob_for() {
    local needle="$1" nocase="$2" i c rest other out=""
    local lower=abcdefghijklmnopqrstuvwxyz upper=ABCDEFGHIJKLMNOPQRSTUVWXYZ
    for ((i = 0; i < ${#needle}; i++)); do
        c="${needle:i:1}"
        other=""
        if [[ "${nocase}" == 1 ]]; then
            case "${c}" in
                [a-z]) rest="${lower%%"${c}"*}"; other="${upper:${#rest}:1}" ;;
                [A-Z]) rest="${upper%%"${c}"*}"; other="${lower:${#rest}:1}" ;;
            esac
        fi
        if [[ -n "${other}" ]]; then
            out="${out}[${c}${other}]"
        else
            out="${out}\\${c}"
        fi
    done
    printf '%s' "${out}"
}

# cnh__redact_token <text> <needle> <replacement> [nocase: 0|1] -- replace
# <needle>, matched literally, only where both neighbours are token edges (so
# user `root` never turns `rootless` into `<USER>less`). Echoes the result
# without a newline.
cnh__redact_token() {
    local text="$1" needle="$2" repl="$3" nocase="${4:-0}" out="" before after joined prev next pat
    pat="$(cnh__glob_for "${needle}" "${nocase}")" || return 1
    while [[ "${text}" == *${pat}* ]]; do
        before="${text%%${pat}*}"
        after="${text#*${pat}}"
        joined="${out}${before}"
        prev="${joined: -1}"
        next="${after:0:1}"
        out="${joined}"
        if cnh__is_token_edge "${prev}" && cnh__is_token_edge "${next}"; then
            out="${out}${repl}"
        else
            out="${out}${text:${#before}:${#needle}}"
        fi
        text="${after}"
    done
    printf '%s' "${out}${text}"
}

cnh_sanitize() {
    local home="$1" user="$2" host="$3" fixture="$4" text short
    # The trailing sentinel keeps the stream's trailing newlines through the
    # command substitution.
    text="$(cat; printf x)" || return 1
    text="${text%x}"
    # Longest-context sources first (the fixture root and home are usually
    # prefixes of paths that also contain the user name); each is literal.
    # Home and fixture are long path literals, replaced wherever they occur;
    # user and host are short words and are replaced only as whole tokens.
    [[ -n "${fixture}" ]] && text="${text//"${fixture}"/<FIXTURE>}"
    [[ -n "${home}" ]] && text="${text//"${home}"/<HOME>}"
    if [[ -n "${host}" ]]; then
        text="$(cnh__redact_token "${text}" "${host}" "<HOST>" 1; printf x)" || return 1
        text="${text%x}"
        # The short form (`box` for `box.lan`) too, after the full name so the
        # full name is consumed whole first.
        short="${host%%.*}"
        if [[ -n "${short}" && "${short}" != "${host}" ]]; then
            text="$(cnh__redact_token "${text}" "${short}" "<HOST>" 1; printf x)" || return 1
            text="${text%x}"
        fi
    fi
    if [[ -n "${user}" ]]; then
        text="$(cnh__redact_token "${text}" "${user}" "<USER>"; printf x)" || return 1
        text="${text%x}"
    fi
    printf '%s' "${text}" | sed -E \
        -e 's/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/<UUID>/g' \
        -e 's/github_pat_[A-Za-z0-9_]{20,}/<TOKEN>/g' \
        -e 's/gh[pousr]_[A-Za-z0-9]{20,}/<TOKEN>/g' \
        -e 's/(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{16,}/\1<TOKEN>/g' \
        -e 's/xox[abprs]-[A-Za-z0-9-]{10,}/<TOKEN>/g' \
        -e 's/AKIA[0-9A-Z]{16}/<TOKEN>/g' \
        -e 's/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<TOKEN>/g' \
        -e 's/([Bb]earer )[A-Za-z0-9._~+\/=-]+/\1<TOKEN>/g'
}

cnh_classify_verdict() {
    local expect="${1:-}" attempted="${2:-}" target="${3:-}" blocked="${4:-}"
    case "${expect}" in
        deny | allow) ;;
        *)
            echo inconclusive
            return 2
            ;;
    esac
    # Only an attempted call proves anything: an unwritten target with no
    # evidence the tool ran is indistinguishable from a model that never tried.
    case "${attempted}:${target}:${blocked}" in
        yes:absent:yes) echo denied ;;
        yes:written:no)
            if [[ "${expect}" == deny ]]; then echo hooks-inactive; else echo allowed; fi
            ;;
        *) echo inconclusive ;;
    esac
    return 0
}

cnh_is_sandbox() {
    [[ "${CENCI_SANDBOX:-}" == 1 ]] || return 1
    local -a markers
    if [[ "$#" -gt 0 ]]; then
        markers=("$@")
    else
        read -r -a markers <<< "${CNH_DEFAULT_CONTAINER_MARKERS}"
    fi
    local m
    for m in "${markers[@]}"; do
        [[ -f "${m}" ]] && return 0
    done
    return 1
}

cnh_trust_bypass_flag() {
    if cnh_is_sandbox "$@"; then
        printf '%s\n' --dangerously-bypass-hook-trust
    fi
    return 0
}
