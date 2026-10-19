#!/usr/bin/env bash
# doc-conventions.sh -- read-only discovery of a repository's documentation
# conventions (#1302). Prints paths and classification lines only; no file body
# is ever read into the output.
#
#   doc-conventions.sh discover <repo-root>
#   doc-conventions.sh mentions <repo-root> <term>
#
# discover prints, in this order (each group sorted with LC_ALL=C):
#   agents: <path>    each AGENTS.md; a CLAUDE.md is listed only when no
#                     AGENTS.md sits beside it (a legacy router)
#   index: <path>     a docs/index.md or docs/README.md
#   owner: <path>     every other Markdown file under a docs/ directory at any
#                     depth, except the lesson trees (docs/lessons/**,
#                     docs/learnings/**)
#   lessons: ...      exactly one line: `structured admission=<path> records=<dir>`
#                     when a docs/lessons (or docs/learnings) directory holds a
#                     README.md or index.md admission doc plus at least one other
#                     Markdown record, otherwise `fallback`
#   skipped: <path> (<reason>)
#                     a symlink that was not used: outside repository, dangling
#                     symlink, symlink loop, not a regular file
#   convention: none  only when nothing above was found, followed by
#                     recommend:/evidence: pairs
# Classification is by PATH only. Files are never opened by `discover`, and the
# links inside an index are never parsed.
#
# mentions prints every Markdown file (one repo-relative path per line, sorted)
# that contains <term> as a fixed string. Symlinks resolving outside the
# repository are never read.
#
# Walk: `find -P` from the canonical root, pruning .git, .worktrees,
# node_modules and .plans. Symlinks are resolved with a bounded readlink loop,
# never descended into, and used only when their final target stays inside the
# repository root. Names containing control characters are ignored.
#
# A symlink whose target resolves into a pruned directory (.git, .worktrees,
# node_modules, .plans, at any depth) is skipped as `pruned target`.
#
# Exit codes: 0 on success (including "no match" for mentions); 2 with exactly
# one stderr line and nothing on stdout on a usage error or any failed external
# command (stdout is buffered in the work directory and printed only after the
# whole run succeeded). The script never writes inside the repository, and uses
# no bash-reserved variable names.

export LC_ALL=C
# CDPATH would make `cd` search other directories and print the path it chose.
unset CDPATH

die() {
  echo "doc-conventions.sh: $1" >&2
  exit 2
}

if [ "$#" -lt 1 ]; then
  die "usage: doc-conventions.sh discover <repo-root> | mentions <repo-root> <term>"
fi

SUBCOMMAND="$1"
TERM_ARG=""
case "${SUBCOMMAND}" in
  discover)
    [ "$#" -eq 2 ] || die "usage: doc-conventions.sh discover <repo-root>"
    ;;
  mentions)
    [ "$#" -eq 3 ] || die "usage: doc-conventions.sh mentions <repo-root> <term>"
    TERM_ARG="$3"
    [ -n "${TERM_ARG}" ] || die "usage: mentions requires a non-empty term"
    case "${TERM_ARG}" in
      *"
"*) die "usage: mentions requires a single-line term" ;;
    esac
    ;;
  *)
    die "usage: unknown subcommand (expected discover or mentions)"
    ;;
esac

REPO_ARG="$2"
[ -n "${REPO_ARG}" ] || die "usage: repo root must not be empty"
[ -d "${REPO_ARG}" ] || die "repo root is not a directory"
cd -P -- "${REPO_ARG}" 2>/dev/null || die "cannot enter repo root"
ROOT="$(pwd -P)" || die "cannot resolve repo root"
[ -n "${ROOT}" ] || die "cannot resolve repo root"
# Every path below the root starts with ROOT_PREFIX (root "/" yields "/").
ROOT_PREFIX="${ROOT%/}/"

# The work directory is created after `cd` into the repository, so a relative
# TMPDIR would land inside it: only an absolute TMPDIR is honoured.
case "${TMPDIR:-}" in
  /*) TMP_BASE="${TMPDIR%/}" ;;
  *) TMP_BASE="/tmp" ;;
esac
WORK="$(mktemp -d "${TMP_BASE}/cenci-doc-conventions.XXXXXX" 2>/dev/null)" || die "cannot create a temporary directory"
[ -n "${WORK}" ] && [ -d "${WORK}" ] || die "cannot create a temporary directory"
cleanup() {
  rm -rf -- "${WORK}"
}
trap cleanup EXIT
trap 'exit 2' HUP INT TERM

OUTF="${WORK}/stdout"
FOUND="${WORK}/found"
KEPT="${WORK}/kept"
SKIPPED="${WORK}/skipped"

# new_list <file> -- create or empty a temporary list file.
new_list() {
  : > "$1" || die "cannot write a temporary file"
}

# append_line <file> <line> -- append one line to a temporary list file.
append_line() {
  printf '%s\n' "$2" >> "$1" || die "cannot write a temporary file"
}

# add_kept <rel> / add_skipped <rel> <reason> -- record a usable or skipped path.
add_kept() {
  append_line "${KEPT}" "$1"
}
add_skipped() {
  append_line "${SKIPPED}" "skipped: $1 ($2)"
}

# kept_has <exact line> -- true when KEPT lists it; dies when grep itself fails.
kept_has() {
  grep -qxF -e "$1" "${KEPT}" 2>/dev/null && return 0
  [ "$?" -eq 1 ] || die "failed to scan the Markdown list"
  return 1
}

# is_pruned_path <repo-relative path> -- true when the path is, or lies below, a
# directory the walk prunes (.git, .worktrees, node_modules, .plans) at any depth.
is_pruned_path() {
  case "/$1/" in
    */.git/* | */.worktrees/* | */node_modules/* | */.plans/*) return 0 ;;
    *) return 1 ;;
  esac
}

# is_contained <absolute directory> -- true when the directory is the repo root
# or lies below it. "$dir/" must match "$ROOT_PREFIX"*, which accepts equality
# and rejects a sibling that merely shares the prefix (repo-evil vs repo).
is_contained() {
  case "$1/" in
    "${ROOT_PREFIX}"*) return 0 ;;
    *) return 1 ;;
  esac
}

# resolve_link <absolute path of a symlink>
# Follows the link chain with a bounded readlink loop (no GNU `readlink -f`),
# canonicalising the parent directory after every hop. Sets:
#   RES_STATE  ok | dangling | loop
#   RES_PATH   the final, canonical target (when ok)
RES_STATE=""
RES_PATH=""
resolve_link() {
  local cur="$1" hops=0 target next next_dir next_base next_dir_real
  RES_STATE="ok"
  RES_PATH=""
  while [ -L "${cur}" ]; do
    hops=$((hops + 1))
    if [ "${hops}" -gt 40 ]; then
      RES_STATE="loop"
      return 0
    fi
    target="$(readlink "${cur}")" || { RES_STATE="dangling"; return 0; }
    case "${target}" in
      /*) next="${target}" ;;
      *) next="${cur%/*}/${target}" ;;
    esac
    if [ -d "${next}" ]; then
      # A directory (or a link chain ending in one): cd -P resolves all of it.
      cur="$(cd -P -- "${next}" 2>/dev/null && pwd -P)" || { RES_STATE="dangling"; return 0; }
      break
    fi
    next_dir="${next%/*}"
    next_base="${next##*/}"
    [ -n "${next_dir}" ] || next_dir="/"
    if [ -z "${next_base}" ]; then
      RES_STATE="dangling"
      return 0
    fi
    next_dir_real="$(cd -P -- "${next_dir}" 2>/dev/null && pwd -P)" || { RES_STATE="dangling"; return 0; }
    cur="${next_dir_real%/}/${next_base}"
  done
  if [ -L "${cur}" ]; then
    RES_STATE="loop"
    return 0
  fi
  if [ ! -e "${cur}" ]; then
    RES_STATE="dangling"
    return 0
  fi
  RES_PATH="${cur}"
}

# enumerate -- fills KEPT (repo-relative Markdown paths that may be used) and
# SKIPPED (`skipped:` lines). Enumeration goes through an exit-checked temp file
# rather than process substitution so a find failure is observable.
enumerate() {
  new_list "${KEPT}"
  new_list "${SKIPPED}"
  find -P . \( -name .git -o -name .worktrees -o -name node_modules -o -name .plans \) -prune \
    -o \( \( -type f -name '*.md' \) -o -type l \) -print0 > "${FOUND}" 2>/dev/null \
    || die "failed to enumerate the repository"

  local entry rel is_md final_dir
  while IFS= read -r -d '' entry; do
    rel="${entry#./}"
    case "${rel}" in
      *[[:cntrl:]]*) continue ;;
    esac
    is_md=0
    case "${rel}" in
      *.md) is_md=1 ;;
    esac
    if [ ! -L "${rel}" ]; then
      add_kept "${rel}"
      continue
    fi
    resolve_link "${ROOT_PREFIX}${rel}"
    case "${RES_STATE}" in
      dangling)
        [ "${is_md}" -eq 1 ] && add_skipped "${rel}" "dangling symlink"
        continue
        ;;
      loop)
        [ "${is_md}" -eq 1 ] && add_skipped "${rel}" "symlink loop"
        continue
        ;;
    esac
    if [ -d "${RES_PATH}" ]; then
      # Never descended; only reported when it leaves the repository.
      if ! is_contained "${RES_PATH}"; then
        add_skipped "${rel}" "outside repository"
      fi
      continue
    fi
    [ "${is_md}" -eq 1 ] || continue
    final_dir="${RES_PATH%/*}"
    [ -n "${final_dir}" ] || final_dir="/"
    if ! is_contained "${final_dir}"; then
      add_skipped "${rel}" "outside repository"
    elif is_pruned_path "${RES_PATH#"${ROOT_PREFIX}"}"; then
      add_skipped "${rel}" "pruned target"
    elif [ -f "${RES_PATH}" ]; then
      add_kept "${rel}"
    else
      add_skipped "${rel}" "not a regular file"
    fi
  done < "${FOUND}"
  return 0
}

# print_sorted <file> -- the file's lines, sorted and de-duplicated.
print_sorted() {
  sort -u "$1" > "$1.sorted" 2>/dev/null || die "failed to sort results"
  cat "$1.sorted" 2>/dev/null || die "failed to print results"
}

# lesson_dir_of <repo-relative path> -- prints the docs/lessons/ or
# docs/learnings/ directory (with trailing slash) the path lives under, if any.
lesson_dir_of() {
  case "$1" in
    docs/lessons/*) printf 'docs/lessons/\n' ;;
    docs/learnings/*) printf 'docs/learnings/\n' ;;
    */docs/lessons/*) printf '%s/docs/lessons/\n' "${1%%/docs/lessons/*}" ;;
    */docs/learnings/*) printf '%s/docs/learnings/\n' "${1%%/docs/learnings/*}" ;;
  esac
}

discover() {
  enumerate
  local AGENTS_F="${WORK}/agents" INDEX_F="${WORK}/index" OWNER_F="${WORK}/owner" LDIRS="${WORK}/ldirs"
  new_list "${AGENTS_F}"
  new_list "${INDEX_F}"
  new_list "${OWNER_F}"
  new_list "${LDIRS}"

  local rel base dir sibling ldir
  while IFS= read -r rel; do
    base="${rel##*/}"
    dir=""
    case "${rel}" in
      */*) dir="${rel%/*}" ;;
    esac
    ldir="$(lesson_dir_of "${rel}")" || die "failed to classify a path"
    if [ -n "${ldir}" ]; then
      append_line "${LDIRS}" "${ldir}"
    fi
    case "${base}" in
      AGENTS.md)
        append_line "${AGENTS_F}" "agents: ${rel}"
        continue
        ;;
      CLAUDE.md)
        sibling="AGENTS.md"
        [ -z "${dir}" ] || sibling="${dir}/AGENTS.md"
        # Hidden only when the sibling AGENTS.md was actually kept: a dangling or
        # outside-repository AGENTS.md symlink is skipped, so it routes nothing.
        if ! kept_has "${sibling}"; then
          append_line "${AGENTS_F}" "agents: ${rel}"
        fi
        continue
        ;;
    esac
    case "/${rel}" in
      */docs/*) ;;
      *) continue ;;
    esac
    [ -z "${ldir}" ] || continue
    case "${base}" in
      index.md | README.md)
        case "/${dir}" in
          */docs)
            append_line "${INDEX_F}" "index: ${rel}"
            continue
            ;;
        esac
        ;;
    esac
    append_line "${OWNER_F}" "owner: ${rel}"
  done < "${KEPT}"

  print_sorted "${AGENTS_F}"
  print_sorted "${INDEX_F}"
  print_sorted "${OWNER_F}"

  # Exactly one lessons: line; the first (sorted) structured lesson tree wins.
  local lessons_line="lessons: fallback" candidate admission records
  sort -u "${LDIRS}" > "${LDIRS}.sorted" 2>/dev/null || die "failed to sort results"
  while IFS= read -r candidate; do
    admission=""
    if kept_has "${candidate}README.md"; then
      admission="${candidate}README.md"
    elif kept_has "${candidate}index.md"; then
      admission="${candidate}index.md"
    fi
    [ -n "${admission}" ] || continue
    records=0
    while IFS= read -r rel; do
      case "${rel}" in
        "${candidate}"*)
          case "${rel#"${candidate}"}" in
            README.md | index.md) ;;
            *) records=1 ;;
          esac
          ;;
      esac
    done < "${KEPT}"
    if [ "${records}" -eq 1 ]; then
      lessons_line="lessons: structured admission=${admission} records=${candidate}"
      break
    fi
  done < "${LDIRS}.sorted"
  printf '%s\n' "${lessons_line}"

  print_sorted "${SKIPPED}"

  if [ ! -s "${AGENTS_F}" ] && [ ! -s "${INDEX_F}" ] && [ ! -s "${OWNER_F}" ] \
    && [ "${lessons_line}" = "lessons: fallback" ]; then
    # Skipped entries mean something may exist that could not be used, so the
    # evidence must not claim the repository has no router or owner docs at all.
    local usable="" note=""
    if [ -s "${SKIPPED}" ]; then
      usable="usable "
      note=" (skipped: entries exist and were not used)"
    fi
    printf 'convention: none\n'
    printf 'recommend: root AGENTS.md as a short router that points to focused owner docs\n'
    printf 'evidence: no %sAGENTS.md router was found in the repository%s\n' "${usable}" "${note}"
    printf 'recommend: docs/<topic>.md focused owner docs for contracts, catalogs, setup and procedures\n'
    printf 'evidence: no %sMarkdown owner docs were found under a docs/ directory%s\n' "${usable}" "${note}"
  fi
}

mentions() {
  enumerate
  local rel rc
  sort -u "${KEPT}" > "${KEPT}.sorted" 2>/dev/null || die "failed to sort results"
  while IFS= read -r rel; do
    grep -qF -e "${TERM_ARG}" -- "${rel}" 2>/dev/null
    rc=$?
    case "${rc}" in
      0) printf '%s\n' "${rel}" ;;
      1) ;;
      *) die "failed to search a Markdown file" ;;
    esac
  done < "${KEPT}.sorted"
}

# stdout is buffered in the work directory and printed only once the whole run
# succeeded, so an exit 2 never leaves partial output.
case "${SUBCOMMAND}" in
  discover) discover > "${OUTF}" ;;
  mentions) mentions > "${OUTF}" ;;
esac
cat -- "${OUTF}" 2>/dev/null || die "failed to print results"
exit 0
